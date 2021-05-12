package renter

import (
	"io/ioutil"
	"testing"

	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/persist"
)

// TestDirUpdateBatcherQueue verifies the callQueueDirUpdate method of the
// dirUpdateBatcher.
func TestDirUpdateBatcherQueue(t *testing.T) {
	// Check that the maps of the dub are being allocated correctly.
	dub := new(dirUpdateBatcher)
	dub.nextBatch = new(dirUpdateBatch)
	dub.nextBatch.completeChan = make(chan struct{})
	priorChan := make(chan struct{})
	close(priorChan)
	dub.nextBatch.priorCompleteChan = priorChan
	dub.nextBatch.dirUpdateBatchDeps.renter = new(Renter)
	logger, err := persist.NewLogger(ioutil.Discard)
	dub.nextBatch.dirUpdateBatchDeps.renter.staticLog = logger
	dub.nextBatch.dirUpdateBatchDeps.executeTracker.executedDirs = make(map[skymodules.SiaPath]struct{})
	dub.nextBatch.dirUpdateBatchDeps.executeTracker.lastDepth = 10e3
	depthFive, err := skymodules.NewSiaPath("one/two/three/four/five")
	if err != nil {
		t.Fatal(err)
	}
	dub.callQueueDirUpdate(depthFive)
	if len(dub.nextBatch.batchSet) != 6 {
		t.Error("bad")
	}
	for i := 0; i < 5; i++ {
		if len(dub.nextBatch.batchSet[i]) != 0 {
			t.Error(len(dub.nextBatch.batchSet[i]))
		}
	}
	if len(dub.nextBatch.batchSet[5]) != 1 {
		t.Error(len(dub.nextBatch.batchSet[5]))
	}
	// Try adding the same directory again, this should be a no-op.
	dub.callQueueDirUpdate(depthFive)
	if len(dub.nextBatch.batchSet) != 6 {
		t.Error("bad")
	}
	for i := 0; i < 5; i++ {
		if len(dub.nextBatch.batchSet[i]) != 0 {
			t.Error(len(dub.nextBatch.batchSet[i]))
		}
	}
	if len(dub.nextBatch.batchSet[5]) != 1 {
		t.Error(len(dub.nextBatch.batchSet[5]))
	}

	// Try adding a directory at the same level.
	depthFive2, err := skymodules.NewSiaPath("one/two/three/four/fiveeee")
	if err != nil {
		t.Fatal(err)
	}
	dub.callQueueDirUpdate(depthFive2)
	if len(dub.nextBatch.batchSet) != 6 {
		t.Error("bad")
	}
	for i := 0; i < 5; i++ {
		if len(dub.nextBatch.batchSet[i]) != 0 {
			t.Error(len(dub.nextBatch.batchSet[i]))
		}
	}
	if len(dub.nextBatch.batchSet[5]) != 2 {
		t.Error(len(dub.nextBatch.batchSet[5]))
	}
	// Try adding the same directory again, this should be a no-op.
	dub.callQueueDirUpdate(depthFive2)
	if len(dub.nextBatch.batchSet) != 6 {
		t.Error("bad")
	}
	for i := 0; i < 5; i++ {
		if len(dub.nextBatch.batchSet[i]) != 0 {
			t.Error(len(dub.nextBatch.batchSet[i]))
		}
	}
	if len(dub.nextBatch.batchSet[5]) != 2 {
		t.Error(len(dub.nextBatch.batchSet[5]))
	}

	// Try adding a directory at a deeper level
	depthSix, err := skymodules.NewSiaPath("one/two/three/four/five/six")
	if err != nil {
		t.Fatal(err)
	}
	dub.callQueueDirUpdate(depthSix)
	if len(dub.nextBatch.batchSet) != 7 {
		t.Error("bad")
	}
	for i := 0; i < 5; i++ {
		if len(dub.nextBatch.batchSet[i]) != 0 {
			t.Error(len(dub.nextBatch.batchSet[i]))
		}
	}
	if len(dub.nextBatch.batchSet[5]) != 2 {
		t.Error(len(dub.nextBatch.batchSet[5]))
	}
	if len(dub.nextBatch.batchSet[6]) != 1 {
		t.Error(len(dub.nextBatch.batchSet[6]))
	}

	// Try adding a directory at a less deep level.
	depthFour, err := skymodules.NewSiaPath("one/two/three/four")
	if err != nil {
		t.Fatal(err)
	}
	dub.callQueueDirUpdate(depthFour)
	if len(dub.nextBatch.batchSet) != 7 {
		t.Error("bad")
	}
	for i := 0; i < 4; i++ {
		if len(dub.nextBatch.batchSet[i]) != 0 {
			t.Error(len(dub.nextBatch.batchSet[i]))
		}
	}
	if len(dub.nextBatch.batchSet[4]) != 1 {
		t.Error(len(dub.nextBatch.batchSet[5]))
	}
	if len(dub.nextBatch.batchSet[5]) != 2 {
		t.Error(len(dub.nextBatch.batchSet[5]))
	}
	if len(dub.nextBatch.batchSet[6]) != 1 {
		t.Error(len(dub.nextBatch.batchSet[6]))
	}

	// Try adding a directory at disjoint level.
	disjoint, err := skymodules.NewSiaPath("oneeee/two/three/four/five")
	if err != nil {
		t.Fatal(err)
	}
	dub.callQueueDirUpdate(disjoint)
	if len(dub.nextBatch.batchSet) != 7 {
		t.Error("bad")
	}
	for i := 0; i < 4; i++ {
		if len(dub.nextBatch.batchSet[i]) != 0 {
			t.Error(len(dub.nextBatch.batchSet[i]))
		}
	}
	if len(dub.nextBatch.batchSet[4]) != 1 {
		t.Error(len(dub.nextBatch.batchSet[5]))
	}
	if len(dub.nextBatch.batchSet[5]) != 3 {
		t.Error(len(dub.nextBatch.batchSet[5]))
	}
	if len(dub.nextBatch.batchSet[6]) != 1 {
		t.Error(len(dub.nextBatch.batchSet[6]))
	}

	// Set up the dependency injection to track calls to execute, then call
	// execute and verify that everything we wanted got called.
	dub.nextBatch.dirUpdateBatchDeps.testSwitch = "testExecute"
	dub.nextBatch.managedExecute()
	executedDirs := dub.nextBatch.dirUpdateBatchDeps.executeTracker.executedDirs
	if len(executedDirs) != 13 {
		t.Error("wrong number of directories executed")
	}
	// Check for root.
	rootSP := skymodules.RootSiaPath()
	_, exists := executedDirs[rootSP]
	if !exists {
		t.Error("root directory not scanned")
	}
	// Check for the other 12 dirs.
	expectedDirs := []string{
		"one/two/three/four/five/six",
		"one/two/three/four/five",
		"one/two/three/four/fiveeee",
		"oneeee/two/three/four/five",
		"one/two/three/four",
		"oneeee/two/three/four",
		"one/two/three",
		"oneeee/two/three",
		"one/two",
		"oneeee/two",
		"one",
		"oneeee",
	}
	for _, dirStr := range expectedDirs {
		sp, err := skymodules.NewSiaPath(dirStr)
		if err != nil {
			t.Fatal(err)
		}
		_, exists := executedDirs[sp]
		if !exists {
			t.Error(sp)
		}
	}
}
