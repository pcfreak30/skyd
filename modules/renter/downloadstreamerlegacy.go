package renter

// downloadstreamerlegacy is the streamer that gets used for files which do not
// have support for partial downloads. This streamer will provide speedups
// similar to the streaming cache speedups that were in place when we were doing
// caching before partial downloads were available, but not better.
//
// This type of cacheing needs to be supported for backwards comaptibility
// reasons, however there is no intention to optimize this streamer further.
// Because this code is destined always to be rather slow, a log.Println is used
// to log a warning that download streaming with a legacy file is not
// recommended.
//
// The key criteria for targeting this type of streamer is whether or not the
// file supports partial downloads. If the file does not support partial
// downloads, this streamer is used. If the file does support partial downloads,
// a more optimized streamer is used.

import (
	"bytes"
	"io"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/errors"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/renter/siafile"
)

type (
	// streamerLegacy is a modules.Streamer that can be used to stream downloads
	// from the sia network for files that do no support partial downloads.
	streamerLegacy struct {
		// Reader variables. The snapshot is a snapshot of the file as it
		// existed when it was opened, something that we do to give the streamer
		// a consistent view of the file even if the file is being actively
		// updated. Having this snapshot also isolates the reader from events
		// such as name changes and deletions.
		//
		// We also keep the full file entry as it allows us to update metadata
		// items in the file such as the access time.
		staticFile *siafile.Snapshot
		offset     int64
		r          *Renter

		// The cache is a single chunk fetched from the remote file and kept
		// locally. The chunk will be kept until the offset of the streamer is
		// outside of the chunk, at which point a new chunk will be fetched to
		// be the cache.
		cache       []byte // The cache itself.
		cacheOffset int64  // Offset within the file that the cache starts at.
		readErr     error  // Error that prevented the cache from loading if there was an issue.

		// Used to ensure thread safety between calls to the reader.
		mu sync.Mutex
	}
)

// fillCache will determine whether or not the cache of the streamer needs to be
// filled, and if it does it will add data to the streamer.
func (s *streamerLegacy) fillCache() {
	// Helper variables used throughout the function.
	cacheOffset := s.cacheOffset
	streamOffset := s.offset
	cacheLen := int64(len(s.cache))
	readErr := s.readErr
	fileSize := int64(s.staticFile.Size())
	chunkSize := s.staticFile.ChunkSize()

	// If there has been a read error in the stream, abort.
	if readErr != nil {
		return
	}
	// Check whether the cache has reached the end of the file and also the
	// streamOffset is contained within the cache. If so, no updates are needed.
	if cacheOffset <= streamOffset && cacheOffset+cacheLen == fileSize {
		return
	}
	// If the cache from the previous fetch still has data, return false.
	if cacheOffset <= streamOffset && streamOffset < cacheOffset+cacheLen {
		return
	}

	// Determine what data to fetch. The previous cache has been fully used up,
	// so a new chunk needs to be fetched. Check that the fetch request does not
	// exceed the bounds of the file.
	chunkIndex, _ := s.staticFile.ChunkIndexByOffset(uint64(streamOffset))
	fetchOffset := int64(chunkIndex * chunkSize)
	fetchLen := int64(chunkSize)
	if fetchOffset+fetchLen > fileSize {
		fetchLen = fileSize - fetchOffset
	}

	// Perform the download.
	buffer := bytes.NewBuffer([]byte{})
	ddw := newDownloadDestinationWriter(buffer)
	d, err := s.r.managedNewDownload(downloadParams{
		destination:       ddw,
		destinationType:   destinationTypeSeekStream,
		destinationString: "httpresponse",
		file:              s.staticFile,

		latencyTarget: 250 * time.Millisecond,
		length:        uint64(fetchLen),
		needsMemory:   true,
		offset:        uint64(fetchOffset),
		overdrive:     3,
		priority:      100,
	})
	if err != nil {
		closeErr := ddw.Close()
		s.readErr = errors.Compose(s.readErr, err, closeErr)
		s.r.log.Println("Error downloading for stream file:", s.readErr)
		return
	}
	// When the download finishes, close the download destination buffer.
	d.OnComplete(func(_ error) error {
		return ddw.Close()
	})

	// Block until the download has completed.
	select {
	case <-d.completeChan:
		err := d.Err()
		if err != nil {
			completeErr := errors.AddContext(err, "download failed")
			s.readErr = errors.Compose(s.readErr, completeErr)
			s.r.log.Println("Error during stream download:", s.readErr)
			return
		}
	case <-s.r.tg.StopChan():
		s.readErr = errors.New("download interrupted by shutdown")
		return
	}

	// Update the cache.
	s.cache = buffer.Bytes()
	s.cacheOffset = fetchOffset
	return
}

// Close closes the streamer.
func (s *streamerLegacy) Close() error {
	return nil
}

// Read will check the stream cache for the data that is being requested. If the
// data is fully or partially there, Read will return what data is available
// without error. If the data is not there, Read will issue a call to fill the
// cache and then block until the data is at least partially available.
func (s *streamerLegacy) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Start by tring to fill the cache. If the cache already contains some of
	// the requested data, or if there has already been an error fetching data,
	// the cache fill will no-op.
	s.fillCache()

	// Check if there has been an error reading the file.
	if s.readErr != nil {
		return 0, s.readErr
	}

	// Determine what part of the cache to read.
	dataStart := int(s.offset - s.cacheOffset)
	dataEnd := dataStart + len(p)
	if dataEnd > len(s.cache) {
		dataEnd = len(s.cache)
	}

	// Copy the cache into the output buffer and return the amount of data
	// provided.
	copy(p, s.cache[dataStart:dataEnd])
	s.offset += int64(dataEnd - dataStart)
	return dataEnd - dataStart, nil
}

// Seek sets the offset for the next Read to offset, interpreted according to
// whence: SeekStart means relative to the start of the file, SeekCurrent means
// relative to the current offset, and SeekEnd means relative to the end. Seek
// returns the new offset relative to the start of the file and an error, if
// any.
func (s *streamerLegacy) Seek(offset int64, whence int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = 0
	case io.SeekCurrent:
		newOffset = s.offset
	case io.SeekEnd:
		newOffset = int64(s.staticFile.Size())
	}
	newOffset += offset
	if newOffset < 0 {
		return s.offset, errors.New("cannot seek to negative offset")
	}

	// Update the offset of the stream and immediately send a thread to update
	// the cache.
	s.offset = newOffset
	return newOffset, nil
}

// managedStreamerLegacy creates a streamer from a siafile snapshot and starts
// filling its cache.
func (r *Renter) managedStreamerLegacy(snapshot *siafile.Snapshot) modules.Streamer {
	r.log.Println("WARN: Legacy streamer is being used to stream", snapshot.SiaPath(), "file does not support partial downloads. This will significantly harm performance")
	s := &streamer{
		staticFile: snapshot,
		r:          r,

		activateCache: make(chan struct{}),
		cacheReady:    make(chan struct{}),
	}
	return s
}
