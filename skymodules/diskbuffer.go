package skymodules

import (
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
)

type (
	// DiskBuffer defines the interface for a buffer that stores its contents on
	// disk. All the data written to it will be written to a file without
	// syncing. After the last bit of data is written to the buffer it needs to
	// be closed with Close and after io.EOF is returned by the buffer Cleanup
	// needs to be called to remove it from disk.
	DiskBuffer interface {
		io.ReadWriteCloser

		// Cleanup removes the buffer from disk. This should be called by the
		// reading side when it is clear that no more data will be read from the
		// buffer.
		Cleanup() error
	}

	// diskBuffer implements the DiskBuffer interface.
	diskBuffer struct {
		staticFile      *os.File
		staticWriteChan chan struct{}

		closed      bool
		remaining   int
		writeOffset int64
		mu          sync.Mutex
	}
)

// NewDiskBuffer creates a new disk buffer.
func NewDiskBuffer() (DiskBuffer, error) {
	return newDiskBuffer(os.TempDir())
}

// newDiskBuffer creates a disk buffer in the given dir.
func newDiskBuffer(dir string) (*diskBuffer, error) {
	path := filepath.Join(dir, hex.EncodeToString(fastrand.Bytes(20)))
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &diskBuffer{
		staticFile:      f,
		staticWriteChan: make(chan struct{}, 1),
	}, nil
}

// Close closes the buffer. This should be called by the writing side when it is
// clear that no more data is written to disk. It will unblock pending readers.
func (db *diskBuffer) Close() error {
	// unblock all future reads
	close(db.staticWriteChan)
	return nil
}

// Cleanup removes the buffer from disk. This should be called by the reading
// side when it is clear that no more data will be read from the buffer.
func (db *diskBuffer) Cleanup() error {
	// close underlying file and remove it.
	errClose := db.staticFile.Close()
	errRemove := os.Remove(db.staticFile.Name())
	return errors.Compose(errClose, errRemove)
}

// Read implements io.Reader. It blocks if not enough data is available in the
// buffer. Unless the buffer is closed. In that case it returns the remaining
// data and nil. Subsequent calls will return 0, io.EOF.
func (db *diskBuffer) Read(b []byte) (int, error) {
	// If no data is requested return right away.
	if len(b) == 0 {
		return 0, nil
	}
	for {
		// If more data is requested than available in the buffer, block.
		db.mu.Lock()
		remaining := db.remaining
		db.mu.Unlock()
		if len(b) > remaining {
			select {
			case <-db.staticWriteChan:
			}
		}

		// If after blocking the buffer is empty, return io.EOF.
		db.mu.Lock()
		if db.remaining == 0 {
			db.mu.Unlock()
			return 0, io.EOF
		}

		// If after blocking, the amount of data didn't increase, the buffer was
		// closed. Read exactly what's left in the buffer.
		if remaining == db.remaining {
			db.remaining = 0
			db.mu.Unlock()
			return io.ReadFull(db.staticFile, b[:remaining])
		}

		// If there is still not enough data, wait for the next write.
		if db.remaining < len(b) {
			db.mu.Unlock()
			continue
		}

		// Otherwise, read the data.
		db.remaining -= len(b)
		db.mu.Unlock()
		return io.ReadFull(db.staticFile, b)
	}
}

// Write writes data to the buffer. This is not blocking
func (db *diskBuffer) Write(b []byte) (int, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	// Write the data to the file.
	n, err := db.staticFile.WriteAt(b, db.writeOffset)
	if err != nil {
		return n, err
	}

	// Add the written data to the remaining bytes.
	db.remaining += n
	db.writeOffset += int64(n)

	// Notify the write channel of the new bytes.
	select {
	case db.staticWriteChan <- struct{}{}:
	default:
	}
	return n, nil
}
