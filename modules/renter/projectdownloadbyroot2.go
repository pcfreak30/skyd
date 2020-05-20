package renter

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"

	"gitlab.com/NebulousLabs/errors"
)

// TODO: Polish this up, add length based buckets.
type projectDownloadByRootMetrics struct {
	totalTime   float64
	numRequests float64

	decayedTime     float64
	decayedRequests float64

	mu sync.Mutex
}

// TODO
func (m *projectDownloadByRootMetrics) managedAddDatapoint(timeElapsed time.Duration) {
	m.mu.Lock()
	m.totalTime += float64(timeElapsed)
	m.numRequests++
	m.decayedTime *= 0.9
	m.decayedRequests *= 0.9
	m.decayedTime += float64(timeElapsed)
	m.decayedRequests++
	reqs := m.numRequests
	avg := m.totalTime / m.numRequests / float64(time.Millisecond)
	decReq := m.decayedRequests
	decAvg := m.decayedTime / m.decayedRequests / float64(time.Millisecond)
	m.mu.Unlock()
	fmt.Println("Average Performance:", avg, "ms over", reqs, "requests")
	fmt.Println("Recent Performance:", decAvg, "ms over", decReq, "requests")
}

// TODO:
func (m *projectDownloadByRootMetrics) managedAverageDownloadTime() time.Duration {
	m.mu.Lock()
	avg := time.Duration(m.decayedTime / m.decayedRequests)
	m.mu.Unlock()
	return avg
}

// TODO: add as element of renter instead of global var
var pm projectDownloadByRootMetrics

// managedDownloadByRoot will fetch data using the merkle root of that data.
// Unlike the exported version of this function, this function does not request
// memory from the memory manager.
func (r *Renter) managedDownloadByRoot(root crypto.Hash, offset, length uint64, timeout time.Duration) ([]byte, error) {
	start := time.Now()

	// Create a channel to time out the project.
	var timeoutChan <-chan time.Time
	if timeout > 0 {
		// TODO: Switch to a timer that we can drain.
		println("there is a timeout: ", timeout)
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

	// TODO: For now we filter out any workers that cannot do the v148 protocol.
	// Need to change this so that we submit jobs appropritately (through a
	// serial HasSector and DownloadSector) so that all workers can scan the
	// network and all files can be found.
	total := 0
	for _, worker := range workers {
		cache := worker.staticCache()
		if build.VersionCmp(cache.staticHostVersion, minAsyncVersion) != 0 {
			continue
		}
		workers[total] = worker
		total++
	}
	workers = workers[:total]

	// Create a channel to receive all of the results from the workers. The
	// channel is buffered with one slot per worker, so that the workers do not
	// have to block when returning the result of the job.
	staticResponseChan := make(chan *jobHasSectorResponse, len(workers))
	responses := 0
	// Create a channel to signal when the job has been completed.
	cancelChan := make(chan struct{})
	defer func() {
		// Automatically cancel the work when the function exits.
		close(cancelChan)
	}()

	// Send the work to all of the workers.
	for _, worker := range workers {
		jhs := jobHasSector{
			staticCanceledChan:     cancelChan,
			staticSector:       root,
			staticResponseChan: staticResponseChan,
		}
		if !worker.staticJobHasSectorQueue.callAdd(jhs) {
			responses++
			continue
		}
	}

	/*
			// This variant of the loop will download sequentially from every single
			// worker, allowing us to get a proper profile.
			var goodWs []*worker
		BOB:
			for responses < len(workers) {
				// Block for a response, and also wait for the timeout.
				var resp *jobHasSectorResponse
				select {
				case resp = <-staticResponseChan:
					responses++
				case <-timeoutChan:
					// Don't wait forever, workers that don't come back fast enough
					// don't count.
					println("breaking because timer is done")
					break BOB
				}
				println("got a response")

				// Add this worker to the set of workers that we can use if it has the
				// sector.
				if resp != nil && resp.staticErr == nil && resp.staticAvailable {
					println("adding a good w")
					goodWs = append(goodWs, resp.staticWorker)
				}
			}

			// Go through each worker sequentially and perform the download. The workers
			// will track themselves how fast they go.
			println("going through the good ws")
			for _, w := range goodWs {
				println("starting a new worker")
				start := time.Now()
				readSectorRespChan := make(chan *jobReadSectorResponse)
				jrs := jobReadSector{
					staticCanceledChan:     cancelChan,
					staticResponseChan: readSectorRespChan,

					length: length,
					offset: offset,
					staticSector: root,
				}
				if !w.staticJobReadSectorQueue.callAdd(jrs) {
					continue
				}

				// Wait for a response, respect the timeout.
				var readSectorResp *jobReadSectorResponse
				workerTimeoutChan := time.After(time.Second * 90)
				select {
				case readSectorResp = <-readSectorRespChan:
				case <-workerTimeoutChan:
					continue
				}
				if readSectorResp != nil && readSectorResp.staticErr == nil {
					pm.managedAddDatapoint(time.Since(start))
					fmt.Printf("%v: Sector data received: %v\n", w.staticHostPubKeyStr, time.Since(start))
					continue
				}
			}

			// Print out the fastest times yet seen by any worker.
			fmt.Println()
			fmt.Println()
			fmt.Println("starting worker dump")
			for _, worker := range r.staticWorkerPool.callWorkers() {
				// Ignore old workers.
				cache := worker.staticCache()
				if build.VersionCmp(cache.staticHostVersion, minAsyncVersion) != 0 {
					continue
				}

				jq := worker.staticJobReadSectorQueue
				jq.mu.Lock()
				fastestJob := jq.fastestJob
				jq.mu.Unlock()
				hasBeenValid := atomic.LoadUint64(&worker.atomicPriceTableHasBeenValid) == 1
				fmt.Printf("%v: HasBeenValid: %v, Fastest Job: %v\n", worker.staticHostPubKey, hasBeenValid, fastestJob)
			}
			fmt.Println()
			fmt.Println()

			println("got through all of the good ws, now printlng the worker dump")
	*/

	// Run a loop to get responses, and then if a worker has found the root,
	// attempt to download the root. If that download is successful, cancel all
	// of the other work. If the download is not successful, go back to looping
	// through the responses.
	var bestWorker *worker
	var bestWorkerTime time.Duration
	useBestWorkerTimer := time.After(pm.managedAverageDownloadTime()) // TODO: Switch to timer with cleanup
	useBestWorker := false
	for responses < len(workers) || bestWorker != nil {
		// Check for the timeout. This is done separately to ensure the timeout
		// has priority.
		select {
		case <-timeoutChan:
			fmt.Println("TIMEOUT")
			return nil, errors.Compose(ErrProjectTimedOut, ErrRootNotFound)
		default:
		}

		// Block for a response, and also wait for the timeout. If the response
		// indicatese that the sector was located, print a success.
		var resp *jobHasSectorResponse
		if responses < len(workers) {
			select {
			case <-useBestWorkerTimer:
				useBestWorker = true
			case resp = <-staticResponseChan:
				responses++
			case <-timeoutChan:
				fmt.Println("TIMEOUT")
				return nil, errors.Compose(ErrProjectTimedOut, ErrRootNotFound)
			}
		} else {
			useBestWorker = true
		}

		// Check for the edge case where this reponse did not end up with a
		// worker that can perform the download.
		if (resp == nil || resp.staticErr != nil || !resp.staticAvailable) && (!useBestWorker || bestWorker == nil) {
			if resp == nil {
				println("nil resp")
				continue
			}
			if resp.staticErr != nil {
				println("resp err", resp.staticErr.Error())
				continue
			}
			if !resp.staticAvailable {
				println("worker does not have sector")
				continue
			}
			continue
		}
		fmt.Printf("%v: HasSector positive response received: %v\n", root, time.Since(start))

		// Replace the best worker with this worker if this worker is better.
		if resp != nil && resp.staticErr == nil && resp.staticAvailable {
			println("updating best worker")
			avgDLTime := resp.staticWorker.staticJobReadSectorQueue.callAverageJobTime(length)
			if bestWorkerTime == 0 || avgDLTime < bestWorkerTime {
				bestWorkerTime = avgDLTime
				bestWorker = resp.staticWorker
			}
		}

		// If we are not being told to use our best worker, and also our best
		// worker is not predicted to start running before the average job time,
		// look for another worker.
		if !useBestWorker && pm.managedAverageDownloadTime() < time.Since(start)+bestWorkerTime {
			println("moving on, this worker is probably not the best")
			continue
		}
		println("a worker is now trying")

		// This worker has found the sector root! Queue a job on the worker to
		// perform the download.
		readSectorRespChan := make(chan *jobReadSectorResponse)
		jrs := jobReadSector{
			staticCanceledChan:     cancelChan,
			staticResponseChan: readSectorRespChan,

			staticLength: length,
			staticOffset: offset,
			staticSector: root,
		}
		if !bestWorker.staticJobReadSectorQueue.callAdd(jrs) {
			// TODO: This is why we need a list of best workers, if this guy
			// gets killed we're pretty sunk.
			continue
		}

		// Wait for a response, respect the timeout.
		//
		// TODO: the aggression timeout is causing a memory leak.
		var readSectorResp *jobReadSectorResponse
		workerTimeoutChan := time.After(time.Second * 9)
		select {
		case readSectorResp = <-readSectorRespChan:
		case <-timeoutChan:
			fmt.Println("TIMEOUT")
			return nil, errors.Compose(ErrProjectTimedOut, ErrRootNotFound)
		case <-workerTimeoutChan:
			// This worker is too slow try another one.
			fmt.Printf("%v: Sector data fetch failed due to aggressive worker timeout: %v\n", root, time.Since(start))
			continue
		}
		if readSectorResp != nil && readSectorResp.staticErr == nil {
			pm.managedAddDatapoint(time.Since(start))
			fmt.Printf("%v: Sector data received: %v\n", root, time.Since(start))

			// Print out the fastest times yet seen by any worker.
			fmt.Println()
			fmt.Println()
			fmt.Println("starting worker dump")
			for _, worker := range r.staticWorkerPool.callWorkers() {
				// Ignore old workers.
				cache := worker.staticCache()
				if build.VersionCmp(cache.staticHostVersion, minAsyncVersion) != 0 {
					continue
				}

				hasBeenValid := atomic.LoadUint64(&worker.atomicPriceTableHasBeenValid) == 1
				hasStreams := atomic.LoadUint64(&worker.atomicStreamHasBeenValid) == 1
				fmt.Printf("%v: HasBeenValid: %v:%v\n", worker.staticHostPubKey, hasStreams, hasBeenValid)
			}
			fmt.Println()
			fmt.Println()
			return readSectorResp.staticData, nil
		}
		fmt.Printf("%v: Sector data fetch failed: %v\n", root, time.Since(start))
	}

	// All workers have failed.
	fmt.Println("NOT FOUND")
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
