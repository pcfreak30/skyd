package renter

import (
	"sync"

	"gitlab.com/NebulousLabs/fastrand"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

type (
	// registryRevisionCache is a helper type to cache information about registry values
	// in memory. It decides randomly which entries to evict to make it more
	// unpredictable for the host.
	registryRevisionCache struct {
		entryMap   map[modules.RegistryEntryID]*cachedEntry
		entryList  []*cachedEntry
		maxEntries uint64
		staticHPK  types.SiaPublicKey
		mu         sync.Mutex
	}

	// cachedEntry describes a single cached entry. To make sure we can cache as
	// many entries as possible, this only contains the necessary information.
	cachedEntry struct {
		key modules.RegistryEntryID
		rv  modules.RegistryValue
	}
)

// cachedEntryEstimatedSize is the estimated size of a cachedEntry in memory.
// hash + revision + overhead of 2 pointers
const cachedEntryEstimatedSize = 32 + 8 + 16

// newRegistryCache creates a new registry cache.
func newRegistryCache(size uint64, hpk types.SiaPublicKey) *registryRevisionCache {
	return &registryRevisionCache{
		entryMap:   make(map[modules.RegistryEntryID]*cachedEntry),
		entryList:  nil,
		maxEntries: size / cachedEntryEstimatedSize,
		staticHPK:  hpk,
	}
}

// Delete deletes an entry from the cache without replacing it.
func (rc *registryRevisionCache) Delete(sid modules.RegistryEntryID) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	entry, exists := rc.entryMap[sid]
	if !exists {
		return
	}
	delete(rc.entryMap, sid)
	for idx := range rc.entryList {
		if rc.entryList[idx] != entry {
			continue
		}
		rc.entryList[idx] = rc.entryList[len(rc.entryList)-1]
		rc.entryList = rc.entryList[:len(rc.entryList)-1]
		break
	}
}

// Get fetches an entry from the cache.
func (rc *registryRevisionCache) Get(sid modules.RegistryEntryID) (modules.RegistryValue, bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	cachedEntry, exists := rc.entryMap[sid]
	if !exists {
		return modules.RegistryValue{}, false
	}
	return cachedEntry.rv, true
}

// Set sets an entry in the registry. When 'force' is false, settings a lower
// revision number will be a no-op.
func (rc *registryRevisionCache) Set(sid modules.RegistryEntryID, rv modules.SignedRegistryValue, force bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Check if entry already exists.
	ce, exists := rc.entryMap[sid]

	// Check if the entry is preferable over the known one.
	var update bool
	if exists {
		update, _ = ce.rv.ShouldUpdateWith(&rv.RegistryValue, rc.staticHPK)
	}

	// If it does, update the revision.
	if exists && (update || force) {
		ce.rv = rv.RegistryValue
		return
	} else if exists {
		return
	}

	// If it doesn't, create a new one.
	ce = &cachedEntry{
		key: sid,
		rv:  rv.RegistryValue,
	}
	rc.entryMap[sid] = ce
	rc.entryList = append(rc.entryList, ce)

	// Make sure we stay within maxEntries.
	for uint64(len(rc.entryList)) > rc.maxEntries {
		// Figure out which entry to delete.
		idx := fastrand.Intn(len(rc.entryList))
		toDelete := rc.entryList[idx]

		// Delete it from the map.
		delete(rc.entryMap, toDelete.key)

		// Delete it from the list.
		rc.entryList[idx] = rc.entryList[len(rc.entryList)-1]
		rc.entryList = rc.entryList[:len(rc.entryList)-1]
	}
}
