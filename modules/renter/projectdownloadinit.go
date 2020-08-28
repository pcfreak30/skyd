package renter

import (
	"gitlab.com/NebulousLabs/errors"
)

// TODO: When picking through the initial set of workers, we should try to
// account for things like cooldown, because once we've launched workers, we
// have no choice but to fail to overdrive if some of the launches fail. This is
// beacuse the other launches already happened, and therefore can no longer be
// replaced by the initial selection code.

// TODO: Should assume that each unresolved worker can only fetch a single
// piece. If we potentially end up using the same worker to fetch multiple
// pieces (maybe we should not do this!), we need to make sure that the second
// piece has a fetch size which is double to account for the new overheads...
//
// CHALLENGE: if we don't decide to fetch multiple pieces from a single worker,
// it becomes harder to know if it's possible to launch a set, because if we
// choose to put a worker which can do pieces 1+2 into the 1 slot, and everyone
// else is only able to do 1... so what we can do is drop a worker into multiple
// slots, but then mark the number of slots that worker is currently in (a
// pointer that is shared across all slots), and be greedy about replacing that
// worker if we can insert a different worker. Or maybe we just need to track
// all of the workers that can fit into a slot and compact them down at the end?
// Hmm a bit trickier than I had realized.

// TODO: Create a function that can iterate over your in-progress set and
// replace/remove the worst element, replacing it with a better element that may
// go in a different slot. Only if the slot is blank, if the slot is not blank
// you have to be better than whatever is already in that slot.

// pdcInitialWorker is a helper struct for discovering the optimal set of
// workers that can be used to complete the chunk download.
//
// The struct contains a worker, the list of pieces the worker can fetch, the
// cost of retrieving the data, and the expected amount of time until the data
// is returned.
type pdcIniitalWorker struct {
	cost types.Currency
	time time.Duration

	pieces []uint64
	worker *worker
}

func (pdc *projectDownloadChunk) initalSortedWorkers(unresolvedWorkers []*pcwsUnresolvedWorker) []*pdcInitialWorker {
}

// createInitialWorkerSet will go through the current set of workers and
// determine the best set of workers to use when attempting to download a piece.
func (pdc *projectDownloadChunk) createInitialWorkerSet() (<-chan struct{}, []*launchablePiece, error) {
	// TODO: We need to be able to alternate between a set that we are building
	// and a completed set. This means that pointers can really screw us up. I
	// think we'll just use the copy builtin function, and then ensure that the
	// top level array is doing everything correctly.

	// Get the list of unresolved workers. This will also grab an update, so any
	// workers that have resolved recently will be reflected in the newly
	// returned set of values.
	unresolvedWorkers, updateChan := pdc.unresolvedWorkers()

	// Create a list of usable workers, sorted by the amount of time they are
	// expected to take to return.
	var sortedWorkers []*pdcInitialWorker
	sortedWorkers := pdc.sortedInitialWorkers(unresolvedWorkers)

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
	var bestSet []*pdcInitialPiece
	var bestSetDuration time.Duration
	var bestSetCost types.Currency
	var workingSet []*pdcInitialPiece
	var workingSetDuration time.Duration
	var workingSetCost types.Currency

	// Step 1: block until 
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

		// TODO: Check here that it's possible to complete the set.

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
