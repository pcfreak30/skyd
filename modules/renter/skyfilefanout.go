package renter

// skyfilefanout.go implements the encoding and decoding of skyfile fanouts. A
// fanout is a description of all of the Merkle roots in a file, organized by
// chunk. Each chunk has N pieces, and each piece has a Merkle root which is a
// 32 byte hash.
//
// The fanout is encoded such that the first 32 bytes are chunk 0 index 0, the
// second 32 bytes are chunk 0 index 1, etc... and then the second chunk is
// appended immedately after, and so on.

import (
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/renter/filesystem"
	"gitlab.com/NebulousLabs/Sia/modules/renter/filesystem/siafile"
	"gitlab.com/NebulousLabs/Sia/skykey"
	"gitlab.com/NebulousLabs/errors"
)

// fanoutStreamBufferDataSource implements streamBufferDataSource with the
// skyfile so that it can be used to open a stream from the streamBufferSet.
type fanoutStreamBufferDataSource struct {
	// Each chunk is an array of sector hashes that correspond to pieces which
	// can be fetched.
	staticChunks       [][]crypto.Hash
	staticChunkSize    uint64
	staticErasureCoder modules.ErasureCoder
	staticLayout       skyfileLayout
	staticMasterKey    crypto.CipherKey
	staticMetadata     modules.SkyfileMetadata
	staticStreamID     streamDataSourceID

	// staticTimeout defines a timeout that is applied to every chunk download
	staticTimeout time.Duration

	// Utils.
	staticRenter *Renter
	mu           sync.Mutex
}

// newFanoutStreamer will create a modules.Streamer from the fanout of a
// skyfile. The streamer is created by implementing the streamBufferDataSource
// interface on the skyfile, and then passing that to the stream buffer set.
func (r *Renter) newFanoutStreamer(link modules.Skylink, ll skyfileLayout, metadata modules.SkyfileMetadata, fanoutBytes []byte, timeout time.Duration, sk skykey.Skykey) (modules.Streamer, error) {
	masterKey, err := r.deriveFanoutKey(&ll, sk)
	if err != nil {
		return nil, errors.AddContext(err, "count not recover siafile fanout because cipher key was unavailable")
	}

	// Create the erasure coder
	ec, err := siafile.NewRSSubCode(int(ll.fanoutDataPieces), int(ll.fanoutParityPieces), crypto.SegmentSize)
	if err != nil {
		return nil, errors.New("unable to initialize erasure code")
	}

	// Build the base streamer object.
	fs := &fanoutStreamBufferDataSource{
		staticChunkSize:    modules.SectorSize * uint64(ll.fanoutDataPieces),
		staticErasureCoder: ec,
		staticLayout:       ll,
		staticMasterKey:    masterKey,
		staticMetadata:     metadata,
		staticStreamID:     streamDataSourceID(crypto.HashObject(link.String())),
		staticTimeout:      timeout,
		staticRenter:       r,
	}
	err = fs.decodeFanout(fanoutBytes)
	if err != nil {
		return nil, errors.AddContext(err, "unable to decode fanout of skyfile")
	}

	// Grab and return the stream.
	stream := r.staticStreamBufferSet.callNewStream(fs, 0)
	return stream, nil
}

// decodeFanout will take the fanout bytes from a skyfile and decode them in to
// the staticChunks filed of the fanoutStreamBufferDataSource.
func (fs *fanoutStreamBufferDataSource) decodeFanout(fanoutBytes []byte) error {
	// Special case: if the data of the file is using 1-of-N erasure coding,
	// each piece will be identical, so the fanout will only have encoded a
	// single piece for each chunk.
	ll := fs.staticLayout
	var piecesPerChunk uint64
	var chunkRootsSize uint64
	if ll.fanoutDataPieces == 1 && ll.cipherType == crypto.TypePlain {
		piecesPerChunk = 1
		chunkRootsSize = crypto.HashSize
	} else {
		// This is the case where the file data is not 1-of-N. Every piece is
		// different, so every piece must get enumerated.
		piecesPerChunk = uint64(ll.fanoutDataPieces) + uint64(ll.fanoutParityPieces)
		chunkRootsSize = crypto.HashSize * piecesPerChunk
	}
	// Sanity check - the fanout bytes should be an even number of chunks.
	if uint64(len(fanoutBytes))%chunkRootsSize != 0 {
		return errors.New("the fanout bytes do not contain an even number of chunks")
	}
	numChunks := uint64(len(fanoutBytes)) / chunkRootsSize

	// Decode the fanout data into the list of chunks for the
	// fanoutStreamBufferDataSource.
	fs.staticChunks = make([][]crypto.Hash, 0, numChunks)
	for i := uint64(0); i < numChunks; i++ {
		chunk := make([]crypto.Hash, piecesPerChunk)
		for j := uint64(0); j < piecesPerChunk; j++ {
			fanoutOffset := (i * chunkRootsSize) + (j * crypto.HashSize)
			copy(chunk[j][:], fanoutBytes[fanoutOffset:])
		}
		fs.staticChunks = append(fs.staticChunks, chunk)
	}
	return nil
}

// skyfileEncodeFanout will create the serialized fanout for a fileNode. The
// encoded fanout is just the list of hashes that can be used to retrieve a file
// concatenated together, where piece 0 of chunk 0 is first, piece 1 of chunk 0
// is second, etc. The full set of erasure coded pieces are included.
//
// There is a special case for unencrypted 1-of-N files. Because every piece is
// identical for an unencrypted 1-of-N file, only the first piece of each chunk
// is included.
func skyfileEncodeFanout(fileNode *filesystem.FileNode) ([]byte, error) {
	// Grab the erasure coding scheme and encryption scheme from the file.
	cipherType := fileNode.MasterKey().Type()
	dataPieces := fileNode.ErasureCode().MinPieces()
	numPieces := fileNode.ErasureCode().NumPieces()
	onlyOnePieceNeeded := dataPieces == 1 && cipherType == crypto.TypePlain

	// Allocate the memory for the fanout.
	var fanout []byte
	if onlyOnePieceNeeded {
		fanout = make([]byte, 0, fileNode.NumChunks()*crypto.HashSize)
	} else {
		fanout = make([]byte, 0, fileNode.NumChunks()*uint64(numPieces)*crypto.HashSize)
	}

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
		if onlyOnePieceNeeded {
			for _, pieceSet := range allPieces {
				root := findPieceInPieceSet(pieceSet)
				if root != emptyHash {
					fanout = append(fanout, root[:]...)
					break
				}
			}
			continue
		}

		// General case: get one root per piece.
		for _, pieceSet := range allPieces {
			root := findPieceInPieceSet(pieceSet)
			fanout = append(fanout, root[:]...)
		}
	}
	return fanout, nil
}

// DataSize returns the amount of file data in the underlying skyfile.
func (fs *fanoutStreamBufferDataSource) DataSize() uint64 {
	return fs.staticLayout.filesize
}

// ID returns the id of the skylink being fetched, this is just the hash of the
// skylink.
func (fs *fanoutStreamBufferDataSource) ID() streamDataSourceID {
	return fs.staticStreamID
}

// Metadata returns the Skyfile metadata for this skylink.
func (fs *fanoutStreamBufferDataSource) Metadata() modules.SkyfileMetadata {
	return fs.staticMetadata
}

// ReadAt will fetch data from the siafile at the provided offset.
func (fs *fanoutStreamBufferDataSource) ReadAt(b []byte, offset int64) (int, error) {
	// Input checking.
	if offset < 0 {
		fs.staticRenter.log.Critical("fanout streamer had a request at a negative offset")
		return 0, errors.New("cannot read from a negative offset")
	}
	// Can only grab data within one chunk.
	offsetWithinChunk := uint64(offset) % fs.staticChunkSize
	if uint64(len(b))+offsetWithinChunk > fs.staticChunkSize {
		fs.staticRenter.log.Critical("fanout streamer had a request that straddled chunks")
		return 0, errors.New("request needs to be no more than RequestSize()")
	}
	// Must be aligned with an RSSubCode
	chunkSegmentSize := crypto.SegmentSize * uint64(fs.staticLayout.fanoutDataPieces)
	if uint64(offset)%chunkSegmentSize != 0 {
		fs.staticRenter.log.Critical("fanout streamer had a request that was not chunk segment size aligned")
		return 0, errors.New("request needs to be aligned to RequestSize()")
	}
	// Must not go beyond the end of the file.
	if uint64(offset)+uint64(len(b)) > fs.staticLayout.filesize {
		fs.staticRenter.log.Critical("fanout streamer had a request that went beyond the size of the file")
		return 0, errors.New("making a read request that goes beyond the boundaries of the file")
	}

	// Determine which chunk contains the data.
	chunkIndex := uint64(offset) / fs.staticChunkSize

	// Determine how much data needs to be downloaded from the chunk to fill out
	// the read request.
	downloadLen := uint64(len(b))
	if uint64(len(b))%chunkSegmentSize != 0 {
		// Round up to the nearest chunk segment size. We do this by abusing
		// integer division.
		downloadLen += chunkSegmentSize
		downloadLen /= chunkSegmentSize
		downloadLen *= chunkSegmentSize
	}

	// Perform a download to fetch the chunk.
	chunkData, err := fs.managedFetchChunk(chunkIndex, offsetWithinChunk, downloadLen)
	if err != nil {
		return 0, errors.AddContext(err, "unable to fetch chunk in ReadAt call on fanout streamer")
	}
	n := copy(b, chunkData)
	return n, nil
}

// RequestSize implements streamBufferDataSource and will return the size of a
// logical data chunk.
func (fs *fanoutStreamBufferDataSource) RequestSize() uint64 {
	// If the cipher type overhead is not 0, the chunk has some data at the
	// front that needs to be skipped before doing erasure decoding, and support
	// for that hasn't been implemented.
	//
	// The only files that are affected would be files that were uploaded to the
	// Sia network many years ago, as all of the new encryption schemes have 0
	// overhead.
	if fs.staticLayout.cipherType.Overhead() != 0 {
		return fs.staticChunkSize
	}

	// NOTE: Because of the way that sectors are segmented, the divsor here has
	// to be a power of 2, and can be no larger than 2^16.
	//
	// TODO: 256 is probably a better divisor, this results in sector request
	// sizes that are 64kb.
	return fs.staticChunkSize / 4
}

// SilentClose will clean up any resources that the fanoutStreamBufferDataSource
// keeps open.
func (fs *fanoutStreamBufferDataSource) SilentClose() {
	// Nothing to clean up.
	return
}
