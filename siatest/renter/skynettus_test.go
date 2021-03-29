package renter

import (
	"bytes"
	"testing"

	"github.com/eventials/go-tus"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/skynetlabs/skyd/node"
	"gitlab.com/skynetlabs/skyd/node/api/client"
	"gitlab.com/skynetlabs/skyd/siatest"
	"gitlab.com/skynetlabs/skyd/siatest/dependencies"
)

// TestSkynetTUSUploader runs all skynetTUSUploader related tests.
func TestSkynetTUSUploader(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Prepare a testgroup.
	groupParams := siatest.GroupParams{
		Hosts:   3,
		Miners:  1,
		Renters: 1,
	}
	groupDir := renterTestDir(t.Name())
	tg, err := siatest.NewGroupFromTemplate(groupDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Run tests.
	t.Run("SimpleUpload", func(t *testing.T) {
		testTUSUploaderSmallFile(t, tg.Renters()[0])
	})
	t.Run("UnstableConnection", func(t *testing.T) {
		testTUSUploaderUnstableConnection(t, tg)
	})
}

// testTUSUploadSmallFile tests uploading a small file using the TUS protocol.
func testTUSUploaderSmallFile(t *testing.T, r *siatest.TestNode) {
	// upload a 100 byte file in chunks of 10 bytes.
	fileSize := 100
	chunkSize := int64(fileSize / 10)
	uploadedData := fastrand.Bytes(fileSize)
	skylink, err := r.SkynetTUSUploadFromBytes(uploadedData, chunkSize)
	if err != nil {
		t.Fatal(err)
	}

	// Download the uploaded data and compare it to the uploaded data.
	downloadedData, _, err := r.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(uploadedData, downloadedData) {
		t.Fatal("data doesn't match")
	}
}

// testTUSUploaderUnstableConnection tests uploading with a TUS uploader where
// every chunk upload fails halfway through.
func testTUSUploaderUnstableConnection(t *testing.T, tg *siatest.TestGroup) {
	// Add a custom renter with dependency.
	rp := node.RenterTemplate
	rp.RenterDeps = &dependencies.DependencyUnstableTUSUpload{}
	nodes, err := tg.AddNodes(rp)
	if err != nil {
		t.Fatal(err)
	}
	r := nodes[0]
	defer func() {
		if err := tg.RemoveNode(r); err != nil {
			t.Fatal(err)
		}
	}()

	// Get a tus client.
	chunkSize := int64(10)
	tc, err := r.SkynetTUSClient(chunkSize)
	if err != nil {
		t.Fatal(err)
	}

	// upload a 1000 byte file in chunks of 10 bytes.
	uploadedData := fastrand.Bytes(1000)
	src := bytes.NewReader(uploadedData)
	upload := tus.NewUpload(src, src.Size(), tus.Metadata{}, "test")

	// Create uploader.
	uploader, err := tc.CreateUpload(upload)
	if err != nil {
		t.Fatal(err)
	}

	// Upload, this should fail after the first chunk.
	err = uploader.Upload()
	if err == nil {
		t.Fatal("upload should fail due to the dependency")
	}
	var success bool
	for i := 0; i < 200; i++ {
		uploader, err = tc.ResumeUpload(upload)
		if err != nil {
			t.Fatal(err)
		}
		// Upload
		err = uploader.Upload()
		if err != nil {
			continue // try again
		}
		// Success
		success = true
		break
	}
	if !success {
		t.Fatal("failed to upload data")
	}

	// Fetch skylink after upload is done.
	skylink, err := client.SkylinkFromTUSUpload(tc, uploader)
	if err != nil {
		t.Fatal(err)
	}

	// Download the uploaded data and compare it to the uploaded data.
	downloadedData, _, err := r.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(uploadedData, downloadedData) {
		t.Fatal("data doesn't match")
	}
}
