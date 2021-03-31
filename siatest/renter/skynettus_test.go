package renter

import (
	"bytes"
	"testing"
	"time"

	"github.com/eventials/go-tus"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/skynetlabs/skyd/node"
	"gitlab.com/skynetlabs/skyd/node/api/client"
	"gitlab.com/skynetlabs/skyd/siatest"
	"gitlab.com/skynetlabs/skyd/siatest/dependencies"
	"gitlab.com/skynetlabs/skyd/skymodules"
	"gitlab.com/skynetlabs/skyd/skymodules/renter"
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
	// Get the number of files before the test.
	dir, err := r.RenterDirRootGet(skymodules.SkynetFolder)
	if err != nil {
		t.Fatal(err)
	}
	nFilesBefore := dir.Directories[0].AggregateNumFiles

	// upload a 100 byte file in chunks of 10 bytes.
	chunkSize := 2 * int64(skymodules.ChunkSize(crypto.TypePlain, uint64(skymodules.RenterDefaultDataPieces)))
	fileSize := chunkSize*5 + chunkSize/2 // 5 1/2 chunks.
	uploadedData := fastrand.Bytes(int(fileSize))
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

	// Wait for a full pruning interval to implicitly check that we are not
	// deleting successful uploads from disk.
	time.Sleep(renter.PruneTUSUploadTimeout)

	// Wait a bit longer to make sure the pruning had time to finish.
	time.Sleep(100 * time.Millisecond)

	// Check that the number of files increased by 2. One for the regular sia
	// file and one for the extension.
	dir, err = r.RenterDirRootGet(skymodules.SkynetFolder)
	if err != nil {
		t.Fatal(err)
	}
	nFiles := dir.Directories[0].AggregateNumFiles
	if nFiles-nFilesBefore != 2 {
		t.Fatal("expected 2 new files but got", nFiles-nFilesBefore)
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
	skylink, err := client.SkylinkFromTUSURL(tc, uploader.Url())
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
