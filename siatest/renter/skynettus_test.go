package renter

import (
	"bytes"
	"testing"

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
