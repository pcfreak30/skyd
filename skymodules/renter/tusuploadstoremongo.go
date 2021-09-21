package renter

import (
	"context"
	"time"

	lock "github.com/square/mongo-lock"
	"github.com/tus/tusd/pkg/handler"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
	"go.sia.tech/siad/crypto"
)

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
		ctx context.Context

		staticClient         *mongo.Client
		staticLockClient     *lock.Client
		staticPortalHostname string
	}

	mongoTUSUpload struct {
		ID         string    `bson:"_id"`
		Complete   bool      `bson:"complete"`
		LastWrite  time.Time `bson:"lastwrite"`
		PortalName string    `bson:"portalname"`

		FanoutBytes []byte             `bson:"fanoutbytes"`
		FileInfo    handler.FileInfo   `bson:"fileinfo"`
		FileName    string             `bson:"filename"`
		SiaPath     skymodules.SiaPath `bson:"siapath"`

		BaseChunkRedundancy uint8             `bson:"basechunkredundancy"`
		Metadata            []byte            `bson:"metadata"`
		FanoutDataPieces    int               `bson:"fanoutdatapieces"`
		FanoutParityPieces  int               `bson:"fanoutparitypieces"`
		CipherType          crypto.CipherType `bson:"ciphertype"`
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
	return &skynetMongoLock{
		staticClient:         us.staticLockClient,
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

// ToPrune returns the uploads which should be pruned by the portal.
func (us *skynetTUSMongoUploadStore) ToPrune(ctx context.Context) ([]skymodules.SkynetTUSUpload, error) {
	c := us.staticUploadCollection()

	filter := bson.M{
		"lastwrite": bson.M{
			"$lt": time.Now().Add(-PruneTUSUploadTimeout),
		},
		"complete":   false,
		"portalname": us.staticPortalHostname,
	}
	// Find uploads.
	cursor, err := c.Find(ctx, filter, options.Find().SetBatchSize(100))
	if err != nil {
		return nil, err
	}
	// Decode uploads.
	var uploads []skymodules.SkynetTUSUpload
	for cursor.Next(ctx) {
		var upload mongoTUSUpload
		if err := cursor.Decode(&upload); err != nil {
			return nil, err
		}
		uploads = append(uploads, &upload)
	}
	return uploads, nil
}

func (us *skynetTUSMongoUploadStore) Prune(string) error {
	purger := lock.NewPurger(us.staticLockClient)
	_, err := purger.Purge(us.ctx)
	if err != nil {
		return errors.AddContext(err, "failed to purge old locks")
	}
	panic("not implemented yet")
}

// CreateUpload creates a new upload and adds it to the store.
func (us *skynetTUSMongoUploadStore) CreateUpload(ctx context.Context, fi handler.FileInfo, sp skymodules.SiaPath, fileName string, baseChunkRedundancy uint8, fanoutDataPieces, fanoutParityPieces int, sm []byte, ct crypto.CipherType) (skymodules.SkynetTUSUpload, error) {
	upload := &mongoTUSUpload{
		ID:          fi.ID,
		Complete:    false,
		FanoutBytes: nil,
		FileInfo:    fi,
		LastWrite:   time.Now(),
		FileName:    fileName,
		SiaPath:     sp,

		BaseChunkRedundancy: baseChunkRedundancy,
		Metadata:            sm,
		PortalName:          us.staticPortalHostname,

		FanoutDataPieces:   fanoutDataPieces,
		FanoutParityPieces: fanoutParityPieces,
		CipherType:         ct,
	}
	// Insert into db.
	_, err := us.staticUploadCollection().InsertOne(ctx, upload)
	if err != nil {
		return nil, errors.AddContext(err, "failed to insert new upload into db")
	}
	return upload, nil
}

func (us *skynetTUSMongoUploadStore) GetUpload(_ context.Context, id string) (skymodules.SkynetTUSUpload, error) {
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

// Skylink returns the upload's skylink if available already.
func (u *mongoTUSUpload) Skylink() (skymodules.Skylink, bool) {
	panic("not implemented yet")
}

// GetInfo returns the FileInfo of the upload.
func (u *mongoTUSUpload) GetInfo(ctx context.Context) (handler.FileInfo, error) {
	panic("not implemented yet")
}

// IsSmallUpload indicates whether the upload is considered a
// small upload. That means the upload contained less than a
// chunksize of data.
func (u *mongoTUSUpload) IsSmallUpload(ctx context.Context) (bool, error) {
	panic("not implemented yet")
}

// PruneInfo returns the info required to prune uploads.
func (u *mongoTUSUpload) PruneInfo(ctx context.Context) (id string, sp skymodules.SiaPath, err error) {
	panic("not implemented yet")
}

// UploadParams returns the upload parameters used for the
// upload.
func (u *mongoTUSUpload) UploadParams(ctx context.Context) (skymodules.SkyfileUploadParameters, skymodules.FileUploadParams, error) {
	panic("not implemented yet")
}

// CommitWriteChunkSmallFile commits writing a chunk of a small
// file.
func (u *mongoTUSUpload) CommitWriteChunkSmallFile(newOffset int64, newLastWrite time.Time, smallUploadData []byte) error {
	panic("not implemented yet")
}

// CommitWriteChunk commits writing a chunk of either a small or
// large file with fanout.
func (u *mongoTUSUpload) CommitWriteChunk(newOffset int64, newLastWrite time.Time, isSmall bool, fanout []byte) error {
	panic("not implemented yet")
}

// CommitFinishUpload commits a finalised upload.
func (u *mongoTUSUpload) CommitFinishUpload(skylink skymodules.Skylink) error {
	panic("not implemented yet")
}

// CommitFinishPartialUpload commits a finalised partial upload.
func (u *mongoTUSUpload) CommitFinishPartialUpload() error {
	panic("not implemented yet")
}

// Fanout returns the fanout of the upload. Should only be
// called once it's done uploading.
func (u *mongoTUSUpload) Fanout(ctx context.Context) ([]byte, error) {
	panic("not implemented yet")
}

// SkyfileMetadata returns the metadata of the upload. Should
// only be called once it's done uploading.
func (u *mongoTUSUpload) SkyfileMetadata(ctx context.Context) ([]byte, error) {
	panic("not implemented yet")
}

// SmallFileData returns the data to upload for a small file
// upload.
func (u *mongoTUSUpload) SmallFileData(ctx context.Context) ([]byte, error) {
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
	// guarantees here for the sake of performance. A read happens on the
	// local mongodb instance and writes are only required to propagate to 1
	// other instance.
	opts := options.Client().
		ApplyURI(uri).
		SetAuth(creds).
		SetReadConcern(readconcern.Local())
	if build.Release == "release" {
		opts = opts.SetWriteConcern(writeconcern.New(writeconcern.W(1)))
	}

	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, err
	}

	// Create store.
	us := &skynetTUSMongoUploadStore{
		staticClient:         client,
		staticPortalHostname: portalName,
	}

	// Create lock client.
	lockClient := lock.NewClient(us.staticLockCollection())
	err = lockClient.CreateIndexes(ctx)
	if err != nil {
		return nil, err
	}
	us.staticLockClient = lockClient
	return us, nil
}
