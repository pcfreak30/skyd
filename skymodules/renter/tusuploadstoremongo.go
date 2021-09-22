package renter

import (
	"context"
	"os"
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
		ID          string    `bson:"_id"`
		Complete    bool      `bson:"complete"`
		LastWrite   time.Time `bson:"lastwrite"`
		PortalNames []string  `bson:"portalnames"`

		FanoutBytes []byte             `bson:"fanoutbytes"`
		FileInfo    handler.FileInfo   `bson:"fileinfo"`
		FileName    string             `bson:"filename"`
		SiaPath     skymodules.SiaPath `bson:"siapath"`

		BaseChunkRedundancy uint8             `bson:"basechunkredundancy"`
		Metadata            []byte            `bson:"metadata"`
		FanoutDataPieces    int               `bson:"fanoutdatapieces"`
		FanoutParityPieces  int               `bson:"fanoutparitypieces"`
		CipherType          crypto.CipherType `bson:"ciphertype"`

		IsSmallFile     bool   `bson:"issmallfile"`
		SmallUploadData []byte `bson:"smalluploaddata"`

		staticUploads *mongo.Collection
	}

	// skynetMongoLock is a lock used for locking an upload.
	skynetMongoLock struct {
		staticClient         *lock.Client
		staticPortalHostname string
		staticUploadID       string
	}
)

// Close closes the upload store and disconnects it from the backend.
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
		"complete": false,
		"$or": bson.A{
			bson.M{"portalnames": us.staticPortalHostname},
			bson.M{"portalnames": bson.M{"$size": 0}},
		},
	}
	// Find uploads.
	cursor, err := c.Find(ctx, filter)
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

// Prune prunes the uploads with the provided ids from the store.
func (us *skynetTUSMongoUploadStore) Prune(ctx context.Context, ids []string) error {
	c := us.staticUploadCollection()

	// Remove the hostname from all uploads.
	updateFilter := bson.M{
		"_id": bson.M{
			"$in": ids,
		},
		"complete": false,
	}
	_, err := c.UpdateMany(ctx, updateFilter, bson.M{
		"$pull": bson.M{
			"portalnames": us.staticPortalHostname,
		},
	})
	if err != nil {
		return errors.AddContext(err, "failed to pull hostname for pruned entries")
	}

	// Delete all the documents that now have an empty portalnames array.
	_, err = c.DeleteMany(ctx, bson.M{
		"_id": bson.M{
			"$in": ids,
		},
		"complete": false,
		"$or": bson.A{
			bson.M{"portalnames": bson.M{"$size": 0}},
		},
	})
	if err != nil {
		return errors.AddContext(err, "failed to purge uploads")
	}

	// Finally purge old locks.
	purger := lock.NewPurger(us.staticLockClient)
	_, err = purger.Purge(us.ctx)
	if err != nil {
		return errors.AddContext(err, "failed to purge old locks")
	}
	return nil
}

// CreateUpload creates a new upload and adds it to the store.
func (us *skynetTUSMongoUploadStore) CreateUpload(ctx context.Context, fi handler.FileInfo, sp skymodules.SiaPath, fileName string, baseChunkRedundancy uint8, fanoutDataPieces, fanoutParityPieces int, sm []byte, ct crypto.CipherType) (skymodules.SkynetTUSUpload, error) {
	upload := &mongoTUSUpload{
		ID:          fi.ID,
		Complete:    false,
		FanoutBytes: nil,
		FileInfo:    fi,
		LastWrite:   time.Now().UTC(),
		FileName:    fileName,
		SiaPath:     sp,

		BaseChunkRedundancy: baseChunkRedundancy,
		Metadata:            sm,
		PortalNames:         []string{us.staticPortalHostname},

		FanoutDataPieces:   fanoutDataPieces,
		FanoutParityPieces: fanoutParityPieces,
		CipherType:         ct,

		staticUploads: us.staticUploadCollection(),
	}
	// Insert into db.
	_, err := us.staticUploadCollection().InsertOne(ctx, upload)
	if err != nil {
		return nil, errors.AddContext(err, "failed to insert new upload into db")
	}
	return upload, nil
}

// GetUpload returns the upload specified by the given id.
func (us *skynetTUSMongoUploadStore) GetUpload(ctx context.Context, id string) (skymodules.SkynetTUSUpload, error) {
	r := us.staticUploadCollection().FindOne(ctx, bson.M{"_id": id})
	if errors.Contains(r.Err(), mongo.ErrNoDocuments) {
		return nil, os.ErrNotExist // return os.ErrNotExist for TUS
	}
	if r.Err() != nil {
		return nil, r.Err()
	}
	var upload mongoTUSUpload
	if err := r.Decode(&upload); err != nil {
		return nil, errors.AddContext(err, "failed to decode upload")
	}
	upload.staticUploads = us.staticUploadCollection()
	return &upload, nil
}

// staticLockCollection returns the mongo collection for the uploads.
func (us *skynetTUSMongoUploadStore) staticUploadCollection() *mongo.Collection {
	return us.staticClient.Database(tusDBName).Collection(tusUploadsMongoCollectionName)
}

// staticLockCollection returns the mongo collection for the locks.
func (us *skynetTUSMongoUploadStore) staticLockCollection() *mongo.Collection {
	return us.staticClient.Database(tusDBName).Collection(tusLocksMongoCollectionName)
}

// Skylink returns the upload's skylink if available already.
func (u *mongoTUSUpload) Skylink() (skymodules.Skylink, bool) {
	sl, exists := u.FileInfo.MetaData["Skylink"]
	if !exists {
		return skymodules.Skylink{}, false
	}
	var skylink skymodules.Skylink
	if err := skylink.LoadString(sl); err != nil {
		build.Critical("upload contains invalid skylink")
		return skymodules.Skylink{}, false
	}
	return skylink, true
}

// GetInfo returns the FileInfo of the upload.
func (u *mongoTUSUpload) GetInfo(ctx context.Context) (handler.FileInfo, error) {
	return u.FileInfo, nil
}

// IsSmallUpload indicates whether the upload is considered a
// small upload. That means the upload contained less than a
// chunksize of data.
func (u *mongoTUSUpload) IsSmallUpload(ctx context.Context) (bool, error) {
	return u.IsSmallFile, nil
}

// PruneInfo returns the info required to prune uploads.
func (u *mongoTUSUpload) PruneInfo(ctx context.Context) (id string, sp skymodules.SiaPath, err error) {
	id = u.FileInfo.ID
	sp = u.SiaPath
	return
}

// UploadParams returns the upload parameters used for the
// upload.
func (u *mongoTUSUpload) UploadParams(ctx context.Context) (skymodules.SkyfileUploadParameters, skymodules.FileUploadParams, error) {
	sup := skymodules.SkyfileUploadParameters{
		BaseChunkRedundancy: u.BaseChunkRedundancy,
		Filename:            u.FileName,
		SiaPath:             u.SiaPath,
	}
	fanoutSiaPath, err := u.SiaPath.AddSuffixStr(skymodules.ExtendedSuffix)
	if err != nil {
		return skymodules.SkyfileUploadParameters{}, skymodules.FileUploadParams{}, err
	}
	up, err := fileUploadParams(fanoutSiaPath, u.FanoutDataPieces, u.FanoutParityPieces, true, u.CipherType)
	if err != nil {
		return skymodules.SkyfileUploadParameters{}, skymodules.FileUploadParams{}, err
	}
	return sup, up, nil
}

// CommitWriteChunkSmallFile commits writing a chunk of a small
// file.
func (u *mongoTUSUpload) CommitWriteChunkSmallFile(ctx context.Context, newOffset int64, newLastWrite time.Time, smallFileData []byte) error {
	u.SmallUploadData = smallFileData
	return u.commitWriteChunk(ctx, bson.M{
		"smalluploaddata": u.SmallUploadData,
	}, newOffset, newLastWrite, true)
}

// CommitWriteChunk commits writing a chunk of either a small or
// large file with fanout.
func (u *mongoTUSUpload) CommitWriteChunk(ctx context.Context, newOffset int64, newLastWrite time.Time, isSmall bool, fanout []byte) error {
	// NOTE: This could potentially be improved to append to the fanout
	// instead of replacing it.
	u.FanoutBytes = append(u.FanoutBytes, fanout...)
	return u.commitWriteChunk(ctx, bson.M{
		"fanoutbytes": u.FanoutBytes,
	}, newOffset, newLastWrite, isSmall)
}

// commitWriteChunk commits a chunk write and also applies the updates provided
// by update.
func (u *mongoTUSUpload) commitWriteChunk(ctx context.Context, set bson.M, newOffset int64, newLastWrite time.Time, smallFile bool) error {
	u.FileInfo.Offset = newOffset
	u.LastWrite = newLastWrite
	u.IsSmallFile = smallFile
	set["fileinfo"] = u.FileInfo
	set["lastwrite"] = u.LastWrite.UTC()
	set["issmallfile"] = u.IsSmallFile
	update := bson.M{
		"$set": set,
	}
	result := u.staticUploads.FindOneAndUpdate(ctx, bson.M{"_id": u.FileInfo.ID}, update)
	return result.Err()
}

// CommitFinishUpload commits a finalised upload.
func (u *mongoTUSUpload) CommitFinishUpload(ctx context.Context, skylink skymodules.Skylink) error {
	u.FileInfo.MetaData["Skylink"] = skylink.String()
	u.Complete = true
	result := u.staticUploads.FindOneAndUpdate(ctx, bson.M{"_id": u.FileInfo.ID}, bson.M{
		"$set": bson.M{
			"fileinfo": u.FileInfo,
			"complete": u.Complete,
		},
	})
	return result.Err()
}

// CommitFinishPartialUpload commits a finalised partial upload.
func (u *mongoTUSUpload) CommitFinishPartialUpload(ctx context.Context) error {
	u.Complete = true
	result := u.staticUploads.FindOneAndUpdate(ctx, bson.M{"_id": u.FileInfo.ID}, bson.M{
		"$set": bson.M{
			"complete": u.Complete,
		},
	})
	return result.Err()
}

// Fanout returns the fanout of the upload. Should only be
// called once it's done uploading.
func (u *mongoTUSUpload) Fanout(ctx context.Context) ([]byte, error) {
	return u.FanoutBytes, nil
}

// SkyfileMetadata returns the metadata of the upload. Should
// only be called once it's done uploading.
func (u *mongoTUSUpload) SkyfileMetadata(ctx context.Context) ([]byte, error) {
	return u.Metadata, nil
}

// SmallFileData returns the data to upload for a small file
// upload.
func (u *mongoTUSUpload) SmallFileData(ctx context.Context) ([]byte, error) {
	return u.SmallUploadData, nil
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
