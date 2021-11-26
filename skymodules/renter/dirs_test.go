package renter

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/siatest/dependencies"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem/siadir"
)

// timeEquals is a helper function for checking if two times are equal
//
// Since we can't check timestamps for equality cause they are often set to
// `time.Now()` by methods, we allow a timestamp to be off by a certain delta.
func timeEquals(t1, t2 time.Time, delta time.Duration) bool {
	if t1.After(t2) && t1.After(t2.Add(delta)) {
		return false
	}
	if t2.After(t1) && t2.After(t1.Add(delta)) {
		return false
	}
	return true
}

// equivalentMetadata checks whether the metadata of two directories is
// equivalent. In this case that means most of the fields are expected to match,
// but some fields like the timestamp are allowed to be off by a bit.
func equivalentMetadata(md1, md2 siadir.Metadata, delta time.Duration) (err error) {
	// Check all the time fields first
	// Check AggregateLastHealthCheckTime
	if !timeEquals(md1.AggregateLastHealthCheckTime, md2.AggregateLastHealthCheckTime, delta) {
		err = errors.Compose(err, fmt.Errorf("AggregateLastHealthCheckTimes not equal %v and %v (%v)", md1.AggregateLastHealthCheckTime, md2.AggregateLastHealthCheckTime, delta))
	}
	// Check AggregateModTime
	if !timeEquals(md2.AggregateModTime, md1.AggregateModTime, delta) {
		err = errors.Compose(err, fmt.Errorf("AggregateModTime not equal %v and %v (%v)", md1.AggregateModTime, md2.AggregateModTime, delta))
	}
	// Check LastHealthCheckTime
	if !timeEquals(md1.LastHealthCheckTime, md2.LastHealthCheckTime, delta) {
		err = errors.Compose(err, fmt.Errorf("LastHealthCheckTimes not equal %v and %v (%v)", md1.LastHealthCheckTime, md2.LastHealthCheckTime, delta))
	}
	// Check ModTime
	if !timeEquals(md2.ModTime, md1.ModTime, delta) {
		err = errors.Compose(err, fmt.Errorf("ModTime not equal %v and %v (%v)", md1.ModTime, md2.ModTime, delta))
	}

	// Check a copy of md2 with the time fields of md1 and check for Equality
	md2Copy := md2
	md2Copy.AggregateLastHealthCheckTime = md1.AggregateLastHealthCheckTime
	md2Copy.AggregateModTime = md1.AggregateModTime
	md2Copy.LastHealthCheckTime = md1.LastHealthCheckTime
	md2Copy.ModTime = md1.ModTime
	return errors.Compose(err, siadir.EqualMetadatas(md1, md2Copy))
}

// FileListCollect returns information on all of the files stored by the
// renter at the specified folder. The 'cached' argument specifies whether
// cached values should be returned or not.
func (r *Renter) FileListCollect(siaPath skymodules.SiaPath, recursive, cached bool) ([]skymodules.FileInfo, error) {
	var files []skymodules.FileInfo
	var mu sync.Mutex
	err := r.FileList(siaPath, recursive, cached, func(fi skymodules.FileInfo) {
		mu.Lock()
		files = append(files, fi)
		mu.Unlock()
	})
	// Sort slices by SiaPath.
	sort.Slice(files, func(i, j int) bool {
		return files[i].SiaPath.String() < files[j].SiaPath.String()
	})
	return files, err
}

// TestRenterCreateDirectories checks that the renter properly created metadata files
// for direcotries
func TestRenterCreateDirectories(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create renterTester
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Test creating directory
	siaPath, err := skymodules.NewSiaPath("foo/bar/baz")
	if err != nil {
		t.Fatal(err)
	}
	err = rt.renter.CreateDir(siaPath, skymodules.DefaultDirPerm)
	if err != nil {
		t.Fatal(err)
	}

	// Confirm that directory metadata files were created in all directories
	for {
		if err := rt.checkDirInitialized(siaPath); err != nil {
			t.Logf("check for '%v'", siaPath)
			t.Fatal(err)
		}
		if siaPath.IsRoot() {
			break
		}
		siaPath, err = siaPath.Dir()
		if err != nil {
			t.Fatal(err)
		}
	}
}

// checkDirInitialized is a helper function that checks that the directory was
// initialized correctly and the metadata file exist and contain the correct
// information
func (rt *renterTester) checkDirInitialized(siaPath skymodules.SiaPath) (err error) {
	siaDir, err := rt.renter.staticFileSystem.OpenSiaDir(siaPath)
	if err != nil {
		return fmt.Errorf("unable to load directory %v metadata: %v", siaPath, err)
	}
	defer func() {
		err = errors.Compose(err, siaDir.Close())
	}()
	fullpath := siaPath.SiaDirMetadataSysPath(rt.renter.staticFileSystem.Root())
	if _, err := os.Stat(fullpath); err != nil {
		return err
	}

	// Check that metadata is default value
	metadata, err := siaDir.Metadata()
	if err != nil {
		return err
	}
	// Check Aggregate Fields
	if metadata.AggregateHealth != siadir.DefaultDirHealth {
		return fmt.Errorf("AggregateHealth not initialized properly: have %v expected %v", metadata.AggregateHealth, siadir.DefaultDirHealth)
	}
	if !metadata.AggregateLastHealthCheckTime.IsZero() {
		return fmt.Errorf("AggregateLastHealthCheckTime should be a zero timestamp: %v", metadata.AggregateLastHealthCheckTime)
	}
	if metadata.AggregateModTime.IsZero() {
		return fmt.Errorf("AggregateModTime not initialized: %v", metadata.AggregateModTime)
	}
	if metadata.AggregateMinRedundancy != siadir.DefaultDirRedundancy {
		return fmt.Errorf("AggregateMinRedundancy not initialized properly: have %v expected %v", metadata.AggregateMinRedundancy, siadir.DefaultDirRedundancy)
	}
	if metadata.AggregateStuckHealth != siadir.DefaultDirHealth {
		return fmt.Errorf("AggregateStuckHealth not initialized properly: have %v expected %v", metadata.AggregateStuckHealth, siadir.DefaultDirHealth)
	}
	// Check SiaDir Fields
	if metadata.Health != siadir.DefaultDirHealth {
		return fmt.Errorf("Health not initialized properly: have %v expected %v", metadata.Health, siadir.DefaultDirHealth)
	}
	if !metadata.LastHealthCheckTime.IsZero() {
		return fmt.Errorf("LastHealthCheckTime should be a zero timestamp: %v", metadata.LastHealthCheckTime)
	}
	if metadata.ModTime.IsZero() {
		return fmt.Errorf("ModTime not initialized: %v", metadata.ModTime)
	}
	if metadata.MinRedundancy != siadir.DefaultDirRedundancy {
		return fmt.Errorf("MinRedundancy not initialized properly: have %v expected %v", metadata.MinRedundancy, siadir.DefaultDirRedundancy)
	}
	if metadata.StuckHealth != siadir.DefaultDirHealth {
		return fmt.Errorf("StuckHealth not initialized properly: have %v expected %v", metadata.StuckHealth, siadir.DefaultDirHealth)
	}
	path, err := siaDir.Path()
	if err != nil {
		return err
	}
	if path != rt.renter.staticFileSystem.DirPath(siaPath) {
		return fmt.Errorf("Expected path to be %v, got %v", path, rt.renter.staticFileSystem.DirPath(siaPath))
	}
	return nil
}

// TestDirInfo probes the DirInfo method
func TestDirInfo(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create renterTester
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create directory
	siaPath, err := skymodules.NewSiaPath("foo/")
	if err != nil {
		t.Fatal(err)
	}
	err = rt.renter.CreateDir(siaPath, skymodules.DefaultDirPerm)
	if err != nil {
		t.Fatal(err)
	}

	// Check that DirInfo returns the same information as stored in the metadata
	fooDirInfo, err := rt.renter.staticFileSystem.DirInfo(siaPath)
	if err != nil {
		t.Fatal(err)
	}
	rootDirInfo, err := rt.renter.staticFileSystem.DirInfo(skymodules.RootSiaPath())
	if err != nil {
		t.Fatal(err)
	}
	fooEntry, err := rt.renter.staticFileSystem.OpenSiaDir(siaPath)
	if err != nil {
		t.Fatal(err)
	}
	rootEntry, err := rt.renter.staticFileSystem.OpenSiaDir(skymodules.RootSiaPath())
	if err != nil {
		t.Fatal(err)
	}
	err = compareDirectoryInfoAndMetadata(fooDirInfo, fooEntry)
	if err != nil {
		t.Fatal(err)
	}
	err = compareDirectoryInfoAndMetadata(rootDirInfo, rootEntry)
	if err != nil {
		t.Fatal(err)
	}
}

// TestRenterListDirectory verifies that the renter properly lists the contents
// of a directory
func TestRenterListDirectory(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create renterTester
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create directory
	siaPath, err := skymodules.NewSiaPath("foo/")
	if err != nil {
		t.Fatal(err)
	}
	err = rt.renter.CreateDir(siaPath, skymodules.DefaultDirPerm)
	if err != nil {
		t.Fatal(err)
	}

	// Upload a file
	_, err = rt.renter.newRenterTestFile()
	if err != nil {
		t.Fatal(err)
	}

	// Confirm that we get expected number of FileInfo and DirectoryInfo.
	directories, err := rt.renter.DirList(skymodules.RootSiaPath())
	if err != nil {
		t.Fatal(err)
	}
	// 5 Directories because of root, foo, home, var, and snapshots
	if len(directories) != 5 {
		t.Fatal("Expected 5 DirectoryInfos but got", len(directories))
	}
	files, err := rt.renter.FileListCollect(skymodules.RootSiaPath(), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatal("Expected 1 FileInfo but got", len(files))
	}

	// Ensure that the metadata of all the directories gets collected into the
	// root aggregate stats.
	for _, dir := range directories {
		err = rt.renter.UpdateMetadata(dir.SiaPath, false)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Wait for root directory to show proper number of files and subdirs.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		root, err := rt.renter.staticFileSystem.OpenSiaDir(skymodules.RootSiaPath())
		if err != nil {
			return err
		}
		rootMD, err := root.Metadata()
		if err != nil {
			return err
		}
		// Check the aggregate and siadir fields.
		//
		// Expecting /home, /home/user, /var, /var/skynet, /snapshots, /foo
		if rootMD.AggregateNumSubDirs != 6 {
			return fmt.Errorf("Expected 6 subdirs in aggregate but got %v", rootMD.AggregateNumSubDirs)
		}
		if rootMD.NumSubDirs != 4 {
			return fmt.Errorf("Expected 4 subdirs but got %v", rootMD.NumSubDirs)
		}
		if rootMD.AggregateNumFiles != 1 {
			return fmt.Errorf("Expected 1 file in aggregate but got %v", rootMD.AggregateNumFiles)
		}
		if rootMD.NumFiles != 1 {
			return fmt.Errorf("Expected 1 file but got %v", rootMD.NumFiles)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify that the directory information matches the on disk information
	err = build.Retry(100, 100*time.Millisecond, func() error {
		rootDir, err := rt.renter.staticFileSystem.OpenSiaDir(skymodules.RootSiaPath())
		if err != nil {
			return err
		}
		fooDir, err := rt.renter.staticFileSystem.OpenSiaDir(siaPath)
		if err != nil {
			return err
		}
		homeDir, err := rt.renter.staticFileSystem.OpenSiaDir(skymodules.HomeFolder)
		if err != nil {
			return err
		}
		snapshotsDir, err := rt.renter.staticFileSystem.OpenSiaDir(skymodules.BackupFolder)
		if err != nil {
			return err
		}
		defer func() {
			err = errors.Compose(err, rootDir.Close(), fooDir.Close(), homeDir.Close(), snapshotsDir.Close())
		}()

		// Refresh Directories
		directories, err = rt.renter.DirList(skymodules.RootSiaPath())
		if err != nil {
			return err
		}
		// Sort directories.
		sort.Slice(directories, func(i, j int) bool {
			return strings.Compare(directories[i].SiaPath.String(), directories[j].SiaPath.String()) < 0
		})
		if err = compareDirectoryInfoAndMetadataCustom(directories[0], rootDir, false); err != nil {
			return err
		}
		if err = compareDirectoryInfoAndMetadataCustom(directories[1], fooDir, false); err != nil {
			return err
		}
		if err = compareDirectoryInfoAndMetadataCustom(directories[2], homeDir, false); err != nil {
			return err
		}
		if err = compareDirectoryInfoAndMetadataCustom(directories[3], snapshotsDir, false); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// compareDirectoryInfoAndMetadata is a helper that compares the information in
// a DirectoryInfo struct and a SiaDirSetEntry struct
func compareDirectoryInfoAndMetadata(di skymodules.DirectoryInfo, siaDir *filesystem.DirNode) error {
	return compareDirectoryInfoAndMetadataCustom(di, siaDir, true)
}

// compareDirectoryInfoAndMetadataCustom is a helper that compares the
// information in a DirectoryInfo struct and a SiaDirSetEntry struct with the
// option to ignore fields based on differences in persistence
func compareDirectoryInfoAndMetadataCustom(di skymodules.DirectoryInfo, siaDir *filesystem.DirNode, checkTimes bool) error {
	md, err := siaDir.Metadata()
	if err != nil {
		return err
	}

	// Compare Aggregate Fields
	if md.AggregateHealth != di.AggregateHealth {
		return fmt.Errorf("AggregateHealths not equal, %v and %v", md.AggregateHealth, di.AggregateHealth)
	}
	aggregateMaxHealth := math.Max(md.AggregateHealth, md.AggregateStuckHealth)
	if di.AggregateMaxHealth != aggregateMaxHealth {
		return fmt.Errorf("AggregateMaxHealths not equal %v and %v", di.AggregateMaxHealth, aggregateMaxHealth)
	}
	aggregateMaxHealthPercentage := skymodules.HealthPercentage(aggregateMaxHealth)
	if di.AggregateMaxHealthPercentage != aggregateMaxHealthPercentage {
		return fmt.Errorf("AggregateMaxHealthPercentage not equal %v and %v", di.AggregateMaxHealthPercentage, aggregateMaxHealthPercentage)
	}
	if md.AggregateMinRedundancy != di.AggregateMinRedundancy {
		return fmt.Errorf("AggregateMinRedundancy not equal, %v and %v", md.AggregateMinRedundancy, di.AggregateMinRedundancy)
	}
	if md.AggregateNumFiles != di.AggregateNumFiles {
		return fmt.Errorf("AggregateNumFiles not equal, %v and %v", md.AggregateNumFiles, di.AggregateNumFiles)
	}
	if md.AggregateNumLostFiles != di.AggregateNumLostFiles {
		return fmt.Errorf("AggregateNumLostFiles not equal, %v and %v", md.AggregateNumLostFiles, di.AggregateNumLostFiles)
	}
	if md.AggregateNumStuckChunks != di.AggregateNumStuckChunks {
		return fmt.Errorf("AggregateNumStuckChunks not equal, %v and %v", md.AggregateNumStuckChunks, di.AggregateNumStuckChunks)
	}
	if md.AggregateNumSubDirs != di.AggregateNumSubDirs {
		return fmt.Errorf("AggregateNumSubDirs not equal, %v and %v", md.AggregateNumSubDirs, di.AggregateNumSubDirs)
	}
	if md.AggregateNumUnfinishedFiles != di.AggregateNumUnfinishedFiles {
		return fmt.Errorf("AggregateNumUnfinishedFiles not equal, %v and %v", md.AggregateNumUnfinishedFiles, di.AggregateNumUnfinishedFiles)
	}
	if md.AggregateSize != di.AggregateSize {
		return fmt.Errorf("AggregateSizes not equal, %v and %v", md.AggregateSize, di.AggregateSize)
	}
	if md.NumStuckChunks != di.AggregateNumStuckChunks {
		return fmt.Errorf("NumStuckChunks not equal, %v and %v", md.NumStuckChunks, di.AggregateNumStuckChunks)
	}

	// Compare Aggregate Time Fields
	if checkTimes {
		if di.AggregateLastHealthCheckTime != md.AggregateLastHealthCheckTime {
			return fmt.Errorf("AggregateLastHealthCheckTimes not equal %v and %v", di.AggregateLastHealthCheckTime, md.AggregateLastHealthCheckTime)
		}
		if di.AggregateMostRecentModTime != md.AggregateModTime {
			return fmt.Errorf("AggregateModTimes not equal %v and %v", di.AggregateMostRecentModTime, md.AggregateModTime)
		}
	}

	// Compare Directory Fields
	if md.Health != di.Health {
		return fmt.Errorf("healths not equal, %v and %v", md.Health, di.Health)
	}
	maxHealth := math.Max(md.Health, md.StuckHealth)
	if di.MaxHealth != maxHealth {
		return fmt.Errorf("MaxHealths not equal %v and %v", di.MaxHealth, maxHealth)
	}
	maxHealthPercentage := skymodules.HealthPercentage(maxHealth)
	if di.MaxHealthPercentage != maxHealthPercentage {
		return fmt.Errorf("MaxHealthPercentage not equal %v and %v", di.MaxHealthPercentage, maxHealthPercentage)
	}
	if md.MinRedundancy != di.MinRedundancy {
		return fmt.Errorf("MinRedundancy not equal, %v and %v", md.MinRedundancy, di.MinRedundancy)
	}
	if md.NumFiles != di.NumFiles {
		return fmt.Errorf("NumFiles not equal, %v and %v", md.NumFiles, di.NumFiles)
	}
	if md.NumLostFiles != di.NumLostFiles {
		return fmt.Errorf("NumLostFiles not equal, %v and %v", md.NumLostFiles, di.NumLostFiles)
	}
	if md.NumStuckChunks != di.NumStuckChunks {
		return fmt.Errorf("NumStuckChunks not equal, %v and %v", md.NumStuckChunks, di.NumStuckChunks)
	}
	if md.NumSubDirs != di.NumSubDirs {
		return fmt.Errorf("NumSubDirs not equal, %v and %v", md.NumSubDirs, di.NumSubDirs)
	}
	if md.NumUnfinishedFiles != di.NumUnfinishedFiles {
		return fmt.Errorf("NumUnfinishedFiles not equal, %v and %v", md.NumUnfinishedFiles, di.NumUnfinishedFiles)
	}
	if md.Size != di.DirSize {
		return fmt.Errorf("Sizes not equal, %v and %v", md.Size, di.DirSize)
	}
	if md.StuckHealth != di.StuckHealth {
		return fmt.Errorf("stuck healths not equal, %v and %v", md.StuckHealth, di.StuckHealth)
	}

	// Compare Directory Time Fields
	if checkTimes {
		if di.LastHealthCheckTime != md.LastHealthCheckTime {
			return fmt.Errorf("LastHealthCheckTimes not equal %v and %v", di.LastHealthCheckTime, md.LastHealthCheckTime)
		}
		if di.MostRecentModTime != md.ModTime {
			return fmt.Errorf("ModTimes not equal %v and %v", di.MostRecentModTime, md.ModTime)
		}
	}
	return nil
}
