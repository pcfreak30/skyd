package skynet

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/eventials/go-tus"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/node"
	"gitlab.com/SkynetLabs/skyd/node/api/client"
	"gitlab.com/SkynetLabs/skyd/siatest"
	"gitlab.com/SkynetLabs/skyd/siatest/dependencies"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter"
	"go.sia.tech/siad/modules"
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
	groupDir := skynetTestDir(t.Name())
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
	t.Run("Basic", func(t *testing.T) {
		testTUSUploaderBasic(t, tg.Renters()[0])
	})
	t.Run("PruneIdle", func(t *testing.T) {
		testTUSUploaderPruneIdle(t, tg.Renters()[0])
	})
	t.Run("UnstableConnection", func(t *testing.T) {
		testTUSUploaderUnstableConnection(t, tg)
	})
}

// testTUSUploadBasic tests uploading multiple files using the TUS protocol and
// verifies that pruning doesn't delete completed .sia files.
func testTUSUploaderBasic(t *testing.T, r *siatest.TestNode) {
	// Get the number of files before the test.
	dir, err := r.RenterDirRootGet(skymodules.SkynetFolder)
	if err != nil {
		t.Fatal(err)
	}
	nFilesBefore := dir.Directories[0].AggregateNumFiles

	// Declare the chunkSize.
	chunkSize := 2 * int64(skymodules.ChunkSize(crypto.TypePlain, uint64(skymodules.RenterDefaultDataPieces)))

	// Declare a test helper that uploads a file and downloads it.
	uploadTest := func(fileSize int64) {
		uploadedData := fastrand.Bytes(int(fileSize))
		skylink, err := r.SkynetTUSUploadFromBytes(uploadedData, chunkSize)
		if err != nil {
			t.Fatal(err)
		}

		// Download the uploaded data and compare it to the uploaded data.
		downloadedData, sm, err := r.SkynetSkylinkGet(skylink)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(uploadedData, downloadedData) {
			t.Fatal("data doesn't match")
		}
		if sm.Length != uint64(len(uploadedData)) {
			t.Fatal("wrong length in metadata")
		}
	}

	// Upload a large file.
	uploadTest(chunkSize*5 + chunkSize/2)

	// Upload a byte that's smaller than a sector but still a large file.
	uploadTest(int64(modules.SectorSize) - 1)

	// Upload a small file.
	uploadTest(1)

	// Upload empty file.
	uploadTest(0)

	// Wait for two full pruning intervals to make sure pruning ran at least
	// once.
	time.Sleep(2 * renter.PruneTUSUploadTimeout)

	// Check that the number of files increased by 6. One for the small and zero
	// uploads and 2 for each of the large ones.
	dir, err = r.RenterDirRootGet(skymodules.SkynetFolder)
	if err != nil {
		t.Fatal(err)
	}
	nFiles := dir.Directories[0].AggregateNumFiles
	if nFiles-nFilesBefore != 6 {
		t.Fatal("expected 6 new files but got", nFiles-nFilesBefore)
	}
}

// testTUSUploaderPruneIdle checks that incomplete uploads get pruned after a
// while and have their .sia files deleted from disk.
func testTUSUploaderPruneIdle(t *testing.T, r *siatest.TestNode) {
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

	// Get a tus client and upload.
	tc, upload, err := r.SkynetTUSNewUploadFromBytes(uploadedData, chunkSize)
	if err != nil {
		t.Fatal(err)
	}

	// Start upload.
	uploader, err := tc.CreateUpload(upload)
	if err != nil {
		t.Fatal(err)
	}

	// Upload a single chunk.
	err = uploader.UploadChunck()
	if err != nil {
		t.Fatal(err)
	}

	// Wait for two full pruning intervals to make sure pruning ran at least
	// once.
	time.Sleep(2 * renter.PruneTUSUploadTimeout)

	// Upload another chunk.
	err = uploader.UploadChunck()
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatal(err)
	}

	// Try to resume upload.
	uploader, err = tc.ResumeUpload(upload)
	if err == nil || !errors.Contains(err, tus.ErrUploadNotFound) {
		t.Fatal(err)
	}

	// Check that the number of files didn't increase since the new files were
	// purged.
	dir, err = r.RenterDirRootGet(skymodules.SkynetFolder)
	if err != nil {
		t.Fatal(err)
	}
	nFiles := dir.Directories[0].AggregateNumFiles
	if nFiles-nFilesBefore != 0 {
		t.Fatal("expected 0 new files but got", nFiles-nFilesBefore)
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
	chunkSize := 2 * int64(skymodules.ChunkSize(crypto.TypePlain, uint64(skymodules.RenterDefaultDataPieces)))
	tc, err := r.SkynetTUSClient(chunkSize)
	if err != nil {
		t.Fatal(err)
	}

	// Upload some chunks.
	uploadedData := fastrand.Bytes(int(chunkSize * 10))
	src := bytes.NewReader(uploadedData)
	upload := tus.NewUpload(src, src.Size(), tus.Metadata{}, "test")

	// Create uploader.
	uploader, err := tc.CreateUpload(upload)
	if err != nil {
		t.Fatal(err)
	}

	// Upload all the chunks. Whenever we encounter an error we resume the
	// upload until we run out of retries.
	remainingChunks := len(uploadedData) / int(chunkSize)
	remainingTries := 5 * remainingChunks
	for remainingChunks > 0 {
		err = uploader.UploadChunck()
		if err == nil {
			remainingChunks--
			continue // continue
		}
		// Decrement remaining tries.
		if remainingTries == 0 {
			t.Fatal("out of retries")
		}
		remainingTries--
		// Resume upload.
		uploader, err = tc.ResumeUpload(upload)
		if err != nil {
			t.Fatal(err)
		}
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
