package siafile

import (
	"container/heap"
	"time"

	"gitlab.com/NebulousLabs/Sia/modules"
)

type (
	// entryCache is a data structure which stores siaFileSetEntries which have
	// been removed from the siaFileSet to prevent them from being removed from
	// memory.
	entryCache struct {
		maxSize int
		em      map[modules.SiaPath]*siaFileSetEntry
		eh      entryHeap
	}

	// entryHeap is a min heap that returns the least recently opened entry.
	entryHeap []*siaFileSetEntry
)

// Len implements the heap interface for entryHeap.
func (eh entryHeap) Len() int { return len(eh) }

// Less implements the heap interface for entryHeap.
func (eh entryHeap) Less(i, j int) bool { return eh[i].lastOpened.Before(eh[j].lastOpened) }

// Swap implements the heap interface for entryHeap.
func (eh entryHeap) Swap(i, j int) {
	eh[i], eh[j] = eh[j], eh[i]
	eh[i].heapIndex = i
	eh[j].heapIndex = j
}

// Push implements the heap interface for entryHeap.
func (eh *entryHeap) Push(x interface{}) {
	entry := x.(*siaFileSetEntry)
	entry.heapIndex = len(*eh)
	*eh = append(*eh, entry)
}

// Pop implements the heap interface for entryHeap.
func (eh *entryHeap) Pop() interface{} {
	entry := (*eh)[len(*eh)-1]
	entry.heapIndex = -1 // for safety
	*eh = (*eh)[:len(*eh)-1]
	return entry
}

// updateTimestamp updates the timestamp of the given entry to the current time.
func (eh *entryHeap) updateTimestamp(entry *siaFileSetEntry) {
	entry.lastOpened = time.Now()
	heap.Fix(eh, entry.heapIndex)
}

// newEntryCache creates a new cache from a given cache size.
func newEntryCache(cacheSize int) *entryCache {
	ec := &entryCache{
		maxSize: cacheSize,
		em:      make(map[modules.SiaPath]*siaFileSetEntry),
		eh:      make(entryHeap, 0, cacheSize),
	}
	heap.Init(&ec.eh)
	return ec
}

// Add adds an entry to the cache. If the cache has reached its maximum size,
// old entries will be pruned.
func (*entryCache) Add(entry *siaFileSetEntry) {
	panic("not implemented yet")
}

// ChangeMaxSize changes the maximum size of the cache. If the cache has already
// exceeded its new size, the oldest entries will be removed.
func (*entryCache) ChangeMaxSize(size int) {
	panic("not implemented yet")
}

// Exists checks if an entry with the provided siaPath exists within the cache.
func (*entryCache) Exists(siaPath modules.SiaPath) bool {
	panic("not implemented yet")
}

// TryCache tries to grab an entry from the cache by its siaPath. If no entry
// was found 'false' is returned.
func (*entryCache) TryCache(siaPath modules.SiaPath) (*siaFileSetEntry, bool) {
	panic("not implemented yet")
}
