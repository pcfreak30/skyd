package renter

import (
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/types"
)

// maxWaitUnresolvedWorkerUpdate defines the amount of time we want to wait for
// unresolved workers to become resolved when trying to create the initial
// worker set.
const maxWaitUnresolvedWorkerUpdate = 10 * time.Millisecond

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

		// distribution returns the worker's read distribution, for an already
		// launched worker the distribution will have been shifted by the amount
		// of time since it was launched.
		distribution() *skymodules.Distribution

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

		// worker returns the underlying worker
		worker() *worker

		// TODO: remove me (added for debugging purposes and I'd like to keep
		// them in there until the code is more finalised, they help give more
		// information about the worker when logging information)
		launched() bool
		chimera() bool
	}

	// chimeraWorker is a worker that's built from unresolved workers until the
	// chance it has a piece is exactly 1. At that point we can treat a chimera
	// worker exactly the same as a resolved worker in the download algorithm
	// that constructs the best worker set.
	chimeraWorker struct {
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
		staticUID [8]byte
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

		// currentPiece is the piece that was marked by the download algorithm
		// as the piece to download next, this is used to ensure that workers
		// in the worker are not selected for duplicate piece indices.
		currentPiece uint64

		// staticUID uniquely identifies the chimera worker
		staticUID [8]byte

		staticExpectedResolveTime time.Time
		staticReadDistribution    *skymodules.Distribution
		staticWorker              *worker
	}

	// workerSet is a collection of workers that may or may not have been
	// launched yet in order to fulfil a download.
	workerSet struct {
		workers []downloadWorker

		staticExpectedDuration time.Duration
		staticLength           uint64
		staticMinPieces        int
	}

	// coinflips is a collection of chances where every item is the chance the
	// coin will turn up heads. We use the concept of a coin because it allows
	// to more easily reason about chance calculations.
	coinflips []float64
)

// resolved returns whether this individual worker is a resolved worker or not,
// which is the case if it's resolve chance is exactly 1.
func (iw *individualWorker) resolved() bool {
	return iw.resolveChance == 1
}

// TODO: remove me (debugging)
func (iw *individualWorker) chimera() bool {
	return false
}

// TODO: remove me (debugging)
func (cw *chimeraWorker) chimera() bool {
	return true
}

// TODO: remove me (debugging)
func (iw *individualWorker) launched() bool {
	return !iw.launchedAt.IsZero()
}

// TODO: remove me (debugging)
func (cw *chimeraWorker) launched() bool {
	return false
}

// TODO: remove me (debugging)
func (iw *individualWorker) identifier() string {
	identifier := hex.EncodeToString(iw.staticUID[:])
	if identifier == "0000000000000000" {
		build.Critical("developer error, uid not initialized")
	}
	return identifier
}

// TODO: remove me (debugging)
func (cw *chimeraWorker) identifier() string {
	identifier := hex.EncodeToString(cw.staticUID[:])
	if identifier == "0000000000000000" {
		build.Critical("developer error, uid not initialized")
	}
	return identifier
}

// markPieceForDownload takes a piece index and marks it as the piece to
// download next for this worker.
func (iw *individualWorker) markPieceForDownload(pieceIndex uint64) {
	iw.currentPiece = pieceIndex
}

// markPieceForDownload takes a piece index and marks it as the piece to
// download for this worker. In the case of a chimera worker this method is
// essentially a no-op since chimera workers are never launched
func (cw *chimeraWorker) markPieceForDownload(pieceIndex uint64) {
	// this is a no-op
}

// getPieceForDownload returns the piece to download next
func (iw *individualWorker) getPieceForDownload() uint64 {
	return iw.currentPiece
}

// getPieceForDownload returns the piece to download next, for a chimera worker
// this is always 0 and should never be called, which is why we add a
// build.Critical to signal developer error.
func (cw *chimeraWorker) getPieceForDownload() uint64 {
	build.Critical("developer error, should not get called on a chimera worker")
	return 0
}

// NewChimeraWorker returns a new chimera worker object.
func NewChimeraWorker(numPieces int) *chimeraWorker {
	pieceIndices := make([]uint64, numPieces)
	for i := 0; i < numPieces; i++ {
		pieceIndices[i] = uint64(i)
	}
	return &chimeraWorker{remaining: 1, staticPieceIndices: pieceIndices}
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
	numWorkers := uint64(len(cw.workers))
	if numWorkers == 0 {
		return types.ZeroCurrency
	}

	var total types.Currency
	for _, w := range cw.workers {
		total = total.Add(w.staticJobReadQueue.callExpectedJobCost(length))
	}
	return total.Div64(numWorkers)
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

// pieces implements the downloadWorker interface, chimera workers return all
// pieces as we don't know yet what pieces they can resolve
func (cw *chimeraWorker) pieces() []uint64 {
	return cw.staticPieceIndices
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
	return iw.staticWorker.staticJobReadQueue.callExpectedJobCost(length)
}

// distribution implements the downloadWorker interface.
func (iw *individualWorker) distribution() *skymodules.Distribution {
	// if the worker has been launched already, we want to shift the
	// distribution with the time that elapsed since it was launched
	//
	// NOTE: we always shift on a clone of the original read distribution to
	// avoid shifting the same distribution multiple times
	if iw.isLaunched() {
		clone := iw.staticReadDistribution.Clone()
		clone.Shift(time.Since(iw.launchedAt))
		return clone
	}
	return iw.staticReadDistribution
}

// isLaunched returns true when this workers has been launched.
func (iw *individualWorker) isLaunched() bool {
	return !iw.launchedAt.IsZero()
}

// pieces implements the downloadWorker interface.
func (iw *individualWorker) pieces() []uint64 {
	return iw.pieceIndices
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

		staticReadDistribution: iw.staticReadDistribution,
		staticWorker:           iw.staticWorker,
	}
	remainder := &individualWorker{
		launchedAt:    iw.launchedAt,
		pieceIndices:  iw.pieceIndices,
		resolveChance: iw.resolveChance - chance,

		staticReadDistribution: iw.staticReadDistribution,
		staticWorker:           iw.staticWorker,
	}

	return main, remainder
}

// clone returns a shallow copy of the worker set.
func (ws *workerSet) clone() *workerSet {
	return &workerSet{
		workers: append([]downloadWorker{}, ws.workers...),

		staticExpectedDuration: ws.staticExpectedDuration,
		staticLength:           ws.staticLength,
		staticMinPieces:        ws.staticMinPieces,
	}
}

// cheaperSetFromCandidate returns a new worker set if the given candidate
// worker can improve the cost of the worker set. The worker that is being
// swapped by the candidate is the most expensive worker possible, which is not
// necessarily the most expensive worker in the set because we have to take into
// account the pieces the worker can download.
func (ws *workerSet) cheaperSetFromCandidate(candidate downloadWorker) *workerSet {
	// build two maps for fast lookups
	workersToIndex := make(map[string]int)
	pieceToIndex := make(map[uint64]int)
	for i, w := range ws.workers {
		// skip chimera workers, those workers can resolve all pieces
		_, ok := w.(*chimeraWorker)
		if ok {
			continue
		}

		// map the worker index
		workersToIndex[w.identifier()] = i
		pieceToIndex[w.getPieceForDownload()] = i
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

		expensiveWorkerIndex := workersToIndex[expensiveWorker.identifier()]

		// the candidate is only useful if it can replace a worker and
		// contribute pieces to the worker set for which we don't already have
		// another worker
		for _, piece := range candidate.pieces() {
			currentWorkerIndex, exists := pieceToIndex[piece]
			if !exists || currentWorkerIndex == expensiveWorkerIndex {
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
func (ws workerSet) chancesAfter(dur time.Duration) coinflips {
	chances := make(coinflips, len(ws.workers))
	for i, w := range ws.workers {
		chances[i] = w.distribution().ChanceAfter(dur)
	}
	return chances
}

// chanceGreaterThanHalf returns whether the total chance this worker set
// completes the download before the given duration is more than 50%.
//
// NOTE: this function abstracts the chance a worker resolves after the given
// duration as a coinflip to make it easier to reason about the problem given
// that the workerset consists out of one or more overdrive workers.
func (ws workerSet) chanceGreaterThanHalf(dur time.Duration) bool {
	// convert every worker into a coinflip
	coinflips := ws.chancesAfter(dur)

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
func (ws workerSet) numOverdriveWorkers() int {
	numWorkers := len(ws.workers)
	if numWorkers < ws.staticMinPieces {
		return 0
	}
	return numWorkers - ws.staticMinPieces
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
		rwIndex, rwExists := resolved[rw.worker.staticHostPubKeyStr]
		uwIndex, uwExists := unresolved[rw.worker.staticHostPubKeyStr]

		// handle the edge case where both don't exist, this might happen when a
		// worker is not part of the worker array because it was deemed unfit
		// for downloading
		if !rwExists && !uwExists {
			continue
		}

		// if it became resolved, update the worker accordingly
		if !rwExists && uwExists {
			workers[uwIndex].pieceIndices = append([]uint64{}, rw.pieceIndices...)
			workers[uwIndex].resolveChance = 1
			continue
		}

		// if it was resolved already, we want to update a couple of fields
		if rwExists {
			// update the launched at
			workers[rwIndex].launchedAt = pdc.launchedAt(rw.worker)
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

		stats := rw.worker.staticJobReadQueue.staticStats
		rdt := stats.distributionTrackerForLength(length)

		iw := &individualWorker{
			launchedAt:    pdc.launchedAt(rw.worker),
			pieceIndices:  append([]uint64{}, rw.pieceIndices...),
			resolveChance: 1,

			staticReadDistribution: rdt.Distribution(0),
			staticWorker:           rw.worker,
		}
		fastrand.Read(iw.staticUID[:])
		workers = append(workers, iw)
	}

	// add all unresolved workers that are deemed good for downloading
	for _, uw := range ws.unresolvedWorkers {
		w := uw.staticWorker
		stats := w.staticJobReadQueue.staticStats
		rdt := stats.distributionTrackerForLength(length)

		// exclude workers that are not useful
		if !isGoodForDownload(w) {
			continue
		}

		// unresolved workers can still have all pieces
		pieceIndices := make([]uint64, ec.MinPieces())
		for i := 0; i < len(pieceIndices); i++ {
			pieceIndices[i] = uint64(i)
		}

		if w.staticJobHasSectorQueue.callAvailabilityRate(ec.NumPieces()) == 1 {
			build.Critical("ja dat ist")
		}

		iw := &individualWorker{
			pieceIndices:  pieceIndices,
			resolveChance: w.staticJobHasSectorQueue.callAvailabilityRate(ec.NumPieces()),

			staticExpectedResolveTime: uw.staticExpectedResolvedTime,
			staticReadDistribution:    rdt.Distribution(0),
			staticWorker:              w,
		}
		fastrand.Read(iw.staticUID[:])
		workers = append(workers, iw)
	}

	return workers
}

// launchedAt returns the most recent time at which this worker was launched.
func (pdc *projectDownloadChunk) launchedAt(w *worker) time.Time {
	launchedPieces, exists := pdc.launchedPiecesByWorker[w.staticHostPubKeyStr]
	if !exists {
		return time.Time{}
	}

	var mostRecentLaunchTime time.Time
	for _, launchTime := range launchedPieces {
		if launchTime.After(mostRecentLaunchTime) {
			mostRecentLaunchTime = launchTime
		}
	}
	return mostRecentLaunchTime
}

// isLaunched returns true if the given worker was already launched for the
// given piece.
func (pdc *projectDownloadChunk) isLaunched(w *worker, piece uint64) bool {
	launchedPieces, exists := pdc.launchedPiecesByWorker[w.staticHostPubKeyStr]
	if !exists {
		return false
	}
	_, exists = launchedPieces[piece]
	return exists
}

// launchWorkerSet will range over the workers in the given worker set and will
// try to launch every worker that has not yet been launched and is ready to
// launch.
func (pdc *projectDownloadChunk) launchWorkerSet(ws *workerSet) {
	// convenience variables
	minPieces := pdc.workerSet.staticErasureCoder.MinPieces()

	// range over all workers in the set and launch if possible
	for _, w := range ws.workers {
		// continue if the worker is a chimera worker
		_, ok := w.(*chimeraWorker)
		if ok {
			continue
		}

		// continue if the worker is already launched
		piece := w.getPieceForDownload()
		if pdc.isLaunched(w.worker(), piece) {
			fmt.Println("worker already launched")
			continue
		}

		// launch the piece
		isOverdrive := len(pdc.launchedWorkers) >= minPieces
		pdc.launchWorker(w.worker(), piece, isOverdrive)
	}
	return
}

// threadedLaunchWorkers performs the main download loop, every iteration we
// update the pdc's available pieces, construct a new worker set and launch
// every worker that can be launched from that set. Every iteration we check
// whether the download was finished.
func (pdc *projectDownloadChunk) threadedLaunchWorkers() {
	// register for a worker update chan
	ws := pdc.workerState
	ws.mu.Lock()
	workerUpdateChan := ws.registerForWorkerUpdate()
	ws.mu.Unlock()

	// grab the workers, this set of workers will not change but rather get
	// updated to avoid needless performing gouging checks on every iteration
	workers := pdc.workers()

	// verify we have enough workers to complete the download
	minPieces := pdc.workerSet.staticErasureCoder.MinPieces()
	if len(workers) < minPieces {
		pdc.fail(errors.Compose(ErrRootNotFound, errors.AddContext(errNotEnoughWorkers, fmt.Sprintf("%v < %v", len(workers), minPieces))))
		return
	}

	for {
		// update the available pieces
		pdc.updateAvailablePieces()

		// update the workers on every iteration
		pdc.updateWorkers(workers)

		// create a worker set and launch it
		workerSet, err := pdc.createWorkerSet(workers, maxOverdriveWorkers)
		if err != nil {
			pdc.fail(err)
			return
		}
		if workerSet != nil {
			pdc.launchWorkerSet(workerSet)
		}

		// check whether the download is completed
		completed, err := pdc.finished()
		if completed {
			pdc.finalize()
			return
		}
		if err != nil {
			pdc.fail(err)
			return
		}

		// iterate
		select {
		case <-time.After(maxWaitUnresolvedWorkerUpdate):
			// recreate the workerset every 10ms
		case <-workerUpdateChan:
			// register for another update chan
			ws := pdc.workerState
			ws.mu.Lock()
			workerUpdateChan = ws.registerForWorkerUpdate()
			ws.mu.Unlock()
		case jrr := <-pdc.workerResponseChan:
			pdc.handleJobReadResponse(jrr)
		case <-pdc.ctx.Done():
			pdc.fail(errors.New("download timed out"))
			return
		}
	}
}

// createWorkerSet tries to create a worker set from the pdc's resolved and
// unresolved workers, the maximum amount of overdrive workers in the set is
// defined by the given 'maxOverdriveWorkers' argument.
func (pdc *projectDownloadChunk) createWorkerSet(allWorkers []*individualWorker, maxOverdriveWorkers int) (*workerSet, error) {
	// convenience variables
	ppms := pdc.pricePerMS
	length := pdc.pieceLength
	minPieces := pdc.workerSet.staticErasureCoder.MinPieces()
	numPieces := pdc.workerSet.staticErasureCoder.NumPieces()

	// split the given workers in resolved and unresolved workers
	var resolvedWorkers []*individualWorker
	var unresolvedWorkers []*individualWorker
	for _, iw := range allWorkers {
		if iw.resolveChance == 1 {
			resolvedWorkers = append(resolvedWorkers, iw)
		} else {
			unresolvedWorkers = append(unresolvedWorkers, iw)
		}
	}

	// sort unresolved workers by expected resolve time
	sort.Slice(unresolvedWorkers, func(i, j int) bool {
		eRTI := unresolvedWorkers[i].staticExpectedResolveTime
		eRTJ := unresolvedWorkers[j].staticExpectedResolveTime
		return eRTI.Before(eRTJ)
	})

	// combine unresolved workers into a set of chimera workers
	var chimeraWorkers []*chimeraWorker
	current := NewChimeraWorker(numPieces)
	for _, uw := range unresolvedWorkers {
		remainder := current.addWorker(uw)
		if remainder == nil {
			// chimera is not complete yet
			continue
		}

		// chimera is complete, so we add it and reset the "current" worker
		// using the remainder worker
		chimeraWorkers = append(chimeraWorkers, current)
		current = NewChimeraWorker(numPieces)
		current.addWorker(remainder)
	}

	// note that we ignore the "current" worker as it is not complete

	// combine all workers
	workers := make([]downloadWorker, len(resolvedWorkers)+len(chimeraWorkers))
	for rwi, rw := range resolvedWorkers {
		workers[rwi] = rw
	}
	for cwi, cw := range chimeraWorkers {
		workers[len(resolvedWorkers)+cwi] = cw
	}
	if len(workers) == 0 {
		return nil, nil
	}

	// loop state
	var bestSet *workerSet
	var bestSetFound bool

OUTER:
	for numOverdrive := 0; numOverdrive <= maxOverdriveWorkers; numOverdrive++ {
		workersNeeded := minPieces + numOverdrive
		for bI := 0; bI < skymodules.DistributionTrackerTotalBuckets; bI++ {
			bDur := skymodules.DistributionDurationForBucketIndex(bI)
			// exit early if ppms in combination with the bucket duration
			// already exceeds the adjusted cost of the current best set,
			// workers would be too slow by definition
			if bestSetFound && bDur > bestSet.adjustedDuration(ppms) {
				break OUTER
			}

			// sort the workers by percentage chance they complete after the
			// current bucket duration
			sort.Slice(workers, func(i, j int) bool {
				chanceI := workers[i].distribution().ChanceAfter(bDur)
				chanceJ := workers[j].distribution().ChanceAfter(bDur)
				return chanceI > chanceJ
			})

			// group the most likely workers to complete in the current duration
			// in a way that we ensure no two workers are going after the same
			// piece
			var mostLikely []downloadWorker
			var lessLikely []downloadWorker
			pieces := make(map[uint64]struct{})
			for _, w := range workers {
				var launchedPieces map[uint64]time.Time
				if w.worker() != nil {
					launchedPieces = pdc.launchedPiecesByWorker[w.worker().staticHostPubKeyStr]
				}

				for _, pieceIndex := range w.pieces() {
					_, exists := pieces[pieceIndex]
					if exists {
						continue
					}
					_, exists = launchedPieces[pieceIndex]
					if exists {
						continue
					}

					w.markPieceForDownload(pieceIndex)
					pieces[pieceIndex] = struct{}{}

					if len(mostLikely) < workersNeeded {
						mostLikely = append(mostLikely, w)
					} else {
						lessLikely = append(lessLikely, w)
					}
					break // only use a worker once
				}
			}

			mostLikelySet := &workerSet{
				workers: mostLikely,

				staticExpectedDuration: bDur,
				staticLength:           length,
				staticMinPieces:        minPieces,
			}

			// if the chance of the most likely set does not exceed 50%, it is
			// not high enough to continue, no need to continue this iteration,
			// we need to try a slower and thus more likely bucket
			if !mostLikelySet.chanceGreaterThanHalf(bDur) {
				continue
			}

			// now loop the remaining workers and try and swap them with the
			// most expensive workers in the most likely set
			for _, w := range lessLikely {
				cheaperSet := mostLikelySet.cheaperSetFromCandidate(w)
				if cheaperSet == nil {
					continue
				}
				if !cheaperSet.chanceGreaterThanHalf(bDur) {
					break
				}
				mostLikelySet = cheaperSet
			}

			// perform price per ms comparison
			if !bestSetFound {
				bestSet = mostLikelySet
				bestSetFound = true
			} else {
				if mostLikelySet.adjustedDuration(ppms) < bestSet.adjustedDuration(ppms) {
					bestSet = mostLikelySet
				}
			}
		}
	}

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
