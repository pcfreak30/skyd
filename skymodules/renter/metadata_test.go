package renter

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/skynetlabs/skyd/siatest/dependencies"
	"gitlab.com/skynetlabs/skyd/skymodules"
	"gitlab.com/skynetlabs/skyd/skymodules/renter/filesystem/siadir"
)

// randomMetadata returns a siadir Metadata struct with random values set
func randomMetadata() siadir.Metadata {
	md := siadir.Metadata{
		AggregateHealth:              float64(fastrand.Intn(100)),
		AggregateLastHealthCheckTime: time.Now(),
		AggregateMinRedundancy:       float64(fastrand.Intn(100)),
		AggregateModTime:             time.Now(),
		AggregateNumFiles:            fastrand.Uint64n(100),
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

// TODO:
//  - Combine following tests to verify managedCalculateDirectoryMetadata and
//  move to metadata_test.go
//		- TestBubbleHealth
//		- TestNumFiles
//		- TestDirectorySize
//		- TestDirectoryModTime
//
func TestCalculateDirectoryMetadata(t *testing.T) {}
