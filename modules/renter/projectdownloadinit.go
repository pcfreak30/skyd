package renter

// projectdownloadinit.go implements an algorithm to select the best set of
// initial workers for completing a download. This algorithm is balancing
// between two different criteria. The first is the amount of time that the
// download will take to complete - the algorithm tries to minimize this - and
// the second is cost.
//
// The download is created with an input of 'pricePerMS', which means that the
// download algorithm should pick a slower set of workers so long as the amount
// of money saved is greater than the price per millisecond multiplied by the
// number of milliseconds of slowdown that is incurred by switching to cheaper
// workers.
//
// The algorithm used is fairly involved, but achieves an okay runtime. First,
// the complete set of workers are placed into a heap sorted by their expected
// return time. The fastest workers are sorted to be popped out of the heap
// first.
//
// Because of parallelism, the expected return time of the project is equal to
// the expected return time of the slowest worker. Because of the 'pricePerMS'
// value, we can convert a duration into a price. The total adjusted cost of a
// set of workers is therefore the monetary cost of each worker plus the
// 'pricePerMS' multiplied by the duration until the slowest worker would
// finish.
//
// To get a baseline, we pop of 'MinPieces' workers, tally up the financial
// cost, and track the duration of the slowest worker. We then compute the total
// adjusted cost of using the fastest possible set of workers. We save a copy of
// this set as the 'bestSet', retaining the inital consturction as the
// 'workingSet'.
//
// Then we iterate by popping a new worker off of the heap. Two things are at
// play. The first is that this worker may be cheaper that one of our existing
// workers. And the second is that this worker is slower (due to the time sorted
// heap), so therefore may drive the total cost up despite being cheaper. We do
// not know at this time if the optimal set includes an even slower worker. We
// update the working set by replacing the most expensive worker with the new
// worker, assuming that the new worker is cheaper. (if the new worker is not
// cheaper, the new worker is ignored). After the update, we check whether the
// working set's new cost is cheaper than the best set's cost. If so, we
// overwrite the best set with the current working set. If not, we continue
// popping off new workers in pursuit of a cheaper adjusted set of workers.
//
// We know that if the optimal set of workers contains a slower worker than the
// current worker, and the current worker is cheaper than an existing worker,
// then there is no time penalty to swapping out an existing worker for the
// current cheaper worker. Keeping a best set and a working set allows us to
// build towards the optimal set even if there are suboptimal increments along
// the way.
//
// There are two complications. The first complication is that not every worker
// can fetch every piece. The second complication is that some workers can fetch
// multiple pieces.
//
// For workers that cannot fetch every piece, we will only consider the pieces
// that they can fetch. If they can fetch a piece that has no worker in it, that
// worker will be added to the workingSet and the most expensive worker will be
// evicted. If a new worker only has pieces that overlap with workers already in
// the workingSet, the new worker will evict the most expensive worker that it
// is capable of replacing. If it cannot replace anyone because everyone it
// could replace is already cheaper, the new worker will be ignored.
//
// When an existing worker is evicted, it will go back into the heap so that
// there can be an attempt to add it back to the set. The worker that got
// evicted may be able to replace some other worker in the working set.
//
// For workers that can fetch multiple pieces, the worker will be added back
// into the heap after it is inserted into the working set. To account for the
// extra load that is put onto the worker for having to fetch multiple pieces,
// the 'readDuration' of the worker will be added to the 'competeTime' each
// additional time that the worker is put back into the heap. This is overly
// pessimistic, but guarantees that we do not overload a particular worker and
// slow the entire download down.

import (
	"container/heap"

	"gitlab.com/NebulousLabs/errors"
)

// TODO: When picking through the initial set of workers, we should try to
// account for things like cooldown, because once we've launched workers, we
// have no choice but to fail to overdrive if some of the launches fail. This is
// beacuse the other launches already happened, and therefore can no longer be
// replaced by the initial selection code.

// pdcInitialWorker tracks information about a worker that is useful for
// building the optimal set of launch workers.
type pdcInitialWorker struct {
	// The readDuration tracks the amount of time the worker is expected to take
	// to execute a read job. The readDuration gets added to the duration each
	// time the worker is added back into the heap to potentially be used an
	// additional time. Technically, the worker is able to fetch in parallel and
	// so assuming an additional full 'readDuration' per read is overly
	// pessimistic, at the same time we prefer to spread our downloads over
	// multiple workers so the pessimism is not too bad.
	//
	// The cost is the amount of money will be spent on fetching a single piece
	// for this pdc.
	completeTime time.Time
	cost         types.Currency
	readDuration time.Duration

	// The list of pieces indicates which pieces the worker is capable of
	// fetching. If 'unresolved' is set to true, the worker will be treated as
	// though it can fetch the first 'MinPieces' pieces.
	pieces     []uint64
	unresolved bool
	worker     *worker
}

// TODO: Heap is sorted by time. Fastest is first, slowest is last. This is
// important to the runtime efficiency of the algorithm.
type pdcInitialWorkerHeap []*pdcInitialWorker

// TODO: Implement the heap.

// TODO: Initial sorted workers, because sorting is annoying, should do the
// worker-extension stuff (where you allow workers that can grab multiple pieces
// to be inserted multipe times). There's a smart way to add them where you add
// them at the end, and then once you reach them you know that you can quit once
// you've added like 10 of them. We can do this by keeping a 2x list, and a 3x
// list, etc. And then each time we add a new one to the list, we just check the
// 2x list and 3x list,etc. I keep thinking it's good enough to stop at...
//
// Okay, so since we have to push things in real time, what we actually need is
// a heap. fublewub
func (pdc *projectDownloadChunk) initialWorkerHeap(unresolvedWorkers []*pcwsUnresolvedWorker) []*pdcInitialWorker {
	// TODO: When pushing unresolved workers, need to make it so that 'pieces'
	// is the first MinPieces pieces, so that they count properly for all of
	// their options.
}

// TODO: The algorithm for replacing a worker that can potentially fill multiple
// slots should be... when you are putting the worker in its first slot, you
// slip the worker in and then add the worker back into the list of available
// workers with a cost that is 2x the original. When you slip it in a second
// time, add it back into the list with a cost that is 3x the original, etc.
//
// When replacing it, we will try to re-add it somewhere else using the same
// timing that it is already using. This will give us an optimal fill-out
// without computational expense.

// createInitialWorkerSet will go through the current set of workers and
// determine the best set of workers to use when attempting to download a piece.
func (pdc *projectDownloadChunk) createInitialWorkerSet() (<-chan struct{}, []*launchablePiece, error) {
	// Convenience variable.
	ec := pdc.workerSet.staticErasureCoder

	// Get the list of unresolved workers. This will also grab an update, so any
	// workers that have resolved recently will be reflected in the newly
	// returned set of values.
	unresolvedWorkers, updateChan := pdc.unresolvedWorkers()

	// Create a list of usable workers, sorted by the amount of time they are
	// expected to take to return.
	workerHeap := pdc.initialWorkerHeap(unresolvedWorkers)

	// Keep track of the current best set, and the amount of time it will take
	// the best set to return. And keep track of the current working set, and
	// the amount of time it will take the current working set to return.
	//
	// The total adjusted cost of a set is the cost of launching each of its
	// individual workers, plus a single adjustment for the duration of the set.
	// The duration of the set is the longest of any duration of its individual
	// workers.
	//
	// The algorithm for finding the best set is to start by adding all of the
	// fastest workers, and putting them into the best set. Then, we copy the
	// best set into the working set. We add slower workers to the working set
	// one at a time. Each time we add a worker, we replace any of the faster
	// workers that is more expensive than the slower worker. When we are done,
	// we look at the new total adjusted cost of the working set. If it is less
	// than the best set, we replace the best set with the current working set
	// and continue building out the working set. If it is not better than the
	// best set, we just keep building out the working set. This is guaranteed
	// to find the optimal best set while only using a linear amount of total
	// computation.
	bestSet := make([]*pdcInitialPiece, ec.NumPieces())
	workingSet := make([]*pdcInitialPiece, ec.NumPieces())
	var bestSetCost types.Currency
	var workingSetCost types.Currency
	var workingSetDuration time.Duration

	// Kick things off by forming the naive bestSet, which is looping over the
	// sorted workers until MinPieces() workers have been added.
	for {
		// Grab the next worker from the heap. If the heap is empty, we are
		// done.
		nextWorker := heap.Pop(&workerHeap)
		if nextWorker == nil {
			break
		}

		// Iterate through the working set and determine the cost and index of
		// the most expensive worker. If the new worker is not cheaper, the
		// working set cannot be updated.
		highestCost := nextWorker.cost
		highestCostIndex := 0
		totalWorkers := 0
		for i := 0; i < len(workingSet); i++ {
			if workingSet[i] == nil {
				continue
			}
			if workingSet[i].cost.Cmp(highestCost) > 0 {
				highestCost = workingSet[i].cost
				highestCostIndex = i
			}
			totalWorkers++
		}
		// Consistency check: we should never have more than MinPieces workers
		// assigned.
		if totalWorkers > ec.MinPieces() {
			// TODO: Critical
		}

		// If the time cost of this worker is strictly higher than the full cost
		// of the best set, there can be no more improvements to the best set,
		// and the loop can exit.
		workerTimeCost := pdc.pricePerMS.Mul64(uint64(nextWorker.duration))
		if workerTimeCost.Cmp(bestSetCost) > 0 && totalWorkers == ec.MinPieces() {
			break
		}
		// If there is not a more expensive worker in the working set, skip this
		// iteration.
		if highestCost.Cmp(nextWorker) <= 0 && totalWorkers == ec.MinPieces() {
			continue
		}

		// Find a spot for this new worker. The new worker only gets a spot if
		// it can fit into an empty spot, or if it can evict an existing worker
		// and have a better cost. If there are multiple spots where an eviction
		// could happen, the most expensive should be evicted. Going into an
		// empty spot is best, because that means we can evict the most
		// expensive worker in the whole working set.
		workerUseful := false
		bestSpotEmpty := false
		bestSpotCost := types.ZeroCurrency
		bestSpotIndex := 0
		for _, i := range nextWorker.pieces {
			if workingSet[i] == nil {
				bestSpotEmpty = true
				bestSpotIndex = i
				break
			}
			if workingSet[i].cost.Cmp(bestSpotCost) > 0 {
				workerUseful = true
				bestSpotCost = workingSet[i].cost
				bestSpotIndex = i
			}
		}
		// Check whether the worker is useful at all. It may not be useful if
		// the only pieces it has are already available via cheaper workers.
		if !bestSpotEmtpy && !workerUseful {
			continue
		}

		// We know for certain now that the current worker is useful. Update the
		// duration of the working set to be the speed of the nextWorker if the
		// nextWorker is slower.
		//
		// nextWorker may not be slower if it was re-added to the heap in a
		// previous interation due to being evicted from its spot. If it was
		// evicted and re-added, that means there is hope that this worker was
		// useful in a different place.
		if nextWorker.duration > workingSetDuration {
			workingSetDuration = nextWorker.duration
		}

		// Perform the actual replacement. Remember to update the total cost of
		// the working set.
		//
		// NOTE: we could dedup code between these branches, but I felt the code
		// was cleaner this way.
		//
		// TODO: Explain why we push the evicted worker back into the heap: we
		// do it because maybe there is another slot where they could be useful.
		if bestSpotEmpty {
			workingSetCost = workingSetCost.Add(nextWorker.cost)
			workingSet[bestSpotIndex] = nextWorker
			// Only do the eviction if we already have enough workers.
			if totalWorkers >= ec.MinPieces() {
				workingSetCost = workingSetCost.Sub(highestCost)
				heap.Push(&workerHeap, workingSet[highestCostIndex])
				workingSet[highestCostIndex] = nil
			}
		} else {
			workingSetCost = workingSetCost.Add(nextWorker.cost)
			workingSetCost = workingSetCost.Sub(workingSet[bestSpotIndex].cost)
			heap.Push(&workerHeap, workingSet[bestSpotIndex])
			workingSet[bestSpotIndex] = nextWorker
		}

		// Determine whether the working set is now cheaper than the best set.
		// Adding in the new worker has made the working set cheaper in terms of
		// raw cost, but the new worker is slower, so the time penalty has gone
		// up.
		if nextWorker.time < 0 {
			// Quick edge case management.
			nextWorker.time = 0
			// TODO: Critical here
		}
		workingSetTimeCost := pdc.pricePerMS.Mul64(uint64(workingSetDuration))
		workingSetTotalCost := workingSetTimeCost.Add(workingSetCost)
		if workingSetTotalCost.Cmp(bestSetCost) < 0 {
			// Do a copy operation. Can't set one equal to the other because
			// then changes to the working set will update the best set.
			copy(bestSet, workingSet)
		}

		// Create a new entry for 'nextWorker' and push that entry back into the
		// heap. This is in case 'nextWorker' is able to fetch multiple pieces.
		// The duration of the next worker will be increased by the
		// 'readDuration' as a worst case estmiation of what the performance hit
		// will be for using the same worker multiple times.
		if len(nextWorker.pieces) > 1 {
			copyWorker := *nextWorker
			copyWorker.duration = nextWorker.duration + nextWorker.readDuration
			heap.Push(&workerHeap, &copyWorker)
		}
	}

	// We now have the best set. If the best set does not have enough workers to
	// complete the download, return an error. If the best set has enough
	// workers to complete the download but some of the workers in the best set
	// are yet unresolved, return the updateChan and everything else is nil, if
	// the best set is done and all of the workers in the best set are resolved,
	// return the best set and everything else is nil.
	totalWorkers := 0
	isUnresolved := false
	for _, worker := range bestSet {
		if worker == nil {
			continue
		}
		totalWorkers++
		isUnresolved = isUnresolved || worker.unresolved
	}
	if totalWorkers < ec.MinPieces() {
		return nil, nil, errors.New("not enough workers to complete download")
	}
	if isUnresolved {
		return updateChan, nil, nil
	}
	return nil, bestSet, nil
}

// launchInitialWorkers will pick the initial set of workers that needs to be
// launched and then launch them. This is a non-blocking function that returns
// once jobs have been scheduled for MinPieces workers.
func (pdc *projectDownloadChunk) launchInitialWorkers() error {
	var finalWorkers []*launchablePiece
	for {
		var updateChan <-chan struct{}
		updateChan, finalWorkers, err := pdc.createInitialWorkerSet()
		if err != nil {
			return errors.AddContext(err, "unable to build initial set of workers")
		}
		// If the function returned an actual set of workers, we are good to
		// launch.
		if finalWorkers != nil {
			return pdc.launchFinalWorkers(finalWorkers)
		}

		if finalWorkers != nil {
			select {
			case <-updateChan:
			case <-pdc.ctx.Done():
				return errors.New("timed out while trying to build initial set of workers")
			}
		}
	}

	return errors.New("never implemented a function to launch the initial workers")
}
