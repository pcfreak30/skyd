package renter

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/siatest/dependencies"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem/siadir"
)

// randomMetadata returns a siadir Metadata struct with random values set
func randomMetadata() siadir.Metadata {
	md := siadir.Metadata{
		AggregateHealth:              float64(fastrand.Intn(100)),
		AggregateLastHealthCheckTime: time.Now(),
		AggregateMinRedundancy:       float64(fastrand.Intn(100)),
		AggregateModTime:             time.Now(),
		AggregateNumFiles:            fastrand.Uint64n(100),
		AggregateNumLostFiles:        fastrand.Uint64n(100),
		AggregateNumStuckChunks:      fastrand.Uint64n(100),
		AggregateNumSubDirs:          fastrand.Uint64n(100),
		AggregateRemoteHealth:        float64(fastrand.Intn(100)),
		AggregateRepairSize:          fastrand.Uint64n(100),
		AggregateSize:                fastrand.Uint64n(100),
		AggregateStuckHealth:         float64(fastrand.Intn(100)),
		AggregateStuckSize:           fastrand.Uint64n(100),

		AggregateSkynetFiles: fastrand.Uint64n(100),
		AggregateSkynetSize:  fastrand.Uint64n(100),

		Health:              float64(fastrand.Intn(100)),
		LastHealthCheckTime: time.Now(),
		MinRedundancy:       float64(fastrand.Intn(100)),
		ModTime:             time.Now(),
		NumFiles:            fastrand.Uint64n(100),
		NumLostFiles:        fastrand.Uint64n(100),
		NumStuckChunks:      fastrand.Uint64n(100),
		NumSubDirs:          fastrand.Uint64n(100),
		RemoteHealth:        float64(fastrand.Intn(100)),
		RepairSize:          fastrand.Uint64n(100),
		Size:                fastrand.Uint64n(100),
		StuckHealth:         float64(fastrand.Intn(100)),
		StuckSize:           fastrand.Uint64n(100),

		SkynetFiles: fastrand.Uint64n(100),
		SkynetSize:  fastrand.Uint64n(100),
	}
	return md
}

// safeRandomMetadata returns random siadir metadata but with select fields
// adjusted to prevent developer errors.
//
// NOTE: You cannot set the NumStuckChunks to a non zero number without a
// file in the directory as this will create a developer error
func safeRandomMetadata() siadir.Metadata {
	md := randomMetadata()
	md.AggregateNumFiles = 0
	md.NumFiles = 0
	md.AggregateNumStuckChunks = 0
	md.NumStuckChunks = 0
	// Set LostFiles to zero as well since we can't have zero files but non zero
	// lost files.
	md.AggregateNumLostFiles = 0
	md.NumLostFiles = 0
	return md
}

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
	ct := crypto.RandomCipherType()
	worstFileHealth := 1 - (0-float64(dp))/float64(pp)
	repairSize := modules.SectorSize * uint64(dp+pp)

	// Add a file to the root directory
	rootFile, err := rt.renter.createRenterTestFileWithParamsAndSize(skymodules.RandomSiaPath(), rsc, ct, fileSize)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rootFile.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Add a file to the root directory that has a skylink
	rootSkyFile, err := rt.renter.createRenterTestFileWithParamsAndSize(skymodules.RandomSiaPath(), rsc, ct, fileSize)
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
	err = equalBubbledMetadata(md, expected, time.Duration(0))
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
