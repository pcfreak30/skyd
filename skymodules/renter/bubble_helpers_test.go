package renter

import (
	"fmt"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem/siadir"
)

var (
	// bubbleWaitInTestTime is the amount of time a test should wait before
	// returning an error in test. This is a conservative value to prevent test
	// timeouts.
	bubbleWaitInTestTime = time.Minute
)

// bubbleBlocking is a helper for the renterTester to call bubble on a directory
// and block until the bubble has executed.
func (rt *renterTester) bubbleBlocking(siaPath skymodules.SiaPath) error {
	complete := rt.renter.staticBubbleScheduler.callQueueBubble(siaPath)
	select {
	case <-complete:
	case <-time.After(bubbleWaitInTestTime):
		return errors.New("test blocked too long for bubble")
	}
	return nil
}

// bubbleAllBlocking is a helper for the renterTester to call bubble on multiple
// directories and block until all the bubbles has executed.
func (rt *renterTester) bubbleAllBlocking(siaPaths []skymodules.SiaPath) (errs error) {
	// Define common variables
	var errMU sync.Mutex
	siaPathChan := make(chan skymodules.SiaPath, numBubbleWorkerThreads)
	var wg sync.WaitGroup

	// Define bubbleWorker to call bubble on siaPaths
	bubbleWorker := func() {
		for siaPath := range siaPathChan {
			err := errors.AddContext(rt.bubbleBlocking(siaPath), fmt.Sprintf("error with bubble on %v", siaPath))
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
func equalBubbledAggregateMetadata(md1, md2 siadir.Metadata, delta time.Duration) (err error) {
	// Check AggregateHealth
	if md1.AggregateHealth != md2.AggregateHealth {
		err = errors.Compose(err, fmt.Errorf("AggregateHealth not equal, %v and %v", md1.AggregateHealth, md2.AggregateHealth))
	}
	// Check AggregateLastHealthCheckTime
	if !timeEquals(md1.AggregateLastHealthCheckTime, md2.AggregateLastHealthCheckTime, delta) {
		err = errors.Compose(err, fmt.Errorf("AggregateLastHealthCheckTimes not equal %v and %v (%v)", md1.AggregateLastHealthCheckTime, md2.AggregateLastHealthCheckTime, delta))
	}
	// Check AggregateMinRedundancy
	if md1.AggregateMinRedundancy != md2.AggregateMinRedundancy {
		err = errors.Compose(err, fmt.Errorf("AggregateMinRedundancy not equal, %v and %v", md1.AggregateMinRedundancy, md2.AggregateMinRedundancy))
	}
	// Check AggregateModTime
	if !timeEquals(md2.AggregateModTime, md1.AggregateModTime, delta) {
		err = errors.Compose(err, fmt.Errorf("AggregateModTime not equal %v and %v (%v)", md1.AggregateModTime, md2.AggregateModTime, delta))
	}
	// Check AggregateNumFiles
	if md1.AggregateNumFiles != md2.AggregateNumFiles {
		err = errors.Compose(err, fmt.Errorf("AggregateNumFiles not equal, %v and %v", md1.AggregateNumFiles, md2.AggregateNumFiles))
	}
	// Check AggregateNumStuckChunks
	if md1.AggregateNumStuckChunks != md2.AggregateNumStuckChunks {
		err = errors.Compose(err, fmt.Errorf("AggregateNumStuckChunks not equal, %v and %v", md1.AggregateNumStuckChunks, md2.AggregateNumStuckChunks))
	}
	// Check AggregateNumSubDirs
	if md1.AggregateNumSubDirs != md2.AggregateNumSubDirs {
		err = errors.Compose(err, fmt.Errorf("AggregateNumSubDirs not equal, %v and %v", md1.AggregateNumSubDirs, md2.AggregateNumSubDirs))
	}
	// Check AggregateRemoteHealth
	if md1.AggregateRemoteHealth != md2.AggregateRemoteHealth {
		err = errors.Compose(err, fmt.Errorf("AggregateRemoteHealth not equal, %v and %v", md1.AggregateRemoteHealth, md2.AggregateRemoteHealth))
	}
	// Check AggregateRepairSize
	if md1.AggregateRepairSize != md2.AggregateRepairSize {
		err = errors.Compose(err, fmt.Errorf("AggregateRepairSize not equal, %v and %v", md1.AggregateRepairSize, md2.AggregateRepairSize))
	}
	// Check AggregateSize
	if md1.AggregateSize != md2.AggregateSize {
		err = errors.Compose(err, fmt.Errorf("AggregateSize not equal, %v and %v", md1.AggregateSize, md2.AggregateSize))
	}
	// Check AggregateStuckHealth
	if md1.AggregateStuckHealth != md2.AggregateStuckHealth {
		err = errors.Compose(err, fmt.Errorf("AggregateStuckHealth not equal, %v and %v", md1.AggregateStuckHealth, md2.AggregateStuckHealth))
	}
	// Check AggregateStuckSize
	if md1.AggregateStuckSize != md2.AggregateStuckSize {
		err = errors.Compose(err, fmt.Errorf("AggregateStuckSize not equal, %v and %v", md1.AggregateStuckSize, md2.AggregateStuckSize))
	}

	// Aggregate Skynet Fields
	//
	// Check AggregateSkynetFiles
	if md1.AggregateSkynetFiles != md2.AggregateSkynetFiles {
		err = errors.Compose(err, fmt.Errorf("AggregateSkynetFiles not equal, %v and %v", md1.AggregateSkynetFiles, md2.AggregateSkynetFiles))
	}
	// Check AggregateSkynetSize
	if md1.AggregateSkynetSize != md2.AggregateSkynetSize {
		err = errors.Compose(err, fmt.Errorf("AggregateSkynetSize not equal, %v and %v", md1.AggregateSkynetSize, md2.AggregateSkynetSize))
	}
	return
}

// equalBubbledDirectoryMetadata is a helper that checks for equality in the
// non-aggregate siadir metadata fields that gets bubbled
//
// Since we can't check timestamps for equality cause they are often set to
// `time.Now()` by methods, we allow a timestamp to be off by a certain delta.
func equalBubbledDirectoryMetadata(md1, md2 siadir.Metadata, delta time.Duration) (err error) {
	// Check Health
	if md1.Health != md2.Health {
		err = errors.Compose(err, fmt.Errorf("Health not equal, %v and %v", md1.Health, md2.Health))
	}
	// Check LastHealthCheckTime
	if !timeEquals(md1.LastHealthCheckTime, md2.LastHealthCheckTime, delta) {
		err = errors.Compose(err, fmt.Errorf("LastHealthCheckTimes not equal %v and %v (%v)", md1.LastHealthCheckTime, md2.LastHealthCheckTime, delta))
	}
	// Check MinRedundancy
	if md1.MinRedundancy != md2.MinRedundancy {
		err = errors.Compose(err, fmt.Errorf("MinRedundancy not equal, %v and %v", md1.MinRedundancy, md2.MinRedundancy))
	}
	// Check ModTime
	if !timeEquals(md2.ModTime, md1.ModTime, delta) {
		err = errors.Compose(err, fmt.Errorf("ModTime not equal %v and %v (%v)", md1.ModTime, md2.ModTime, delta))
	}
	// Check NumFiles
	if md1.NumFiles != md2.NumFiles {
		err = errors.Compose(err, fmt.Errorf("NumFiles not equal, %v and %v", md1.NumFiles, md2.NumFiles))
	}
	// Check NumStuckChunks
	if md1.NumStuckChunks != md2.NumStuckChunks {
		err = errors.Compose(err, fmt.Errorf("NumStuckChunks not equal, %v and %v", md1.NumStuckChunks, md2.NumStuckChunks))
	}
	// Check NumSubDirs
	if md1.NumSubDirs != md2.NumSubDirs {
		err = errors.Compose(err, fmt.Errorf("NumSubDirs not equal, %v and %v", md1.NumSubDirs, md2.NumSubDirs))
	}
	// Check RemoteHealth
	if md1.RemoteHealth != md2.RemoteHealth {
		err = errors.Compose(err, fmt.Errorf("RemoteHealth not equal, %v and %v", md1.RemoteHealth, md2.RemoteHealth))
	}
	// Check RepairSize
	if md1.RepairSize != md2.RepairSize {
		err = errors.Compose(err, fmt.Errorf("RepairSize not equal, %v and %v", md1.RepairSize, md2.RepairSize))
	}
	// Check Size
	if md1.Size != md2.Size {
		err = errors.Compose(err, fmt.Errorf("Size not equal, %v and %v", md1.Size, md2.Size))
	}
	// Check StuckHealth
	if md1.StuckHealth != md2.StuckHealth {
		err = errors.Compose(err, fmt.Errorf("StuckHealth not equal, %v and %v", md1.StuckHealth, md2.StuckHealth))
	}
	// Check StuckSize
	if md1.StuckSize != md2.StuckSize {
		err = errors.Compose(err, fmt.Errorf("StuckSize not equal, %v and %v", md1.StuckSize, md2.StuckSize))
	}

	// Skynet Fields
	//
	// Check SkynetFiles
	if md1.SkynetFiles != md2.SkynetFiles {
		err = errors.Compose(err, fmt.Errorf("SkynetFiles not equal, %v and %v", md1.SkynetFiles, md2.SkynetFiles))
	}
	// Check SkynetSize
	if md1.SkynetSize != md2.SkynetSize {
		err = errors.Compose(err, fmt.Errorf("SkynetSize not equal, %v and %v", md1.SkynetSize, md2.SkynetSize))
	}
	return
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
