package renter

import (
	"context"
	"encoding/json"
	"fmt"
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

	// mongoDefaultTimeout is the default timeout for mongo operations that
	// require a context but where the input arguments don't contain a
	// context.
	mongoDefaultTimeout = time.Minute

	// TusDBName is the name of the database all TUS related data is stored
	// in.
	TusDBName = "tus"

	// TusUploadsMongoCollectionName is the name of the collection within
	// the database used to store upload info.
	TusUploadsMongoCollectionName = "uploads"

	// tusLocksMongoCollectionName is the name of the collection within the
	// database used to store locks.
	tusLocksMongoCollectionName = "locks"

	// tusLockOwnerName is passed as the 'Owner' when creating a new lock in
	// the db for tus uploads.
	tusLockOwnerName = "TUS"
)

// ErrUploadFinished is returned if we try to finish an upload that is already
// complete.
var ErrUploadFinished = errors.New("upload already finished")

type (
	skynetTUSMongoUploadStore struct {
		staticClient         *mongo.Client
		staticLockClient     *lock.Client
		staticPortalHostname string
	}

	// MongoTUSUpload describes an upload in mongodb.
	MongoTUSUpload struct {
		ID          string    `bson:"_id"`
		Complete    bool      `bson:"complete"`
		LastWrite   time.Time `bson:"lastwrite"`
		ServerNames []string  `bson:"servernames"`

		FanoutBytes []byte             `bson:"fanoutbytes"`
		FileInfo    handler.FileInfo   `bson:"fileinfo"`
		FileName    string             `bson:"filename"`
		SiaPath     skymodules.SiaPath `bson:"siapath"`

		BaseChunkRedundancy uint8             `bson:"basechunkredundancy"`
		Metadata            []byte            `bson:"metadata"`
		FanoutDataPieces    int               `bson:"fanoutdatapieces"`
		FanoutParityPieces  int               `bson:"fanoutparitypieces"`
		CipherType          crypto.CipherType `bson:"ciphertype"`

		staticUploadStore *skynetTUSMongoUploadStore
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
	ctx, cancel := context.WithTimeout(context.Background(), mongoDefaultTimeout)
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
		Owner: tusLockOwnerName,
		Host:  l.staticPortalHostname,
		TTL:   mongoLockTTL,
	}
	ctx, cancel := context.WithTimeout(context.Background(), mongoDefaultTimeout)
	defer cancel()
	err := client.XLock(ctx, l.staticUploadID, l.staticUploadID, ld)
	if err == lock.ErrAlreadyLocked {
		return handler.ErrFileLocked
	}
	return err
}

// Unlock attempts to unlock an upload. It will retry doing so for a certain
// time before giving up.
func (l *skynetMongoLock) Unlock() error {
	ctx, cancel := context.WithTimeout(context.Background(), mongoDefaultTimeout)
	defer cancel()
	var err error
	for {
		_, err = l.staticClient.Unlock(ctx, l.staticUploadID)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			build.Critical("Failed to unlock the lock", err)
			return err
		case <-time.After(time.Second):
		}
	}
}

// ToPrune returns the uploads which should be pruned by the portal.
func (us *skynetTUSMongoUploadStore) ToPrune(ctx context.Context) ([]skymodules.SkynetTUSUpload, error) {
	c := us.staticUploadCollection()

	filter := bson.M{
		"lastwrite": bson.M{
			"$lt": time.Now().Add(-PruneTUSUploadTimeout).UTC(),
		},
		"complete": false,
		"$or": bson.A{
			bson.M{"servernames": us.staticPortalHostname},
			bson.M{"servernames": bson.M{"$size": 0}},
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
		var upload MongoTUSUpload
		if err := cursor.Decode(&upload); err != nil {
			build.Critical("ToPrune: failed to decode upload", err)
			continue
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
			"servernames": us.staticPortalHostname,
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
		"complete":    false,
		"servernames": bson.M{"$size": 0},
	})
	if err != nil {
		return errors.AddContext(err, "failed to purge uploads")
	}

	// Finally purge old locks.
	purger := lock.NewPurger(us.staticLockClient)
	_, err = purger.Purge(ctx)
	if err != nil {
		return errors.AddContext(err, "failed to purge old locks")
	}
	return nil
}

// CreateUpload creates a new upload and adds it to the store.
func (us *skynetTUSMongoUploadStore) CreateUpload(ctx context.Context, fi handler.FileInfo, sp skymodules.SiaPath, fileName string, baseChunkRedundancy uint8, fanoutDataPieces, fanoutParityPieces int, sm []byte, ct crypto.CipherType) (skymodules.SkynetTUSUpload, error) {
	upload := &MongoTUSUpload{
		ID:          fi.ID,
		Complete:    false,
		FanoutBytes: nil,
		FileInfo:    fi,
		LastWrite:   time.Now().UTC(),
		FileName:    fileName,
		SiaPath:     sp,

		BaseChunkRedundancy: baseChunkRedundancy,
		Metadata:            sm,
		ServerNames:         []string{us.staticPortalHostname},

		FanoutDataPieces:   fanoutDataPieces,
		FanoutParityPieces: fanoutParityPieces,
		CipherType:         ct,

		staticUploadStore: us,
	}
	// Insert into db.
	_, err := us.staticUploadCollection().InsertOne(ctx, upload)
	if err != nil {
		return nil, errors.AddContext(err, "failed to insert new upload into db")
	}
	return upload, nil
}

// GetUpload returns the upload specified by the given id. The upload will also
// have this portal's name added to it.
func (us *skynetTUSMongoUploadStore) GetUpload(ctx context.Context, id string) (skymodules.SkynetTUSUpload, error) {
	// Ignore uploads which are ready to be pruned to avoid a loop
	// where portals keep adding themselves back and then remove
	// themselves again as part of the pruning process.
	filter := bson.M{
		"_id": id,
		"$or": bson.A{
			bson.M{
				"lastwrite": bson.M{
					"$gte": time.Now().Add(-PruneTUSUploadTimeout).UTC(),
				},
			},
			bson.M{
				"complete": true,
			},
		},
	}
	// Add the portal to the set of servers.
	update := bson.M{
		"$addToSet": bson.M{
			"servernames": us.staticPortalHostname,
		},
	}
	r := us.staticUploadCollection().FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetReturnDocument(options.After))
	if errors.Contains(r.Err(), mongo.ErrNoDocuments) {
		return nil, os.ErrNotExist // return os.ErrNotExist for TUS
	}
	if r.Err() != nil {
		return nil, r.Err()
	}
	// Decode result.
	var upload MongoTUSUpload
	if err := r.Decode(&upload); err != nil {
		return nil, errors.AddContext(err, "failed to decode upload")
	}
	upload.staticUploadStore = us
	return &upload, nil
}

// staticUploadCollection returns the mongo collection for the uploads.
func (us *skynetTUSMongoUploadStore) staticUploadCollection() *mongo.Collection {
	return us.staticClient.Database(TusDBName).Collection(TusUploadsMongoCollectionName)
}

// staticLockCollection returns the mongo collection for the locks.
func (us *skynetTUSMongoUploadStore) staticLockCollection() *mongo.Collection {
	return us.staticClient.Database(TusDBName).Collection(tusLocksMongoCollectionName)
}

// GetSkylink returns the upload's skylink if available already.
func (u *MongoTUSUpload) GetSkylink() (skymodules.Skylink, bool) {
	sl, exists := u.FileInfo.MetaData["Skylink"]
	if !exists {
		return skymodules.Skylink{}, false
	}
	var skylink skymodules.Skylink
	if err := skylink.LoadString(sl); err != nil {
		return skymodules.Skylink{}, false
	}
	return skylink, true
}

// GetInfo returns the FileInfo of the upload.
func (u *MongoTUSUpload) GetInfo(ctx context.Context) (handler.FileInfo, error) {
	return u.FileInfo, nil
}

// PruneInfo returns the info required to prune uploads.
func (u *MongoTUSUpload) PruneInfo(ctx context.Context) (id string, sp skymodules.SiaPath, err error) {
	id = u.FileInfo.ID
	sp = u.SiaPath
	return
}

// UploadParams returns the upload parameters used for the
// upload.
func (u *MongoTUSUpload) UploadParams(ctx context.Context) (skymodules.SkyfileUploadParameters, skymodules.FileUploadParams, error) {
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

// CommitWriteChunk commits writing a chunk of either a small or
// large file with fanout.
func (u *MongoTUSUpload) CommitWriteChunk(ctx context.Context, newOffset int64, newLastWrite time.Time, isSmall bool, fanout []byte) error {
	var sm skymodules.SkyfileMetadata
	// NOTE: This could potentially be improved to append to the fanout
	// instead of replacing it.
	err := json.Unmarshal(u.Metadata, &sm)
	fmt.Println("CommitWriteChunk before append", err)
	// Create the new fanout bytes. Make sure we are not reusing
	// u.FanoutBytes memory but instead assign new memory and then copy into
	// that.
	newFanoutBytes := make([]byte, len(u.FanoutBytes)+len(fanout))
	copy(newFanoutBytes, u.FanoutBytes)
	copy(newFanoutBytes[len(u.FanoutBytes):], fanout)
	err = json.Unmarshal(u.Metadata, &sm)
	fmt.Println("CommitWriteChunk after append", err)
	err = u.commitWriteChunk(ctx, bson.M{
		"fanoutbytes": newFanoutBytes,
	}, newOffset, newLastWrite)
	if err != nil {
		return err
	}
	u.FanoutBytes = newFanoutBytes
	return nil
}

// commitWriteChunk commits a chunk write and also applies the updates provided
// by update.
func (u *MongoTUSUpload) commitWriteChunk(ctx context.Context, set bson.M, newOffset int64, newLastWrite time.Time) error {
	// First, try to update the db. That way, we don't need to revert the
	// in-memory state if writing to the database fails.
	uploads := u.staticUploadStore.staticUploadCollection()
	newFileInfo := u.FileInfo
	newFileInfo.Offset = newOffset
	set["fileinfo"] = newFileInfo
	set["lastwrite"] = newLastWrite.UTC()
	update := bson.M{
		"$set": set,
	}
	result := uploads.FindOneAndUpdate(ctx, bson.M{"_id": u.FileInfo.ID}, update)
	if errors.Contains(result.Err(), mongo.ErrNoDocuments) {
		return os.ErrNotExist // return os.ErrNotExist for TUS
	}
	if err := result.Err(); err != nil {
		return err
	}
	// Then update the in-memory state.
	u.FileInfo = newFileInfo
	u.LastWrite = newLastWrite
	return result.Err()
}

// CommitFinishUpload commits a finalised upload.
func (u *MongoTUSUpload) CommitFinishUpload(ctx context.Context, skylink skymodules.Skylink) error {
	if u.Complete {
		return ErrUploadFinished
	}
	uploads := u.staticUploadStore.staticUploadCollection()

	// First, try to update the db. That way, we don't need to revert the
	// in-memory state if writing to the database fails.
	newComplete := true
	newFileInfo := u.FileInfo
	newFileInfo.Offset = newFileInfo.Size
	newFileInfo.MetaData["Skylink"] = skylink.String()
	result := uploads.FindOneAndUpdate(ctx, bson.M{"_id": u.FileInfo.ID}, bson.M{
		"$set": bson.M{
			"complete": newComplete,
			"fileinfo": newFileInfo,
		},
		// Clean up some space.
		"$unset": bson.M{
			"fanoutbytes": "",
		},
	})
	if errors.Contains(result.Err(), mongo.ErrNoDocuments) {
		return os.ErrNotExist // return os.ErrNotExist for TUS
	}
	if err := result.Err(); err != nil {
		return err
	}

	// Then update the in-memory state.
	u.Complete = newComplete
	u.FileInfo = newFileInfo
	u.LastWrite = time.Now()
	u.FanoutBytes = nil
	return nil
}

// Fanout returns the fanout of the upload. Should only be
// called once it's done uploading.
func (u *MongoTUSUpload) Fanout(ctx context.Context) ([]byte, error) {
	return u.FanoutBytes, nil
}

// SkyfileMetadata returns the metadata of the upload. Should
// only be called once it's done uploading.
func (u *MongoTUSUpload) SkyfileMetadata(ctx context.Context) ([]byte, error) {
	return u.Metadata, nil
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
	// instance.
	opts := options.Client().
		ApplyURI(uri).
		SetAuth(creds).
		SetReadConcern(readconcern.Local()).
		SetWriteConcern(writeconcern.New(writeconcern.W(1)))

	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, err
	}

	// Sanity check portal name.
	if portalName == "" {
		return nil, errors.New("portalName can't be empty string")
	}

	// Create store.
	us := &skynetTUSMongoUploadStore{
		staticClient:         client,
		staticPortalHostname: portalName,
	}

	// Create the indices for the uploads collection.
	indexes := []mongo.IndexModel{
		{Keys: bson.M{"complete": 1}},
		{Keys: bson.M{"lastwrite": 1}},
		{Keys: bson.M{"servernames": 1}},
	}
	_, err = us.staticUploadCollection().Indexes().CreateMany(ctx, indexes)
	if err != nil {
		return nil, err
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

// WithTransaction allows for grouping multiple database operations into a
// single atomic transaction.
func (us *skynetTUSMongoUploadStore) WithTransaction(ctx context.Context, handler func(context.Context) error) error {
	return us.staticClient.UseSession(ctx, func(sctx mongo.SessionContext) error {
		_, err := sctx.WithTransaction(ctx, func(sctx mongo.SessionContext) (interface{}, error) {
			return nil, handler(sctx)
		})
		return err
	})
}
