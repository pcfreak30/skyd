package renter

import (
	"fmt"
	"sort"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/types"
)

type (
	downloadWorker interface {
		chanceCompleteAfter(dur time.Duration) float64
		cost(length uint64) types.Currency
		distribution() *skymodules.Distribution
		pieces() []uint64
		worker() *worker
	}

	// TODO PJ: docstring
	chimeraWorker struct {
		readDistribution *skymodules.Distribution
		workers          []*worker
	}

	// TODO PJ: docstring
	individualWorker struct {
		staticLaunchedSince    time.Time
		staticPieceIndices     []uint64
		staticResolveChance    float64
		staticReadDistribution *skymodules.Distribution
		staticWorker           *worker
	}

	// TODO PJ: docstring
	individualWorkers []*individualWorker

	workerSet struct {
		workers []downloadWorker

		staticDuration  time.Duration
		staticLength    uint64
		staticMinPieces int
	}
)

// NewChimeraWorker returns a new chimera worker object.
func NewChimeraWorker() *chimeraWorker {
	dt := skymodules.NewDistributionTrackerStandard()
	return &chimeraWorker{
		readDistribution: dt.Distribution(0),
		workers:          make([]*worker, 0),
	}
}

// addWorker adds the given worker to the chimera worker.
func (cw *chimeraWorker) addWorker(w *individualWorker) {
	cw.workers = append(cw.workers, w.staticWorker)
	cw.readDistribution.MergeWith(w.staticReadDistribution, w.staticResolveChance)
}

// TODO PJ: docstring
func (cw *chimeraWorker) chanceCompleteAfter(dur time.Duration) float64 {
	chance := 1 // chimera worker's chance is 1 per definition
	return float64(chance) * cw.readDistribution.ChanceAfter(dur)
}

func (cw *chimeraWorker) distribution() *skymodules.Distribution {
	return cw.readDistribution
}

func (cw *chimeraWorker) pieces() []uint64 {
	return nil
}

func (cw *chimeraWorker) worker() *worker {
	return nil
}

func (cw *chimeraWorker) cost(length uint64) types.Currency {
	var total types.Currency
	for _, w := range cw.workers {
		jrq := w.staticJobReadQueue
		total = total.Add(jrq.callExpectedJobCost(length))
	}
	return total.Div64(uint64(len(cw.workers)))
}

// TODO PJ: docstring
func (iw *individualWorker) chanceCompleteAfter(dur time.Duration) float64 {
	chance := iw.staticResolveChance
	if chance != 1 {
		// this should only be called on a resolved worker whose chance is 1.
		build.Critical("unexpected")
	}
	return chance * iw.staticReadDistribution.ChanceAfter(dur)
}

func (iw *individualWorker) distribution() *skymodules.Distribution {
	// if this worker has launched we want to shift its distribution by the time
	// since launch
	//
	// NOTE: we always shift on a clone of the original distribution
	if iw.staticLaunchedSince != (time.Time{}) {
		clone := iw.staticReadDistribution.Clone()
		clone.Shift(time.Since(iw.staticLaunchedSince))
		return clone
	}
	return iw.staticReadDistribution
}

func (iw *individualWorker) pieces() []uint64 {
	return iw.staticPieceIndices
}

func (iw *individualWorker) worker() *worker {
	return iw.staticWorker
}

func (iw *individualWorker) cost(length uint64) types.Currency {
	// already launched workers have a cost of zero to ensure they are very
	// favourable when building the best set, besides cost we also shift their
	// distribution by the time since launch
	if iw.staticLaunchedSince != (time.Time{}) {
		return types.ZeroCurrency
	}
	return iw.staticWorker.staticJobReadQueue.callExpectedJobCost(length)
}

// TODO PJ: docstring
func (workers individualWorkers) toChimeraWorkers() (chimeras []*chimeraWorker) {
	current := NewChimeraWorker()
	remaining := float64(1)
	for _, w := range workers {
		if w.staticResolveChance > remaining {
			// split the worker into the 'main' worker and a 'remainder'
			main, remainder := w.split(remaining)
			current.addWorker(main)
			chimeras = append(chimeras, current)

			// reset the chimera worker and add the remainder
			current = NewChimeraWorker()
			current.addWorker(remainder)
			remaining = 1 - remainder.staticResolveChance
		} else {
			remaining -= w.staticResolveChance
			current.addWorker(w)
		}
	}
	return
}

// split will split the download worker into two workers, the first worker will
// have the given chance, the second worker will have the remainder as its
// chance value.
func (iw *individualWorker) split(chance float64) (*individualWorker, *individualWorker) {
	// TODO PJ: validate chance
	main := &individualWorker{
		staticLaunchedSince:    iw.staticLaunchedSince,
		staticPieceIndices:     iw.staticPieceIndices,
		staticResolveChance:    chance,
		staticReadDistribution: iw.staticReadDistribution,
		staticWorker:           iw.staticWorker,
	}
	remainder := &individualWorker{
		staticLaunchedSince:    iw.staticLaunchedSince,
		staticPieceIndices:     iw.staticPieceIndices,
		staticResolveChance:    iw.staticResolveChance - chance,
		staticReadDistribution: iw.staticReadDistribution,
		staticWorker:           iw.staticWorker,
	}

	return main, remainder
}

func (ws *workerSet) clone() *workerSet {
	return &workerSet{
		workers: append([]downloadWorker{}, ws.workers...),

		staticLength:    ws.staticLength,
		staticMinPieces: ws.staticMinPieces,
	}
}

func (ws *workerSet) maxCost() (types.Currency, int) {
	var max types.Currency
	maxIndex := -1
	for i, w := range ws.workers {
		cost := w.cost(ws.staticLength)
		// NOTE: we have to gte here because the cost might be zero (for
		// launched workers) and we want to avoid -1 index in that case
		if cost.Cmp(max) >= 0 {
			max = cost
			maxIndex = i
		}
	}
	return max, maxIndex
}

func (ws *workerSet) totalCost() types.Currency {
	var total types.Currency
	for _, w := range ws.workers {
		total = total.Add(w.cost(ws.staticLength))
	}
	return total
}

func (ws *workerSet) adjustedDuration(ppms types.Currency) time.Duration {
	return addCostPenalty(ws.staticDuration, ws.totalCost(), ppms)
}

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
	// coin is tails and all others come up as hands as well, and add this to
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

	// if there is a lot of overdrive, use an expecation approximation.
	expected := float64(0)
	for _, flip := range coinflips {
		expected += flip
	}
	return expected > float64(ws.staticMinPieces)
}

func (ws workerSet) numOverdriveWorkers() int {
	numWorkers := len(ws.workers)
	if numWorkers < ws.staticMinPieces {
		return 0
	}
	return numWorkers - ws.staticMinPieces
}

func (ws workerSet) consistsOfUniquePieceWorkers() bool {
	counts := make(map[uint64]uint64)
	for _, w := range ws.workers {
		for _, p := range w.pieces() {
			counts[p]++
			if counts[p] > 1 {
				return false
			}
		}
	}
	return true
}

// workers returns the resolved and unresolved workers as separate slices
func (pdc *projectDownloadChunk) workers(onlyUseful bool) (resolved, unresolved individualWorkers) {
	ws := pdc.workerState
	ws.mu.Lock()
	defer ws.mu.Unlock()

	// convenience variables
	minPieces := pdc.workerSet.staticErasureCoder.MinPieces()
	length := pdc.pieceLength

	for _, rw := range ws.resolvedWorkers {
		// exclude workers that can't download any pieces
		if onlyUseful && len(rw.pieceIndices) == 0 {
			continue
		}

		w := rw.worker
		dt := w.staticJobReadQueue.distributionTrackerForLength(length)
		resolved = append(resolved, &individualWorker{
			staticLaunchedSince:    pdc.launchedSince(w),
			staticPieceIndices:     rw.pieceIndices,
			staticResolveChance:    1,
			staticReadDistribution: dt.Distribution(0),
			staticWorker:           w,
		})
	}

	for _, uw := range ws.unresolvedWorkers {
		w := uw.staticWorker
		dt := w.staticJobReadQueue.distributionTrackerForLength(length)

		// unresolved workers can still have all pieces
		pieceIndices := make([]uint64, minPieces)
		for i := 0; i < len(pieceIndices); i++ {
			pieceIndices[i] = uint64(i)
		}

		unresolved = append(unresolved, &individualWorker{
			staticLaunchedSince:    pdc.launchedSince(w),
			staticPieceIndices:     pieceIndices,
			staticResolveChance:    w.staticJobHasSectorQueue.callSuccessRate(),
			staticReadDistribution: dt.Distribution(0),
			staticWorker:           w,
		})
	}

	return
}

func (pdc *projectDownloadChunk) launchedSince(w *worker) time.Time {
	for _, lw := range pdc.launchedWorkers {
		if lw.staticWorker.staticHostPubKeyStr == w.staticHostPubKeyStr {
			return lw.staticLaunchTime
		}
	}
	return time.Time{}
}

func (pdc *projectDownloadChunk) isLaunchedForPiece(w *worker, pieceIndex uint64) bool {
	for _, lw := range pdc.launchedWorkers {
		if lw.staticWorker.staticHostPubKeyStr == w.staticHostPubKeyStr && lw.staticPieceIndex == pieceIndex {
			return true
		}
	}
	return false
}

func (pdc *projectDownloadChunk) launchWorkers(maxOverdriveWorkers int) error {
	for {
		pdc.updateAvailablePieces()

		err := pdc.launchWorkersIter(maxOverdriveWorkers)
		if err != nil {
			return err
		}

		completed, err := pdc.finished()
		if completed {
			minPieces := pdc.workerSet.staticErasureCoder.MinPieces()
			launched := uint64(len(pdc.launchedWorkers))
			overdrive := launched - uint64(minPieces)

			dls := pdc.workerState.staticRenter.staticDLStats
			dls.mu.Lock()
			dls.totalDownloads++
			if overdrive > 0 {
				dls.totalOverdrive++
				dls.totalWorkersLaunched += launched
				dls.totalOverdriveWorkersLaunched += overdrive
			}

			odAvg := float64(dls.totalOverdrive) / float64(dls.totalDownloads)
			odWorkersAvg := float64(dls.totalOverdriveWorkersLaunched) / float64(dls.totalWorkersLaunched)
			dls.mu.Unlock()

			pdc.finalize()
			fmt.Printf("OD avg: %v OD workers avg: %v\n", odAvg, odWorkersAvg)
			return nil
		}
		if err != nil {
			pdc.fail(err)
			return err
		}

		select {
		case <-time.After(20 * time.Millisecond):
		case jrr := <-pdc.workerResponseChan:
			pdc.handleJobReadResponse(jrr)
		case <-pdc.ctx.Done():
			return errors.New("timed out while trying to build initial set of workers")
		}
	}
}
func (pdc *projectDownloadChunk) launchWorkersIter(maxOverdriveWorkers int) error {
	ppms := pdc.pricePerMS
	length := pdc.pieceLength
	minPieces := pdc.workerSet.staticErasureCoder.MinPieces()

	// fetch all workers
	resolvedWorkers, unresolvedWorkers := pdc.workers(true)

	// verify we have enough workers to complete the download
	if len(resolvedWorkers)+len(unresolvedWorkers) < minPieces {
		return errors.AddContext(errNotEnoughWorkers, fmt.Sprintf("%v < %v", len(resolvedWorkers)+len(unresolvedWorkers), minPieces))
	}

	// sort unresolved workers by expected resolve time
	sort.Slice(unresolvedWorkers, func(i, j int) bool {
		dI := unresolvedWorkers[i].staticReadDistribution
		dJ := unresolvedWorkers[j].staticReadDistribution
		return dI.ExpectedDuration() < dJ.ExpectedDuration()
	})

	// turn the unresolved workers into chimera workers
	chimeraWorkers := unresolvedWorkers.toChimeraWorkers()

	// combine all workers
	workers := make([]downloadWorker, len(resolvedWorkers)+len(chimeraWorkers))
	for rwi, rw := range resolvedWorkers {
		workers[rwi] = rw
	}
	for cwi, cw := range chimeraWorkers {
		workers[len(resolvedWorkers)+cwi] = cw
	}
	if len(workers) == 0 {
		return nil
	}

	// grab the first worker's distribution, we only need it to get at the
	// numof buckets and transform bucket indices to durations
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
				return workers[i].chanceCompleteAfter(bDur) > workers[j].chanceCompleteAfter(bDur)
			})

			// select workers in a way there's no duplicate
			selected := make([]downloadWorker, 0)
			pieces := make(map[uint64]struct{})
			for _, w := range workers {
				// already resolved workers can
				for _, pieceIndex := range w.pieces() {
					_, exists := pieces[pieceIndex]
					if !exists {
						selected = append(selected, w)
						pieces[pieceIndex] = struct{}{}
					}
				}
			}

			var mostLikelySet *workerSet
			var remainingWorkers []downloadWorker
			if len(selected) > workersNeeded {
				mostLikelySet = &workerSet{
					workers: selected[:workersNeeded],

					staticDuration:  bDur,
					staticLength:    length,
					staticMinPieces: minPieces,
				}
				remainingWorkers = selected[workersNeeded:]
			} else {
				mostLikelySet = &workerSet{
					workers: selected,

					staticDuration:  bDur,
					staticLength:    length,
					staticMinPieces: minPieces,
				}
			}

			// if the chance of the most likely set does not exceed 50%, it is
			// not high enough to continue, no need to continue this iteration,
			// we need to try a slower and thus more likely bucket
			if !mostLikelySet.chanceGreaterThanHalf(bDur) {
				continue
			}

			// now loop the remaining workers and try and swap them with the
			// most expensive workers in the most likely set
			for _, w := range remainingWorkers {
				mostExpensive, mostExpensiveIndex := mostLikelySet.maxCost()
				if w.cost(length).Cmp(mostExpensive) > 0 {
					continue
				}

				cheaperSet := mostLikelySet.clone()
				cheaperSet.workers[mostExpensiveIndex] = w
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

	if !bestSet.consistsOfUniquePieceWorkers() {
		fmt.Println("non unique:")
		for i, w := range bestSet.workers {
			msg := fmt.Sprintf("worker %v pieces: ", i+1)
			for _, piece := range w.pieces() {
				msg += fmt.Sprintf("%v,", piece)
			}
			fmt.Println(msg)
		}
		// build.Critical("non unique workers, we'll need to solve this")
	}

	// launch all workers that can be launched
	for _, w := range bestSet.workers {
		worker := w.worker()
		if worker != nil {
			for _, pieceIndex := range w.pieces() {
				if !pdc.isLaunchedForPiece(worker, pieceIndex) {
					isOverdrive := len(pdc.launchedWorkers) >= minPieces
					pdc.launchWorker(worker, pieceIndex, isOverdrive)
					break // only launch one piece per worker
				}
			}
		} else {
			fmt.Println("chimera worker in best set")
		}
	}
	return nil
}
