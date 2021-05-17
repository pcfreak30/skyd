package renter

// dirupdatebatcher_helpers.go contains the logic for injected dependencies that
// make the dirupdatebatcher easier to unit test.

import (
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

// dirUpdateBatchDeps contains some dependency injections for testing.
type dirUpdateBatchDeps struct {
	// testSwitch can be changed to various strings to modify the logic of
	// certain functions for testing purposes.
	testSwitch string

	// Various structures used in testing.
	executeTracker dirUpdateExecuteTracker

	// Used in production.
	renter *Renter
}

// dirUpdateExecuteTracker tracks which directories have been updated in a call
// to execute.
type dirUpdateExecuteTracker struct {
	lastDepth    int
	executedDirs map[skymodules.SiaPath]struct{}
}

// managedUpdateDirMetadata passes through to the renter's version of
// managedUpdateDirMetadata unless we have mocked it out.
func (dubd dirUpdateBatchDeps) managedUpdateDirMetadata(dirPath skymodules.SiaPath) error {
	// Test switch set to "testExecute", run the variation that is helpful for
	// testing the execution.
	if dubd.testSwitch == "testExecute" {
		dubd.executeTracker.Track(dirPath)
		return nil
	}

	// Unrecognized test switch, do a normal passthrough to the renter.
	return dubd.renter.managedUpdateDirMetadata(dirPath)
}

// Track will add a directory to the tracker which knows which directories have
// been updated by a call to execute. It will panic if a directory is called
// twice, as the purpose of the batcher is to avoid redundant calls.
func (duet dirUpdateExecuteTracker) Track(dirPath skymodules.SiaPath) {
	// Make sure that each directory is only called once.
	_, exists := duet.executedDirs[dirPath]
	if exists {
		panic("double call to a directory")
	}

	// Make sure that deeper directories are always called before shallower
	// directories.
	dirDepth := dirPath.Depth()
	if duet.lastDepth < dirDepth {
		panic("directories are being updated out of order")
	}
	duet.lastDepth = dirDepth
	duet.executedDirs[dirPath] = struct{}{}
}
