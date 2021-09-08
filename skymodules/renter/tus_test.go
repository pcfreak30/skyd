package renter

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/benweissmann/memongo"
	"github.com/strikesecurity/strikememongo"
	"github.com/tus/tusd/pkg/handler"
	"gitlab.com/NebulousLabs/fastrand"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.sia.tech/siad/modules"
)

// TestWriteChunkSetLastWrite is a unit test that confirms lastWrite is updated
// correctly on uploads.
func TestWriteChunkSetLastWrite(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	wt, err := newWorkerTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := wt.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	r := wt.rt.renter
	tu := r.staticSkynetTUSUploader

	// Small upload.
	info := handler.FileInfo{
		MetaData: make(handler.MetaData),
		Size:     1,
	}
	upload, err := tu.NewUpload(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	stu := upload.(*skynetTUSUpload)
	if stu.lastWrite.IsZero() {
		t.Fatal("lastWrite wasn't initialized")
	}
	start := time.Now()
	n, err := upload.WriteChunk(context.Background(), 0, bytes.NewReader(fastrand.Bytes(int(info.Size))))
	if err != nil {
		t.Fatal(err)
	}
	if n != info.Size {
		t.Fatal("wrong n", n)
	}
	if start.After(stu.lastWrite) {
		t.Fatal("last write wasn't updated", start, stu.lastWrite)
	}

	// Large upload.
	info = handler.FileInfo{
		MetaData: make(handler.MetaData),
		Size:     int64(modules.SectorSize) + 1,
	}
	upload, err = tu.NewUpload(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	stu = upload.(*skynetTUSUpload)
	if stu.lastWrite.IsZero() {
		t.Fatal("lastWrite wasn't initialized")
	}
	start = time.Now()
	n, err = upload.WriteChunk(context.Background(), 0, bytes.NewReader(fastrand.Bytes(int(info.Size))))
	if err != nil {
		t.Fatal(err)
	}
	if n != info.Size {
		t.Fatal("wrong n", n)
	}
	if start.After(stu.lastWrite) {
		t.Fatal("last write wasn't updated", start, stu.lastWrite)
	}
}

func TestMongoDB(t *testing.T) {
	mongoServer, err := strikememongo.Start("4.0.5")
	if err != nil {
		t.Fatal(err)
	}
	defer mongoServer.Stop()

	dbName := memongo.RandomDatabase()
	dbURI := mongoServer.URI()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(dbURI))
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err = client.Disconnect(ctx); err != nil {
			panic(err)
		}
	}()

	type testType struct {
		name string
	}
	var test []interface{}
	test = append(test, bson.D{
		{
			Key:   "name",
			Value: "chris",
		},
	})
	test = append(test, bson.D{
		{
			Key:   "name",
			Value: "david",
		},
	})

	collection := client.Database(dbName).Collection("testing")
	_, err = collection.InsertMany(ctx, test)
	if err != nil {
		t.Fatal(err)
	}

	replaceResult := collection.FindOneAndReplace(ctx, bson.D{{Key: "name", Value: "david"}}, bson.D{{Key: "name", Value: []byte{1, 2, 3}}})

	if err := replaceResult.Err(); err != nil {
		t.Fatal(err)
	}
	var decoded bson.D
	err = replaceResult.Decode(&decoded)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("resplace result", decoded)

	findResult := collection.FindOne(ctx, bson.D{{Key: "name", Value: []byte{1, 2, 3}}})
	if err := findResult.Err(); err != nil {
		t.Fatal(err)
	}
	err = findResult.Decode(&decoded)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("find result", decoded)

	findResult = collection.FindOne(ctx, bson.D{{Key: "name", Value: "david"}})
	if err := findResult.Err(); err != nil {
		t.Fatal(err)
	}
}
