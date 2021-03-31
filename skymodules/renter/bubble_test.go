package renter

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/skynetlabs/skyd/build"
	"gitlab.com/skynetlabs/skyd/siatest/dependencies"
	"gitlab.com/skynetlabs/skyd/skymodules"
	"gitlab.com/skynetlabs/skyd/skymodules/renter/filesystem/siadir"
)

// TestBubble tests the bubble code.
func TestBubble(t *testing.T) {
	t.Parallel()

	// bubbleQueue unit tests
	t.Run("BubbleQueue", testBubbleQueue)

	// bubbleScheduler unit tests
	t.Run("BubbleScheduler", testBubbleScheduler)
}

// testBubbleQueue probes the bubbleQueue
func testBubbleQueue(t *testing.T) {
	// Initialize a queue
	bq := newBubbleQueue()

	// Calling pop should result in nil because it is empty
	bu := bq.Pop()
	if bu != nil {
		t.Error("expected nil bubble update")
	}

	// Add a bubble update to the queue
	newBU := &bubbleUpdate{
		staticSiaPath: skymodules.RandomSiaPath(),
		status:        bubbleQueued,
	}
	bq.Push(newBU)

	// Pop should return the bubble update just pushed
	bu = bq.Pop()
	if bu == nil {
		t.Fatal("nil bubble update popped")
	}
	if !reflect.DeepEqual(newBU, bu) {
		t.Log("found", bu)
		t.Log("expected", newBU)
		t.Error("Popped bubble update unexpected")
	}

	// Another call to Pop should return nil
	bu = bq.Pop()
	if bu != nil {
		t.Error("expected nil bubble update")
	}

	// Push the bubble Update back to the queue
	bq.Push(newBU)

	// Push a few more updates
	for i := 0; i < 5; i++ {
		bq.Push(&bubbleUpdate{
			staticSiaPath: skymodules.RandomSiaPath(),
			status:        bubbleQueued,
		})
	}

	// Pop should return the first update pushed
	bu = bq.Pop()
	if bu == nil {
		t.Fatal("nil bubble update popped")
	}
	if !reflect.DeepEqual(newBU, bu) {
		t.Log("found", bu)
		t.Log("expected", newBU)
		t.Error("Popped bubble update unexpected")
	}
}

// testBubbleScheduler probes the bubbleScheduler
func testBubbleScheduler(t *testing.T) {
	// Basic functionality test
	t.Run("Basic", testBubbleScheduler_Basic)

	// Specific Methods
	t.Run("managedQueueParent", testBubbleScheduler_managedQueueParent)

	if testing.Short() {
		t.SkipNow()
	}

	// Blocking functionality test
	t.Run("Blocking", testBubbleScheduler_Blocking)
	t.Run("BubbledMetadata", testBubbleScheduler_BubbledMetadata)
}

// testBubbleScheduler_Basic probes the basic functionality of the
// bubbleScheduler
func testBubbleScheduler_Basic(t *testing.T) {
	// Initialize a bubble scheduler
	bs := newBubbleScheduler(&Renter{})

	// Check status
	bs.mu.Lock()
	fifoSize := bs.fifo.Len()
	mapSize := len(bs.bubbleUpdates)
	bs.mu.Unlock()
	if fifoSize != 0 || mapSize != 0 {
		t.Error("unexpected", fifoSize, mapSize)
	}

	// managedPop should return nil
	bu := bs.managedPop()
	if bu != nil {
		t.Error("expected nil bubble update")
	}

	// checkStatus is a helper function to check the status of the bubble update
	checkStatus := func(siaPath skymodules.SiaPath, status bubbleStatus, checkQueue bool) {
		// Map and queue should both contain the same update
		bu, ok := bs.bubbleUpdates[siaPath]
		if !ok {
			t.Fatal("bad")
		}
		if !bu.staticSiaPath.Equals(siaPath) {
			t.Log("found", bu.staticSiaPath)
			t.Log("expected", siaPath)
			t.Error("incorrect siaPath found")
		}
		if bu.status != status {
			t.Log("found", bu.status)
			t.Log("expected", status)
			t.Error("incorrect status found")
		}

		if !checkQueue {
			return
		}

		// Check the queue
		queuedBU := bs.fifo.Pop()
		if !reflect.DeepEqual(bu, queuedBU) {
			t.Log("map BU", bu)
			t.Log("queue BU", queuedBU)
			t.Error("map and queue have different bubble updates")
		}

		// Push the update back to the queue
		bs.fifo.Push(queuedBU)
	}

	// queue a bubble update request
	siaPath := skymodules.RandomSiaPath()
	_ = bs.callQueueBubble(siaPath)

	// Check the status
	checkStatus(siaPath, bubbleQueued, true)

	// Check status
	bs.mu.Lock()
	fifoSize = bs.fifo.Len()
	mapSize = len(bs.bubbleUpdates)
	bs.mu.Unlock()
	if fifoSize != 1 || mapSize != 1 {
		t.Error("unexpected", fifoSize, mapSize)
	}

	// Calling queue again should have no impact
	_ = bs.callQueueBubble(siaPath)
	checkStatus(siaPath, bubbleQueued, true)

	// Check status
	bs.mu.Lock()
	fifoSize = bs.fifo.Len()
	mapSize = len(bs.bubbleUpdates)
	bs.mu.Unlock()
	if fifoSize != 1 || mapSize != 1 {
		t.Error("unexpected", fifoSize, mapSize)
	}

	// managedPop should return the bubble update with the status now set to
	// bubbleActive
	bu = bs.managedPop()
	checkStatus(siaPath, bubbleActive, false)

	// Queue should be empty but map should still have update
	bs.mu.Lock()
	fifoSize = bs.fifo.Len()
	mapSize = len(bs.bubbleUpdates)
	bs.mu.Unlock()
	if fifoSize != 0 || mapSize != 1 {
		t.Error("unexpected", fifoSize, mapSize)
	}

	// Calling queue should update the status to pending
	_ = bs.callQueueBubble(siaPath)
	checkStatus(siaPath, bubblePending, false)

	// Queue should be empty but map should still have update
	bs.mu.Lock()
	fifoSize = bs.fifo.Len()
	mapSize = len(bs.bubbleUpdates)
	bs.mu.Unlock()
	if fifoSize != 0 || mapSize != 1 {
		t.Error("unexpected", fifoSize, mapSize)
	}

	// Calling queue again should have no impact
	_ = bs.callQueueBubble(siaPath)
	checkStatus(siaPath, bubblePending, false)

	// Queue should be empty but map should still have update
	bs.mu.Lock()
	fifoSize = bs.fifo.Len()
	mapSize = len(bs.bubbleUpdates)
	bs.mu.Unlock()
	if fifoSize != 0 || mapSize != 1 {
		t.Error("unexpected", fifoSize, mapSize)
	}

	// Calling complete should add the update back to the queue
	bs.managedCompleteBubbleUpdate(siaPath)
	checkStatus(siaPath, bubbleQueued, true)

	// Check Status
	bs.mu.Lock()
	fifoSize = bs.fifo.Len()
	mapSize = len(bs.bubbleUpdates)
	bs.mu.Unlock()
	if fifoSize != 1 || mapSize != 1 {
		t.Error("unexpected", fifoSize, mapSize)
	}

	// calling managed pop and then calling complete should result in an empty map
	// and queue
	_ = bs.managedPop()
	bs.managedCompleteBubbleUpdate(siaPath)

	// Check Status
	bs.mu.Lock()
	fifoSize = bs.fifo.Len()
	mapSize = len(bs.bubbleUpdates)
	bs.mu.Unlock()
	if fifoSize != 0 || mapSize != 0 {
		t.Error("unexpected", fifoSize, mapSize)
	}
}

// testBubbleScheduler_Blocking probes the blocking nature of the complete
// channel in the bubble update
func testBubbleScheduler_Blocking(t *testing.T) {
	// Initialize a bubble scheduler
	bs := newBubbleScheduler(&Renter{})

	// queue a bubble update request
	siaPath := skymodules.RandomSiaPath()
	completeChan := bs.callQueueBubble(siaPath)

	// Call complete in a go routine.
	start := time.Now()
	duration := time.Second
	go func() {
		time.Sleep(duration)
		// Call pop to prevent panic for incorrect status when complete is called
		bu := bs.managedPop()
		if bu == nil {
			t.Error("no bubble update")
			return
		}
		// calling complete should close the channel
		bs.managedCompleteBubbleUpdate(siaPath)
	}()

	// Should be blocking until after the duration
	select {
	case <-completeChan:
	case <-time.After(bubbleWaitInTestTime):
		t.Fatal("test blocked too long for bubble")
	}
	if time.Since(start) < duration {
		t.Error("complete chan closed sooner than expected")
	}

	// Complete chan should not block anymore
	select {
	case <-completeChan:
	case <-time.After(bubbleWaitInTestTime):
		t.Fatal("test blocked too long for bubble")
	}

	// If multiple calls are made to queue the same bubble update, they should all
	// block on the same channel
	start = time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			completeChan := bs.callQueueBubble(siaPath)
			select {
			case <-completeChan:
			case <-time.After(bubbleWaitInTestTime):
				t.Error("test blocked too long for bubble")
				return
			}
			if time.Since(start) < duration {
				t.Error("complete chan closed before time duration")
			}
		}()
	}

	// Sleep for the duration
	time.Sleep(duration)
	// Call pop to prevent panic for incorrect status when complete is called
	bu := bs.managedPop()
	if bu == nil {
		t.Fatal("no bubble update")
	}
	// calling complete should close the channel
	bs.managedCompleteBubbleUpdate(siaPath)

	// Wait for go routines to finish
	wg.Wait()

	// Queue the bubble update request
	completeChan = bs.callQueueBubble(siaPath)

	// Pop the update
	bu = bs.managedPop()
	if bu == nil {
		t.Fatal("no bubble update")
	}

	// Call queue again to update the status to pending
	//
	// The complete channel returned should be the same as the original channel
	completeChan2 := bs.callQueueBubble(siaPath)

	// Call complete
	bs.managedCompleteBubbleUpdate(siaPath)

	// Both of the original complete channels should not longer be blocking
	select {
	case <-completeChan:
	default:
		t.Error("first complete chan is still blocking")
	}
	select {
	case <-completeChan2:
	default:
		t.Error("second complete chan is still blocking")
	}

	// The complete chan in the bubble update should still be blocking
	select {
	case <-bu.complete:
		t.Error("bubble update complete chan is not blocking")
	default:
	}
}

// testBubbleScheduler_managedQueueParent probes the managedQueueParent method.
func testBubbleScheduler_managedQueueParent(t *testing.T) {
	// Initialize a bubble scheduler
	bs := newBubbleScheduler(&Renter{})

	// Calling managedQueueParent on root should be a no-op
	err := bs.managedQueueParent(skymodules.RootSiaPath())
	if err != nil {
		t.Fatal(err)
	}

	// There should be nothing in the bubbleScheduler
	bs.mu.Lock()
	if len(bs.bubbleUpdates) != 0 {
		t.Error("unexpected")
	}
	if bs.fifo.Len() != 0 {
		t.Error("unexpected")
	}
	bs.mu.Unlock()

	// Call managedQueueParent on a siapath
	err = bs.managedQueueParent(skymodules.RandomSiaPath())
	if err != nil {
		t.Fatal(err)
	}

	// There should be a root dir update in the bubbleScheduler
	bs.mu.Lock()
	if len(bs.bubbleUpdates) != 1 {
		t.Error("unexpected")
	}
	if bs.fifo.Len() != 1 {
		t.Error("unexpected")
	}
	mapBU, ok := bs.bubbleUpdates[skymodules.RootSiaPath()]
	if !ok {
		t.Error("root update not found in map")
	}
	bs.mu.Unlock()
	popBU := bs.managedPop()
	if popBU == nil {
		t.Error("nil bubble update popped")
	}
	if !reflect.DeepEqual(mapBU, popBU) {
		t.Error("map and popped update don't match")
	}
}

// testBubbleScheduler_BubbledMetadata verifies that the metadata is bubbled as
// expected.
func testBubbleScheduler_BubbledMetadata(t *testing.T) {
	// Create test renter that has the background health and repair loops disabled
	// so that we know the only bubbled that are being triggered are from this
	// test.
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Bubble all the system dirs.
	beforeBubble := time.Now()
	err = rt.bubbleAllBlocking([]skymodules.SiaPath{skymodules.BackupFolder, skymodules.SkynetFolder, skymodules.UserFolder})
	if err != nil {
		t.Fatal(err)
	}
	defaultMetadata := siadir.Metadata{
		AggregateHealth: siadir.DefaultDirHealth,
		Health:          siadir.DefaultDirHealth,

		AggregateStuckHealth: siadir.DefaultDirHealth,
		StuckHealth:          siadir.DefaultDirHealth,

		AggregateLastHealthCheckTime: beforeBubble,
		LastHealthCheckTime:          beforeBubble,

		AggregateMinRedundancy: siadir.DefaultDirRedundancy,
		MinRedundancy:          siadir.DefaultDirRedundancy,

		AggregateNumSubDirs: 5,
		NumSubDirs:          3,
	}
	err = build.Retry(20, time.Second, func() error {
		// Get Root Directory Health
		metadata, err := rt.renter.managedDirectoryMetadata(skymodules.RootSiaPath())
		if err != nil {
			return err
		}
		// Set the ModTimes since those are not initialized until Bubble is Called
		defaultMetadata.AggregateModTime = metadata.AggregateModTime
		defaultMetadata.ModTime = metadata.ModTime
		// Check metadata
		if err = equalBubbledMetadata(metadata, defaultMetadata, time.Since(beforeBubble)); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create a filesystem with the following structure
	//
	// Default structure
	// fs/
	// fs/var
	// fs/var/skynet
	// fs/home
	// fs/home/user
	// fs/snapshots
	//
	// Additional folders
	// fs/SubDir1/
	// fs/SubDir1/SubDir1/
	// fs/SubDir1/SubDir2/

	// Create directory tree
	subDir1, err := skymodules.NewSiaPath("SubDir1")
	if err != nil {
		t.Fatal(err)
	}
	subDir2, err := skymodules.NewSiaPath("SubDir2")
	if err != nil {
		t.Fatal(err)
	}
	subDir1_1, err := subDir1.Join(subDir1.String())
	if err != nil {
		t.Fatal(err)
	}
	subDir1_2, err := subDir1.Join(subDir2.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.renter.CreateDir(subDir1_1, skymodules.DefaultDirPerm); err != nil {
		t.Fatal(err)
	}
	if err := rt.renter.CreateDir(subDir1_2, skymodules.DefaultDirPerm); err != nil {
		t.Fatal(err)
	}

	// Define random metadata to ensure all fields are impacted. Then adjust the
	// metadata as needed. This helps future proof the test and potentially fail
	// if new fields are added.
	metadataUpdate := safeRandomMetadata()
	// Adjust select fields to target specific test cases and avoid NDFs.
	checkTime := time.Now()
	// Define the health fields to control which directory has the worst.
	metadataUpdate.AggregateHealth, metadataUpdate.Health = 1, 1
	metadataUpdate.AggregateRemoteHealth, metadataUpdate.RemoteHealth = 0, 0
	metadataUpdate.AggregateStuckHealth, metadataUpdate.StuckHealth = 0, 0
	// Define the time fields to avoid NDFs during comparison.
	metadataUpdate.AggregateLastHealthCheckTime, metadataUpdate.LastHealthCheckTime = checkTime, checkTime
	// Reset subdir fields to avoid NDFs. The number of subdir for this test is
	// known and is updated when checking the metadata.
	metadataUpdate.AggregateNumSubDirs, metadataUpdate.NumSubDirs = 0, 0
	// Reset file and size fields to avoid NDFs. The number of files and the size
	// for this test are known and are updated when checking the metadata.
	metadataUpdate.AggregateNumFiles, metadataUpdate.NumFiles = 0, 0
	metadataUpdate.AggregateSize, metadataUpdate.Size = 0, 0
	// Reset Skynet Fields.
	metadataUpdate.AggregateSkynetFiles, metadataUpdate.SkynetFiles = 0, 0
	metadataUpdate.AggregateSkynetSize, metadataUpdate.SkynetSize = 0, 0
	// Reset repair fields to avoid NDFs.
	metadataUpdate.AggregateRepairSize, metadataUpdate.RepairSize = 0, 0
	metadataUpdate.AggregateStuckSize, metadataUpdate.StuckSize = 0, 0

	// Update the metadatas
	siaPaths := []skymodules.SiaPath{skymodules.RootSiaPath(), subDir1, subDir1_1, subDir1_2}
	for _, sp := range siaPaths {
		if sp.Equals(subDir1_2) {
			// Set health of subDir1/subDir2 to be the worst health
			metadataUpdate.AggregateHealth = 4
			metadataUpdate.Health = 4
		}
		if err := rt.openAndUpdateDir(sp, metadataUpdate); err != nil {
			t.Fatal(err)
		}
	}

	// Define common test parameters
	fileSize := uint64(100)
	var dp, pp int = 1, 1
	rsc, _ := skymodules.NewRSCode(dp, pp)
	// Worst health with current erasure coding is 2 = (1 - (0-1)/1)
	worstFileHealth := 1 - (0-float64(dp))/float64(pp)

	// Define test helper
	bubbleAndVerifyMetadata := func(testCase string, dirToBubble, expectedMDDir skymodules.SiaPath, dirMD siadir.Metadata, anf, ansd, asf uint64) {
		// Bubble target directory
		beforeBubble := time.Now()
		if err := rt.bubbleBlocking(dirToBubble); err != nil {
			t.Fatal(err)
		}

		// Verify the directories metadata is as expected before continuing
		expectedMetadata, err := rt.renter.managedDirectoryMetadata(expectedMDDir)
		if err != nil {
			t.Log("Test case:", testCase)
			t.Fatal(err)
		}

		// Update Times
		dirMD.AggregateLastHealthCheckTime = expectedMetadata.AggregateLastHealthCheckTime
		dirMD.LastHealthCheckTime = expectedMetadata.LastHealthCheckTime
		dirMD.AggregateModTime = expectedMetadata.AggregateModTime
		dirMD.ModTime = expectedMetadata.ModTime

		err = equalBubbledMetadata(expectedMetadata, dirMD, time.Since(beforeBubble))
		if err != nil {
			t.Log("Test case:", testCase)
			expectedData, _ := json.MarshalIndent(dirMD, "", " ")
			data, _ := json.MarshalIndent(expectedMetadata, "", " ")
			t.Log("Expected Metadata")
			t.Log(string(expectedData))
			t.Log("Dir Metadata")
			t.Log(string(data))
			t.Fatal(err)
		}

		// Check metadata from expectedMDDir to root
		var metadata siadir.Metadata
		if err = build.Retry(100, 100*time.Millisecond, func() error {
			// Get Expected Directory Metadata
			expectedMetadata, err = rt.renter.managedDirectoryMetadata(expectedMDDir)
			if err != nil {
				return err
			}
			// Get Root Directory Metadata
			metadata, err = rt.renter.managedDirectoryMetadata(skymodules.RootSiaPath())
			if err != nil {
				return err
			}

			// Set values for the expected Aggregate count fields that won't be
			// a direct match between the metadatas
			//
			// Files, Size, SubDirs
			expectedMetadata.AggregateNumFiles = anf
			expectedMetadata.AggregateSize = anf * fileSize
			expectedMetadata.AggregateNumSubDirs = ansd
			// Skynet
			expectedMetadata.AggregateSkynetFiles = asf
			expectedMetadata.AggregateSkynetSize = 0
			if asf > 0 {
				expectedMetadata.AggregateSkynetSize = (asf + 1) * fileSize
			}
			// Repair, StuckRepair should always match based on the structure of the
			// test.
			expectedMetadata.AggregateRepairSize = anf*modules.SectorSize*uint64(dp+pp) - expectedMetadata.AggregateStuckSize

			// Set expected health values
			ef := float64(expectedMetadata.AggregateHealth)
			rf := float64(metadata.AggregateHealth)
			expectedMetadata.AggregateHealth = math.Max(ef, rf)

			// Mod Times are impacted by the bubbles so use the root directory values
			expectedMetadata.AggregateModTime = metadata.AggregateModTime

			// Compare the aggregate fields from the expectedMDDir and root
			return equalBubbledAggregateMetadata(metadata, expectedMetadata, time.Since(beforeBubble))
		}); err != nil {
			t.Log("Test case:", testCase)
			expectedData, _ := json.MarshalIndent(expectedMetadata, "", " ")
			data, _ := json.MarshalIndent(metadata, "", " ")
			t.Logf("Bubbling %v, expected %v", dirToBubble, expectedMDDir)
			t.Log("Expected Metadata")
			t.Log(string(expectedData))
			t.Log("Root Metadata")
			t.Log(string(data))
			t.Fatal(err)
		}
	}

	/*
	* TEST BUBBLE HANDLING OF HEALTH, FILE, AND SUBDIR COUNTS
	 */

	// Bubble the health of the directory that has the worst pre set health
	// subDir1/subDir2, the health that gets bubbled should be the health of
	// subDir1/subDir1 since subDir1/subDir2 is empty meaning it's calculated
	// health will return to the default health, even though we set the health
	// to be the worst health
	//
	// Note: this tests the edge case of bubbling an empty directory and
	// directories with no files but do have sub directories since bubble will
	// execute on all the parent directories
	tc := "Empty directory reset"
	expected := metadataUpdate
	expected.AggregateHealth, expected.Health = 1, 1
	bubbleAndVerifyMetadata(tc, subDir1_2, subDir1_1, expected, 0, 8, 0)

	// Add a file to the lowest level
	fileSiaPath, err := subDir1_2.Join("test")
	if err != nil {
		t.Fatal(err)
	}
	ct := crypto.RandomCipherType()
	f, err := rt.renter.createRenterTestFileWithParamsAndSize(fileSiaPath, rsc, ct, fileSize)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Since we are just adding the file, no chunks will have been uploaded
	// meaning the health of the file should be the worst case health. Now the
	// health that is bubbled up should be the health of the file added to
	// subDir1/subDir2
	//
	// Note: this tests the edge case of bubbling a directory with a file
	// but no sub directories
	rt.renter.managedUpdateRenterContractsAndUtilities()
	offline, goodForRenew, _, _ := rt.renter.callRenterContractsAndUtilities()
	fileHealth, _, _, _, _, _, _ := f.Health(offline, goodForRenew)
	// Worst health with current erasure coding is 2 = (1 - (0-1)/1)
	if fileHealth != worstFileHealth {
		t.Fatalf("Expected heath to be %v, got %v", worstFileHealth, fileHealth)
	}

	// Mark the file as stuck by marking one of its chunks as stuck
	f.SetStuck(0, true)

	// Update the file metadata within the dir.
	err = rt.updateFileMetadatas(subDir1_2)
	if err != nil {
		t.Fatal(err)
	}

	// Now when we bubble we expected the following updates to the bubbled folder:
	//
	// Health should be 0 because the file is stuck
	//
	// MinRedundancy should be 0
	//
	// NumFiles is 1
	//
	// NumStuckChunks is 1 for the stuck file that has 1 chunk
	//
	// Size should reflect the 1 file
	//
	// Stuck Health should be the worst file health
	//
	// StuckSize should be the sector size times the number of pieces
	expected.AggregateHealth, expected.Health = 0, 0
	expected.AggregateMinRedundancy, expected.MinRedundancy = 0, 0
	expected.AggregateNumFiles, expected.NumFiles = 1, 1
	expected.AggregateNumStuckChunks, expected.NumStuckChunks = 1, 1
	expected.AggregateSize, expected.Size = fileSize, fileSize
	expected.AggregateStuckHealth, expected.StuckHealth = worstFileHealth, worstFileHealth
	repairSize := modules.SectorSize * uint64(dp+pp)
	expected.AggregateStuckSize, expected.StuckSize = repairSize, repairSize
	expected.Version = "1.0"

	tc = "Adding stuck file"
	bubbleAndVerifyMetadata(tc, subDir1_2, subDir1_2, expected, 1, 8, 0)

	// Mark the file as un-stuck
	f.SetStuck(0, false)

	// Update the file metadata within the dir.
	err = rt.updateFileMetadatas(subDir1_2)
	if err != nil {
		t.Fatal(err)
	}

	// Now when we bubble we expected the following updates to the bubbled folder:
	//
	// Health should be the worst file health
	//
	// RemoteHealth should be the worst file health because the file doesn't have
	// a local file on disk
	//
	// NumStuckChunks is 0
	//
	// RepairSize should be the sector size times the number of pieces
	//
	// Stuck Health should be 0
	//
	// StuckSize should be 0
	expected.AggregateHealth, expected.Health = worstFileHealth, worstFileHealth
	expected.AggregateNumStuckChunks, expected.NumStuckChunks = 0, 0
	expected.AggregateRemoteHealth, expected.RemoteHealth = worstFileHealth, worstFileHealth
	expected.AggregateRepairSize, expected.RepairSize = repairSize, repairSize
	expected.AggregateStuckHealth, expected.StuckHealth = 0, 0
	expected.AggregateStuckSize, expected.StuckSize = 0, 0

	tc = "Un-stuck file"
	bubbleAndVerifyMetadata(tc, subDir1_2, subDir1_2, expected, 1, 8, 0)

	/*
	* TEST BUBBLE HANDLING OF SKYNET COUNTS
	 */

	// Add a file to the root directory that has a skylink. This should not count
	// towards to number of skyfiles but should count towards the skynet size.
	rootFile, err := rt.renter.createRenterTestFileWithParamsAndSize(skymodules.RandomSiaPath(), rsc, ct, fileSize)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rootFile.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	var skylink skymodules.Skylink
	err = rootFile.AddSkylink(skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Add a file to the skynet directory
	skyfileSiaPath, err := skymodules.SkynetFolder.Join(hex.EncodeToString(fastrand.Bytes(8)))
	if err != nil {
		t.Fatal(err)
	}
	skyFile, err := rt.renter.createRenterTestFileWithParamsAndSize(skyfileSiaPath, rsc, ct, fileSize)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := skyFile.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Now when we bubble we expected the following updates to the bubbled folder:
	//
	// SkynetFiles should be a 1
	//
	// SkynetSize should be the file size.  In Production this is padded to
	// a sector size
	expected.AggregateSkynetFiles, expected.SkynetFiles = 1, 1
	expected.AggregateSkynetSize, expected.SkynetSize = fileSize, fileSize
	tc = "Skynet Stats Bubble"
	bubbleAndVerifyMetadata(tc, skymodules.SkynetFolder, skymodules.SkynetFolder, expected, 3, 8, 1)

	/*
	* FINAL TEST CASE
	 */

	// Add a sub directory to the directory that contains the file that has a
	// worst health than the file and confirm that health gets bubbled up.
	//
	// Note: this tests the edge case of bubbling a directory that has both a
	// file and a sub directory
	//
	// Note: This test is last otherwise all the impact directories would need to
	// be reset
	subDir1_2_1, err := subDir1_2.Join(subDir1.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.renter.CreateDir(subDir1_2_1, skymodules.DefaultDirPerm); err != nil {
		t.Fatal(err)
	}

	// Reset metadataUpdate with expected values
	metadataUpdate.AggregateHealth, metadataUpdate.Health = 4, 4
	metadataUpdate.AggregateRemoteHealth, metadataUpdate.RemoteHealth = 4, 4
	metadataUpdate.AggregateStuckHealth, metadataUpdate.StuckHealth = 4, 4
	if err := rt.openAndUpdateDir(subDir1_2_1, metadataUpdate); err != nil {
		t.Fatal(err)
	}

	// Now when we bubble we expected the following updates to the bubbled folder:
	//
	// Aggregate Health values should be a 4 and directory health values should be worstFileHealth
	//
	// Aggregate Stuck Health should be a 4 and directory stuck health should be 0
	//
	// NumSubDirs is 1
	//
	// No Skynet Stats
	expected.AggregateHealth, expected.Health = 4, worstFileHealth
	expected.AggregateNumSubDirs, expected.NumSubDirs = 1, 1
	expected.AggregateRemoteHealth, expected.RemoteHealth = 4, worstFileHealth
	expected.AggregateStuckHealth, expected.StuckHealth = 4, 0
	expected.AggregateSkynetSize, expected.SkynetSize = 0, 0
	expected.AggregateSkynetFiles, expected.SkynetFiles = 0, 0
	tc = "Empty lowest level directory"
	bubbleAndVerifyMetadata(tc, subDir1_2, subDir1_2, expected, 3, 9, 1)
}
