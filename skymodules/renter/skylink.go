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
	err = r.managedHandleIsBlockedCheck(r.tg.StopCtx(), skylink, skymodules.SiaPath{})
	if err != nil {
		return err
	}

	// Check for dependency injection
	if r.staticDeps.Disrupt("SkipUnpinRequest") {
		return nil
	}

	// Add the unpin request
	r.staticSkylinkManager.managedAddUnpinRequest(skylink)
	return nil
}

// managedBlocklistHash returns the hash to be used in the blocklist
func (r *Renter) managedBlocklistHash(ctx context.Context, sl skymodules.Skylink) (crypto.Hash, error) {
	// We want to return the hash of the merkleroot of the V1 skylink. This
	// means for V2 skylinks we need to resolve it first.
	switch {
	case sl.IsSkylinkV1():
		return crypto.HashObject(sl.MerkleRoot()), nil
	case sl.IsSkylinkV2():
		// NOTE: We don't want to check the blocklist while the V2 link
		// is being resolved. This is so a link that is already on the
		// block list could be added again in a large user generated
		// list.
		slv1, _, err := r.managedTryResolveSkylinkV2(ctx, sl, false)
		if err != nil {
			return crypto.Hash{}, errors.AddContext(err, "unable to resolve V2 skylink")
		}
		return crypto.HashObject(slv1.MerkleRoot()), nil
	default:
		build.Critical(ErrInvalidSkylinkVersion)
	}
	return crypto.Hash{}, ErrInvalidSkylinkVersion
}

// managedHandleIsBlockedCheck handles checking if a skylink is blocked. It will
// also handle the deletion of a blocked file if necessary. This method can be
// used for both V1 and V2 skylinks.
func (r *Renter) managedHandleIsBlockedCheck(ctx context.Context, sl skymodules.Skylink, siaPath skymodules.SiaPath) error {
	// Convert skylink to blocklist hash
	hash, err := r.managedBlocklistHash(ctx, sl)
	if err != nil {
		return errors.AddContext(err, "unable to get blocklist hash")
	}
	// Check the Skynet Blocklist
	shouldDelete, isBlocked := r.staticSkynetBlocklist.IsHashBlocked(hash)
	if !isBlocked {
		return nil
	}
	// Check if the skyfile should be deleted
	if shouldDelete && !siaPath.IsEmpty() {
		// Skylink is blocked and the data should be deleted, try and
		// delete file
		deleteErr := r.DeleteFile(siaPath)
		// Don't bother logging an error if the file doesn't exist
		if deleteErr != nil && !errors.Contains(deleteErr, filesystem.ErrNotExist) {
			r.staticLog.Println("WARN: unable to delete blocked skyfile at", siaPath.String())
		}
	}
	// Return that the Skylink is blocked
	return ErrSkylinkBlocked
}

// managedParseBlocklistHashes parses the input hash string slice and returns
// the appropriate hash to be added to the blocklist.
func (r *Renter) managedParseBlocklistHashes(ctx context.Context, hashStrs []string, isHash bool) ([]crypto.Hash, error) {
	hashes := make([]crypto.Hash, len(hashStrs))
	for i, paramStr := range hashStrs {
		var hash crypto.Hash
		// Convert Hash
		if isHash {
			err := hash.LoadString(paramStr)
			if err != nil {
				return nil, errors.AddContext(err, "error parsing hash")
			}
		} else {
			// Convert Skylink
			var skylink skymodules.Skylink
			err := skylink.LoadString(paramStr)
			if err != nil {
				return nil, errors.AddContext(err, "error parsing skylink")
			}
			hash, err = r.managedBlocklistHash(ctx, skylink)
			if err != nil {
				return nil, errors.AddContext(err, "error generating skylink blocklist hash")
			}
		}
		hashes[i] = hash
	}
	return hashes, nil
}
