package renter

import (
	"context"
	"sync"
	"time"

	lock "github.com/square/mongo-lock"
	"github.com/tus/tusd/pkg/handler"
	"github.com/tus/tusd/pkg/memorylocker"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

// TODO: Implement pruning for locks.

const (
	// mongoLockTTL is the time-to-live in seconds for a lock in the
	// mongodb. After that time passes, an entry is no longer considered
	// locked. This avoids deadlocks in case a server locks an entry and
	// then crashes before unlocking it.
	mongoLockTTL = 300 // 5 minutes

	// tusDBName is the name of the database all TUS related data is stored
	// in.
	tusDBName = "tus"

	// tusUploadsMongoCollectionName is the name of the collection within
	// the database used to store upload info.
	tusUploadsMongoCollectionName = "uploads"

	// tusLocksMongoCollectionName is the name of the collection within the
	// database used to store locks.
	tusLocksMongoCollectionName = "locks"
)

type (
	skynetTUSMongoUploadStore struct {
		staticClient         *mongo.Client
		staticPortalHostname string
	}

	mongoTUSUpload struct {
		ID string `bson:"_id"`
	}

	// skynetMongoLock is a lock used for locking an upload.
	skynetMongoLock struct {
		staticClient         *lock.Client
		staticPortalHostname string
		staticUploadID       string
	}
)

func (us *skynetTUSMongoUploadStore) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return us.staticClient.Disconnect(ctx)
}

// NewLock creates a new lock for the upload with the given ID.
func (us *skynetTUSMongoUploadStore) NewLock(uploadID string) (handler.Lock, error) {
	client := lock.NewClient(us.staticLockCollection())
	err := client.CreateIndexes(context.Background())
	if err != nil {
		return nil, err
	}
	return &skynetMongoLock{
		staticClient:         client,
		staticPortalHostname: us.staticPortalHostname,
		staticUploadID:       uploadID,
	}, nil
}

// Lock exclusively locks the lock. It returns handler.ErrFileLocked if the
// upload is already locked and it will put an expiration time on the lock in
// case the server dies while the file is locked. That way uploads won't remain
// locked forever.
func (l *skynetMongoLock) Lock() error {
	client := l.staticClient
	ld := lock.LockDetails{
		Owner: "TUS",
		Host:  l.staticPortalHostname,
		TTL:   mongoLockTTL,
	}
	err := client.XLock(context.Background(), l.staticUploadID, l.staticUploadID, ld)
	if err == lock.ErrAlreadyLocked {
		return handler.ErrFileLocked
	}
	return err
}

// Unlock attempts to unlock an upload. It will retry doing so for a certain
// time before giving up.
func (l *skynetMongoLock) Unlock() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var err error
LOOP:
	for {
		_, err = l.staticClient.Unlock(context.Background(), l.staticUploadID)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			break LOOP
		case <-time.After(time.Second):
		}
	}
	build.Critical("Failed to unlock the lock", err)
	return err
}

func (us *skynetTUSMongoUploadStore) ToPrune() ([]skymodules.SkynetTUSUpload, error) {
	panic("not implemented yet")
}

func (us *skynetTUSMongoUploadStore) Prune(skymodules.SkynetTUSUpload) error {
	panic("not implemented yet")
}

func (us *skynetTUSMongoUploadStore) CreateUpload(fi handler.FileInfo, sup skymodules.SkyfileUploadParameters, up skymodules.FileUploadParams, sm skymodules.SkyfileMetadata) (skymodules.SkynetTUSUpload, error) {
	panic("not implemented yet")
}

func (us *skynetTUSMongoUploadStore) SaveUpload(id string, upload skymodules.SkynetTUSUpload) error {
	panic("not implemented yet")
}

func (us *skynetTUSMongoUploadStore) staticUploadCollection() *mongo.Collection {
	return us.staticClient.Database(tusDBName).Collection(tusUploadsMongoCollectionName)
}

func (us *skynetTUSMongoUploadStore) staticLockCollection() *mongo.Collection {
	return us.staticClient.Database(tusDBName).Collection(tusLocksMongoCollectionName)
}

func (us *skynetTUSMongoUploadStore) Upload(id string) (skymodules.SkynetTUSUpload, error) {
	panic("not implemented yet")
}

// NewSkynetTUSMongoUploadStore creates a new upload store using a mongodb as
// the storage backend.
func NewSkynetTUSMongoUploadStore(ctx context.Context, uri, portalName string, creds options.Credential) (skymodules.SkynetTUSUploadStore, error) {
	return newSkynetTUSMongoUploadStore(ctx, uri, portalName, creds)
}

// newSkynetTUSMongoUploadStore creates a new upload store using a mongodb as
// the storage backend.
func newSkynetTUSMongoUploadStore(ctx context.Context, uri, portalName string, creds options.Credential) (*skynetTUSMongoUploadStore, error) {
	// NOTE: Since we know that in the case of a success, an upload will
	// only ever exist on a single portal, we choose very loose consistency
	// guarantees here for the sake of performance. The worst thing that can
	// happen if a user continues uploading from an outdated portal is
	// redundant pinning.
	opts := options.Client().
		ApplyURI(uri).
		SetAuth(creds).
		SetReadConcern(readconcern.Local()).
		SetWriteConcern(writeconcern.New(writeconcern.J(true)))
	client, err := mongo.Connect(ctx, opts)
	return &skynetTUSMongoUploadStore{
		staticClient:         client,
		staticPortalHostname: portalName,
	}, err
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
