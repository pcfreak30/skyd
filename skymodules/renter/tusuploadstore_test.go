package renter

import (
	"context"
	"reflect"
	"sync"
	"testing"

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

// initMongo initializes the connection between skyd and mongodb for testing.
func initMongo() {
	// Create client.
	auth := options.Credential{
		Username: "root",
		Password: "pwd",
	}
	uri := build.MongoDBURI()
	opts := options.Client().ApplyURI(uri).SetAuth(auth)
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
