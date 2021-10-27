package renter

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/siatest/dependencies"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem/siadir"
	"go.sia.tech/siad/modules"
)

// TestCalculateFileMetadatas probes the calculate file metadata methods of the
// renter.
func TestCalculateFileMetadatas(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create renter
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}

	// Add files
	var siaPaths []skymodules.SiaPath
	for i := 0; i < 5; i++ {
		sf, err := rt.renter.newRenterTestFile()
		if err != nil {
			t.Fatal(err)
		}
		siaPath := rt.renter.staticFileSystem.FileSiaPath(sf)
		siaPaths = append(siaPaths, siaPath)
	}

	// calculate metadatas individually
	var mds1 []bubbledSiaFileMetadata
	for _, siaPath := range siaPaths {
		md, err := rt.renter.managedCachedFileMetadata(siaPath)
		if err != nil {
			t.Fatal(err)
		}
		mds1 = append(mds1, md)
	}

	// calculate metadatas together
	mds2, err := rt.renter.managedCachedFileMetadatas(siaPaths)
	if err != nil {
		t.Fatal(err)
	}

	// sort by siapath
	sort.Slice(mds1, func(i, j int) bool {
		return strings.Compare(mds1[i].sp.String(), mds1[j].sp.String()) < 0
	})
	sort.Slice(mds2, func(i, j int) bool {
		return strings.Compare(mds2[i].sp.String(), mds2[j].sp.String()) < 0
	})

	// Compare the two slices of metadatas
	if !reflect.DeepEqual(mds1, mds2) {
		t.Log("mds1:", mds1)
		t.Log("mds2:", mds2)
		t.Fatal("different metadatas")
	}
}

// TestDirectoryMetadatas probes the directory metadata methods of the
// renter.
func TestDirectoryMetadatas(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create renter
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}

	// Add directories
	var siaPaths []skymodules.SiaPath
	for i := 0; i < 5; i++ {
		siaPath := skymodules.RandomSiaPath()
		err = rt.renter.CreateDir(siaPath, skymodules.DefaultDirPerm)
		if err != nil {
			t.Fatal(err)
		}
		siaPaths = append(siaPaths, siaPath)
	}

	// Get metadatas individually
	var mds1 []bubbledSiaDirMetadata
	for _, siaPath := range siaPaths {
		md, err := rt.renter.managedDirectoryMetadata(siaPath)
		if err != nil {
			t.Fatal(err)
		}
		mds1 = append(mds1, bubbledSiaDirMetadata{
			siaPath,
			md,
		})
	}

	// Get metadatas together
	mds2, err := rt.renter.managedDirectoryMetadatas(siaPaths)
	if err != nil {
		t.Fatal(err)
	}

	// sort by siapath
	sort.Slice(mds1, func(i, j int) bool {
		return strings.Compare(mds1[i].sp.String(), mds1[j].sp.String()) < 0
	})
	sort.Slice(mds2, func(i, j int) bool {
		return strings.Compare(mds2[i].sp.String(), mds2[j].sp.String()) < 0
	})

	// Compare the two slices of metadatas
	if !reflect.DeepEqual(mds1, mds2) {
		t.Log("mds1:", mds1)
		t.Log("mds2:", mds2)
		t.Fatal("different metadatas")
	}
}

// TestCalculateDirectoryMetadata probes the callCalculateDirectoryMetadata
// method
func TestCalculateDirectoryMetadata(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create renter with deps
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Define common test parameters
	fileSize := uint64(100)
	var dp, pp int = 1, 1
	rsc, _ := skymodules.NewRSCode(dp, pp)
	worstFileHealth := 1 - (0-float64(dp))/float64(pp)
	repairSize := modules.SectorSize * uint64(dp+pp)

	// Add a file to the root directory
	rootFile, err := rt.newTestSiaFile(skymodules.RandomSiaPath(), "", rsc, fileSize)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rootFile.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Add a file to the root directory that has a skylink
	rootSkyFile, err := rt.newTestSiaFile(skymodules.RandomSiaPath(), "", rsc, fileSize)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rootSkyFile.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	var skylink skymodules.Skylink
	err = rootSkyFile.AddSkylink(skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Update siafile metadatas
	err = rt.updateFileMetadatas(skymodules.RootSiaPath())
	if err != nil {
		t.Fatal(err)
	}

	// Mock adding a file to the skynet directory by updating the var metadata
	varMetadata := siadir.Metadata{
		AggregateLastHealthCheckTime: time.Now(),
		AggregateNumFiles:            1,
		AggregateNumSubDirs:          1,
		AggregateRepairSize:          repairSize,
		AggregateSize:                modules.SectorSize,

		AggregateSkynetFiles: 1,
		AggregateSkynetSize:  modules.SectorSize,
	}
	if err := rt.openAndUpdateDir(skymodules.VarFolder, varMetadata); err != nil {
		t.Fatal(err)
	}

	// Make sure the home folder has the bare minimum information expected
	homeMetadata := siadir.Metadata{
		AggregateLastHealthCheckTime: time.Now(),
		AggregateNumSubDirs:          1,
	}
	if err := rt.openAndUpdateDir(skymodules.HomeFolder, homeMetadata); err != nil {
		t.Fatal(err)
	}

	// Make sure the backup folder has the bare minimum information expected
	backupMetadata := siadir.Metadata{
		AggregateLastHealthCheckTime: time.Now(),
	}
	if err := rt.openAndUpdateDir(skymodules.BackupFolder, backupMetadata); err != nil {
		t.Fatal(err)
	}

	// Define expected Metadata
	beforeUpdate := time.Now()
	expected := siadir.Metadata{
		AggregateHealth:              worstFileHealth,
		AggregateLastHealthCheckTime: beforeUpdate,
		AggregateMinRedundancy:       0,
		AggregateModTime:             beforeUpdate,
		AggregateNumFiles:            3,
		AggregateNumLostFiles:        2,
		AggregateNumStuckChunks:      0,
		AggregateNumSubDirs:          5,
		AggregateRemoteHealth:        worstFileHealth,
		AggregateRepairSize:          3 * repairSize,
		AggregateSize:                2*fileSize + modules.SectorSize,
		AggregateStuckHealth:         0,
		AggregateStuckSize:           0,

		AggregateSkynetFiles: 1,
		AggregateSkynetSize:  fileSize + modules.SectorSize,

		Health:              worstFileHealth,
		LastHealthCheckTime: beforeUpdate,
		MinRedundancy:       0,
		ModTime:             beforeUpdate,
		NumFiles:            2,
		NumLostFiles:        2,
		NumStuckChunks:      0,
		NumSubDirs:          3,
		RemoteHealth:        worstFileHealth,
		RepairSize:          2 * repairSize,
		Size:                2 * fileSize,
		StuckHealth:         0,
		StuckSize:           0,

		SkynetFiles: 0,
		SkynetSize:  fileSize,
	}

	// call callCalculateDirectoryMetadata
	md, err := rt.renter.callCalculateDirectoryMetadata(skymodules.RootSiaPath())
	if err != nil {
		t.Fatal(err)
	}

	// Explicitly check that the times should have been updated
	if !md.AggregateLastHealthCheckTime.Before(beforeUpdate) {
		t.Error("AggregateLastHealthCheckTime was not updated")
	}
	expected.AggregateLastHealthCheckTime = md.AggregateLastHealthCheckTime
	if !md.LastHealthCheckTime.Before(beforeUpdate) {
		t.Error("LastHealthCheckTime was not updated")
	}
	expected.LastHealthCheckTime = md.LastHealthCheckTime
	if !md.AggregateModTime.Before(beforeUpdate) {
		t.Error("AggregateModTime was not updated")
	}
	expected.AggregateModTime = md.AggregateModTime
	if !md.ModTime.Before(beforeUpdate) {
		t.Error("ModTime was not updated")
	}
	expected.ModTime = md.ModTime

	// Verify metadata
	err = equivalentMetadata(md, expected, time.Duration(0))
	if err != nil {
		expectedData, _ := json.MarshalIndent(expected, "", " ")
		actual, _ := json.MarshalIndent(md, "", " ")
		t.Log("Expected Metadata")
		t.Log(string(expectedData))
		t.Log("Actual Metadata")
		t.Log(string(actual))
		t.Fatal(err)
	}
}

// TestPruneUnfinishedFiles probes the pruning of unfinished files.
func TestPruneUnfinishedFiles(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	t.Skip("Re-enable with deletion")

	// Create renter
	rt, err := newRenterTesterWithDependency(t.Name(), dependencies.NewDependencyUnfinishedFiles())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create file
	sp := skymodules.RandomSiaPath()
	file, err := rt.renter.createRenterTestFile(sp)
	if err != nil {
		t.Fatal(err)
	}

	// Verify unfinished siafile metadata
	if file.Finished() {
		t.Fatal("File should be considered unfinished")
	}

	// Close File
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	// Trigger bubble
	err = rt.renter.UpdateMetadata(skymodules.RootSiaPath(), true)
	if err != nil {
		t.Fatal(err)
	}

	// Verify unfinished directory metadata
	dirs, err := rt.renter.DirList(skymodules.RootSiaPath())
	if err != nil {
		t.Fatal(err)
	}
	if dirs[0].AggregateNumUnfinishedFiles != 1 {
		t.Fatal("Expected 1 aggregate unfinished file, found", dirs[0].AggregateNumUnfinishedFiles)
	}
	if dirs[0].NumUnfinishedFiles != 1 {
		t.Fatal("Expected 1 unfinished file, found", dirs[0].NumUnfinishedFiles)
	}

	// Wait for file to be pruned
	err = build.Retry(15, time.Second, func() error {
		// Trigger bubble
		err = rt.renter.UpdateMetadata(skymodules.RootSiaPath(), true)
		if err != nil {
			return err
		}

		// Verify file is deleted
		dirs, err = rt.renter.DirList(skymodules.RootSiaPath())
		if err != nil {
			return err
		}
		if dirs[0].AggregateNumUnfinishedFiles != 0 {
			return fmt.Errorf("Expected 0 aggregate unfinished file, found %v", dirs[0].AggregateNumUnfinishedFiles)
		}
		if dirs[0].NumUnfinishedFiles != 0 {
			return fmt.Errorf("Expected 0 unfinished file, found %v", dirs[0].NumUnfinishedFiles)
		}
		_, err = rt.renter.File(sp)
		if err == nil || !errors.Contains(err, filesystem.ErrNotExist) {
			return fmt.Errorf("expected file to be deleted but got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
