package renter

import (
	"container/list"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
)

type cachedDataSection struct {
	length       int
	staticOffset int64 // offset within file
}

type cachedDataSource struct {
	staticID          crypto.Hash
	staticLRU         *persistedLRU
	staticSectionSize int

	deleted        bool
	sections       map[uint64]cachedDataSection
	unusedSections []cachedDataSection
	mu             sync.Mutex
}

func (ds *cachedDataSource) newSection(index uint64, length int) int64 {
	// Try to use unused section first.
	var section cachedDataSection
	if len(ds.unusedSections) > 0 {
		section = ds.unusedSections[len(ds.unusedSections)-1]
		ds.unusedSections = ds.unusedSections[:len(ds.unusedSections)-1]
	} else {
		// Otherwise create a new one.
		section = cachedDataSection{
			staticOffset: int64(len(ds.sections) * ds.staticSectionSize),
		}
	}
	section.length = length
	_, exists := ds.sections[index]
	if exists {
		build.Critical("adding duplicate section to data source")
	}
	ds.sections[index] = section
	return section.staticOffset
}

func (ds *cachedDataSource) managedFreeSection(index uint64) (int64, error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	return ds.freeSection(index)
}

func (ds *cachedDataSource) freeSection(index uint64) (int64, error) {
	section, exists := ds.sections[index]
	if !exists {
		build.Critical("trying to free uncached section")
		return 0, nil
	}
	ds.unusedSections = append(ds.unusedSections, section)
	delete(ds.sections, index)

	var err error
	if len(ds.sections) == 0 {
		err = ds.staticLRU.staticRemoveCacheFile(ds.staticID)
		ds.deleted = true
	}
	return int64(section.length), err
}

type persistedLRU struct {
	staticPath        string
	staticSectionSize uint64

	staticLRU   *list.List
	lruElements map[crypto.Hash]map[uint64]*list.Element

	cachedSize         int64
	staticMaxCacheSize int64

	dataSources map[crypto.Hash]*cachedDataSource
	mu          sync.Mutex
}

func (lru *persistedLRU) managedPruneLRU() (int64, bool, error) {
	ele := lru.staticLRU.Front()
	if ele == nil {
		return 0, false, nil
	}
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
	ds, exists := lru.dataSources[toPrune.staticDSID]
	if !exists {
		return 0, true, nil
	}
	ds.mu.Lock()
	length, err := ds.freeSection(toPrune.staticSectionIndex)
	ds.mu.Unlock()
	return length, true, err
}

func (lru *persistedLRU) staticNewCachedDataSource(sectionSize int) *cachedDataSource {
	return &cachedDataSource{
		staticLRU:         lru,
		sections:          make(map[uint64]cachedDataSection),
		staticSectionSize: sectionSize,
	}
}

func newPersistedLRU(path string, sectionSize uint64) (*persistedLRU, error) {
	// Remove root dir to prune any existing cached elements.
	if err := os.RemoveAll(path); err != nil {
		return nil, err
	}
	// Create root dir.
	if err := os.MkdirAll(path, skymodules.DefaultDirPerm); err != nil {
		return nil, err
	}
	return &persistedLRU{
		dataSources:       make(map[crypto.Hash]*cachedDataSource),
		lruElements:       make(map[crypto.Hash]map[uint64]*list.Element),
		staticLRU:         list.New(),
		staticPath:        path,
		staticSectionSize: sectionSize,
	}, nil
}

func (lru *persistedLRU) staticDataSourceIDToPath(dsid crypto.Hash) string {
	s := hex.EncodeToString(dsid[:])
	return filepath.Join(lru.staticPath, s[0:2], s[2:4], s[4:6], s[6:8], s[8:]+".dat")
}

func (lru *persistedLRU) staticOpenCacheFile(dsid crypto.Hash) (*os.File, error) {
	path := lru.staticDataSourceIDToPath(dsid)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, skymodules.DefaultDirPerm); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE, skymodules.DefaultFilePerm)
}

func (lru *persistedLRU) staticRemoveCacheFile(dsid crypto.Hash) error {
	// TODO: maybe also remove potentially empy folders.
	return os.Remove(lru.staticDataSourceIDToPath(dsid))
}

func (lru *persistedLRU) Get(dsid crypto.Hash, sectionIndex uint64) ([]byte, bool, error) {
	lru.mu.Lock()
	ds, exists := lru.dataSources[dsid]
	lru.mu.Unlock()
	if !exists {
		return nil, false, nil
	}
	data, found, err := ds.managedGet(dsid, sectionIndex)
	if err != nil {
		return nil, false, err
	}
	// Refresh the cache if we got the data cached.
	if found {
		lru.managedRefreshCachedEntry(dsid, sectionIndex)
	}
	return data, found, nil
}

type lruElement struct {
	staticDSID         crypto.Hash
	staticSectionIndex uint64
}

func (lru *persistedLRU) Put(dsid crypto.Hash, sectionIndex uint64, data []byte) error {
	// Get the cached datasource or create if possible.
	lru.mu.Lock()
	ds, exists := lru.dataSources[dsid]
	if !exists {
		// If not, create a new one.
		ds = lru.staticNewCachedDataSource(int(lru.staticSectionSize))
		lru.dataSources[dsid] = ds
	}
	lru.mu.Unlock()

	// Add the section to the source.
	added, err := ds.managedPut(dsid, sectionIndex, data)
	if err != nil {
		return err
	}

	// If it was added, we add the length of the added data to the sum.
	if added {
		lru.managedAddCachedData(int64(len(data)))
	}

	// Update the lru.
	lru.managedRefreshCachedEntry(dsid, sectionIndex)
	return nil
}

func (lru *persistedLRU) managedAddCachedData(size int64) error {
	// Figure out how much data we need to prune and assume that it is
	// pruned.
	lru.mu.Lock()
	lru.cachedSize += size
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
	if toPrune > 0 && err == nil {
		build.Critical("managedAddCachedData: ran out of entries to prune")
	}
	if toPrune != 0 {
		lru.cachedSize += toPrune
		if lru.cachedSize < 0 {
			lru.cachedSize = 0
			build.Critical("managedAddCachedData: negative cachedSize after prune")
		}
	}
	return err
}

func (lru *persistedLRU) managedRefreshCachedEntry(dsid crypto.Hash, sectionIndex uint64) {
	lru.mu.Lock()
	elements, exists := lru.lruElements[dsid]
	if !exists {
		elements = make(map[uint64]*list.Element)
		lru.lruElements[dsid] = elements
	}
	ele, exists := elements[sectionIndex]
	if exists {
		// Remove element and add it at the front.
		lru.staticLRU.Remove(ele)
		lru.staticLRU.PushFront(ele.Value)
	} else {
		// Push a new element.
		ele = lru.staticLRU.PushFront(lruElement{
			staticDSID:         dsid,
			staticSectionIndex: sectionIndex,
		})
		elements[sectionIndex] = ele
	}
	lru.mu.Unlock()
}

func (ds *cachedDataSource) managedPut(dsid crypto.Hash, sectionIndex uint64, data []byte) (_ bool, err error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	if len(data) > ds.staticSectionSize {
		err := fmt.Errorf("data length out-of-bounds %v > %v", len(data), ds.staticSectionSize)
		build.Critical(err)
		return false, err
	}

	// Check if the data source was deleted already. In that case we don't
	// use it anymore.
	if ds.deleted {
		return false, errors.New("data source has been deleted")
	}

	// Check if the section is already cached.
	_, exists := ds.sections[sectionIndex]
	if exists {
		return false, nil
	}

	// Open the cache file.
	cacheFile, err := ds.staticLRU.staticOpenCacheFile(dsid)
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
	offset := ds.newSection(sectionIndex, len(data))
	_, err = cacheFile.WriteAt(data, offset)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (ds *cachedDataSource) managedGet(dsid crypto.Hash, sectionIndex uint64) (_ []byte, _ bool, err error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	// Check if the data source was deleted already.
	if ds.deleted {
		return nil, false, nil
	}

	section, exists := ds.sections[sectionIndex]
	if !exists {
		return nil, false, nil
	}

	// Open the cache file.
	cacheFile, err := ds.staticLRU.staticOpenCacheFile(dsid)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		err = errors.Compose(err, cacheFile.Close())
	}()

	// Read the section.
	data := make([]byte, section.length)
	_, err = cacheFile.ReadAt(data, section.staticOffset)
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	return data, true, nil
}
