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

// DOWNLOAD CODE IMPROVEMENTS:
//
// the current design of the download algorithm has some key places where
// there's still room for improvement. Below we list some of the ideas that
// could contribute to a faster and more robust algorithm.
//
// 1. add a fixed cost to account for our own bandwidth expenses: because the
// host network on Sia possibly has a lot of very cheap hosts, we should be
// offsetting the cost with a fixed cost to account for our own bandwidth
// expenses. E.g. if we use a fixed cost of 2$/TB, so ~100SC, then a worker that
// cost 1SC becomes 101SC and a worker that costs 100SC becomes 200SC. That
// makes it so the difference between those workers is 2x instead of the 100x it
// was before the fixed cost.
//
// 2. add a mechanism to slow down the DTs or switch to different DTs if we have
// too many jobs launched at once: the distribution tracker does not take into
// account worker load, that means they're chance values become too optimistic,
// which might hurt the performance of the algorithm when not taken into
// account.
//
// 3. play with the 50% number, and account for the cost of being unlucky: a
// worker set's total chance has to be greater than 50% in order for it be
// accepted as a viable worker set, this 50% number is essentially arbitry, and
// even at 50% there's still a 50% chance we fall on the other side of the
// fence. This should/could be taken into account.
//
// 4. fix the algorithm that chooses which worker to replace in your set: the
// algorithm that decides what worker to replace by a cheaper worker in the
// current working set is a bit naievely implemented. Figuring out what worker
// to replace can be a very complex algorithm. There's definitely room for
// improvement here.
//
// 5. fix the algorithm that constructs chimeras: chimeras are currently
// constructed for every bucket duration, however we could also rebuild
// chimeras, or partially rebuild chimeras, when we are swapping out cheaper
// workers in the working set. The currently algorithm considers the chimera
// worker and its cost as fixed, but that does not necessarily have to be the
// case. We can further improve this by swapping out series of workers for
// cheaper workers inside of the chimera itself.

var (
	// maxWaitUnresolvedWorkerUpdate defines the maximum amount of time we want
	// to wait for unresolved workers to become resolved before trying to
	// recreate the worker set.
	maxWaitUnresolvedWorkerUpdate = 25 * time.Millisecond

	// maxWaitUpdateWorkers defines the maximum amount of time we want to wait
	// for workers to be updated.
	maxWaitUpdateWorkers = 25 * time.Millisecond

	// chimeraAvailabilityRateThreshold defines the number that much be reached
	// when composing a chimera from unresolved workers. If the sum of the
	// availability rate of each worker reaches this number we can build a
	// chimera out of them.
	chimeraAvailabilityRateThreshold = float64(2)
)

// NOTE: all of the following defined types are used by the PDC, which is
// inherently thread un-safe, that means that these types don't not need to be
// thread safe either. If fields are marked `static` it is meant to signal they
// won't change after being set.
type (
	// downloadWorker is an interface implemented by both the individual and
	// chimera workers that represents a worker that can be used for downloads.
	downloadWorker interface {
		// completeChanceCached returns the chance this download worker
		// completes a read within the duration that corresponds to the given
		// bucket index. This value is cached and recomputed for every bucket
		// duration.
		completeChanceCached() float64

		// cost returns the expected job cost for downloading a piece. If the
		// worker has already been launched, its cost will be zero.
		cost() float64

		// getPieceForDownload returns the piece to download next
		getPieceForDownload() uint64

		// identifier returns a unique identifier for the download worker, this
		// identifier can be used as a key when mapping the download worker
		identifier() string

		// markPieceForDownload allows specifying what piece to download for
		// this worker in the case the worker resolved multiple pieces
		markPieceForDownload(pieceIndex uint64)

		// pieces returns all piece indices this worker can resolve
		pieces(pdc *projectDownloadChunk) []uint64

		// worker returns the underlying worker
		worker() *worker
	}

	// chimeraWorker is a worker that's built from unresolved workers until the
	// chance it has a piece is exactly 1. At that point we can treat a chimera
	// worker exactly the same as a resolved worker in the download algorithm
	// that constructs the best worker set.
	chimeraWorker struct {
		// staticChanceComplete is the chance this worker completes after the
		// duration at which this chimera worker was built.
		staticChanceComplete float64

		// staticCost returns the cost of the chimera worker, which is the
		// average cost taken across all workers this chimera worker is
		// comprised of, it is static because it never gets updated after the
		// chimera is finalized and this field is calculated
		staticCost float64

		// staticIdentifier uniquely identifies the chimera worker
		staticIdentifier string
	}

	// individualWorker is a struct that represents a single worker object, both
	// resolved and unresolved workers in the pdc can be represented by an
	// individual worker. An individual worker can be used to build a chimera
	// worker with.
	//
	// NOTE: extending this struct requires an update to the `split` method.
	individualWorker struct {
		// the following fields are cached and recalculated at precise times
		// within the download code algorithm
		cachedCompleteChance float64
		cachedLookupIndex    int
		cachedReadDTChances  skymodules.Chances

		// the following fields are continuously updated on the worker
		pieceIndices []uint64
		resolved     bool

		// currentPiece is the piece that was marked by the download algorithm
		// as the piece to download next, this is used to ensure that workers
		// in the worker are not selected for duplicate piece indices.
		currentPiece           uint64
		currentPieceLaunchedAt time.Time

		staticAvailabilityRate   float64
		staticCost               float64
		staticDownloadLaunchTime time.Time
		staticIdentifier         string
		staticLookupDistribution *skymodules.Distribution
		staticReadDistribution   *skymodules.Distribution
		staticWorker             *worker
	}

	// unresolvedWorkerInfo is a subset of the individualWorker, containing all
	// necessary pieces of information to build a chimera worker with
	unresolvedWorkerInfo struct {
		staticAvailabilityRate float64
		staticCompleteChance   float64
		staticCost             float64
	}

	// workerSet is a collection of workers that may or may not have been
	// launched yet in order to fulfil a download.
	workerSet struct {
		workers []downloadWorker

		staticBucketDuration time.Duration
		staticBucketIndex    int
		staticMinPieces      int
		staticNumOverdrive   int

		staticPDC *projectDownloadChunk
	}

	// coinflips is a collection of chances where every item is the chance the
	// coin will turn up heads. We use the concept of a coin because it allows
	// to more easily reason about chance calculations.
	coinflips []float64
)

// NewChimeraWorker returns a new chimera worker object.
func NewChimeraWorker(workers []*unresolvedWorkerInfo) *chimeraWorker {
	// calculate cost, complete chance and both distributions using the given
	// workers, making sure every worker contributes to the total in relation to
	// its weight being the availability rate
	var totalCompleteChance float64
	var totalCost float64
	for _, w := range workers {
		totalCompleteChance += w.staticCompleteChance * w.staticAvailabilityRate
		totalCost += w.staticCost
	}

	chance := totalCompleteChance / float64(len(workers))
	cost := totalCost / float64(len(workers))

	return &chimeraWorker{
		staticChanceComplete: chance,
		staticCost:           cost,
		staticIdentifier:     hex.EncodeToString(fastrand.Bytes(16)),
	}
}

// cost returns the cost for this chimera worker, this method can only be called
// on a chimera that is finalized
func (cw *chimeraWorker) cost() float64 {
	return cw.staticCost
}

// completeChanceCached returns the chance this chimera completes
func (cw *chimeraWorker) completeChanceCached() float64 {
	return cw.staticChanceComplete
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
	return cw.staticIdentifier
}

// markPieceForDownload takes a piece index and marks it as the piece to
// download for this worker. In the case of a chimera worker this method is
// essentially a no-op since chimera workers are never launched
func (cw *chimeraWorker) markPieceForDownload(pieceIndex uint64) {
	// this is a no-op
}

// pieces implements the downloadWorker interface, chimera workers return all
// pieces as we don't know yet what pieces they can resolve
func (cw *chimeraWorker) pieces(pdc *projectDownloadChunk) []uint64 {
	return pdc.staticPieceIndices
}

// worker implements the downloadWorker interface, chimera workers return nil
// since it's comprised of multiple workers
func (cw *chimeraWorker) worker() *worker {
	return nil
}

// cost implements the downloadWorker interface.
func (iw *individualWorker) cost() float64 {
	// workers that have already been launched have a zero cost
	if iw.isLaunched() {
		return 0
	}
	return iw.staticCost
}

func (iw *individualWorker) calculateDistributionChances() {
	if !iw.isResolved() {
		// if the worker is not resolved yet, we want to always shift the lookup
		// DT by the time since launch
		dur := time.Since(iw.staticDownloadLaunchTime)
		lookupDT := iw.staticLookupDistribution.Clone()
		lookupDT.Shift(dur)
		lookupDTExpectedDur := lookupDT.ExpectedDuration()

		iw.cachedLookupIndex = skymodules.DistributionBucketIndexForDuration(lookupDTExpectedDur)
		iw.cachedReadDTChances = iw.staticReadDistribution.ChancesAfter()
		return
	}

	// if the worker is resolved, and launched, we want to shift the read DT by
	// the time since the worker got launched
	if iw.isLaunched() {
		dur := time.Since(iw.currentPieceLaunchedAt)
		readDT := iw.staticReadDistribution.Clone()
		readDT.Shift(dur)
		iw.cachedReadDTChances = readDT.ChancesAfter()
	} else {
		iw.cachedReadDTChances = iw.staticReadDistribution.ChancesAfter()
	}
}

// calculateCompleteChance calculates the chance this worker completes at given
// index. This chance is a combination of the chance it resolves and the chance
// it completes the read by the given index. The resolve (or lookup) chance only
// plays a part for workers that have not resolved yet.
//
// This function calculates the complete chance by approximation, meaning if we
// request the chance at index 100 (or 200ms), for an unresolved worker, we
// calculate the expected resolve index, let's say it's 20, and offset the read
// chance by that number, meaning we will return the read chance at index 80.
func (iw *individualWorker) calculateCompleteChance(index int) {
	// if the worker is resolved, the complete chance is simply the read chance
	// at given index
	if iw.isResolved() {
		iw.cachedCompleteChance = iw.cachedReadDTChances[index]
		return
	}

	// if the given index is less than the lookup index, it means the lookup is
	// expected to not be completed yet, in which case the complete chance is 0
	if iw.cachedLookupIndex >= index {
		iw.cachedCompleteChance = 0
		return
	}

	// if the given index is greater than the lookup index, we return the read
	// chance offset by the lookup index, this essentially means that if the
	// lookup is expected to complete by index 10, and the requested index is
	// 30, we return the chance the read completes at index 20.
	iw.cachedCompleteChance = iw.cachedReadDTChances[index-iw.cachedLookupIndex]
}

// completeChanceCached returns the chance this worker will complete
func (iw *individualWorker) completeChanceCached() float64 {
	return iw.cachedCompleteChance
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
	return !iw.currentPieceLaunchedAt.IsZero()
}

// isResolved returns whether this individual worker has resolved.
func (iw *individualWorker) isResolved() bool {
	return iw.resolved
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
			build.Critical(fmt.Sprintf("markPieceForDownload is marking a piece that is not present in the worker's piece indices, %v does not include %v", iw.pieceIndices, pieceIndex))
		}
	}
	iw.currentPiece = pieceIndex
}

// pieces implements the downloadWorker interface.
func (iw *individualWorker) pieces(_ *projectDownloadChunk) []uint64 {
	return iw.pieceIndices
}

// worker implements the downloadWorker interface.
func (iw *individualWorker) worker() *worker {
	return iw.staticWorker
}

// split will split the download worker into two workers, the first worker will
// have the given availability, the second worker will have the remainder as its
// availability rate value.
func (iwi *unresolvedWorkerInfo) split(availability float64) (*unresolvedWorkerInfo, *unresolvedWorkerInfo) {
	if availability >= iwi.staticAvailabilityRate {
		build.Critical("chance value on which we split should be strictly less than the worker's resolve chance")
		return nil, nil
	}

	main := &unresolvedWorkerInfo{
		staticAvailabilityRate: availability,
		staticCompleteChance:   iwi.staticCompleteChance,
		staticCost:             iwi.staticCost,
	}

	remainder := &unresolvedWorkerInfo{
		staticAvailabilityRate: iwi.staticAvailabilityRate - availability,
		staticCompleteChance:   iwi.staticCompleteChance,
		staticCost:             iwi.staticCost,
	}

	return main, remainder
}

// clone returns a shallow copy of the worker set.
func (ws *workerSet) clone() *workerSet {
	return &workerSet{
		workers: append([]downloadWorker{}, ws.workers...),

		staticBucketDuration: ws.staticBucketDuration,
		staticBucketIndex:    ws.staticBucketIndex,
		staticMinPieces:      ws.staticMinPieces,
		staticNumOverdrive:   ws.staticNumOverdrive,

		staticPDC: ws.staticPDC,
	}
}

// cheaperSetFromCandidate returns a new worker set if the given candidate
// worker can improve the cost of the worker set. The worker that is being
// swapped by the candidate is the most expensive worker possible, which is not
// necessarily the most expensive worker in the set because we have to take into
// account the pieces the worker can download.
func (ws *workerSet) cheaperSetFromCandidate(candidate downloadWorker) *workerSet {
	// convenience variables
	pdc := ws.staticPDC

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
		wCostI := byCostDesc[i].cost()
		wCostJ := byCostDesc[j].cost()
		return wCostI > wCostJ
	})

	// range over the workers
	swapIndex := -1
LOOP:
	for _, w := range byCostDesc {
		// if the candidate is not cheaper than this worker we can stop looking
		// to build a cheaper set since the workers are sorted by cost
		if candidate.cost() > w.cost() {
			break
		}

		// if the current worker is launched, don't swap it out
		expensiveWorkerPiece, launched, _ := pdc.currentDownload(w)
		if launched {
			continue
		}

		// if the current worker is a chimera worker, and we're cheaper, swap
		expensiveWorkerIndex := originalIndexMap[w.identifier()]
		if _, ok := w.(*chimeraWorker); ok {
			swapIndex = expensiveWorkerIndex
			break LOOP
		}

		// range over the candidate's pieces and see whether we can swap
		for _, piece := range candidate.pieces(pdc) {
			// if the candidate can download the same piece as the expensive
			// worker, swap it out because it's cheaper
			if piece == expensiveWorkerPiece {
				swapIndex = expensiveWorkerIndex
				break LOOP
			}

			// if the candidate can download a piece that is currently not being
			// downloaded by anyone else, swap it as well
			_, workerForPiece := piecesToIndexMap[piece]
			if !workerForPiece {
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
	var totalCost float64
	for _, w := range ws.workers {
		totalCost += w.cost()
	}

	// calculate the cost penalty using the given price per ms and apply it to
	// the worker set's expected duration.
	totalCostCurr := types.NewCurrency64(uint64(totalCost))
	return addCostPenalty(ws.staticBucketDuration, totalCostCurr, ppms)
}

// chancesAfter is a small helper function that returns a list of every worker's
// chance it's completed after the given duration.
func (ws *workerSet) chancesAfter() coinflips {
	chances := make(coinflips, len(ws.workers))
	for i, w := range ws.workers {
		chances[i] = w.completeChanceCached()
	}
	return chances
}

// chanceGreaterThanHalf returns whether the total chance this worker set
// completes the download before the given duration is more than 50%.
//
// NOTE: this function abstracts the chance a worker resolves after the given
// duration as a coinflip to make it easier to reason about the problem given
// that the workerset consists out of one or more overdrive workers.
func (ws *workerSet) chanceGreaterThanHalf() bool {
	// convert every worker into a coinflip
	coinflips := ws.chancesAfter()

	var chance float64
	switch ws.staticNumOverdrive {
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

// String returns a string representation of the worker set
func (ws *workerSet) String() string {
	output := fmt.Sprintf("WORKERSET bucket: %v bucket dur %v expected dur: %v num overdrive: %v \nworkers:\n", ws.staticBucketIndex, skymodules.DistributionDurationForBucketIndex(ws.staticBucketIndex), ws.staticBucketDuration, ws.staticNumOverdrive)
	for i, w := range ws.workers {
		_, chimera := w.(*chimeraWorker)
		selected := -1
		if !chimera {
			selected = int(w.getPieceForDownload())
		}

		chance := w.completeChanceCached()
		output += fmt.Sprintf("%v) worker: %v chimera: %v chance: %v cost: %v pieces: %v selected: %v\n", i+1, w.identifier(), chimera, chance, w.cost(), w.pieces(ws.staticPDC), selected)
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
func (pdc *projectDownloadChunk) updateWorkers(workers []*individualWorker) {
	ws := pdc.workerState
	ws.mu.Lock()
	defer ws.mu.Unlock()

	// make a map of all resolved workers to their piece indices
	resolved := make(map[string][]uint64, len(workers))
	for _, rw := range ws.resolvedWorkers {
		resolved[rw.worker.staticHostPubKeyStr] = rw.pieceIndices
	}

	// loop over all workers and update the resolved status and piece indices
	for _, w := range workers {
		pieceIndices, resolved := resolved[w.staticWorker.staticHostPubKeyStr]
		if !w.isResolved() && resolved {
			w.resolved = true
			w.pieceIndices = pieceIndices
		}

		// recalculate the distributions
		w.calculateDistributionChances()
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
	numPieces := ec.NumPieces()

	// add all resolved workers that are deemed good for downloading
	for _, rw := range ws.resolvedWorkers {
		jrq := rw.worker.staticJobReadQueue
		rdt := jrq.staticStats.distributionTrackerForLength(length)
		jhsq := rw.worker.staticJobHasSectorQueue
		ldt := jhsq.staticDT

		if !isGoodForDownload(rw.worker, rw.pieceIndices) {
			continue
		}

		cost, _ := jrq.callExpectedJobCost(length).Float64()
		workers = append(workers, &individualWorker{
			resolved:     true,
			pieceIndices: rw.pieceIndices,

			staticCost:               cost,
			staticIdentifier:         rw.worker.staticHostPubKey.ShortString(),
			staticLookupDistribution: ldt.Distribution(0).Clone(),
			staticReadDistribution:   rdt.Distribution(0).Clone(),
			staticWorker:             rw.worker,
		})

	}

	// add all unresolved workers that are deemed good for downloading
	for _, uw := range ws.unresolvedWorkers {
		w := uw.staticWorker
		jrq := w.staticJobReadQueue
		rdt := jrq.staticStats.distributionTrackerForLength(length)
		jhsq := w.staticJobHasSectorQueue
		ldt := jhsq.staticDT

		// unresolved workers can still have all pieces
		pieceIndices := make([]uint64, ec.MinPieces())
		for i := 0; i < len(pieceIndices); i++ {
			pieceIndices[i] = uint64(i)
		}

		// exclude workers that are not useful
		if !isGoodForDownload(w, pieceIndices) {
			continue
		}

		cost, _ := jrq.callExpectedJobCost(length).Float64()
		workers = append(workers, &individualWorker{
			pieceIndices: pieceIndices,
			resolved:     false,

			staticAvailabilityRate:   jhsq.callAvailabilityRate(numPieces),
			staticCost:               cost,
			staticIdentifier:         w.staticHostPubKey.ShortString(),
			staticLookupDistribution: ldt.Distribution(0).Clone(),
			staticReadDistribution:   rdt.Distribution(0).Clone(),
			staticWorker:             w,
		})

	}

	return workers
}

// currentDownload returns the piece that was marked on the worker to download
// next, alongside two booleans that indicate whether it was launched and
// whether it completed.
func (pdc *projectDownloadChunk) currentDownload(w downloadWorker) (uint64, bool, bool) {
	// return defaults if the worker is a chimera worker, those are not
	// downloading by definition
	iw, ok := w.(*individualWorker)
	if !ok {
		return 0, false, false
	}

	// get the marked piece for this worker
	currentPiece := w.getPieceForDownload()

	// fetch the worker's download progress, if that does not exist, it's
	// neither launched nor completed.
	workerProgress, exists := pdc.workerProgress[iw.identifier()]
	if !exists {
		return currentPiece, false, false
	}

	_, launched := workerProgress.launchedPieces[currentPiece]
	_, completed := workerProgress.completedPieces[currentPiece]
	return currentPiece, launched, completed
}

// launchWorkerSet will range over the workers in the given worker set and will
// try to launch every worker that has not yet been launched and is ready to
// launch.
func (pdc *projectDownloadChunk) launchWorkerSet(ws *workerSet) bool {
	// convenience variables
	minPieces := pdc.workerSet.staticErasureCoder.MinPieces()

	// range over all workers in the set and launch if possible
	var workerLaunched bool
	for _, w := range ws.workers {
		// continue if the worker is a chimera worker
		_, ok := w.(*chimeraWorker)
		if ok {
			continue
		}

		// continue if the worker is already launched
		piece, launched, _ := pdc.currentDownload(w)
		if launched {
			continue
		}

		// launch the worker
		isOverdrive := len(pdc.launchedWorkers) >= minPieces
		cost := w.cost()
		expectedCompleteTime, launched := pdc.launchWorker(w, piece, isOverdrive)

		// debugging
		if launched {
			workerLaunched = true
			if span := opentracing.SpanFromContext(pdc.ctx); span != nil {
				span.LogKV(
					"aWorkerLaunched", w.identifier(),
					"piece", piece,
					"cost", cost,
					"overdriveWorker", isOverdrive,
					"expectedDuration", time.Until(expectedCompleteTime),
					"chanceAfterDur", w.completeChanceCached(),
					"wsDuration", ws.staticBucketDuration,
					"wsIndex", ws.staticBucketIndex,
				)
			}
		}
	}

	// debugging
	if workerLaunched {
		if span := opentracing.SpanFromContext(pdc.ctx); span != nil {
			span.LogKV("launchedWorkerSet", ws)
		}
	}
	return workerLaunched
}

// threadedLaunchProjectDownload performs the main download loop, every
// iteration we update the pdc's available pieces, construct a new worker set
// and launch every worker that can be launched from that set. Every iteration
// we check whether the download was finished.
func (pdc *projectDownloadChunk) threadedLaunchProjectDownload() {
	// grab some variables
	ws := pdc.workerState
	ec := pdc.workerSet.staticErasureCoder

	// grab the workers from the pdc, every iteration we will update this set of
	// workers to avoid needless performing gouging checks on every iteration
	start := time.Now()
	workers := pdc.workers()
	elpased := time.Since(start)
	if span := opentracing.SpanFromContext(pdc.ctx); span != nil {
		span.LogKV("pdc.workers()", elpased)
	}

	// verify we have enough workers to complete the download
	if len(workers) < ec.MinPieces() {
		pdc.fail(errors.Compose(ErrRootNotFound, errors.AddContext(errNotEnoughWorkers, fmt.Sprintf("%v < %v", len(workers), ec.MinPieces()))))
		return
	}

	// register for a worker update chan
	workerUpdateChan := ws.managedRegisterForWorkerUpdate()

	// TODO: remove (debug purposes)
	var workerSet *workerSet

	prevLog := time.Now()
	prevWorkerUpdate := time.Now()

	for {
		if span := opentracing.SpanFromContext(pdc.ctx); span != nil && time.Since(prevLog) > 100*time.Millisecond {
			span.LogKV(
				"downloadLoopIter", time.Since(pdc.staticLaunchTime),
				"currWorkerSet", workerSet,
			)
			prevLog = time.Now()
		}

		// update the pieces
		updated := pdc.updatePieces()

		// update the workers
		if updated || time.Since(prevWorkerUpdate) > maxWaitUpdateWorkers {
			pdc.updateWorkers(workers)
			prevWorkerUpdate = time.Now()
		}

		// create a worker set and launch it
		workerSet, err := pdc.createWorkerSet(workers)
		if err != nil {
			pdc.fail(err)
			return
		}
		if workerSet != nil {
			pdc.launchWorkerSet(workerSet)
		}

		// iterate
		select {
		case <-time.After(maxWaitUnresolvedWorkerUpdate):
			// recreate the workerset after maxwait
		case <-workerUpdateChan:
			// replace the worker update channel
			workerUpdateChan = ws.managedRegisterForWorkerUpdate()
		case jrr := <-pdc.workerResponseChan:
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
			pdc.fail(errors.New("download timed out"))
			return
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
	}
}

// createWorkerSet tries to create a worker set from the pdc's resolved and
// unresolved workers, the maximum amount of overdrive workers in the set is
// defined by the given 'maxOverdriveWorkers' argument.
func (pdc *projectDownloadChunk) createWorkerSet(workers []*individualWorker) (*workerSet, error) {
	// convenience variables
	ppms := pdc.pricePerMS
	minPieces := pdc.workerSet.staticErasureCoder.MinPieces()

	// loop state
	var bestSet *workerSet

	// can't create a workerset without download workers
	if len(workers) == 0 {
		return nil, nil
	}

OUTER:
	for numOverdrive := 0; numOverdrive <= maxOverdriveWorkers; numOverdrive++ {
		workersNeeded := minPieces + numOverdrive
		for bI := 0; bI < skymodules.DistributionTrackerTotalBuckets; bI += 4 {
			// exit early if ppms in combination with the bucket duration
			// already exceeds the adjusted cost of the current best set,
			// workers would be too slow by definition
			bDur := skymodules.DistributionDurationForBucketIndex(bI)
			if bestSet != nil && bDur > bestSet.adjustedDuration(ppms) {
				break OUTER
			}

			// recalculate the complete chance at given index
			for _, w := range workers {
				w.calculateCompleteChance(bI)
			}

			// build the download workers
			downloadWorkers := pdc.buildDownloadWorkers(workers)

			// divide the workers in most likely and less likely
			mostLikely, lessLikely := pdc.splitMostlikelyLessLikely(downloadWorkers, workersNeeded, numOverdrive)

			// if there aren't even likely workers, escape early
			if len(mostLikely) == 0 {
				break OUTER
			}

			// build the most likely set
			mostLikelySet := &workerSet{
				workers: mostLikely,

				staticBucketDuration: bDur,
				staticBucketIndex:    bI,
				staticNumOverdrive:   numOverdrive,
				staticMinPieces:      minPieces,

				staticPDC: pdc,
			}

			// if the chance of the most likely set does not exceed 50%, it is
			// not high enough to continue, no need to continue this iteration,
			// we need to try a slower and thus more likely bucket
			if !mostLikelySet.chanceGreaterThanHalf() {
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
				if !cheaperSet.chanceGreaterThanHalf() {
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

// buildChimeraWorkers turns a list of individual workers into chimera workers.
func (pdc *projectDownloadChunk) buildChimeraWorkers(unresolvedWorkers []*unresolvedWorkerInfo) []downloadWorker {
	// sort workers by chance they complete
	sort.Slice(unresolvedWorkers, func(i, j int) bool {
		eRTI := unresolvedWorkers[i].staticCompleteChance
		eRTJ := unresolvedWorkers[j].staticCompleteChance
		return eRTI > eRTJ
	})

	// create an array that will hold all chimera workers
	chimeras := make([]downloadWorker, 0, len(unresolvedWorkers))

	// create some loop state
	currAvail := float64(0)
	start := 0

	// loop over the unresolved workers
	for curr := 0; curr < len(unresolvedWorkers); curr++ {
		currAvail += unresolvedWorkers[curr].staticAvailabilityRate
		if currAvail >= chimeraAvailabilityRateThreshold {
			end := curr + 1
			chimera := NewChimeraWorker(unresolvedWorkers[start:end])
			chimeras = append(chimeras, chimera)

			// reset
			start = end
			currAvail = 0

			// debug
			pdc.callNewChimeraWorker++
		}
	}

	// NOTE: we don't add the current as it's not a complete chimera worker

	return chimeras
}

// buildDownloadWorkers is a helper function that takes a list of individual
// workers and turns them into download workers.
func (pdc *projectDownloadChunk) buildDownloadWorkers(workers []*individualWorker) []downloadWorker {
	// create an array of download workers
	downloadWorkers := make([]downloadWorker, 0, len(workers))

	// split the workers into resolved and unresolved workers, the resolved
	// workers can be added directly to the array of download workers
	resolvedWorkers, unresolvedWorkers := splitResolvedUnresolved(workers)
	downloadWorkers = append(downloadWorkers, resolvedWorkers...)

	// the unresolved workers are used to build chimeras with
	chimeraWorkers := pdc.buildChimeraWorkers(unresolvedWorkers)
	return append(downloadWorkers, chimeraWorkers...)
}

// splitMostlikelyLessLikely takes a list of download workers alongside a
// duration and an amount of workers that are needed for the most likely set of
// workers to compplete a download (this is not necessarily equal to 'minPieces'
// workers but also takes into account an amount of overdrive workers). This
// method will split the given workers array in a list of most likely workers,
// and a list of less likely workers.
func (pdc *projectDownloadChunk) splitMostlikelyLessLikely(workers []downloadWorker, workersNeeded, numOverdriveWorkers int) ([]downloadWorker, []downloadWorker) {
	// calculate the less likely cap, taking into account underflow
	var lessLikelyCap int
	if len(workers) > workersNeeded {
		lessLikelyCap = len(workers) - workersNeeded
	}

	// prepare two slices that hold the workers which are most likely and the
	// ones that are less likely
	mostLikely := make([]downloadWorker, 0, workersNeeded)
	lessLikely := make([]downloadWorker, 0, lessLikelyCap)

	// define some state variables to ensure we select workers in a way the
	// pieces are unique and we are not using a worker twice
	numPieces := pdc.workerSet.staticErasureCoder.NumPieces()
	pieces := make(map[uint64]struct{}, numPieces)
	added := make(map[string]struct{}, 0)

	// add worker is a helper function that adds a worker to either the most
	// likely or less likely worker array and updates our state variables
	addWorker := func(w downloadWorker, pieceIndex uint64) {
		if len(mostLikely) < workersNeeded {
			mostLikely = append(mostLikely, w)
		} else {
			lessLikely = append(lessLikely, w)
		}

		added[w.identifier()] = struct{}{}
		pieces[pieceIndex] = struct{}{}
		w.markPieceForDownload(pieceIndex)
	}

	// sort the workers by percentage chance they complete after the current
	// bucket duration, essentially sorting them from most to least likely
	sort.Slice(workers, func(i, j int) bool {
		chanceI := workers[i].completeChanceCached()
		chanceJ := workers[j].completeChanceCached()
		return chanceI > chanceJ
	})

	// loop over the workers and try to add them
	for _, w := range workers {
		// do not reuse the same worker twice
		_, added := added[w.identifier()]
		if added {
			continue
		}

		// workers that have in-progress downloads are re-added as long as we
		// don't already have a worker for the piece they are downloading
		currPiece, launched, completed := pdc.currentDownload(w)
		if launched && !completed {
			_, exists := pieces[currPiece]
			if !exists {
				addWorker(w, currPiece)
				continue
			}
		}

		// loop the worker's pieces to see whether it can download a piece for
		// which we don't have a worker yet or which we haven't downloaded yet
		for _, pieceIndex := range w.pieces(pdc) {
			if pdc.piecesInfo[pieceIndex].downloaded {
				continue
			}

			_, exists := pieces[pieceIndex]
			if exists {
				continue
			}

			addWorker(w, pieceIndex)
			break // only use a worker once
		}
	}

	// if we have enough likely workers we can return
	if len(mostLikely) == workersNeeded {
		return mostLikely, lessLikely
	}

	// if we don't have enough likely workers, and we want to overdrive, we can
	// select 'numOverdriveWorkers' to overdrive on pieces for which we already
	// have a worker.
	//
	// this is mostly important for base setor downloads, if the worker set
	// consisted only out of unique pieces, we would never overdrive on base
	// sector downloads
	if numOverdriveWorkers > 0 {
		for _, w := range workers {
			// break if we've reached the amount of workers needed
			if len(mostLikely) == workersNeeded {
				break
			}

			// don't re-use workers
			_, added := added[w.identifier()]
			if added {
				continue
			}

			// loop over the worker's pieces and break to ensure we use it once
			for _, pieceIndex := range w.pieces(pdc) {
				addWorker(w, pieceIndex)
				break
			}
		}
	}

	return mostLikely, lessLikely
}

// isGoodForDownload is a helper function that returns true if and only if the
// worker meets a certain set of criteria that make it useful for downloads.
// It's only useful if it is not on any type of cooldown, if it's async ready
// and if it's not price gouging.
func isGoodForDownload(w *worker, pieces []uint64) bool {
	// workers that can't download any pieces are ignored
	if len(pieces) == 0 {
		return false
	}

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

// splitResolvedUnresolved is a helper function that splits the given workers
// into resolved and unresolved worker arrays.
func splitResolvedUnresolved(workers []*individualWorker) ([]downloadWorker, []*unresolvedWorkerInfo) {
	resolvedWorkers := make([]downloadWorker, 0, len(workers))
	unresolvedWorkers := make([]*unresolvedWorkerInfo, 0, len(workers))

	for _, w := range workers {
		if w.isResolved() {
			resolvedWorkers = append(resolvedWorkers, w)
		} else if w.cachedCompleteChance > 0 {
			unresolvedWorkers = append(unresolvedWorkers, &unresolvedWorkerInfo{
				staticAvailabilityRate: w.staticAvailabilityRate,
				staticCompleteChance:   w.cachedCompleteChance,
				staticCost:             w.staticCost,
			})
		}
	}
	return resolvedWorkers, unresolvedWorkers
}
