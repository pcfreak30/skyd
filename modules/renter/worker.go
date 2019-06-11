package renter

import (
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
)

// workerPool is the pool of workers plus the hosts and some status information
// about the hosts that they draw from. The workerPool has its own mutex,
// separate from the renter.
//
// The worker pool shares a log and a threadgroup with the renter.
type workerPool struct {
	hosts             map[string]modules.RenterContract // List of hosts in use by the renter, mapped to the most recent contract with the host.
	hostsGoodForRenew map[string]bool                   // List of which hosts are GoodForRenew.
	hostsOffline      map[string]bool                   // List of which hosts are offline.
	workers           map[string]*worker                // All of the active workers in the renter.

	// Utilities. The mutex is a RW mutex because the worker pool is going to be
	// read very frequently and updated very infrequently.
	mu     sync.RWMutex
	renter *Renter
}

// A worker listens for work on a certain host.
//
// The mutex of the worker only protects the 'unprocessedChunks' and the
// 'standbyChunks' fields of the worker. The rest of the fields are only
// interacted with exclusively by the primary worker thread, and only one of
// those ever exists at a time.
//
// The workers have a concept of 'cooldown' for uploads and downloads. If a
// download or upload operation fails, the assumption is that future attempts
// are also likely to fail, because whatever condition resulted in the failure
// will still be present until some time has passed. Without any cooldowns,
// uploading and downloading with flaky hosts in the worker sets has
// substantially reduced overall performance and throughput.
type worker struct {
	// The contract and host used by this worker.
	hostPubKey types.SiaPublicKey

	// Download variables that are not protected by a mutex, but also do not
	// need to be protected by a mutex, as they are only accessed by the master
	// thread for the worker.
	ownedDownloadConsecutiveFailures int       // How many failures in a row?
	ownedDownloadRecentFailure       time.Time // How recent was the last failure?

	// Download variables related to queuing work. They have a separate mutex to
	// minimize lock contention.
	downloadChan       chan struct{}              // Notifications of new work. Takes priority over uploads.
	downloadChunks     []*unfinishedDownloadChunk // Yet unprocessed work items.
	downloadMu         sync.Mutex
	downloadTerminated bool // Has downloading been terminated for this worker?

	// Upload variables.
	unprocessedChunks         []*unfinishedUploadChunk // Yet unprocessed work items.
	uploadChan                chan struct{}            // Notifications of new work.
	uploadConsecutiveFailures int                      // How many times in a row uploading has failed.
	uploadRecentFailure       time.Time                // How recent was the last failure?
	uploadRecentFailureErr    error                    // What was the reason for the last failure?
	uploadTerminated          bool                     // Have we stopped uploading?

	// Utilities.
	//
	// The mutex is only needed when interacting with 'downloadChunks' and
	// 'unprocessedChunks', as everything else is only accessed from the single
	// master thread.
	//
	// The logger and the threadgroup are shared with the worker pool, which
	// shares with the renter, therefore these are the same threadgroup and log
	// as the renter log. Same with the contractor.
	killChan chan struct{} // Worker will shut down if a signal is sent down this channel.
	mu       sync.Mutex
	renter   *Renter
}

// updateWorkerPool will grab the set of contracts from the contractor and
// update the worker pool to match.
func (wp *workerPool) managedUpdate() {
	// Build the set of maps that the renter uses to know the status of hosts
	// when assessing health and peforming file repair. The contractMap is used
	// when determining if a worker should be shut down. These resources do not
	// need to be built holding a mutex.
	contracts := wp.renter.hostContractor.Contracts()
	hosts := make(map[string]modules.RenterContract)
	hostsGoodForRenew := make(map[string]bool)
	hostsOffline := make(map[string]bool)
	for _, contract := range contracts {
		hpk := contract.HostPublicKey.String()
		hosts[hpk] = contract
		hostsGoodForRenew[hpk] = contract.Utility.GoodForRenew
		hostsOffline[hpk] = wp.renter.hostContractor.IsOffline(contract.HostPublicKey)
	}

	// Update the worker pool to reflect any changes in the contracts.
	wp.mu.Lock()
	defer wp.mu.Unlock()
	wp.hosts = hosts
	wp.hostsGoodForRenew = hostsGoodForRenew
	wp.hostsOffline = hostsOffline

	// Add a worker for any contract that does not already have a worker.
	for _, contract := range contracts {
		hpk := contract.HostPublicKey.String()
		_, exists := wp.workers[hpk]
		if !exists {
			worker := &worker{
				hostPubKey: contract.HostPublicKey,

				downloadChan: make(chan struct{}, 1),
				killChan:     make(chan struct{}),
				uploadChan:   make(chan struct{}, 1),

				renter: wp.renter,
			}
			wp.workers[hpk] = worker
			if err := wp.renter.tg.Add(); err != nil {
				// Stop starting workers on shutdown.
				break
			}
			go func() {
				defer wp.renter.tg.Done()
				worker.threadedWorkLoop()
			}()
		}
	}

	// Remove a worker for any worker that is not in the set of new contracts.
	for id, worker := range wp.workers {
		select {
		case <-wp.renter.tg.StopChan():
			// Release the lock and return to prevent error of trying to close
			// the worker channel after a shutdown
			return
		default:
		}
		_, exists := hosts[worker.hostPubKey.String()]
		if !exists {
			delete(wp.workers, id)
			close(worker.killChan)
		}
	}

	// For debug builds, log some statistics about the workers. Because this
	// block of code calls locks on the workers, this code is fully separated
	// out instead of just depending on the log levels.
	if build.DEBUG {
		totalWorkersOnCooldown := 0
		for _, worker := range wp.workers {
			worker.mu.Lock()
			onCoolDown, coolDownTime := worker.onUploadCooldown()
			if onCoolDown {
				totalWorkersOnCooldown++
			}
			contract, _ := hosts[worker.hostPubKey.String()] // Contract may not exist, this will just cause a 'false' reported in the 'GoodForUpload' value of the worker.
			wp.renter.log.Debugf("Worker %v is GoodForUpload %v for contract %v\n    and is on uploadCooldown %v for %v because of %v", worker.hostPubKey, contract.Utility.GoodForUpload, contract.ID, onCoolDown, coolDownTime, worker.uploadRecentFailureErr)
			worker.mu.Unlock()
		}
		wp.renter.log.Debugf("Refreshed Worker Pool has %v total workers and %v are on cooldown", len(wp.workers), totalWorkersOnCooldown)
	}
}

// threadedWorkLoop repeatedly issues work to a worker, stopping when the worker
// is killed or when the thread group is closed.
func (w *worker) threadedWorkLoop() {
	defer w.managedKillUploading()
	defer w.managedKillDownloading()

	for {
		// Perform one step of processing download work.
		downloadChunk := w.managedNextDownloadChunk()
		if downloadChunk != nil {
			// managedDownload will handle removing the worker internally. If
			// the chunk is dropped from the worker, the worker will be removed
			// from the chunk. If the worker executes a download (success or
			// failure), the worker will be removed from the chunk. If the
			// worker is put on standby, it will not be removed from the chunk.
			w.managedDownload(downloadChunk)
			continue
		}

		// Perform one step of processing upload work.
		chunk, pieceIndex := w.managedNextUploadChunk()
		if chunk != nil {
			w.managedUpload(chunk, pieceIndex)
			continue
		}

		// Block until new work is received via the upload or download channels,
		// or until a kill or stop signal is received.
		select {
		case <-w.downloadChan:
			continue
		case <-w.uploadChan:
			continue
		case <-w.killChan:
			return
		case <-w.renter.tg.StopChan():
			return
		}
	}
}

// newWorkerPool returns a worker pool with all fields initialized.
func newWorkerPool(r *Renter) *workerPool {
	wp := &workerPool{
		hosts:             make(map[string]modules.RenterContract),
		hostsGoodForRenew: make(map[string]bool),
		hostsOffline:      make(map[string]bool),
		workers:           make(map[string]*worker),

		renter: r,
	}
	wp.renter.tg.OnStop(func() error {
		wp.mu.RLock()
		for _, worker := range wp.workers {
			close(worker.killChan)
		}
		wp.mu.RUnlock()
		return nil
	})
	wp.managedUpdate()
	return wp
}
