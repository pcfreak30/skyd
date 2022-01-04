package renter

import (
	"container/list"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"

	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
)

type cachedDataSection struct {
	staticOffset int64 // offset within file
}

type cachedDataSource struct {
	staticSections map[uint64]cachedDataSection
	unusedSections []cachedDataSection

	staticSectionSize int
}

func (ds *cachedDataSource) newSection(index uint64) int64 {
	// Try to use unused section first.
	var section cachedDataSection
	if len(ds.unusedSections) > 0 {
		section = ds.unusedSections[len(ds.unusedSections)-1]
		ds.unusedSections = ds.unusedSections[:len(ds.unusedSections)-1]
	} else {
		// Otherwise create a new one.
		section = cachedDataSection{
			staticOffset: int64(len(ds.staticSections) * ds.staticSectionSize),
		}
	}
	_, exists := ds.staticSections[index]
	if exists {
		build.Critical("adding duplicate section to data source")
	}
	ds.staticSections[index] = section
	return section.staticOffset
}

func (ds *cachedDataSource) freeSection(index uint64) {
	section, exists := ds.staticSections[index]
	if !exists {
		build.Critical("trying to free uncached section")
		return
	}
	ds.unusedSections = append(ds.unusedSections, section)
	delete(ds.staticSections, index)
}

type persistedLRU struct {
	// lru is the list of cached elements sorted from most-recently to
	// least-recently used.
	staticLRU *list.List

	staticPath        string
	staticSectionSize uint64

	staticDataSources map[crypto.Hash]*cachedDataSource

	mu sync.Mutex
}

func newCachedDataSource(sectionSize int) *cachedDataSource {
	return &cachedDataSource{
		staticSections:    make(map[uint64]cachedDataSection),
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
		staticDataSources: make(map[crypto.Hash]*cachedDataSource),
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

func (lru *persistedLRU) Get(dsid crypto.Hash, sectionIndex uint64, data []byte) ([]byte, bool, error) {
	panic("not implemented yet")
}

func (lru *persistedLRU) Put(dsid crypto.Hash, sectionIndex uint64, data []byte) error {
	// Get the cached datasource or create if possible.
	ds, exists := lru.staticDataSources[dsid]
	if !exists {
		// If not, create a new one.
		ds = newCachedDataSource(int(lru.staticSectionSize))
	}

	// Check if the section is already cached.
	_, exists = ds.staticSections[sectionIndex]
	if !exists {
		// Open the cache file.
		cacheFile, err := lru.staticOpenCacheFile(dsid)
		if err != nil {
			return err
		}
		// Create the section and write it to the file.
		offset := ds.newSection(sectionIndex)
		_, err = cacheFile.WriteAt(data, offset)
		if err != nil {
			ds.freeSection(sectionIndex)
			return err
		}
		if err := cacheFile.Close(); err != nil {
			ds.freeSection(sectionIndex)
			return err
		}
	}

	// TODO: Update the lru.
	return nil
}
