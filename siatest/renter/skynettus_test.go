package renter

import (
	"fmt"
	"net"
	"testing"

	"github.com/eventials/go-tus"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/skynetlabs/skyd/siatest"
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
}

// testTUSUploadSmallFile tests uploading a small file using the TUS protocol.
func testTUSUploaderSmallFile(t *testing.T, r *siatest.TestNode) {
	// create the tus client.
	_, port, err := net.SplitHostPort(r.APIAddress())
	if err != nil {
		t.Fatal(err)
	}

	// upload a 100 byte file in chunks of 10 bytes.
	fileSize := 100
	chunkSize := fileSize / 10

	// create the client.
	config := tus.DefaultConfig()
	config.ChunkSize = int64(chunkSize)
	client, err := tus.NewClient(fmt.Sprintf("http://localhost:%v/skynet/tus", port), config)
	if err != nil {
		t.Fatal(err)
	}

	// create an upload from a file.
	upload := tus.NewUploadFromBytes(fastrand.Bytes(fileSize))

	// create the uploader.
	uploader, err := client.CreateUpload(upload)
	if err != nil {
		t.Fatal(err)
	}

	// start the uploading process.
	err = uploader.Upload()
	if err != nil {
		t.Fatal(err)
	}
}
