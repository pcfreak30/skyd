package renter

import (
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"

	"gitlab.com/NebulousLabs/errors"
)


// managedDownloadByRoot will fetch data using the merkle root of that data.
// Unlike the exported version of this function, this function does not request
// memory from the memory manager.
func (r *Renter) managedDownloadByRoot(root crypto.Hash, offset, length uint64, timeout time.Duration) ([]byte, error) {
	// Create a channel to time out the project.
	var timeoutChan <-chan time.Time
	if timeout > 0 {
		timeoutChan = time.After(timeout)
	}

	// Apply the timeout to the project. A timeout of 0 will be ignored.
	if r.deps.Disrupt("timeoutProjectDownloadByRoot") {
		return nil, errors.Compose(ErrProjectTimedOut, ErrRootNotFound)
	}

	// Get the full list of workers that could potentially download the root.
	workers := r.staticWorkerPool.callWorkers()
	if len(workers) == 0 {
		return nil, errors.New("cannot perform DownloadByRoot, no workers in worker pool")
	}

	println("starting async dbr: ", len(workers))

	// TODO: For now we filter out any workers that cannot do the v148 protocol.
	// Need to change this so that we submit jobs appropritately (through a
	// serial HasSector and DownloadSector) so that all workers can scan the
	// network and all files can be found.
	total := 0
	for _, worker := range workers {
		cache := worker.staticCache()
		if build.VersionCmp(cache.staticHostVersion, minAsyncVersion) != 0 {
			println("skipping worker: ", cache.staticHostVersion, minAsyncVersion)
			continue
		}
		workers[total] = worker
		total++
	}
	workers = workers[:total]
	println("num qualified workers: ", len(workers))

	// Create a channel to receive all of the results from the workers. The
	// channel is buffered with one slot per worker, so that the workers do not
	// have to block when returning the result of the job.
	responseChan := make(chan *jobHasSectorResponse, len(workers))
	responses := 0
	// Create a channel to signal when the job has been completed.
	cancelChan := make(chan struct{})
	defer func() {
		// Automatically cancel the work when the function exits.
		println("cancelling the cancel chan")
		close(cancelChan)
	}()

	// Send the work to all of the workers.
	println("adding jobs to the workers")
	for _, worker := range workers {
		println("adding job to one worker")
		jhs := jobHasSector{
			canceled:     cancelChan,
			sector:       root,
			responseChan: responseChan,
		}
		if !worker.staticJobHasSectorQueue.callAdd(jhs) {
			responses++
			continue
		}
	}

	// Run a loop to get responses, and then if a worker has found the root,
	// attempt to download the root. If that download is successful, cancel all
	// of the other work. If the download is not successful, go back to looping
	// through the responses.
	println("going through the responses")
	for responses < len(workers) {
		// Check for the timeout. This is done separately to ensure the timeout
		// has priority.
		println("checking timeout")
		select {
		case <-timeoutChan:
			return nil, errors.Compose(ErrProjectTimedOut, ErrRootNotFound)
		default:
		}

		// Block for a response, and also wait for the timeout.
		println("waiting on resp chan")
		var resp *jobHasSectorResponse
		println("))))))))))))))))))")
		println(responseChan)
		select {
		case resp = <-responseChan:
			responses++
		case <-timeoutChan:
			return nil, errors.Compose(ErrProjectTimedOut, ErrRootNotFound)
		}

		println("__________ ___ ___ ___ __ got a response")
		if resp == nil || resp.staticErr != nil {
			if resp.staticErr != nil {
				println("worker failout: ", resp.staticErr.Error())
			} else {
				println("nil resp sent")
			}
			continue
		}
		println("response is positive")

		// This worker has found the sector root! Queue a job on the worker to
		// perform the download.
		println("doing the has sector job")
		readSectorRespChan := make(chan *jobReadSectorResponse)
		jrs := jobReadSector {
			canceled: cancelChan,
			responseChan: readSectorRespChan,

			length: length,
			offset: offset,
			sector: root,
		}
		if !resp.staticWorker.staticJobReadSectorQueue.callAdd(jrs) {
			continue
		}

		// Wait for a response, respect the timeout.
		var readSectorResp *jobReadSectorResponse
		select {
		case readSectorResp = <-readSectorRespChan:
		case <-timeoutChan:
			return nil, errors.Compose(ErrProjectTimedOut, ErrRootNotFound)
		}
		if readSectorResp != nil && readSectorResp.staticErr == nil {
			println("returning the data!")
			println(len(readSectorResp.staticData))
			return readSectorResp.staticData, nil
		}
	}

	// All workers have failed.
	return nil, ErrRootNotFound
}

// DownloadByRoot2 will fetch data using the merkle root of that data. This uses
// all of the async worker primitives to improve speed and throughput.
func (r *Renter) DownloadByRoot2(root crypto.Hash, offset, length uint64, timeout time.Duration) ([]byte, error) {
	// Block until there is memory available, and then ensure the memory gets
	// returned.
	if !r.memoryManager.Request(length, true) {
		return nil, errors.New("renter shut down before memory could be allocated for the project")
	}
	defer r.memoryManager.Return(length)
	data, err := r.managedDownloadByRoot(root, offset, length, timeout)
	if errors.Contains(err, ErrProjectTimedOut) {
		err = errors.AddContext(err, fmt.Sprintf("timed out after %vs", timeout.Seconds()))
	}
	return data, err
}
