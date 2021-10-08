package siafile

import (
	"fmt"
	"testing"

	"go.sia.tech/siad/modules"
)

// TestMetadataCompatCheck probes the metadataCompatCheck method for updating
// the version
func TestMetadataCompatCheck(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Check siafile
	sf := newBlankTestFile()

	// Version check
	if sf.staticMetadata.StaticVersion != metadataVersion {
		t.Fatal("wrong version")
	}

	// Set version to nil
	sf.staticMetadata.StaticVersion = nilMetadataVesion
	err := sf.SaveMetadata()
	if err != nil {
		t.Fatal(err)
	}

	// Version check
	if sf.staticMetadata.StaticVersion != nilMetadataVesion {
		t.Fatal("wrong version")
	}

	// Load siafile
	sf, err = loadSiaFile(sf.siaFilePath, sf.wal, sf.deps)
	if err != nil {
		t.Fatal(err)
	}

	// Version check
	if sf.staticMetadata.StaticVersion != metadataVersion {
		t.Fatal("wrong version")
	}
}

// TestUniqueIDMissing makes sure that loading a siafile sets the unique id in
// the metadata if it wasn't set before.
func TestUniqueIDMissing(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a new file.
	sf, wal, _ := newBlankTestFileAndWAL(1)
	// It should have a UID.
	if sf.staticMetadata.UniqueID == "" {
		t.Fatal("unique ID wasn't set")
	}
	// Set the UID to a blank string, and reset the metadata version and
	// save the file.
	sf.staticMetadata.UniqueID = ""
	sf.staticMetadata.StaticVersion = nilMetadataVesion
	err := sf.SaveMetadata()
	if err != nil {
		t.Fatal(err)
	}
	// Load the file again.
	sf, err = LoadSiaFile(sf.siaFilePath, wal)
	if err != nil {
		t.Fatal(err)
	}
	// It should have a UID now.
	if sf.staticMetadata.UniqueID == "" {
		t.Fatal("unique ID wasn't set after loading file")
	}
}

// TestZeroByteFileCompat checks that 0-byte siafiles that have been uploaded
// before caching was introduced have the correct cached values after being
// loaded.
func TestZeroByteFileCompat(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create the file.
	siaFilePath, _, source, rc, sk, _, _, fileMode := newTestFileParams(1, true)
	sf, wal, _ := customTestFileAndWAL(siaFilePath, source, rc, sk, 0, 0, fileMode)
	// Check that the number of chunks in the file is correct.
	if sf.numChunks != 0 {
		panic(fmt.Sprintf("newTestFile didn't create the expected number of chunks: %v", sf.numChunks))
	}

	// Set the cached fields and version to 0 like they would be if the file
	// was already uploaded before caching was introduced.
	sf.staticMetadata.StaticVersion = nilMetadataVesion
	sf.staticMetadata.CachedHealth = 0
	sf.staticMetadata.CachedRedundancy = 0
	sf.staticMetadata.CachedRepairBytes = 0
	sf.staticMetadata.CachedStuckBytes = 0
	sf.staticMetadata.CachedStuckHealth = 0
	sf.staticMetadata.CachedUserRedundancy = 0
	sf.staticMetadata.CachedUploadProgress = 0
	// Save the file and reload it.
	if err := sf.SaveMetadata(); err != nil {
		t.Fatal(err)
	}
	sf, err := loadSiaFile(siaFilePath, wal, modules.ProdDependencies)
	if err != nil {
		t.Fatal(err)
	}
	// Make sure the loaded file has the correct cached values.
	if sf.staticMetadata.CachedHealth != 0 {
		t.Fatalf("CachedHealth should be 0 but was %v", sf.staticMetadata.CachedHealth)
	}
	expectedRedundancy := float64(rc.NumPieces()) / float64(rc.MinPieces())
	if sf.staticMetadata.CachedRedundancy != expectedRedundancy {
		t.Fatalf("CachedRedundancy should be %v but was %v", expectedRedundancy, sf.staticMetadata.CachedRedundancy)
	}
	if sf.staticMetadata.CachedRepairBytes != 0 {
		t.Fatalf("CachedRepairBytes should be 0 but was %v", sf.staticMetadata.CachedRepairBytes)
	}
	if sf.staticMetadata.CachedStuckBytes != 0 {
		t.Fatalf("CachedStuckBytes should be 0 but was %v", sf.staticMetadata.CachedStuckBytes)
	}
	if sf.staticMetadata.CachedStuckHealth != 0 {
		t.Fatalf("CachedStuckHealth should be 0 but was %v", sf.staticMetadata.CachedStuckHealth)
	}
	if sf.staticMetadata.CachedUserRedundancy != expectedRedundancy {
		t.Fatalf("CachedRedundancy should be %v but was %v", expectedRedundancy, sf.staticMetadata.CachedUserRedundancy)
	}
	if sf.staticMetadata.CachedUploadProgress != 100 {
		t.Fatalf("CachedUploadProgress should be 100 but was %v", sf.staticMetadata.CachedUploadProgress)
	}
}
