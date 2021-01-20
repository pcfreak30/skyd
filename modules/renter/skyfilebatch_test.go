package renter

import (
	"bytes"
	"os"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/fastrand"
)

// TestBatchAddFile probes the addFile method of the skylinkBatch
func TestBatchAddFile(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create renter and grab the batchManager
	rt, err := newRenterTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err = rt.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()
	bm := rt.renter.staticBatchManager

	// Create a skyFileObj
	filesPerBatch := 3
	size := maxBatchFileSize / uint64(filesPerBatch)
	data := fastrand.Bytes(int(size))
	f := &skyFileObj{
		data: data,
		size: size,
	}

	// The first call to addFile should trigger the batch initialization which
	// will result in a background timer that will eventually close the results
	// chan
	//
	// First prove that the background timer isn't running by waiting for longer
	// than the batch time
	initialBatch := bm.activeBatch
	select {
	case <-initialBatch.resultChan:
		t.Fatal("resultChan closed before maxBatchTime")
	case <-time.After(maxBatchTime * 2):
	}

	// Now add a file to trigger the background timer
	//
	// When manually calling addFile the batchManager Lock must be acquired
	bm.mu.Lock()
	skylinkData, resultChan := initialBatch.addFile(f)
	bm.mu.Unlock()
	select {
	case <-resultChan:
		t.Fatal("resultChan closed before maxBatchTime")
	case <-time.After(maxBatchTime):
	}

	// Make sure the result chan is closed, if the resultChan is still open this
	// will block and timeout the test suite.
	<-resultChan

	// Since the uploads will fail in a unit test, the skylinkData should have an
	// err and a null skylink
	if skylinkData.err == nil {
		t.Error("unexpected")
	}
	var nullSkyLink modules.Skylink
	if skylinkData.skylink != nullSkyLink {
		t.Error("unexpected")
	}

	// Check that the batch was properly updated
	if !initialBatch.finalized {
		t.Error("unexpected")
	}
	if len(initialBatch.currentFiles) != 1 {
		t.Errorf("Expected 1 file found %v", len(initialBatch.currentFiles))
	}
	if initialBatch.err == nil {
		t.Error("unexpected")
	}
	if len(initialBatch.externSkylinkData) != 1 {
		t.Errorf("Expected 1 file found %v", len(initialBatch.currentFiles))
	}
	if initialBatch.remainingMemory != maxBatchSize-size {
		t.Errorf("Expected remainingMemory to be  %v but found %v", maxBatchSize-size, initialBatch.remainingMemory)
	}

	// The active batch on the batchManager should now be fresh. Check the
	// resultChan of the active batch
	select {
	case <-bm.activeBatch.resultChan:
		t.Fatal("channel shouldn't be close")
	default:
	}

	// Grab the new batch
	batch := bm.activeBatch

	// Add multiple files to the batch to trigger the resetting of the batch due
	// to the memory limit being hit. This time we must use the batchManager's
	// callAddFile method to avoid data races.
	for i := 0; i < filesPerBatch+1; i++ {
		go func() {
			// When manually calling addFile the batchManager Lock must be acquired
			bm.mu.Lock()
			batch.addFile(f)
			bm.mu.Unlock()
		}()
	}
	select {
	case <-batch.resultChan:
	case <-time.After(maxBatchTime):
		t.Fatal("result chan should have closed before the maxBatchTime")
	}

	// Check that the batch was properly updated
	if !batch.finalized {
		t.Error("unexpected")
	}
	if len(batch.currentFiles) != filesPerBatch {
		t.Errorf("Expected %v file found %v", filesPerBatch, len(initialBatch.currentFiles))
	}
	if batch.err == nil {
		t.Error("unexpected")
	}
	if len(batch.externSkylinkData) != filesPerBatch {
		t.Errorf("Expected %v file found %v", filesPerBatch, len(initialBatch.currentFiles))
	}
	expected := maxBatchSize - size*uint64(filesPerBatch)
	if batch.remainingMemory != expected {
		t.Errorf("Expected remainingMemory to be  %v but found %v", expected, batch.remainingMemory)
	}

	// The active branch of the batchManager should have also been initialized so
	// we would expect its resultChan to close before the maxBatchTime
	//
	// Check while holding the lock to ensure that the activeBatch has
	// successfully been created.
	bm.mu.Lock()
	defer bm.mu.Unlock()
	select {
	case <-bm.activeBatch.resultChan:
		t.Fatal("result chan should not have been closed before maxBatchTime")
	case <-time.After(maxBatchTime):
	}
}

// TestBatchManager tests the initialization of the batchManager and the
// blocking functionality.
func TestBatchManager(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create renter
	rt, err := newRenterTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err = rt.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()
	if rt.renter.staticBatchManager == nil {
		t.Fatal("batchManager not initialized with renter startup")
	}

	// Check batchManager
	bm := rt.renter.staticBatchManager
	if bm.activeBatch == nil {
		t.Fatal("activeBatch not initialized")
	}

	// Define too large file
	sup := modules.SkyfileUploadParameters{Batch: true}
	fileSize := maxBatchFileSize + 1
	reader := bytes.NewReader(fastrand.Bytes(int(fileSize)))
	sur := modules.NewSkyfileReader(reader, sup)
	_, err = rt.renter.BatchSkyfile(sup, sur)
	if err != errFileToLarge {
		t.Error("expected errFileToLarge", err)
	}

	// Test blocking for single file
	done1 := make(chan struct{})
	fileSize = maxBatchFileSize - 1
	reader = bytes.NewReader(fastrand.Bytes(int(fileSize)))
	sur = modules.NewSkyfileReader(reader, sup)

	// Launch batch call in a go routine. Call will fail when it tries to upload
	// but the point is that it blocks until maxBatchTime
	go func() {
		rt.renter.BatchSkyfile(sup, sur)
		close(done1)
	}()
	select {
	case <-done1:
		t.Error("batch call returned before maxBatchTime")
	case <-time.After(maxBatchTime):
	}

	// Test blocking for multiple files
	done2 := make(chan struct{})

	// Redefine initial reader as it would be drained and define second reader or
	// the first call will drain the reader and not trigger the memory limit.
	reader = bytes.NewReader(fastrand.Bytes(int(fileSize)))
	sur = modules.NewSkyfileReader(reader, sup)
	reader2 := bytes.NewReader(fastrand.Bytes(int(fileSize)))
	sur2 := modules.NewSkyfileReader(reader2, sup)

	// Launch two batch call in a separate go routines. The second call will
	// trigger the first batch to finalize.
	go func() {
		rt.renter.BatchSkyfile(sup, sur)
		close(done2)
	}()
	go func() {
		rt.renter.BatchSkyfile(sup, sur2)
	}()
	select {
	case <-done2:
	case <-time.After(maxBatchTime):
		t.Error("batch call should have returned before maxBatchTime")
	}
}

// TestBatch_validBatchSUP probes the validBatchSUP function
func TestBatch_validBatchSUP(t *testing.T) {
	t.Parallel()

	// Start with a fully set SkyfileUploadParameters
	sup := modules.SkyfileUploadParameters{
		SiaPath:             modules.RandomSiaPath(),
		DryRun:              true,
		Force:               true,
		Root:                true,
		BaseChunkRedundancy: 1,
		Filename:            "testfile",
		Mode:                os.ModePerm,
		DefaultPath:         "default",
		DisableDefaultPath:  true,
		Reader:              bytes.NewReader([]byte("file")),
		SkykeyName:          "skykey",
		// only need SkykeyName or SkykeyID
		Batch: false,
	}

	// First we should see the err for Batch not being true
	err := validBatchSUP(sup)
	if err != errBatchNotEnabled {
		t.Error("unexpected")
	}
	sup.Batch = true
	// Next we should see the err for Force being true
	err = validBatchSUP(sup)
	if err != errBatchForce {
		t.Error("unexpected")
	}
	sup.Force = false
	// Next we should see the err for encryption
	err = validBatchSUP(sup)
	if err != errBatchEncrypted {
		t.Error("unexpected")
	}
	sup.SkykeyName = ""
	// Next we should see the err for DryRun being true
	err = validBatchSUP(sup)
	if err != errBatchDryRun {
		t.Error("unexpected")
	}
	sup.DryRun = false
	// Next we should see the err for BaseChunkRedundancy
	err = validBatchSUP(sup)
	if err != errBatchRedundancy {
		t.Error("unexpected")
	}
	sup.BaseChunkRedundancy = SkyfileDefaultBaseChunkRedundancy
	// Next we should see the err for DefaultPath
	err = validBatchSUP(sup)
	if err != errBatchDefaultPath {
		t.Error("unexpected")
	}
	sup.DisableDefaultPath = false
	err = validBatchSUP(sup)
	if err != errBatchDefaultPath {
		t.Error("unexpected")
	}
	sup.DefaultPath = ""

	// We should have no errors now
	err = validBatchSUP(sup)
	if err != nil {
		t.Error(err)
	}
	// We should also only need to set the Batch parameter to true
	sup = modules.SkyfileUploadParameters{}
	sup.Batch = true
	err = validBatchSUP(sup)
	if err != nil {
		t.Error(err)
	}
}
