package renter

// skyfilefanout.go implements the encoding and decoding of skyfile fanouts. A
// fanout is a description of all of the Merkle roots in a file, organized by
// chunk. Each chunk has N pieces, and each piece has a Merkle root which is a
// 32 byte hash.
//
// The fanout is encoded such that the first 32 bytes are chunk 0 index 0, the
// second 32 bytes are chunk 0 index 1, etc... and then the second chunk is
// appended immediately after, and so on.

import (
	"bytes"
	"fmt"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/skynetlabs/skyd/build"
	"gitlab.com/skynetlabs/skyd/skymodules/renter/filesystem"
	"gitlab.com/skynetlabs/skyd/skymodules/renter/filesystem/siafile"
)

// fanoutWriter is a helper type used to create the fanout for a skyfile. Raw
// uploaded data is written to it and it erasure codes it and encrypts it to
// create the fanout.
type fanoutWriter struct {
	chunkIndex     uint64
	fanout         []byte
	pieces         *bytes.Buffer
	staticFn       *filesystem.FileNode
	staticOnePiece bool
}

// newFanoutWriter creates a new fanout writer from a given fileNode.
func newFanoutWriter(fileNode *filesystem.FileNode, onePiece bool) *fanoutWriter {
	return &fanoutWriter{
		pieces:         bytes.NewBuffer(make([]byte, 0, fileNode.ErasureCode().MinPieces()*int(fileNode.PieceSize()))),
		staticFn:       fileNode,
		staticOnePiece: onePiece,
	}
}

// Fanout returns the fanout the writer constructed. It will return an error if
// the internal piece buffer is not empty.
func (fw *fanoutWriter) Fanout() ([]byte, error) {
	var err error
	// If there is data left in the buffer we need to pad and encode it.
	if fw.pieces.Len() > 0 {
		err = fw.encodeChunk()
	}
	return fw.fanout, err
}

// Write constructs the fanout from the given data. The input data should be
// exactly a piece. If it is not, it will return an error.
func (fw *fanoutWriter) Write(b []byte) (int, error) {
	fileNode := fw.staticFn
	pieceSize := fileNode.PieceSize()
	ec := fileNode.ErasureCode()

	// Append piece to pieces.
	n, err := fw.pieces.Write(b)
	if err != nil {
		return n, err
	}

	// If we don't have enough pieces for a chunk we are done.
	if fw.pieces.Len() < int(pieceSize)*ec.MinPieces() {
		return len(b), nil
	}

	// If we got enough pieces for a full chunk, encode it.
	err = fw.encodeChunk()
	if err != nil {
		return 0, err
	}

	// If we reached the end of the buffer, reset it. That way we reuse its
	// internal memory for the next piece.
	if fw.pieces.Len() == 0 {
		fw.pieces.Reset()
	}

	// Increment chunk index.
	fw.chunkIndex++
	return len(b), nil
}

// encodeChunk reads up to a full chunk from the internal pieces buffer, pads
// them, encodes them, encrypts them and adds the root to the fanout.
func (fw *fanoutWriter) encodeChunk() error {
	fileNode := fw.staticFn
	pieceSize := fileNode.PieceSize()
	ec := fileNode.ErasureCode()

	pieces, _, err := readDataPieces(fw.pieces, ec, pieceSize)
	if err != nil {
		return err
	}
	logicalChunkData, _ := fileNode.ErasureCode().EncodeShards(pieces)
	for pieceIndex := range logicalChunkData {
		// Encrypt and pad the piece with the given index.
		padAndEncryptPiece(fw.chunkIndex, uint64(pieceIndex), logicalChunkData, fileNode.MasterKey())
		root := crypto.MerkleRoot(logicalChunkData[pieceIndex])
		// Unlike in skyfileEncodeFanoutFromFileNode we don't check for an
		// emptyHash here since if MerkleRoot returned an emptyHash it would
		// mean that an emptyHash is a valid MerkleRoot and a host should be
		// able to return the corresponding data.
		fw.fanout = append(fw.fanout, root[:]...)

		// If only one piece is needed break out of the inner loop.
		if fw.staticOnePiece {
			break
		}
	}

	// If we reached the end of the buffer, reset it. That way we reuse its
	// internal memory for the next piece.
	if fw.pieces.Len() == 0 {
		fw.pieces.Reset()
	}

	// Increment chunk index.
	fw.chunkIndex++
	return nil
}

// skyfileEncodeFanoutFromFileNode will create the serialized fanout for
// a fileNode. The encoded fanout is just the list of hashes that can be used to
// retrieve a file concatenated together, where piece 0 of chunk 0 is first,
// piece 1 of chunk 0 is second, etc. This method assumes the  special case for
// unencrypted 1-of-N files. Because every piece is identical for an unencrypted
// 1-of-N file, only the first piece of each chunk is included.
func skyfileEncodeFanoutFromFileNode(fileNode *filesystem.FileNode, onePiece bool) ([]byte, error) {
	// Allocate the memory for the fanout.
	fanout := make([]byte, 0, fileNode.NumChunks()*crypto.HashSize)

	// findPieceInPieceSet will scan through a piece set and return the first
	// non-empty piece in the set. If the set is empty, or every piece in the
	// set is empty, then the emptyHash is returned.
	var emptyHash crypto.Hash
	findPieceInPieceSet := func(pieceSet []siafile.Piece) crypto.Hash {
		for _, piece := range pieceSet {
			if piece.MerkleRoot != emptyHash {
				return piece.MerkleRoot
			}
		}
		return emptyHash
	}

	// Build the fanout one chunk at a time.
	for i := uint64(0); i < fileNode.NumChunks(); i++ {
		// Get the pieces for this chunk.
		allPieces, err := fileNode.Pieces(i)
		if err != nil {
			return nil, errors.AddContext(err, "unable to get sector roots from file")
		}

		// Special case: if only one piece is needed, only use the first piece
		// that is available. This is because 1-of-N files are encoded more
		// compactly in the fanout.
		if onePiece {
			root := emptyHash
			for _, pieceSet := range allPieces {
				root = findPieceInPieceSet(pieceSet)
				if root != emptyHash {
					fanout = append(fanout, root[:]...)
					break
				}
			}
			// If root is still equal to emptyHash it means that we didn't add a
			// piece root for this chunk.
			if root == emptyHash {
				err = fmt.Errorf("No piece root encoded for chunk %v", i)
				build.Critical(err)
				return nil, err
			}
			continue
		}

		// Generate all the piece roots
		for pi, pieceSet := range allPieces {
			root := findPieceInPieceSet(pieceSet)
			if root == emptyHash {
				err = fmt.Errorf("Empty piece root at index %v found for chunk %v", pi, i)
				build.Critical(err)
				return nil, err
			}
			fanout = append(fanout, root[:]...)
		}
	}
	return fanout, nil
}
