package renter

import (
	"context"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem"
	"go.sia.tech/siad/crypto"
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

	// Add the unpin request and set the time in the future by twice the target
	// health check interval.
	sm.unpinRequests[skylinkStr] = time.Now().Add(TargetHealthCheckFrequency * 2)
}

// BlocklistHash returns the hash to be used in the blocklist
//
// TODO: Test. Generate a V1 skylink and the a V2 skylink from the V1 skylink.
// Calling this function on them should result in the same hash.
func (r *Renter) BlocklistHash(ctx context.Context, sl skymodules.Skylink) (crypto.Hash, error) {
	err := r.tg.Add()
	if err != nil {
		return crypto.Hash{}, err
	}
	defer r.tg.Done()

	// We want to return the hash of the merkleroot of the V1 skylink. This
	// means for V2 skylinks we need to resolve it first.
	switch {
	case sl.IsSkylinkV1():
		return crypto.Hash(sl.MerkleRoot()), nil
	case sl.IsSkylinkV2():
		slv1, _, err := r.ResolveSkylinkV2(ctx, sl)
		if err != nil {
			return crypto.Hash{}, errors.AddContext(err, "unable to resolve V2 skylink")
		}
		return crypto.Hash(slv1.MerkleRoot()), nil
	default:
		build.Critical(ErrInvalidSkylinkVersion)
	}
	return crypto.Hash{}, ErrInvalidSkylinkVersion
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

	// Check if link is v2.
	if skylink.IsSkylinkV2() {
		return errors.New("can't unpin version 2 skylink")
	}

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
