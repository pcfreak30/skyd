package renter

import (
	"fmt"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/skynetlabs/skyd/skymodules"
	"gitlab.com/skynetlabs/skyd/skymodules/renter/filesystem/siadir"
)

var (
	// bubbleWaitInTestTime is the amount of time a test should wait before
	// returning an error in test. This is a conservative value to prevent test
	// timeouts.
	bubbleWaitInTestTime = time.Minute
)

// bubble is a helper for the renterTester to call bubble on a directory and
// block until the bubble has executed.
func (rt *renterTester) bubble(siaPath skymodules.SiaPath) error {
	complete := rt.renter.staticBubbleScheduler.callQueueBubble(siaPath)
	select {
	case <-complete:
	case <-time.After(bubbleWaitInTestTime):
		return errors.New("test blocked too long for bubble")
	}
	return nil
}

// bubbleAll is a helper for the renterTester to call bubble on multiple
// directories and block until all the bubbles has executed.
func (rt *renterTester) bubbleAll(siaPaths []skymodules.SiaPath) (errs error) {
	// Define common variables
	var errMU sync.Mutex
	siaPathChan := make(chan skymodules.SiaPath, numBubbleWorkerThreads)
	var wg sync.WaitGroup

	// Define bubbleWorker to call bubble on siaPaths
	bubbleWorker := func() {
		for siaPath := range siaPathChan {
			err := rt.bubble(siaPath)
			errMU.Lock()
			errs = errors.Compose(errs, err)
			errMU.Unlock()
		}
	}

	// Launch bubble workers
	for i := 0; i < numBubbleWorkerThreads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bubbleWorker()
		}()
	}

	// Send siaPaths to bubble workers over the siaPathChan
	for _, siaPath := range siaPaths {
		// renterTester bubble method has timeout protection so no need for it here
		siaPathChan <- siaPath
	}
	return
}

// equalBubbledMetadata is a helper that checks for equality in the siadir
// metadata that gets bubbled
//
// Since we can't check timestamps for equality cause they are often set to
// `time.Now()` by methods, we allow a timestamp to be off by a certain delta.
func equalBubbledMetadata(md1, md2 siadir.Metadata, delta time.Duration) error {
	err1 := equalBubbledAggregateMetadata(md1, md2, delta)
	err2 := equalBubbledDirectoryMetadata(md1, md2, delta)
	return errors.Compose(err1, err2)
}

// equalBubbledAggregateMetadata is a helper that checks for equality in the
// aggregate siadir metadata fields that gets bubbled
//
// Since we can't check timestamps for equality cause they are often set to
// `time.Now()` by methods, we allow a timestamp to be off by a certain delta.
func equalBubbledAggregateMetadata(md1, md2 siadir.Metadata, delta time.Duration) error {
	// Check AggregateHealth
	if md1.AggregateHealth != md2.AggregateHealth {
		return fmt.Errorf("AggregateHealth not equal, %v and %v", md1.AggregateHealth, md2.AggregateHealth)
	}
	// Check AggregateLastHealthCheckTime
	if !timeEquals(md1.AggregateLastHealthCheckTime, md2.AggregateLastHealthCheckTime, delta) {
		return fmt.Errorf("AggregateLastHealthCheckTimes not equal %v and %v (%v)", md1.AggregateLastHealthCheckTime, md2.AggregateLastHealthCheckTime, delta)
	}
	// Check AggregateMinRedundancy
	if md1.AggregateMinRedundancy != md2.AggregateMinRedundancy {
		return fmt.Errorf("AggregateMinRedundancy not equal, %v and %v", md1.AggregateMinRedundancy, md2.AggregateMinRedundancy)
	}
	// Check AggregateModTime
	if !timeEquals(md2.AggregateModTime, md1.AggregateModTime, delta) {
		return fmt.Errorf("AggregateModTime not equal %v and %v (%v)", md1.AggregateModTime, md2.AggregateModTime, delta)
	}
	// Check AggregateNumFiles
	if md1.AggregateNumFiles != md2.AggregateNumFiles {
		return fmt.Errorf("AggregateNumFiles not equal, %v and %v", md1.AggregateNumFiles, md2.AggregateNumFiles)
	}
	// Check AggregateNumStuckChunks
	if md1.AggregateNumStuckChunks != md2.AggregateNumStuckChunks {
		return fmt.Errorf("AggregateNumStuckChunks not equal, %v and %v", md1.AggregateNumStuckChunks, md2.AggregateNumStuckChunks)
	}
	// Check AggregateNumSubDirs
	if md1.AggregateNumSubDirs != md2.AggregateNumSubDirs {
		return fmt.Errorf("AggregateNumSubDirs not equal, %v and %v", md1.AggregateNumSubDirs, md2.AggregateNumSubDirs)
	}
	// Check AggregateRemoteHealth
	if md1.AggregateRemoteHealth != md2.AggregateRemoteHealth {
		return fmt.Errorf("AggregateRemoteHealth not equal, %v and %v", md1.AggregateRemoteHealth, md2.AggregateRemoteHealth)
	}
	// Check AggregateRepairSize
	if md1.AggregateRepairSize != md2.AggregateRepairSize {
		return fmt.Errorf("AggregateRepairSize not equal, %v and %v", md1.AggregateRepairSize, md2.AggregateRepairSize)
	}
	// Check AggregateSize
	if md1.AggregateSize != md2.AggregateSize {
		return fmt.Errorf("AggregateSize not equal, %v and %v", md1.AggregateSize, md2.AggregateSize)
	}
	// Check AggregateStuckHealth
	if md1.AggregateStuckHealth != md2.AggregateStuckHealth {
		return fmt.Errorf("AggregateStuckHealth not equal, %v and %v", md1.AggregateStuckHealth, md2.AggregateStuckHealth)
	}
	// Check AggregateStuckSize
	if md1.AggregateStuckSize != md2.AggregateStuckSize {
		return fmt.Errorf("AggregateStuckSize not equal, %v and %v", md1.AggregateStuckSize, md2.AggregateStuckSize)
	}

	// Aggregate Skynet Fields
	//
	// Check AggregateSkynetFiles
	if md1.AggregateSkynetFiles != md2.AggregateSkynetFiles {
		return fmt.Errorf("AggregateSkynetFiles not equal, %v and %v", md1.AggregateSkynetFiles, md2.AggregateSkynetFiles)
	}
	// Check AggregateSkynetSize
	if md1.AggregateSkynetSize != md2.AggregateSkynetSize {
		return fmt.Errorf("AggregateSkynetSize not equal, %v and %v", md1.AggregateSkynetSize, md2.AggregateSkynetSize)
	}
	return nil
}

// equalBubbledDirectoryMetadata is a helper that checks for equality in the
// non-aggregate siadir metadata fields that gets bubbled
//
// Since we can't check timestamps for equality cause they are often set to
// `time.Now()` by methods, we allow a timestamp to be off by a certain delta.
func equalBubbledDirectoryMetadata(md1, md2 siadir.Metadata, delta time.Duration) error {
	// Check Health
	if md1.Health != md2.Health {
		return fmt.Errorf("Health not equal, %v and %v", md1.Health, md2.Health)
	}
	// Check LastHealthCheckTime
	if !timeEquals(md1.LastHealthCheckTime, md2.LastHealthCheckTime, delta) {
		return fmt.Errorf("LastHealthCheckTimes not equal %v and %v (%v)", md1.LastHealthCheckTime, md2.LastHealthCheckTime, delta)
	}
	// Check MinRedundancy
	if md1.MinRedundancy != md2.MinRedundancy {
		return fmt.Errorf("MinRedundancy not equal, %v and %v", md1.MinRedundancy, md2.MinRedundancy)
	}
	// Check ModTime
	if !timeEquals(md2.ModTime, md1.ModTime, delta) {
		return fmt.Errorf("ModTime not equal %v and %v (%v)", md1.ModTime, md2.ModTime, delta)
	}
	// Check NumFiles
	if md1.NumFiles != md2.NumFiles {
		return fmt.Errorf("NumFiles not equal, %v and %v", md1.NumFiles, md2.NumFiles)
	}
	// Check NumStuckChunks
	if md1.NumStuckChunks != md2.NumStuckChunks {
		return fmt.Errorf("NumStuckChunks not equal, %v and %v", md1.NumStuckChunks, md2.NumStuckChunks)
	}
	// Check NumSubDirs
	if md1.NumSubDirs != md2.NumSubDirs {
		return fmt.Errorf("NumSubDirs not equal, %v and %v", md1.NumSubDirs, md2.NumSubDirs)
	}
	// Check RemoteHealth
	if md1.RemoteHealth != md2.RemoteHealth {
		return fmt.Errorf("RemoteHealth not equal, %v and %v", md1.RemoteHealth, md2.RemoteHealth)
	}
	// Check RepairSize
	if md1.RepairSize != md2.RepairSize {
		return fmt.Errorf("RepairSize not equal, %v and %v", md1.RepairSize, md2.RepairSize)
	}
	// Check Size
	if md1.Size != md2.Size {
		return fmt.Errorf("Size not equal, %v and %v", md1.Size, md2.Size)
	}
	// Check StuckHealth
	if md1.StuckHealth != md2.StuckHealth {
		return fmt.Errorf("StuckHealth not equal, %v and %v", md1.StuckHealth, md2.StuckHealth)
	}
	// Check StuckSize
	if md1.StuckSize != md2.StuckSize {
		return fmt.Errorf("StuckSize not equal, %v and %v", md1.StuckSize, md2.StuckSize)
	}

	// Skynet Fields
	//
	// Check SkynetFiles
	if md1.SkynetFiles != md2.SkynetFiles {
		return fmt.Errorf("SkynetFiles not equal, %v and %v", md1.SkynetFiles, md2.SkynetFiles)
	}
	// Check SkynetSize
	if md1.SkynetSize != md2.SkynetSize {
		return fmt.Errorf("SkynetSize not equal, %v and %v", md1.SkynetSize, md2.SkynetSize)
	}
	return nil
}

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
