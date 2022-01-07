package renter

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"sync"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/persist"
)

const (
	// testLRUMaxCacheSize is the max cache size for most tests.
	testLRUMaxCacheSize = 1 << 20
)

// lruTestDir creates a dir for testing the persistedLRU.
func lruTestDir(testName string) string {
	path := build.TempDir("lru", testName)
	err := os.RemoveAll(path)
	if err != nil {
		panic(err)
	}
	err = os.MkdirAll(path, persist.DefaultDiskPermissionsTest)
	if err != nil {
		panic(err)
	}
	return path
}

// newTestLRU creates a new LRU for testing.
func newTestLRU(path string) *persistedLRU {
	lru, err := newPersistedLRU(path, testLRUMaxCacheSize, 1, time.Hour)
	if err != nil {
		panic(err)
	}
	return lru
}

// TestPersistedLRU runs all tests related to the persistedLRU.
func TestPersistedLRU(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	tests := []struct {
		name string
		f    func(t *testing.T)
	}{
		{
			name: "DataSourceIDToPath",
			f:    testDataSourceIDToPath,
		},
		{
			name: "Persistence",
			f:    testPersistence,
		},
		{
			name: "Section",
			f:    testSection,
		},
		{
			name: "PutGet",
			f:    testPutGet,
		},
		{
			name: "LRURefresh",
			f:    testLRURefresh,
		},
		{
			name: "PruneLRU",
			f:    testLRUPrune,
		},
		{
			name: "TryPruneData",
			f:    testTryPruneData,
		},
		{
			name: "Parallel",
			f:    testLRUParallel,
		},
	}
	for _, test := range tests {
		t.Run(test.name, test.f)
	}
}

// testDataSourceIDToPath is a unit test for staticDataSourceIDToPath.
func testDataSourceIDToPath(t *testing.T) {
	dir := lruTestDir(t.Name())
	lru := newTestLRU(dir)

	if lru.staticPath != dir {
		t.Fatal("wrong path", lru.staticPath)
	}

	var dsid crypto.Hash
	err := dsid.LoadString("5db3df3ddf0622ab7bbee847a23db4122b0279d7a3cb4601606faed83bbf1f24")
	if err != nil {
		t.Fatal(err)
	}

	expectedPath := dir + "/5d/b3/df3ddf0622ab7bbee847a23db4122b0279d7a3cb4601606faed83bbf1f24/1.dat"
	if path := lru.staticDataSourceIDToPath(skymodules.DataSourceID(dsid), 1); path != expectedPath {
		t.Log(path)
		t.Log(expectedPath)
		t.Fatal("wrong path")
	}
}

// testPersistence makes tests creating and deleting cache files.
func testPersistence(t *testing.T) {
	dir := lruTestDir(t.Name())
	lru := newTestLRU(dir)

	var dsid skymodules.DataSourceID
	fastrand.Read(dsid[:])

	f, err := lru.staticOpenCacheFile(dsid, 1)
	if err != nil {
		t.Fatal(err)
	}
	writtenBytes := fastrand.Bytes(10)
	_, err = f.Write(writtenBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := ioutil.ReadFile(lru.staticDataSourceIDToPath(dsid, 1))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, writtenBytes) {
		t.Fatal("wrong data", data, writtenBytes)
	}
	l, err := lru.staticRemoveCacheFile(dsid, 1)
	if err != nil {
		t.Fatal(err)
	}
	if int(l) != len(writtenBytes) {
		t.Fatal("wrong length", l, len(writtenBytes))
	}
	if _, err := os.Stat(lru.staticDataSourceIDToPath(dsid, 1)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// testSection is a unit test for newSection and freeSection.
func testSection(t *testing.T) {
	lru := newTestLRU(t.Name())
	var dsid skymodules.DataSourceID
	fastrand.Read(dsid[:])
	ds := lru.staticNewCachedDataSource(dsid)

	// Check if id was set.
	if ds.staticID != dsid {
		t.Fatal("wrong id", ds.staticID, dsid)
	}

	// Create sections
	ds.newSection(1)
	ds.newSection(5)
	ds.newSection(10)

	// Free one of them.
	ds.freeSection(5)
	if len(ds.sections) != 2 {
		t.Fatal("wrong number of used sections", len(ds.sections))
	}
}

// testPutGet tests adding files to the cache and reading them.
func testPutGet(t *testing.T) {
	dir := lruTestDir(t.Name())
	lru := newTestLRU(dir)

	if lru.staticPath != dir {
		t.Fatal("wrong path", lru.staticPath)
	}

	var dsid skymodules.DataSourceID
	fastrand.Read(dsid[:])

	sectionSize := 100
	section1 := fastrand.Bytes(sectionSize)
	section2 := fastrand.Bytes(sectionSize)
	section3 := fastrand.Bytes(1) // small section
	section4 := fastrand.Bytes(1) // small section

	testPutGet := func(dsid skymodules.DataSourceID, sectorIndex uint64, data []byte) error {
		if err := lru.Put(dsid, sectorIndex, data); err != nil {
			return err
		}
		cachedData, cached, err := lru.Get(dsid, sectorIndex)
		if err != nil {
			return err
		}
		if !cached {
			return errors.New("data not found in cache")
		}
		if !bytes.Equal(data, cachedData) {
			return fmt.Errorf("cached data != data %v %v", len(data), len(cachedData))
		}
		return nil
	}

	// Cache section 1 twice.
	if err := testPutGet(dsid, 0, section1); err != nil {
		t.Fatal(err)
	}
	if err := testPutGet(dsid, 0, section1); err != nil {
		t.Fatal(err)
	}

	// Cache section 2.
	if err := testPutGet(dsid, 1, section2); err != nil {
		t.Fatal(err)
	}

	// Free section 1.
	lru.dataSources[dsid].freeSection(0)

	// Cache section 3.
	if err := testPutGet(dsid, 2, section3); err != nil {
		t.Fatal(err)
	}

	// Cache section 4.
	if err := testPutGet(dsid, 3, section4); err != nil {
		t.Fatal(err)
	}

	// Run the above again but in a loop with more randomness.
	var dsid2 skymodules.DataSourceID
	fastrand.Read(dsid2[:])
	var dsid3 skymodules.DataSourceID
	fastrand.Read(dsid3[:])

	dsids := []skymodules.DataSourceID{dsid, dsid2, dsid3}
	sections := [][]byte{section1, section2, section3}

	for i := 0; i < 100; i++ {
		dsidI := fastrand.Intn(3)
		sectionI := fastrand.Intn(3)

		if err := testPutGet(dsids[dsidI], uint64(sectionI), sections[sectionI]); err != nil {
			t.Error(err)
			return
		}

		// 50% chance to free the section again.
		if fastrand.Intn(2) == 0 {
			lru.dataSources[dsids[dsidI]].freeSection(uint64(sectionI))
		}
	}
}

// testLRURefresh is a unit test for checking that Put and Get call
// managedRefreshCachedEntry and that it correctly updates the lru.
func testLRURefresh(t *testing.T) {
	dir := lruTestDir(t.Name())
	lru := newTestLRU(dir)

	var dsid1 skymodules.DataSourceID
	fastrand.Read(dsid1[:])
	var dsid2 skymodules.DataSourceID
	fastrand.Read(dsid2[:])

	// Put some data in the cache for dsid1 section1.
	if err := lru.Put(dsid1, 1, fastrand.Bytes(1)); err != nil {
		t.Fatal(err)
	}
	if lru.staticLRU.Len() != 1 {
		t.Fatal("wrong lru len", lru.staticLRU.Len())
	}
	if len(lru.lruElements[dsid1]) != 1 {
		t.Fatal("wrong lruElements len", len(lru.lruElements[dsid1]))
	}
	element := lru.staticLRU.Front().Value.(lruElement)
	if element.staticDSID != dsid1 || element.staticSectionIndex != 1 {
		t.Fatal("wrong element in list")
	}

	// Put some data in the cache for dsid2 section1. It should now be at
	// the front of the LRU.
	if err := lru.Put(dsid2, 1, fastrand.Bytes(1)); err != nil {
		t.Fatal(err)
	}
	if lru.staticLRU.Len() != 2 {
		t.Fatal("wrong lru len", lru.staticLRU.Len())
	}
	if len(lru.lruElements[dsid2]) != 1 {
		t.Fatal("wrong lruElements len", len(lru.lruElements[dsid2]))
	}
	element = lru.staticLRU.Front().Value.(lruElement)
	if element.staticDSID != dsid2 || element.staticSectionIndex != 1 {
		t.Fatal("wrong element in list")
	}

	// Get data for dsid1 section1. Should be back in the front.
	_, cached, err := lru.Get(dsid1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !cached {
		t.Fatal("should be cached")
	}
	if lru.staticLRU.Len() != 2 {
		t.Fatal("wrong lru len", lru.staticLRU.Len())
	}
	if len(lru.lruElements[dsid1]) != 1 {
		t.Fatal("wrong lruElements len", len(lru.lruElements[dsid1]))
	}
	element = lru.staticLRU.Front().Value.(lruElement)
	if element.staticDSID != dsid1 || element.staticSectionIndex != 1 {
		t.Fatal("wrong element in list")
	}
	element = lru.staticLRU.Back().Value.(lruElement)
	if element.staticDSID != dsid2 || element.staticSectionIndex != 1 {
		t.Fatal("wrong element in list")
	}

	// Put some data for dsid1 section2. The new order should be dsid1
	// section2, dsid1 section1 and then dsid2 section1.
	if err := lru.Put(dsid1, 2, fastrand.Bytes(1)); err != nil {
		t.Fatal(err)
	}
	if lru.staticLRU.Len() != 3 {
		t.Fatal("wrong lru len", lru.staticLRU.Len())
	}
	if len(lru.lruElements[dsid1]) != 2 {
		t.Fatal("wrong lruElements len", len(lru.lruElements[dsid1]))
	}
	if len(lru.lruElements[dsid2]) != 1 {
		t.Fatal("wrong lruElements len", len(lru.lruElements[dsid2]))
	}
	element = lru.staticLRU.Front().Value.(lruElement)
	if element.staticDSID != dsid1 || element.staticSectionIndex != 2 {
		t.Fatal("wrong element in list")
	}
	element = lru.staticLRU.Front().Next().Value.(lruElement)
	if element.staticDSID != dsid1 || element.staticSectionIndex != 1 {
		t.Fatal("wrong element in list")
	}
	element = lru.staticLRU.Back().Value.(lruElement)
	if element.staticDSID != dsid2 || element.staticSectionIndex != 1 {
		t.Fatal("wrong element in list")
	}

	// Get dsid2 section1. This should put it back in the front, followed by
	// dsid1 section2 and dsid1 section1.
	if _, cached, err := lru.Get(dsid2, 1); !cached || err != nil {
		t.Fatal(err)
	}
	if lru.staticLRU.Len() != 3 {
		t.Fatal("wrong lru len", lru.staticLRU.Len())
	}
	if len(lru.lruElements[dsid1]) != 2 {
		t.Fatal("wrong lruElements len", len(lru.lruElements[dsid1]))
	}
	if len(lru.lruElements[dsid2]) != 1 {
		t.Fatal("wrong lruElements len", len(lru.lruElements[dsid2]))
	}
	element = lru.staticLRU.Front().Value.(lruElement)
	if element.staticDSID != dsid2 || element.staticSectionIndex != 1 {
		t.Fatal("wrong element in list")
	}
	element = lru.staticLRU.Front().Next().Value.(lruElement)
	if element.staticDSID != dsid1 || element.staticSectionIndex != 2 {
		t.Fatal("wrong element in list")
	}
	element = lru.staticLRU.Back().Value.(lruElement)
	if element.staticDSID != dsid1 || element.staticSectionIndex != 1 {
		t.Fatal("wrong element in list")
	}
}

func testLRUPrune(t *testing.T) {
	dir := lruTestDir(t.Name())
	lru := newTestLRU(dir)

	// Put some data in the cache for dsid1.
	var dsid1 skymodules.DataSourceID
	fastrand.Read(dsid1[:])
	section1 := fastrand.Bytes(1)
	section2 := fastrand.Bytes(int(4096))
	if err := lru.Put(dsid1, 1, section1); err != nil {
		t.Fatal(err)
	}
	if err := lru.Put(dsid1, 2, section2); err != nil {
		t.Fatal(err)
	}
	if lru.staticLRU.Len() != 2 {
		t.Fatal("wrong lru len", lru.staticLRU.Len())
	}
	if len(lru.lruElements[dsid1]) != 2 {
		t.Fatal("wrong lruElements len", len(lru.lruElements[dsid1]))
	}

	// Check the relevant size fields.
	if lru.staticMaxCacheSize != int64(testLRUMaxCacheSize) {
		t.Fatal("wrong max size", lru.staticMaxCacheSize)
	}
	if lru.cachedSize != int64(len(section1)+len(section2)) {
		t.Fatal("wrong cached size", lru.cachedSize)
	}

	// Prune. This should remove section1 since we added that before
	// section2.
	length, more, err := lru.managedPruneLRU()
	if err != nil {
		t.Fatal(err)
	}
	if !more {
		t.Fatal("more should be true since the lru isn't empty")
	}
	if length != int64(len(section1)) {
		t.Fatal("wrong pruned size")
	}
	if lru.staticLRU.Len() != 1 {
		t.Fatal("wrong lru len", lru.staticLRU.Len())
	}
	if len(lru.lruElements[dsid1]) != 1 {
		t.Fatal("wrong lruElements len", len(lru.lruElements[dsid1]))
	}
	_, exists1 := lru.lruElements[dsid1][1]
	_, exists2 := lru.lruElements[dsid1][2]
	if exists1 || !exists2 {
		t.Fatal("wrong lru element exists", exists1, exists2)
	}
	// Check datasource.
	ds := lru.dataSources[dsid1]
	if ds.staticID != dsid1 {
		t.Fatal("wrong id")
	}
	// Try to open the cache files on disk. Should work.
	_, err = os.Stat(lru.staticDataSourceIDToPath(dsid1, 1))
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	_, err = os.Stat(lru.staticDataSourceIDToPath(dsid1, 2))
	if err != nil {
		t.Fatal(err)
	}

	// Prune. This should remove section2 since it's the last one left.
	length, more, err = lru.managedPruneLRU()
	if err != nil {
		t.Fatal(err)
	}
	if !more {
		t.Fatal("more should be true since we pruned some data")
	}
	if length != int64(len(section2)) {
		t.Fatal("wrong pruned size")
	}
	if lru.staticLRU.Len() != 0 {
		t.Fatal("wrong lru len", lru.staticLRU.Len())
	}
	if len(lru.lruElements[dsid1]) != 0 {
		t.Fatal("wrong lruElements len", len(lru.lruElements[dsid1]))
	}
	_, exists1 = lru.lruElements[dsid1][1]
	_, exists2 = lru.lruElements[dsid1][2]
	if exists1 || exists2 {
		t.Fatal("wrong lru element exists", exists1, exists2)
	}
	// Check datasource.
	_, exists := lru.managedAcquireDataSource(dsid1)
	if exists {
		t.Fatal("should be deleted")
	}
	// Try to open the cache file on disk. Should fail since it was deleted.
	_, err = os.Stat(lru.staticDataSourceIDToPath(dsid1, 1))
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	_, err = os.Stat(lru.staticDataSourceIDToPath(dsid1, 2))
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// testTryPruneData is a unit test for managedTryPruneData.
func testTryPruneData(t *testing.T) {
	dir := lruTestDir(t.Name())
	maxSize := uint64(100)
	sectionSize := uint64(50)
	lru, err := newPersistedLRU(dir, maxSize, 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// Put some data in the cache for dsid.
	var dsid skymodules.DataSourceID
	fastrand.Read(dsid[:])
	section := fastrand.Bytes(int(sectionSize))
	if err := lru.Put(dsid, 1, section); err != nil {
		t.Fatal(err)
	}
	if err := lru.Put(dsid, 2, section); err != nil {
		t.Fatal(err)
	}

	// Check cache size.
	if lru.cachedSize != int64(maxSize) {
		t.Fatal("wrong cached size", lru.cachedSize)
	}

	// Call managedAddCachedData with 0 bytes added. This shouldn't do
	// anything.
	err = lru.managedTryPruneData()
	if err != nil {
		t.Fatal(err)
	}
	if lru.cachedSize != int64(maxSize) {
		t.Fatal("wrong cached size", lru.cachedSize)
	}
	if lru.staticLRU.Len() != 2 {
		t.Fatal("wrong lru length", lru.staticLRU.Len())
	}

	// Call it again with 51 bytes. This pushes it above the max and will
	// cause it to prune both sections and leave the size at 51.
	lru.cachedSize += 51
	err = lru.managedTryPruneData()
	if err != nil {
		t.Fatal(err)
	}
	if lru.cachedSize != 51 {
		t.Fatal("wrong cached size", lru.cachedSize)
	}
	if lru.staticLRU.Len() != 0 {
		t.Fatal("wrong lru length", lru.staticLRU.Len())
	}
	_, ok, err := lru.Get(dsid, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("shouldn't be cached")
	}
	_, ok, err = lru.Get(dsid, 2)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("shouldn't be cached")
	}
}

// testLRUParallel tests adding entries, fetching them and pruning them from
// multiple threads.
func testLRUParallel(t *testing.T) {
	dir := lruTestDir(t.Name())
	maxSize := uint64(100)
	sectionSize := int(25)
	lru, err := newPersistedLRU(dir, maxSize, 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	s := func() []byte {
		return fastrand.Bytes(sectionSize)
	}

	// Prepare 2 data sources with 3 sections each.
	var dsid1 skymodules.DataSourceID
	fastrand.Read(dsid1[:])
	var dsid2 skymodules.DataSourceID
	fastrand.Read(dsid2[:])

	dsids := []skymodules.DataSourceID{dsid1, dsid2}
	sections1 := [][]byte{s(), s(), s()}
	sections2 := [][]byte{s(), s(), s()}
	sectionss := [][][]byte{sections1, sections2}

	// Define a reader. The reader tries to read a random section from a
	// random datasource. If the cache is empty, Put is called to fill it
	// instead. 6 sections exist in total but only 4 can be in the cache at
	// any given time. This guarantees some pruning.
	reader := func() {
		for i := 0; i < 100; i++ {
			dsidI := fastrand.Intn(len(dsids))
			sections := sectionss[dsidI]

			dsid := dsids[dsidI]
			sectionI := fastrand.Intn(len(sections))

			section, cached, err := lru.Get(dsid, uint64(sectionI))
			if err != nil {
				t.Error(err)
				return
			}

			if cached {
				// Check cached data.
				if !bytes.Equal(section, sections[sectionI]) {
					t.Fatal("section mismatch")
				}
			} else {
				// Add data to cache.
				err = lru.Put(dsid, uint64(sectionI), sections[sectionI])
				if err != nil {
					t.Error(err)
					return
				}
			}
		}
	}

	numThreads := 3
	var wg sync.WaitGroup
	for i := 0; i < numThreads; i++ {
		wg.Add(1)
		go func(i int) {
			reader()
			wg.Done()
		}(i)
	}

	// Wait for readers to be done.
	wg.Wait()

	// Check the cache.
	if lru.cachedSize != int64(maxSize) {
		t.Error("wrong cached size", lru.cachedSize)
	}
	if lru.staticLRU.Len() != int(maxSize)/sectionSize {
		t.Error("wrong lru length", lru.staticLRU.Len())
	}
}

func TestHitTracker(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Specify a hit tracker that requires 3 hits within a second to return
	// true.
	ht := newCacheHitTracker(3, time.Second)

	var dsid1 skymodules.DataSourceID
	fastrand.Read(dsid1[:])

	// Report 2 hits. Both should return false.
	cache := ht.ReportHit(dsid1, 0)
	if cache {
		t.Fatal("should be false")
	}
	cache = ht.ReportHit(dsid1, 0)
	if cache {
		t.Fatal("should be false")
	}
	// The third and fourth ones return true.
	cache = ht.ReportHit(dsid1, 0)
	if !cache {
		t.Fatal("should be true")
	}
	cache = ht.ReportHit(dsid1, 0)
	if !cache {
		t.Fatal("should be true")
	}
	// A hit for a different datasource returns false.
	var dsid2 skymodules.DataSourceID
	fastrand.Read(dsid2[:])
	cache = ht.ReportHit(dsid2, 0)
	if cache {
		t.Fatal("should be false")
	}
	// A hit for a different section returns false.
	cache = ht.ReportHit(dsid1, 1)
	if cache {
		t.Fatal("should be false")
	}
	// A hit for the initial source and section returns still true.
	cache = ht.ReportHit(dsid1, 0)
	if !cache {
		t.Fatal("should be true")
	}
	// Wait for a second. The next hit returns false.
	time.Sleep(ht.staticDuration)
	cache = ht.ReportHit(dsid1, 0)
	if cache {
		t.Fatal("should be false")
	}
	// Prune the tracker. It should now have 1 dsid.
	ht.Prune()
	if len(ht.hits) != 1 {
		t.Fatal("wrong length", len(ht.hits))
	}
	if len(ht.hits[dsid1]) != 1 {
		t.Fatal("wrong length", len(ht.hits[dsid1]))
	}
	if _, exists := ht.hits[dsid1][0]; !exists {
		t.Fatal("wrong hit found")
	}
}
