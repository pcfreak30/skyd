package renter

// healthloop_test.go contains unit tests for the health loop code.

import (
	"testing"
	"time"
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
	if dirFinder.estimatedSystemScanDuration > time.Second * 101 || dirFinder.estimatedSystemScanDuration < time.Second * 99 {
		t.Error("bad", dirFinder.estimatedSystemScanDuration)
	}
	// Try again with the same values, the EMA should not be off target if we
	// are moving at the same speed.
	dirFinder.windowFilesProcessed = 10
	dirFinder.windowStartTime = time.Now().Add(-10 * time.Second)
	dirFinder.windowSleepTime = 0
	dirFinder.updateEstimatedSystemScanDuration()
	if dirFinder.estimatedSystemScanDuration > time.Second * 101 || dirFinder.estimatedSystemScanDuration < time.Second * 99 {
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
	if dirFinder.estimatedSystemScanDuration > time.Second * 101 || dirFinder.estimatedSystemScanDuration < time.Second * 99 {
		t.Error("bad", dirFinder.estimatedSystemScanDuration)
	}
	// Try again, but this time double the total number of files, this should
	// double the total estimate.
	dirFinder.totalFiles = 200
	dirFinder.windowFilesProcessed = 10
	dirFinder.windowStartTime = time.Now().Add(-20 * time.Second)
	dirFinder.windowSleepTime = 10 * time.Second
	dirFinder.updateEstimatedSystemScanDuration()
	if dirFinder.estimatedSystemScanDuration > time.Second * 205 || dirFinder.estimatedSystemScanDuration < time.Second * 195 {
		t.Error("bad", dirFinder.estimatedSystemScanDuration)
	}
	// Try again, but this time we are going faster per file, this should
	// improve the estimated total time by a bit.
	dirFinder.windowFilesProcessed = 20
	dirFinder.windowStartTime = time.Now().Add(-20 * time.Second)
	dirFinder.windowSleepTime = 10 * time.Second
	dirFinder.updateEstimatedSystemScanDuration()
	if dirFinder.estimatedSystemScanDuration > time.Second * 165 || dirFinder.estimatedSystemScanDuration < time.Second * 155 {
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
	if dirFinder.estimatedSystemScanDuration > time.Second * 105 || dirFinder.estimatedSystemScanDuration < time.Second * 95 {
		t.Error("bad", dirFinder.estimatedSystemScanDuration)
	}
	// Introduce going a lot slower, the result should slow the estimated time.
	dirFinder.windowFilesProcessed = 5
	dirFinder.windowStartTime = time.Now().Add(-20 * time.Second)
	dirFinder.windowSleepTime = 10 * time.Second
	dirFinder.updateEstimatedSystemScanDuration()
	if dirFinder.estimatedSystemScanDuration > time.Second * 115 || dirFinder.estimatedSystemScanDuration < time.Second * 100 {
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
	if dirFinder.estimatedSystemScanDuration > time.Second * 410 || dirFinder.estimatedSystemScanDuration < time.Second * 395 {
		t.Error("bad", dirFinder.estimatedSystemScanDuration)
	}
}
