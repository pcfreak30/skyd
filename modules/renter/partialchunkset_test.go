package renter

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"gitlab.com/NebulousLabs/fastrand"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/writeaheadlog"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules/renter/siafile"
)

// TestCombinedChunkName tests the combinedChunkName function.
func TestCombinedChunkName(t *testing.T) {
	rs, err := siafile.NewRSSubCode(10, 20, crypto.SegmentSize)
	if err != nil {
		t.Fatal(err)
	}
	rcid := randomChunkID()
	chunkName := combinedChunkName(rs, rcid)
	expectedName := fmt.Sprintf("%v%v%v", rs.Identifier(), combinedChunkNameSeparator, rcid)
	if chunkName != expectedName {
		t.Fatalf("name doesn't match expected name: %v %v",
			chunkName, expectedName)
	}
}

// TestLoadPartialChunk tests the LoadPartialChunk method of the partial chunk
// set.
func TestLoadPartialChunk(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	// Create a partial chunk set.
	testDir := build.TempDir("renter", t.Name())
	_ = os.RemoveAll(testDir)
	pcs, err := newPartialChunkSet(filepath.Join(testDir, "pcs"))
	if err != nil {
		t.Fatal(err)
	}
	// Create the erasure coder.
	ec, err := siafile.NewRSSubCode(10, 20, crypto.SegmentSize)
	if err != nil {
		t.Fatal(err)
	}
	chunkSize := (modules.SectorSize - crypto.TypeDefaultRenter.Overhead()) * uint64(ec.MinPieces())

	wal := newTestingWal()
	sfs := siafile.NewSiaFileSet(filepath.Join(testDir, "siafiles"), wal)
	if err != nil {
		t.Fatal(err)
	}
	up1 := modules.FileUploadParams{
		SiaPath:     modules.RandomSiaPath(),
		ErasureCode: ec,
	}
	up2 := modules.FileUploadParams{
		SiaPath:     modules.RandomSiaPath(),
		ErasureCode: ec,
	}
	// Create two files with partial chunks that fill out 70% of a combined chunk
	// each.
	sf1, err1 := sfs.NewSiaFile(up1, crypto.GenerateSiaKey(crypto.TypeDefaultRenter), uint64(0.7*float64(chunkSize)), 0600)
	sf2, err2 := sfs.NewSiaFile(up2, crypto.GenerateSiaKey(crypto.TypeDefaultRenter), uint64(0.7*float64(chunkSize)), 0600)
	if err := errors.Compose(err1, err2); err != nil {
		t.Fatal(err)
	}
	// Save their partial chunks.
	partialChunk1 := fastrand.Bytes(int(sf1.Size()))
	if err := pcs.SavePartialChunk(sf1.SiaFile, partialChunk1); err != nil {
		t.Fatal(err)
	}
	partialChunk2 := fastrand.Bytes(int(sf1.Size()))
	if err := pcs.SavePartialChunk(sf2.SiaFile, partialChunk2); err != nil {
		t.Fatal(err)
	}
	// Load their partial chunks and compare them.
	snap1, err := sf1.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	snap2, err := sf2.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	pc1a, err := pcs.LoadPartialChunk(&unfinishedDownloadChunk{renterFile: snap1})
	if err != nil {
		t.Fatal(err)
	}
	pc2a, err := pcs.LoadPartialChunk(&unfinishedDownloadChunk{renterFile: snap2})
	if err != nil {
		t.Fatal(err)
	}
	pc2b, err := pcs.LoadPartialChunk(&unfinishedDownloadChunk{renterFile: snap2, staticChunkIndex: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(partialChunk1, pc1a) {
		t.Error("loaded chunk doesn't match saved chunk", len(partialChunk1), len(pc1a))
	}
	if !bytes.Equal(partialChunk2, append(pc2a, pc2b...)) {
		t.Error("loaded chunk doesn't match saved chunk", len(partialChunk2), len(pc2a), len(pc2b))
	}
}

// TestNewUnfinishedCombinedChunk tests the NewUnfinishedCombinedChunk method.
func TestNewUnfinishedCombinedChunk(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	// Create a partial chunk set.
	testDir := build.TempDir("renter", t.Name())
	_ = os.RemoveAll(testDir)
	pcs, err := newPartialChunkSet(testDir)
	if err != nil {
		t.Fatal(err)
	}
	// Create the erasure coder.
	ec, err := siafile.NewRSSubCode(10, 20, crypto.SegmentSize)
	if err != nil {
		t.Fatal(err)
	}
	// Start a new unfinished combined chunk and apply the update.
	ccid, updates, err := pcs.newUnfinishedCombinedChunk(ec)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeaheadlog.ApplyUpdates(updates...); err != nil {
		t.Fatal(err)
	}
	// Make sure the file exists at the right location.
	fi, err := os.Stat(pcs.combinedChunkPath(ccid, ec, true))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != 0 {
		t.Fatal("Expected new unfinished chunk to be empty")
	}
	// Check if the metadata is valid.
	md, err := pcs.loadChunkMetadata(ccid, ec)
	if err != nil {
		t.Fatal("Failed to load metadata", err)
	}
	expectedMD := combinedChunkMetadata{}
	if !reflect.DeepEqual(md, expectedMD) {
		t.Fatal("Chunk's metadata should be the default metadata")
	}
}

// TestNewPartialChunkSet tests that creating a new partial chunks set works as
// expected for both fresh sets and loading existing ones.
func TestNewPartialChunkSet(t *testing.T) {
	// Create an empty testDir.
	testDir := build.TempDir("renter", t.Name())
	_ = os.RemoveAll(testDir)
	if err := os.MkdirAll(testDir, 0600); err != nil {
		t.Fatal(err)
	}
	combinedChunkDir := filepath.Join(testDir, "combined_chunks")
	// Create a new partialChunkSet.
	pcs, err := newPartialChunkSet(combinedChunkDir)
	if err != nil {
		t.Fatal(err)
	}
	// The combinedChunkDir should exist.
	if _, err := os.Stat(combinedChunkDir); err != nil {
		t.Fatal("combined chunk dir should exist")
	}
	// The partial chunk set shouldn't track any incomplete combined chunks right
	// now.
	if len(pcs.unfinishedCombinedChunk) != 0 {
		t.Fatalf("There should be 0 unfinished combined chunks but got %v",
			len(pcs.unfinishedCombinedChunk))
	}
	// Write down a finished and one unfinished chunk for a certain erasure coder,
	// one finished chunk for a second erasure coder and one unfinished chunk for a
	// third erasure coder.
	ec11, err1 := siafile.NewRSSubCode(1, 1, crypto.SegmentSize)
	ec12, err2 := siafile.NewRSSubCode(1, 2, crypto.SegmentSize)
	ec31, err3 := siafile.NewRSSubCode(3, 1, crypto.SegmentSize)
	if err := errors.Compose(err1, err2, err3); err != nil {
		t.Fatal(err)
	}
	unfinishedChunkID1 := randomChunkID()
	unfinishedChunkID2 := randomChunkID()
	finishedChunk11 := combinedChunkName(ec11, randomChunkID()) + modules.CombinedChunkExtension
	unfinishedChunk11 := combinedChunkName(ec11, unfinishedChunkID1) + modules.CombinedChunkExtension + modules.UnfinishedChunkExtension
	finishedChunk12 := combinedChunkName(ec12, randomChunkID()) + modules.CombinedChunkExtension
	unfinishedChunk31 := combinedChunkName(ec31, unfinishedChunkID2) + modules.CombinedChunkExtension + modules.UnfinishedChunkExtension
	err = ioutil.WriteFile(filepath.Join(pcs.combinedChunkRoot, finishedChunk11), []byte{0}, 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = ioutil.WriteFile(filepath.Join(pcs.combinedChunkRoot, unfinishedChunk11), []byte{0}, 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = ioutil.WriteFile(filepath.Join(pcs.combinedChunkRoot, finishedChunk12), []byte{0}, 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = ioutil.WriteFile(filepath.Join(pcs.combinedChunkRoot, unfinishedChunk31), []byte{0}, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// Load the partialChunkSet with those new files. There should be 2 unfinished
	// chunks now.
	pcs, err = newPartialChunkSet(testDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcs.unfinishedCombinedChunk) != 2 {
		t.Fatalf("There should be 2 unfinished combined chunks but got %v",
			len(pcs.unfinishedCombinedChunk))
	}
	// Make sure the right chunks are tracked.
	if cid, exists := pcs.unfinishedCombinedChunk[ec11.Identifier()]; !exists || cid != unfinishedChunkID1 {
		t.Fatalf("%v %v %v", cid, unfinishedChunkID1, exists)
	}
	if cid, exists := pcs.unfinishedCombinedChunk[ec31.Identifier()]; !exists || cid != unfinishedChunkID2 {
		t.Fatalf("%v %v %v", cid, unfinishedChunkID2, exists)
	}
	// Write another unfinished chunk to disk and reuse the erasure coder
	// identifier to cause a conflict.
	unfinishedChunkConflict := combinedChunkName(ec11, randomChunkID()) + modules.CombinedChunkExtension + modules.UnfinishedChunkExtension
	err = ioutil.WriteFile(filepath.Join(pcs.combinedChunkRoot, unfinishedChunkConflict), []byte{0}, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// Creating it should fail.
	pcs, err = newPartialChunkSet(testDir)
	if err == nil {
		t.Fatal("Creating a partial chunk set should fail but didn't")
	}
}

// TestFetchLogicalCombinedChunk tests if FetchLogicalCombinedChunk correctly
// creates and loads combined chunks.
func TestFetchLogicalCombinedChunk(t *testing.T) {
	// Prepare a testdir.
	testdir := build.TempDir("renter", t.Name())
	sourcesDir := filepath.Join(testdir, "sources")
	filesDir := filepath.Join(testdir, "siafiles")
	combinedChunkDir := filepath.Join(testdir, "combinedchunks")
	if err := os.MkdirAll(sourcesDir, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filesDir, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(combinedChunkDir, 0600); err != nil {
		t.Fatal(err)
	}
	// Create a wal.
	txns, wal, err := writeaheadlog.New(filepath.Join(testdir, "wal.wal"))
	if err != nil {
		t.Fatal(err)
	}
	if len(txns) > 0 {
		t.Fatal("wal shouldn't return any pending transactions")
	}
	// Create a fileset.
	sfs := siafile.NewSiaFileSet(filesDir, wal)
	// Each file will have the same redundancy settings, chunkSize and filemode.
	ec, err := siafile.NewRSSubCode(1, 1, crypto.SegmentSize)
	if err != nil {
		t.Fatal(err)
	}
	fm := os.FileMode(0600)
	chunkSize := uint64(ec.MinPieces()) * modules.SectorSize
	// Create the partialChunkSet.
	pcs, err := newPartialChunkSet(combinedChunkDir)
	if err != nil {
		t.Fatal(err)
	}
	// Declare a helper function to create a siafile for testing already contained
	// within a minimal unfinishedUploadChunk.
	newFile := func(fileSize uint64) (*unfinishedUploadChunk, []byte) {
		if fileSize >= chunkSize {
			t.Fatal("can only create files with a single partial chunk")
		}
		p := filepath.Join(sourcesDir, hex.EncodeToString(fastrand.Bytes(16)))
		sp, err := modules.NewSiaPath(filepath.Base(p))
		if err != nil {
			t.Fatal(err)
		}
		up := modules.FileUploadParams{
			Source:              p,
			SiaPath:             sp,
			ErasureCode:         ec,
			Force:               false,
			DisablePartialChunk: false,
			Repair:              false,
		}
		mk := crypto.GenerateSiaKey(crypto.TypeThreefish)
		sf, err := sfs.NewSiaFile(up, mk, fileSize, fm)
		if err != nil {
			t.Fatal(err)
		}
		uuc := &unfinishedUploadChunk{
			fileEntry: sf,
		}
		return uuc, fastrand.Bytes(int(fileSize))
	}
	// Create 3 files. Two with 30% chunkSize and the third one with 50% to get
	// over 100%.
	uuc30One, uuc30OnePartialChunk := newFile(uint64(float64(chunkSize) / 3.0))
	uuc30Two, uuc30TwoPartialChunk := newFile(uint64(float64(chunkSize) / 3.0))
	uuc30Three, uuc30ThreePartialChunk := newFile(uint64(float64(chunkSize) / 2.0))
	defer uuc30One.fileEntry.Close()
	defer uuc30Two.fileEntry.Close()
	defer uuc30Three.fileEntry.Close()
	// The status should be "hasChunk"
	if !uuc30One.fileEntry.HasPartialChunk() || len(uuc30One.fileEntry.PartialChunks()) != 0 ||
		!uuc30Two.fileEntry.HasPartialChunk() || len(uuc30Two.fileEntry.PartialChunks()) != 0 ||
		!uuc30Three.fileEntry.HasPartialChunk() || len(uuc30Three.fileEntry.PartialChunks()) != 0 {
		t.Fatal("files should have partial chunk but no combined chunks yet")
	}
	// Try to get combined chunks of those files. This should fail as they have the
	// wrong status.
	if _, err := pcs.FetchLogicalCombinedChunk(uuc30One); err == nil {
		t.Fatal("FetchLogicalCombinedChunk should return an error")
	}
	if _, err := pcs.FetchLogicalCombinedChunk(uuc30Two); err == nil {
		t.Fatal("FetchLogicalCombinedChunk should return an error")
	}
	if _, err := pcs.FetchLogicalCombinedChunk(uuc30Three); err == nil {
		t.Fatal("FetchLogicalCombinedChunk should return an error")
	}
	// Save the partial chunks to move the status to "Incomplete"
	if err := pcs.SavePartialChunk(uuc30One.fileEntry.SiaFile, uuc30OnePartialChunk); err != nil {
		t.Fatal(err)
	}
	if uuc30One.fileEntry.PartialChunks()[0].Status != siafile.CombinedChunkStatusInComplete {
		t.Fatal("status of file isn't 'incomplete'")
	}
	if len(uuc30One.fileEntry.PartialChunks()) != 1 {
		t.Fatalf("expected file to have 1 combined chunk but got %v",
			len(uuc30One.fileEntry.PartialChunks()))
	}
	if uuc30One.fileEntry.PartialChunks()[0].Index != 0 {
		t.Fatal("file should have chunk index 0")
	}
	// Fetch the combined chunk for the first file. This should return 'false'
	// since we don't have enough data yet.
	fetched, err := pcs.FetchLogicalCombinedChunk(uuc30One)
	if err != nil {
		t.Fatal(err)
	}
	if fetched {
		t.Fatal("combined chunk was fetched even though it shouldn't have")
	}
	// Save the partial chunk for the second file.
	if err := pcs.SavePartialChunk(uuc30Two.fileEntry.SiaFile, uuc30TwoPartialChunk); err != nil {
		t.Fatal(err)
	}
	if uuc30Two.fileEntry.PartialChunks()[0].Status != siafile.CombinedChunkStatusInComplete {
		t.Fatal("status of file isn't 'incomplete'")
	}
	if len(uuc30Two.fileEntry.PartialChunks()) != 1 {
		t.Fatalf("expected file to have 1 combined chunk but got %v",
			len(uuc30Two.fileEntry.PartialChunks()))
	}
	if uuc30Two.fileEntry.PartialChunks()[0].Index != 0 {
		t.Fatal("file should have chunk index 0")
	}
	// Fetch the combined chunk for the second file. This should still return
	// 'false'.
	fetched, err = pcs.FetchLogicalCombinedChunk(uuc30Two)
	if err != nil {
		t.Fatal(err)
	}
	if fetched {
		t.Fatal("combined chunk was fetched even though it shouldn't have")
	}
	// Save the partial chunk for the third file.
	if err := pcs.SavePartialChunk(uuc30Three.fileEntry.SiaFile, uuc30ThreePartialChunk); err != nil {
		t.Fatal(err)
	}
	if uuc30Three.fileEntry.PartialChunks()[0].Status != siafile.CombinedChunkStatusInComplete {
		t.Fatal("status of file isn't 'incomplete'")
	}
	if uuc30Three.fileEntry.PartialChunks()[1].Status != siafile.CombinedChunkStatusInComplete {
		t.Fatal("status of file isn't 'incomplete'")
	}
	if len(uuc30Three.fileEntry.PartialChunks()) != 2 {
		t.Fatalf("expected file to have 2 combined chunks but got %v",
			len(uuc30Three.fileEntry.PartialChunks()))
	}
	if uuc30Three.fileEntry.PartialChunks()[0].Index != 0 || uuc30Three.fileEntry.PartialChunks()[1].Index != 1 {
		t.Fatal("file should have chunk indices 0 and 1")
	}
	// Fetch the combined chunk for all files. This should return 'true'.
	fetched, err = pcs.FetchLogicalCombinedChunk(uuc30One)
	if err != nil {
		t.Fatal(err)
	}
	if !fetched {
		t.Fatal("combined chunk wasn't fetched")
	}
	fetched, err = pcs.FetchLogicalCombinedChunk(uuc30Two)
	if err != nil {
		t.Fatal(err)
	}
	if !fetched {
		t.Fatal("combined chunk wasn't fetched")
	}
	fetched, err = pcs.FetchLogicalCombinedChunk(uuc30Three)
	if err != nil {
		t.Fatal(err)
	}
	if !fetched {
		t.Fatal("combined chunk wasn't fetched")
	}
	// The status should be "Completed" now.
	// TODO: Uncomment this once it's actually set to completed
	//	if uuc30One.fileEntry.CombinedChunkStatus() != siafile.CombinedChunkStatusCompleted ||
	//		uuc30Two.fileEntry.CombinedChunkStatus() != siafile.CombinedChunkStatusCompleted ||
	//		uuc30Three.fileEntry.CombinedChunkStatus() != siafile.CombinedChunkStatusCompleted {
	//		t.Fatal("status of files isn't 'Completed'")
	//	}
	// The logical data should be set.
	if uuc30One.logicalChunkData == nil {
		t.Fatal("logicalChunkData wasn't set")
	}
	if uuc30Two.logicalChunkData == nil {
		t.Fatal("logicalChunkData wasn't set")
	}
	if uuc30Three.logicalChunkData == nil {
		t.Fatal("logicalChunkData wasn't set")
	}
	// All files should have their offsets and lengths set correctly.
	files := []*siafile.SiaFile{uuc30One.fileEntry.SiaFile, uuc30Two.fileEntry.SiaFile, uuc30Three.fileEntry.SiaFile}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Metadata().PartialChunks[0].Offset < files[j].Metadata().PartialChunks[0].Offset
	})
	// Check the offsets of the files.
	if files[0].Metadata().PartialChunks[0].Offset != 0 {
		t.Fatal("first file doesn't have correct offset")
	}
	if files[1].Metadata().PartialChunks[0].Offset != files[0].Size() {
		t.Fatal("second file doesn't have correct offset")
	}
	if files[2].Metadata().PartialChunks[0].Offset != files[0].Size()+files[1].Size() {
		t.Fatal("third file doesn't have correct offset")
	}
	// Check the lengths of the files.
	if files[0].Metadata().PartialChunks[0].Length != files[0].Size() {
		t.Fatal("first file doesn't have correct length")
	}
	if files[1].Metadata().PartialChunks[0].Length != files[1].Size() {
		t.Fatal("second file doesn't have correct length")
	}
	if files[2].Metadata().PartialChunks[0].Length+files[2].Metadata().PartialChunks[1].Length != files[2].Size() {
		t.Fatal("third file doesn't have correct length")
	}
}
