package siafile

import (
	"fmt"
	"testing"

	"gitlab.com/NebulousLabs/errors"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// TestMetadataCompatCheck probes the metadataCompatCheck method
func TestMetadataCompatCheck(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	t.Run("NilMetadataToV1", testUpgradeNilMetadataToV1)
	t.Run("V1MetadataToV2", testUpgradeV1MetadataToV2)
	t.Run("V2MetadataToV3", testUpgradeV2MetadataToV3)
}

// testUpgradeNilMetadataToV1 tests the compat code related to upgrading from a
// nilMetadataVerion to metadataVersion1
func testUpgradeNilMetadataToV1(t *testing.T) {
	t.Parallel()
	t.Run("UniqueIDMissing", testUniqueIDMissing)
	t.Run("ZeroByteFile", testZeroByteFileCompat)
}

// testUpgradeV1MetadataToV2 tests the compat code related to upgrading from a
// metadataVersion1 to metadataVersion2
func testUpgradeV1MetadataToV2(t *testing.T) {
	t.Parallel()

	// Create siafile
	sf := newBlankTestFile()

	// Add a piece to the file so that it shows uploaded bytes
	err := sf.AddPiece(types.SiaPublicKey{}, uint64(0), 0, crypto.Hash{})
	if err != nil {
		t.Fatal(err)
	}

	// Assert test conditions
	_, unique, err := sf.uploadedBytes()
	if err != nil {
		t.Fatal(err)
	}
	if unique != modules.SectorSize {
		t.Fatal("unique not expected", unique)
	}

	// check checks the metadata
	check := func(sf *SiaFile, finished, stuck bool, version [16]byte) error {
		// Version check
		if sf.staticMetadata.StaticVersion != version {
			return fmt.Errorf("Wrong version, expected %v got %v", version, sf.staticMetadata.StaticVersion)
		}

		// Check Stuck
		isStuck := sf.staticMetadata.NumStuckChunks > 0
		if stuck != isStuck {
			return fmt.Errorf("Expected stuck %v, got stuck %v", stuck, isStuck)
		}

		// Check finished
		if finished != sf.finished() {
			return fmt.Errorf("Expected finished %v, got finished %v", finished, sf.finished())
		}
		return nil
	}

	// reset resets the metadata
	reset := func(sf *SiaFile, finished, stuck, localPath bool) {
		// Reset version and finished flag
		sf.staticMetadata.StaticVersion = metadataVersion1
		sf.staticMetadata.Finished = false

		// Set the expected stuck state
		if stuck {
			sf.staticMetadata.NumStuckChunks = 1
		} else {
			sf.staticMetadata.NumStuckChunks = 0
		}

		// Set the expected finished state
		if finished {
			sf.staticMetadata.FileSize = 1
		} else {
			sf.staticMetadata.FileSize = int64(modules.SectorSize) + 1
		}

		// Set the localPath
		if localPath {
			sf.staticMetadata.LocalPath = "localpath"
		} else {
			sf.staticMetadata.LocalPath = ""
		}
	}

	// Define tests
	var tests = []struct {
		name string

		finished bool
		stuck    bool

		localPath bool
	}{
		// With Local Path
		//
		// Test for finished stuck
		{"Finished_Stuck", true, true, true},
		// Test for unfinished stuck
		{"Unfinished_Stuck", false, true, true},
		// Test for finished unstuck
		{"Finished_Unstuck", true, false, true},
		// Test for unfinished unstuck
		{"Unfinished_Unstuck", false, false, true},
		// Without Local Path
		//
		// Test for finished stuck
		{"Finished_Stuck", true, true, false},
		// Test for unfinished stuck
		{"Unfinished_Stuck", false, true, false},
		// Test for finished unstuck
		{"Finished_Unstuck", true, false, false},
		// Test for unfinished unstuck
		{"Unfinished_Unstuck", false, false, false},
	}

	// Run tests
	for _, test := range tests {
		// A file is considered finished if it finished or has a localpath
		finished := test.finished || test.localPath

		// At the end of the compat, the file should only be stuck if it
		// was originally finished or has a localpath, and is stuck.
		stuck := finished && test.stuck

		// Reset the metadata
		reset(sf, test.finished, test.stuck, test.localPath)

		// test testUpgradeV1MetadataToV2
		err = sf.upgradeMetadataFromV1ToV2()
		if err != nil {
			t.Fatal(test, err)
		}

		// Check the metadata
		err = check(sf, finished, stuck, metadataVersion2)
		if err != nil {
			t.Fatal(test, err)
		}

		// Reset the metadata
		reset(sf, test.finished, test.stuck, test.localPath)

		// Save the metadata to disk
		err = sf.SaveMetadata()
		if err != nil {
			t.Fatal(test, err)
		}

		// Reload the SiaFile from disk
		sf, err = LoadSiaFile(sf.siaFilePath, sf.wal)
		if err != nil {
			t.Fatal(test, err)
		}

		// Check the metadata
		//
		// Update for MetadateVersion3, all cases should now be
		// !finished and !stuck
		err = check(sf, false, false, metadataVersion)
		if err != nil {
			t.Fatal(test, err)
		}
	}
}

// testUpgradeV2MetadataToV3 tests the compat code related to upgrading from a
// metadataVersion2 to metadataVersion3
func testUpgradeV2MetadataToV3(t *testing.T) {
	t.Parallel()

	// Create siafile
	sf := newBlankTestFile()

	// check checks the metadata
	check := func(sf *SiaFile, version [16]byte) error {
		// Version check
		if sf.staticMetadata.StaticVersion != version {
			return fmt.Errorf("Wrong version, expected %v got %v", version, sf.staticMetadata.StaticVersion)
		}

		// Check Stuck
		if sf.staticMetadata.NumStuckChunks > 0 {
			return fmt.Errorf("File should not be stuck %v", sf.staticMetadata.NumStuckChunks)
		}

		// Check finished
		if sf.finished() {
			return errors.New("file should not be finished")
		}
		return nil
	}

	// reset resets the metadata
	reset := func(sf *SiaFile, finished, stuck bool) {
		// Reset version and finished flag
		sf.staticMetadata.StaticVersion = metadataVersion2
		sf.staticMetadata.Finished = finished

		// Set the expected stuck state
		if stuck {
			sf.staticMetadata.NumStuckChunks = 1
		} else {
			sf.staticMetadata.NumStuckChunks = 0
		}
	}

	// Define tests
	var tests = []struct {
		name string

		finished bool
		stuck    bool
	}{
		// Test for finished stuck
		{"Finished_Stuck", true, true},
		// Test for unfinished stuck
		{"Unfinished_Stuck", false, true},
		// Test for finished unstuck
		{"Finished_Unstuck", true, false},
		// Test for unfinished unstuck
		{"Unfinished_Unstuck", false, false},
	}

	// Run tests
	for _, test := range tests {
		// Reset the metadata
		reset(sf, test.finished, test.stuck)

		// test testUpgradeV2MetadataToV3
		err := sf.upgradeMetadataFromV2ToV3()
		if err != nil {
			t.Fatal(test, err)
		}

		// Check the metadata
		err = check(sf, metadataVersion3)
		if err != nil {
			t.Fatal(test, err)
		}

		// Reset the metadata
		reset(sf, test.finished, test.stuck)

		// Save the metadata to disk
		err = sf.SaveMetadata()
		if err != nil {
			t.Fatal(test, err)
		}

		// Reload the SiaFile from disk
		sf, err = LoadSiaFile(sf.siaFilePath, sf.wal)
		if err != nil {
			t.Fatal(test, err)
		}

		// Check the metadata
		err = check(sf, metadataVersion)
		if err != nil {
			t.Fatal(test, err)
		}
	}
}

// testUniqueIDMissing makes sure that loading a siafile sets the unique id in
// the metadata if it wasn't set before.
func testUniqueIDMissing(t *testing.T) {
	// Create a new file.
	sf, wal, _ := newBlankTestFileAndWAL(1)
	// It should have a UID.
	if sf.staticMetadata.UniqueID == "" {
		t.Fatal("unique ID wasn't set")
	}

	// check checks the metadata
	check := func(sf *SiaFile, version [16]byte) error {
		// It should have a UID now.
		if sf.staticMetadata.UniqueID == "" {
			return errors.New("unique ID still blank")
		}

		// Check Version
		if sf.staticMetadata.StaticVersion != version {
			return fmt.Errorf("Wrong version, expected %v got %v", version, sf.staticMetadata.StaticVersion)
		}
		return nil
	}

	// reset resets the metadata
	reset := func(sf *SiaFile) {
		// Set the UID to a blank string, and reset the metadata version
		sf.staticMetadata.UniqueID = ""
		sf.staticMetadata.StaticVersion = nilMetadataVesion
	}

	// Reset the metadata
	reset(sf)

	// Test upgradeMetadataFromNilToV1
	sf.upgradeMetadataFromNilToV1()

	// Check
	err := check(sf, metadataVersion1)
	if err != nil {
		t.Fatal(err)
	}

	// Reset the metadata
	reset(sf)

	// Save the metadata to disk
	err = sf.SaveMetadata()
	if err != nil {
		t.Fatal(err)
	}

	// Load the file again.
	sf, err = LoadSiaFile(sf.siaFilePath, wal)
	if err != nil {
		t.Fatal(err)
	}

	// Check
	err = check(sf, metadataVersion)
	if err != nil {
		t.Fatal(err)
	}
}

// testZeroByteFileCompat checks that 0-byte siafiles that have been uploaded
// before caching was introduced have the correct cached values after being
// loaded.
func testZeroByteFileCompat(t *testing.T) {
	// Create the file.
	siaFilePath, _, source, rc, sk, _, _, fileMode := newTestFileParams(1, true)
	sf, wal, _ := customTestFileAndWAL(siaFilePath, source, rc, sk, 0, 0, fileMode)
	// Check that the number of chunks in the file is correct.
	if sf.numChunks != 0 {
		panic(fmt.Sprintf("newTestFile didn't create the expected number of chunks: %v", sf.numChunks))
	}

	expectedRedundancy := float64(rc.NumPieces()) / float64(rc.MinPieces())
	// check checks the metadata
	check := func(sf *SiaFile, version [16]byte) error {
		// Make sure the loaded file has the correct cached values.
		if sf.staticMetadata.CachedHealth != 0 {
			return fmt.Errorf("CachedHealth should be 0 but was %v", sf.staticMetadata.CachedHealth)
		}
		if sf.staticMetadata.CachedRedundancy != expectedRedundancy {
			return fmt.Errorf("CachedRedundancy should be %v but was %v", expectedRedundancy, sf.staticMetadata.CachedRedundancy)
		}
		if sf.staticMetadata.CachedRepairBytes != 0 {
			return fmt.Errorf("CachedRepairBytes should be 0 but was %v", sf.staticMetadata.CachedRepairBytes)
		}
		if sf.staticMetadata.CachedStuckBytes != 0 {
			return fmt.Errorf("CachedStuckBytes should be 0 but was %v", sf.staticMetadata.CachedStuckBytes)
		}
		if sf.staticMetadata.CachedStuckHealth != 0 {
			return fmt.Errorf("CachedStuckHealth should be 0 but was %v", sf.staticMetadata.CachedStuckHealth)
		}
		if sf.staticMetadata.CachedUserRedundancy != expectedRedundancy {
			return fmt.Errorf("CachedRedundancy should be %v but was %v", expectedRedundancy, sf.staticMetadata.CachedUserRedundancy)
		}
		if sf.staticMetadata.CachedUploadProgress != 100 {
			return fmt.Errorf("CachedUploadProgress should be 100 but was %v", sf.staticMetadata.CachedUploadProgress)
		}
		// Check Version
		if sf.staticMetadata.StaticVersion != version {
			return fmt.Errorf("Wrong version, expected %v got %v", version, sf.staticMetadata.StaticVersion)
		}
		return nil
	}

	// reset resets the metadata
	reset := func(sf *SiaFile) {
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
	}

	// Reset the metadata to pre compat state
	reset(sf)

	// test upgradeMetadataFromNilToV1
	sf.upgradeMetadataFromNilToV1()

	// Check the Metadata
	err := check(sf, metadataVersion1)
	if err != nil {
		t.Fatal(err)
	}

	// Reset the metadata to pre compat state
	reset(sf)

	// Save the file and reload it.
	if err := sf.SaveMetadata(); err != nil {
		t.Fatal(err)
	}
	sf, err = loadSiaFile(siaFilePath, wal, modules.ProdDependencies)
	if err != nil {
		t.Fatal(err)
	}

	// Check the Metadata
	err = check(sf, metadataVersion)
	if err != nil {
		t.Fatal(err)
	}
}
