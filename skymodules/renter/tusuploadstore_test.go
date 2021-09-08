package renter

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var (
	// initMongoOnce makes sure ethat initMongo is only called once.
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
	host, ok := os.LookupEnv("MONGODB_HOST")
	if !ok {
		initMongoErr = errors.New("MONGODB_HOST not specified")
		return
	}
	opts := options.Client().ApplyURI(fmt.Sprintf("mongodb://%s:27017", host)).SetAuth(auth)
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
	client, err := newMongoDBForTesting()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())

	entry := bson.M{
		"_id": fastrand.Intn(1000),
	}
	// Create a single entry in a collection.
	collection := client.Database(t.Name()).Collection("smoke")
	_, err = collection.InsertOne(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}

	// Should be able to find that entry.
	result := collection.FindOne(context.Background(), entry)
	if result.Err() != nil {
		t.Fatal(err)
	}
	readEntry := bson.M{
		"_id": 0,
	}
	err = result.Decode(&readEntry)
	if err != nil {
		t.Fatal(err)
	}
	expectedID, ok := entry["_id"]
	if !ok {
		t.Fatal("unexpected")
	}
	readID, ok := entry["_id"]
	if !ok {
		t.Fatal("unexpected")
	}
	if expectedID != readID {
		t.Fatal("ids don't match")
	}
}
