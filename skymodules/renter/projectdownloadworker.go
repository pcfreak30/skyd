package renter

import (
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/opentracing/opentracing-go"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/types"
)

const (
	// maxWaitUnresolvedWorkerUpdate defines the amount of time we want to wait
	// for unresolved workers to become resolved when trying to create the
	// initial worker set.
	maxWaitUnresolvedWorkerUpdate = 10 * time.Millisecond
)

// NOTE: all of the following defined types are used by the PDC, which is
// inherently thread un-safe, that means that these types don't not need to be
// thread safe either. If fields are marked `static` it is meant to signal they
// won't change after being set.
type (
	// downloadWorker is an interface implemented by both the individual and
	// chimera workers that represents a worker that can be used for downloads.
	downloadWorker interface {
		// cost returns the expected job cost for downloading a piece of data
		// with given length from the worker. If the worker has already been
		// launched, its cost will be zero.
		cost(length uint64) types.Currency

		// getPieceForDownload returns the piece to download next
		getPieceForDownload() uint64

		// identifier returns a unique identifier for the download worker, this
		// identifier can be used as a key when mapping the download worker
		identifier() string

		// markPieceForDownload allows specifying what piece to download for
		// this worker in the case the worker resolved multiple pieces
		markPieceForDownload(pieceIndex uint64)

		// pieces returns all piece indices this worker can resolve
		pieces() []uint64

		// chanceAfter returns the chance this download worker completes a read
		// within the given duration
		//
		// recalculateChanceAfter recalculates the chances that are cached on
		// the download workers because it's computationally intensive to
		// calculate those
		chanceAfter(index int) float64
		recalculateChanceAfter()

		// worker returns the underlying worker
		worker() *worker
	}

	// chimeraWorker is a worker that's built from unresolved workers until the
	// chance it has a piece is exactly 1. At that point we can treat a chimera
	// worker exactly the same as a resolved worker in the download algorithm
	// that constructs the best worker set.
	chimeraWorker struct {
		// cachedCost returns the read cost of the chimera worker, which is the
		// average across all workers this chimera worker is comprised of
		cachedCost types.Currency

		// cachedChancesAfter maps a duration to the chance this worker can
		// download a piece in the timespan defined by that duration, we have to
		// cache this because it's computationally expensive to compute
		cachedChancesAfter [skymodules.DistributionTrackerTotalBuckets]float64

		// cachedDistribution contains a distribution that is the weighted
		// combination of all worker distrubtions in this chimera worker, it is
		// cached meaning it will only be calculated the first time the
		// distribution is requested after the chimera worker was finalized.
		cachedDistribution *skymodules.Distribution

		// remaining keeps track of how much "chance" is remaining until the
		// chimeraworker is comprised of enough to workers to be able to resolve
		// a piece. This is a helper field that avoids calculating
		// 1-SUM(weights) over and over again
		remaining float64

		// distributions contains the read distribution for every worker that is
		// part of the chimera worker
		distributions []*skymodules.Distribution

		// weights contains the weight for every worker, the weight represents
		// the chance the worker will resolve and influences how much each
		// worker's distrubtion weighs through in the merged distribution
		weights []float64

		// workers contains all workers that make up the chimera worker
		workers []*worker

		// staticPieceIndices contains a list of piece indices, for a chimera
		// worker this will be an array containing every piece index
		staticPieceIndices []uint64

		// staticUID uniquely identifies the chimera worker
		staticUID string
	}

	// individualWorker is a struct that represents a single worker object, both
	// resolved and unresolved workers in the pdc can be represented by an
	// individual worker. An individual worker can be used to build a chimera
	// worker with.
	//
	// NOTE: extending this struct requires an update to the `split` method.
	individualWorker struct {
		launchedAt    time.Time
		pieceIndices  []uint64
		resolveChance float64

		// cachedChancesAfter maps a duration to the chance this worker can
		// download a piece in the timespan defined by that duration, we have to
		// cache this because it's computationally expensive to compute
		cachedChancesAfter [skymodules.DistributionTrackerTotalBuckets]float64

		// currentPiece is the piece that was marked by the download algorithm
		// as the piece to download next, this is used to ensure that workers
		// in the worker are not selected for duplicate piece indices.
		currentPiece uint64

		staticExpectedCost        types.Currency
		staticExpectedResolveTime time.Time
		staticIdentifier          string
		staticReadDistribution    *skymodules.Distribution
		staticWorker              *worker
	}

	// workerSet is a collection of workers that may or may not have been
	// launched yet in order to fulfil a download.
	workerSet struct {
		workers []downloadWorker

		staticBucketIndex      int
		staticExpectedDuration time.Duration
		staticLength           uint64
		staticMinPieces        int

		staticPDC *projectDownloadChunk
	}

	// coinflips is a collection of chances where every item is the chance the
	// coin will turn up heads. We use the concept of a coin because it allows
	// to more easily reason about chance calculations.
	coinflips []float64
)

// NewChimeraWorker returns a new chimera worker object.
func NewChimeraWorker(numPieces int) *chimeraWorker {
	pieceIndices := make([]uint64, numPieces)
	for i := 0; i < numPieces; i++ {
		pieceIndices[i] = uint64(i)
	}

	var staticUID [16]byte
	fastrand.Read(staticUID[:])

	ch := &chimeraWorker{
		remaining: 1,

		staticPieceIndices: pieceIndices,
		staticUID:          hex.EncodeToString(staticUID[:]),
	}

	// generate a uid for the chimera that'll serve as a unique identifier
	return ch
}

// addWorker adds the given worker to the chimera worker.
func (cw *chimeraWorker) addWorker(w *individualWorker) *individualWorker {
	// calculate the remaining chance this chimera worker needs to be complete
	if cw.remaining == 0 {
		return w
	}

	// the given worker's chance can be higher than the remaining chance of this
	// chimera worker, in that case we have to split the worker in a part we
	// want to add, and a remainder we'll use to build the next chimera with
	toAdd := w
	var remainder *individualWorker
	if w.resolveChance > cw.remaining {
		toAdd, remainder = w.split(cw.remaining)
	}

	// update the remaining chance
	cw.remaining -= toAdd.resolveChance

	// add the worker to the chimera
	cw.distributions = append(cw.distributions, toAdd.staticReadDistribution)
	cw.weights = append(cw.weights, toAdd.resolveChance)
	cw.workers = append(cw.workers, toAdd.staticWorker)
	return remainder
}

// cost implements the downloadWorker interface.
func (cw *chimeraWorker) cost(length uint64) types.Currency {
	if cw.remaining != 0 {
		build.Critical("developer error, chimera is not complete")
		return types.ZeroCurrency
	}

	if cw.cachedCost.IsZero() {
		var total types.Currency
		for _, w := range cw.workers {
			jrq := w.staticJobReadQueue
			total = total.Add(jrq.callExpectedJobCost(length))
		}
		cw.cachedCost = total.Div64(uint64(len(cw.workers)))
	}

	return cw.cachedCost
}

// chanceAfter returns the chance this worker completes a read after the given
// duration.
func (cw *chimeraWorker) chanceAfter(index int) float64 {
	return cw.cachedChancesAfter[index]
}

// distribution implements the downloadWorker interface.
func (cw *chimeraWorker) distribution() *skymodules.Distribution {
	if cw.remaining != 0 {
		build.Critical("developer error, chimera is not complete")
		return nil
	}

	if cw.cachedDistribution == nil && len(cw.distributions) > 0 {
		halfLife := cw.distributions[0].HalfLife()
		cw.cachedDistribution = skymodules.NewDistribution(halfLife)
		for i, distribution := range cw.distributions {
			cw.cachedDistribution.MergeWith(distribution, cw.weights[i])
		}
	}
	return cw.cachedDistribution
}

// getPieceForDownload returns the piece to download next, for a chimera worker
// this is always 0 and should never be called, which is why we add a
// build.Critical to signal developer error.
func (cw *chimeraWorker) getPieceForDownload() uint64 {
	build.Critical("developer error, should not get called on a chimera worker")
	return 0
}

// identifier returns a unqiue identifier for this worker.
func (cw *chimeraWorker) identifier() string {
	return cw.staticUID
}

// markPieceForDownload takes a piece index and marks it as the piece to
// download for this worker. In the case of a chimera worker this method is
// essentially a no-op since chimera workers are never launched
func (cw *chimeraWorker) markPieceForDownload(pieceIndex uint64) {
	// this is a no-op
}

// pieces implements the downloadWorker interface, chimera workers return all
// pieces as we don't know yet what pieces they can resolve
func (cw *chimeraWorker) pieces() []uint64 {
	return cw.staticPieceIndices
}

// recalculateChanceAfter recalculates the chances reads complete after every
// duration from the distribution tracker.
func (cw *chimeraWorker) recalculateChanceAfter() {
	cw.cachedChancesAfter = cw.distribution().ChancesAfter()
}

// worker implements the downloadWorker interface, chimera workers return nil
// since it's comprised of multiple workers
func (cw *chimeraWorker) worker() *worker {
	return nil
}

// cost implements the downloadWorker interface.
func (iw *individualWorker) cost(length uint64) types.Currency {
	// workers that have already been launched have a zero cost
	if iw.isLaunched() {
		return types.ZeroCurrency
	}
	return iw.staticExpectedCost
}

// chanceAfter returns the chance this worker completes a read after the given
// duration.
func (iw *individualWorker) chanceAfter(index int) float64 {
	return iw.cachedChancesAfter[index]
}

// recalculateChanceAfter recalculates the chances reads complete after every
// duration from the distribution tracker.
func (iw *individualWorker) recalculateChanceAfter() {
	distribution := iw.staticReadDistribution

	// if the worker has been launched already, we want to shift the
	// distribution with the time that elapsed since it was launched
	//
	// NOTE: we always shift on a clone of the original read distribution to
	// avoid shifting the same distribution multiple times
	if iw.isLaunched() {
		clone := iw.staticReadDistribution.Clone()
		clone.Shift(time.Since(iw.launchedAt))
		distribution = clone
	}

	iw.cachedChancesAfter = distribution.ChancesAfter()
}

// getPieceForDownload returns the piece to download next
func (iw *individualWorker) getPieceForDownload() uint64 {
	return iw.currentPiece
}

// identifier returns a unqiue identifier for this worker, for an individual
// worker this is equal to the short string version of the worker's host pubkey.
func (iw *individualWorker) identifier() string {
	return iw.staticIdentifier
}

// isLaunched returns true when this workers has been launched.
func (iw *individualWorker) isLaunched() bool {
	return !iw.launchedAt.IsZero()
}

// markPieceForDownload takes a piece index and marks it as the piece to
// download next for this worker.
func (iw *individualWorker) markPieceForDownload(pieceIndex uint64) {
	// sanity check the given piece is a piece present in the worker's pieces
	if build.Release == "testing" {
		var found bool
		for _, availPieceIndex := range iw.pieceIndices {
			if pieceIndex == availPieceIndex {
				found = true
				break
			}
		}
		if !found {
			build.Critical("markPieceForDownload is marking a piece that is not present in the worker's piece indices")
		}
	}
	iw.currentPiece = pieceIndex
}

// pieces implements the downloadWorker interface.
func (iw *individualWorker) pieces() []uint64 {
	return iw.pieceIndices
}

// resolved returns whether this individual worker is a resolved worker or not,
// which is the case if it's resolve chance is exactly 1.
func (iw *individualWorker) resolved() bool {
	return iw.staticExpectedResolveTime.IsZero()
}

// worker implements the downloadWorker interface.
func (iw *individualWorker) worker() *worker {
	return iw.staticWorker
}

// split will split the download worker into two workers, the first worker will
// have the given chance, the second worker will have the remainder as its
// chance value.
func (iw *individualWorker) split(chance float64) (*individualWorker, *individualWorker) {
	if chance >= iw.resolveChance {
		build.Critical("chance value on which we split should be strictly less than the worker's resolve chance")
		return nil, nil
	}

	main := &individualWorker{
		launchedAt:    iw.launchedAt,
		pieceIndices:  iw.pieceIndices,
		resolveChance: chance,

		cachedChancesAfter: iw.cachedChancesAfter,
		currentPiece:       iw.currentPiece,

		staticExpectedCost:        iw.staticExpectedCost,
		staticExpectedResolveTime: iw.staticExpectedResolveTime,
		staticIdentifier:          iw.staticIdentifier,
		staticReadDistribution:    iw.staticReadDistribution,
		staticWorker:              iw.staticWorker,
	}

	remainder := &individualWorker{
		launchedAt:    iw.launchedAt,
		pieceIndices:  iw.pieceIndices,
		resolveChance: iw.resolveChance - chance,

		cachedChancesAfter: iw.cachedChancesAfter,
		currentPiece:       iw.currentPiece,

		staticExpectedCost:        iw.staticExpectedCost,
		staticExpectedResolveTime: iw.staticExpectedResolveTime,
		staticIdentifier:          iw.staticIdentifier,
		staticReadDistribution:    iw.staticReadDistribution,
		staticWorker:              iw.staticWorker,
	}

	return main, remainder
}

// clone returns a shallow copy of the worker set.
func (ws *workerSet) clone() *workerSet {
	return &workerSet{
		workers: append([]downloadWorker{}, ws.workers...),

		staticBucketIndex:      ws.staticBucketIndex,
		staticExpectedDuration: ws.staticExpectedDuration,
		staticLength:           ws.staticLength,
		staticMinPieces:        ws.staticMinPieces,

		staticPDC: ws.staticPDC,
	}
}

// cheaperSetFromCandidate returns a new worker set if the given candidate
// worker can improve the cost of the worker set. The worker that is being
// swapped by the candidate is the most expensive worker possible, which is not
// necessarily the most expensive worker in the set because we have to take into
// account the pieces the worker can download.
func (ws *workerSet) cheaperSetFromCandidate(candidate downloadWorker) *workerSet {
	// build two maps for fast lookups
	originalIndexMap := make(map[string]int)
	piecesToIndexMap := make(map[uint64]int)
	for i, w := range ws.workers {
		originalIndexMap[w.identifier()] = i
		if _, ok := w.(*individualWorker); ok {
			piecesToIndexMap[w.getPieceForDownload()] = i
		}
	}

	// sort the workers by cost, most expensive to cheapest
	byCostDesc := append([]downloadWorker{}, ws.workers...)
	sort.Slice(byCostDesc, func(i, j int) bool {
		wCostI := byCostDesc[i].cost(ws.staticLength)
		wCostJ := byCostDesc[j].cost(ws.staticLength)
		return wCostI.Cmp(wCostJ) > 0
	})

	// range over the workers
	swapIndex := -1
LOOP:
	for _, expensiveWorker := range byCostDesc {
		// if the candidate is not cheaper than this worker we can stop looking
		// to build a cheaper set since the workers are sorted by cost
		if candidate.cost(ws.staticLength).Cmp(expensiveWorker.cost(ws.staticLength)) >= 0 {
			break
		}

		// get some information on the expensive worker
		piece, launched, completed := ws.staticPDC.currentDownload(expensiveWorker)
		expensiveWorkerIndex := originalIndexMap[expensiveWorker.identifier()]
		expensiveWorkerPiece := piece
		expensiveWorkerDownloading := launched && !completed

		// range over the candidate's pieces and see whether we can swap
		for _, piece := range candidate.pieces() {
			// if the candidate can download the same piece, swap it
			if piece == expensiveWorkerPiece {
				swapIndex = expensiveWorkerIndex
				break LOOP
			}

			// if the candidate can download a piece for which we have no worker
			// yet, we want to swap it providing the current worker is not
			// actively downloading
			_, workerForPiece := piecesToIndexMap[piece]
			if !workerForPiece && !expensiveWorkerDownloading {
				swapIndex = expensiveWorkerIndex
				break LOOP
			}
		}
	}

	if swapIndex > -1 {
		cheaperSet := ws.clone()
		cheaperSet.workers[swapIndex] = candidate
		return cheaperSet
	}
	return nil
}

// adjustedDuration returns the cost adjusted expected duration of the worker
// set using the given price per ms.
func (ws *workerSet) adjustedDuration(ppms types.Currency) time.Duration {
	// calculate the total cost of the worker set
	var totalCost types.Currency
	for _, w := range ws.workers {
		totalCost = totalCost.Add(w.cost(ws.staticLength))
	}

	// calculate the cost penalty using the given price per ms and apply it to
	// the worker set's expected duration.
	return addCostPenalty(ws.staticExpectedDuration, totalCost, ppms)
}

// chancesAfter is a small helper function that returns a list of every worker's
// chance it's completed after the given duration.
func (ws *workerSet) chancesAfter(index int) coinflips {
	chances := make(coinflips, len(ws.workers))
	for i, w := range ws.workers {
		chances[i] = w.chanceAfter(index)
	}
	return chances
}

// chanceGreaterThanHalf returns whether the total chance this worker set
// completes the download before the given duration is more than 50%.
//
// NOTE: this function abstracts the chance a worker resolves after the given
// duration as a coinflip to make it easier to reason about the problem given
// that the workerset consists out of one or more overdrive workers.
func (ws *workerSet) chanceGreaterThanHalf(index int) bool {
	// convert every worker into a coinflip
	coinflips := ws.chancesAfter(index)

	var chance float64
	switch ws.numOverdriveWorkers() {
	case 0:
		// if we don't have to consider any overdrive workers, the chance it's
		// all heads is the chance that needs to be greater than half
		chance = coinflips.chanceAllHeads()
	case 1:
		// if there is 1 overdrive worker, we can essentially have one of the
		// coinflips come up as tails, as long as all the others are heads
		chance = coinflips.chanceHeadsAllowOneTails()
	case 2:
		// if there are 2 overdrive workers, we can have two of them come up as
		// tails, as long as all the others are heads
		chance = coinflips.chanceHeadsAllowTwoTails()
	default:
		// if there are a lot of overdrive workers, we use an approximation by
		// summing all coinflips to see whether we are expected to be able to
		// download min pieces within the given duration
		return coinflips.chanceSum() > float64(ws.staticMinPieces)
	}

	return chance > 0.5
}

// numOverdriveWorkers returns the number of overdrive workers in the worker
// set.
func (ws *workerSet) numOverdriveWorkers() int {
	numWorkers := len(ws.workers)
	if numWorkers < ws.staticMinPieces {
		return 0
	}
	return numWorkers - ws.staticMinPieces
}

// String returns a string representation of the worker set
func (ws *workerSet) String() string {
	output := fmt.Sprintf("WORKERSET:\nexpected dur: %v\nworkers:\n", ws.staticExpectedDuration)
	for i, w := range ws.workers {
		_, chimera := w.(*chimeraWorker)
		selected := -1
		if !chimera {
			selected = int(w.getPieceForDownload())
		}
		output += fmt.Sprintf("%v) worker: %v chimera: %v cost: %v pieces: %v selected: %v\n", i+1, w.identifier(), chimera, w.cost(ws.staticLength), w.pieces(), selected)
	}
	return output
}

// chanceAllHeads returns the chance all coins show heads.
func (cf coinflips) chanceAllHeads() float64 {
	if len(cf) == 0 {
		return 0
	}

	chanceAllHeads := float64(1)
	for _, chanceHead := range cf {
		chanceAllHeads *= chanceHead
	}
	return chanceAllHeads
}

// chanceHeadsAllowOneTails returns the chance at least n-1 coins show heads
// where n is the amount of coins.
func (cf coinflips) chanceHeadsAllowOneTails() float64 {
	chanceAllHeads := cf.chanceAllHeads()

	totalChance := chanceAllHeads
	for _, chanceHead := range cf {
		chanceTails := 1 - chanceHead
		totalChance += (chanceAllHeads / chanceHead * chanceTails)
	}
	return totalChance
}

// chanceHeadsAllowTwoTails returns the chance at least n-2 coins show heads
// where n is the amount of coins.
func (cf coinflips) chanceHeadsAllowTwoTails() float64 {
	chanceAllHeads := cf.chanceAllHeads()
	totalChance := cf.chanceHeadsAllowOneTails()

	for i := 0; i < len(cf)-1; i++ {
		chanceIHeads := cf[i]
		chanceITails := 1 - chanceIHeads
		chanceOnlyITails := chanceAllHeads / chanceIHeads * chanceITails
		for jj := i + 1; jj < len(cf); jj++ {
			chanceJHeads := cf[jj]
			chanceJTails := 1 - chanceJHeads
			chanceOnlyIAndJJTails := chanceOnlyITails / chanceJHeads * chanceJTails
			totalChance += chanceOnlyIAndJJTails
		}
	}
	return totalChance
}

// chanceSum returns the sum of all chances
func (cf coinflips) chanceSum() float64 {
	var sum float64
	for _, flip := range cf {
		sum += flip
	}
	return sum
}

// updateWorkers will update the given set of workers in-place, we update the
// workers instead of recreating them because we found that the process of
// creating an individualWorker involves some cpu intensive steps, like gouging.
// By updating them, rather than recreating them, we avoid doing these
// computations in every iteration of the download algorithm.
//
// This method essentially transforms unresolved workers into resolved workers
// and updates the state of a resolved worker in case it got launched or just
// completed a download.
func (pdc *projectDownloadChunk) updateWorkers(workers []*individualWorker) {
	ws := pdc.workerState
	ws.mu.Lock()
	defer ws.mu.Unlock()

	// create two maps to help update the workers
	resolved := make(map[string]int, len(workers))
	unresolved := make(map[string]int, len(workers))
	for i, w := range workers {
		if w.resolved() {
			resolved[w.staticWorker.staticHostPubKeyStr] = i
		} else {
			unresolved[w.staticWorker.staticHostPubKeyStr] = i
		}
	}

	// now iterate over all resolved workers, if we did not have that worker as
	// resolved before, it became resolved and we want to update it
	for _, rw := range ws.resolvedWorkers {
		_, rwExists := resolved[rw.worker.staticHostPubKeyStr]
		uwIndex, uwExists := unresolved[rw.worker.staticHostPubKeyStr]

		// handle the edge case where both don't exist, this might happen when a
		// worker is not part of the worker array because it was deemed unfit
		// for downloading
		if !rwExists && !uwExists {
			continue
		}

		// if it became resolved, update the worker accordingly
		if !rwExists && uwExists {
			workers[uwIndex].pieceIndices = rw.pieceIndices
			workers[uwIndex].resolveChance = 1
			continue
		}
	}
}

// workers returns both resolved and unresolved workers as a single slice of
// individual workers
func (pdc *projectDownloadChunk) workers() []*individualWorker {
	ws := pdc.workerState
	ws.mu.Lock()
	defer ws.mu.Unlock()

	var workers []*individualWorker

	// convenience variables
	ec := pdc.workerSet.staticErasureCoder
	length := pdc.pieceLength

	// add all resolved workers that are deemed good for downloading
	for _, rw := range ws.resolvedWorkers {
		if !isGoodForDownload(rw.worker) {
			continue
		}

		jrq := rw.worker.staticJobReadQueue
		rdt := jrq.staticStats.distributionTrackerForLength(length)

		workers = append(workers, &individualWorker{
			pieceIndices:  rw.pieceIndices,
			resolveChance: 1,

			staticExpectedCost:     jrq.callExpectedJobCost(length),
			staticIdentifier:       rw.worker.staticHostPubKey.ShortString(),
			staticReadDistribution: rdt.Distribution(0),
			staticWorker:           rw.worker,
		})
	}

	// add all unresolved workers that are deemed good for downloading
	for _, uw := range ws.unresolvedWorkers {
		w := uw.staticWorker
		jrq := w.staticJobReadQueue
		rdt := jrq.staticStats.distributionTrackerForLength(length)

		// exclude workers that are not useful
		if !isGoodForDownload(w) {
			continue
		}

		// unresolved workers can still have all pieces
		pieceIndices := make([]uint64, ec.MinPieces())
		for i := 0; i < len(pieceIndices); i++ {
			pieceIndices[i] = uint64(i)
		}

		workers = append(workers, &individualWorker{
			pieceIndices:  pieceIndices,
			resolveChance: w.staticJobHasSectorQueue.callAvailabilityRate(ec.NumPieces()),

			staticIdentifier:          w.staticHostPubKey.ShortString(),
			staticExpectedCost:        jrq.callExpectedJobCost(length),
			staticExpectedResolveTime: uw.staticExpectedResolvedTime,
			staticReadDistribution:    rdt.Distribution(0),
			staticWorker:              w,
		})
	}

	return workers
}

// currentDownload returns the piece that was marked on the worker to download
// next, alongside two booleans that indicate whether it was already launched
// and whether it was copmleted.
func (pdc *projectDownloadChunk) currentDownload(w downloadWorker) (uint64, bool, bool) {
	// return defaults if the worker is a chimera worker, those are not
	// downloading by definition
	iw, ok := w.(*individualWorker)
	if !ok {
		return 0, false, false
	}

	// get the marked piece for this worker
	currentPiece := w.getPieceForDownload()

	// use the launched and completed maps on the pdc to find out whether the
	// download is ongoing.
	workerKey := iw.worker().staticHostPubKeyStr
	launchedPieces, exists := pdc.launchedPiecesByWorker[workerKey]
	if !exists {
		return currentPiece, false, false
	}

	_, exists = launchedPieces[currentPiece]
	if !exists {
		return currentPiece, false, false
	}

	completedPieces, exists := pdc.completedPiecesByWorker[workerKey]
	if !exists {
		return currentPiece, true, false
	}

	_, exists = completedPieces[currentPiece]
	return currentPiece, true, exists
}

// launchWorkerSet will range over the workers in the given worker set and will
// try to launch every worker that has not yet been launched and is ready to
// launch.
func (pdc *projectDownloadChunk) launchWorkerSet(ws *workerSet, allWorkers []downloadWorker) {
	// convenience variables
	minPieces := pdc.workerSet.staticErasureCoder.MinPieces()

	// range over all workers in the set and launch if possible
	for _, w := range ws.workers {
		// continue if the worker is a chimera worker
		cw, ok := w.(*chimeraWorker)
		if ok {
			resolved := ""
			for _, dw := range allWorkers {
				if iw, ok := dw.(*individualWorker); ok && len(iw.pieceIndices) > 0 {
					resolved += fmt.Sprintf("%v resolves in %v has %v chance to complete after %v\n", iw.staticWorker.staticHostPubKeyStr, time.Until(iw.staticExpectedResolveTime), iw.chanceAfter(
						ws.staticBucketIndex), ws.staticExpectedDuration)
				}
			}
			if span := opentracing.SpanFromContext(pdc.ctx); span != nil {
				span.LogKV(
					"chimera", cw.cachedChancesAfter[ws.staticBucketIndex],
					"resolvedWorkers", resolved,
					"workerSetExpectedDuration", ws.staticExpectedDuration,
				)
			}
			continue
		}

		// continue if the worker is already launched
		piece, launched, _ := pdc.currentDownload(w)
		if launched {
			continue
		}

		// launch the piece
		isOverdrive := len(pdc.launchedWorkers) >= minPieces
		expectedCompleteTime, launched := pdc.launchWorker(w.worker(), piece, isOverdrive)
		// fmt.Printf("- %v launched for %v success %v\n", w.identifier(), piece, launched)
		if launched {
			// Log the event.
			chimeras := ""
			for _, dw := range allWorkers {
				if cw, ok := dw.(*chimeraWorker); ok {
					chimeras += fmt.Sprintf("%v (%v) chance: %v chanceAtMaxIndex: %v\n", cw.identifier(), len(cw.workers), cw.chanceAfter(ws.staticBucketIndex), cw.chanceAfter(skymodules.DistributionTrackerTotalBuckets-1))
				}
			}
			if span := opentracing.SpanFromContext(pdc.ctx); span != nil {
				span.LogKV(
					"launchWorker", w.identifier(),
					"overdriveWorker", isOverdrive,
					"expectedDuration", time.Until(expectedCompleteTime),
					"chanceAfterDur", w.chanceAfter(ws.staticBucketIndex),
					"wsDuration", ws.staticExpectedDuration,
					"wsIndex", ws.staticBucketIndex,
					"chimeras", chimeras,
					"success", launched,
				)
			}

			iw := w.(*individualWorker)
			iw.launchedAt = time.Now()
		}
	}
	return
}

// threadedLaunchProjectDownload performs the main download loop, every
// iteration we update the pdc's available pieces, construct a new worker set
// and launch every worker that can be launched from that set. Every iteration
// we check whether the download was finished.
func (pdc *projectDownloadChunk) threadedLaunchProjectDownload() {
	// grab some variables
	ec := pdc.workerSet.staticErasureCoder
	minPieces := ec.MinPieces()
	numPieces := ec.NumPieces()
	ws := pdc.workerState

	// grab the workers, this set of workers will not change but rather get
	// updated to avoid needless performing gouging checks on every iteration
	workers := pdc.workers()

	// verify we have enough workers to complete the download
	if len(workers) < minPieces {
		pdc.fail(errors.Compose(ErrRootNotFound, errors.AddContext(errNotEnoughWorkers, fmt.Sprintf("%v < %v", len(workers), minPieces))))
		return
	}

	// create download workers out of the workers
	buildDownloadWorkersCnt := 1
	downloadWorkers := buildDownloadWorkers(workers, numPieces)

	// register for a worker update chan
	workerUpdateChan := ws.managedRegisterForWorkerUpdate()

	prevLog := time.Now()
	for {
		// fmt.Println("ITER")
		if span := opentracing.SpanFromContext(pdc.ctx); span != nil && time.Since(prevLog) > 100*time.Millisecond {
			span.LogKV(
				"downloadLoopIter", time.Since(pdc.launchTime),
			)
			prevLog = time.Now()
		}

		// update the workers on every iteration
		pdc.updateWorkers(workers)

		// update the available pieces
		updated := pdc.updateAvailablePieces()
		if updated {
			// recreate the download workers, we only do this when we know the
			// resolved vs unresolved workers configuration changed, because
			// that might cause a different set of download workers
			//
			// we have to avoid rebuilding it as much as possible because it
			// recomputes the chances after duration, which is a computationally
			// very intensive
			// fmt.Println("rebuild download workers")
			downloadWorkers = buildDownloadWorkers(workers, numPieces)
			buildDownloadWorkersCnt++
		}

		// create a worker set and launch it
		workerSet, err := pdc.createWorkerSet(downloadWorkers, maxOverdriveWorkers)
		if err != nil {
			pdc.fail(err)
			return
		}
		if workerSet != nil {
			// fmt.Println(workerSet.String())
			pdc.launchWorkerSet(workerSet, downloadWorkers)
		}

		// iterate
		select {
		case <-time.After(maxWaitUnresolvedWorkerUpdate):
			// recreate the workerset every 10ms
		case <-workerUpdateChan:
			// replace the worker update channel
			workerUpdateChan = ws.managedRegisterForWorkerUpdate()
		case jrr := <-pdc.workerResponseChan:
			// fmt.Printf("+ %v completed piece %v with err %v\n",
			// jrr.staticMetadata.staticWorker.staticHostPubKey.ShortString(),
			// jrr.staticMetadata.staticPieceRootIndex, jrr.staticErr)
			start := time.Now()
			pdc.handleJobReadResponse(jrr)
			if span := opentracing.SpanFromContext(pdc.ctx); span != nil {
				span.LogKV(
					"handleJobReadResponse", jrr.staticMetadata.staticWorker.staticHostPubKeyStr,
					"took", time.Since(start),
					"error", jrr.staticErr,
				)
			}
		case <-pdc.ctx.Done():
			// fmt.Println("DOWNLOAD TIMED OUT", err)
			pdc.fail(errors.New("download timed out"))
			return
		}

		// check whether the download is completed
		completed, err := pdc.finished()
		if completed {
			if span := opentracing.SpanFromContext(pdc.ctx); span != nil {
				span.LogKV(
					"finished", 1,
					"buildDownloadWorkersCnt", buildDownloadWorkersCnt,
				)
			}
			// fmt.Println("DOWNLOAD COMPLETED")
			pdc.finalize()
			return
		}
		if err != nil {
			// fmt.Println("DOWNLOAD FAILED", err)
			pdc.fail(err)
			return
		}
	}
}

// createWorkerSet tries to create a worker set from the pdc's resolved and
// unresolved workers, the maximum amount of overdrive workers in the set is
// defined by the given 'maxOverdriveWorkers' argument.
func (pdc *projectDownloadChunk) createWorkerSet(downloadWorkers []downloadWorker, maxOverdriveWorkers int) (*workerSet, error) {
	// convenience variables
	ppms := pdc.pricePerMS
	length := pdc.pieceLength
	minPieces := pdc.workerSet.staticErasureCoder.MinPieces()

	// loop state
	var bestSet *workerSet

	// can't create a workerset without download workers
	if len(downloadWorkers) == 0 {
		return nil, nil
	}

OUTER:
	for numOverdrive := 0; numOverdrive <= maxOverdriveWorkers; numOverdrive++ {
		workersNeeded := minPieces + numOverdrive
		for bI := 0; bI < skymodules.DistributionTrackerTotalBuckets; bI++ {
			bDur := skymodules.DistributionDurationForBucketIndex(bI)
			// exit early if ppms in combination with the bucket duration
			// already exceeds the adjusted cost of the current best set,
			// workers would be too slow by definition
			if bestSet != nil && bDur > bestSet.adjustedDuration(ppms) {
				// fmt.Printf("best set found with %v OD, breaking at %v\n", numOverdrive, bDur)
				break OUTER
			}

			// divide the workers in most likely and less likely
			mostLikely, lessLikely := pdc.splitMostlikelyLessLikely(downloadWorkers, bI, workersNeeded)
			// fmt.Printf("%v workers split in %v most likely and %v less\n", len(downloadWorkers), len(mostLikely), len(lessLikely))

			// if there aren't even likely workers, escape early
			if len(mostLikely) == 0 {
				break OUTER
			}

			// build the most likely set
			mostLikelySet := &workerSet{
				workers: mostLikely,

				staticBucketIndex:      bI,
				staticExpectedDuration: bDur,
				staticLength:           length,
				staticMinPieces:        minPieces,

				staticPDC: pdc,
			}

			// fmt.Printf("%v workers split into %v most likely and %v less likely\n", len(downloadWorkers), len(mostLikely), len(lessLikely))
			// fmt.Println("mostlikely set", mostLikelySet)

			// if the chance of the most likely set does not exceed 50%, it is
			// not high enough to continue, no need to continue this iteration,
			// we need to try a slower and thus more likely bucket
			if !mostLikelySet.chanceGreaterThanHalf(bI) {
				// fmt.Println("mostlikely set not good enough")
				continue
			}

			// now loop the less likely workers and try and swap them with the
			// most expensive workers in the most likely set
			for _, w := range lessLikely {
				cheaperSet := mostLikelySet.cheaperSetFromCandidate(w)
				if cheaperSet == nil {
					continue
				}

				// if the cheaper set's chance of completing before the given
				// duration is not greater than half we can break because the
				// `lessLikely` workers were sorted by chance
				if !cheaperSet.chanceGreaterThanHalf(bI) {
					break
				}
				mostLikelySet = cheaperSet
			}

			// perform price per ms comparison
			if bestSet == nil {
				bestSet = mostLikelySet
			} else if mostLikelySet.adjustedDuration(ppms) < bestSet.adjustedDuration(ppms) {
				bestSet = mostLikelySet
			}
		}
	}

	// fmt.Println("best set", bestSet)
	return bestSet, nil
}

// addCostPenalty takes a certain job time and adds a penalty to it depending on
// the jobcost and the pdc's price per MS.
func addCostPenalty(jobTime time.Duration, jobCost, pricePerMS types.Currency) time.Duration {
	// If the pricePerMS is higher or equal than the cost of the job, simply
	// return without penalty.
	if pricePerMS.Cmp(jobCost) >= 0 {
		return jobTime
	}

	// Otherwise, add a penalty
	var adjusted time.Duration
	penalty, err := jobCost.Div(pricePerMS).Uint64()
	if err != nil || penalty > math.MaxInt64 {
		adjusted = time.Duration(math.MaxInt64)
	} else if reduced := math.MaxInt64 - int64(penalty); int64(jobTime) > reduced {
		adjusted = time.Duration(math.MaxInt64)
	} else {
		adjusted = jobTime + (time.Duration(penalty) * time.Millisecond)
	}
	return adjusted
}

// buildChimeraWorkers is a helper function that turns a list of individual
// workers into chimera workers.
func buildChimeraWorkers(workers []*individualWorker, numPieces int) []downloadWorker {
	// sort workers by expected resolve time
	sort.Slice(workers, func(i, j int) bool {
		eRTI := workers[i].staticExpectedResolveTime
		eRTJ := workers[j].staticExpectedResolveTime
		return eRTI.Before(eRTJ)
	})

	// build chimera workers out of them
	var chimeras []downloadWorker
	current := NewChimeraWorker(numPieces)
	for _, w := range workers {
		// sanity check it's an unresolved worker
		if w.resolved() {
			build.Critical("developer error, chimera workers are built using only unresolved workers")
		}

		// add the worker, if there is no remainder keep going
		remainder := current.addWorker(w)
		if remainder == nil {
			continue
		}

		// sanity check the remainder
		if remainder.resolveChance >= 1 {
			build.Critical("developer error, the resolve chance of the remainder needs to be strictly less than one")
		}

		// we have a remainder, meaning the current worker is a complete chimera
		// worker, so reset the current and add the chimera to the list
		chimeras = append(chimeras, current)
		current = NewChimeraWorker(numPieces)
		current.addWorker(remainder)
	}

	// NOTE: we don't add the current as it's not a complete chimera worker

	return chimeras
}

// buildDownloadWorkers is a helper function that takes a list of individual
// workers and turns them into download workers.
func buildDownloadWorkers(workers []*individualWorker, numPieces int) []downloadWorker {
	// turn the given workers into download workers, if an individual worker is
	// resolved it can be added straight away, unresolved workers are combined
	// into chimera workers
	var downloadWorkers []downloadWorker
	var unresolvedWorkers []*individualWorker
	for _, w := range workers {
		// workers that can't download any pieces are ignored
		if len(w.pieceIndices) == 0 {
			continue
		}
		if w.resolved() {
			downloadWorkers = append(downloadWorkers, w)
		} else {
			unresolvedWorkers = append(unresolvedWorkers, w)
		}
	}

	// create chimera workers out of the unresolved worker and add them
	chimeraWorkers := buildChimeraWorkers(unresolvedWorkers, numPieces)
	downloadWorkers = append(downloadWorkers, chimeraWorkers...)

	// precompute the chanceAfterDur values
	for _, w := range downloadWorkers {
		w.recalculateChanceAfter()
	}

	return downloadWorkers
}

// splitMostlikelyLessLikely takes a list of download workers alongside a
// duration and an amount of workers that are needed for the most likely set of
// workers to compplete a download (this is not necessarily equal to 'minPieces'
// workers but also takes into account an amount of overdrive workers). This
// method will split the given workers array in a list of most likely workers,
// and a list of less likely workers.
func (pdc *projectDownloadChunk) splitMostlikelyLessLikely(workers []downloadWorker, bI int, workersNeeded int) ([]downloadWorker, []downloadWorker) {
	// sort the workers by percentage chance they complete after the
	// current bucket duration
	sort.Slice(workers, func(i, j int) bool {
		chanceI := workers[i].chanceAfter(bI)
		chanceJ := workers[j].chanceAfter(bI)
		return chanceI > chanceJ
	})

	// group 'workersNeeded' from the workers as the most likely, and
	// group the rest as less likely
	mostLikely := make([]downloadWorker, 0, workersNeeded)
	var lessLikelyCap int
	if len(workers) > workersNeeded {
		lessLikelyCap = len(workers) - workersNeeded
	}
	lessLikely := make([]downloadWorker, 0, lessLikelyCap)

	// loop over the download workers once, we want to add all workers
	// that have already launched a piece and make sure they are in the
	// worker set
	added := make(map[string]struct{}, 0)
	pieces := make(map[uint64]struct{}, 0)
	for _, w := range workers {
		piece, launched, completed := pdc.currentDownload(w)
		if launched && !completed {
			mostLikely = append(mostLikely, w)
			added[w.identifier()] = struct{}{}
			pieces[piece] = struct{}{}
		}
	}

	for downloaded := range pdc.downloadedPiecesByIndex {
		pieces[downloaded] = struct{}{}
	}

	// loop over the download workers again
	for _, w := range workers {
		_, added := added[w.identifier()]
		if added {
			continue
		}

		for _, pieceIndex := range w.pieces() {
			_, exists := pieces[pieceIndex]
			if exists {
				continue
			}

			if len(mostLikely) < workersNeeded {
				mostLikely = append(mostLikely, w)
			} else {
				lessLikely = append(lessLikely, w)
			}

			w.markPieceForDownload(pieceIndex)
			pieces[pieceIndex] = struct{}{}
			break // only use a worker once
		}
	}

	return mostLikely, lessLikely
}

// isGoodForDownload is a helper function that returns true if and only if the
// worker meets a certain set of criteria that make it useful for downloads.
// It's only useful if it is not on any type of cooldown, if it's async ready
// and if it's not price gouging.
func isGoodForDownload(w *worker) bool {
	// workers on cooldown or that are non async ready are not useful
	if w.managedOnMaintenanceCooldown() || !w.managedAsyncReady() {
		return false
	}

	// workers with a read queue on cooldown
	hsq := w.staticJobHasSectorQueue
	rjq := w.staticJobReadQueue
	if hsq.onCooldown() || rjq.onCooldown() {
		return false
	}

	// workers that are price gouging are not useful
	pt := w.staticPriceTable().staticPriceTable
	allowance := w.staticCache().staticRenterAllowance
	if err := checkProjectDownloadGouging(pt, allowance); err != nil {
		return false
	}

	return true
}
