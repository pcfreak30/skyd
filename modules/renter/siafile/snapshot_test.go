package siafile

import (
	"bytes"
	"reflect"
	"testing"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
)

// TestSnapshot tests if a snapshot is created correctly from a SiaFile.
func TestSnapshot(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a random file for testing and create a snapshot from it.
	sf := dummyEntry(newTestFile())
	snap, err := sf.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	// Make sure the snapshot has the same fields as the SiaFile.
	if sf.numChunks() != uint64(len(snap.staticChunks)) {
		t.Errorf("expected %v chunks but got %v", sf.numChunks(), len(snap.staticChunks))
	}
	if sf.staticMetadata.FileSize != snap.staticFileSize {
		t.Errorf("staticFileSize was %v but should be %v",
			snap.staticFileSize, sf.staticMetadata.FileSize)
	}
	if sf.staticMetadata.StaticPieceSize != snap.staticPieceSize {
		t.Errorf("staticPieceSize was %v but should be %v",
			snap.staticPieceSize, sf.staticMetadata.StaticPieceSize)
	}
	if sf.staticMetadata.staticErasureCode.MinPieces() != snap.staticErasureCode.MinPieces() {
		t.Errorf("minPieces was %v but should be %v",
			sf.staticMetadata.staticErasureCode.MinPieces(), snap.staticErasureCode.MinPieces())
	}
	if sf.staticMetadata.staticErasureCode.NumPieces() != snap.staticErasureCode.NumPieces() {
		t.Errorf("numPieces was %v but should be %v",
			sf.staticMetadata.staticErasureCode.NumPieces(), snap.staticErasureCode.NumPieces())
	}
	if !bytes.Equal(sf.staticMetadata.StaticMasterKey, snap.staticMasterKey.Key()) {
		t.Error("masterkeys don't match")
	}
	if sf.staticMetadata.StaticMasterKeyType != snap.staticMasterKey.Type() {
		t.Error("masterkey types don't match")
	}
	if sf.staticMetadata.Mode != snap.staticMode {
		t.Error("modes don't match")
	}
	if !reflect.DeepEqual(sf.pubKeyTable, snap.staticPubKeyTable) {
		t.Error("pubkeytables don't match")
	}
	sf.staticSiaFileSet.mu.Lock()
	if sf.staticSiaFileSet.siaPath(sf) != snap.staticSiaPath {
		t.Error("siapaths don't match")
	}
	sf.staticSiaFileSet.mu.Unlock()
	// Compare the pieces.
	for i := range sf.allChunks() {
		sfPieces, err := sf.Pieces(uint64(i))
		snapPieces := snap.Pieces(uint64(i))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(sfPieces, snapPieces) {
			t.Error("Pieces don't match")
		}
	}
}

// BenchmarkSnapshot10MB benchmarks the creation of snapshots for Siafiles
// which hold the metadata of a 10 MB file.
func BenchmarkSnapshot10MB(b *testing.B) {
	benchmarkSnapshot(b, uint64(1e7))
}

// BenchmarkSnapshot100MB benchmarks the creation of snapshots for Siafiles
// which hold the metadata of a 100 MB file.
func BenchmarkSnapshot100MB(b *testing.B) {
	benchmarkSnapshot(b, uint64(1e8))
}

// BenchmarkSnapshot1GB benchmarks the creation of snapshots for Siafiles
// which hold the metadata of a 1 GB file.
func BenchmarkSnapshot1GB(b *testing.B) {
	benchmarkSnapshot(b, uint64(1e9))
}

// BenchmarkSnapshot10GB benchmarks the creation of snapshots for Siafiles
// which hold the metadata of a 10 GB file.
func BenchmarkSnapshot10GB(b *testing.B) {
	benchmarkSnapshot(b, uint64(1e10))
}

// benchmarkSnapshot is a helper function for benchmarking the creation of
// snapshots of Siafiles with different sizes.
func benchmarkSnapshot(b *testing.B, fileSize uint64) {
	// Create the file.
	siafile := newBlankTestFile()
	sf := dummyEntry(siafile)
	// Add a host key to the table.
	sf.addRandomHostKeys(1)
	// Add numPieces to each chunk.
	for i := uint64(0); i < sf.NumChunks(); i++ {
		for j := uint64(0); j < uint64(sf.ErasureCode().NumPieces()); j++ {
			sf.AddPiece(types.SiaPublicKey{}, i, j, crypto.Hash{})
		}
	}

	// Save the file to disk.
	if err := sf.saveFile(); err != nil {
		b.Fatal(err)
	}
	// Reset the timer.
	b.ResetTimer()

	// Create snapshots as fast as possible.
	for i := 0; i < b.N; i++ {
		_, err := sf.Snapshot()
		if err != nil {
			b.Fatal(err)
		}
	}
}
