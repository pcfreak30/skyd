package renter

// workerjobhassector.go defines the job to check whether a host has a sector
// available.

import (
	"sync"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"

	"gitlab.com/NebulousLabs/errors"
)

type (
	// jobHasSector contains information about a hasSector query.
	jobHasSector struct {
		canceled     chan struct{}             // Can signal that the job has been canceled
		sector       crypto.Hash               // Which sector is being looked up
		responseChan chan jobResponseHasSector // Channel to send a response down
	}

	// jobQueueHasSector is a list of hasSector queries that have been assigned
	// to the worker.
	jobQueueHasSector struct {
		killed bool
		jobs   []jobHasSector

		staticWorker *worker
		mu           sync.Mutex
	}

	// jobResponseHasSector contains the result of a hasSector query.
	jobResponseHasSector struct {
		available bool
		err       error
	}
)

// newJobQueueHasSector will initialize a has sector job queue for the worker.
// This is only meant to be run once at startup.
func (w *worker) newJobQueueHasSector() {
	// Sanity check that there is no existing job queue.
	if w.staticJobQueueHasSector != nil {
		w.renter.log.Critical("incorred call on newJobQueueHasSector")
	}
	w.staticJobQueueHasSector = &jobQueueHasSector{
		staticWorker: w,
	}
}

// staticCanceled is a convenience function to check whether a job has been
// canceled.
func (j *jobHasSector) staticCanceled() bool {
	select {
	case <-j.canceled:
		return true
	default:
		return false
	}
}

// callAdd will add a job to the queue. False will be returned if the job cannot
// be queued because the worker has been killed.
func (jq *jobQueueHasSector) callAdd(job jobHasSector) bool {
	jq.mu.Lock()
	defer jq.mu.Unlock()

	if jq.killed {
		return false
	}
	jq.jobs = append(jq.jobs, job)
	return true
}

// callNext will provide the next jobHasSector from the set of jobs.
func (jq *jobQueueHasSector) callNext() (func(), uint64, uint64) {
	var job jobHasSector
	jq.mu.Lock()
	for {
		if len(jq.jobs) == 0 {
			jq.mu.Unlock()
			return nil, 0, 0
		}

		// Grab the next job.
		job := jq.jobs[0]
		jq.jobs = jq.jobs[1:]

		// Break out of the loop only if this job has not been canceled.
		if job.staticCanceled() {
			continue
		}
		break
	}
	jq.mu.Unlock()

	// Create the actual job that will be run by the async job launcher.
	jobFn := func() {
		available, err := jq.staticWorker.managedHasSector(job.sector)
		response := jobResponseHasSector{
			available: available,
			err:       err,
		}

		// Send the response in a goroutine so that the worker resources can be
		// released faster.
		go func() {
			job.responseChan <- response
		}()
	}

	// Return the job along with the bandwidth estimates for completing the job.
	//
	// TODO: These values are overly conservative, once we've got the protocol
	// more optimized we can bring these down.
	return jobFn, 20e3, 20e3
}

// managedHasSector returns whether or not the host has a sector with given root
func (w *worker) managedHasSector(sectorRoot crypto.Hash) (bool, error) {
	// Create the program.
	//
	// TODO: Switch the price table to being fetched statically using atomic
	// pointers.
	pt := w.staticPriceTable().staticPriceTable
	pb := modules.NewProgramBuilder(&pt)
	pb.AddHasSectorInstruction(sectorRoot)
	program, programData := pb.Program()
	cost, _, _ := pb.Cost(true)

	// Add bandwidth costs to the budget.
	//
	// TODO temporarily increase budget to ensure it is sufficient to cover the
	// cost, until we have defined the true bandwidth cost of the new protocol
	cost = cost.Add(pb.BandwidthCost())
	cost = cost.Mul64(10)

	// Execute the program and parse the responses.
	//
	// TODO: Are we expecting more than one response? Should we check that there
	// was only one response?
	var hasSector bool
	var responses []programResponse
	responses, err := w.managedExecuteProgram(program, programData, w.staticHostFCID, cost)
	if err != nil {
		return false, errors.AddContext(err, "Unable to execute program")
	}
	for _, resp := range responses {
		if resp.Error != nil {
			return false, errors.AddContext(resp.Error, "Output error")
		}
		hasSector = resp.Output[0] == 1
		break
	}
	return hasSector, nil
}

// managedKillJobsHasSector will release all remaining HasSector jobs as failed.
func (w *worker) managedKillJobsHasSector() {
	jq := w.staticJobQueueHasSector // Convenience variable
	jq.mu.Lock()
	defer jq.mu.Unlock()
	for _, job := range jq.jobs {
		// Send the response in a goroutine so that the worker resources can be
		// released faster.
		go func(j jobHasSector) {
			response := jobResponseHasSector{
				err: errors.New("worker killed"),
			}
			j.responseChan <- response
		}(job)
	}
	jq.killed = true
	jq.jobs = nil
}
