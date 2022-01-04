package renter

import (
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
	staticLRU         *persistedLRU
	staticSectionSize int

	deleted        bool
	staticSections map[uint64]cachedDataSection
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
			staticOffset: int64(len(ds.staticSections) * ds.staticSectionSize),
		}
	}
	section.length = length
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
	staticPath        string
	staticSectionSize uint64

	dataSources map[crypto.Hash]*cachedDataSource
	mu          sync.Mutex
}

func (lru *persistedLRU) staticNewCachedDataSource(sectionSize int) *cachedDataSource {
	return &cachedDataSource{
		staticLRU:         lru,
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
		dataSources:       make(map[crypto.Hash]*cachedDataSource),
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
	return ds.managedGet(dsid, sectionIndex)
}

func (lru *persistedLRU) Put(dsid crypto.Hash, sectionIndex uint64, data []byte) error {
	// TODO: add locking

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
	if err := ds.managedPut(dsid, sectionIndex, data); err != nil {
		return err
	}

	// TODO: Update the lru.
	return nil
}

func (ds *cachedDataSource) managedPut(dsid crypto.Hash, sectionIndex uint64, data []byte) (err error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	if len(data) > ds.staticSectionSize {
		err := fmt.Errorf("data length out-of-bounds %v > %v", len(data), ds.staticSectionSize)
		build.Critical(err)
		return err
	}

	// Check if the data source was deleted already. In that case we don't
	// use it anymore.
	if ds.deleted {
		return errors.New("data source has been deleted")
	}

	// Check if the section is already cached.
	_, exists := ds.staticSections[sectionIndex]
	if exists {
		return nil
	}

	// Open the cache file.
	cacheFile, err := ds.staticLRU.staticOpenCacheFile(dsid)
	if err != nil {
		return err
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
		return err
	}
	return nil
}

func (ds *cachedDataSource) managedGet(dsid crypto.Hash, sectionIndex uint64) (_ []byte, _ bool, err error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	// Check if the data source was deleted already.
	if ds.deleted {
		return nil, false, nil
	}

	section, exists := ds.staticSections[sectionIndex]
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
