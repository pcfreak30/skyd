package renter

import (
	"container/list"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

// PersistedLRU is the interface for a persistedLRU. This is useful for mocking
// the lru during testing.
type PersistedLRU interface {
	// Get tries to fetch data from the cache. If the data is not cached, false is
	// returned.
	Get(dsid skymodules.DataSourceID, sectionIndex uint64) ([]byte, bool, error)
	// Put adds a new section to the cache.
	Put(dsid skymodules.DataSourceID, sectionIndex uint64, data []byte) error
}

type (
	// cachedDataSource describes a cached datasource which can contain multiple
	// sections.
	cachedDataSource struct {
		staticID  skymodules.DataSourceID
		staticLRU *persistedLRU

		sections map[uint64]struct{}
		mu       sync.Mutex
	}

	// lruElement describes an element within the LRU.
	lruElement struct {
		staticDSID         skymodules.DataSourceID
		staticSectionIndex uint64
	}

	// persistedLRU is the LRU itself. It stores cached elements in a tree
	// structure on disk.
	persistedLRU struct {
		staticPath string

		staticLRU   *list.List
		lruElements map[skymodules.DataSourceID]map[uint64]*list.Element

		cachedSize         int64
		staticMaxCacheSize int64

		dataSources map[skymodules.DataSourceID]*cachedDataSource
		mu          sync.Mutex
	}
)

// freeSection removes a section from the datasource and deletes it from disk.
// It also returns the deleted files length and whether it was the last section.
func (ds *cachedDataSource) freeSection(index uint64) (int64, bool, error) {
	_, exists := ds.sections[index]
	if !exists {
		// already freed.
		return 0, true, nil
	}
	delete(ds.sections, index)

	// Remove the section from disk.
	length, err := ds.staticLRU.staticRemoveCacheFile(ds.staticID, index)
	return length, len(ds.sections) == 0, err
}

// get returns some cached data from a datasource. If the data isn't available,
// it returns false.
func (ds *cachedDataSource) get(dsid skymodules.DataSourceID, sectionIndex uint64) (_ []byte, _ bool, err error) {
	_, exists := ds.sections[sectionIndex]
	if !exists {
		return nil, false, nil
	}

	// Open the cache file.
	cacheFile, err := ds.staticLRU.staticOpenCacheFile(dsid, sectionIndex)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		err = errors.Compose(err, cacheFile.Close())
	}()

	// Read the section.
	data, err := ioutil.ReadAll(cacheFile)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// newSection creates a new section within the datasource.
func (ds *cachedDataSource) newSection(index uint64) {
	_, exists := ds.sections[index]
	if exists {
		build.Critical("adding duplicate section to data source")
	}
	ds.sections[index] = struct{}{}
}

// put places some data to be cached into a data source at the given section
// index.
func (ds *cachedDataSource) put(dsid skymodules.DataSourceID, sectionIndex uint64, data []byte) (_ bool, err error) {
	// Check if the section is already cached.
	_, exists := ds.sections[sectionIndex]
	if exists {
		return false, nil
	}

	// Open the cache file.
	cacheFile, err := ds.staticLRU.staticOpenCacheFile(dsid, sectionIndex)
	if err != nil {
		return false, err
	}
	// Cleanup.
	defer func() {
		err = errors.Compose(err, cacheFile.Close())
		if err != nil {
			ds.freeSection(sectionIndex)
		}
	}()
	// Create the section and write it to the file.
	ds.newSection(sectionIndex)
	_, err = cacheFile.Write(data)
	if err != nil {
		return false, err
	}
	return true, nil
}

// newPersistedLRU creates a new LRU at the given root path with the given max
// size.
func newPersistedLRU(path string, maxSize uint64) (*persistedLRU, error) {
	// Remove root dir to prune any existing cached elements.
	if err := os.RemoveAll(path); err != nil {
		return nil, err
	}
	// Create root dir.
	if err := os.MkdirAll(path, skymodules.DefaultDirPerm); err != nil {
		return nil, err
	}
	return &persistedLRU{
		dataSources:        make(map[skymodules.DataSourceID]*cachedDataSource),
		lruElements:        make(map[skymodules.DataSourceID]map[uint64]*list.Element),
		staticMaxCacheSize: int64(maxSize),
		staticLRU:          list.New(),
		staticPath:         path,
	}, nil
}

// managedAcquireCreateDataSource is a helper method to correctly acquire or
// create and acquire a datasource.
func (lru *persistedLRU) managedAcquireCreateDataSource(dsid skymodules.DataSourceID) *cachedDataSource {
	for {
		lru.mu.Lock()
		ds, exists := lru.dataSources[dsid]
		if !exists {
			ds = lru.staticNewCachedDataSource(dsid)
			lru.dataSources[dsid] = ds
		}
		lru.mu.Unlock()
		ds.mu.Lock()

		lru.mu.Lock()
		ds2, exists := lru.dataSources[dsid]
		lru.mu.Unlock()
		if !exists || ds2 != ds {
			ds.mu.Unlock()
			continue // try again
		}
		return ds
	}
}

// managedAcquireDataSource is a helper method to correctly lock a datasource.
func (lru *persistedLRU) managedAcquireDataSource(dsid skymodules.DataSourceID) (*cachedDataSource, bool) {
	lru.mu.Lock()
	ds, exists := lru.dataSources[dsid]
	lru.mu.Unlock()
	if !exists {
		return nil, false
	}
	ds.mu.Lock()

	lru.mu.Lock()
	ds2, exists := lru.dataSources[dsid]
	lru.mu.Unlock()
	if !exists || ds2 != ds {
		ds.mu.Unlock()
		return nil, false
	}
	return ds, true
}

// managedDeleteDataSource is a helper method to correctly unlock and delete a
// datasoure from the LRU.
func (lru *persistedLRU) managedDeleteDataSource(ds *cachedDataSource) {
	lru.mu.Lock()
	ds, exists := lru.dataSources[ds.staticID]
	if !exists {
		lru.mu.Unlock()
		build.Critical("trying to delete already deleted data source")
		return
	}
	delete(lru.dataSources, ds.staticID)
	lru.mu.Unlock()
	ds.mu.Unlock()
}

// managedReturnDataSource is a helper method to correctly unlock a datasource.
func (lru *persistedLRU) managedReturnDataSource(ds *cachedDataSource) {
	lru.mu.Lock()
	ds, exists := lru.dataSources[ds.staticID]
	lru.mu.Unlock()
	if !exists {
		build.Critical("no data source with that id")
	}
	ds.mu.Unlock()
}

// managedPruneLRU prunes the least recently used element from the persistedLRU.
func (lru *persistedLRU) managedPruneLRU() (int64, bool, error) {
	lru.mu.Lock()
	ele := lru.staticLRU.Back()
	if ele == nil {
		lru.mu.Unlock()
		return 0, false, nil
	}
	lru.staticLRU.Remove(ele)

	toPrune := ele.Value.(lruElement)

	// Cleanup the lru's maps first.
	sections, exists := lru.lruElements[toPrune.staticDSID]
	if exists {
		// Delete the element in the inner map.
		delete(sections, toPrune.staticSectionIndex)
	}
	if len(sections) == 0 {
		delete(lru.lruElements, toPrune.staticDSID)
	}

	// Then find the datasource.
	lru.mu.Unlock()
	ds, exists := lru.managedAcquireDataSource(toPrune.staticDSID)
	if !exists {
		// no ds
		return 0, true, nil
	}
	length, deleted, err := ds.freeSection(toPrune.staticSectionIndex)

	// Delete the datasource if it was marked as deleted.
	if deleted {
		lru.managedDeleteDataSource(ds)
	} else {
		lru.managedReturnDataSource(ds)
	}
	return length, true, err
}

// staticNewCachedDataSource creates a new cachedDataSource object.
func (lru *persistedLRU) staticNewCachedDataSource(id skymodules.DataSourceID) *cachedDataSource {
	return &cachedDataSource{
		staticID:  id,
		staticLRU: lru,
		sections:  make(map[uint64]struct{}),
	}
}

// staticDataSourceIDToPath is a helper method to get the path for a given
// datasource and section.
func (lru *persistedLRU) staticDataSourceIDToPath(dsid skymodules.DataSourceID, sectionIndex uint64) string {
	s := hex.EncodeToString(dsid[:])
	// Using a depth of 2 - approach will result in 65536 folders on the
	// bottom layer of the tree and twice that in total. Assuming a 4kib
	// block size of the filesystem, that's and approximately 500 mib folder
	// overhead if all the folders exist. If we decide to increase the depth
	// we might want to add support for deleting empty folders again but
	// that would add some locking complexity.
	return filepath.Join(lru.staticPath, s[0:2], s[2:4], s[4:], fmt.Sprint(sectionIndex)+".dat")
}

// staticOpenCacheFile is a helper method to open a cache file for a given
// datasource and section.
func (lru *persistedLRU) staticOpenCacheFile(dsid skymodules.DataSourceID, sectionIndex uint64) (*os.File, error) {
	path := lru.staticDataSourceIDToPath(dsid, sectionIndex)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, skymodules.DefaultDirPerm); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE, skymodules.DefaultFilePerm)
}

// staticRemoveCacheFile removes a cache file from disk and returns its size.
func (lru *persistedLRU) staticRemoveCacheFile(dsid skymodules.DataSourceID, sectionIndex uint64) (int64, error) {
	path := lru.staticDataSourceIDToPath(dsid, sectionIndex)
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), os.Remove(path)
}

// Get tries to fetch data from the cache. If the data is not cached, false is
// returned.
func (lru *persistedLRU) Get(dsid skymodules.DataSourceID, sectionIndex uint64) ([]byte, bool, error) {
	ds, exists := lru.managedAcquireDataSource(dsid)
	if !exists {
		return nil, false, nil
	}
	data, found, err := ds.get(dsid, sectionIndex)
	if err != nil {
		lru.managedReturnDataSource(ds)
		return nil, false, err
	}
	lru.managedReturnDataSource(ds)

	// Refresh the cache if we got the data cached.
	if found {
		lru.managedRefreshCachedEntry(dsid, sectionIndex) // TODO enable
	}
	return data, found, nil
}

// Put adds a new section to the cache.
func (lru *persistedLRU) Put(dsid skymodules.DataSourceID, sectionIndex uint64, data []byte) error {
	// Get the cached datasource or create if possible.
	ds := lru.managedAcquireCreateDataSource(dsid)

	// Add the section to the source.
	added, err := ds.put(dsid, sectionIndex, data)
	if err != nil {
		return err
	}

	// Unlock ds.
	lru.managedReturnDataSource(ds)

	// If it was added, we add the length of the added data to the sum.
	if added {
		lru.managedAddCachedEntry(dsid, sectionIndex, int64(len(data)))
	}

	// Update the lru.
	lru.managedTryPruneData()
	return nil
}

// managedAddCachedEntry adds a new entry to cache to the LRU.
func (lru *persistedLRU) managedAddCachedEntry(dsid skymodules.DataSourceID, sectionIndex uint64, size int64) {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	elements, exists := lru.lruElements[dsid]
	if !exists {
		elements = make(map[uint64]*list.Element)
		lru.lruElements[dsid] = elements
	}
	_, exists = elements[sectionIndex]
	if !exists {
		// Push a new element.
		elements[sectionIndex] = lru.staticLRU.PushFront(lruElement{
			staticDSID:         dsid,
			staticSectionIndex: sectionIndex,
		})
		// Increment the cachedSize.
		lru.cachedSize += size
	}
}

// managedRefreshCachedEntry moves a cached element to the front of the LRU.
func (lru *persistedLRU) managedRefreshCachedEntry(dsid skymodules.DataSourceID, sectionIndex uint64) {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	elements, exists := lru.lruElements[dsid]
	if !exists {
		return
	}
	ele, exists := elements[sectionIndex]
	if exists {
		// Move element to the front.
		lru.staticLRU.MoveToFront(ele)
	}
}

// managedTryPruneData checks the current cache size and if necessary, prunes
// it. To avoid holding a lock while doing disk i/o, it will assume that the
// pruning is successful by subtracting the amount of data to prune from the
// cache size right away. After the pruning it will adjust the cache size again
// using the actual amount of pruned data.
func (lru *persistedLRU) managedTryPruneData() error {
	// Figure out how much data we need to prune and assume that it is
	// pruned.
	lru.mu.Lock()
	toPrune := lru.cachedSize - lru.staticMaxCacheSize
	if toPrune <= 0 {
		lru.mu.Unlock()
		return nil
	}
	lru.cachedSize -= toPrune
	lru.mu.Unlock()

	// Prune at least toPrune data. If we encounter an error we abort but we
	// can't return right away since we still need to adjust the cache size.
	var err error
	for toPrune > 0 {
		var pruned int64
		var more bool
		pruned, more, err = lru.managedPruneLRU()
		if err != nil {
			return err
		}
		if pruned == 0 && !more {
			break
		}
		toPrune -= pruned
	}

	// Adjust the cachedSize now that we know how much data we pruned
	// exactly.
	lru.mu.Lock()
	defer lru.mu.Unlock()
	if toPrune != 0 {
		lru.cachedSize += toPrune
		if lru.cachedSize < 0 {
			lru.cachedSize = 0
			build.Critical("managedAddCachedData: negative cachedSize after prune")
		}
	}
	return err
}
