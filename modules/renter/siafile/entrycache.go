package siafile

import (
	"container/heap"
	"fmt"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules"
)

type (
	// entryCache is a data structure which stores siaFileSetEntries which have
	// been removed from the siaFileSet to prevent them from being removed from
	// memory.
	// The cache only contains objects of type siaFileSetEntry instead of
	// SiaFileSetEntry which means there is no threadMap while files are in the
	// cache.
	entryCache struct {
		filesDir string
		maxSize  int
		em       map[modules.SiaPath]*siaFileSetEntry
		eh       entryHeap
		mu       sync.Mutex
	}

	// entryHeap is a min heap that returns the least recently opened entry.
	entryHeap []*siaFileSetEntry
)

// Len implements the heap interface for entryHeap.
func (eh entryHeap) Len() int { return len(eh) }

// Less implements the heap interface for entryHeap.
func (eh entryHeap) Less(i, j int) bool { return eh[i].cacheTime.Before(eh[j].cacheTime) }

// Swap implements the heap interface for entryHeap.
func (eh entryHeap) Swap(i, j int) {
	eh[i], eh[j] = eh[j], eh[i]
	eh[i].cacheIndex = i
	eh[j].cacheIndex = j
}

// Push implements the heap interface for entryHeap.
func (eh *entryHeap) Push(x interface{}) {
	entry := x.(*siaFileSetEntry)
	entry.cacheTime = time.Now()
	entry.cacheIndex = len(*eh)
	*eh = append(*eh, entry)
}

// Pop implements the heap interface for entryHeap.
func (eh *entryHeap) Pop() interface{} {
	entry := (*eh)[len(*eh)-1]
	entry.cacheIndex = -1 // for safety
	*eh = (*eh)[:len(*eh)-1]
	return entry
}

// newEntryCache creates a new cache from a given cache size.
func newEntryCache(filesDir string, cacheSize int) *entryCache {
	ec := &entryCache{
		filesDir: filesDir,
		maxSize:  cacheSize,
		em:       make(map[modules.SiaPath]*siaFileSetEntry),
		eh:       make(entryHeap, 0, cacheSize),
	}
	heap.Init(&ec.eh)
	return ec
}

// Add adds an entry to the cache. If the cache has reached its maximum size,
// old entries will be pruned.
func (ec *entryCache) Add(entry *siaFileSetEntry) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	// Make sure we aren't adding a duplicate.
	sp := entry.SiaPath()
	if _, exists := ec.em[sp]; exists {
		// File is already cached.
		if entry.cacheIndex == -1 {
			build.Critical("entry is supposed to be cached but index is -1")
		}
		return
	}
	// Add the entry to the map and heap and prune the heap afterwards.
	ec.em[sp] = entry
	heap.Push(&ec.eh, entry)
	ec.prune(ec.maxSize)
}

// ChangeMaxSize changes the maximum size of the cache. If the cache has already
// exceeded its new size, the oldest entries will be removed.
func (ec *entryCache) ChangeMaxSize(size int) error {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	// Check for invalid size.
	if size < 0 {
		return fmt.Errorf("cache size can't be negative value %v", size)
	}
	// Set size and prune heap.
	ec.maxSize = size
	ec.prune(ec.maxSize)
	return nil
}

// Exists checks if an entry with the provided siaPath exists within the cache.
func (ec *entryCache) Exists(siaPath modules.SiaPath) bool {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	_, exists := ec.em[siaPath]
	return exists
}

// TryCache tries to grab an entry from the cache by its siaPath. If no entry
// was found 'false' is returned.
func (ec *entryCache) TryCache(siaPath modules.SiaPath) (*siaFileSetEntry, bool) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	// Get the entry from the cache if possible.
	entry, exists := ec.em[siaPath]
	if !exists {
		return nil, false
	}
	// Remove the entry from the map and the heap.
	delete(ec.em, siaPath)
	heap.Remove(&ec.eh, entry.cacheIndex)
	entry.cacheIndex = -1
	// Sanity check length.
	if len(ec.em) != ec.eh.Len() {
		build.Critical("cache map and heap are not the same length", len(ec.em), ec.eh.Len())
	}
	return entry, true
}

// prune pops elements off the heap until the heap has at most size 'size'.
func (ec *entryCache) prune(size int) {
	for ec.eh.Len() > size {
		// Remove from heap
		entry := heap.Pop(&ec.eh).(*siaFileSetEntry)
		entry.cacheIndex = -1
		sp := entry.SiaPath()
		// Remove from map
		if _, ok := ec.em[sp]; !ok {
			build.Critical("Entry not found in cache map", sp)
		}
		delete(ec.em, sp)
	}
	// Sanity check length.
	if len(ec.em) != ec.eh.Len() {
		build.Critical("cache map and heap are not the same length", len(ec.em), ec.eh.Len())
	}
}
