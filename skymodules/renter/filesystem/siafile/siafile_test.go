package siafile

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"

	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// randomChunk is a helper method for testing that creates a random chunk.
func randomChunk() chunk {
	numPieces := 30
	chunk := chunk{}
	chunk.Pieces = make([][]piece, numPieces)
	fastrand.Read(chunk.ExtensionInfo[:])

	// Add 0-3 pieces for each pieceIndex within the file.
	for pieceIndex := range chunk.Pieces {
		n := fastrand.Intn(4) // [0;3]
		// Create and add n pieces at pieceIndex i.
		for i := 0; i < n; i++ {
			var piece piece
			piece.HostTableOffset = uint32(fastrand.Intn(100))
			fastrand.Read(piece.MerkleRoot[:])
			chunk.Pieces[pieceIndex] = append(chunk.Pieces[pieceIndex], piece)
		}
	}
	return chunk
}

// randomPiece is a helper method for testing that creates a random piece.
func randomPiece() piece {
	var piece piece
	piece.HostTableOffset = uint32(fastrand.Intn(100))
	fastrand.Read(piece.MerkleRoot[:])
	return piece
}

// TestFileNumChunks checks the numChunks method of the file type.
func TestFileNumChunks(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	fileSize := func(numSectors uint64) uint64 {
		return numSectors*modules.SectorSize + uint64(fastrand.Intn(int(modules.SectorSize)))
	}
	// Since the pieceSize is 'random' now we test a variety of random inputs.
	tests := []struct {
		fileSize   uint64
		dataPieces int
	}{
		{fileSize(10), 10},
		{fileSize(50), 10},
		{fileSize(100), 10},

		{fileSize(11), 10},
		{fileSize(51), 10},
		{fileSize(101), 10},

		{fileSize(10), 100},
		{fileSize(50), 100},
		{fileSize(100), 100},

		{fileSize(11), 100},
		{fileSize(51), 100},
		{fileSize(101), 100},

		{0, 10}, // 0-length
	}

	for _, test := range tests {
		// Create erasure-coder
		rsc, _ := skymodules.NewRSCode(test.dataPieces, 1) // can't use 0
		// Create the file
		siaFilePath, _, source, _, sk, _, _, fileMode := newTestFileParams(1, true)
		f, _, _ := customTestFileAndWAL(siaFilePath, source, rsc, sk, test.fileSize, -1, fileMode)
		// Make sure the file reports the correct pieceSize.
		if f.PieceSize() != modules.SectorSize-f.MasterKey().Type().Overhead() {
			t.Fatal("file has wrong pieceSize for its encryption type")
		}
		// Check that the number of chunks matches the expected number.
		expectedNumChunks := test.fileSize / (f.PieceSize() * uint64(test.dataPieces))
		if expectedNumChunks == 0 && test.fileSize > 0 {
			// There is at least 1 chunk for non 0-byte files.
			expectedNumChunks = 1
		} else if expectedNumChunks%(f.PieceSize()*uint64(test.dataPieces)) != 0 {
			// If it doesn't divide evenly there will be 1 chunk padding.
			expectedNumChunks++
		}
		if f.NumChunks() != expectedNumChunks {
			t.Errorf("Test %v: expected %v, got %v", test, expectedNumChunks, f.NumChunks())
		}
		if err := ensureMetadataValid(f.Metadata()); err != nil {
			t.Fatal(err)
		}
	}
}

// TestFileRedundancy tests that redundancy is correctly calculated for files
// with varying number of filecontracts and erasure code settings.
func TestFileRedundancy(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	nDatas := []int{1, 2, 10}
	neverOffline := make(map[string]bool)
	goodForRenew := make(map[string]bool)
	for i := 0; i < 6; i++ {
		pk := types.SiaPublicKey{Key: []byte{byte(i)}}
		neverOffline[pk.String()] = false
		goodForRenew[pk.String()] = true
	}
	// Create a testDir.
	dir := filepath.Join(os.TempDir(), t.Name())
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0600); err != nil {
		t.Fatal(err)
	}

	for _, nData := range nDatas {
		rsc, _ := skymodules.NewRSCode(nData, 10)
		siaFilePath, _, source, _, sk, fileSize, numChunks, fileMode := newTestFileParamsWithRC(2, false, rsc)
		f, _, _ := customTestFileAndWAL(siaFilePath, source, rsc, sk, fileSize, numChunks, fileMode)
		// Test that an empty file has 0 redundancy.
		r, ur, err := f.Redundancy(neverOffline, goodForRenew)
		if err != nil {
			t.Fatal(err)
		}
		if r != 0 || ur != 0 {
			t.Error("expected 0 and 0 redundancy, got", r, ur)
		}
		// Test that a file with 1 host that has a piece for every chunk but
		// one chunk still has a redundancy of 0.
		for i := uint64(0); i < f.NumChunks()-1; i++ {
			err := f.AddPiece(types.SiaPublicKey{Key: []byte{byte(0)}}, i, 0, crypto.Hash{})
			if err != nil {
				t.Fatal(err)
			}
		}
		r, ur, err = f.Redundancy(neverOffline, goodForRenew)
		if err != nil {
			t.Fatal(err)
		}
		if r != 0 || ur != 0 {
			t.Error("expected 0 and 0 redundancy, got", r, ur)
		}
		// Test that adding another host with a piece for every chunk but one
		// chunk still results in a file with redundancy 0.
		for i := uint64(0); i < f.NumChunks()-1; i++ {
			err := f.AddPiece(types.SiaPublicKey{Key: []byte{byte(1)}}, i, 1, crypto.Hash{})
			if err != nil {
				t.Fatal(err)
			}
		}
		r, ur, err = f.Redundancy(neverOffline, goodForRenew)
		if err != nil {
			t.Fatal(err)
		}
		if r != 0 || ur != 0 {
			t.Error("expected 0 and 0 redundancy, got", r, ur)
		}
		// Test that adding a file contract with a piece for the missing chunk
		// results in a file with redundancy > 0 && <= 1.
		err = f.AddPiece(types.SiaPublicKey{Key: []byte{byte(2)}}, f.NumChunks()-1, 0, crypto.Hash{})
		if err != nil {
			t.Fatal(err)
		}
		// 1.0 / MinPieces because the chunk with the least number of pieces has 1 piece.
		expectedR := 1.0 / float64(f.ErasureCode().MinPieces())
		r, ur, err = f.Redundancy(neverOffline, goodForRenew)
		if err != nil {
			t.Fatal(err)
		}
		if r != expectedR || ur != expectedR {
			t.Errorf("expected %f redundancy, got %f %f", expectedR, r, ur)
		}
		// Test that adding a file contract that has erasureCode.MinPieces() pieces
		// per chunk for all chunks results in a file with redundancy > 1.
		for iChunk := uint64(0); iChunk < f.NumChunks(); iChunk++ {
			for iPiece := uint64(1); iPiece < uint64(f.ErasureCode().MinPieces()); iPiece++ {
				err := f.AddPiece(types.SiaPublicKey{Key: []byte{byte(3)}}, iChunk, iPiece, crypto.Hash{})
				if err != nil {
					t.Fatal(err)
				}
			}
			err := f.AddPiece(types.SiaPublicKey{Key: []byte{byte(4)}}, iChunk, uint64(f.ErasureCode().MinPieces()), crypto.Hash{})
			if err != nil {
				t.Fatal(err)
			}
		}
		// 1+MinPieces / MinPieces because the chunk with the least number of pieces has 1+MinPieces pieces.
		expectedR = float64(1+f.ErasureCode().MinPieces()) / float64(f.ErasureCode().MinPieces())
		r, ur, err = f.Redundancy(neverOffline, goodForRenew)
		if err != nil {
			t.Fatal(err)
		}
		if r != expectedR || ur != expectedR {
			t.Errorf("expected %f redundancy, got %f", expectedR, r)
		}

		// verify offline file contracts are not counted in the redundancy
		for iChunk := uint64(0); iChunk < f.NumChunks(); iChunk++ {
			for iPiece := uint64(0); iPiece < uint64(f.ErasureCode().MinPieces()); iPiece++ {
				err := f.AddPiece(types.SiaPublicKey{Key: []byte{byte(5)}}, iChunk, iPiece, crypto.Hash{})
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		specificOffline := make(map[string]bool)
		for pk := range goodForRenew {
			specificOffline[pk] = false
		}
		specificOffline[string(byte(5))] = true
		r, ur, err = f.Redundancy(specificOffline, goodForRenew)
		if err != nil {
			t.Fatal(err)
		}
		if r != expectedR || ur != expectedR {
			t.Errorf("expected redundancy to ignore offline file contracts, wanted %f got %f", expectedR, r)
		}
		if err := ensureMetadataValid(f.Metadata()); err != nil {
			t.Fatal(err)
		}
	}
}

// TestFileHealth tests that the health of the file is correctly calculated.
//
// Health is equal to (targetParityPieces - actualParityPieces)/targetParityPieces
func TestFileHealth(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a Zero byte file
	rsc, _ := skymodules.NewRSCode(10, 20)
	siaFilePath, _, source, _, sk, _, _, fileMode := newTestFileParams(1, true)
	zeroFile, _, _ := customTestFileAndWAL(siaFilePath, source, rsc, sk, 0, 0, fileMode)

	// Create offline map
	offlineMap := make(map[string]bool)
	goodForRenewMap := make(map[string]bool)

	// Confirm the health is correct
	health, stuckHealth, userHealth, userStuckHealth, numStuckChunks, repairBytes, stuckBytes := zeroFile.Health(offlineMap, goodForRenewMap)
	if health != 0 {
		t.Error("Expected health to be 0 but was", health)
	}
	if stuckHealth != 0 {
		t.Error("Expected stuck health to be 0 but was", stuckHealth)
	}
	if userHealth != 0 {
		t.Error("Expected userHealth to be 0 but was", health)
	}
	if userStuckHealth != 0 {
		t.Error("Expected user stuck health to be 0 but was", stuckHealth)
	}
	if numStuckChunks != 0 {
		t.Error("Expected no stuck chunks but found", numStuckChunks)
	}
	if repairBytes != 0 {
		t.Errorf("Repair Bytes of file not as expected, got %v expected %v", repairBytes, 0)
	}
	if stuckBytes != 0 {
		t.Errorf("Stuck Bytes of file not as expected, got %v expected %v", stuckBytes, 0)
	}

	// Create File with 1 chunk
	siaFilePath, _, source, _, sk, _, _, fileMode = newTestFileParams(1, true)
	f, _, _ := customTestFileAndWAL(siaFilePath, source, rsc, sk, 100, 1, fileMode)

	// Check file health, since there are no pieces in the chunk yet no good
	// pieces will be found resulting in a health of 1.5 with the erasure code
	// settings of 10/30. Since there are no stuck chunks the stuckHealth of the
	// file should be 0
	//
	// 1 - ((0 - 10) / 20)
	health, stuckHealth, _, _, _, repairBytes, stuckBytes = f.Health(offlineMap, goodForRenewMap)
	if health != 1.5 {
		t.Errorf("Health of file not as expected, got %v expected 1.5", health)
	}
	if stuckHealth != float64(0) {
		t.Errorf("Stuck Health of file not as expected, got %v expected 0", stuckHealth)
	}
	expected := uint64(rsc.NumPieces()) * modules.SectorSize
	if repairBytes != expected {
		t.Errorf("Repair Bytes of file not as expected, got %v expected %v", repairBytes, expected)
	}
	if stuckBytes != 0 {
		t.Errorf("Stuck Bytes of file not as expected, got %v expected %v", stuckBytes, 0)
	}

	for i := 0; i < 2; i++ {
		spk := types.SiaPublicKey{Algorithm: types.SignatureEd25519, Key: []byte{byte(i)}}
		offlineMap[spk.String()] = false
		goodForRenewMap[spk.String()] = true
		if err := f.AddPiece(spk, 0, 0, crypto.Hash{}); err != nil {
			t.Fatal(err)
		}
	}

	// Check health, even though two pieces were added the health should be 1.45
	// since the two good pieces were added to the same pieceSet
	//
	// 1 - ((1 - 10) / 20)
	health, _, _, _, _, repairBytes, stuckBytes = f.Health(offlineMap, goodForRenewMap)
	if health != 1.45 {
		t.Fatalf("Health of file not as expected, got %v expected 1.45", health)
	}
	expected = uint64(rsc.NumPieces()-1) * modules.SectorSize
	if repairBytes != expected {
		t.Errorf("Repair Bytes of file not as expected, got %v expected %v", repairBytes, expected)
	}
	if stuckBytes != 0 {
		t.Errorf("Stuck Bytes of file not as expected, got %v expected %v", stuckBytes, 0)
	}

	// Add one good pieces to second piece set, confirm health is now 1.40.
	spk := types.SiaPublicKey{Algorithm: types.SignatureEd25519, Key: []byte{0}}
	offlineMap[spk.String()] = false
	goodForRenewMap[spk.String()] = true
	if err := f.AddPiece(spk, 0, 1, crypto.Hash{}); err != nil {
		t.Fatal(err)
	}
	health, _, _, _, _, repairBytes, stuckBytes = f.Health(offlineMap, goodForRenewMap)
	if health != 1.40 {
		t.Fatalf("Health of file not as expected, got %v expected 1.40", health)
	}
	expected = uint64(rsc.NumPieces()-2) * modules.SectorSize
	if repairBytes != expected {
		t.Errorf("Repair Bytes of file not as expected, got %v expected %v", repairBytes, expected)
	}
	if stuckBytes != 0 {
		t.Errorf("Stuck Bytes of file not as expected, got %v expected %v", stuckBytes, 0)
	}

	// Add another good pieces to second piece set, confirm health is still 1.40.
	spk = types.SiaPublicKey{Algorithm: types.SignatureEd25519, Key: []byte{1}}
	offlineMap[spk.String()] = false
	goodForRenewMap[spk.String()] = true
	if err := f.AddPiece(spk, 0, 1, crypto.Hash{}); err != nil {
		t.Fatal(err)
	}
	health, _, _, _, _, repairBytes, stuckBytes = f.Health(offlineMap, goodForRenewMap)
	if health != 1.40 {
		t.Fatalf("Health of file not as expected, got %v expected 1.40", health)
	}
	expected = uint64(rsc.NumPieces()-2) * modules.SectorSize
	if repairBytes != expected {
		t.Errorf("Repair Bytes of file not as expected, got %v expected %v", repairBytes, expected)
	}
	if stuckBytes != 0 {
		t.Errorf("Stuck Bytes of file not as expected, got %v expected %v", stuckBytes, 0)
	}

	// Mark chunk as stuck
	err := f.SetStuck(0, true)
	if err != nil {
		t.Fatal(err)
	}
	health, stuckHealth, _, _, numStuckChunks, repairBytes, stuckBytes = f.Health(offlineMap, goodForRenewMap)
	// Health should now be 0 since there are no unstuck chunks
	if health != 0 {
		t.Fatalf("Health of file not as expected, got %v expected 0", health)
	}
	// Stuck Health should now be 1.4
	if stuckHealth != 1.40 {
		t.Fatalf("Stuck Health of file not as expected, got %v expected 1.40", stuckHealth)
	}
	// There should be 1 stuck chunk
	if numStuckChunks != 1 {
		t.Fatalf("Expected 1 stuck chunk but found %v", numStuckChunks)
	}
	expected = uint64(rsc.NumPieces()-2) * modules.SectorSize
	if repairBytes != 0 {
		t.Errorf("Repair Bytes of file not as expected, got %v expected %v", repairBytes, 0)
	}
	if stuckBytes != expected {
		t.Errorf("Stuck Bytes of file not as expected, got %v expected %v", stuckBytes, expected)
	}

	// Add good pieces until the file health is below the RepairThreshold
	thresholdPieces := rsc.NumPieces() - 1
	for i := 0; i < thresholdPieces; i++ {
		spk := types.SiaPublicKey{Algorithm: types.SignatureEd25519, Key: []byte{byte(i)}}
		offlineMap[spk.String()] = false
		goodForRenewMap[spk.String()] = true
		if err := f.AddPiece(spk, 0, uint64(i), crypto.Hash{}); err != nil {
			t.Fatal(err)
		}
	}

	// Health should still be 0 since there are no unstuck chunks
	health, stuckHealth, _, _, numStuckChunks, repairBytes, stuckBytes = f.Health(offlineMap, goodForRenewMap)
	if health != 0 {
		t.Fatalf("Health of file not as expected, got %v expected 0", health)
	}
	// Stuck Health should now be 0.05
	if stuckHealth != 0.05 {
		t.Fatalf("Stuck Health of file not as expected, got %v expected 0.05", stuckHealth)
	}
	// There should be 1 stuck chunk
	if numStuckChunks != 1 {
		t.Fatalf("Expected 1 stuck chunk but found %v", numStuckChunks)
	}
	// There should be no repair bytes
	if repairBytes != 0 {
		t.Errorf("Repair Bytes of file not as expected, got %v expected %v", repairBytes, 0)
	}
	// There should be stuck bytes
	expected = uint64(rsc.NumPieces()-thresholdPieces) * modules.SectorSize
	if stuckBytes != expected {
		t.Errorf("Stuck Bytes of file not as expected, got %v expected %v", stuckBytes, expected)
	}

	// Mark as not stuck
	err = f.SetStuck(0, false)
	if err != nil {
		t.Fatal(err)
	}

	// Health should still be 0.05 now
	health, stuckHealth, _, _, numStuckChunks, repairBytes, stuckBytes = f.Health(offlineMap, goodForRenewMap)
	if health != 0.05 {
		t.Fatalf("Health of file not as expected, got %v expected 0.05", health)
	}
	// Stuck Health should now be 0
	if stuckHealth != 0 {
		t.Fatalf("Stuck Health of file not as expected, got %v expected 0", stuckHealth)
	}
	// There should be 0 stuck chunks
	if numStuckChunks != 0 {
		t.Fatalf("Expected 0 stuck chunks but found %v", numStuckChunks)
	}
	// There should be no repair bytes
	if repairBytes != 0 {
		t.Errorf("Repair Bytes of file not as expected, got %v expected %v", repairBytes, 0)
	}
	// There should be no stuck bytes
	if stuckBytes != 0 {
		t.Errorf("Stuck Bytes of file not as expected, got %v expected %v", stuckBytes, 0)
	}

	// Create File with 2 chunks
	siaFilePath, _, source, _, sk, _, _, fileMode = newTestFileParams(1, true)
	f, _, _ = customTestFileAndWAL(siaFilePath, source, rsc, sk, 5e4, 2, fileMode)
	if err != nil {
		t.Fatal(err)
	}

	// Create offline map
	offlineMap = make(map[string]bool)
	goodForRenewMap = make(map[string]bool)

	// Check file health, since there are no pieces in the chunk yet no good
	// pieces will be found resulting in a health of 1.5
	health, _, _, _, _, repairBytes, stuckBytes = f.Health(offlineMap, goodForRenewMap)
	if health != 1.5 {
		t.Fatalf("Health of file not as expected, got %v expected 1.5", health)
	}
	firstRepair := uint64(rsc.NumPieces()) * modules.SectorSize
	secondRepair := uint64(rsc.NumPieces()) * modules.SectorSize
	expected = firstRepair + secondRepair
	if repairBytes != expected {
		t.Errorf("Repair Bytes of file not as expected, got %v expected %v", repairBytes, expected)
	}
	if stuckBytes != 0 {
		t.Errorf("Stuck Bytes of file not as expected, got %v expected %v", stuckBytes, 0)
	}

	// Add good pieces to the first chunk
	for i := 0; i < 4; i++ {
		spk := types.SiaPublicKey{Algorithm: types.SignatureEd25519, Key: []byte{byte(i)}}
		offlineMap[spk.String()] = false
		goodForRenewMap[spk.String()] = true
		if err := f.AddPiece(spk, 0, uint64(i%2), crypto.Hash{}); err != nil {
			t.Fatal(err)
		}
	}

	// Check health, should still be 1.5 because other chunk doesn't have any
	// good pieces
	health, stuckHealth, _, _, _, repairBytes, stuckBytes = f.Health(offlineMap, goodForRenewMap)
	if health != 1.5 {
		t.Fatalf("Health of file not as expected, got %v expected 1.5", health)
	}
	firstRepair = uint64(rsc.NumPieces()-2) * modules.SectorSize
	secondRepair = uint64(rsc.NumPieces()) * modules.SectorSize
	expected = firstRepair + secondRepair
	if repairBytes != expected {
		t.Errorf("Repair Bytes of file not as expected, got %v expected %v", repairBytes, expected)
	}
	if stuckBytes != 0 {
		t.Errorf("Stuck Bytes of file not as expected, got %v expected %v", stuckBytes, 0)
	}

	for i := 0; i < 4; i++ {
		spk := types.SiaPublicKey{Algorithm: types.SignatureEd25519, Key: []byte{byte(i)}}
		offlineMap[spk.String()] = false
		goodForRenewMap[spk.String()] = true
		if err := f.AddPiece(spk, 1, uint64(i%2), crypto.Hash{}); err != nil {
			t.Fatal(err)
		}
	}
	health, _, _, _, _, repairBytes, stuckBytes = f.Health(offlineMap, goodForRenewMap)
	if health != 1.40 {
		t.Fatalf("Health of file not as expected, got %v expected 1.40", health)
	}
	firstRepair = uint64(rsc.NumPieces()-2) * modules.SectorSize
	secondRepair = uint64(rsc.NumPieces()-2) * modules.SectorSize
	expected = firstRepair + secondRepair
	if repairBytes != expected {
		t.Errorf("Repair Bytes of file not as expected, got %v expected %v", repairBytes, expected)
	}
	if stuckBytes != 0 {
		t.Errorf("Stuck Bytes of file not as expected, got %v expected %v", stuckBytes, 0)
	}

	// Mark second chunk as stuck
	err = f.SetStuck(1, true)
	if err != nil {
		t.Fatal(err)
	}
	health, stuckHealth, _, _, numStuckChunks, repairBytes, stuckBytes = f.Health(offlineMap, goodForRenewMap)
	// Since both chunks have the same health, the file health and the file stuck health should be the same
	if health != 1.40 {
		t.Fatalf("Health of file not as expected, got %v expected 1.40", health)
	}
	if stuckHealth != 1.40 {
		t.Fatalf("Stuck Health of file not as expected, got %v expected 1.4", stuckHealth)
	}
	// Check health, verify there is 1 stuck chunk
	if numStuckChunks != 1 {
		t.Fatalf("Expected 1 stuck chunk but found %v", numStuckChunks)
	}
	if err := ensureMetadataValid(f.Metadata()); err != nil {
		t.Fatal(err)
	}
	firstRepair = uint64(rsc.NumPieces()-2) * modules.SectorSize
	secondRepair = uint64(rsc.NumPieces()-2) * modules.SectorSize
	if repairBytes != firstRepair {
		t.Errorf("Repair Bytes of file not as expected, got %v expected %v", repairBytes, firstRepair)
	}
	if stuckBytes != secondRepair {
		t.Errorf("Stuck Bytes of file not as expected, got %v expected %v", stuckBytes, secondRepair)
	}
}

// TestShrink is a unit test for the Shrink method.
func TestShrink(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Declare a check method.
	checkFile := func(sf *SiaFile, numChunks, size, diskSize uint64) error {
		if numChunks != sf.NumChunks() {
			return fmt.Errorf("Expected %v chunks but was %v", numChunks, sf.NumChunks())
		}
		if size != sf.Size() {
			return fmt.Errorf("Expected size to be %v but was %v", size, sf.Size())
		}
		fi, err := os.Stat(sf.siaFilePath)
		if err != nil {
			return err
		}
		if fi.Size() != int64(diskSize) {
			return fmt.Errorf("Expected diskSize to be %v but was %v", diskSize, fi.Size())
		}
		return nil
	}

	// Create a siafile with 3 chunks.
	siaFilePath, _, source, rc, sk, _, _, fileMode := newTestFileParams(1, false)
	numChunks := 3
	chunkSize := skymodules.ChunkSize(crypto.TypeDefaultRenter, uint64(rc.MinPieces()))
	fileSize := chunkSize*uint64(numChunks) - 1 // last chunk is partial
	sf, _, _ := customTestFileAndWAL(siaFilePath, source, rc, sk, fileSize, numChunks, fileMode)

	// Check initial file. The initial size is 3 full pages + a marshaled
	// piece. One page for the metadata, 2 for the first 2 chunks and the
	// empty chunk at the end.
	if err := checkFile(sf, 3, fileSize, 3*pageSize+uint64(marshaledChunkSize(0))); err != nil {
		t.Fatal(err)
	}

	// Shrink to 2 chunks.
	err := sf.Shrink(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkFile(sf, 2, 2*chunkSize, 3*pageSize); err != nil {
		t.Fatal(err)
	}

	// Shrink to 0 chunks.
	err = sf.Shrink(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkFile(sf, 0, 0, pageSize); err != nil {
		t.Fatal(err)
	}

	// Shrink to 1 chunk. Should fail.
	err = sf.Shrink(1)
	if !errors.Contains(err, errShrinkWithTooManyChunks) {
		t.Fatal("growing the file should fail", err)
	}
	if err := checkFile(sf, 0, 0, pageSize); err != nil {
		t.Fatal(err)
	}
}

// TestGrowNumChunks is a unit test for the SiaFile's GrowNumChunks method.
func TestGrowNumChunks(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a blank file.
	siaFilePath, _, source, rc, sk, fileSize, numChunks, fileMode := newTestFileParams(1, false)
	sf, wal, _ := customTestFileAndWAL(siaFilePath, source, rc, sk, fileSize, numChunks, fileMode)
	expectedChunks := sf.NumChunks()
	expectedSize := sf.Size()

	// Declare a check method.
	checkFile := func(sf *SiaFile, numChunks, size uint64) {
		if numChunks != sf.NumChunks() {
			t.Fatalf("Expected %v chunks but was %v", numChunks, sf.NumChunks())
		}
		if size != sf.Size() {
			t.Fatalf("Expected size to be %v but was %v", size, sf.Size())
		}
	}

	// Increase the size of the file by 1 chunk.
	expectedChunks++
	expectedSize += sf.ChunkSize()
	err := sf.GrowNumChunks(expectedChunks)
	if err != nil {
		t.Fatal(err)
	}
	// Check the file after growing the chunks.
	checkFile(sf, expectedChunks, expectedSize)
	// Load the file from disk again to also check that persistence works.
	sf, err = LoadSiaFile(sf.siaFilePath, wal)
	if err != nil {
		t.Fatal(err)
	}
	// Check that size and chunks still match.
	checkFile(sf, expectedChunks, expectedSize)

	// Call GrowNumChunks with the same argument again. This should be a no-op.
	err = sf.GrowNumChunks(expectedChunks)
	if err != nil {
		t.Fatal(err)
	}
	// Check the file after growing the chunks.
	checkFile(sf, expectedChunks, expectedSize)
	// Load the file from disk again to also check that no wrong persistence
	// happened.
	sf, err = LoadSiaFile(sf.siaFilePath, wal)
	if err != nil {
		t.Fatal(err)
	}
	// Check that size and chunks still match.
	checkFile(sf, expectedChunks, expectedSize)

	// Grow the file by 2 chunks to see if multiple chunks also work.
	expectedChunks += 2
	expectedSize += 2 * sf.ChunkSize()
	err = sf.GrowNumChunks(expectedChunks)
	if err != nil {
		t.Fatal(err)
	}
	// Check the file after growing the chunks.
	checkFile(sf, expectedChunks, expectedSize)
	// Load the file from disk again to also check that persistence works.
	sf, err = LoadSiaFile(sf.siaFilePath, wal)
	if err != nil {
		t.Fatal(err)
	}
	// Check that size and chunks still match.
	checkFile(sf, expectedChunks, expectedSize)
	if err := ensureMetadataValid(sf.Metadata()); err != nil {
		t.Fatal(err)
	}
}

// TestPruneHosts is a unit test for the pruneHosts method.
func TestPruneHosts(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a siafile without partial chunk since partial chunk.
	siaFilePath, _, source, rc, sk, fileSize, numChunks, fileMode := newTestFileParams(1, false)
	sf, _, _ := customTestFileAndWAL(siaFilePath, source, rc, sk, fileSize, numChunks, fileMode)

	// Add 3 random hostkeys to the file.
	sf.addRandomHostKeys(3)

	// Save changes to disk.
	updates, err := sf.saveHeaderUpdates()
	if err != nil {
		t.Fatal(err)
	}
	if err := sf.createAndApplyTransaction(updates...); err != nil {
		t.Fatal(err)
	}

	// Add one piece for every host to every pieceSet of the
	for _, hk := range sf.HostPublicKeys() {
		err := sf.iterateChunksReadonly(func(chunk chunk) error {
			for pieceIndex := range chunk.Pieces {
				if err := sf.AddPiece(hk, uint64(chunk.Index), uint64(pieceIndex), crypto.Hash{}); err != nil {
					t.Fatal(err)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Mark hostkeys 0 and 2 as unused.
	sf.pubKeyTable[0].Used = false
	sf.pubKeyTable[2].Used = false
	remainingKey := sf.pubKeyTable[1]

	// Prune the file.
	updates, err = sf.pruneHosts()
	if err != nil {
		t.Fatal(err)
	}
	if err := sf.createAndApplyTransaction(updates...); err != nil {
		t.Fatal(err)
	}

	// Check that there is only a single key left.
	if len(sf.pubKeyTable) != 1 {
		t.Fatalf("There should only be 1 key left but was %v", len(sf.pubKeyTable))
	}
	// The last key should be the correct one.
	if !reflect.DeepEqual(remainingKey, sf.pubKeyTable[0]) {
		t.Fatal("Remaining key doesn't match")
	}
	// Loop over all the pieces and make sure that the pieces with missing
	// hosts were pruned and that the remaining pieces have the correct offset
	// now.
	err = sf.iterateChunksReadonly(func(chunk chunk) error {
		for _, pieceSet := range chunk.Pieces {
			if len(pieceSet) != 1 {
				t.Fatalf("Expected 1 piece in the set but was %v", len(pieceSet))
			}
			// The HostTableOffset should always be 0 since the keys at index 0
			// and 2 were pruned which means that index 1 is now index 0.
			for _, piece := range pieceSet {
				if piece.HostTableOffset != 0 {
					t.Fatalf("HostTableOffset should be 0 but was %v", piece.HostTableOffset)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureMetadataValid(sf.Metadata()); err != nil {
		t.Fatal(err)
	}
}

// TestNumPieces tests the chunk's numPieces method.
func TestNumPieces(t *testing.T) {
	// create a random chunk.
	chunk := randomChunk()

	// get the number of pieces of the chunk.
	totalPieces := 0
	for _, pieceSet := range chunk.Pieces {
		totalPieces += len(pieceSet)
	}

	// compare it to the one reported by numPieces.
	if totalPieces != chunk.numPieces() {
		t.Fatalf("Expected %v pieces but was %v", totalPieces, chunk.numPieces())
	}
}

// TestDefragChunk tests if the defragChunk methods correctly prunes pieces
// from a chunk.
func TestDefragChunk(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	// Get a blank
	sf, _, _ := newBlankTestFileAndWAL(2) // make sure we have 1 full chunk at the beginning of sf.fullChunks

	// Use the first chunk of the file for testing.
	chunk, err := sf.chunk(0)
	if err != nil {
		t.Fatal(err)
	}

	// Add 100 pieces to each set of pieces, all belonging to the same unused
	// host.
	sf.pubKeyTable = append(sf.pubKeyTable, HostPublicKey{Used: false})
	for i := range chunk.Pieces {
		for j := 0; j < 100; j++ {
			chunk.Pieces[i] = append(chunk.Pieces[i], piece{HostTableOffset: 0})
		}
	}

	// Defrag the chunk. This should remove all the pieces since the host is
	// unused.
	sf.defragChunk(&chunk)
	if chunk.numPieces() != 0 {
		t.Fatalf("chunk should have 0 pieces after defrag but was %v", chunk.numPieces())
	}

	// Do the same thing again, but this time the host is marked as used.
	sf.pubKeyTable[0].Used = true
	for i := range chunk.Pieces {
		for j := 0; j < 100; j++ {
			chunk.Pieces[i] = append(chunk.Pieces[i], piece{HostTableOffset: 0})
		}
	}

	// Defrag the chunk.
	maxChunkSize := int64(sf.staticMetadata.StaticPagesPerChunk) * pageSize
	maxPieces := (maxChunkSize - marshaledChunkOverhead) / marshaledPieceSize
	maxPiecesPerSet := maxPieces / int64(len(chunk.Pieces))
	sf.defragChunk(&chunk)

	// The chunk should be smaller than maxChunkSize.
	if chunkSize := marshaledChunkSize(chunk.numPieces()); chunkSize > maxChunkSize {
		t.Errorf("chunkSize is too big %v > %v", chunkSize, maxChunkSize)
	}
	// The chunk should have less than maxPieces pieces.
	if int64(chunk.numPieces()) > maxPieces {
		t.Errorf("chunk should have <= %v pieces after defrag but was %v",
			maxPieces, chunk.numPieces())
	}
	// The chunk should have numPieces * maxPiecesPerSet pieces.
	if expectedPieces := int64(sf.ErasureCode().NumPieces()) * maxPiecesPerSet; expectedPieces != int64(chunk.numPieces()) {
		t.Errorf("chunk should have %v pieces but was %v", expectedPieces, chunk.numPieces())
	}
	// Every set of pieces should have maxPiecesPerSet pieces.
	for i, pieceSet := range chunk.Pieces {
		if int64(len(pieceSet)) != maxPiecesPerSet {
			t.Errorf("pieceSet%v length is %v which is greater than %v",
				i, len(pieceSet), maxPiecesPerSet)
		}
	}

	// Create a new file with 2 used hosts and 1 unused one. This file should
	// use 2 pages per chunk.
	sf, _, _ = newBlankTestFileAndWAL(2) // make sure we have 1 full chunk at the beginning of the file.
	sf.staticMetadata.StaticPagesPerChunk = 2
	sf.pubKeyTable = append(sf.pubKeyTable, HostPublicKey{Used: true})
	sf.pubKeyTable = append(sf.pubKeyTable, HostPublicKey{Used: true})
	sf.pubKeyTable = append(sf.pubKeyTable, HostPublicKey{Used: false})
	sf.pubKeyTable[0].PublicKey.Key = fastrand.Bytes(crypto.EntropySize)
	sf.pubKeyTable[1].PublicKey.Key = fastrand.Bytes(crypto.EntropySize)
	sf.pubKeyTable[2].PublicKey.Key = fastrand.Bytes(crypto.EntropySize)

	// Save the above changes to disk to avoid failing sanity checks when
	// calling AddPiece.
	updates, err := sf.saveHeaderUpdates()
	if err != nil {
		t.Fatal(err)
	}
	if err := sf.createAndApplyTransaction(updates...); err != nil {
		t.Fatal(err)
	}

	// Add 500 pieces to the first chunk of the file, randomly belonging to
	// any of the 3 hosts. This should never produce an error.
	var duration time.Duration
	for i := 0; i < 50; i++ {
		chunk, err := sf.chunk(0)
		if err != nil {
			t.Fatal(err)
		}
		pk := sf.pubKeyTable[fastrand.Intn(len(sf.pubKeyTable))].PublicKey
		pieceIndex := fastrand.Intn(len(chunk.Pieces))
		before := time.Now()
		if err := sf.AddPiece(pk, 0, uint64(pieceIndex), crypto.Hash{}); err != nil {
			t.Fatal(err)
		}
		duration += time.Since(before)
	}

	// Save the file to disk again to make sure cached fields are persisted.
	updates, err = sf.saveHeaderUpdates()
	if err != nil {
		t.Fatal(err)
	}
	if err := sf.createAndApplyTransaction(updates...); err != nil {
		t.Fatal(err)
	}

	// Finally load the file from disk again and compare it to the original.
	sf2, err := LoadSiaFile(sf.siaFilePath, sf.wal)
	if err != nil {
		t.Fatal(err)
	}
	// Compare the files.
	if err := equalFiles(sf, sf2); err != nil {
		t.Fatal(err)
	}
	if err := ensureMetadataValid(sf.Metadata()); err != nil {
		t.Fatal(err)
	}
	if err := ensureMetadataValid(sf2.Metadata()); err != nil {
		t.Fatal(err)
	}
}

// TestChunkHealth probes the chunkHealth method
func TestChunkHealth(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	// Get a blank siafile with at least 3 chunks.
	sf, _, _ := newBlankTestFileAndWAL(3)
	rc := sf.ErasureCode()

	// Create offline map
	offlineMap := make(map[string]bool)
	goodForRenewMap := make(map[string]bool)

	// Check and Record file health of initialized file
	fileHealth, _, _, _, _, repairBytes, stuckBytes := sf.Health(offlineMap, goodForRenewMap)
	initHealth := float64(1) - (float64(0-rc.MinPieces()) / float64(rc.NumPieces()-rc.MinPieces()))
	if fileHealth != initHealth {
		t.Fatalf("Expected file to be %v, got %v", initHealth, fileHealth)
	}
	expectedChunkRepairBytes := uint64(rc.NumPieces()) * modules.SectorSize
	expectedFileRepairBytes := sf.NumChunks() * expectedChunkRepairBytes
	if repairBytes != expectedFileRepairBytes {
		t.Errorf("Expected repairBytes to be %v, got %v", expectedFileRepairBytes, repairBytes)
	}
	if stuckBytes != 0 {
		t.Errorf("Expected stuckBytes to be %v, got %v", 0, stuckBytes)
	}

	// Since we are using a pre set offlineMap, all the chunks should have the
	// same health as the file
	err := sf.iterateChunksReadonly(func(chunk chunk) error {
		chunkHealth, _, repairBytes, err := sf.chunkHealth(chunk, offlineMap, goodForRenewMap)
		if err != nil {
			return err
		}
		if chunkHealth != fileHealth {
			t.Log("ChunkHealth:", chunkHealth)
			t.Log("FileHealth:", fileHealth)
			t.Fatal("Expected file and chunk to have same health")
		}
		if repairBytes != expectedChunkRepairBytes {
			return fmt.Errorf("Expected repairBytes to be %v, got %v", expectedChunkRepairBytes, repairBytes)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Add good piece to first chunk
	spk := types.SiaPublicKey{Algorithm: types.SignatureEd25519, Key: []byte{1}}
	offlineMap[spk.String()] = false
	goodForRenewMap[spk.String()] = true
	if err := sf.AddPiece(spk, 0, 0, crypto.Hash{}); err != nil {
		t.Fatal(err)
	}

	// Chunk at index 0 should now have a health of 1 higher than before
	chunk, err := sf.chunk(0)
	if err != nil {
		t.Fatal(err)
	}
	newHealth := float64(1) - (float64(1-rc.MinPieces()) / float64(rc.NumPieces()-rc.MinPieces()))
	ch, _, repairBytes, err := sf.chunkHealth(chunk, offlineMap, goodForRenewMap)
	if err != nil {
		t.Fatal(err)
	}
	if ch != newHealth {
		t.Fatalf("Expected chunk health to be %v, got %v", newHealth, ch)
	}
	expectedChunkRepairBytes = uint64(rc.NumPieces()-1) * modules.SectorSize
	if repairBytes != expectedChunkRepairBytes {
		t.Errorf("Expected repairBytes to be %v, got %v", expectedChunkRepairBytes, repairBytes)
	}

	// Chunk at index 1 should still have lower health
	chunk, err = sf.chunk(1)
	if err != nil {
		t.Fatal(err)
	}
	ch, _, repairBytes, err = sf.chunkHealth(chunk, offlineMap, goodForRenewMap)
	if err != nil {
		t.Fatal(err)
	}
	if ch != fileHealth {
		t.Fatalf("Expected chunk health to be %v, got %v", fileHealth, ch)
	}
	expectedChunkRepairBytes = uint64(rc.NumPieces()) * modules.SectorSize
	if repairBytes != expectedChunkRepairBytes {
		t.Errorf("Expected repairBytes to be %v, got %v", expectedChunkRepairBytes, repairBytes)
	}

	// Add good piece to second chunk
	spk = types.SiaPublicKey{Algorithm: types.SignatureEd25519, Key: []byte{2}}
	offlineMap[spk.String()] = false
	goodForRenewMap[spk.String()] = true
	if err := sf.AddPiece(spk, 1, 0, crypto.Hash{}); err != nil {
		t.Fatal(err)
	}

	// Chunk at index 1 should now have a health of 1 higher than before
	chunk, err = sf.chunk(1)
	if err != nil {
		t.Fatal(err)
	}
	ch, _, repairBytes, err = sf.chunkHealth(chunk, offlineMap, goodForRenewMap)
	if err != nil {
		t.Fatal(err)
	}
	if ch != newHealth {
		t.Fatalf("Expected chunk health to be %v, got %v", newHealth, ch)
	}
	expectedChunkRepairBytes = uint64(rc.NumPieces()-1) * modules.SectorSize
	if repairBytes != expectedChunkRepairBytes {
		t.Errorf("Expected repairBytes to be %v, got %v", expectedChunkRepairBytes, repairBytes)
	}

	// Mark Chunk at index 1 as stuck and confirm that doesn't impact the result
	// of chunkHealth
	if err := sf.SetStuck(1, true); err != nil {
		t.Fatal(err)
	}
	chunk, err = sf.chunk(1)
	if err != nil {
		t.Fatal(err)
	}
	ch, _, repairBytes, err = sf.chunkHealth(chunk, offlineMap, goodForRenewMap)
	if err != nil {
		t.Fatal(err)
	}
	if ch != newHealth {
		t.Fatalf("Expected file to be %v, got %v", newHealth, ch)
	}
	if err := ensureMetadataValid(sf.Metadata()); err != nil {
		t.Fatal(err)
	}
	if repairBytes != expectedChunkRepairBytes {
		t.Errorf("Expected repairBytes to be %v, got %v", expectedChunkRepairBytes, repairBytes)
	}
}

// TestStuckChunks checks to make sure the NumStuckChunks return the expected
// values and that the stuck chunks are persisted properly
func TestStuckChunks(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create siafile
	sf := newTestFile()

	// Mark every other chunk as stuck
	expectedStuckChunks := 0
	for chunkIndex := 0; chunkIndex < sf.numChunks; chunkIndex++ {
		if (chunkIndex % 2) != 0 {
			continue
		}
		if err := sf.SetStuck(uint64(chunkIndex), true); err != nil {
			t.Fatal(err)
		}
		expectedStuckChunks++
	}

	// Check that the total number of stuck chunks is consistent
	numStuckChunks := sf.NumStuckChunks()
	if numStuckChunks != uint64(expectedStuckChunks) {
		t.Fatalf("Wrong number of stuck chunks, got %v expected %v", numStuckChunks, expectedStuckChunks)
	}

	// Load siafile from disk
	sf, err := LoadSiaFile(sf.SiaFilePath(), sf.wal)
	if err != nil {
		t.Fatal(err)
	}

	// Check that the total number of stuck chunks is consistent
	if numStuckChunks != sf.NumStuckChunks() {
		t.Fatalf("Wrong number of stuck chunks, got %v expected %v", numStuckChunks, sf.NumStuckChunks())
	}

	// Check chunks and Stuck Chunk Table
	err = sf.iterateChunksReadonly(func(chunk chunk) error {
		if chunk.Index%2 != 0 {
			if chunk.Stuck {
				t.Fatal("Found stuck chunk when un-stuck chunk was expected")
			}
			return nil
		}
		if !chunk.Stuck {
			t.Fatal("Found un-stuck chunk when stuck chunk was expected")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureMetadataValid(sf.Metadata()); err != nil {
		t.Fatal(err)
	}
}

// TestUploadedBytes tests that uploadedBytes() returns the expected values for
// total and unique uploaded bytes.
func TestUploadedBytes(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	// Create a new blank test file
	f := newBlankTestFile()
	// Add multiple pieces to the first pieceSet of the first piece of the first
	// chunk
	for i := 0; i < 4; i++ {
		err := f.AddPiece(types.SiaPublicKey{}, uint64(0), 0, crypto.Hash{})
		if err != nil {
			t.Fatal(err)
		}
	}
	totalBytes, uniqueBytes, err := f.uploadedBytes()
	if err != nil {
		t.Fatal(err)
	}
	if totalBytes != 4*modules.SectorSize {
		t.Errorf("expected totalBytes to be %v, got %v", 4*modules.SectorSize, totalBytes)
	}
	if uniqueBytes != modules.SectorSize {
		t.Errorf("expected uniqueBytes to be %v, got %v", modules.SectorSize, uniqueBytes)
	}
	if err := ensureMetadataValid(f.Metadata()); err != nil {
		t.Fatal(err)
	}
}

// TestFileUploadProgressPinning verifies that uploadProgress() returns at most
// 100%, even if more pieces have been uploaded,
func TestFileUploadProgressPinning(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	f := newBlankTestFile()

	for chunkIndex := uint64(0); chunkIndex < f.NumChunks(); chunkIndex++ {
		for pieceIndex := uint64(0); pieceIndex < uint64(f.ErasureCode().NumPieces()); pieceIndex++ {
			err1 := f.AddPiece(types.SiaPublicKey{Key: []byte{byte(0)}}, chunkIndex, pieceIndex, crypto.Hash{})
			err2 := f.AddPiece(types.SiaPublicKey{Key: []byte{byte(1)}}, chunkIndex, pieceIndex, crypto.Hash{})
			if err := errors.Compose(err1, err2); err != nil {
				t.Fatal(err)
			}
		}
	}
	if f.staticMetadata.CachedUploadProgress != 100 {
		t.Fatal("expected uploadProgress to report 100% but was", f.staticMetadata.CachedUploadProgress)
	}
	if err := ensureMetadataValid(f.Metadata()); err != nil {
		t.Fatal(err)
	}
}

// TestFileExpiration probes the expiration method of the file type.
func TestFileExpiration(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	siaFilePath, _, source, rc, sk, fileSize, numChunks, fileMode := newTestFileParams(1, false)
	f, _, _ := customTestFileAndWAL(siaFilePath, source, rc, sk, fileSize, numChunks, fileMode)
	contracts := make(map[string]skymodules.RenterContract)
	_ = f.Expiration(contracts)
	if f.staticMetadata.CachedExpiration != 0 {
		t.Error("file with no pieces should report as having no time remaining")
	}
	// Create 3 public keys
	pk1 := types.SiaPublicKey{Key: []byte{0}}
	pk2 := types.SiaPublicKey{Key: []byte{1}}
	pk3 := types.SiaPublicKey{Key: []byte{2}}

	// Add a piece for each key to the file.
	err1 := f.AddPiece(pk1, 0, 0, crypto.Hash{})
	err2 := f.AddPiece(pk2, 0, 1, crypto.Hash{})
	err3 := f.AddPiece(pk3, 0, 2, crypto.Hash{})
	if err := errors.Compose(err1, err2, err3); err != nil {
		t.Fatal(err)
	}

	// Add a contract.
	fc := skymodules.RenterContract{}
	fc.EndHeight = 100
	contracts[pk1.String()] = fc
	_ = f.Expiration(contracts)
	if f.staticMetadata.CachedExpiration != 100 {
		t.Error("file did not report lowest WindowStart", f.staticMetadata.CachedExpiration)
	}

	// Add a contract with a lower WindowStart.
	fc.EndHeight = 50
	contracts[pk2.String()] = fc
	_ = f.Expiration(contracts)
	if f.staticMetadata.CachedExpiration != 50 {
		t.Error("file did not report lowest WindowStart", f.staticMetadata.CachedExpiration)
	}

	// Add a contract with a higher WindowStart.
	fc.EndHeight = 75
	contracts[pk3.String()] = fc
	_ = f.Expiration(contracts)
	if f.staticMetadata.CachedExpiration != 50 {
		t.Error("file did not report lowest WindowStart", f.staticMetadata.CachedExpiration)
	}
	if err := ensureMetadataValid(f.Metadata()); err != nil {
		t.Fatal(err)
	}
}

// BenchmarkLoadSiaFile benchmarks loading an existing siafile's metadata into
// memory.
func BenchmarkLoadSiaFile(b *testing.B) {
	// Get new file params
	siaFilePath, _, source, _, sk, _, _, fileMode := newTestFileParams(1, false)
	// Create the path to the file.
	dir, _ := filepath.Split(siaFilePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		b.Fatal(err)
	}
	// Create the file.
	wal, _ := newTestWAL()
	rc, err := skymodules.NewRSSubCode(10, 20, crypto.SegmentSize)
	if err != nil {
		b.Fatal(err)
	}
	sf, err := New(siaFilePath, source, wal, rc, sk, 1, fileMode) // 1 chunk file
	if err != nil {
		b.Fatal(err)
	}
	if err := sf.GrowNumChunks(10); err != nil { // Grow file to 10 chunks
		b.Fatal(err)
	}
	// Add pieces to chunks until every chunk has full redundancy.
	hostKeys := make([]types.SiaPublicKey, rc.NumPieces())
	for i := range hostKeys {
		fastrand.Read(hostKeys[i].Key)
	}
	for pieceIndex := 0; pieceIndex < rc.NumPieces(); pieceIndex++ {
		for chunkIndex := 0; chunkIndex < int(sf.NumChunks()); chunkIndex++ {
			if err := sf.AddPiece(hostKeys[pieceIndex], uint64(chunkIndex), uint64(pieceIndex), crypto.Hash{}); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.ResetTimer()
	for loads := 0; loads < b.N; loads++ {
		sf, err = LoadSiaFile(siaFilePath, wal)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRandomChunkWriteSingleThreaded(b *testing.B) {
	benchmarkRandomChunkWrite(1, b)
}
func BenchmarkRandomChunkWriteMultiThreaded(b *testing.B) {
	// 50 seems reasonable since it matches the number of hosts we usually have contracts with
	benchmarkRandomChunkWrite(50, b)
}

// BenchmarkRandomChunkWrite benchmarks writing pieces to random chunks within a
// siafile.
func benchmarkRandomChunkWrite(numThreads int, b *testing.B) {
	// Get new file params
	siaFilePath, _, source, _, sk, _, _, fileMode := newTestFileParams(1, false)
	// Create the path to the file.
	dir, _ := filepath.Split(siaFilePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		b.Fatal(err)
	}
	// Create the file.
	wal, _ := newTestWAL()
	rc, err := skymodules.NewRSSubCode(10, 20, crypto.SegmentSize)
	if err != nil {
		b.Fatal(err)
	}
	sf, err := New(siaFilePath, source, wal, rc, sk, 1, fileMode) // 1 chunk file
	if err != nil {
		b.Fatal(err)
	}
	if err := sf.GrowNumChunks(100); err != nil { // Grow file to 100 chunks
		b.Fatal(err)
	}
	// Add pieces to random chunks until every chunk has full redundancy.
	var writes uint64
	piecePerm := fastrand.Perm(rc.NumPieces())
	chunkPerm := fastrand.Perm(int(sf.NumChunks()))
	hostKeys := make([]types.SiaPublicKey, rc.NumPieces())
	for i := range hostKeys {
		fastrand.Read(hostKeys[i].Key)
	}
	start := make(chan struct{})
	worker := func() {
		<-start
		for atomic.LoadUint64(&writes) < uint64(b.N) {
			for _, pieceIndex := range piecePerm {
				for _, chunkIndex := range chunkPerm {
					if err := sf.AddPiece(hostKeys[pieceIndex], uint64(chunkIndex), uint64(pieceIndex), crypto.Hash{}); err != nil {
						b.Fatal(err)
					}
					atomic.AddUint64(&writes, 1)
					if atomic.LoadUint64(&writes) >= uint64(b.N) {
						return
					}
				}
			}
		}
	}
	// Spawn worker threads.
	var wg sync.WaitGroup
	for i := 0; i < numThreads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker()
		}()
	}
	// Reset timer and start threads.
	b.ResetTimer()
	close(start)
	wg.Wait()
}

// BenchmarkRandomChunkRead benchmarks reading pieces of a random chunks within
// a siafile.
func BenchmarkRandomChunkRead(b *testing.B) {
	// Get new file params
	siaFilePath, _, source, _, sk, _, _, fileMode := newTestFileParams(1, false)
	// Create the path to the file.
	dir, _ := filepath.Split(siaFilePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		b.Fatal(err)
	}
	// Create the file.
	wal, _ := newTestWAL()
	rc, err := skymodules.NewRSSubCode(10, 20, crypto.SegmentSize)
	if err != nil {
		b.Fatal(err)
	}
	sf, err := New(siaFilePath, source, wal, rc, sk, 1, fileMode) // 1 chunk file
	if err != nil {
		b.Fatal(err)
	}
	if err := sf.GrowNumChunks(10); err != nil { // Grow file to 10 chunks
		b.Fatal(err)
	}
	// Add pieces to chunks until every chunk has full redundancy.
	hostKeys := make([]types.SiaPublicKey, rc.NumPieces())
	for i := range hostKeys {
		fastrand.Read(hostKeys[i].Key)
	}
	for pieceIndex := 0; pieceIndex < rc.NumPieces(); pieceIndex++ {
		for chunkIndex := 0; chunkIndex < int(sf.NumChunks()); chunkIndex++ {
			if err := sf.AddPiece(hostKeys[pieceIndex], uint64(chunkIndex), uint64(pieceIndex), crypto.Hash{}); err != nil {
				b.Fatal(err)
			}
		}
	}
	// Read random pieces
	reads := 0
	chunkPerm := fastrand.Perm(int(sf.NumChunks()))
	b.ResetTimer()
	for reads < b.N {
		for _, chunkIndex := range chunkPerm {
			if _, err := sf.Pieces(uint64(chunkIndex)); err != nil {
				b.Fatal(err)
			}
			reads++
			if reads == b.N {
				return
			}
		}
	}
}

// ensureMetadataValid is a helper method which ensures we can backup and
// recover siafile metadata. By doing that, it also ensures that all the fields
// have valid values.
func ensureMetadataValid(md Metadata) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s", r)
		}
	}()
	md.backup()
	return nil
}

// TestCalculateHealth probes the CalculateHealth functions
func TestCalculateHealth(t *testing.T) {
	t.Parallel()

	// Define health check function
	checkHealth := func(gp, mp, np int, h float64) {
		health := CalculateHealth(gp, mp, np)
		if health != h {
			t.Logf("gp %v mp %v np %v", gp, mp, np)
			t.Errorf("expected health of %v, got %v", h, health)
		}
	}

	// Prepare rounding helper.
	round := func(h float64) float64 {
		return math.Round(h*10e3) / 10e3
	}

	// 0 good pieces
	mp := fastrand.Intn(10) + 1 // +1 avoid 0 minpieces
	// +1 and +mp to avoid 0 paritypieces and numPieces == minPieces
	np := mp + fastrand.Intn(10) + 1
	h := round(1 - float64(0-mp)/float64(np-mp))
	checkHealth(0, mp, np, h)

	// Full health
	mp = fastrand.Intn(10) + 1 // +1 avoid 0 minpieces
	// +1 and +mp to avoid 0 paritypieces and numPieces == minPieces
	np = mp + fastrand.Intn(10) + 1
	checkHealth(np, mp, np, 0)

	// In the middle
	mp = fastrand.Intn(10) + 1 // +1 avoid 0 minpieces
	// +1 and +mp to avoid 0 paritypieces and numPieces == minPieces
	np = mp + fastrand.Intn(10) + 1
	gp := fastrand.Intn(np)
	h = round(1 - float64(gp-mp)/float64(np-mp))
	checkHealth(gp, mp, np, h)

	// Recover check
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected critical")
		}
	}()
	checkHealth(0, 0, 0, 0)
}

// TestUpdateMetadata probes the UpdateMetadata method
func TestUpdateMetadata(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a file
	rsc, _ := skymodules.NewRSCode(10, 20)
	siaFilePath, _, source, _, sk, _, _, fileMode := newTestFileParams(1, true)
	file, _, _ := customTestFileAndWAL(siaFilePath, source, rsc, sk, 0, 0, fileMode)

	// Add a used host to the pubkeyTable
	pk := types.SiaPublicKey{Key: []byte{byte(0)}}
	pubkeyTable := []HostPublicKey{{PublicKey: pk, Used: true}}
	pubkeyTableCopy := make([]HostPublicKey, len(pubkeyTable))
	copy(pubkeyTableCopy, pubkeyTable)

	// Set all the cached values to values that will be updated when
	// UpdateMetadata is called
	file.mu.Lock()
	// pubKeyTable is updated during UpdateMetadata
	file.pubKeyTable = pubkeyTable
	// Cached redundancy fields are updated during UpdateMetadata
	file.staticMetadata.CachedRedundancy = math.MaxFloat64
	file.staticMetadata.CachedUserRedundancy = math.MaxFloat64
	// Cached health fields are updated during UpdateMetadata
	file.staticMetadata.CachedHealth = math.MaxFloat64
	file.staticMetadata.CachedStuckHealth = math.MaxFloat64
	file.staticMetadata.CachedRepairBytes = math.MaxUint64
	file.staticMetadata.CachedStuckBytes = math.MaxUint64
	file.staticMetadata.CachedNumStuckChunks = math.MaxUint64
	// LastHealthCheckTime is updated during UpdateMetadata
	file.staticMetadata.LastHealthCheckTime = time.Time{}
	// CachedExpiration is updated during UpdateMetadata
	file.staticMetadata.CachedExpiration = types.BlockHeight(0)
	file.mu.Unlock()

	// Define helper function
	checkMetadata := func(sf *SiaFile) (err error) {
		// Check PubKeyTable
		if reflect.DeepEqual(sf.pubKeyTable, pubkeyTableCopy) {
			err = errors.Compose(err, fmt.Errorf("pubkeyTable not updated; found %v", sf.pubKeyTable))
		}
		// Check redundancy
		if sf.staticMetadata.CachedRedundancy == math.MaxFloat64 {
			err = errors.Compose(err, fmt.Errorf("CachedRedundancy not updated; found %v", sf.staticMetadata.CachedRedundancy))
		}
		if sf.staticMetadata.CachedUserRedundancy == math.MaxFloat64 {
			err = errors.Compose(err, fmt.Errorf("CachedUserRedundancy not updated; found %v", sf.staticMetadata.CachedUserRedundancy))
		}
		// Check health
		if sf.staticMetadata.CachedHealth == math.MaxFloat64 {
			err = errors.Compose(err, fmt.Errorf("CachedHealth not updated; found %v", sf.staticMetadata.CachedHealth))
		}
		if sf.staticMetadata.CachedStuckHealth == math.MaxFloat64 {
			err = errors.Compose(err, fmt.Errorf("CachedStuckHealth not updated; found %v", sf.staticMetadata.CachedStuckHealth))
		}
		if sf.staticMetadata.CachedRepairBytes == math.MaxUint64 {
			err = errors.Compose(err, fmt.Errorf("CachedRepairBytes not updated; found %v", sf.staticMetadata.CachedRepairBytes))
		}
		if sf.staticMetadata.CachedStuckBytes == math.MaxUint64 {
			err = errors.Compose(err, fmt.Errorf("CachedStuckBytes not updated; found %v", sf.staticMetadata.CachedStuckBytes))
		}
		if sf.staticMetadata.CachedNumStuckChunks == math.MaxUint64 {
			err = errors.Compose(err, fmt.Errorf("CachedNumStuckChunks not updated; found %v", sf.staticMetadata.CachedNumStuckChunks))
		}
		// Check LastHealthCheckTime
		if sf.staticMetadata.LastHealthCheckTime.IsZero() {
			err = errors.Compose(err, fmt.Errorf("LastHealthCheckTime not updated; found %v", sf.staticMetadata.LastHealthCheckTime))
		}
		// Check Expiration
		if sf.staticMetadata.CachedExpiration == types.BlockHeight(0) {
			err = errors.Compose(err, fmt.Errorf("CachedExpiration not updated; found %v", sf.staticMetadata.CachedExpiration))
		}
		return
	}
	err := checkMetadata(file)
	if err == nil {
		t.Fatal("metadata not initialized properly")
	}

	// Create offline and goodForRenew maps
	offlineMap := make(map[string]bool)
	goodForRenewMap := make(map[string]bool)

	// Create list of used Hosts
	used := []types.SiaPublicKey{}

	// Create contracts map
	contractsMap := make(map[string]skymodules.RenterContract)

	// UpdateMetadata
	err = file.UpdateMetadata(offlineMap, goodForRenewMap, contractsMap, used)
	if err != nil {
		t.Fatal(err)
	}

	// Check metadata
	err = checkMetadata(file)
	if err != nil {
		t.Fatal(err)
	}
}

// TestUnrecoverable probes the Unrecoverable function.
func TestUnrecoverable(t *testing.T) {
	t.Parallel()

	// Define Tests
	var tests = []struct {
		health        float64
		onDisk        bool
		unrecoverable bool
	}{
		// Unrecoverable
		{1.01, false, true},
		{math.MaxFloat64, false, true},

		// Recoverable
		//
		// If onDisk, health doesn't matter
		{0, true, false},
		{1.01, true, false},
		{math.MaxFloat64, true, false},
		// If !onDisk, health must be <=1
		{0, false, false},
		{0.99, false, false},
		{1, false, false},
	}

	// Execute Tests
	for _, test := range tests {
		if Unrecoverable(test.health, test.onDisk) != test.unrecoverable {
			t.Error("Unexpected results", test)
		}
	}
}

// TestUpdateMetadataPruneHosts is a regression test for UpdateMetadata. It
// covers the edge case where the host pubkey table is pruned, causing host
// offsets within pieces to become invalid.
func TestUpdateMetadataPruneHosts(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	// Get a blank siafile with a 1-1 ec.
	sf, _, _ := newBlankTestFileAndWAL(2) // make sure we have 1 full chunk at the beginning of sf.fullChunks
	ec, err := skymodules.NewRSSubCode(1, 1, crypto.SegmentSize)
	if err != nil {
		t.Fatal(err)
	}
	sf.staticMetadata.staticErasureCode = ec

	// Use the first chunk of the file for testing.
	c, err := sf.chunk(0)
	if err != nil {
		t.Fatal(err)
	}

	// Add 4 hosts to the table.
	_, pk1 := crypto.GenerateKeyPair()
	hpk1 := types.Ed25519PublicKey(pk1)
	_, pk2 := crypto.GenerateKeyPair()
	hpk2 := types.Ed25519PublicKey(pk2)
	_, pk3 := crypto.GenerateKeyPair()
	hpk3 := types.Ed25519PublicKey(pk3)
	_, pk4 := crypto.GenerateKeyPair()
	hpk4 := types.Ed25519PublicKey(pk4)
	sf.pubKeyTable = append(sf.pubKeyTable, HostPublicKey{Used: true, PublicKey: hpk1})
	sf.pubKeyTable = append(sf.pubKeyTable, HostPublicKey{Used: true, PublicKey: hpk2})
	sf.pubKeyTable = append(sf.pubKeyTable, HostPublicKey{Used: true, PublicKey: hpk3})
	sf.pubKeyTable = append(sf.pubKeyTable, HostPublicKey{Used: true, PublicKey: hpk4})

	// Add 2 pieces to each set of pieces. One for hpk1 and one for hpk4.
	sf.pubKeyTable = append(sf.pubKeyTable, HostPublicKey{Used: false})
	for i := range c.Pieces {
		c.Pieces[i] = append(c.Pieces[i], piece{HostTableOffset: uint32(len(sf.pubKeyTable) - 1)})
		c.Pieces[i] = append(c.Pieces[i], piece{HostTableOffset: 0})
	}

	// Add more hosts to reach the threshold of max unused hosts.
	for i := 0; i < pubKeyTablePruneThreshold; i++ {
		sf.pubKeyTable = append(sf.pubKeyTable, HostPublicKey{Used: false})
	}

	// Saves changes.
	err = sf.saveFile([]chunk{c})
	if err != nil {
		t.Fatal(err)
	}

	// Update the metadata.
	m := make(map[string]bool)
	contracts := make(map[string]skymodules.RenterContract)
	err = sf.UpdateMetadata(m, m, contracts, []types.SiaPublicKey{hpk1, hpk2, hpk3})
	if err != nil {
		t.Fatal(err)
	}

	// Pubkeytable should have a length of 3 afterwards.
	if len(sf.pubKeyTable) != 3 {
		t.Fatal("wrong length", len(sf.pubKeyTable))
	}
}

// TestFinished is a simple unit test to probe the Finished and SetFinished
// Method of the siafile
func TestFinished(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create file
	siaFilePath, _, _, rc, sk, fileSize, numChunks, fileMode := newTestFileParams(1, true)
	file, _, _ := customTestFileAndWAL(siaFilePath, "", rc, sk, fileSize, numChunks, fileMode)

	// Create helper
	checkFinished := func(finished bool) error {
		if file.Finished() != finished {
			return fmt.Errorf("Expected file to be finished %v but found finished %v", finished, file.Finished())
		}
		return nil
	}

	// Initial File should be unfinished
	if err := checkFinished(false); err != nil {
		t.Fatal(err)
	}

	// SetFinished shouldn't change status for health > 1
	file.SetFinished(1.1)
	if err := checkFinished(false); err != nil {
		t.Fatal(err)
	}

	// SetFinished with health of 1 should mark as finished
	file.SetFinished(1)
	if err := checkFinished(true); err != nil {
		t.Fatal(err)
	}

	// Manually reset
	file.staticMetadata.Finished = false
	if err := checkFinished(false); err != nil {
		t.Fatal(err)
	}

	// SetFinished with health of 0 should mark as finished
	file.SetFinished(0)
	if err := checkFinished(true); err != nil {
		t.Fatal(err)
	}

	// Calling SetFinished with a high health again should have no effect.
	file.SetFinished(10)
	if err := checkFinished(true); err != nil {
		t.Fatal(err)
	}

	// Manually reset
	file.staticMetadata.Finished = false
	if err := checkFinished(false); err != nil {
		t.Fatal(err)
	}

	// File should be considered finished with local path even if
	// SetFinished is called with a high health
	file.staticMetadata.LocalPath = "notblank"
	file.SetFinished(10)
	if err := checkFinished(true); err != nil {
		t.Fatal(err)
	}

	// Removing the localpath should have no effect
	file.staticMetadata.LocalPath = ""
	file.SetFinished(10)
	if err := checkFinished(true); err != nil {
		t.Fatal(err)
	}
}
