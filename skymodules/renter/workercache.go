package renter

import (
	"sync/atomic"
	"time"
	"unsafe"

	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/types"
)

var (
	// workerCacheUpdateFrequency specifies how much time must pass before the
	// worker updates its cache.
	workerCacheUpdateFrequency = build.Select(build.Var{
		Dev:      time.Second * 5,
		Standard: time.Minute,
		Testing:  time.Second,
	}).(time.Duration)
)

type (
	// workerCache contains all of the cached values for the worker. Every field
	// must be static because this object is saved and loaded using
	// atomic.Pointer.
	workerCache struct {
		staticBlockHeight     types.BlockHeight
		staticContractID      types.FileContractID
		staticContractUtility skymodules.ContractUtility
		staticHostVersion     string
		staticRenterAllowance skymodules.Allowance
		staticHostMuxAddress  string
		staticMaliciousHost   bool
		staticSynced          bool

		staticLastUpdate time.Time
	}
)

// managedUpdateCache performs the actual worker cache update. The function is
// managed because it calls exported functions on the hostdb and on the
// consensus set.
//
// NOTE: The concurrency around the atomicCacheUpdating value is a little bit
// annoying. You can't just use 'defer atmoic.StoreUint64()` because you need to
// update the value before calling tg.AfterFunc at the end of the function.
func (w *worker) managedUpdateCache() {
	// Check if there is already a cache update in progress. If not, atomically
	// signal that a cache update is in progress.
	if !atomic.CompareAndSwapUint64(&w.atomicCacheUpdating, 0, 1) {
		return
	}

	// Grab the host to check the version.
	host, ok, err := w.staticRenter.staticHostDB.Host(w.staticHostPubKey)
	if !ok || err != nil {
		w.staticRenter.staticLog.Printf("Worker %v could not update the cache, hostdb found host %v, with error: %v, worker being killed", w.staticHostPubKeyStr, ok, err)
		w.managedKill()
		atomic.StoreUint64(&w.atomicCacheUpdating, 0)
		return
	}

	// Grab the renter contract from the host contractor.
	renterContract, exists := w.staticRenter.staticHostContractor.ContractByPublicKey(w.staticHostPubKey)
	if !exists {
		w.staticRenter.staticLog.Printf("Worker %v could not update the cache, host not found in contractor, worker being killed", w.staticHostPubKeyStr)
		w.managedKill()
		atomic.StoreUint64(&w.atomicCacheUpdating, 0)
		return
	}

	// Check if the host is potentially malicious.
	malicious, err := w.staticRenter.staticHostDB.IsMalicious(host)
	if err != nil {
		w.staticRenter.staticLog.Printf("Worker %v could not update the cache, hostdb failed to check if host is malicious with error: %v, worker being killed", w.staticHostPubKeyStr, err)
		w.managedKill()
		atomic.StoreUint64(&w.atomicCacheUpdating, 0)
		return
	}

	// Create the cache object.
	newCache := &workerCache{
		staticBlockHeight:     w.staticRenter.staticConsensusSet.Height(),
		staticContractID:      renterContract.ID,
		staticContractUtility: renterContract.Utility,
		staticHostMuxAddress:  host.SiaMuxAddress(),
		staticMaliciousHost:   malicious,
		staticHostVersion:     host.Version,
		staticRenterAllowance: w.staticRenter.staticHostContractor.Allowance(),
		staticSynced:          w.staticRenter.staticConsensusSet.Synced(),

		staticLastUpdate: time.Now(),
	}

	// Atomically store the cache object in the worker.
	ptr := unsafe.Pointer(newCache)
	atomic.StorePointer(&w.atomicCache, ptr)

	// Wake the worker when the cache needs to be updated again. Note that we
	// need to signal the cache update is complete before waking the worker,
	// just in case a bizarre race condition means that the worker wakes
	// immediately, then sees that an update is in progress, then fails to
	// update its cache.
	atomic.StoreUint64(&w.atomicCacheUpdating, 0)
	w.staticTG.AfterFunc(workerCacheUpdateFrequency, func() {
		w.staticWake()
	})
}

// newCache will initialize an unitialized cache on the worker.
func (w *worker) newCache() {
	if w.staticCache() != nil {
		w.staticRenter.staticLog.Critical("creating a new cache one already exists")
	}
	ptr := unsafe.Pointer(new(workerCache))
	atomic.StorePointer(&w.atomicCache, ptr)
}

// staticTryUpdateCache will perform a cache update on the worker.
//
// 'false' will be returned if the cache cannot be updated, signaling that the
// worker should exit.
func (w *worker) staticTryUpdateCache() {
	// Check if an update is necessary.
	cache := w.staticCache()
	if cache != nil && time.Since(cache.staticLastUpdate) < workerCacheUpdateFrequency {
		return
	}

	// Get the new cache in a goroutine. This is because the cache update grabs
	// a lock on the consensus object, which can sometimes take a while if there
	// are new blocks being processed or a reorg being processed.
	err := w.staticTG.Launch(w.managedUpdateCache)
	if err != nil {
		w.staticRenter.staticLog.Print("staticTryUpdateCache: failed to launch cache update", err)
	}
}

// staticCache returns the current worker cache object.
func (w *worker) staticCache() *workerCache {
	ptr := atomic.LoadPointer(&w.atomicCache)
	return (*workerCache)(ptr)
}
