package renter

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/tus/tusd/pkg/handler"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
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
	uri := build.MongoDBURI()
	opts := options.Client().ApplyURI(uri).SetAuth(mongoTestCreds)
	mongoClient, initMongoErr = mongo.Connect(context.Background(), opts)
}

// newMongoDBForTesting returns a connection to the mongodb in form of a client.
// If the connection hasn't been initialized yet it will do so.
func newMongoDBForTesting() (*mongo.Client, error) {
	initMongoOnce.Do(initMongo)
	return mongoClient, initMongoErr
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

	us, err := newSkynetTUSMongoUploadStore(build.MongoDBURI(), mongoTestCreds)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := us.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create 2 uploads A and B.
	uploadA := mongoTUSUpload{ID: t.Name() + "_A"}
	uploadB := mongoTUSUpload{ID: t.Name() + "_B"}
	collection := us.staticClient.Database(tusDBName).Collection(tusUploadsMongoCollectionName)
	_, err = collection.InsertOne(context.Background(), uploadA)
	if err != nil {
		t.Fatal(err)
	}
	_, err = collection.InsertOne(context.Background(), uploadB)
	if err != nil {
		t.Fatal(err)
	}

	// Create 3 locks. The first two for the same upload and the second one
	// for a different upload.
	lockA1, err := us.NewLock(uploadA.ID)
	if err != nil {
		t.Fatal(err)
	}

	lockA2, err := us.NewLock(uploadA.ID)
	if err != nil {
		t.Fatal(err)
	}

	lockB, err := us.NewLock(uploadB.ID)
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

	var readUpload mongoTUSUpload
	sr := collection.FindOne(context.Background(), bson.M{"_id": uploadA.ID})
	if sr.Err() != nil {
		t.Fatal(err)
	}
	err = sr.Decode(&readUpload)
	if err != nil {
		t.Fatal(err)
	}
}
