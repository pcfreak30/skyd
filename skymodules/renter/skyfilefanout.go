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
	"fmt"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetHQ/skyd/build"
	"gitlab.com/SkynetHQ/skyd/skymodules/renter/filesystem"
	"gitlab.com/SkynetHQ/skyd/skymodules/renter/filesystem/siafile"
)

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
