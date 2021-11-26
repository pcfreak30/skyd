package renter

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"

	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/node"
	"gitlab.com/SkynetLabs/skyd/siatest"
	"gitlab.com/SkynetLabs/skyd/siatest/dependencies"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
)

// TestRenterDownloadStreamCache checks that the download stream caching is
// functioning correctly - that there are no rough edges around weirdly sized
// files or alignments, and that the cache serves data correctly.
func TestRenterDownloadStreamCache(t *testing.T) {
	if testing.Short() || !build.VLONG {
		t.SkipNow()
	}
	t.Parallel()

	// Create a testgroup with a renter.
	groupParams := siatest.GroupParams{
		Hosts:   3,
		Renters: 1,
		Miners:  1,
	}
	tg, err := siatest.NewGroupFromTemplate(renterTestDir(t.Name()), groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := tg.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	// Upload a file to the renter.
	fileSize := 123456
	renter := tg.Renters()[0]
	localFile, remoteFile, err := renter.UploadNewFileBlocking(fileSize, 2, 1, false)
	if err != nil {
		t.Fatal(err)
	}

	// Download that file using a download stream.
	_, downloadedData, err := renter.DownloadByStream(remoteFile)
	if err != nil {
		t.Fatal(err)
	}
	err = localFile.Equal(downloadedData)
	if err != nil {
		t.Fatal(err)
	}

	// Test downloading a bunch of random partial streams. Generally these will
	// not be aligned at all.
	for i := 0; i < 25; i++ {
		// Get random values for 'from' and 'to'.
		from := fastrand.Intn(fileSize)
		to := fastrand.Intn(fileSize - from)
		to += from
		if to == from {
			continue
		}

		// Stream some data.
		streamedPartialData, err := renter.StreamPartial(remoteFile, localFile, uint64(from), uint64(to))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Compare(streamedPartialData, downloadedData[from:to]) != 0 {
			t.Error("Read range returned the wrong data")
		}
	}

	// Test downloading a bunch of partial streams that start from 0.
	for i := 0; i < 25; i++ {
		// Get random values for 'from' and 'to'.
		from := 0
		to := fastrand.Intn(fileSize - from)
		if to == from {
			continue
		}

		// Stream some data.
		streamedPartialData, err := renter.StreamPartial(remoteFile, localFile, uint64(from), uint64(to))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Compare(streamedPartialData, downloadedData[from:to]) != 0 {
			t.Error("Read range returned the wrong data")
		}
	}

	// Test a series of chosen values to have specific alignments.
	for i := 0; i < 5; i++ {
		for j := 0; j < 3; j++ {
			// Get random values for 'from' and 'to'.
			from := 0 + j
			to := 8190 + i
			if to == from {
				continue
			}

			// Stream some data.
			streamedPartialData, err := renter.StreamPartial(remoteFile, localFile, uint64(from), uint64(to))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Compare(streamedPartialData, downloadedData[from:to]) != 0 {
				t.Error("Read range returned the wrong data")
			}
		}
	}
	for i := 0; i < 5; i++ {
		for j := 0; j < 5; j++ {
			// Get random values for 'from' and 'to'.
			from := 8190 + j
			to := 16382 + i
			if to == from {
				continue
			}

			// Stream some data.
			streamedPartialData, err := renter.StreamPartial(remoteFile, localFile, uint64(from), uint64(to))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Compare(streamedPartialData, downloadedData[from:to]) != 0 {
				t.Error("Read range returned the wrong data")
			}
		}
	}
	for i := 0; i < 3; i++ {
		// Get random values for 'from' and 'to'.
		from := fileSize - i
		to := fileSize
		if to == from {
			continue
		}

		// Stream some data.
		streamedPartialData, err := renter.StreamPartial(remoteFile, localFile, uint64(from), uint64(to))
		if err != nil {
			t.Fatal(err, from, to)
		}
		if bytes.Compare(streamedPartialData, downloadedData[from:to]) != 0 {
			t.Error("Read range returned the wrong data")
		}
	}
}

// TestRenterStream executes a number of subtests using the same TestGroup to
// save time on initialization
func TestRenterStream(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a group for the subtests
	groupParams := siatest.GroupParams{
		Hosts:   3,
		Renters: 1,
		Miners:  1,
	}
	groupDir := renterTestDir(t.Name())

	// Specify subtests to run
	subTests := []siatest.SubTest{
		{Name: "TestStreamLargeFile", Test: testStreamLargeFile},
		{Name: "TestStreamRepair", Test: testStreamRepair},
		{Name: "TestUploadStreaming", Test: testUploadStreaming},
		{Name: "TestUploadStreamingWithBadDeps", Test: testUploadStreamingWithBadDeps},
	}

	// Run tests
	if err := siatest.RunSubTests(t, groupParams, groupDir, subTests); err != nil {
		t.Fatal(err)
	}
}

// testStreamLargeFile tests that using the streaming endpoint to download
// multiple chunks works.
func testStreamLargeFile(t *testing.T, tg *siatest.TestGroup) {
	// Grab the first of the group's renters
	renter := tg.Renters()[0]
	// Upload file, creating a piece for each host in the group
	dataPieces := uint64(2)
	parityPieces := uint64(len(tg.Hosts())) - dataPieces
	ct := crypto.TypeDefaultRenter
	fileSize := int(10 * siatest.ChunkSize(dataPieces, ct))
	localFile, remoteFile, err := renter.UploadNewFileBlocking(fileSize, dataPieces, parityPieces, false)
	if err != nil {
		t.Fatal("Failed to upload a file for testing: ", err)
	}
	// Stream the file partially a few times. At least 1 byte is streamed.
	for i := 0; i < 5; i++ {
		from := fastrand.Intn(fileSize - 1)             // [0..fileSize-2]
		to := from + 1 + fastrand.Intn(fileSize-from-1) // [from+1..fileSize-1]
		_, err = renter.StreamPartial(remoteFile, localFile, uint64(from), uint64(to))
		if err != nil {
			t.Fatal(err)
		}
	}

	// Verify some specific cases
	rand := uint64(fastrand.Intn(fileSize))
	var tests = []struct {
		from uint64
		to   uint64
	}{
		{0, 0},       // Requesting the first byte of a file
		{0, 1},       // Request the first byte of a file
		{rand, rand}, // Requesting a random single byte of the file
	}
	for _, test := range tests {
		_, err = renter.StreamPartial(remoteFile, localFile, test.from, test.to)
		if err != nil {
			t.Log("Failed Test Case:", test)
			t.Fatal(err)
		}
	}
}

// testStreamRepair tests if repairing a file using the streaming endpoint
// works.
func testStreamRepair(t *testing.T, tg *siatest.TestGroup) {
	// Grab the first of the group's renters
	r := tg.Renters()[0]

	// Check that we have enough hosts for this test.
	if len(tg.Hosts()) < 2 {
		t.Fatal("This test requires at least 2 hosts")
	}

	// Set fileSize and redundancy for upload
	fileSize := int(5*modules.SectorSize) + siatest.Fuzz()
	dataPieces := uint64(1)
	parityPieces := uint64(len(tg.Hosts())) - dataPieces

	t.Log("fileSize", fileSize)

	// Upload file
	localFile, remoteFile, err := r.UploadNewFileBlocking(fileSize, dataPieces, parityPieces, false)
	if err != nil {
		t.Fatal(err)
	}

	// Move the file locally to make sure the repair loop can't find it.
	if err := localFile.Move(); err != nil {
		t.Fatal("failed to delete local file", err)
	}

	// Take down all of the hosts and check if redundancy decreases.
	var hostsRemoved []*siatest.TestNode
	hosts := tg.Hosts()
	for i := uint64(0); i < parityPieces+dataPieces; i++ {
		hostsRemoved = append(hostsRemoved, hosts[i])
	}
	if err := tg.RemoveNodeN(hostsRemoved...); err != nil {
		t.Fatal("Failed to shutdown host", err)
	}
	// Wait for the workers to disappear.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		wps, err := r.RenterWorkersGet()
		if err != nil {
			t.Fatal(err)
		}
		if wps.NumWorkers != 0 {
			return fmt.Errorf("wrong number of workers %v != %v", wps.NumWorkers, 0)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.WaitForDecreasingRedundancy(remoteFile, 0); err != nil {
		t.Fatal("Redundancy isn't decreasing", err)
	}
	// Bring up hosts to replace the ones that went offline.
	_, err = tg.AddNodeN(node.HostTemplate, len(hostsRemoved))
	if err != nil {
		t.Fatal("Failed to replace hosts", err)
	}

	// Read the contents of the file from disk.
	b, err := ioutil.ReadFile(localFile.Path())
	if err != nil {
		t.Fatal(err)
	}
	// Prepare fake, corrupt contents as well.
	corruptB := fastrand.Bytes(len(b))
	// Try repairing the file with the corrupt data. This should fail.
	if err := r.RenterUploadStreamRepairPost(bytes.NewReader(corruptB), remoteFile.SiaPath()); err == nil {
		t.Fatal("Corrupt file repair should fail")
	}
	if err := r.WaitForDecreasingRedundancy(remoteFile, 0); err != nil {
		t.Fatal("Redundancy isn't staying at 0", err)
	}
	if err := r.RenterUploadStreamRepairPost(bytes.NewReader(b), remoteFile.SiaPath()); err != nil {
		t.Fatal(err)
	}
	if err := r.WaitForUploadHealth(remoteFile); err != nil {
		t.Fatal("File wasn't repaired", err)
	}
	// We should be able to download
	err = build.Retry(100, 100*time.Millisecond, func() error {
		if _, _, err := r.DownloadByStream(remoteFile); err != nil {
			return errors.AddContext(err, "Failed to download file")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Repair the file again to make sure we don't get stuck on chunks that are
	// already repaired. Datapieces and paritypieces can be set to 0 as long as
	// repair is true.
	if err := r.RenterUploadStreamRepairPost(bytes.NewReader(b), remoteFile.SiaPath()); err != nil {
		t.Fatal(err)
	}
}

// testUploadStreaming uploads random data using the upload streaming API.
func testUploadStreaming(t *testing.T, tg *siatest.TestGroup) {
	if len(tg.Renters()) == 0 {
		t.Fatal("Test requires at least 1 renter")
	}
	r := tg.Renters()[0]

	// Define the upload Stream test
	uploadStreamTest := func(siaPath skymodules.SiaPath, fileSize int) {
		// Create some random data to write.
		data := fastrand.Bytes(fileSize)
		d := bytes.NewReader(data)

		// Upload the data.
		err := r.RenterUploadStreamPost(d, siaPath, 1, uint64(len(tg.Hosts())-1), false)
		if err != nil {
			t.Fatal(siaPath, err)
		}

		// Make sure the file reached full redundancy.
		err = build.Retry(100, 600*time.Millisecond, func() error {
			rfg, err := r.RenterFileGet(siaPath)
			if err != nil {
				return err
			}
			if rfg.File.Redundancy < float64(len(tg.Hosts())) {
				return fmt.Errorf("expected redundancy %v but was %v",
					len(tg.Hosts()), rfg.File.Redundancy)
			}
			if rfg.File.Filesize != uint64(len(data)) {
				return fmt.Errorf("expected uploaded file to have size %v but was %v",
					len(data), rfg.File.Filesize)
			}
			return nil
		})
		if err != nil {
			t.Fatal(siaPath, err)
		}
		// Download the file again.
		_, downloadedData, err := r.RenterDownloadHTTPResponseGet(siaPath, 0, uint64(len(data)), true, false)
		if err != nil {
			t.Fatal(siaPath, err)
		}
		// Compare downloaded data to original one.
		if !bytes.Equal(data, downloadedData) {
			t.Log("originalData:", data)
			t.Log("downloadedData:", downloadedData)
			t.Fatal("Downloaded data doesn't match uploaded data for", siaPath)
		}
	}

	// Define sizes to test
	ss := int(modules.SectorSize)
	rand := fastrand.Intn(2*ss) + siatest.Fuzz() + 2 // between 1 and 2*SectorSize + 3 bytes
	sizes := []int{0, 1, ss - 1, ss, ss + 1, 2*ss - 1, 2 * ss, 2*ss + 1, rand}

	// Run Tests
	for _, size := range sizes {
		siaPath, err := skymodules.NewSiaPath(fmt.Sprintf("%v-byte-file", size))
		if err != nil {
			t.Fatal(err)
		}
		uploadStreamTest(siaPath, size)
	}
}

// testUploadStreamingWithBadDeps uploads random data using the upload streaming
// API, depending on a disrupt to cause a failure. This is a regression test
// that would have caused a production build panic.
func testUploadStreamingWithBadDeps(t *testing.T, tg *siatest.TestGroup) {
	// Create a custom renter with a dependency and remove it after the test is
	// done.
	renterParams := node.Renter(filepath.Join(renterTestDir(t.Name()), "renter"))
	renterParams.RenterDeps = &dependencies.DependencyFailUploadStreamFromReader{}
	nodes, err := tg.AddNodes(renterParams)
	if err != nil {
		t.Fatal(err)
	}
	renter := nodes[0]
	defer func() {
		if err := tg.RemoveNode(renter); err != nil {
			t.Fatal(err)
		}
	}()

	// Create some random data to write.
	fileSize := fastrand.Intn(2*int(modules.SectorSize)) + siatest.Fuzz() + 2 // between 1 and 2*SectorSize + 3 bytes
	data := fastrand.Bytes(fileSize)
	d := bytes.NewReader(data)

	// Upload the data.
	siaPath, err := skymodules.NewSiaPath("/foo")
	if err != nil {
		t.Fatal(err)
	}
	err = renter.RenterUploadStreamPost(d, siaPath, 1, uint64(len(tg.Hosts())-1), false)
	if err == nil {
		t.Fatal("dependency injection should have caused the upload to fail")
	}
}
