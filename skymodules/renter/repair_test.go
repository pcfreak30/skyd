package renter

import (
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/persist"

	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/siatest/dependencies"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem/siadir"
)

// updateFileMetadatas updates the metadata of all siafiles within a dir.
func (rt *renterTester) updateFileMetadatas(dirSiaPath skymodules.SiaPath) error {
	// Get cached offline and goodforrenew maps.
	offlineMap, goodForRenewMap, contracts, used := rt.renter.callRenterContractsAndUtilities()
	return rt.renter.managedUpdateFileMetadatasParams(dirSiaPath, offlineMap, goodForRenewMap, contracts, used)
}

// openAndUpdateDir is a helper method for updating a siadir metadata
func (rt *renterTester) openAndUpdateDir(siapath skymodules.SiaPath, metadata siadir.Metadata) error {
	siadir, err := rt.renter.staticFileSystem.OpenSiaDir(siapath)
	if err != nil {
		return err
	}
	err = siadir.UpdateMetadata(metadata)
	return errors.Compose(err, siadir.Close())
}

// TestDirectoryModTime verifies that the last update time of a directory is
// accurately reported
func TestDirectoryModTime(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a test directory with sub folders
	//
	// root/ file
	// root/SubDir1/
	// root/SubDir1/SubDir2/ file

	// Create test renter
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create directory tree
	subDir1, err := skymodules.NewSiaPath("SubDir1")
	if err != nil {
		t.Fatal(err)
	}
	subDir2, err := skymodules.NewSiaPath("SubDir2")
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.renter.CreateDir(subDir1, skymodules.DefaultDirPerm); err != nil {
		t.Fatal(err)
	}
	subDir1_2, err := subDir1.Join(subDir2.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.renter.CreateDir(subDir1_2, skymodules.DefaultDirPerm); err != nil {
		t.Fatal(err)
	}

	// Call Bubble to update filesystem ModTimes so there are no zero times
	if err := rt.renter.UpdateMetadata(subDir1_2, false); err != nil {
		t.Fatal(err)
	}
	// Sleep for 1 second to allow bubbles to update filesystem. Retry doesn't
	// work here as we are waiting for the ModTime to be fully updated but we
	// don't know what that value will be. We need this value to be updated and
	// static before we create the SiaFiles to be able to ensure the ModTimes of
	// the SiaFiles are the most recent
	time.Sleep(time.Second)

	// Add files
	sp1 := skymodules.RandomSiaPath()
	rsc, _ := skymodules.NewRSCode(1, 1)
	up := skymodules.FileUploadParams{
		Source:      "",
		SiaPath:     sp1,
		ErasureCode: rsc,
	}
	fileSize := uint64(100)
	f1, err := rt.newTestSiaFile(up.SiaPath, up.Source, up.ErasureCode, fileSize)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f1.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	sp2, err := subDir1_2.Join(hex.EncodeToString(fastrand.Bytes(8)))
	if err != nil {
		t.Fatal(err)
	}
	up.SiaPath = sp2
	f2, err := rt.newTestSiaFile(up.SiaPath, up.Source, up.ErasureCode, fileSize)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f2.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Call bubble on lowest lever and confirm top level reports accurate last
	// update time
	if err := rt.renter.UpdateMetadata(subDir1_2, false); err != nil {
		t.Fatal(err)
	}
	err = build.Retry(100, 100*time.Millisecond, func() error {
		dirInfo, err := rt.renter.staticFileSystem.DirInfo(skymodules.RootSiaPath())
		if err != nil {
			return err
		}
		if dirInfo.MostRecentModTime != f1.ModTime() {
			return fmt.Errorf("MostRecentModTime is incorrect, got %v expected %v", dirInfo.MostRecentModTime, f1.ModTime())
		}
		if dirInfo.AggregateMostRecentModTime != f2.ModTime() {
			return fmt.Errorf("AggregateMostRecentModTime is incorrect, got %v expected %v", dirInfo.AggregateMostRecentModTime, f2.ModTime())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRandomStuckDirectory probes managedStuckDirectory to make sure it
// randomly picks a correct directory
func TestRandomStuckDirectory(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create test renter
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create a test directory with sub folders
	//
	// root/home/siafiles/
	// root/home/siafiles/SubDir1/
	// root/home/siafiles/SubDir1/SubDir2/
	// root/home/siafiles/SubDir2/
	subDir1, err := skymodules.NewSiaPath("SubDir1")
	if err != nil {
		t.Fatal(err)
	}
	subDir2, err := skymodules.NewSiaPath("SubDir2")
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.renter.CreateDir(subDir1, skymodules.DefaultDirPerm); err != nil {
		t.Fatal(err)
	}
	if err := rt.renter.CreateDir(subDir2, skymodules.DefaultDirPerm); err != nil {
		t.Fatal(err)
	}
	subDir1_2, err := subDir1.Join(subDir2.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.renter.CreateDir(subDir1_2, skymodules.DefaultDirPerm); err != nil {
		t.Fatal(err)
	}

	// Add a file to siafiles and SubDir1/SubDir2 and mark the first chunk as
	// stuck in each file
	//
	// This will test the edge case of continuing to find stuck files when a
	// directory has no files only directories
	rsc, _ := skymodules.NewRSCode(1, 1)
	up := skymodules.FileUploadParams{
		Source:      "",
		SiaPath:     skymodules.RandomSiaPath(),
		ErasureCode: rsc,
	}
	f, err := rt.newTestSiaFile(up.SiaPath, up.Source, up.ErasureCode, 100)
	if err != nil {
		t.Fatal(err)
	}
	err = rt.renter.SetFileStuck(up.SiaPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.SetStuck(uint64(0), true); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	up.SiaPath, err = subDir1_2.Join(hex.EncodeToString(fastrand.Bytes(8)))
	if err != nil {
		t.Fatal(err)
	}
	f, err = rt.newTestSiaFile(up.SiaPath, up.Source, up.ErasureCode, 100)
	if err != nil {
		t.Fatal(err)
	}
	err = f.GrowNumChunks(2)
	if err != nil {
		t.Fatal(err)
	}
	err = rt.renter.SetFileStuck(up.SiaPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.SetStuck(uint64(0), true); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Bubble directory information so NumStuckChunks is updated, there should
	// be at least 3 stuck chunks because of the 3 we manually marked as stuck,
	// but the repair loop could have marked the rest as stuck so we just want
	// to ensure that the root directory reflects at least the 3 we marked as
	// stuck
	if err := rt.renter.UpdateMetadata(skymodules.RootSiaPath(), true); err != nil {
		t.Fatal(err)
	}
	err = build.Retry(100, 100*time.Millisecond, func() error {
		// Get Root Directory Metadata
		metadata, err := rt.renter.managedDirectoryMetadata(skymodules.RootSiaPath())
		if err != nil {
			return err
		}
		// Check Aggregate number of stuck chunks
		if metadata.AggregateNumStuckChunks < uint64(3) {
			return fmt.Errorf("Incorrect number of stuck chunks, got %v expected at least 3", metadata.AggregateNumStuckChunks)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Find a stuck directory randomly, it should never find root/SubDir1 or
	// root/SubDir2 and should find root/SubDir1/SubDir2 more than root
	var count1_2, countRoot, countSiaFiles int
	for i := 0; i < 100; i++ {
		dir, err := rt.renter.managedStuckDirectory()
		if err != nil {
			t.Fatal(err)
		}
		if dir.Equals(subDir1_2) {
			count1_2++
			continue
		}
		if dir.Equals(skymodules.RootSiaPath()) {
			countRoot++
			continue
		}
		if dir.Equals(skymodules.UserFolder) {
			countSiaFiles++
			continue
		}
		t.Fatal("Unstuck dir found", dir.String())
	}

	// Randomness is weighted so we should always find file 1 more often
	if countRoot > count1_2 {
		t.Log("Should have found root/SubDir1/SubDir2 more than root")
		t.Fatalf("Found root/SubDir1/SubDir2 %v times and root %v times", count1_2, countRoot)
	}
	// If we never find root/SubDir1/SubDir2 then that is a failure
	if count1_2 == 0 {
		t.Fatal("Found root/SubDir1/SubDir2 0 times")
	}
	// If we never find root that is not ideal, Log this error. If it happens
	// a lot then the weighted randomness should be improved
	if countRoot == 0 {
		t.Logf("Found root 0 times. Consider improving the weighted randomness")
	}
	t.Log("Found root/SubDir1/SubDir2", count1_2, "times and root", countRoot, "times")
}

// TestRandomStuckFile tests that the renter can randomly find stuck files
// weighted by the number of stuck chunks
func TestRandomStuckFile(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create Renter
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create 3 files at root
	//
	// File 1 will have all chunks stuck
	file1, err := rt.renter.newRenterTestFile()
	if err != nil {
		t.Fatal(err)
	}
	err = file1.GrowNumChunks(3)
	if err != nil {
		t.Fatal(err)
	}
	siaPath1 := rt.renter.staticFileSystem.FileSiaPath(file1)
	err = rt.renter.SetFileStuck(siaPath1, true)
	if err != nil {
		t.Fatal(err)
	}

	// File 2 will have only 1 chunk stuck
	file2, err := rt.renter.newRenterTestFile()
	if err != nil {
		t.Fatal(err)
	}
	siaPath2 := rt.renter.staticFileSystem.FileSiaPath(file2)
	err = file2.SetStuck(0, true)
	if err != nil {
		t.Fatal(err)
	}

	// File 3 will be unstuck
	file3, err := rt.renter.newRenterTestFile()
	if err != nil {
		t.Fatal(err)
	}
	siaPath3 := rt.renter.staticFileSystem.FileSiaPath(file3)

	// Since we disabled the health loop for this test, call it manually to
	// update the directory metadata
	if err := rt.renter.UpdateMetadata(skymodules.UserFolder, false); err != nil {
		t.Fatal(err)
	}
	i := 0
	err = build.Retry(100, 100*time.Millisecond, func() error {
		i++
		if i%10 == 0 {
			if err := rt.renter.UpdateMetadata(skymodules.RootSiaPath(), true); err != nil {
				t.Fatal(err)
			}
		}
		// Get Root Directory Metadata
		metadata, err := rt.renter.managedDirectoryMetadata(skymodules.RootSiaPath())
		if err != nil {
			return err
		}
		// Check Aggregate number of stuck chunks
		if metadata.AggregateNumStuckChunks == 0 {
			return errors.New("no stuck chunks found")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	checkFindRandomFile(t, rt.renter, skymodules.RootSiaPath(), siaPath1, siaPath2, siaPath3)

	// Create a directory
	dir, err := skymodules.NewSiaPath("Dir")
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.renter.CreateDir(dir, skymodules.DefaultDirPerm); err != nil {
		t.Fatal(err)
	}

	// Move siafiles to dir
	newSiaPath1, err := dir.Join(siaPath1.String())
	if err != nil {
		t.Fatal(err)
	}
	err = rt.renter.RenameFile(siaPath1, newSiaPath1)
	if err != nil {
		t.Fatal(err)
	}
	newSiaPath2, err := dir.Join(siaPath2.String())
	if err != nil {
		t.Fatal(err)
	}
	err = rt.renter.RenameFile(siaPath2, newSiaPath2)
	if err != nil {
		t.Fatal(err)
	}
	newSiaPath3, err := dir.Join(siaPath3.String())
	if err != nil {
		t.Fatal(err)
	}
	err = rt.renter.RenameFile(siaPath3, newSiaPath3)
	if err != nil {
		t.Fatal(err)
	}
	// Since we disabled the health loop for this test, call it manually to
	// update the directory metadata
	if err := rt.renter.UpdateMetadata(dir, false); err != nil {
		t.Fatal(err)
	}
	i = 0
	err = build.Retry(100, 100*time.Millisecond, func() error {
		i++
		if i%10 == 0 {
			if err := rt.renter.UpdateMetadata(dir, false); err != nil {
				t.Fatal(err)
			}
		}
		// Get Directory Metadata
		metadata, err := rt.renter.managedDirectoryMetadata(dir)
		if err != nil {
			return err
		}
		// Check Aggregate number of stuck chunks
		if metadata.AggregateNumStuckChunks == 0 {
			return errors.New("no stuck chunks found")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	checkFindRandomFile(t, rt.renter, dir, newSiaPath1, newSiaPath2, newSiaPath3)
}

// checkFindRandomFile is a helper function that checks the output from
// managedStuckFile in a loop
func checkFindRandomFile(t *testing.T, r *Renter, dir, siaPath1, siaPath2, siaPath3 skymodules.SiaPath) {
	// Find a stuck file randomly, it should never find file 3 and should find
	// file 1 more than file 2.
	var count1, count2 int
	for i := 0; i < 100; i++ {
		siaPath, err := r.managedStuckFile(dir)
		if err != nil {
			t.Fatal(err)
		}
		if siaPath.Equals(siaPath1) {
			count1++
		}
		if siaPath.Equals(siaPath2) {
			count2++
		}
		if siaPath.Equals(siaPath3) {
			t.Fatal("Unstuck file 3 found")
		}
	}

	// Randomness is weighted so we should always find file 1 more often
	if count2 > count1 {
		t.Log("Should have found file 1 more than file 2")
		t.Fatalf("Found file 1 %v times and file 2 %v times", count1, count2)
	}
	// If we never find file 1 then that is a failure
	if count1 == 0 {
		t.Fatal("Found file 1 0 times")
	}
	// If we never find file 2 that is not ideal, Log this error. If it happens
	// a lot then the weighted randomness should be improved
	if count2 == 0 {
		t.Logf("Found file 2 0 times. Consider improving the weighted randomness")
	}
	t.Log("Found file1", count1, "times and file2", count2, "times")
}

// TestCalculateFileMetadata checks that the values returned from
// managedCalculateFileMetadata make sense
func TestCalculateFileMetadata(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create renter
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create a file at root with a skylink
	rsc, _ := skymodules.NewRSCode(1, 1)
	siaPath, err := skymodules.NewSiaPath("rootFile")
	if err != nil {
		t.Fatal(err)
	}
	up := skymodules.FileUploadParams{
		Source:      "",
		SiaPath:     siaPath,
		ErasureCode: rsc,
	}
	fileSize := uint64(100)
	err = rt.renter.staticFileSystem.NewSiaFile(up.SiaPath, up.Source, up.ErasureCode, crypto.GenerateSiaKey(crypto.RandomCipherType()), fileSize, persist.DefaultDiskPermissionsTest)
	if err != nil {
		t.Fatal(err)
	}
	sf, err := rt.renter.staticFileSystem.OpenSiaFile(up.SiaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sf.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	var skylink skymodules.Skylink
	err = sf.AddSkylink(skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Grab initial metadata values
	rt.renter.managedUpdateRenterContractsAndUtilities()
	offline, goodForRenew, _, _ := rt.renter.callRenterContractsAndUtilities()
	health, stuckHealth, _, _, numStuckChunks, repairBytes, stuckBytes := sf.Health(offline, goodForRenew)
	redundancy, _, err := sf.Redundancy(offline, goodForRenew)
	if err != nil {
		t.Fatal(err)
	}
	lastHealthCheckTime := sf.LastHealthCheckTime()
	modTime := sf.ModTime()

	// Update the file metadata.
	err = rt.updateFileMetadatas(skymodules.RootSiaPath())
	if err != nil {
		t.Fatal(err)
	}

	// Check calculated metadata
	bubbledMetadatas, err := rt.renter.managedCachedFileMetadatas([]skymodules.SiaPath{up.SiaPath})
	if err != nil {
		t.Fatal(err)
	}
	fileMetadata := bubbledMetadatas[0].bm

	// Check siafile calculated metadata
	if fileMetadata.Health != health {
		t.Fatalf("health incorrect, expected %v got %v", health, fileMetadata.Health)
	}
	if fileMetadata.StuckHealth != stuckHealth {
		t.Fatalf("stuckHealth incorrect, expected %v got %v", stuckHealth, fileMetadata.StuckHealth)
	}
	if fileMetadata.Redundancy != redundancy {
		t.Fatalf("redundancy incorrect, expected %v got %v", redundancy, fileMetadata.Redundancy)
	}
	if fileMetadata.RepairBytes != repairBytes {
		t.Fatalf("RepairBytes incorrect, expected %v got %v", repairBytes, fileMetadata.RepairBytes)
	}
	if fileMetadata.StuckBytes != stuckBytes {
		t.Fatalf("StuckBytes incorrect, expected %v got %v", stuckBytes, fileMetadata.StuckBytes)
	}
	if fileMetadata.Size != fileSize {
		t.Fatalf("size incorrect, expected %v got %v", fileSize, fileMetadata.Size)
	}
	if fileMetadata.NumStuckChunks != numStuckChunks {
		t.Fatalf("numstuckchunks incorrect, expected %v got %v", numStuckChunks, fileMetadata.NumStuckChunks)
	}
	if fileMetadata.LastHealthCheckTime.Equal(lastHealthCheckTime) || fileMetadata.LastHealthCheckTime.IsZero() {
		t.Log("Initial lasthealthchecktime", lastHealthCheckTime)
		t.Log("Calculated lasthealthchecktime", fileMetadata.LastHealthCheckTime)
		t.Fatal("Expected lasthealthchecktime to have updated and be non zero")
	}
	if !fileMetadata.ModTime.Equal(modTime) {
		t.Fatalf("Unexpected modtime, expected %v got %v", modTime, fileMetadata.ModTime)
	}
	if fileMetadata.NumSkylinks != 1 {
		t.Fatalf("NumSkylinks incorrect, expected %v got %v", 1, fileMetadata.NumSkylinks)
	}
	if !fileMetadata.Unrecoverable {
		t.Fatal("expected file to be unrecoverable")
	}
}

// TestCreateMissingSiaDir confirms that the repair code creates a siadir file
// if one is not found
func TestCreateMissingSiaDir(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create test renter
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Confirm the siadir file is on disk
	siaDirPath := skymodules.RootSiaPath().SiaDirMetadataSysPath(rt.renter.staticFileSystem.Root())
	_, err = os.Stat(siaDirPath)
	if err != nil {
		t.Fatal(err)
	}

	// Remove .siadir file on disk
	err = os.Remove(siaDirPath)
	if err != nil {
		t.Fatal(err)
	}

	// Confirm siadir is gone
	_, err = os.Stat(siaDirPath)
	if !os.IsNotExist(err) {
		t.Fatal("Err should have been IsNotExist", err)
	}

	// Create siadir file with managedDirectoryMetadata
	_, err = rt.renter.managedDirectoryMetadata(skymodules.RootSiaPath())
	if err != nil {
		t.Fatal(err)
	}

	// Confirm it is on disk
	_, err = os.Stat(siaDirPath)
	if err != nil {
		t.Fatal(err)
	}
}

// TestAddStuckChunksToHeap probes the managedAddStuckChunksToHeap method
func TestAddStuckChunksToHeap(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// create renter with dependencies, first to disable the background health,
	// repair, and stuck loops from running, then update it to bypass the worker
	// pool length check in managedBuildUnfinishedChunks
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// create file with no stuck chunks
	rsc, _ := skymodules.NewRSCode(1, 1)
	up := skymodules.FileUploadParams{
		Source:      "",
		SiaPath:     skymodules.RandomSiaPath(),
		ErasureCode: rsc,
	}
	f, err := rt.newTestSiaFile(up.SiaPath, up.Source, up.ErasureCode, 100)
	if err != nil {
		t.Fatal(err)
	}

	// Create maps for method inputs
	hosts := make(map[string]struct{})
	offline := make(map[string]bool)
	goodForRenew := make(map[string]bool)

	// Manually add workers to worker pool
	rt.renter.staticWorkerPool.mu.Lock()
	for i := 0; i < int(f.NumChunks()); i++ {
		rt.renter.staticWorkerPool.workers[fmt.Sprint(i)] = &worker{
			wakeChan: make(chan struct{}, 1),
		}
	}
	rt.renter.staticWorkerPool.mu.Unlock()

	// call managedAddStuckChunksToHeap, no chunks should be added
	err = rt.renter.managedAddStuckChunksToHeap(up.SiaPath, hosts, offline, goodForRenew)
	if !errors.Contains(err, errNoStuckChunks) {
		t.Fatal(err)
	}
	if rt.renter.staticUploadHeap.managedLen() != 0 {
		t.Fatal("Expected uploadHeap to be of length 0 got", rt.renter.staticUploadHeap.managedLen())
	}

	// make chunk stuck
	if err = f.SetStuck(uint64(0), true); err != nil {
		t.Fatal(err)
	}

	// call managedAddStuckChunksToHeap, chunk should be added to heap
	err = rt.renter.managedAddStuckChunksToHeap(up.SiaPath, hosts, offline, goodForRenew)
	if err != nil {
		t.Fatal(err)
	}
	if rt.renter.staticUploadHeap.managedLen() != 1 {
		t.Fatal("Expected uploadHeap to be of length 1 got", rt.renter.staticUploadHeap.managedLen())
	}

	// Pop chunk, chunk should be marked as fileRecentlySuccessful true
	chunk := rt.renter.staticUploadHeap.managedPop()
	if !chunk.fileRecentlySuccessful {
		t.Fatal("chunk not marked as fileRecentlySuccessful true")
	}
}

// TestRandomStuckFileRegression tests an edge case where no siapath was being
// returned from managedStuckFile.
func TestRandomStuckFileRegression(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create Renter
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create 1 file at root with all chunks stuck
	file, err := rt.renter.newRenterTestFile()
	if err != nil {
		t.Fatal(err)
	}
	siaPath := rt.renter.staticFileSystem.FileSiaPath(file)
	err = rt.renter.SetFileStuck(siaPath, true)
	if err != nil {
		t.Fatal(err)
	}

	// Set the root directories metadata to have a large number of aggregate
	// stuck chunks. Since there is only 1 stuck chunk this was causing the
	// likelihood of the stuck file being chosen to be very low.
	rootDir, err := rt.renter.staticFileSystem.OpenSiaDir(skymodules.RootSiaPath())
	if err != nil {
		t.Fatal(err)
	}
	md, err := rootDir.Metadata()
	if err != nil {
		t.Fatal(err)
	}
	md.AggregateNumStuckChunks = 50000
	md.NumStuckChunks = 1
	md.NumFiles = 1
	err = rootDir.UpdateMetadata(md)
	if err != nil {
		t.Fatal(err)
	}

	stuckSiaPath, err := rt.renter.managedStuckFile(skymodules.RootSiaPath())
	if err != nil {
		t.Fatal(err)
	}
	if !stuckSiaPath.Equals(siaPath) {
		t.Fatalf("Stuck siapath should have been the one file in the directory, expected %v got %v", siaPath, stuckSiaPath)
	}
}
