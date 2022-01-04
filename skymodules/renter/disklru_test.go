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
	ds := newCachedDataSource(int(sectionSize))

	addTestSection := func(index uint64, expectedOffset int64) error {
		offset := ds.newSection(index)
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
	if err := addTestSection(1, 0); err != nil {
		t.Fatal(err)
	}
	if err := addTestSection(5, sectionSize); err != nil {
		t.Fatal(err)
	}
	if err := addTestSection(10, 2*sectionSize); err != nil {
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
	if err := addTestSection(100, sectionSize); err != nil {
		t.Fatal(err)
	}
	if len(ds.unusedSections) != 0 {
		t.Fatal("wrong number of unused sections")
	}
	if len(ds.staticSections) != 3 {
		t.Fatal("wrong number of used sections", len(ds.staticSections))
	}
}

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
	section3 := 1 // small section

}
