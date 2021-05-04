package renter

// healthloop_test.go contains unit tests for the health loop code.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/SkynetLabs/skyd/build"
)

// TestSystemScanDurationEstimator checks that the logic for computing the
// estimated system scan duration is working correctly.
func TestSystemScanDurationEstimator(t *testing.T) {
	// Base case, check what happens when computing an estimate on an empty dir
	// finder.
	dirFinder := &healthLoopDirFinder{
		totalFiles: 100,
	}
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
	// double the total estimate.
	dirFinder.totalFiles = 200
	dirFinder.windowFilesProcessed = 10
	dirFinder.windowStartTime = time.Now().Add(-20 * time.Second)
	dirFinder.windowSleepTime = 10 * time.Second
	dirFinder.updateEstimatedSystemScanDuration()
	if dirFinder.estimatedSystemScanDuration > time.Second*205 || dirFinder.estimatedSystemScanDuration < time.Second*195 {
		t.Error("bad", dirFinder.estimatedSystemScanDuration)
	}
	// Try again, but this time we are going faster per file, this should
	// improve the estimated total time by a bit.
	dirFinder.windowFilesProcessed = 20
	dirFinder.windowStartTime = time.Now().Add(-20 * time.Second)
	dirFinder.windowSleepTime = 10 * time.Second
	dirFinder.updateEstimatedSystemScanDuration()
	if dirFinder.estimatedSystemScanDuration > time.Second*165 || dirFinder.estimatedSystemScanDuration < time.Second*155 {
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
	if dirFinder.estimatedSystemScanDuration > time.Second*115 || dirFinder.estimatedSystemScanDuration < time.Second*100 {
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
	// be a full second of sleep betwen each file.
	dirFinder.totalFiles = 3
	dirFinder.filesInNextDir = 1
	dirFinder.leastRecentCheck = time.Now().Add(-1 * TargetHealthCheckFrequency / 2)
	sleepDuration = dirFinder.sleepDurationBeforeNextDir()
	if sleepDuration < time.Second-(time.Millisecond*5) || sleepDuration > time.Second+(time.Millisecond*5) {
		t.Error("bad", sleepDuration)
	}
	dirFinder.filesInNextDir = 2
	sleepDuration = dirFinder.sleepDurationBeforeNextDir()
	if sleepDuration < 2*time.Second-(time.Millisecond*5) || sleepDuration > 2*time.Second+(time.Millisecond*5) {
		t.Error("bad", sleepDuration)
	}
	dirFinder.filesInNextDir = 3
	sleepDuration = dirFinder.sleepDurationBeforeNextDir()
	if sleepDuration < 3*time.Second-(time.Millisecond*5) || sleepDuration > 3*time.Second+(time.Millisecond*5) {
		t.Error("bad", sleepDuration)
	}

	// Check that the compression is working as desired.
	dirFinder.filesInNextDir = 2
	halfwayToUrgent := TargetHealthCheckFrequency + (urgentHealthCheckFrequency-TargetHealthCheckFrequency)/2
	dirFinder.leastRecentCheck = time.Now().Add(-1 * halfwayToUrgent)
	sleepDuration = dirFinder.sleepDurationBeforeNextDir()
	if sleepDuration < time.Second-(time.Millisecond*5) || sleepDuration > time.Second+(time.Millisecond*5) {
		t.Error("bad", sleepDuration)
	}

	// Check that a manual check being active results in no sleep.
	dirFinder.manualCheckTime = time.Now().Add(time.Second)
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
