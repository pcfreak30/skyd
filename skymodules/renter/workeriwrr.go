package renter

import (
	"math"

	"gitlab.com/SkynetLabs/skyd/build"
	"go.sia.tech/siad/modules"
)

// iwrr implements the interleaved weighted round robin algorithm for the
// worker's async jobs.
// https://en.wikipedia.org/wiki/Weighted_round_robin
type iwrr struct {
	staticQueues    []weightedJobQueue
	staticMaxWeight uint64

	currentIndex int
	currentRound uint64
}

// weightedJobQueue is the interface a queue needs to implement to be used with
// the iwrr.
type weightedJobQueue interface {
	// callLen returns the length of the queue.
	callLen() int

	// callNextWithWeight returns the next job in the worker queue if it meets a
	// certain minimum weight. If there is no job in the queue, or the next job
	// doesn't fullfil the weight requirement, 'nil' will be returned.
	callNextWithWeight(minWeight uint64) (uint64, workerJob)

	// staticMaxWeight returns the max weight that a job from this queue can
	// have.
	staticMaxWeight() uint64
}

// These constants determine the max weight for a queue in the iwrr. For most
// queues the max weight equals the weight for the job except for dynamic jobs
// like downloads where the weight depends on the download length.
//
// NOTE: These values can be tweaked. The higher the weight, the more often a
// job can be scheduled within a cycle.
// The highest weight determines the number of times the algorithm loops over
// the queues. So make sure those numbers are reasonably low because a high
// weight might cause an unnecessary performance impact.
var (
	// The job with the lowest weight. Async system repairs.
	lowPrioReadQueueWeight uint64 = 1

	// Medium weighted jobs.
	readQueueMinWeight          uint64 = 10
	readQueueMaxWeight          uint64 = readQueueMinWeight + modules.SectorSize
	downloadSnapshotQueueWeight uint64 = 10
	uploadSnapshotQueueWeight   uint64 = 10

	// These are the high weight jobs since they are the fastest ones.
	hasSectorQueueWeight      uint64 = readQueueMaxWeight
	readRegistryQueueWeight   uint64 = readQueueMaxWeight
	updateRegistryQueueWeight uint64 = readQueueMaxWeight

	// Renewing is so rare that we also give it the same priority as the high
	// weight jobs
	renewQueueWeight = readQueueMaxWeight
)

// newIWRR creates a new iwrr from queues.
func newIWRR(queues []weightedJobQueue) *iwrr {
	var maxWeight uint64
	for _, queue := range queues {
		if queue.staticMaxWeight() > maxWeight {
			maxWeight = queue.staticMaxWeight()
		}
	}
	return &iwrr{
		staticQueues:    queues,
		staticMaxWeight: maxWeight,
	}
}

// initIWRR initializes the iwrr. Needs to be called after the initializers for
// the queues.
func (w *worker) initIWRR() {
	if w.staticIWRR != nil {
		build.Critical("iwrr already initialized")
	}

	w.staticIWRR = newIWRR([]weightedJobQueue{
		w.staticJobRenewQueue,
		w.staticJobHasSectorQueue,
		w.staticJobReadRegistryQueue,
		w.staticJobUpdateRegistryQueue,
		w.staticJobReadQueue,
		w.staticJobDownloadSnapshotQueue,
		w.staticJobUploadSnapshotQueue,
		w.staticJobLowPrioReadQueue,
	})
}

// numJobs returns the sum of all the jobs within any of the queues within the
// iwrr.
func (rr *iwrr) numJobs() (n int) {
	for _, queue := range rr.staticQueues {
		n += queue.callLen()
	}
	return
}

// next returns the next job or 'nil' if all queues are empty using the
// interleaved weighted round robin algorithm.
// The higher the weight of a queue, the fewer jobs we draw from it. We draw at
// least 1 job from the highest weighted queue within a cycle.
func (rr *iwrr) next() workerJob {
	// Try to get a job for as long as we have jobs in any of the queues. As
	// long as we have jobs, we should eventually fetch one of them.
	for rr.numJobs() > 0 {
		// Loop over the remaining rounds.
		for rr.currentRound <= rr.staticMaxWeight {
			// Loop over the remaining queues in this round.
			lowestNextWeight := uint64(math.MaxUint64)
			nextHighestNextWeight := uint64(math.MaxUint64)
			for ; rr.currentIndex < len(rr.staticQueues); rr.currentIndex++ {
				queue := rr.staticQueues[rr.currentIndex]

				// Try to fetch a valid job from the queue.
				nextWeight, wj := queue.callNextWithWeight(rr.currentRound)
				if nextWeight < lowestNextWeight {
					lowestNextWeight = nextWeight
				}
				if nextWeight < nextHighestNextWeight && nextWeight > rr.currentRound {
					nextHighestNextWeight = nextWeight
				}
				if wj == nil {
					continue // try next queue
				}

				// Increment the index to make sure we don't look at the same
				// queue again the next time.
				rr.currentIndex++
				return wj
			}
			// Reset the index after the round.
			rr.currentIndex = 0

			// Figure out the next round that's not a no-op.
			if lowestNextWeight == math.MaxUint64 && nextHighestNextWeight == math.MaxUint64 {
				break // no next weight found
			} else if nextHighestNextWeight < math.MaxUint64 {
				rr.currentRound = nextHighestNextWeight // found a higher weight
			} else {
				rr.currentRound = lowestNextWeight // use the lowest weight
			}
		}
		// Reset the round after the cycle.
		rr.currentRound = 0
	}
	return nil
}
