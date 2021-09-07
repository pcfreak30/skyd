package renter

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/tus/tusd/pkg/handler"
	"github.com/tus/tusd/pkg/memorylocker"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.mongodb.org/mongo-driver/mongo"
)

type skynetTUSMongoUploadStore struct {
	staticClient *mongo.Client
}

func (us *skynetTUSMongoUploadStore) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return us.staticClient.Disconnect(ctx)
}

func (us *skynetTUSMongoUploadStore) NewLock(id string) (handler.Lock, error) {
	panic("not implemented yet")
}

func (us *skynetTUSMongoUploadStore) ToPrune() ([]skymodules.SkynetTUSUpload, error) {
	panic("not implemented yet")
}

func (us *skynetTUSMongoUploadStore) Prune(skymodules.SkynetTUSUpload) error {
	panic("not implemented yet")
}

func (us *skynetTUSMongoUploadStore) SaveUpload(id string, upload skymodules.SkynetTUSUpload) error {
	panic("not implemented yet")
}

func (us *skynetTUSMongoUploadStore) Upload(id string) (skymodules.SkynetTUSUpload, error) {
	panic("not implemented yet")
}

type skynetTUSMongoUpload struct {
	ID     string `bson:"_id"`
	LockID string `bson:"lockid"`
}

// NewSkynetTUSInMemoryUploadStore creates a new skynetTUSInMemoryUploadStore.
func NewSkynetTUSInMemoryUploadStore() skymodules.SkynetTUSUploadStore {
	return &skynetTUSInMemoryUploadStore{
		uploads:      make(map[string]*skynetTUSUpload),
		staticLocker: memorylocker.New(),
	}
}

// NewSkynetTUSMongoUploadStore creates a new upload store using a mongodb as
// the storage backend.
func NewSkynetTUSMongoUploadStore(client *mongo.Client) (skymodules.SkynetTUSUploadStore, error) {
	return newSkynetTUSMongoUploadStore(client)
}

// newSkynetTUSMongoUploadStore creates a new upload store using a mongodb as
// the storage backend.
func newSkynetTUSMongoUploadStore(client *mongo.Client) (*skynetTUSMongoUploadStore, error) {
	return &skynetTUSMongoUploadStore{
		staticClient: client,
	}, nil
}

// skynetTUSInMemoryUploadStore is an in-memory skynetTUSUploadStore
// implementation.
type skynetTUSInMemoryUploadStore struct {
	uploads      map[string]*skynetTUSUpload
	mu           sync.Mutex
	staticLocker *memorylocker.MemoryLocker
}

func (u *skynetTUSUpload) SiaPath() skymodules.SiaPath {
	return u.staticSUP.SiaPath
}

func (u *skynetTUSUpload) Skylink() (skymodules.Skylink, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	_, exists := u.fi.MetaData["Skylink"]
	return u.sl, exists
}

// Close implements the io.Closer and is a no-op for the in-memory store.
func (us *skynetTUSInMemoryUploadStore) Close() error { return nil }

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
