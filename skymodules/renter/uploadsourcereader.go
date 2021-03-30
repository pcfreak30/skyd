package renter

import (
	"bytes"
	"io"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/skynetlabs/skyd/skymodules"
)

// chunkReader implements the ChunkReader interface by wrapping a io.Reader.
type chunkReader struct {
	staticEC        skymodules.ErasureCoder
	staticPieceSize uint64
	staticReader    io.Reader

	peek []byte
}

// fanoutChunkReader implements the FanoutChunkReader interface by wrapping a
// ChunkReader.
type fanoutChunkReader struct {
	skymodules.ChunkReader

	// fanout related fields
	chunkIndex      uint64
	fanout          []byte
	staticOnePiece  bool
	staticMasterKey crypto.CipherKey
}

// NewChunkReader creates a new chunkReader.
func NewChunkReader(r io.Reader, ec skymodules.ErasureCoder, ct crypto.CipherType) skymodules.ChunkReader {
	return &chunkReader{
		staticReader:    r,
		staticEC:        ec,
		staticPieceSize: modules.SectorSize - ct.Overhead(),
	}
}

// NewFanoutChunkReader creates a new fanoutChunkReader.
func NewFanoutChunkReader(r io.Reader, ec skymodules.ErasureCoder, onePiece bool, mk crypto.CipherKey) skymodules.FanoutChunkReader {
	return &fanoutChunkReader{
		ChunkReader:     NewChunkReader(r, ec, mk.Type()),
		staticOnePiece:  onePiece,
		staticMasterKey: mk,
	}
}

// Peek returns whether the next call to ReadChunk is expected to return a
// chunk or if there is no more data.
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

// ReadChunk reads the next chunk from the reader. The returned chunk is erasure
// coded and will always be a full chunk. It also returns the number of bytes
// that this chunk was created from which is useful because the last chunk might
// be padded.
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
	cr.peek = nil
	return logicalChunkData, n, nil
}

// Fanout returns the current fanout.
func (cr *fanoutChunkReader) Fanout() []byte {
	return cr.fanout
}

// ReadChunk reads the next chunk from the reader. The returned chunk is erasure
// coded and will always be a full chunk. It also returns the number of bytes
// that this chunk was created from which is useful because the last chunk might
// be padded.
func (cr *fanoutChunkReader) ReadChunk() ([][]byte, uint64, error) {
	// If the chunk was read successfully, append the fanout.
	chunk, n, err := cr.ChunkReader.ReadChunk()
	if err != nil {
		return chunk, n, err
	}
	cr.appendFanout(chunk)
	return chunk, n, nil
}

// appendFanout appends the merkle roots of a given logical chunk to the fanout.
func (cr *fanoutChunkReader) appendFanout(logicalChunkData [][]byte) {
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
