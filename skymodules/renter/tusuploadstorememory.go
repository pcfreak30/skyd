package renter

import (
	"os"
	"time"

	"github.com/tus/tusd/pkg/handler"
	"github.com/tus/tusd/pkg/memorylocker"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

// NewSkynetTUSInMemoryUploadStore creates a new skynetTUSInMemoryUploadStore.
func NewSkynetTUSInMemoryUploadStore() skymodules.SkynetTUSUploadStore {
	return &skynetTUSInMemoryUploadStore{
		uploads:      make(map[string]*skynetTUSUpload),
		staticLocker: memorylocker.New(),
	}
}

// Close implements the io.Closer and is a no-op for the in-memory store.
func (us *skynetTUSInMemoryUploadStore) Close() error { return nil }

func (us *skynetTUSInMemoryUploadStore) CreateUpload(fi handler.FileInfo, sup skymodules.SkyfileUploadParameters, up skymodules.FileUploadParams, sm skymodules.SkyfileMetadata) (skymodules.SkynetTUSUpload, error) {
	upload := &skynetTUSUpload{
		fi:        fi,
		lastWrite: time.Now(),
		sm:        sm,
		staticSUP: sup,
		staticUP:  up,
	}
	return upload, us.SaveUpload(fi.ID, upload)
}

// NewLock implements handler.Locker by forwarding the call to an in-memory
// locker.
func (us *skynetTUSInMemoryUploadStore) NewLock(id string) (handler.Lock, error) {
	return us.staticLocker.NewLock(id)
}

// SaveUpload saves an upload.
func (us *skynetTUSInMemoryUploadStore) SaveUpload(id string, u skymodules.SkynetTUSUpload) error {
	us.mu.Lock()
	defer us.mu.Unlock()
	upload, ok := u.(*skynetTUSUpload)
	if !ok {
		err := errors.New("SaveUpload: can't store a non *skynetTUSUpload")
		build.Critical(err)
		return err
	}
	us.uploads[id] = upload
	return nil
}

// Upload returns an upload by ID.
func (us *skynetTUSInMemoryUploadStore) Upload(id string) (skymodules.SkynetTUSUpload, error) {
	us.mu.Lock()
	defer us.mu.Unlock()
	upload, exists := us.uploads[id]
	if !exists {
		return nil, os.ErrNotExist
	}
	return upload, nil
}

// Prune removes uploads that have been idle for too long.
func (us *skynetTUSInMemoryUploadStore) ToPrune() ([]skymodules.SkynetTUSUpload, error) {
	us.mu.Lock()
	defer us.mu.Unlock()
	var toDelete []skymodules.SkynetTUSUpload
	for _, u := range us.uploads {
		u.mu.Lock()
		lastWrite := u.lastWrite
		complete := u.complete
		u.mu.Unlock()
		if time.Since(lastWrite) < PruneTUSUploadTimeout {
			continue // nothing to do
		}
		// If the upload wasn't completed, delete the files on disk.
		if !complete {
			toDelete = append(toDelete, u)
		}
	}
	return toDelete, nil
}

// Prune removes uploads that have been idle for too long.
func (us *skynetTUSInMemoryUploadStore) Prune(toPrune skymodules.SkynetTUSUpload) error {
	us.mu.Lock()
	defer us.mu.Unlock()
	upload := toPrune.(*skynetTUSUpload)
	_ = upload.Close()
	delete(us.uploads, upload.fi.ID)
	return nil
}
