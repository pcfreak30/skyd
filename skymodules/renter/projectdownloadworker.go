package renter

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/types"
)

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
		// of time since it was launched. If the worker has already been
		// launched, its distribution will have been shifted by the time since
		// it was launched.
		distribution() *skymodules.Distribution

		// pieces returns all piece indices this worker can resolve, chimera
		// workers return nil since we don't know yet what pieces they can
		// resolve
		pieces() []uint64

		// worker returns the underlying worker, chimera workers return nil
		// since it's comprised of multiple workers
		worker() *worker
	}

	// chimeraWorker is a worker that's built from unresolved workers until the
	// chance it has a piece is exactly 1. At that point we can treat a chimera
	// worker exactly the same as a resolved worker in the download algorithm
	// that constructs the best worker set.
	chimeraWorker struct {
		// cachedCost returns the average expected job cost of this chimera
		// worker, it is cached meaning it will only be calculated the first
		// time cost is requested after the chimera worker was finalized.
		cachedCost types.Currency

		// cachedDistribution contains a distribution that is the weighted
		// combination of all worker distrubtions in this chimera worker, it is
		// cached meaning it will only be calculated the first time the
		// distribution is requested after the chimera worker was finalized.
		cachedDistribution *skymodules.Distribution

		distributions []*skymodules.Distribution
		weights       []float64
		workers       []*worker
	}

	// individualWorker is a struct that represents a single worker object, both
	// resolved and unresolved workers in the pdc can be represented by an
	// individual worker. An individual worker can be used to build a chimera
	// worker with.
	//
	// NOTE: extending this struct requires an update to the `split` method.
	individualWorker struct {
		staticLaunchedAt       time.Time
		staticPieceIndices     []uint64
		staticResolveChance    float64
		staticReadDistribution *skymodules.Distribution
		staticWorker           *worker
	}

	// workerSet is a collection of workers that may or may not have been
	// launched yet in order to fulfil a download.
	workerSet struct {
		workers []downloadWorker

		staticExpectedDuration time.Duration
		staticLength           uint64
		staticMinPieces        int
	}
)

// NewChimeraWorker returns a new chimera worker object.
func NewChimeraWorker() *chimeraWorker {
	return &chimeraWorker{
		distributions: make([]*skymodules.Distribution, 0),
		weights:       make([]float64, 0),
		workers:       make([]*worker, 0),
	}
}

// addWorker adds the given worker to the chimera worker.
func (cw *chimeraWorker) addWorker(w *individualWorker) (remainder *individualWorker) {
	// calculate the remaining chance this chimera worker needs to be complete
	remaining := float64(1)
	for _, weight := range cw.weights {
		remaining -= weight
	}
	if remaining == 0 {
		return w
	}

	// the given worker's chance can be higher than the remaining chance of this
	// chimera worker, in that case we have to split the worker in a part we
	// want to add, and a remainder we'll use to build the next chimera with
	toAdd := w
	if w.staticResolveChance > remaining {
		toAdd, remainder = w.split(remaining)
	}

	// add the worker to the chimera
	cw.distributions = append(cw.distributions, toAdd.staticReadDistribution)
	cw.weights = append(cw.weights, toAdd.staticResolveChance)
	cw.workers = append(cw.workers, toAdd.staticWorker)
	return
}

// cost implements downloadWorker interface.
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

// distribution implements downloadWorker interface.
func (cw *chimeraWorker) distribution() *skymodules.Distribution {
	if cw.cachedDistribution == nil {
		dt := skymodules.NewDistributionTrackerStandard()
		cw.cachedDistribution = dt.Distribution(0)
		for i, distribution := range cw.distributions {
			cw.cachedDistribution.MergeWith(distribution, cw.weights[i])
		}
	}
	return cw.cachedDistribution
}

// pieces implements downloadWorker interface.
func (cw *chimeraWorker) pieces() []uint64 {
	return nil
}

// worker implements downloadWorker interface.
func (cw *chimeraWorker) worker() *worker {
	return nil
}

// cost implements downloadWorker interface.
func (iw *individualWorker) cost(length uint64) types.Currency {
	if iw.staticLaunchedAt != (time.Time{}) {
		return types.ZeroCurrency
	}
	return iw.staticWorker.staticJobReadQueue.callExpectedJobCost(length)
}

// distribution implements downloadWorker interface.
func (iw *individualWorker) distribution() *skymodules.Distribution {
	if iw.staticLaunchedAt != (time.Time{}) {
		// we always perform the shift on a clone of the worker's distribution,
		// this to ensure we don't keep shifting the same distribution
		clone := iw.staticReadDistribution.Clone()
		clone.Shift(time.Since(iw.staticLaunchedAt))
		return clone
	}
	return iw.staticReadDistribution
}

// pieces implements downloadWorker interface.
func (iw *individualWorker) pieces() []uint64 {
	return iw.staticPieceIndices
}

// worker implements downloadWorker interface.
func (iw *individualWorker) worker() *worker {
	return iw.staticWorker
}

// split will split the download worker into two workers, the first worker will
// have the given chance, the second worker will have the remainder as its
// chance value.
func (iw *individualWorker) split(chance float64) (*individualWorker, *individualWorker) {
	if chance >= iw.staticResolveChance {
		build.Critical("chance value on which we split should be strictly less than the worker's resolve chance")
		return nil, nil
	}

	main := &individualWorker{
		staticLaunchedAt:       iw.staticLaunchedAt,
		staticPieceIndices:     iw.staticPieceIndices,
		staticResolveChance:    chance,
		staticReadDistribution: iw.staticReadDistribution,
		staticWorker:           iw.staticWorker,
	}
	remainder := &individualWorker{
		staticLaunchedAt:       iw.staticLaunchedAt,
		staticPieceIndices:     iw.staticPieceIndices,
		staticResolveChance:    iw.staticResolveChance - chance,
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
	// build a map of what piece is fetched by what worker
	pieces := make(map[uint64]string)
	indices := make(map[string]int)
	for i, w := range ws.workers {
		worker := w.worker().staticHostPubKeyStr
		indices[worker] = i
		for _, piece := range w.pieces() {
			pieces[piece] = worker
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
		expensiveWorkerKey := expensiveWorker.worker().staticHostPubKeyStr

		// if the candidate is not cheaper than this worker we can stop looking
		// to build a cheaper set since the workers are sorted by cost
		if candidate.cost(ws.staticLength).Cmp(expensiveWorker.cost(ws.staticLength)) >= 0 {
			break
		}

		// the candidate is only useful if it can replace a worker and
		// contribute pieces to the worker set for which we don't already have
		// another worker
		for _, piece := range candidate.pieces() {
			existingWorkerKey, exists := pieces[piece]
			if !exists || existingWorkerKey == expensiveWorkerKey {
				swapIndex = indices[expensiveWorkerKey]
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

// chanceGreaterThanHalf returns whether the total chance this worker set
// completes the download before the given duration is more than 50%.
//
// NOTE: this function abstracts the chance a worker resolves after the given
// duration as a coinflip to make it easier to reason about the problem given
// that the workerset consists out of one or more overdrive workers.
func (ws workerSet) chanceGreaterThanHalf(dur time.Duration) bool {
	// convert every worker into a coinflip
	coinflips := make([]float64, len(ws.workers))
	for i, w := range ws.workers {
		coinflips[i] = w.distribution().ChanceAfter(dur)
	}

	// calculate the chance of all heads
	chanceAllHeads := float64(1)
	for _, flip := range coinflips {
		chanceAllHeads *= flip
	}

	// if we don't have to consider any overdrive workers, the chance it's all
	// heads is the chance that needs to be greater than half
	if ws.numOverdriveWorkers() == 0 {
		return chanceAllHeads > 0.5
	}

	// if there is 1 overdrive worker, meaning that we have 1 worker that can
	// come up as tails essentially, we have to consider the chance that one
	// coin is tails and all others come up as heads as well, and add this to
	// the chance that they're all heads
	if ws.numOverdriveWorkers() == 1 {
		totalChance := chanceAllHeads
		for _, flip := range coinflips {
			totalChance += (chanceAllHeads / flip * (1 - flip))
		}
		return totalChance > 0.5
	}

	// if there are 2 overdrive workers, meaning that we have 2 workers that can
	// up as tails, we have to extend the previous case with the case where
	// every possible coin pair comes up as being the only tails
	if ws.numOverdriveWorkers() == 2 {
		totalChance := chanceAllHeads

		// chance each cone is the only tails
		for _, flip := range coinflips {
			totalChance += chanceAllHeads / flip * (1 - flip)
		}

		// chance each possible coin pair becomes the only tails
		numCoins := len(coinflips)
		for i := 0; i < numCoins-1; i++ {
			chanceITails := 1 - coinflips[i]
			chanceOnlyITails := chanceAllHeads / coinflips[i] * chanceITails
			for jj := i + 1; jj < numCoins; jj++ {
				chanceJTails := 1 - coinflips[jj]
				chanceOnlyIAndJJTails := chanceOnlyITails / coinflips[jj] * chanceJTails
				totalChance += chanceOnlyIAndJJTails
			}
		}
		return totalChance > 0.5
	}

	// if there are a lot of overdrive workers, we use an approximation by
	// summing all coinflips to see whether we are expected to be able to
	// download min pieces within the given duration
	expected := float64(0)
	for _, flip := range coinflips {
		expected += flip
	}
	return expected > float64(ws.staticMinPieces)
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

// workers returns the resolved and unresolved workers as separate slices
func (pdc *projectDownloadChunk) workers(onlyUseful bool) (resolved, unresolved []*individualWorker) {
	ws := pdc.workerState
	ws.mu.Lock()
	defer ws.mu.Unlock()

	// convenience variables
	minPieces := pdc.workerSet.staticErasureCoder.MinPieces()
	length := pdc.pieceLength

	// isUseful is a helper function that decides whether the given worker is
	// useful for downloading
	isUseful := func(w *worker) bool {
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

	for _, rw := range ws.resolvedWorkers {
		// exclude workers that can't download any pieces or that are not useful
		if onlyUseful {
			if len(rw.pieceIndices) == 0 || !isUseful(rw.worker) {
				continue
			}
		}

		w := rw.worker
		rdt := w.staticJobReadQueue.distributionTrackerForLength(length)
		resolved = append(resolved, &individualWorker{
			staticLaunchedAt:       pdc.launchedAt(w),
			staticPieceIndices:     rw.pieceIndices,
			staticResolveChance:    1,
			staticReadDistribution: rdt.Distribution(0),
			staticWorker:           w,
		})
	}

	for _, uw := range ws.unresolvedWorkers {
		w := uw.staticWorker
		rdt := w.staticJobReadQueue.distributionTrackerForLength(length)

		// exclude workers that are not useful
		if onlyUseful && !isUseful(w) {
			continue
		}

		// unresolved workers can still have all pieces
		pieceIndices := make([]uint64, minPieces)
		for i := 0; i < len(pieceIndices); i++ {
			pieceIndices[i] = uint64(i)
		}

		unresolved = append(unresolved, &individualWorker{
			staticPieceIndices:     pieceIndices,
			staticResolveChance:    w.staticJobHasSectorQueue.callSuccessRate(),
			staticReadDistribution: rdt.Distribution(0),
			staticWorker:           w,
		})
	}

	return
}

// launchedAt returns the time at which this worker was launched.
func (pdc *projectDownloadChunk) launchedAt(w *worker) time.Time {
	for _, lw := range pdc.launchedWorkers {
		if lw.staticWorker.staticHostPubKeyStr == w.staticHostPubKeyStr {
			return lw.staticLaunchTime
		}
	}
	return time.Time{}
}

// workerIsLaunched returns true if the given worker is launched for the piece
// with given piece index.
func (pdc *projectDownloadChunk) workerIsLaunched(w *worker, pieceIndex uint64) bool {
	for _, lw := range pdc.launchedWorkers {
		if lw.staticWorker.staticHostPubKeyStr == w.staticHostPubKeyStr && lw.staticPieceIndex == pieceIndex {
			return true
		}
	}
	return false
}

// workerIsDownloading returns true if the given worker is launched and has not
// returned yet.
func (pdc *projectDownloadChunk) workerIsDownloading(w *worker) bool {
	for _, lw := range pdc.launchedWorkers {
		lwKey := lw.staticWorker.staticHostPubKeyStr
		if lwKey == w.staticHostPubKeyStr && lw.completeTime == (time.Time{}) {
			return true
		}
	}
	return false
}

// launchWorkerSet will try to launch every wo
func (pdc *projectDownloadChunk) launchWorkerSet(ws *workerSet) int {
	// TODO: validate worker set (unique pieces)

	// convenience variables
	minPieces := pdc.workerSet.staticErasureCoder.MinPieces()

	// range over all workers in the set and launch if possible
	workersLaunched := 0
	for _, w := range ws.workers {
		worker := w.worker()
		if worker == nil {
			continue // chimera worker
		}
		if pdc.workerIsDownloading(worker) {
			continue // in progress (this ensures we can re-use a worker)
		}

		for _, pieceIndex := range w.pieces() {
			if pdc.workerIsLaunched(worker, pieceIndex) {
				continue
			}
			isOverdrive := len(pdc.launchedWorkers) >= minPieces
			_, launched := pdc.launchWorker(worker, pieceIndex, isOverdrive)
			if launched {
				workersLaunched++
			}
			break // only launch one piece per worker
		}
	}
	return workersLaunched
}

func (pdc *projectDownloadChunk) updateDownloadStats() {
	var dlStats *skymodules.DownloadStats
	switch length := pdc.pieceLength; {
	case length <= 61e3:
		dlStats = pdc.workerSet.staticRenter.staticDownloadStats64kb
	case length <= 982e3:
		dlStats = pdc.workerSet.staticRenter.staticDownloadStats1m
	case length <= 3931e3:
		dlStats = pdc.workerSet.staticRenter.staticDownloadStats4m
	default:
		dlStats = pdc.workerSet.staticRenter.staticDownloadStats10m
	}

	minPieces := pdc.workerSet.staticErasureCoder.MinPieces()
	launched := uint64(len(pdc.launchedWorkers))
	overdrive := launched - uint64(minPieces)
	dlStats.Track(overdrive)
}

// launchWorkers performs the main download loop, every iteration we update the
// pdc's available pieces, construct a new worker set and launch every worker
// that can be launched from that set. Every iteration we check whether the
// download was finished.
func (pdc *projectDownloadChunk) launchWorkers(rootFoundChan chan error) {
	var once sync.Once
	for {
		// TODO: should we handle this more cleanly
		pdc.updateAvailablePieces()

		// create a worker set and launch it
		workerSet, err := pdc.createWorkerSet(maxOverdriveWorkers)
		if err != nil {
			rootFoundChan <- err
			return
		}

		if workerSet != nil {
			numLaunched := pdc.launchWorkerSet(workerSet)
			if numLaunched > 0 {
				once.Do(func() { close(rootFoundChan) })
			}
		}

		// check whether the download is completed
		completed, err := pdc.finished()
		if completed {
			pdc.updateDownloadStats()
			pdc.finalize()
			return
		}
		if err != nil {
			pdc.fail(err)
			return
		}

		// iterate
		select {
		case <-time.After(20 * time.Millisecond):
		case jrr := <-pdc.workerResponseChan:
			pdc.handleJobReadResponse(jrr)
		}
	}
}

// createWorkerSet tries to create a worker set from the pdc's resolved and
// unresolved workers, the maximum amount of overdrive workers in the set is
// defined by the given 'maxOverdriveWorkers' argument.
func (pdc *projectDownloadChunk) createWorkerSet(maxOverdriveWorkers int) (*workerSet, error) {
	// convenience variables
	ppms := pdc.pricePerMS
	length := pdc.pieceLength
	minPieces := pdc.workerSet.staticErasureCoder.MinPieces()

	// fetch all workers
	resolvedWorkers, unresolvedWorkers := pdc.workers(true)

	// verify we have enough workers to complete the download
	if len(resolvedWorkers)+len(unresolvedWorkers) < minPieces {
		return nil, errors.AddContext(errNotEnoughWorkers, fmt.Sprintf("%v < %v", len(resolvedWorkers)+len(unresolvedWorkers), minPieces))
	}

	// sort unresolved workers by expected resolve time
	sort.Slice(unresolvedWorkers, func(i, j int) bool {
		dI := unresolvedWorkers[i].staticReadDistribution
		dJ := unresolvedWorkers[j].staticReadDistribution
		return dI.ExpectedDuration() < dJ.ExpectedDuration()
	})

	// combine unresolved workers into a set of chimera workers
	var chimeraWorkers []*chimeraWorker
	current := NewChimeraWorker()
	for _, uw := range unresolvedWorkers {
		remainder := current.addWorker(uw)
		if remainder != nil {
			// add the current chimera worker to the slice and reset
			chimeraWorkers = append(chimeraWorkers, current)
			current = NewChimeraWorker()
			current.addWorker(remainder)
		}
	}

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

	// grab the first worker's distribution, we only need it to get at the
	// number of buckets and to transform bucket indices to a duration in ms.
	distribution := workers[0].distribution()

	// loop state
	var bestSet *workerSet
	var bestSetFound bool

OUTER:
	for numOverdrive := 0; numOverdrive < maxOverdriveWorkers; numOverdrive++ {
		workersNeeded := minPieces + numOverdrive
		for bI := 0; bI < distribution.NumBuckets(); bI++ {
			bDur := distribution.DurationForIndex(bI)

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
			mostLikely := make([]downloadWorker, 0)
			lessLikely := make([]downloadWorker, 0)
			pieces := make(map[uint64]struct{})
			for _, w := range workers {
				for _, pieceIndex := range w.pieces() {
					_, exists := pieces[pieceIndex]
					if exists {
						continue
					}

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
