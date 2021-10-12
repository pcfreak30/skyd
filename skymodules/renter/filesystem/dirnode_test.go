package filesystem

import (
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem/siadir"
)

// TestHealthPercentage checks the values returned from HealthPercentage
func TestHealthPercentage(t *testing.T) {
	var tests = []struct {
		health           float64
		healthPercentage float64
	}{
		{1.5, 0},
		{1.25, 0},
		{1.0, 25},
		{0.75, 50},
		{0.5, 75},
		{0.25, 100},
		{0, 100},
	}
	for _, test := range tests {
		hp := skymodules.HealthPercentage(test.health)
		if hp != test.healthPercentage {
			t.Fatalf("Expect %v got %v", test.healthPercentage, hp)
		}
	}
}

// TestUpdateSiaDirSetMetadata probes the UpdateMetadata method of the SiaDirSet
func TestUpdateSiaDirSetMetadata(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Prepare a filesystem with a dir.
	root := filepath.Join(testDir(t.Name()), "fs-root")
	fs := newTestFileSystem(root)
	sp := newSiaPath("path/to/dir")
	err := fs.NewSiaDir(sp, skymodules.DefaultDirPerm)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := fs.OpenSiaDir(sp)
	if err != nil {
		t.Fatal(err)
	}

	// Confirm metadata is set properly
	md, err := entry.Metadata()
	if err != nil {
		t.Fatal(err)
	}
	if err = siadir.VerifyMetadataInit(md); err != nil {
		t.Fatal(err)
	}

	// Update the metadata of the entry
	checkTime := time.Now()
	metadataUpdate := md
	// Aggregate fields
	metadataUpdate.AggregateHealth = 7
	metadataUpdate.AggregateLastHealthCheckTime = checkTime
	metadataUpdate.AggregateMinRedundancy = 2.2
	metadataUpdate.AggregateModTime = checkTime
	metadataUpdate.AggregateNumFiles = 11
	metadataUpdate.AggregateNumStuckChunks = 15
	metadataUpdate.AggregateNumSubDirs = 5
	metadataUpdate.AggregateNumUnfinishedFiles = 5
	metadataUpdate.AggregateSize = 2432
	metadataUpdate.AggregateStuckHealth = 5
	// SiaDir fields
	metadataUpdate.Health = 4
	metadataUpdate.LastHealthCheckTime = checkTime
	metadataUpdate.MinRedundancy = 2
	metadataUpdate.ModTime = checkTime
	metadataUpdate.NumFiles = 5
	metadataUpdate.NumStuckChunks = 6
	metadataUpdate.NumSubDirs = 4
	metadataUpdate.NumUnfinishedFiles = 5
	metadataUpdate.Size = 223
	metadataUpdate.StuckHealth = 2

	err = fs.UpdateDirMetadata(sp, metadataUpdate)
	if err != nil {
		t.Fatal(err)
	}

	// Check if the metadata was updated properly in memory and on disk
	md, err = entry.Metadata()
	if err != nil {
		t.Fatal(err)
	}
	err = siadir.EqualMetadatas(md, metadataUpdate)
	if err != nil {
		t.Fatal(err)
	}
}
