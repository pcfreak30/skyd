package siafile

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/NebulousLabs/writeaheadlog"
)

// createLinkedBlankSiafile creates 2 SiaFiles which use the same SiaFile to
// store combined chunks. They reside within 'dir'.
func createLinkedBlankSiafiles(dir string) (*SiaFile, *SiaFile, error) {
	// Create a wal.
	walFilePath := filepath.Join(dir, "writeaheadlog.wal")
	_, wal, err := writeaheadlog.New(walFilePath)
	if err != nil {
		return nil, nil, err
	}
	// Get parameters for the files.
	_, _, source, rc, sk, fileSize, numChunks, fileMode := newTestFileParams(1, true)
	// Create a SiaFile for partial chunks.
	var partialsSiaFile *SiaFile
	partialsSiaPath := modules.CombinedSiaFilePath(rc)
	partialsSiaFilePath := partialsSiaPath.SiaPartialsFileSysPath(dir)
	if _, err = os.Stat(partialsSiaFilePath); os.IsNotExist(err) {
		partialsSiaFile, err = New(partialsSiaFilePath, "", wal, rc, sk, 0, fileMode, nil, false)
	} else {
		partialsSiaFile, err = LoadSiaFile(partialsSiaFilePath, wal)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load partialsSiaFile: %v", err)
	}
	partialsEntry := &SiaFileSetEntry{
		dummyEntry(partialsSiaFile),
		uint64(fastrand.Intn(math.MaxInt32)),
	}
	// Create the files.
	sf1Path := filepath.Join(dir, "sf1"+modules.SiaFileExtension)
	sf2Path := filepath.Join(dir, "sf2"+modules.SiaFileExtension)
	sf1, err := New(sf1Path, source, wal, rc, sk, fileSize, fileMode, partialsEntry, false)
	if err != nil {
		return nil, nil, err
	}
	sf2, err := New(sf2Path, source, wal, rc, sk, fileSize, fileMode, partialsEntry, false)
	if err != nil {
		return nil, nil, err
	}
	// Check that the number of chunks in the files are correct.
	if numChunks >= 0 && sf1.numChunks() != uint64(numChunks) {
		return nil, nil, errors.New("createLinkedBlankSiafiles didn't create the expected number of chunks")
	}
	if numChunks >= 0 && sf2.numChunks() != uint64(numChunks) {
		return nil, nil, errors.New("createLinkedBlankSiafiles didn't create the expected number of chunks")
	}
	if partialsSiaFile.numChunks() != 0 {
		return nil, nil, errors.New("createLinkedBlankSiafiles didn't create an empty partialsSiaFile")
	}
	return sf1, sf2, nil
}

// TestSetCombinedChunk tests the SetCombinedChunk method.
func TestSetCombinedChunk(t *testing.T) {
	// Create a testDir.
	dir := filepath.Join(os.TempDir(), t.Name())
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0600); err != nil {
		t.Fatal(err)
	}
	// Create 2 siafiles linked by using the same siafile for combined chunks.
	sf1, sf2, err := createLinkedBlankSiafiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	sf1PartialChunk := fastrand.Bytes(int(sf1.ChunkSize()) / 10) // 10% of a full chunk
	sf2PartialChunk := fastrand.Bytes(int(sf1.ChunkSize()) / 2)  // 50% of a full chunk
	ci1 := PartialChunkInfo{
		length: uint64(len(sf1PartialChunk)),
		offset: 0,
		sf:     sf1,
	}
	ci2 := PartialChunkInfo{
		length: uint64(len(sf2PartialChunk)),
		offset: uint64(len(sf1PartialChunk)),
		sf:     sf2,
	}
	// Save the partial chunks to have the state of the SiaFiles change.
	if err := sf1.SavePartialChunk(sf1PartialChunk); err != nil {
		t.Fatal(err)
	}
	if err := sf2.SavePartialChunk(sf2PartialChunk); err != nil {
		t.Fatal(err)
	}

	// Get random ID for the chunk + some random chunk data. Also create a
	// combinedChunk of the wrong size.
	combinedChunkID := hex.EncodeToString(fastrand.Bytes(16))
	combinedChunk := append(sf1PartialChunk, sf2PartialChunk...)

	// Try setting the chunk. Should fail since it's not exactly chunkSize large.
	cci := NewCombinedChunkInfo(combinedChunkID, combinedChunk, []PartialChunkInfo{ci1, ci2})
	err = SetCombinedChunk(cci, dir)
	if err == nil {
		t.Fatal("Should've failed")
	}
	// Add some padding to the chunk and try again. This should work.
	combinedChunk = append(combinedChunk, make([]byte, sf1.ChunkSize()-uint64(len(combinedChunk)))...)
	cci = NewCombinedChunkInfo(combinedChunkID, combinedChunk, []PartialChunkInfo{ci1, ci2})
	err = SetCombinedChunk(cci, dir)
	if err != nil {
		t.Fatal(err)
	}
	// Open the combined chunk.
	f, err := os.Open(filepath.Join(dir, combinedChunkID))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// The partial chunks should be in the right spots.
	offset, length := sf1.staticMetadata.CombinedChunkOffset, sf1.staticMetadata.CombinedChunkLength
	readChunk := make([]byte, length)
	if _, err := f.ReadAt(readChunk, int64(offset)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readChunk, sf1PartialChunk) {
		t.Fatal("Read chunk doesn't match sf1PartialChunk", len(readChunk), len(sf1PartialChunk))
	}
	offset, length = sf2.staticMetadata.CombinedChunkOffset, sf2.staticMetadata.CombinedChunkLength
	readChunk = make([]byte, length)
	if _, err := f.ReadAt(readChunk, int64(offset)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readChunk, sf2PartialChunk) {
		t.Fatal("Read chunk doesn't match sf2PartialChunk")
	}
	// The combined chunk should be exactly ChunkSize large.
	if fi, err := f.Stat(); err != nil || fi.Size() != int64(sf1.ChunkSize()) {
		t.Fatal("Failed to confirm correct filesize")
	}
}
