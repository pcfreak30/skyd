package renter

import (
	"bytes"
	"io"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/skynetlabs/skyd/skymodules"
)

type chunkReader struct {
	staticEC        skymodules.ErasureCoder
	staticPieceSize uint64
	staticReader    io.Reader

	peek []byte

	// fanout related fields
	chunkIndex      uint64
	fanout          []byte
	staticOnePiece  bool
	staticMasterKey crypto.CipherKey
}

func NewChunkReader(r io.Reader, ec skymodules.ErasureCoder, pieceSize uint64, onePiece bool, mk crypto.CipherKey) skymodules.ChunkReader {
	return &chunkReader{
		staticReader:    r,
		staticEC:        ec,
		staticPieceSize: pieceSize,
		staticOnePiece:  onePiece,
		staticMasterKey: mk,
	}
}

func (cr *chunkReader) Close() error {
	return nil
}

func (cr *chunkReader) Peek() bool {
	// If 'peek' already has data, then there is more data to consume.
	if len(cr.peek) > 0 {
		return true
	}

	// Read a byte into peek.
	cr.peek = append(cr.peek, 0)
	_, err := io.ReadFull(cr.staticReader, cr.peek)
	if err != nil {
		return false
	}
	return true
}

func (cr *chunkReader) ReadChunk() ([][]byte, uint64, error) {
	r := io.MultiReader(bytes.NewReader(cr.peek), cr.staticReader)
	dataPieces, n, err := readDataPieces(r, cr.staticEC, cr.staticPieceSize)
	if err != nil {
		return nil, 0, errors.AddContext(err, "ReadChunk: failed to read data pieces")
	}
	logicalChunkData, err := cr.staticEC.EncodeShards(dataPieces)
	if err != nil {
		return nil, 0, errors.AddContext(err, "ReadChunk: failed to encode logical chunk data")
	}
	cr.appendFanout(logicalChunkData)
	cr.peek = nil
	return logicalChunkData, n, nil
}

func (cr *chunkReader) appendFanout(logicalChunkData [][]byte) {
	for pieceIndex := range logicalChunkData {
		// Encrypt and pad the piece with the given index.
		padAndEncryptPiece(cr.chunkIndex, uint64(pieceIndex), logicalChunkData, cr.staticMasterKey)
		root := crypto.MerkleRoot(logicalChunkData[pieceIndex])
		// Unlike in skyfileEncodeFanoutFromFileNode we don't check for an
		// emptyHash here since if MerkleRoot returned an emptyHash it would
		// mean that an emptyHash is a valid MerkleRoot and a host should be
		// able to return the corresponding data.
		cr.fanout = append(cr.fanout, root[:]...)

		// If only one piece is needed break out of the inner loop.
		if cr.staticOnePiece {
			break
		}
	}
	cr.chunkIndex++
}
