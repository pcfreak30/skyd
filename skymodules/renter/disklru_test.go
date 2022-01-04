package renter

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/persist"
)

// testLRUSectionSize is the section size for testing.
const testLRUSectionSize = 4096

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

func newTestLRU(path string) *persistedLRU {
	lru, err := newPersistedLRU(path, testLRUSectionSize)
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

	expectedPath := dir + "/5d/b3/df/3d/df0622ab7bbee847a23db4122b0279d7a3cb4601606faed83bbf1f24.dat"
	if path := lru.staticDataSourceIDToPath(dsid); path != expectedPath {
		t.Log(path)
		t.Log(expectedPath)
		t.Fatal("wrong path")
	}
}

// testPersistence makes tests creating and deleting cache files.
func testPersistence(t *testing.T) {
	dir := lruTestDir(t.Name())
	lru := newTestLRU(dir)

	var dsid crypto.Hash
	fastrand.Read(dsid[:])

	f, err := lru.staticOpenCacheFile(dsid)
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
	data, err := ioutil.ReadFile(lru.staticDataSourceIDToPath(dsid))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, writtenBytes) {
		t.Fatal("wrong data", data, writtenBytes)
	}
	err = lru.staticRemoveCacheFile(dsid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lru.staticDataSourceIDToPath(dsid)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// testSection is a unit test for newSection and freeSection.
func testSection(t *testing.T) {
	sectionSize := int64(100)
	lru := newTestLRU(t.Name())
	ds := lru.staticNewCachedDataSource(int(sectionSize))

	addTestSection := func(index uint64, expectedOffset int64, length int) error {
		offset := ds.newSection(index, length)
		if offset != expectedOffset {
			return fmt.Errorf("wrong offset returned %v %v", offset, expectedOffset)
		}
		section, ok := ds.staticSections[index]
		if !ok {
			return errors.New("added section not found")
		}
		if section.staticOffset != expectedOffset {
			return fmt.Errorf("wrong section offset %v %v", section.staticOffset, expectedOffset)
		}
		return nil
	}

	// Create sections
	if err := addTestSection(1, 0, 100); err != nil {
		t.Fatal(err)
	}
	if err := addTestSection(5, sectionSize, 200); err != nil {
		t.Fatal(err)
	}
	if err := addTestSection(10, 2*sectionSize, 300); err != nil {
		t.Fatal(err)
	}

	// Free one of them.
	ds.freeSection(5)
	if len(ds.unusedSections) != 1 {
		t.Fatal("wrong number of unused sections")
	}
	if len(ds.staticSections) != 2 {
		t.Fatal("wrong number of used sections", len(ds.staticSections))
	}

	// Add another one. Should reuse the section.
	if err := addTestSection(100, sectionSize, 400); err != nil {
		t.Fatal(err)
	}
	if len(ds.unusedSections) != 0 {
		t.Fatal("wrong number of unused sections")
	}
	if len(ds.staticSections) != 3 {
		t.Fatal("wrong number of used sections", len(ds.staticSections))
	}
}

// testPutGet tests adding files to the cache and reading them.
func testPutGet(t *testing.T) {
	dir := lruTestDir(t.Name())
	lru := newTestLRU(dir)

	if lru.staticPath != dir {
		t.Fatal("wrong path", lru.staticPath)
	}

	var dsid crypto.Hash
	fastrand.Read(dsid[:])

	section1 := fastrand.Bytes(testLRUSectionSize)
	section2 := fastrand.Bytes(testLRUSectionSize)
	section3 := fastrand.Bytes(1) // small section
	section4 := fastrand.Bytes(1) // small section

	testPutGet := func(dsid crypto.Hash, sectorIndex uint64, data []byte) error {
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
	var dsid2 crypto.Hash
	fastrand.Read(dsid2[:])
	var dsid3 crypto.Hash
	fastrand.Read(dsid3[:])

	dsids := []crypto.Hash{dsid, dsid2, dsid3}
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

	var dsid1 crypto.Hash
	fastrand.Read(dsid1[:])
	var dsid2 crypto.Hash
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

	// Put some data for dsid1 section1 again. Should be back in the front.
	if err := lru.Put(dsid1, 1, fastrand.Bytes(1)); err != nil {
		t.Fatal(err)
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
