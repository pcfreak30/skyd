package renter

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/tus/tusd/pkg/handler"
	"github.com/tus/tusd/pkg/memorylocker"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

type skynetInMemoryUpload struct {
	complete  bool
	fi        handler.FileInfo
	lastWrite time.Time

	// Skyfile upload parameters.
	baseSectorUploadParams skymodules.SkyfileUploadParameters
	fanoutUploadParams     skymodules.FileUploadParams

	mu sync.Mutex
}

// NewSkynetTUSInMemoryUploadStore creates a new skynetTUSInMemoryUploadStore.
func NewSkynetTUSInMemoryUploadStore() skymodules.SkynetTUSUploadStore {
	return &skynetTUSInMemoryUploadStore{
		uploads:      make(map[string]*skynetInMemoryUpload),
		staticLocker: memorylocker.New(),
	}
}

// Close implements the io.Closer and is a no-op for the in-memory store.
func (us *skynetTUSInMemoryUploadStore) Close() error { return nil }

func (us *skynetTUSInMemoryUploadStore) CreateUpload(fi handler.FileInfo, sup skymodules.SkyfileUploadParameters, up skymodules.FileUploadParams, sm skymodules.SkyfileMetadata) (skymodules.SkynetTUSUpload, error) {
	us.mu.Lock()
	defer us.mu.Unlock()

	upload := &skynetInMemoryUpload{
		fi:        fi,
		lastWrite: time.Now(),
		sm:        sm,
		staticSUP: sup,
		staticUP:  up,
	}
	us.uploads[id] = upload
	return upload, nil
}

func (u *skynetInMemoryUpload) CommitFinishUpload(skylink skymodules.Skylink) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.complete = true
	u.fi.MetaData["Skylink"] = skylink.String()
	return nil
}

// NewLock implements handler.Locker by forwarding the call to an in-memory
// locker.
func (us *skynetTUSInMemoryUploadStore) NewLock(id string) (handler.Lock, error) {
	return us.staticLocker.NewLock(id)
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

func (us *skynetTUSInMemoryUploadStore) UploadParams(ctx context.Context) (skymodules.SkyfileUploadParameters, skymodules.FileUploadParams, error) {
	panic("not implemented yet")
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
