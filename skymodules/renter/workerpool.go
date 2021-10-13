package renter

import (
	"fmt"
	"sync"

	"gitlab.com/NebulousLabs/errors"

	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/types"
)

// workerPool is the collection of workers that the renter can use for
// uploading, downloading, and other tasks related to communicating with the
// host. There is one worker per host that the renter has a contract with. This
// includes hosts that have been disabled or otherwise been marked as
// !GoodForRenew or !GoodForUpload. We keep all of these workers so that they
// can be used in emergencies in the event that there seems to be no other way
// to recover data.
//
// TODO: Currently the repair loop does a lot of fetching and passing of host
// maps and offline maps and goodforrenew maps. All of those objects should be
// cached in the worker pool, which will both improve performance and reduce the
// calling complexity of the functions that currently need to pass this
// information around.
type workerPool struct {
	workers      map[string]*worker // The string is the host's public key.
	updateChan   chan struct{}
	mu           sync.RWMutex
	staticRenter *Renter
}

// callUpdateChan returns a channel that is closed the next time the worker pool
// is updated.
func (wp *workerPool) callChangeChan() <-chan struct{} {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	return wp.updateChan
}

// callStatus returns the status of the workers in the worker pool.
func (wp *workerPool) callStatus() skymodules.WorkerPoolStatus {
	// For tests, callUpdate to ensure the worker pool isn't empty
	if build.Release == "testing" {
		wp.callUpdate()
	}

	// Fetch the list of workers from the worker pool.

	var totalDownloadCoolDown, totalMaintenanceCoolDown, totalUploadCoolDown int
	var statuss []skymodules.WorkerStatus // Plural of status is statuss, deal with it.
	workers := wp.callWorkers()

	// Loop all workers and collect their status objects.
	for _, w := range workers {
		status := w.callStatus()
		if status.DownloadOnCoolDown {
			totalDownloadCoolDown++
		}
		if status.MaintenanceOnCooldown {
			totalMaintenanceCoolDown++
		}
		if status.UploadOnCoolDown {
			totalUploadCoolDown++
		}
		statuss = append(statuss, status)
	}
	return skymodules.WorkerPoolStatus{
		NumWorkers:               len(workers),
		TotalDownloadCoolDown:    totalDownloadCoolDown,
		TotalMaintenanceCoolDown: totalMaintenanceCoolDown,
		TotalUploadCoolDown:      totalUploadCoolDown,
		Workers:                  statuss,
	}
}

// callUpdate will grab the set of contracts from the contractor and update the
// worker pool to match, creating new workers and killing existing workers as
// necessary.
func (wp *workerPool) callUpdate() {
	fmt.Println("callUpdate")
	contractSlice := wp.staticRenter.staticHostContractor.Contracts()
	contractMap := make(map[string]skymodules.RenterContract, len(contractSlice))
	for _, contract := range contractSlice {
		if contract.Utility.BadContract {
			// Do not create workers for bad contracts.
			continue
		}
		if wp.staticRenter.staticHostContractor.IsOffline(contract.HostPublicKey) {
			// Do not create workers for offline hosts.
			continue
		}
		contractMap[contract.HostPublicKey.String()] = contract
	}

	// Lock the worker pool for the duration of updating its fields.
	wp.mu.Lock()
	defer wp.mu.Unlock()

	// Replace the update chan if the set changed.
	var setChanged bool
	defer func() {
		if !setChanged {
			return
		}
		oldChan := wp.updateChan
		wp.updateChan = make(chan struct{})
		close(oldChan)
	}()

	// Add a worker for any contract that does not already have a worker.
	for id, contract := range contractMap {
		_, exists := wp.workers[id]
		if exists {
			continue
		}
		setChanged = true
		fmt.Printf("could not find worker %v\n", id)

		// Create a new worker and add it to the map
		w, err := wp.staticRenter.newWorker(contract.HostPublicKey)
		if err != nil {
			wp.staticRenter.staticLog.Println((errors.AddContext(err, fmt.Sprintf("could not create a new worker for host %v", contract.HostPublicKey))))
			continue
		}
		wp.workers[id] = w

		// Start the work loop in a separate goroutine
		err = wp.staticRenter.tg.Launch(w.threadedWorkLoop)
		if err != nil {
			return
		}
		// Start the subscription loop in a separate goroutine.
		// err = wp.staticRenter.tg.Launch(w.threadedSubscriptionLoop)
		// if err != nil {
		// 	return
		// }
	}

	// Remove a worker for any worker that is not in the set of new contracts.
	for id, worker := range wp.workers {
		select {
		case <-wp.staticRenter.tg.StopChan():
			// Release the lock and return to prevent error of trying to close
			// the worker channel after a shutdown
			return
		default:
		}
		_, exists := contractMap[id]
		if exists {
			continue
		}
		setChanged = true
		delete(wp.workers, id)

		// Kill the worker in a goroutine. This avoids locking issues, as
		// wp.mu is currently locked.
		go worker.managedKill()
	}
}

// Worker will return the worker associated with the provided public key.
// If no worker is found, an error will be returned.
func (wp *workerPool) Worker(hostPubKey types.SiaPublicKey) (skymodules.Worker, error) {
	return wp.callWorker(hostPubKey)
}

// callWorker will return the worker associated with the provided public key.
// If no worker is found, an error will be returned.
func (wp *workerPool) callWorker(hostPubKey types.SiaPublicKey) (*worker, error) {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	worker, exists := wp.workers[hostPubKey.String()]
	if !exists {
		return nil, errors.New("worker is not available in the worker pool")
	}
	return worker, nil
}

// WorkerPoolStatus returns the current status of the Renter's worker pool
func (r *Renter) WorkerPoolStatus() (skymodules.WorkerPoolStatus, error) {
	if err := r.tg.Add(); err != nil {
		return skymodules.WorkerPoolStatus{}, err
	}
	defer r.tg.Done()
	return r.staticWorkerPool.callStatus(), nil
}

// callWorkers will safely grab the list of workers in the worker pool. This
// function must be used instead of accessing the worker map directly in any
// situation where the workers are being used as opposed to just counted,
// because it is not safe to use the workers while the worker pool is locked.
func (wp *workerPool) callWorkers() []*worker {
	wp.mu.RLock()
	workers := make([]*worker, 0, len(wp.workers))
	for _, worker := range wp.workers {
		workers = append(workers, worker)
	}
	wp.mu.RUnlock()
	return workers
}

// callNumWorkers returns the number of workers in the worker pool.
func (wp *workerPool) callNumWorkers() int {
	wp.mu.Lock()
	l := len(wp.workers)
	wp.mu.Unlock()
	return l
}

// newWorkerPool will initialize and return a worker pool.
func (r *Renter) newWorkerPool() *workerPool {
	wp := &workerPool{
		workers:      make(map[string]*worker),
		staticRenter: r,
		updateChan:   make(chan struct{}),
	}
	wp.staticRenter.tg.OnStop(func() error {
		wp.mu.RLock()
		for _, w := range wp.workers {
			// Kill the worker in a goroutine. This avoids locking issues, as
			// wp.mu is currently read locked.
			go w.managedKill()
		}
		wp.mu.RUnlock()
		return nil
	})
	wp.callUpdate()
	return wp
}
