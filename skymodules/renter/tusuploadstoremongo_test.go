package renter

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tus/tusd/pkg/handler"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.sia.tech/siad/crypto"
)

var (
	// initMongoOnce makes sure that initMongo is only called once.
	initMongoOnce sync.Once

	// initMongoErr contains any errors happening during the initialization
	// of the mongo connection.
	initMongoErr error

	// mongoClient is a client that can be used for testing.
	mongoClient *mongo.Client
)

// mongoTestCreds are the credentials for the test mongodb.
var mongoTestCreds = options.Credential{
	Username: "root",
	Password: "pwd",
}

// initMongo initializes the connection between skyd and mongodb for testing.
func initMongo() {
	// Create client.
	uri, ok := build.MongoDBURI()
	if !ok {
		err := errors.New("MONGODB_URI not set")
		initMongoErr = err
		return
	}
	opts := options.Client().ApplyURI(uri).SetAuth(mongoTestCreds)
	mongoClient, initMongoErr = mongo.Connect(context.Background(), opts)
}

// newMongoDBForTesting returns a connection to the mongodb in form of a client.
// If the connection hasn't been initialized yet it will do so.
func newMongoDBForTesting() (*mongo.Client, error) {
	initMongoOnce.Do(initMongo)
	return mongoClient, initMongoErr
}

// newMongoTestStore creates a skynetTUSMongoUploadStore for testing.
func newMongoTestStore(name string) (*skynetTUSMongoUploadStore, error) {
	uri, ok := build.MongoDBURI()
	if !ok {
		build.Critical("uri not set")
	}
	return newSkynetTUSMongoUploadStore(context.Background(), uri, name, mongoTestCreds)
}

// TestMongoSmoke is a smoke test for the mongodb connection.
func TestMongoSmoke(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	client, err := newMongoDBForTesting()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())

	type E struct {
		ID int `bson:"_id"`
	}
	entry := E{
		ID: fastrand.Intn(1000),
	}
	// Create a single entry in a collection.
	collection := client.Database(t.Name()).Collection("smoke")
	_, err = collection.InsertOne(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}

	// Should be able to find that entry.
	result := collection.FindOne(context.Background(), bson.M{"_id": entry.ID})
	if result.Err() != nil {
		t.Fatal(result.Err())
	}
	var readEntry E
	err = result.Decode(&readEntry)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(entry, readEntry) {
		t.Fatal("entries don't match", entry, readEntry)
	}
}

// TestMongoLocking tests the skynetMongoLock.
func TestMongoLocking(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	us, err := newMongoTestStore(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := us.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create 2 uploads A and B.
	uploadAID := "uploadA"
	uploadBID := "uploadB"

	// Create 3 locks. The first two for the same upload and the second one
	// for a different upload.
	lockA1, err := us.NewLock(uploadAID)
	if err != nil {
		t.Fatal(err)
	}

	lockA2, err := us.NewLock(uploadAID)
	if err != nil {
		t.Fatal(err)
	}

	lockB, err := us.NewLock(uploadBID)
	if err != nil {
		t.Fatal(err)
	}

	// Lock A once.
	err = lockA1.Lock()
	if err != nil {
		t.Fatal(err)
	}

	// Lock A again. This should fail.
	err = lockA2.Lock()
	if err != handler.ErrFileLocked {
		t.Fatal(err)
	}

	// Locking B should work.
	err = lockB.Lock()
	if err != nil {
		t.Fatal(err)
	}

	// Unlock A.
	err = lockA1.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	// Now locking A should work with lockA2.
	err = lockA2.Lock()
	if err != nil {
		t.Fatal(err)
	}

	// Lock A1 again. This should fail.
	err = lockA1.Lock()
	if err != handler.ErrFileLocked {
		t.Fatal(err)
	}
}

// TestPrune is a unit test for ToPrune and Prune.
func TestPrune(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	us, err := newMongoTestStore(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := us.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Make sure the hostname was set.
	if us.staticPortalHostname == "" {
		t.Fatal("hostname not set")
	}

	// Create a bunch of uploads.

	// The recent upload won't be pruned.
	uploadRecent := MongoTUSUpload{
		ID:          "recent",
		LastWrite:   time.Now().UTC(),
		PortalNames: []string{us.staticPortalHostname},
	}

	// The outdated one will be pruned.
	uploadOutdated := MongoTUSUpload{
		ID:          "outdated",
		PortalNames: []string{us.staticPortalHostname},
	}

	// The outdated one without portal will be pruned.
	uploadOutdatedNoPortal := MongoTUSUpload{
		ID:          "outdatedNoPortal",
		PortalNames: []string{},
	}

	// The outdated one which was set by some other portal won't be pruned.
	uploadOutdatedButWrongPortal := MongoTUSUpload{
		ID:          "outdatedWrongPortal",
		PortalNames: []string{"someOtherPortal"},
	}

	// The outdated one which was successfully completed won't be pruned.
	uploadOutdatedButComplete := MongoTUSUpload{
		ID:          "outdatedButComplete",
		Complete:    true,
		PortalNames: []string{us.staticPortalHostname},
	}

	// The outdated one with multiple hosts won't be fully pruned but the portal
	// will be removed.
	uploadOutdatedMultiPortal := MongoTUSUpload{
		ID:          "outdatedMultiPortal",
		Complete:    false,
		PortalNames: []string{us.staticPortalHostname, "otherPortal"},
	}

	// Reset collection.
	collection := us.staticUploadCollection()
	if err := collection.Drop(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Insert the uploads.
	_, err = collection.InsertOne(context.Background(), uploadRecent)
	if err != nil {
		t.Fatal(err)
	}
	_, err = collection.InsertOne(context.Background(), uploadOutdated)
	if err != nil {
		t.Fatal(err)
	}
	_, err = collection.InsertOne(context.Background(), uploadOutdatedNoPortal)
	if err != nil {
		t.Fatal(err)
	}
	_, err = collection.InsertOne(context.Background(), uploadOutdatedButWrongPortal)
	if err != nil {
		t.Fatal(err)
	}
	_, err = collection.InsertOne(context.Background(), uploadOutdatedButComplete)
	if err != nil {
		t.Fatal(err)
	}
	_, err = collection.InsertOne(context.Background(), uploadOutdatedMultiPortal)
	if err != nil {
		t.Fatal(err)
	}

	// Ask the upload store for the uploads to prune.
	toPrune, err := us.ToPrune(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(toPrune) != 3 {
		for i, u := range toPrune {
			t.Log(i, u.(*MongoTUSUpload).ID)
		}
		t.Fatalf("expected %v uploads but got %v", 4, len(toPrune))
	}
	prunedUpload := toPrune[0].(*MongoTUSUpload)
	if !reflect.DeepEqual(*prunedUpload, uploadOutdated) {
		t.Fatal("wrong upload", toPrune[0], uploadOutdated)
	}

	// Prune the uploads.
	var ids []string
	for _, p := range toPrune {
		ids = append(ids, p.(*MongoTUSUpload).ID)
	}
	err = us.Prune(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}

	// There should be nothing left to prune.
	toPrune, err = us.ToPrune(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(toPrune) != 0 {
		t.Fatalf("expected %v uploads but got %v", 0, len(toPrune))
	}

	// The outdated upload should be gone.
	_, err = us.GetUpload(context.Background(), uploadOutdated.ID)
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// The one without portal should be gone.
	_, err = us.GetUpload(context.Background(), uploadOutdatedNoPortal.ID)
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// The others should still exist.
	upload, err := us.GetUpload(context.Background(), uploadRecent.ID)
	if err != nil {
		t.Fatal(err)
	}
	portalNames := upload.(*MongoTUSUpload).PortalNames
	if !reflect.DeepEqual(portalNames, uploadRecent.PortalNames) {
		t.Fatal("wrong portal name", portalNames)
	}
	upload, err = us.GetUpload(context.Background(), uploadOutdatedButComplete.ID)
	if err != nil {
		t.Fatal(err)
	}
	portalNames = upload.(*MongoTUSUpload).PortalNames
	if !reflect.DeepEqual(portalNames, uploadOutdatedButComplete.PortalNames) {
		t.Fatal("wrong portal name", portalNames)
	}
	upload, err = us.GetUpload(context.Background(), uploadOutdatedButWrongPortal.ID)
	if err != nil {
		t.Fatal(err)
	}
	portalNames = upload.(*MongoTUSUpload).PortalNames
	if !reflect.DeepEqual(portalNames, uploadOutdatedButWrongPortal.PortalNames) {
		t.Fatal("wrong portal name", portalNames)
	}
	// The one with multiple portals should exist and have 1 portal left.
	upload, err = us.GetUpload(context.Background(), uploadOutdatedMultiPortal.ID)
	if err != nil {
		t.Fatal(err)
	}
	portalNames = upload.(*MongoTUSUpload).PortalNames
	if len(portalNames) != 1 || portalNames[0] != "otherPortal" {
		t.Fatal("wrong portal name remains", portalNames)
	}
}

// TestCreateGetUpload is a unit test for CreateUpload and Upload.
func TestCreateGetUpload(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	us, err := newMongoTestStore(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := us.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Reset collection.
	collection := us.staticUploadCollection()
	if err := collection.Drop(context.Background()); err != nil {
		t.Fatal(err)
	}

	fi := handler.FileInfo{
		ID:             "id",
		Size:           100,
		SizeIsDeferred: true,
		Offset:         42,
		MetaData:       handler.MetaData{"field": "value"},
		IsPartial:      true,
		IsFinal:        true,
		PartialUploads: []string{"1", "2", "3"},
		Storage:        map[string]string{"key": "storage"},
	}
	expectedUpload := MongoTUSUpload{
		ID:          fi.ID,
		Complete:    false,
		PortalNames: []string{us.staticPortalHostname},

		FanoutBytes: nil,
		FileInfo:    fi,
		FileName:    "somename",
		SiaPath:     skymodules.RandomSiaPath(),

		BaseChunkRedundancy: 1,
		Metadata:            []byte{3, 2, 1},

		FanoutDataPieces:   2,
		FanoutParityPieces: 3,
		CipherType:         crypto.TypePlain,
	}
	createdUpload, err := us.CreateUpload(context.Background(), fi, expectedUpload.SiaPath, expectedUpload.FileName, expectedUpload.BaseChunkRedundancy, expectedUpload.FanoutDataPieces, expectedUpload.FanoutParityPieces, expectedUpload.Metadata, expectedUpload.CipherType)
	if err != nil {
		t.Fatal(err)
	}
	mu := createdUpload.(*MongoTUSUpload)

	// Check the timsestamp separately.
	if mu.LastWrite.IsZero() {
		t.Fatal("lastWrite not set")
	}
	expectedUpload.LastWrite = mu.LastWrite

	// Check the pointer separately.
	if mu.staticUploadStore == nil {
		t.Fatal("staticUploadStore not set")
	}
	mu.staticUploadStore = nil

	// Compare the remaining fields.
	if !reflect.DeepEqual(expectedUpload, *mu) {
		fmt.Println(expectedUpload)
		fmt.Println(*mu)
		t.Fatal("mismatch")
	}

	// Try again. Should fail.
	_, err = us.CreateUpload(context.Background(), fi, expectedUpload.SiaPath, expectedUpload.FileName, expectedUpload.BaseChunkRedundancy, expectedUpload.FanoutDataPieces, expectedUpload.FanoutParityPieces, expectedUpload.Metadata, expectedUpload.CipherType)
	if err == nil || !strings.Contains(err.Error(), "duplicate key error") {
		t.Fatal(err)
	}

	// Fetch the upload.
	createdUpload, err = us.GetUpload(context.Background(), expectedUpload.ID)
	if err != nil {
		t.Fatal(err)
	}
	mu = createdUpload.(*MongoTUSUpload)

	// Check the timsestamp separately.
	if mu.LastWrite.IsZero() {
		t.Fatal("lastWrite not set")
	}
	expectedUpload.LastWrite = mu.LastWrite

	// Check the pointer separately.
	if mu.staticUploadStore == nil {
		t.Fatal("staticUploadStore not set")
	}
	mu.staticUploadStore = nil

	// Compare the remaining fields.
	if !reflect.DeepEqual(expectedUpload, *mu) {
		fmt.Println(expectedUpload)
		fmt.Println(*mu)
		t.Fatal("mismatch")
	}
}

// TestCommitWriteChunk tests committing small and large uploads.
func TestCommitWriteChunk(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Use different stores for creating the uploads and committing chunks
	// with.
	createStore, err := newMongoTestStore("create")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := createStore.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	commitStore, err := newMongoTestStore("commit")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := commitStore.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Reset collection.
	collection := createStore.staticUploadCollection()
	if err := collection.Drop(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Large upload.
	sm := fastrand.Bytes(10)
	largeUpload, err := createStore.CreateUpload(context.Background(), handler.FileInfo{ID: "large"}, skymodules.RandomSiaPath(), "large", 1, 1, 1, sm, crypto.TypePlain)
	if err != nil {
		t.Fatal(err)
	}
	largeUpload.(*MongoTUSUpload).staticUploadStore = commitStore

	// First commit.
	lastWrite := time.Now().UTC()
	fanout1 := fastrand.Bytes(crypto.HashSize)
	newOffset := int64(10)
	err = largeUpload.CommitWriteChunk(context.Background(), newOffset, lastWrite, false, fanout1)
	if err != nil {
		t.Fatal(err)
	}

	// Check upload.
	u, err := commitStore.GetUpload(context.Background(), "large")
	if err != nil {
		t.Fatal(err)
	}
	upload := u.(*MongoTUSUpload)
	if !bytes.Equal(upload.FanoutBytes, fanout1) {
		t.Fatal("wrong fanout", len(upload.FanoutBytes), len(fanout1))
	}
	if upload.FileInfo.Offset != newOffset {
		t.Fatal("wrong offset", upload.FileInfo.Offset, newOffset)
	}
	if upload.LastWrite.Unix() != lastWrite.Unix() {
		t.Fatal("wrong lastWrite", upload.LastWrite, lastWrite)
	}
	if upload.IsSmallFile != false {
		t.Fatal("wrong isSmallFile", upload.IsSmallFile, false)
	}
	if !bytes.Equal(upload.Metadata, sm) {
		t.Fatal("wrong metadata")
	}
	if !reflect.DeepEqual(upload.PortalNames, []string{"create", "commit"}) {
		t.Fatal("wrong portalnames", upload.PortalNames)
	}

	// Second commit.
	lastWrite = time.Now().UTC()
	fanout2 := fastrand.Bytes(crypto.HashSize)
	newOffset = 20
	err = u.CommitWriteChunk(context.Background(), newOffset, lastWrite, false, fanout2)
	if err != nil {
		t.Fatal(err)
	}

	// Check upload again.
	u, err = createStore.GetUpload(context.Background(), "large")
	if err != nil {
		t.Fatal(err)
	}
	upload = u.(*MongoTUSUpload)
	if !bytes.Equal(upload.FanoutBytes, append(fanout1, fanout2...)) {
		t.Fatal("wrong fanout", len(upload.FanoutBytes))
	}
	if upload.FileInfo.Offset != newOffset {
		t.Fatal("wrong offset", upload.FileInfo.Offset, newOffset)
	}
	if upload.LastWrite.Unix() != lastWrite.Unix() {
		t.Fatal("wrong lastWrite", upload.LastWrite, lastWrite)
	}
	if upload.IsSmallFile != false {
		t.Fatal("wrong isSmallFile", upload.IsSmallFile, false)
	}
	if !bytes.Equal(upload.Metadata, sm) {
		t.Fatal("wrong metadata")
	}
	if !reflect.DeepEqual(upload.PortalNames, []string{"create", "commit"}) {
		t.Fatal("wrong portalnames", upload.PortalNames)
	}

	// Small upload.
	sm = fastrand.Bytes(10)
	u, err = createStore.CreateUpload(context.Background(), handler.FileInfo{ID: "small"}, skymodules.RandomSiaPath(), "small", 1, 1, 1, sm, crypto.TypePlain)
	if err != nil {
		t.Fatal(err)
	}
	u.(*MongoTUSUpload).staticUploadStore = commitStore

	// Commit.
	lastWrite = time.Now().UTC()
	newOffset = 30
	smallFileData := fastrand.Bytes(50)
	err = u.CommitWriteChunkSmallFile(context.Background(), newOffset, lastWrite, smallFileData)
	if err != nil {
		t.Fatal(err)
	}

	// Check upload.
	u, err = commitStore.GetUpload(context.Background(), "small")
	if err != nil {
		t.Fatal(err)
	}
	upload = u.(*MongoTUSUpload)
	if !bytes.Equal(upload.SmallUploadData, smallFileData) {
		t.Fatal("wrong file data", len(upload.SmallUploadData), len(smallFileData))
	}
	if upload.FileInfo.Offset != newOffset {
		t.Fatal("wrong offset", upload.FileInfo.Offset, newOffset)
	}
	if upload.LastWrite.Unix() != lastWrite.Unix() {
		t.Fatal("wrong lastWrite", upload.LastWrite, lastWrite)
	}
	if upload.IsSmallFile != true {
		t.Fatal("wrong isSmallFile", upload.IsSmallFile, true)
	}
	if !bytes.Equal(upload.Metadata, sm) {
		t.Fatal("wrong metadata")
	}
	if !reflect.DeepEqual(upload.PortalNames, []string{"create", "commit"}) {
		t.Fatal("wrong portalnames", upload.PortalNames)
	}
}

// TestCommitFinishUpload is a unit test for CommitFinishUpload and
// CommitFinishPartialUpload.
func TestCommitFinishUpload(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	us, err := newMongoTestStore(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := us.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Reset collection.
	collection := us.staticUploadCollection()
	if err := collection.Drop(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Test the partial finisher first.
	partial, err := us.CreateUpload(context.Background(), handler.FileInfo{ID: "partial", MetaData: make(handler.MetaData)}, skymodules.RandomSiaPath(), "partial", 1, 1, 1, fastrand.Bytes(10), crypto.TypePlain)
	if err != nil {
		t.Fatal(err)
	}

	// Check the relevant fields.
	u, err := us.GetUpload(context.Background(), "partial")
	if err != nil {
		t.Fatal(err)
	}
	upload := u.(*MongoTUSUpload)
	if upload.Complete {
		t.Fatal("new upload shouldn't be complete")
	}

	// Finish it.
	err = partial.CommitFinishPartialUpload(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Check the fields again.
	u, err = us.GetUpload(context.Background(), "partial")
	if err != nil {
		t.Fatal(err)
	}
	upload = u.(*MongoTUSUpload)
	if !upload.Complete {
		t.Fatal("new upload should be complete")
	}

	// Test the regular upload.
	regular, err := us.CreateUpload(context.Background(), handler.FileInfo{ID: "regular", MetaData: make(handler.MetaData)}, skymodules.RandomSiaPath(), "regular", 1, 1, 1, fastrand.Bytes(10), crypto.TypePlain)
	if err != nil {
		t.Fatal(err)
	}

	// Check the relevant fields.
	u, err = us.GetUpload(context.Background(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	upload = u.(*MongoTUSUpload)
	if upload.Complete {
		t.Fatal("new upload shouldn't be complete")
	}
	if _, set := upload.FileInfo.MetaData["Skylink"]; set {
		t.Fatal("skylink shouldn't be set")
	}

	// Finish it.
	var h crypto.Hash
	fastrand.Read(h[:])
	skylink, err := skymodules.NewSkylinkV1(h, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = regular.CommitFinishUpload(context.Background(), skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Check the fields again.
	u, err = us.GetUpload(context.Background(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	upload = u.(*MongoTUSUpload)
	if !upload.Complete {
		t.Fatal("new upload should be complete")
	}
	if sl, set := upload.FileInfo.MetaData["Skylink"]; !set || !reflect.DeepEqual(sl, skylink.String()) {
		t.Fatal("wrong skylink")
	}
}
