package renter

import (
	"sync"
	"time"

	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem"
)

// skylinkManager manages skylink requests
type skylinkManager struct {
	// pruneTimeThreshold is the time threshold for pruning unpin requests.
	pruneTimeThreshold time.Time

	// unpinRequests are requests to unpin a skylink. It is a map of
	// Skylink.String() to a time.Time by when the skylink should be fully
	// unpinned. It is a map of strings instead of skylinks because the FileNode
	// contain a slice of strings and not a slice of Skylinks.
	unpinRequests map[string]time.Time
	mu            sync.Mutex
}

// newSkylinkManager returns a newly initialized skylinkManager
func newSkylinkManager() *skylinkManager {
	return &skylinkManager{
		unpinRequests: make(map[string]time.Time),
	}
}

// callIsUnpinned returns whether or not a FileNode has be requested to be
// unpinned.
func (sm *skylinkManager) callIsUnpinned(fn *filesystem.FileNode) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Check all the skylinks associated with the FileNode. If any have a pending
	// unpin request then we return true.
	for _, skylink := range fn.Metadata().Skylinks {
		// Check if skylink has a pending unpin request.
		if _, ok := sm.unpinRequests[skylink]; ok {
			return true
		}
	}
	return false
}

// callPruneUnpinRequests will prune the skylinkManager's old unpinRequests
func (sm *skylinkManager) callPruneUnpinRequests() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Iterate over the unpinRequests and check for requests that are in the
	// past when compared to the pruneTime
	for sl, t := range sm.unpinRequests {
		// If the unpin request's time is in the past we can remove it from the
		// list.
		if t.Before(sm.pruneTimeThreshold) {
			delete(sm.unpinRequests, sl)
		}
	}
}

// callUpdatePruneTimeThreshold updates the skylinkManager's pruneTimeThreshold
func (sm *skylinkManager) callUpdatePruneTimeThreshold(t time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.pruneTimeThreshold = t
}

// managedAddUnpinRequest adds an unpin request to the skylinkManager
func (sm *skylinkManager) managedAddUnpinRequest(skylink skymodules.Skylink) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Grab the string
	skylinkStr := skylink.String()
	// Check if the request was already submitted. If so, return as to not reset
	// the time.
	_, ok := sm.unpinRequests[skylinkStr]
	if ok {
		// unpin request already submitted
		return
	}

	// Add the unpin request and set the time to twice the healthCheckInterval in
	// the future.
	sm.unpinRequests[skylinkStr] = time.Now().Add(healthCheckInterval * 2)
}

// UnpinSkylink unpins a skylink from the renter by removing the underlying
// siafile.
//
// NOTE: Since the SiaPath is not stored in the SkyfileLayout or the
// SkyfileMetadata, we have to iterate over the entire filesystem to try and
// find the filenode(s) that contain the skylink. We handle this via the bubble
// code and the Renter's skylinkManager.
func (r *Renter) UnpinSkylink(skylink skymodules.Skylink) error {
	err := r.tg.Add()
	if err != nil {
		return err
	}
	defer r.tg.Done()

	// Check if skylink is blocked. If it is we can return early since the bubble
	// code will handle deletion of blocked files.
	if r.staticSkynetBlocklist.IsBlocked(skylink) {
		return ErrSkylinkBlocked
	}

	// Check for dependency injection
	if r.staticDeps.Disrupt("SkipUnpinRequest") {
		return nil
	}

	// Add the unpin request
	r.staticSkylinkManager.managedAddUnpinRequest(skylink)
	return nil
}
