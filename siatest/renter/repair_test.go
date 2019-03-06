package renter

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/renter/siafile"
	"gitlab.com/NebulousLabs/Sia/node"
	"gitlab.com/NebulousLabs/Sia/siatest"
	"gitlab.com/NebulousLabs/fastrand"
)

// TestRenterRepairs executes a number of subtests using the same TestGroup to
// save time on initialization
func TestRenterRepairs(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a group for the subtests
	groupParams := siatest.GroupParams{
		Hosts:   5,
		Renters: 1,
		Miners:  1,
	}

	// Specify subtests to run
	subTests := []test{
		{"TestLocalRepair", testLocalRepair},
		{"TestRemoteRepair", testRemoteRepair},
	}

	// Run tests
	if err := runRenterTests(t, groupParams, subTests); err != nil {
		t.Fatal(err)
	}
}

// testLocalRepair tests if a renter correctly repairs a file from disk
// after a host goes offline.
func testLocalRepair(t *testing.T, tg *siatest.TestGroup) {
	// Grab the first of the group's renters
	renter := tg.Renters()[0]

	// Check that we have enough hosts for this test.
	if len(tg.Hosts()) < 2 {
		t.Fatal("This test requires at least 2 hosts")
	}

	// Set fileSize and redundancy for upload
	fileSize := int(modules.SectorSize)
	dataPieces := uint64(1)
	parityPieces := uint64(len(tg.Hosts())) - dataPieces

	// Upload file
	_, remoteFile, err := renter.UploadNewFileBlocking(fileSize, dataPieces, parityPieces, false)
	if err != nil {
		t.Fatal(err)
	}
	// Get the file info of the fully uploaded file. Tha way we can compare the
	// redundancies later.
	fi, err := renter.File(remoteFile)
	if err != nil {
		t.Fatal("failed to get file info", err)
	}

	// Take down one of the hosts and check if redundancy decreases.
	if err := tg.RemoveNode(tg.Hosts()[0]); err != nil {
		t.Fatal("Failed to shutdown host", err)
	}
	expectedRedundancy := float64(dataPieces+parityPieces-1) / float64(dataPieces)
	if err := renter.WaitForDecreasingRedundancy(remoteFile, expectedRedundancy); err != nil {
		t.Fatal("Redundancy isn't decreasing", err)
	}
	// Mine a block to trigger the repair loop so the chunk is marked as stuck
	m := tg.Miners()[0]
	if err := m.MineBlock(); err != nil {
		t.Fatal(err)
	}
	// Check to see if a chunk got marked as stuck
	err = renter.WaitForStuckChunksToBubble()
	if err != nil {
		t.Fatal(err)
	}
	// We should still be able to download
	if _, err := renter.DownloadByStream(remoteFile); err != nil {
		t.Fatal("Failed to download file", err)
	}
	// Bring up a new host and check if redundancy increments again.
	_, err = tg.AddNodes(node.HostTemplate)
	if err != nil {
		t.Fatal("Failed to create a new host", err)
	}
	if err := renter.WaitForUploadRedundancy(remoteFile, fi.Redundancy); err != nil {
		t.Fatal("File wasn't repaired", err)
	}
	// Check to see if a chunk got repaired and marked as unstuck
	err = renter.WaitForStuckChunksToRepair()
	if err != nil {
		t.Fatal(err)
	}
	// We should be able to download
	if _, err := renter.DownloadByStream(remoteFile); err != nil {
		t.Fatal("Failed to download file", err)
	}
}

// testRemoteRepair tests if a renter correctly repairs a file by
// downloading it after a host goes offline.
func testRemoteRepair(t *testing.T, tg *siatest.TestGroup) {
	// Grab the first of the group's renters
	r := tg.Renters()[0]

	// Check that we have enough hosts for this test.
	if len(tg.Hosts()) < 2 {
		t.Fatal("This test requires at least 2 hosts")
	}

	// Set fileSize and redundancy for upload
	fileSize := int(modules.SectorSize)
	dataPieces := uint64(1)
	parityPieces := uint64(len(tg.Hosts())) - dataPieces

	// Upload file
	localFile, remoteFile, err := r.UploadNewFileBlocking(fileSize, dataPieces, parityPieces, false)
	if err != nil {
		t.Fatal(err)
	}
	// Get the file info of the fully uploaded file. Tha way we can compare the
	// redundancieslater.
	fi, err := r.File(remoteFile)
	if err != nil {
		t.Fatal("failed to get file info", err)
	}

	// Delete the file locally.
	if err := localFile.Delete(); err != nil {
		t.Fatal("failed to delete local file", err)
	}

	// Take down all of the parity hosts and check if redundancy decreases.
	for i := uint64(0); i < parityPieces; i++ {
		if err := tg.RemoveNode(tg.Hosts()[0]); err != nil {
			t.Fatal("Failed to shutdown host", err)
		}
	}
	expectedRedundancy := float64(dataPieces+parityPieces-1) / float64(dataPieces)
	if err := r.WaitForDecreasingRedundancy(remoteFile, expectedRedundancy); err != nil {
		t.Fatal("Redundancy isn't decreasing", err)
	}
	// Mine a block to trigger the repair loop so the chunk is marked as stuck
	m := tg.Miners()[0]
	if err := m.MineBlock(); err != nil {
		t.Fatal(err)
	}
	// Check to see if a chunk got marked as stuck
	err = r.WaitForStuckChunksToBubble()
	if err != nil {
		t.Fatal(err)
	}
	// We should still be able to download
	if _, err := r.DownloadByStream(remoteFile); err != nil {
		t.Error("Failed to download file", err)
	}
	// Bring up new parity hosts and check if redundancy increments again.
	_, err = tg.AddNodeN(node.HostTemplate, int(parityPieces))
	if err != nil {
		t.Fatal("Failed to create a new host", err)
	}
	// When doing remote repair the redundancy might not reach 100%.
	expectedRedundancy = (1.0 - siafile.RemoteRepairDownloadThreshold) * fi.Redundancy
	if err := r.WaitForUploadRedundancy(remoteFile, expectedRedundancy); err != nil {
		t.Fatal("File wasn't repaired", err)
	}
	// Check to see if a chunk got repaired and marked as unstuck
	err = r.WaitForStuckChunksToRepair()
	if err != nil {
		t.Fatal(err)
	}
	// We should be able to download
	if _, err := r.DownloadByStream(remoteFile); err != nil {
		t.Fatal("Failed to download file", err)
	}
}

// TestStuckRepair tests if a renter correctly repairs the renter directory that
// has a mixture of stuck and unstuck files
//
// NOTE: since this test uploads a large amount of data we create its own group
// so to ensure the maximum amount of data can be uploaded
func TestStuckRepair(t *testing.T) {
	// if !build.VLONG {
	// 	t.SkipNow()
	// }
	t.Parallel()

	// Create a group for testing
	groupParams := siatest.GroupParams{
		Hosts:   5,
		Renters: 1,
		Miners:  1,
	}
	testDir := renterTestDir(t.Name())
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		t.Fatal("Failed to create group:", err)
	}
	defer tg.Close()

	// Grab the first of the group's renters
	r := tg.Renters()[0]

	// Create the following directory of files, each directory should have
	// >minNumFiles files that are all >minNumChunks chunks
	//
	// root/
	// dir1/
	// dir1/subDir1/
	// dir1/subDir2/
	// dir2/
	// dir2/subDir1/
	dirSiaPath := ""
	dir1 := "dir1"
	dir2 := "dir2"
	subDir1 := "subDir1"
	subDir2 := "subDir2"
	minNumFiles, minNumChunks := 3, 3
	err = createAndUploadDirectoryOfFiles(r, dirSiaPath, minNumChunks, minNumFiles, len(tg.Hosts()))
	if err != nil {
		t.Fatal(err)
	}

	err = createAndUploadDirectoryOfFiles(r, filepath.Join(dirSiaPath, dir1), minNumChunks, minNumFiles, len(tg.Hosts()))
	if err != nil {
		t.Fatal(err)
	}
	err = createAndUploadDirectoryOfFiles(r, filepath.Join(dirSiaPath, dir1, subDir1), minNumChunks, minNumFiles, len(tg.Hosts()))
	if err != nil {
		t.Fatal(err)
	}
	err = createAndUploadDirectoryOfFiles(r, filepath.Join(dirSiaPath, dir1, subDir2), minNumChunks, minNumFiles, len(tg.Hosts()))
	if err != nil {
		t.Fatal(err)
	}
	err = createAndUploadDirectoryOfFiles(r, filepath.Join(dirSiaPath, dir2), minNumChunks, minNumFiles, len(tg.Hosts()))
	if err != nil {
		t.Fatal(err)
	}
	err = createAndUploadDirectoryOfFiles(r, filepath.Join(dirSiaPath, dir2, subDir1), minNumChunks, minNumFiles, len(tg.Hosts()))
	if err != nil {
		t.Fatal(err)
	}

	// make sure root directory shows no stuck chunks
	if err := r.WaitForStuckChunksToRepair(); err != nil {
		t.Fatal(err)
	}

	// Take down half of parity hosts
	hosts := tg.Hosts()
	hostsToRemove := int(len(hosts) / 2)
	for i := 0; i < hostsToRemove; i++ {
		if err := tg.RemoveNode(hosts[i]); err != nil {
			t.Fatal(err)
		}
	}

	// Wait for redundancy to drop, redundancy should equal the number of hosts
	i := 0
	m := tg.Miners()[0]
	if err := m.MineBlock(); err != nil {
		t.Fatal(err)
	}
	err = build.Retry(1000, 100*time.Millisecond, func() error {
		i++
		if i%100 == 0 {
			if err := m.MineBlock(); err != nil {
				return err
			}
		}
		rf, err := r.Files()
		if err != nil {
			return err
		}
		// Wait for all files to drop in redundancy
		for _, f := range rf {
			if f.Redundancy != float64(len(tg.Hosts())) {
				return fmt.Errorf("expected redundancy to be %v got %v", len(tg.Hosts()), f.Redundancy)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Mine block to help trigger chunks being marked as stuck after the
	// redundancy dropping due to hosts going offline
	if err := m.MineBlock(); err != nil {
		t.Fatal(err)
	}
	// Wait for numStuckChunks to increase
	if err := r.WaitForStuckChunksToBubble(); err != nil {
		t.Fatal(err)
	}

	// Add a new parity host so files can repair
	_, err = tg.AddNodeN(node.HostTemplate, hostsToRemove)
	if err != nil {
		t.Fatal("Failed to create a new host", err)
	}

	// Wait for redundancy to increase
	i = 0
	if err := m.MineBlock(); err != nil {
		t.Fatal(err)
	}
	err = build.Retry(300, time.Second, func() error {
		i++
		if i%30 == 0 {
			if err := m.MineBlock(); err != nil {
				return err
			}
		}
		rf, err := r.Files()
		if err != nil {
			return err
		}
		for _, f := range rf {
			if f.Redundancy != float64(len(tg.Hosts())) {
				return fmt.Errorf("expected redundancy to be %v got %v", len(tg.Hosts()), f.Redundancy)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Now that the file redundancies have recovered there should be no stuck
	// chunks
	if err := r.WaitForStuckChunksToRepair(); err != nil {
		t.Fatal(err)
	}
}

func createAndUploadDirectoryOfFiles(renter *siatest.TestNode, dirSiaPath string, minNumChunks int, minNumFiles int, numHosts int) error {
	// Create Directory on sia
	var rd *siatest.RemoteDir
	if dirSiaPath != "" {
		fd := renter.FilesDir()
		ld, err := fd.CreateDir(dirSiaPath)
		if err != nil {
			return err
		}
		rd, err = renter.UploadDirectory(ld)
		if err != nil {
			return err
		}
	}

	numFiles := fastrand.Intn(minNumFiles) + minNumFiles
	for i := 0; i < numFiles; i++ {
		// Set fileSize and redundancy for upload
		fileSize := (minNumChunks + fastrand.Intn(minNumChunks)) * int(modules.SectorSize)
		dataPieces := uint64(1)
		parityPieces := uint64(numHosts) - dataPieces
		var err error
		if rd == nil {
			_, _, err = renter.UploadNewFileBlocking(fileSize, dataPieces, parityPieces, false)
		} else {
			_, err = renter.UploadNewFileToDirectoryBlocking(rd, fileSize, dataPieces, parityPieces, false)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
