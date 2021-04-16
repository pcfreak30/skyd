package renter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

// newTestBatchWithFiles initializes a skylink batch and adds some files to the
// currentFiles map
func newTestBatchWithFiles(name string, numFiles int) *skylinkBatch {
	batch := &skylinkBatch{
		currentFiles:    make(map[batchUID]*skyFileObj),
		remainingMemory: maxBatchSize,
		staticFilename:  name,
	}

	filesPerBatch := numFiles
	size := maxBatchFileSize / uint64(filesPerBatch)
	for i := 0; i < filesPerBatch; i++ {
		data := fastrand.Bytes(int(size))
		batch.currentFiles[newBatchUID()] = &skyFileObj{
			data: data,
			size: size,
			sup: skymodules.SkyfileUploadParameters{
				Filename: fmt.Sprintf("%v_%v", name, i),
			},
		}
	}
	return batch
}

// TestSkyfileBatch probes the skyfile batch subsystem
func TestSkyfileBatch(t *testing.T) {
	t.Parallel()

	// Short Tests
	t.Run("BuildBaseSector", testBuildBaseSector)
	t.Run("PackFiles", testPackFiles)
	t.Run("ValidSUP", testValidBatchSUP)

	// Long Tests
	if testing.Short() {
		t.SkipNow()
	}

	t.Run("AddFile", testBatchAddFile)
	t.Run("BatchManager", testBatchManager)
}

// testBatchAddFile probes the addFile method of the skylinkBatch
func testBatchAddFile(t *testing.T) {
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
	case <-initialBatch.staticAvailable:
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

	// Make sure the result chan is closed.
	select {
	case <-resultChan:
	case <-time.After(time.Minute):
		t.Fatal("resultChan still blocking")
	}

	// Since the uploads will fail in a unit test, the skylinkData should have an
	// err and a null skylink
	if skylinkData.err == nil {
		t.Error("unexpected")
	}
	var nullSkyLink skymodules.Skylink
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
	case <-bm.activeBatch.staticAvailable:
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
	case <-batch.staticAvailable:
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
	case <-bm.activeBatch.staticAvailable:
		t.Fatal("result chan should not have been closed before maxBatchTime")
	case <-time.After(maxBatchTime):
	}
}

// testBatchManager tests the initialization of the batchManager and the
// blocking functionality.
func testBatchManager(t *testing.T) {
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
	sup := skymodules.SkyfileUploadParameters{
		Batch:    true,
		Filename: "filetoolarge",
		Mode:     persist.DefaultDiskPermissionsTest,
	}
	fileSize := maxBatchFileSize + 1
	reader := bytes.NewReader(fastrand.Bytes(int(fileSize)))
	sur := skymodules.NewSkyfileReader(reader, sup)
	_, err = rt.renter.BatchSkyfile(sup, sur)
	if err != errFileTooLarge {
		t.Error("expected errFileToLarge", err)
	}

	// Test blocking for single file
	done1 := make(chan struct{})
	fileSize = maxBatchFileSize - 1
	reader = bytes.NewReader(fastrand.Bytes(int(fileSize)))
	sur = skymodules.NewSkyfileReader(reader, sup)

	// Launch batch call in a go routine. Call will fail when it tries to upload
	// but the point is that it blocks until maxBatchTime
	go func() {
		// Ignoring error as we only care about the blocking nature and we know the
		// call will error for the upload not succeeding.
		_, _ = rt.renter.BatchSkyfile(sup, sur)
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
	sur = skymodules.NewSkyfileReader(reader, sup)
	reader2 := bytes.NewReader(fastrand.Bytes(int(fileSize)))
	sur2 := skymodules.NewSkyfileReader(reader2, sup)

	// Launch two batch call in a separate go routines. The second call will
	// trigger the first batch to finalize. To ensure the second call executes
	// after the first we also sleep for a bit before calling BatchSkyfile.
	//
	// NOTE: we ignore errors here again as we are checking the blocking nature of
	// the tests and we know both calls will error when trying to upload.
	go func() {
		_, _ = rt.renter.BatchSkyfile(sup, sur)
		close(done2)
	}()
	go func() {
		time.Sleep(maxBatchTime / 3)
		_, _ = rt.renter.BatchSkyfile(sup, sur2)
	}()
	select {
	case <-done2:
	case <-time.After(maxBatchTime):
		t.Error("batch call should have returned before maxBatchTime")
	}
}

// testBuildBaseSector probes the buildBaseSector method
func testBuildBaseSector(t *testing.T) {
	batch := newTestBatchWithFiles(t.Name(), 3)

	// Pack the files
	fps, packedSize, err := batch.packFiles()
	if err != nil {
		t.Fatal(err)
	}

	// Build the base sector
	baseSector, _, err := batch.buildBaseSector(fps, packedSize)
	if err != nil {
		t.Fatal(err)
	}

	// Parse the base sector
	sl, fanout, sm, smRaw, data, err := skymodules.ParseSkyfileMetadata(baseSector)
	if err != nil {
		t.Fatal(err)
	}
	if len(fanout) != 0 {
		t.Fatal("expected no fanout")
	}
	smRaw2, err := json.Marshal(sm)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(smRaw, smRaw2) {
		t.Fatal("mismatch")
	}

	// Build Expected metadata and baseSectorData
	baseSectorData := make([]byte, packedSize)
	subFiles := make(skymodules.SkyfileSubfiles)
	var batchLength uint64
	for _, fp := range fps {
		sfo, ok := batch.currentFiles[batchUID(fp.FileID)]
		if !ok {
			t.Fatal("unexpected")
		}
		sfo.fp = fp

		// Write the file data to the offset
		offset := fp.SectorOffset
		copy(baseSectorData[offset:], sfo.data)

		// Add to Subfiles
		subFileLen := uint64(len(sfo.data))
		subFiles[sfo.sup.Filename] = skymodules.SkyfileSubfileMetadata{
			FileMode: sfo.sup.Mode,
			Filename: sfo.sup.Filename,
			Offset:   fp.SectorOffset,
			Len:      subFileLen,
		}

		// Increment batch length
		batchLength += subFileLen
	}
	expectedMD := skymodules.SkyfileMetadata{
		Filename: batch.staticFilename,
		Length:   batchLength,
		Subfiles: subFiles,
	}

	// Verify metadata
	if !reflect.DeepEqual(sm, expectedMD) {
		t.Log("parsed", sm)
		t.Log("expected", expectedMD)
		t.Error("metadatas not equal")
	}

	// Verify baseSectorData
	if !bytes.Equal(data, baseSectorData) {
		t.Log("parsed", data[len(data)-100:])
		t.Log("expected", baseSectorData[len(baseSectorData)-100:])
		t.Error("base sector data not equal")
	}

	// Build Expected layout
	metadataBytes, err := skymodules.SkyfileMetadataBytes(expectedMD)
	if err != nil {
		t.Fatal(err)
	}
	expectedSL := skymodules.SkyfileLayout{
		Version:      skymodules.SkyfileVersion,
		Filesize:     packedSize,
		MetadataSize: uint64(len(metadataBytes)),
		CipherType:   crypto.TypePlain,
	}

	// Verify Layout
	if !reflect.DeepEqual(sl, expectedSL) {
		t.Log("parsed", sl)
		t.Log("expected", expectedSL)
		t.Error("layouts not equal")
	}
}

// testPackFiles probes the packFiles method
func testPackFiles(t *testing.T) {
	batch := newTestBatchWithFiles(t.Name(), 3)

	// Pack the files
	fps, _, err := batch.packFiles()
	if err != nil {
		t.Fatal(err)
	}

	// Verify files were packed
	if len(fps) != len(batch.currentFiles) {
		t.Errorf("expected %v to be packed but found %v", len(batch.currentFiles), len(fps))
	}
	for _, fp := range fps {
		_, ok := batch.currentFiles[batchUID(fp.FileID)]
		if !ok {
			t.Error("Packed file ID not found in current files map", fp.FileID)
		}
	}
}

// testValidBatchSUP probes the validBatchSUP function
func testValidBatchSUP(t *testing.T) {
	// Start with an incorrect SkyfileUploadParameters
	sup := skymodules.SkyfileUploadParameters{
		SiaPath:             skymodules.RandomSiaPath(),
		DryRun:              true,
		Force:               true,
		Root:                true,
		BaseChunkRedundancy: 1,
		Filename:            "",
		Mode:                0,
		DefaultPath:         "default",
		DisableDefaultPath:  true,
		Reader:              bytes.NewReader([]byte("file")),
		SkykeyName:          "skykey",
		// only need SkykeyName or SkykeyID
		Batch: false,
	}

	// The require fields are checked first
	//
	// First we should see the err for Batch not being true
	err := validBatchSUP(sup)
	if err != errBatchNotEnabled {
		t.Error("unexpected")
	}
	sup.Batch = true
	// Next we should see the err for BaseChunkRedundancy
	err = validBatchSUP(sup)
	if err != errBatchRedundancy {
		t.Error("unexpected")
	}
	sup.BaseChunkRedundancy = SkyfileDefaultBaseChunkRedundancy
	// Next we should see the err for Filename
	err = validBatchSUP(sup)
	if err != errBatchFilename {
		t.Error("unexpected")
	}
	sup.Filename = "testfile"
	// Next we should see the err for Mode
	err = validBatchSUP(sup)
	if err != errBatchMode {
		t.Error("unexpected")
	}
	sup.Mode = os.ModePerm

	// Then the fields that should be left blank are checked
	//
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
	// Next we should see the err for DryRun being true
	err = validBatchSUP(sup)
	if err != errBatchDryRun {
		t.Error("unexpected")
	}
	sup.DryRun = false
	// Next we should see the err for Force being true
	err = validBatchSUP(sup)
	if err != errBatchForce {
		t.Error("unexpected")
	}
	sup.Force = false
	// Next we should see the err for Root
	err = validBatchSUP(sup)
	if err != errBatchRoot {
		t.Error("unexpected")
	}
	sup.Root = false
	// Next we should see the err for encryption
	err = validBatchSUP(sup)
	if err != errBatchEncrypted {
		t.Error("unexpected")
	}
	sup.SkykeyName = ""
	// Next we should see the err for SiaPath
	err = validBatchSUP(sup)
	if err != errBatchSiaPath {
		t.Error("unexpected")
	}
	sup.SiaPath = skymodules.SiaPath{}

	// We should have no errors now
	err = validBatchSUP(sup)
	if err != nil {
		t.Error(err)
	}
	// Setting reader to nil should be no error as well
	sup.Reader = nil
	err = validBatchSUP(sup)
	if err != nil {
		t.Error(err)
	}

	// Verify setting only required fields
	sup = skymodules.SkyfileUploadParameters{}
	sup.Batch = true
	sup.Filename = "testfile"
	sup.Mode = os.ModePerm
	err = validBatchSUP(sup)
	if err != nil {
		t.Error(err)
	}
}
