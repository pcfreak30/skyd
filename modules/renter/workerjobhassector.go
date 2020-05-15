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
		responseChan chan *jobHasSectorResponse // Channel to send a response down

		sector crypto.Hash
	}

	// jobHasSectorQueue is a list of hasSector queries that have been assigned
	// to the worker.
	jobHasSectorQueue struct {
		killed bool
		jobs   []jobHasSector

		staticWorker *worker
		mu           sync.Mutex
	}

	// jobHasSectorResponse contains the result of a hasSector query.
	jobHasSectorResponse struct {
		staticAvailable bool
		staticErr       error

		staticWorker *worker
	}
)

// newJobHasSectorQueue will initialize a has sector job queue for the worker.
// This is only meant to be run once at startup.
func (w *worker) newJobHasSectorQueue() {
	// Sanity check that there is no existing job queue.
	if w.staticJobHasSectorQueue != nil {
		w.renter.log.Critical("incorred call on newJobHasSectorQueue")
	}
	w.staticJobHasSectorQueue = &jobHasSectorQueue{
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
func (jq *jobHasSectorQueue) callAdd(job jobHasSector) bool {
	defer jq.staticWorker.staticWake()
	jq.mu.Lock()
	defer jq.mu.Unlock()

	if job.responseChan == nil {
		println("OOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOOO")
	}

	if jq.killed {
		return false
	}
	jq.jobs = append(jq.jobs, job)
	return true
}

// callNext will provide the next jobHasSector from the set of jobs.
func (jq *jobHasSectorQueue) callNext() (func(), uint64, uint64) {
	println("getting the next job")
	var job jobHasSector
	jq.mu.Lock()
	for {
		if len(jq.jobs) == 0 {
			jq.mu.Unlock()
			return nil, 0, 0
		}

		// Grab the next job.
		job = jq.jobs[0]
		jq.jobs = jq.jobs[1:]

		// Break out of the loop only if this job has not been canceled.
		if !job.staticCanceled() {
			break
		}
		continue
	}
	jq.mu.Unlock()

	// Create the actual job that will be run by the async job launcher.
	jobFn := func() {
		println(".starting job")
		available, err := jq.staticWorker.managedHasSector(job.sector)
		println(".got the sector")
		response := &jobHasSectorResponse{
			staticAvailable: available,
			staticErr:       err,

			staticWorker: jq.staticWorker,
		}

		// Send the response in a goroutine so that the worker resources can be
		// released faster.
		go func() {
			println(".sending the response")
			println("**************")
			println(job.responseChan)
			job.responseChan <- response
		}()
	}

	// Return the job along with the bandwidth estimates for completing the job.
	println("returning said job")
	ulBandwidth, dlBandwidth := programHasSectorBandwidth()
	return jobFn, ulBandwidth, dlBandwidth
}

// programHasSectorBandwidth returns the bandwidth that gets consumed by a
// HasSector program.
//
// TODO: These values are overly conservative, once we've got the protocol more
// optimized we can bring these down.
func programHasSectorBandwidth() (ulBandwidth, dlBandwidth uint64) {
	ulBandwidth = 20e3
	dlBandwidth = 20e3
	return
}

// managedHasSector returns whether or not the host has a sector with given root
func (w *worker) managedHasSector(sectorRoot crypto.Hash) (bool, error) {
	println(".. has sector in progress")
	// Create the program.
	pt := w.staticPriceTable().staticPriceTable
	pb := modules.NewProgramBuilder(&pt)
	pb.AddHasSectorInstruction(sectorRoot)
	program, programData := pb.Program()
	cost, _, _ := pb.Cost(true)

	// take into account bandwidth costs
	ulBandwidth, dlBandwidth := programHasSectorBandwidth()
	bandwidthCost := modules.MDMBandwidthCost(pt, ulBandwidth, dlBandwidth)
	cost = cost.Add(bandwidthCost)

	// Execute the program and parse the responses.
	//
	// TODO: Are we expecting more than one response? Should we check that there
	// was only one response?
	//
	// TODO: for this program we don't actually need the file contract - v149
	// only.
	println(".. executing program")
	var hasSector bool
	var responses []programResponse
	responses, err := w.managedExecuteProgram(program, programData, w.staticCache().staticContractID, cost)
	println(".. program execution finished")
	if err != nil {
		println(".. &&&&&&& program failed: ", err.Error())
		return false, errors.AddContext(err, "Unable to execute program")
	}
	for _, resp := range responses {
		if resp.Error != nil {
			println(".. bad resp")
			return false, errors.AddContext(resp.Error, "Output error")
		}
		hasSector = resp.Output[0] == 1
		break
	}
	println(".. program success")
	return hasSector, nil
}

// managedDumpJobsHasSector will release all remaining HasSector jobs as failed.
func (w *worker) managedDumpJobsHasSector() {
	jq := w.staticJobHasSectorQueue // Convenience variable
	jq.mu.Lock()
	defer jq.mu.Unlock()
	for _, job := range jq.jobs {
		// Send the response in a goroutine so that the worker resources can be
		// released faster.
		go func(j jobHasSector) {
			response := &jobHasSectorResponse{
				staticErr: errors.New("worker is dumping all has sector jobs"),
			}
			j.responseChan <- response
		}(job)
	}
	jq.jobs = nil
}

// managedKillJobsHasSector will release all remaining HasSector jobs as failed.
func (w *worker) managedKillJobsHasSector() {
	jq := w.staticJobHasSectorQueue // Convenience variable
	jq.mu.Lock()
	defer jq.mu.Unlock()
	for _, job := range jq.jobs {
		// Send the response in a goroutine so that the worker resources can be
		// released faster.
		go func(j jobHasSector) {
			response := &jobHasSectorResponse{
				staticErr: errors.New("worker killed"),
			}
			j.responseChan <- response
		}(job)
	}
	jq.killed = true
	jq.jobs = nil
}
