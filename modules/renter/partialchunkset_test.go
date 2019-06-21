package renter

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"gitlab.com/NebulousLabs/fastrand"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules/renter/siafile"
	"gitlab.com/NebulousLabs/writeaheadlog"
)

// TestFetchLogicalCombinedChunk tests if FetchLogicalCombinedChunk correctly
// creates and loads combined chunks.
func TestFetchLogicalCombinedChunk(t *testing.T) {
	// Prepare a testdir.
	testdir := build.TempDir("renter", t.Name())
	sourcesDir := filepath.Join(testdir, "sources")
	filesDir := filepath.Join(testdir, "siafiles")
	if err := os.MkdirAll(sourcesDir, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filesDir, 0600); err != nil {
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
	pcs := newPartialChunkSet()
	// Declare a helper function to create a siafile for testing already contained
	// within a minimal unfinishedUploadChunk.
	newFile := func(fileSize uint64) *unfinishedUploadChunk {
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
		return uuc
	}
	// Create 3 files with a 30% chunkSize each.
	uuc30One := newFile(uint64(float64(chunkSize) / 3.0))
	uuc30Two := newFile(uint64(float64(chunkSize) / 3.0))
	uuc30Three := newFile(uint64(float64(chunkSize) / 3.0))
	defer uuc30One.fileEntry.Close()
	defer uuc30Two.fileEntry.Close()
	defer uuc30Three.fileEntry.Close()
	// The status should be "hasChunk"
	if uuc30One.fileEntry.CombinedChunkStatus() != siafile.CombinedChunkStatusHasChunk ||
		uuc30Two.fileEntry.CombinedChunkStatus() != siafile.CombinedChunkStatusHasChunk ||
		uuc30Three.fileEntry.CombinedChunkStatus() != siafile.CombinedChunkStatusHasChunk {
		t.Fatal("status of files isn't 'hasChunk'")
	}
	// Try to include the first file in a combined chunk. This should fail due to the wrong status.
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
	if err := uuc30One.fileEntry.SavePartialChunk(fastrand.Bytes(int(uuc30One.fileEntry.Size()))); err != nil {
		t.Fatal(err)
	}
	if err := uuc30Two.fileEntry.SavePartialChunk(fastrand.Bytes(int(uuc30Two.fileEntry.Size()))); err != nil {
		t.Fatal(err)
	}
	if err := uuc30Three.fileEntry.SavePartialChunk(fastrand.Bytes(int(uuc30Three.fileEntry.Size()))); err != nil {
		t.Fatal(err)
	}
	// The status should be "Incomplete" now.
	if uuc30One.fileEntry.CombinedChunkStatus() != siafile.CombinedChunkStatusIncomplete ||
		uuc30Two.fileEntry.CombinedChunkStatus() != siafile.CombinedChunkStatusIncomplete ||
		uuc30Three.fileEntry.CombinedChunkStatus() != siafile.CombinedChunkStatusIncomplete {
		t.Fatal("status of files isn't 'incomplete'")
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
	// The partialChunkSet should contain exactly 1 request for uuc30One.
	if len(pcs.requests) != 1 {
		t.Fatalf("len(pcs.requests) should be %v but was %v", 1, len(pcs.requests))
	}
	if n := len(pcs.requests[ec.Identifier()]); n != 1 {
		t.Fatalf("the chunkRequestSet should contain exactly %v request but was %v", 1, n)
	}
	if _, exists := pcs.requests[ec.Identifier()][uuc30One.fileEntry.UID()]; !exists {
		t.Fatal("request for wrong siafile exists")
	}
	// Try to fetch it again. This should still return 'false'.
	fetched, err = pcs.FetchLogicalCombinedChunk(uuc30One)
	if err != nil {
		t.Fatal(err)
	}
	if fetched {
		t.Fatal("combined chunk was fetched even though it shouldn't have")
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
	// The partialChunkSet should contain 1 request for uuc30Two. That means it
	// contains 2 requests in total.
	if len(pcs.requests) != 1 {
		t.Fatalf("len(pcs.requests) should be %v but was %v", 2, len(pcs.requests))
	}
	if n := len(pcs.requests[ec.Identifier()]); n != 2 {
		t.Fatalf("the chunkRequestSet should contain exactly %v request but was %v", 2, n)
	}
	if _, exists := pcs.requests[ec.Identifier()][uuc30One.fileEntry.UID()]; !exists {
		t.Fatal("request for wrong siafile exists")
	}
	if _, exists := pcs.requests[ec.Identifier()][uuc30Two.fileEntry.UID()]; !exists {
		t.Fatal("request for wrong siafile exists")
	}
	// Fetch the combined chunk for the third file. This should still return
	// 'true' since we are within the threshold.
	fetched, err = pcs.FetchLogicalCombinedChunk(uuc30Three)
	if err != nil {
		t.Fatal(err)
	}
	if !fetched {
		t.Fatal("combined chunk wasn't fetched")
	}
	// The status should be "Completed" now.
	if uuc30One.fileEntry.CombinedChunkStatus() != siafile.CombinedChunkStatusCompleted ||
		uuc30Two.fileEntry.CombinedChunkStatus() != siafile.CombinedChunkStatusCompleted ||
		uuc30Three.fileEntry.CombinedChunkStatus() != siafile.CombinedChunkStatusCompleted {
		t.Fatal("status of files isn't 'Completed'")
	}
	// The logical data of uuc30Three should be set.
	if uuc30Three.logicalChunkData == nil {
		t.Fatal("logicalChunkData wasn't set")
	}
	// All files should have their offsets and lengths set correctly.
	files := []*siafile.SiaFile{uuc30One.fileEntry.SiaFile, uuc30Two.fileEntry.SiaFile, uuc30Three.fileEntry.SiaFile}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Metadata().CombinedChunkOffset < files[j].Metadata().CombinedChunkOffset
	})
	// Check the offsets of the files.
	if files[0].Metadata().CombinedChunkOffset != 0 {
		t.Fatal("first file doesn't have correct offset")
	}
	if files[1].Metadata().CombinedChunkOffset != files[0].Size() {
		t.Fatal("second file doesn't have correct offset")
	}
	if files[2].Metadata().CombinedChunkOffset != files[0].Size()+files[1].Size() {
		t.Fatal("third file doesn't have correct offset")
	}
	// Check the lengths of the files.
	if files[0].Metadata().CombinedChunkLength != files[0].Size() {
		t.Fatal("first file doesn't have correct length")
	}
	if files[1].Metadata().CombinedChunkLength != files[1].Size() {
		t.Fatal("second file doesn't have correct length")
	}
	if files[2].Metadata().CombinedChunkLength != files[2].Size() {
		t.Fatal("third file doesn't have correct length")
	}
}
