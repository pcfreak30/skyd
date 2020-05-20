package renter

import (
	"fmt"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"

	"gitlab.com/NebulousLabs/errors"
)

const (
	// projectDownloadByRootPerformanceDecay defines the amount of decay that is
	// applied to the exponential weigted average used to compute the
	// performance of the download by root projects that have run recently.
	projectDownloadByRootPerformanceDecay = 0.9
)

// projectDownloadByRootManager tracks metrics across multiple runs of
// DownloadByRoot projects, and is used by the projects to set expectations for
// performance.
//
// We put downloads into 3 different buckets for performance because the
// performance characterstics are very different depending on which bucket you
// are in.
type projectDownloadByRootManager struct {
	// Aggregate values for download by root projects. These are typically used
	// for research purposes, as opposed to being used in real time.
	totalTime64k   time.Duration
	totalTime1m    time.Duration
	totalTime4m    time.Duration
	totalRequests64k uint64
	totalRequests1m  uint64
	totalRequests4m  uint64

	// Decayed values track the recent performance of jobs in each bucket. These
	// values are generally used to help select workers when scheduling work,
	// because they are more responsive to changing network conditions.
	decayedTime64k     float64
	decayedTime1m      float64
	decayedTime4m      float64
	decayedRequests64k float64
	decayedRequests1m  float64
	decayedRequests4m  float64

	mu sync.Mutex
}

// managedRecordProjectTime adds a download to the historic values of the
// project manager. It takes a length so that it knows which bucket to put the
// data in.
func (m *projectDownloadByRootManager) managedRecordProjectTime(length uint64, timeElapsed time.Duration) {
	var bucket uint64
	var recentAvg time.Duration
	var totalAvg time.Duration
	var totalRequests uint64
	m.mu.Lock()
	if length <= 1 << 16 {
		m.totalTime64k += timeElapsed
		m.totalRequests64k++
		m.decayedTime64k *= projectDownloadByRootPerformanceDecay
		m.decayedRequests64k *= projectDownloadByRootPerformanceDecay
		m.decayedTime64k += float64(timeElapsed)
		m.decayedRequests64k++
		bucket = 1 << 16
		recentAvg = time.Duration(m.decayedTime64k / m.decayedRequests64k)
		totalAvg = m.totalTime64k / time.Duration(m.totalRequests64k)
		totalRequests = m.totalRequests64k
	} else if length <= 1 << 20 {
		m.totalTime1m += timeElapsed
		m.totalRequests1m++
		m.decayedTime1m *= projectDownloadByRootPerformanceDecay
		m.decayedRequests1m *= projectDownloadByRootPerformanceDecay
		m.decayedTime1m += float64(timeElapsed)
		m.decayedRequests1m++
		bucket = 1 << 20
		recentAvg = time.Duration(m.decayedTime1m / m.decayedRequests1m)
		totalAvg = m.totalTime1m / time.Duration(m.totalRequests1m)
		totalRequests = m.totalRequests1m
	} else {
		m.totalTime4m += timeElapsed
		m.totalRequests4m++
		m.decayedTime4m *= projectDownloadByRootPerformanceDecay
		m.decayedRequests4m *= projectDownloadByRootPerformanceDecay
		m.decayedTime4m += float64(timeElapsed)
		m.decayedRequests4m++
		bucket = 1 << 22
		recentAvg = time.Duration(m.decayedTime4m / m.decayedRequests4m)
		totalAvg = m.totalTime4m / time.Duration(m.totalRequests4m)
		totalRequests = m.totalRequests4m
	}
	m.mu.Unlock()
	fmt.Printf("Bucket %v has had recent performance %v, and historic performance %v over %v requests\n", bucket, recentAvg, totalAvg, totalRequests)
}

// mangedAverageProjectTime will return the average download time that prjects
// have had for the given length.
func (m *projectDownloadByRootManager) managedAverageProjectTime(length uint64) time.Duration {
	var avg time.Duration
	m.mu.Lock()
	if length <= 1 << 16 {
		avg = time.Duration(m.decayedTime64k / m.decayedRequests64k)
	} else if length <= 1 << 20 {
		avg = time.Duration(m.decayedTime1m / m.decayedRequests1m)
	} else {
		avg = time.Duration(m.decayedTime4m / m.decayedRequests4m)
	}
	m.mu.Unlock()
	return avg
}

// managedDownloadByRoot will fetch data using the merkle root of that data.
// Unlike the exported version of this function, this function does not request
// memory from the memory manager.
func (r *Renter) managedDownloadByRoot(root crypto.Hash, offset, length uint64, timeout time.Duration) ([]byte, error) {
	pm := r.staticProjectDownloadByRootManager
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
			staticCanceledChan: cancelChan,
			staticSector:       root,
			staticResponseChan: staticResponseChan,
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
	var bestWorker *worker
	var bestWorkerTime time.Duration
	useBestWorkerTimer := time.After(pm.managedAverageProjectTime(length)) // TODO: Switch to timer with cleanup
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
		if !useBestWorker && pm.managedAverageProjectTime(length) < time.Since(start)+bestWorkerTime {
			println("moving on, this worker is probably not the best")
			continue
		}
		println("a worker is now trying")

		// This worker has found the sector root! Queue a job on the worker to
		// perform the download.
		readSectorRespChan := make(chan *jobReadSectorResponse)
		jrs := jobReadSector{
			staticCanceledChan: cancelChan,
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
			pm.managedRecordProjectTime(length, time.Since(start))
			fmt.Printf("%v: Sector data received: %v\n", root, time.Since(start))
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
