package renter

import (
	"os"
	"sync"
	"time"

	"github.com/tus/tusd/pkg/handler"
	"github.com/tus/tusd/pkg/memorylocker"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

// skynetTUSUploadStore defines an interface for a storage backend that is
// capable of storing upload information as well as locking uploads and pruning
// them.
type skynetTUSUploadStore interface {
	Prune() error
	SaveUpload(upload *skynetTUSUpload) error
	Upload(id string) (*skynetTUSUpload, error)

	handler.Locker
}

// newSkynetTUSInMemoryUploadStore creates a new skynetTUSInMemoryUploadStore.
func newSkynetTUSInMemoryUploadStore(r *Renter) *skynetTUSInMemoryUploadStore {
	return &skynetTUSInMemoryUploadStore{
		uploads:      make(map[string]*skynetTUSUpload),
		staticLocker: memorylocker.New(),
		staticRenter: r,
	}
}

// skynetTUSInMemoryUploadStore is an in-memory skynetTUSUploadStore
// implementation.
type skynetTUSInMemoryUploadStore struct {
	uploads      map[string]*skynetTUSUpload
	mu           sync.Mutex
	staticRenter *Renter
	staticLocker *memorylocker.MemoryLocker
}

// NewLock implements handler.Locker by forwarding the call to an in-memory
// locker.
func (us *skynetTUSInMemoryUploadStore) NewLock(id string) (handler.Lock, error) {
	return us.staticLocker.NewLock(id)
}

// SaveUpload saves an upload.
func (us *skynetTUSInMemoryUploadStore) SaveUpload(u *skynetTUSUpload) error {
	us.mu.Lock()
	defer us.mu.Unlock()
	us.uploads[u.fi.ID] = u
	return nil
}

// Upload returns an upload by ID.
func (us *skynetTUSInMemoryUploadStore) Upload(id string) (*skynetTUSUpload, error) {
	us.mu.Lock()
	defer us.mu.Unlock()
	upload, exists := us.uploads[id]
	if !exists {
		return nil, os.ErrNotExist
	}
	return upload, nil
}

// Prune removes uploads that have been idle for too long.
func (us *skynetTUSInMemoryUploadStore) Prune() error {
	us.mu.Lock()
	var toDelete []skymodules.SiaPath
	for id, upload := range us.uploads {
		upload.mu.Lock()
		lastWrite := upload.lastWrite
		complete := upload.complete
		upload.mu.Unlock()
		if time.Since(lastWrite) < PruneTUSUploadTimeout {
			continue // nothing to do
		}
		// Prune
		_ = upload.Close()
		delete(us.uploads, id)

		// If the upload wasn't completed, delete the files on disk.
		if !complete {
			toDelete = append(toDelete, upload.staticSUP.SiaPath)
			toDelete = append(toDelete, upload.staticUP.SiaPath)
		}
	}
	us.mu.Unlock()

	// Delete files outside of lock.
	for _, sp := range toDelete {
		_ = us.staticRenter.DeleteFile(sp)
	}
	return nil
}
