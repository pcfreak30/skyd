package renter

import (
	"context"
	"fmt"
	"io"
	"sync"

	"gitlab.com/NebulousLabs/errors"

	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
)

// Upload Streaming Overview:
// Most of the logic that enables upload streaming can be found within
// UploadStreamFromReader and the StreamShard. As seen at the beginning of the
// big for - loop in UploadStreamFromReader, the streamer currently always
// assumes that the data provided by the user starts at index 0 of chunk 0. In
// every iteration the siafile is grown by a single chunk to prepare for the
// upload of the next chunk. To allow the upload code to repair a chunk from a
// stream, the stream is passed into the unfinished chunk as a new field. If the
// upload code detects a stream, it will use that instead of a local file to
// fetch the chunk's logical data. As soon as the upload code is done fetching
// the logical data, it will close that streamer to signal the loop that it's
// save to upload another chunk.
// This is possible due to the custom StreamShard type which is a wrapper for a
// io.Reader with a channel which is closed when the StreamShard is closed.

// StreamShard is a helper type that allows us to split an io.Reader up into
// multiple readers, wait for the shard to finish reading and then check the
// error for that Read. SignalChan will be closed when the shard has been
// closed.
type StreamShard struct {
	n   uint64
	err error

	r skymodules.ChunkReader

	closed     bool
	mu         sync.Mutex
	signalChan chan struct{}
}

// NewStreamShard creates a new stream shard from a reader.
func NewStreamShard(r skymodules.ChunkReader) *StreamShard {
	return &StreamShard{
		r:          r,
		signalChan: make(chan struct{}),
	}
}

// Peek returns whether the next call to ReadChunk is expected to return a
// chunk or if there is no more data.
func (ss *StreamShard) Peek() bool {
	return ss.r.Peek()
}

// Result returns the returned values of calling Read on the shard.
func (ss *StreamShard) Result() (int, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return int(ss.n), ss.err
}

// ReadChunk implements the ChunkReader interface.
func (ss *StreamShard) ReadChunk() ([][]byte, uint64, error) {
	if ss.closed {
		return nil, 0, errors.New("StreamShard already closed")
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()

	// Read chunk.
	var chunk [][]byte
	chunk, ss.n, ss.err = ss.r.ReadChunk()

	// The chunk is read. Mark the shard as closed.
	ss.closed = true
	close(ss.signalChan)

	return chunk, ss.n, ss.err
}

// UploadStreamFromReader reads from the provided reader until io.EOF is reached
// and upload the data to the Sia network.
func (r *Renter) UploadStreamFromReader(up skymodules.FileUploadParams, reader io.Reader) error {
	if err := r.tg.Add(); err != nil {
		return err
	}
	defer r.tg.Done()

	// Perform the upload, close the filenode, and return.
	fileNode, err := r.callUploadStreamFromReader(r.tg.StopCtx(), up, reader)
	if err != nil {
		return errors.AddContext(err, "unable to stream an upload from a reader")
	}
	return fileNode.Close()
}

// managedInitUploadStream verifies the upload parameters and prepares an empty
// SiaFile for the upload.
func (r *Renter) managedInitUploadStream(up skymodules.FileUploadParams) (*filesystem.FileNode, error) {
	siaPath, ec, force, repair, cipherType := up.SiaPath, up.ErasureCode, up.Force, up.Repair, up.CipherType
	// Check if ec was set. If not use defaults.
	var err error
	if ec == nil && !repair {
		ec = skymodules.NewRSSubCodeDefault()
		up.ErasureCode = ec
	} else if ec != nil && repair {
		return nil, errors.New("can't provide erasure code settings when doing repairs")
	}

	// Make sure that force and repair aren't both set.
	if force && repair {
		return nil, errors.New("'force' and 'repair' can't both be set")
	}

	// Delete existing file if overwrite flag is set. Ignore ErrUnknownPath.
	if force {
		err := r.DeleteFile(siaPath)
		if err != nil && !errors.Contains(err, filesystem.ErrNotExist) {
			return nil, err
		}
	}
	// If repair is set open the existing file.
	if repair {
		entry, err := r.staticFileSystem.OpenSiaFile(siaPath)
		if err != nil {
			return nil, err
		}
		return entry, nil
	}
	// Check that we have contracts to upload to. We need at least data +
	// parity/2 contracts. NumPieces is equal to data+parity, and min pieces is
	// equal to parity. Therefore (NumPieces+MinPieces)/2 = (data+data+parity)/2
	// = data+parity/2.
	numContracts := len(r.staticHostContractor.Contracts())
	requiredContracts := (ec.NumPieces() + ec.MinPieces()) / 2
	if numContracts < requiredContracts && build.Release != "testing" {
		return nil, fmt.Errorf("not enough contracts to upload file: got %v, needed %v", numContracts, (ec.NumPieces()+ec.MinPieces())/2)
	}

	// If there's a cipherKey defined already use that, otherwise generate a new
	// key of the given cipherType.
	cipherKey := up.CipherKey
	if up.CipherKey == nil {
		cipherKey = crypto.GenerateSiaKey(cipherType)
	}

	// Create the Siafile and add to renter
	err = r.staticFileSystem.NewSiaFile(siaPath, up.Source, up.ErasureCode, cipherKey, 0, defaultFilePerm)
	if err != nil {
		return nil, err
	}
	return r.staticFileSystem.OpenSiaFile(siaPath)
}

// callUploadStreamFromReaderWithFileNodeNoBlock reads from the provided reader until
// io.EOF is reached and upload the data to the Sia network. Depending on
// whether backup is true or false, the siafile for the upload will be stored in
// the siafileset or backupfileset.
//
// callUploadStreamFromReader will return as soon as all data to upload is read
// from the reader and passed on to the upload code but before the data is
// available on the network.
func (r *Renter) callUploadStreamFromReaderWithFileNodeNoBlock(ctx context.Context, fileNode *filesystem.FileNode, reader skymodules.ChunkReader, offset int64) (_ []*unfinishedUploadChunk, n int64, err error) {
	// Sanity check offset.
	if offset%int64(fileNode.ChunkSize()) != 0 {
		return nil, 0, fmt.Errorf("callUploadStreamFromReaderWithFileNode called with invalid offset %v mod %v != 0", offset, fileNode.ChunkSize())
	}
	startChunkIndex := uint64(offset) / fileNode.ChunkSize()

	// If Peek is false to start then we are dealing with a zero byte file
	// and should just return as there is nothing to upload to the network.
	if !reader.Peek() {
		return nil, 0, nil
	}

	// Build a map of host public keys.
	pks := make(map[string]types.SiaPublicKey)
	for _, pk := range fileNode.HostPublicKeys() {
		pks[string(pk.Key)] = pk
	}

	// Get the most recent workers.
	hosts := r.managedRefreshHostsAndWorkers()

	// Check if we currently have enough workers for the specified redundancy.
	minWorkers := fileNode.ErasureCode().MinPieces()
	availableWorkers := r.staticWorkerPool.callNumWorkers()
	// Skip this check if testing dependency is used to let the upload fail
	// during upload instead of proactively.
	skip := r.staticDeps.Disrupt("AllowLessThanMinWorkers")
	if availableWorkers < minWorkers && !skip {
		return nil, 0, fmt.Errorf("Need at least %v workers for upload but got only %v", minWorkers, availableWorkers)
	}

	// Read the chunks we want to upload one by one from the input stream using
	// shards. A shard will signal completion after reading the input but
	// before the upload is done.
	var chunks []*unfinishedUploadChunk
	for chunkIndex := startChunkIndex; ; chunkIndex++ {
		// Disrupt the upload by closing the reader and simulating losing
		// connectivity during the upload.
		if r.staticDeps.Disrupt("DisruptUploadStream") {
			c, ok := reader.(io.Closer)
			if ok {
				c.Close()
			}
		}
		// Grow the SiaFile to the right size. Otherwise buildUnfinishedChunk
		// won't realize that there are pieces which haven't been repaired yet.
		if err = fileNode.SiaFile.GrowNumChunks(chunkIndex + 1); err != nil {
			return nil, n, err
		}

		// Start the chunk upload.
		offline, goodForRenew, _, _ := r.callRenterContractsAndUtilities()
		uuc, exists, err := r.managedBuildUnfinishedChunk(ctx, fileNode, chunkIndex, hosts, memoryPriorityHigh, offline, goodForRenew, r.staticUserUploadMemoryManager)
		if err != nil {
			return nil, n, errors.AddContext(err, "unable to fetch chunk for stream")
		}

		// Chunk might already be repairing.
		if exists {
			chunks = append(chunks, uuc)
			continue
		}

		// Create a new shard set it to be the source reader of the chunk.
		ss := NewStreamShard(reader)
		uuc.sourceReader = ss

		// Check if the chunk needs any work or if we can skip it.
		if uuc.piecesCompleted < uuc.staticPiecesNeeded {
			// Add the chunk to the upload heap's repair map.
			existingUUC, pushed, err := r.managedPushChunkForRepair(uuc, chunkTypeStreamChunk)
			if err != nil {
				return nil, n, errors.AddContext(err, "unable to push chunk")
			}
			if !pushed {
				// The chunk wasn't added to the repair map meaning it must have
				// already been in the repair map
				_, _, err := ss.ReadChunk()
				if err != nil {
					return nil, n, errors.AddContext(err, "unable to read pushed chunk")
				}
				// If a uuc already existed, append that instead.
				if existingUUC != nil {
					chunks = append(chunks, existingUUC)
				}
			} else {
				chunks = append(chunks, uuc)
			}
		} else {
			// The chunk doesn't need any work. We still need to read a chunk
			// from the shard though. Otherwise we will upload the wrong chunk
			// for the next chunkIndex. We don't need to check the error though
			// since we check that anyway at the end of the loop.
			_, _, err := ss.ReadChunk()
			if err != nil {
				return nil, n, errors.AddContext(err, "unable to read chunk")
			}
		}
		// Wait for the shard to be read.
		select {
		case <-r.tg.StopChan():
			return nil, n, errors.New("interrupted by shutdown")
		case <-ss.signalChan:
		}

		// If an io.EOF error occurred or less than chunkSize was read, we are
		// done. Otherwise we report the error.
		if ssN, err := ss.Result(); errors.Contains(err, io.EOF) {
			// All chunks successfully submitted.
			n += int64(ssN)
			break
		} else if ss.err != nil {
			return nil, n, ss.err
		}
		n += int64(ss.n)

		// Call Peek to make sure that there's more data for another shard.
		if !ss.Peek() {
			break
		}
	}
	return chunks, n, err
}

// callUploadStreamFromReaderWithFileNode reads from the provided reader until
// io.EOF is reached and upload the data to the Sia network. Depending on
// whether backup is true or false, the siafile for the upload will be stored in
// the siafileset or backupfileset.
//
// callUploadStreamFromReader will return as soon as the data is available on
// the Sia network, this will happen faster than the entire upload is complete -
// the streamer may continue uploading in the background after returning while
// it is boosting redundancy.
func (r *Renter) callUploadStreamFromReaderWithFileNode(ctx context.Context, fileNode *filesystem.FileNode, reader skymodules.ChunkReader, offset int64) (n int64, err error) {
	// Start upload.
	chunks, n, err := r.callUploadStreamFromReaderWithFileNodeNoBlock(ctx, fileNode, reader, offset)
	if err != nil {
		return n, err
	}
	// Wait for all chunks to become available.
	for _, chunk := range chunks {
		select {
		case <-r.tg.StopChan():
			err = errors.New("upload timed out, renter has shutdown")
		case <-chunk.staticAvailableChan:
			chunk.mu.Lock()
			err = chunk.err
			chunk.mu.Unlock()
		}
		if err != nil {
			return n, errors.AddContext(err, "upload streamer failed to get all data available")
		}
	}
	// Disrupt to force an error and ensure the fileNode is being closed
	// correctly.
	if r.staticDeps.Disrupt("failUploadStreamFromReader") {
		return n, errors.New("disrupted by failUploadStreamFromReader")
	}
	return n, nil
}

// callUploadStreamFromReader reads from the provided reader until io.EOF is
// reached and upload the data to the Sia network. Depending on whether backup
// is true or false, the siafile for the upload will be stored in the siafileset
// or backupfileset.
//
// callUploadStreamFromReader will return as soon as the data is available on
// the Sia network, this will happen faster than the entire upload is complete -
// the streamer may continue uploading in the background after returning while
// it is boosting redundancy.
func (r *Renter) callUploadStreamFromReader(ctx context.Context, up skymodules.FileUploadParams, reader io.Reader) (fileNode *filesystem.FileNode, err error) {
	// Check the upload params first.
	fileNode, err = r.managedInitUploadStream(up)
	if err != nil {
		return nil, err
	}
	chunkReader := NewChunkReader(reader, fileNode.ErasureCode(), fileNode.MasterKey())
	_, err = r.callUploadStreamFromReaderWithFileNode(ctx, fileNode, chunkReader, 0)
	if err != nil {
		// Delete the file if the upload wasn't successful.
		//
		// TODO: File is not being deleted??
		err = errors.Compose(err, fileNode.Close())
		return nil, err
	}
	return fileNode, nil
}
