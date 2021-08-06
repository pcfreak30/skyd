package renter

// healthloop_test.go contains unit tests for the health loop code.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/SkynetLabs/skyd/build"
	"go.sia.tech/siad/persist"
)

// TestSystemScanDurationEstimator checks that the logic for computing the
// estimated system scan duration is working correctly.
func TestSystemScanDurationEstimator(t *testing.T) {
	// Base case, check what happens when computing an estimate on an empty dir
	// finder.
	r := new(Renter)
	dirFinder := r.newHealthLoopDirFinder()
	dirFinder.totalFiles = 100
	dirFinder.updateEstimatedSystemScanDuration()
	if dirFinder.estimatedSystemScanDuration != 0 {
		t.Error("bad")
	}

	// Set some window variables, demonstrate processing at about 1 file per
	// second.
	dirFinder.windowFilesProcessed = 10
	dirFinder.windowStartTime = time.Now().Add(-10 * time.Second)
	dirFinder.windowSleepTime = 0
	dirFinder.updateEstimatedSystemScanDuration()
	if dirFinder.estimatedSystemScanDuration > time.Second*101 || dirFinder.estimatedSystemScanDuration < time.Second*99 {
		t.Error("bad", dirFinder.estimatedSystemScanDuration)
	}
	// Try again with the same values, the EMA should not be off target if we
	// are moving at the same speed.
	dirFinder.windowFilesProcessed = 10
	dirFinder.windowStartTime = time.Now().Add(-10 * time.Second)
	dirFinder.windowSleepTime = 0
	dirFinder.updateEstimatedSystemScanDuration()
	if dirFinder.estimatedSystemScanDuration > time.Second*101 || dirFinder.estimatedSystemScanDuration < time.Second*99 {
		t.Error("bad", dirFinder.estimatedSystemScanDuration)
	}
	// Try again, but this time with an average sleep of 1 second per file. So
	// the processing is going at 1 second per file, and the sleep is going at 1
	// second per file, meaning that the EMA should still result in the exact
	// same value.
	dirFinder.windowFilesProcessed = 10
	dirFinder.windowStartTime = time.Now().Add(-20 * time.Second)
	dirFinder.windowSleepTime = 10 * time.Second
	dirFinder.updateEstimatedSystemScanDuration()
	if dirFinder.estimatedSystemScanDuration > time.Second*101 || dirFinder.estimatedSystemScanDuration < time.Second*99 {
		t.Error("bad", dirFinder.estimatedSystemScanDuration)
	}
	// Try again, but this time double the total number of files, this should
	// cause the total estimate to increase.
	dirFinder.totalFiles = 200
	dirFinder.windowFilesProcessed = 10
	dirFinder.windowStartTime = time.Now().Add(-20 * time.Second)
	dirFinder.windowSleepTime = 10 * time.Second
	dirFinder.updateEstimatedSystemScanDuration()
	if dirFinder.estimatedSystemScanDuration > time.Second*135 || dirFinder.estimatedSystemScanDuration < time.Second*125 {
		t.Error("bad", dirFinder.estimatedSystemScanDuration)
	}
	// Try again, but this time we are going faster per file, this should
	// improve the estimated total time by a bit.
	dirFinder.windowFilesProcessed = 20
	dirFinder.windowStartTime = time.Now().Add(-20 * time.Second)
	dirFinder.windowSleepTime = 10 * time.Second
	dirFinder.updateEstimatedSystemScanDuration()
	if dirFinder.estimatedSystemScanDuration > time.Second*125 || dirFinder.estimatedSystemScanDuration < time.Second*115 {
		t.Error("bad", dirFinder.estimatedSystemScanDuration)
	}
	// Update a few times in a loop, the result should converge closely to
	// double the original speed.
	for i := 0; i < 100; i++ {
		dirFinder.windowFilesProcessed = 20
		dirFinder.windowStartTime = time.Now().Add(-20 * time.Second)
		dirFinder.windowSleepTime = 10 * time.Second
		dirFinder.updateEstimatedSystemScanDuration()
	}
	if dirFinder.estimatedSystemScanDuration > time.Second*105 || dirFinder.estimatedSystemScanDuration < time.Second*95 {
		t.Error("bad", dirFinder.estimatedSystemScanDuration)
	}
	// Introduce going a lot slower, the result should slow the estimated time.
	dirFinder.windowFilesProcessed = 5
	dirFinder.windowStartTime = time.Now().Add(-20 * time.Second)
	dirFinder.windowSleepTime = 10 * time.Second
	dirFinder.updateEstimatedSystemScanDuration()
	if dirFinder.estimatedSystemScanDuration > time.Second*135 || dirFinder.estimatedSystemScanDuration < time.Second*125 {
		t.Error("bad", dirFinder.estimatedSystemScanDuration)
	}
	// Update a few times in a loop, the result should converge closely to half
	// the original speed.
	for i := 0; i < 100; i++ {
		dirFinder.windowFilesProcessed = 5
		dirFinder.windowStartTime = time.Now().Add(-20 * time.Second)
		dirFinder.windowSleepTime = 10 * time.Second
		dirFinder.updateEstimatedSystemScanDuration()
	}
	if dirFinder.estimatedSystemScanDuration > time.Second*410 || dirFinder.estimatedSystemScanDuration < time.Second*395 {
		t.Error("bad", dirFinder.estimatedSystemScanDuration)
	}
}

// TestDirFinderSleepDuration tests the logic that determines how long the dir
// finder should be asleep.
func TestDirFinderSleepDuration(t *testing.T) {
	// Need to skip on the short testing because in order for this to work we
	// need to add a logger to the renter owned by the dirFinder.
	if testing.Short() {
		t.SkipNow()
	}

	testdir := build.TempDir("renter", "TestDirFinderSleepDuration")
	err := os.MkdirAll(testdir, 0700)
	if err != nil {
		t.Fatal(err)
	}
	dirFinder := new(healthLoopDirFinder)
	dirFinder.renter = new(Renter)
	dirFinder.renter.staticLog, err = persist.NewFileLogger(filepath.Join(testdir, logFile))
	if err != nil {
		t.Fatal(err)
	}

	// First check, the sleep duration should be the empty filesystem sleep
	// duration if there are no files in the filesystem.
	sleepDuration := dirFinder.sleepDurationBeforeNextDir()
	if sleepDuration != emptyFilesystemSleepDuration {
		t.Error("bad")
	}

	// Standard check - set the number of files to 3, which means there should
	// be a full second of sleep between each file.
	dirFinder.totalFiles = 3
	dirFinder.filesInNextDir = 1
	dirFinder.leastRecentCheck = time.Now().Add(-1 * TargetHealthCheckFrequency / 2)
	sleepDuration = dirFinder.sleepDurationBeforeNextDir()
	baseExpectedTime := TargetHealthCheckFrequency / 3
	if sleepDuration < baseExpectedTime-(time.Millisecond*5) || sleepDuration > baseExpectedTime+(time.Millisecond*5) {
		t.Error("bad", sleepDuration)
	}
	dirFinder.filesInNextDir = 2
	sleepDuration = dirFinder.sleepDurationBeforeNextDir()
	if sleepDuration < 2*baseExpectedTime-(time.Millisecond*5) || sleepDuration > 2*baseExpectedTime+(time.Millisecond*5) {
		t.Error("bad", sleepDuration)
	}
	dirFinder.filesInNextDir = 3
	sleepDuration = dirFinder.sleepDurationBeforeNextDir()
	if sleepDuration < 3*baseExpectedTime-(time.Millisecond*5) || sleepDuration > 3*baseExpectedTime+(time.Millisecond*5) {
		t.Error("bad", sleepDuration)
	}

	// Check that the compression is working as desired.
	dirFinder.filesInNextDir = 2
	halfwayToUrgent := TargetHealthCheckFrequency + (urgentHealthCheckFrequency-TargetHealthCheckFrequency)/2
	dirFinder.leastRecentCheck = time.Now().Add(-1 * halfwayToUrgent)
	sleepDuration = dirFinder.sleepDurationBeforeNextDir()
	if sleepDuration < baseExpectedTime-(time.Millisecond*5) || sleepDuration > baseExpectedTime+(time.Millisecond*5) {
		t.Error("bad", sleepDuration)
	}

	// Check that a manual check being active results in no sleep.
	dirFinder.manualCheckTime = time.Now().Add(baseExpectedTime)
	sleepDuration = dirFinder.sleepDurationBeforeNextDir()
	if sleepDuration != 0 {
		t.Error("bad", sleepDuration)
	}
	dirFinder.manualCheckTime = time.Now().Add(-1 * time.Minute)

	// Check that a slow scan time results in no sleep.
	dirFinder.estimatedSystemScanDuration = urgentHealthCheckFrequency
	sleepDuration = dirFinder.sleepDurationBeforeNextDir()
	if sleepDuration != 0 {
		t.Error("bad", sleepDuration)
	}
	dirFinder.estimatedSystemScanDuration = 0

	// Check that being far behind results in no sleep.
	dirFinder.leastRecentCheck = time.Now().Add(-1 * 2 * urgentHealthCheckFrequency)
	sleepDuration = dirFinder.sleepDurationBeforeNextDir()
	if sleepDuration != 0 {
		t.Error("bad", sleepDuration)
	}
	dirFinder.leastRecentCheck = time.Now()
}

/* TODO: bring back in some form
// TestOldestHealthCheckTime probes managedOldestHealthCheckTime to verify that
// the directory with the oldest LastHealthCheckTime is returned
func TestOldestHealthCheckTime(t *testing.T) {
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
	// root/ 1
	// root/SubDir1/
	// root/SubDir1/SubDir1/
	// root/SubDir1/SubDir2/
	// root/SubDir2/
	// root/SubDir3/
	directories := []string{
		"root",
		"root/SubDir1",
		"root/SubDir1/SubDir1",
		"root/SubDir1/SubDir2",
		"root/SubDir2",
		"root/SubDir3",
	}

	// Create directory tree with consistent metadata
	now := time.Now()
	nowMD := siadir.Metadata{
		Health:                       1,
		StuckHealth:                  0,
		AggregateLastHealthCheckTime: now,
		LastHealthCheckTime:          now,
	}
	for _, dir := range directories {
		// Create Directory
		dirSiaPath := newSiaPath(dir)
		if err := rt.renter.CreateDir(dirSiaPath, skymodules.DefaultDirPerm); err != nil {
			t.Fatal(err)
		}
		err = rt.openAndUpdateDir(dirSiaPath, nowMD)
		if err != nil {
			t.Fatal(err)
		}
		// Put two files in the directory
		for i := 0; i < 2; i++ {
			fileSiaPath, err := dirSiaPath.Join(persist.RandomSuffix())
			if err != nil {
				t.Fatal(err)
			}
			sf, err := rt.renter.createRenterTestFile(fileSiaPath)
			if err != nil {
				t.Fatal(err)
			}
			sf.SetLastHealthCheckTime()
			err = errors.Compose(sf.SaveMetadata(), sf.Close())
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	// Update all common directories to same health check time.
	err1 := rt.openAndUpdateDir(skymodules.RootSiaPath(), nowMD)
	err2 := rt.openAndUpdateDir(skymodules.BackupFolder, nowMD)
	err3 := rt.openAndUpdateDir(skymodules.HomeFolder, nowMD)
	err4 := rt.openAndUpdateDir(skymodules.SkynetFolder, nowMD)
	err5 := rt.openAndUpdateDir(skymodules.UserFolder, nowMD)
	err6 := rt.openAndUpdateDir(skymodules.VarFolder, nowMD)
	err = errors.Compose(err1, err2, err3, err4, err5, err6)
	if err != nil {
		t.Fatal(err)
	}

	// Set the LastHealthCheckTime of SubDir1/SubDir2 to be the oldest
	oldestCheckTime := now.AddDate(0, 0, -1)
	oldestHealthCheckUpdate := siadir.Metadata{
		Health:                       1,
		StuckHealth:                  0,
		AggregateLastHealthCheckTime: oldestCheckTime,
		LastHealthCheckTime:          oldestCheckTime,
	}
	subDir1_2 := newSiaPath("root/SubDir1/SubDir2")
	if err := rt.openAndUpdateDir(subDir1_2, oldestHealthCheckUpdate); err != nil {
		t.Fatal(err)
	}

	// Bubble the health of SubDir1 so that the oldest LastHealthCheckTime of
	// SubDir1/SubDir2 gets bubbled up
	subDir1 := newSiaPath("root/SubDir1")
	if err := rt.bubbleBlocking(subDir1); err != nil {
		t.Fatal(err)
	}
	err = build.Retry(60, time.Second, func() error {
		// Find the oldest directory, even though SubDir1/SubDir2 is the oldest,
		// SubDir1 should be returned since it is the lowest level directory tree
		// containing the Oldest LastHealthCheckTime
		dir, lastCheck, err := rt.renter.managedOldestHealthCheckTime()
		if err != nil {
			return err
		}
		if !dir.Equals(subDir1) {
			return fmt.Errorf("Expected to find %v but found %v", subDir1.String(), dir.String())
		}
		if !lastCheck.Equal(oldestCheckTime) {
			return fmt.Errorf("Expected to find time of %v but found %v", oldestCheckTime, lastCheck)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Now add more files to SubDir1, this will force the return of SubDir1_2
	// since the subtree including SubDir1 now has too many files
	for i := uint64(0); i < healthLoopNumBatchFiles; i++ {
		fileSiaPath, err := subDir1.Join(persist.RandomSuffix())
		if err != nil {
			t.Fatal(err)
		}
		sf, err := rt.renter.createRenterTestFile(fileSiaPath)
		if err != nil {
			t.Fatal(err)
		}
		sf.SetLastHealthCheckTime()
		err = errors.Compose(sf.SaveMetadata(), sf.Close())
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := rt.bubbleBlocking(subDir1); err != nil {
		t.Fatal(err)
	}
	err = build.Retry(60, time.Second, func() error {
		// Find the oldest directory, should be SubDir1_2 now
		dir, lastCheck, err := rt.renter.managedOldestHealthCheckTime()
		if err != nil {
			return err
		}
		if !dir.Equals(subDir1_2) {
			return fmt.Errorf("Expected to find %v but found %v", subDir1_2.String(), dir.String())
		}
		if !lastCheck.Equal(oldestCheckTime) {
			return fmt.Errorf("Expected to find time of %v but found %v", oldestCheckTime, lastCheck)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Now update the root directory to have an older AggregateLastHealthCheckTime
	// than the sub directory but a more recent LastHealthCheckTime. This will
	// simulate a shutdown before all the pending bubbles could finish.
	entry, err := rt.renter.staticFileSystem.OpenSiaDir(skymodules.RootSiaPath())
	if err != nil {
		t.Fatal(err)
	}
	rootTime := now.AddDate(0, 0, -3)
	err = entry.UpdateLastHealthCheckTime(rootTime, now)
	if err != nil {
		t.Fatal(err)
	}
	err = entry.Close()
	if err != nil {
		t.Fatal(err)
	}

	// A call to managedOldestHealthCheckTime should still return the same
	// subDir1_2
	dir, lastCheck, err := rt.renter.managedOldestHealthCheckTime()
	if err != nil {
		t.Fatal(err)
	}
	if !dir.Equals(subDir1_2) {
		t.Error(fmt.Errorf("Expected to find %v but found %v", subDir1_2.String(), dir.String()))
	}
	if !lastCheck.Equal(oldestCheckTime) {
		t.Error(fmt.Errorf("Expected to find time of %v but found %v", oldestCheckTime, lastCheck))
	}
}
*/
