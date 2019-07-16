package renter

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/renter/siafile"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/Sia/siatest/dependencies"
	"gitlab.com/NebulousLabs/fastrand"
)

// TestRenterUploadDirectory verifies that the renter returns an error if a
// directory is provided as the source of an upload.
func TestRenterUploadDirectory(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	rt, err := newRenterTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	testUploadPath, err := ioutil.TempDir("", t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testUploadPath)

	ec, err := siafile.NewRSCode(defaultDataPieces, defaultParityPieces)
	if err != nil {
		t.Fatal(err)
	}
	params := modules.FileUploadParams{
		Source:      testUploadPath,
		SiaPath:     modules.RandomSiaPath(),
		ErasureCode: ec,
	}
	err = rt.renter.Upload(params)
	if err == nil {
		t.Fatal("expected Upload to fail with empty directory as source")
	}
	if err != errUploadDirectory {
		t.Fatal("expected errUploadDirectory, got", err)
	}
}

// TestRenterUpload probes Upload to ensure that it functions as intended for
// initializing an upload
func TestRenterUpload(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}

	// Create Renter
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Create file on disk
	path := filepath.Join(rt.renter.staticFilesDir, persist.RandomSuffix())
	err = ioutil.WriteFile(path, fastrand.Bytes(100), 0600)
	if err != nil {
		t.Fatal(err)
	}

	// Create siapath that will create a directory
	dirSiaPath, err := modules.NewSiaPath("dir")
	if err != nil {
		t.Fatal(err)
	}
	fileSiaPath, err := dirSiaPath.Join("file")
	if err != nil {
		t.Fatal(err)
	}

	// Create upload params and upload
	up := modules.FileUploadParams{
		Source:  path,
		SiaPath: fileSiaPath,
	}
	err = rt.renter.Upload(up)
	if err != nil {
		t.Fatal(err)
	}

	// Confirm SiaDir and SiaFile were created
	_, err = rt.renter.staticDirSet.Open(dirSiaPath)
	if err != nil {
		t.Fatal(err)
	}
	file, err := rt.renter.staticFileSet.Open(fileSiaPath)
	if err != nil {
		t.Fatal(err)
	}

	// Confirm chunks were added to the upload heap
	if rt.renter.uploadHeap.managedLen() != int(file.NumChunks()) {
		t.Fatalf("Unexpected number of chunks in upload heap. Expected %v got %v", rt.renter.uploadHeap.managedLen(), file.NumChunks())
	}

	// Record initial health stats from file
	nilMap := make(map[string]bool)
	health, stuckHealth, numStuckChunks := file.Health(nilMap, nilMap)

	// Directory and Health Directory should both be the same and they should
	// match the initial file health stats due to the metadata update call from
	// Upload
	err = build.Retry(100, 10*time.Millisecond, func() error {
		siaDir, err := rt.renter.staticDirSet.Open(dirSiaPath)
		if err != nil {
			return err
		}
		defer siaDir.Close()
		siaDirMetadata := siaDir.Metadata()
		rootSiaDir, err := rt.renter.staticDirSet.Open(modules.RootSiaPath())
		if err != nil {
			return err
		}
		defer rootSiaDir.Close()
		rootSiaDirMetadata := rootSiaDir.Metadata()
		siaDirHealthUpdated := siaDirMetadata.Health == health &&
			siaDirMetadata.StuckHealth == stuckHealth &&
			siaDirMetadata.NumStuckChunks == numStuckChunks
		siaDirAggregateHealthUpdated := siaDirMetadata.AggregateHealth == health &&
			siaDirMetadata.AggregateStuckHealth == stuckHealth &&
			siaDirMetadata.AggregateNumStuckChunks == numStuckChunks
		rootSiaDirAggregateHealthUpdated := rootSiaDirMetadata.AggregateHealth == health &&
			rootSiaDirMetadata.AggregateStuckHealth == stuckHealth &&
			rootSiaDirMetadata.AggregateNumStuckChunks == numStuckChunks
		if !siaDirHealthUpdated || !siaDirAggregateHealthUpdated || !rootSiaDirAggregateHealthUpdated {
			return fmt.Errorf(`directory metadata not updated
			siadir: %v
			rootDir: %v
			`, siaDirMetadata, rootSiaDirMetadata)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
