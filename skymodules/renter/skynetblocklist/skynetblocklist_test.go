package skynetblocklist

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/encoding"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/persist"
)

// testDir is a helper function for creating the testing directory
func testDir(name string) string {
	return build.TempDir("skynetblocklist", name)
}

// checkNumPersistedLinks checks that the expected number of links has been
// persisted on disk by checking the size of the persistence file.
func checkNumPersistedLinks(blocklistPath string, numLinks int) error {
	expectedSize := numLinks*int(persistSize) + int(persist.MetadataPageSize)
	if fi, err := os.Stat(blocklistPath); err != nil {
		return errors.AddContext(err, "failed to get blocklist filesize")
	} else if fi.Size() != int64(expectedSize) {
		return fmt.Errorf("expected %v links and to have a filesize of %v but was %v", numLinks, expectedSize, fi.Size())
	}
	return nil
}

// checkIsBlocked is a helper to check the IsBlocked method for both if the
// skylink is blocked but also if the data should be deleted.
func checkIsBlocked(sb *SkynetBlocklist, isBlockedExpected, shouldDeleteExpected bool, skylink skymodules.Skylink) error {
	isBlocked, shouldDelete := sb.IsBlocked(skylink)
	if isBlocked != isBlockedExpected {
		return fmt.Errorf("isBlocked %v, expected %v", isBlocked, isBlockedExpected)
	}
	if shouldDelete != shouldDeleteExpected {
		return fmt.Errorf("should delete %v, expected %v", shouldDelete, shouldDeleteExpected)
	}
	return nil
}

// TestPersist tests the persistence of the Skynet blocklist.
func TestPersist(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Define blocklist tests
	var skylink skymodules.Skylink
	blocklistTest := func(hash crypto.Hash, ppe int64, shouldDeleteExpected bool) error {
		// Create a new SkynetBlocklist
		testdir := testDir(t.Name())
		sb, err := New(testdir)
		if err != nil {
			t.Fatal(err)
		}

		filename := filepath.Join(testdir, persistFile)
		if filename != sb.staticAop.FilePath() {
			t.Fatalf("Expected filepath %v, was %v", filename, sb.staticAop.FilePath())
		}

		// There should be no skylinks in the blocklist
		if len(sb.hashes) != 0 {
			t.Fatal("Expected blocklist to be empty but found:", len(sb.hashes))
		}

		// Create the inputs and update the initial blocklist
		add := []crypto.Hash{hash}
		remove := []crypto.Hash{hash}
		err = sb.UpdateBlocklist(add, remove, ppe)
		if err != nil {
			return err
		}

		// Blocklist should be empty because we added and then removed the same
		// skylink
		if len(sb.hashes) != 0 {
			return fmt.Errorf("Expected blocklist to be empty but found: %v", len(sb.hashes))
		}

		// Verify that the correct number of links were persisted to verify no links
		// are being truncated
		if err := checkNumPersistedLinks(filename, 2); err != nil {
			return fmt.Errorf("error verifying correct number of links: %v", err)
		}

		// Add the skylink again
		err = sb.UpdateBlocklist(add, []crypto.Hash{}, ppe)
		if err != nil {
			return err
		}

		// There should be 1 element in the blocklist now
		if len(sb.hashes) != 1 {
			return fmt.Errorf("Expected 1 element in the blocklist but found: %v", len(sb.hashes))
		}
		if err := checkIsBlocked(sb, true, shouldDeleteExpected, skylink); err != nil {
			return err
		}

		// Verify persist file size
		if err := checkNumPersistedLinks(filename, 3); err != nil {
			return fmt.Errorf("error verifying correct number of links: %v", err)
		}

		// Load a new Skynet Blocklist to verify the contents from disk get loaded
		// properly
		sb2, err := New(testdir)
		if err != nil {
			return err
		}

		// Verify that the correct number of links were persisted to verify no links
		// are being truncated
		if err := checkNumPersistedLinks(filename, 3); err != nil {
			return fmt.Errorf("error verifying correct number of links: %v", err)
		}

		// There should be 1 element in the blocklist
		if len(sb2.hashes) != 1 {
			return fmt.Errorf("Expected 1 element in the blocklist but found: %v", len(sb2.hashes))
		}
		if err := checkIsBlocked(sb, true, shouldDeleteExpected, skylink); err != nil {
			return err
		}

		// Add the skylink again
		err = sb2.UpdateBlocklist(add, []crypto.Hash{}, ppe)
		if err != nil {
			return err
		}

		// There should still only be 1 element in the blocklist
		if len(sb2.hashes) != 1 {
			return fmt.Errorf("Expected 1 element in the blocklist but found: %v", len(sb2.hashes))
		}
		if err := checkIsBlocked(sb2, true, shouldDeleteExpected, skylink); err != nil {
			return err
		}

		// Verify persist file size
		if err := checkNumPersistedLinks(filename, 3); err != nil {
			return fmt.Errorf("error verifying correct number of links: %v", err)
		}

		// Load another new Skynet Blocklist to verify the contents from disk get loaded
		// properly
		sb3, err := New(testdir)
		if err != nil {
			return err
		}

		// Verify that the correct number of links were persisted to verify no links
		// are being truncated
		if err := checkNumPersistedLinks(filename, 3); err != nil {
			return fmt.Errorf("error verifying correct number of links: %v", err)
		}

		// There should be 1 element in the blocklist
		if len(sb3.hashes) != 1 {
			return fmt.Errorf("Expected 1 element in the blocklist but found: %v", len(sb3.hashes))
		}
		if err := checkIsBlocked(sb3, true, shouldDeleteExpected, skylink); err != nil {
			return err
		}
		return nil
	}

	// Check once where the file should not be deleted due to being in the probationary period
	hash := crypto.HashObject(skylink.MerkleRoot())
	probationaryPeriodEnd := time.Now().Add(time.Hour).Unix()
	err := blocklistTest(hash, probationaryPeriodEnd, false)
	if err != nil {
		t.Fatal(err)
	}

	// Check once where the file should be deleted due to not being in the probationary period
	probationaryPeriodEnd = 0
	err = blocklistTest(hash, probationaryPeriodEnd, true)
	if err != nil {
		t.Fatal(err)
	}
}

// TestPersistCorruption tests the persistence of the Skynet blocklist when corruption occurs.
func TestPersistCorruption(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a new SkynetBlocklist
	testdir := testDir(t.Name())
	sb, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	filename := filepath.Join(testdir, persistFile)
	if filename != sb.staticAop.FilePath() {
		t.Fatalf("Expected filepath %v, was %v", filename, sb.staticAop.FilePath())
	}

	// There should be no skylinks in the blocklist
	if len(sb.hashes) != 0 {
		t.Fatal("Expected blocklist to be empty but found:", len(sb.hashes))
	}

	// Append a bunch of random data to the end of the blocklist file to test
	// corruption
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, skymodules.DefaultFilePerm)
	if err != nil {
		t.Fatal(err)
	}
	minNumBytes := int(2 * persist.MetadataPageSize)
	_, err = f.Write(fastrand.Bytes(minNumBytes + fastrand.Intn(minNumBytes)))
	if err != nil {
		t.Fatal(err)
	}
	err = f.Close()
	if err != nil {
		t.Fatal(err)
	}

	// The filesize with corruption should be greater than the persist length.
	fi, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	filesize := fi.Size()
	if uint64(filesize) <= sb.staticAop.PersistLength() {
		t.Fatalf("Expected file size greater than %v, got %v", sb.staticAop.PersistLength(), filesize)
	}

	// Update blocklist
	var skylink skymodules.Skylink
	hash := crypto.HashObject(skylink.MerkleRoot())
	add := []crypto.Hash{hash}
	remove := []crypto.Hash{hash}
	err = sb.UpdateBlocklist(add, remove, 0)
	if err != nil {
		t.Fatal(err)
	}

	// The filesize should be equal to the persist length now due to the
	// truncate when updating.
	fi, err = os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	filesize = fi.Size()
	if uint64(filesize) != sb.staticAop.PersistLength() {
		t.Fatalf("Expected file size %v, got %v", sb.staticAop.PersistLength(), filesize)
	}

	// Blocklist should be empty because we added and then removed the same
	// skylink
	if len(sb.hashes) != 0 {
		t.Fatal("Expected blocklist to be empty but found:", len(sb.hashes))
	}

	// Add the skylink again
	err = sb.UpdateBlocklist(add, []crypto.Hash{}, 0)
	if err != nil {
		t.Fatal(err)
	}

	// There should be 1 element in the blocklist now
	if len(sb.hashes) != 1 {
		t.Fatal("Expected 1 element in the blocklist but found:", len(sb.hashes))
	}
	err = checkIsBlocked(sb, true, true, skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Load a new Skynet Blocklist to verify the contents from disk get loaded
	// properly
	sb2, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	// There should be 1 element in the blocklist
	if len(sb2.hashes) != 1 {
		t.Fatal("Expected 1 element in the blocklist but found:", len(sb2.hashes))
	}
	err = checkIsBlocked(sb2, true, true, skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Add the skylink again
	err = sb2.UpdateBlocklist(add, []crypto.Hash{}, 0)
	if err != nil {
		t.Fatal(err)
	}

	// There should still only be 1 element in the blocklist
	if len(sb2.hashes) != 1 {
		t.Fatal("Expected 1 element in the blocklist but found:", len(sb2.hashes))
	}
	err = checkIsBlocked(sb2, true, true, skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Load another new Skynet Blocklist to verify the contents from disk get loaded
	// properly
	sb3, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	// There should be 1 element in the blocklist
	if len(sb3.hashes) != 1 {
		t.Fatal("Expected 1 element in the blocklist but found:", len(sb3.hashes))
	}
	err = checkIsBlocked(sb3, true, true, skylink)
	if err != nil {
		t.Fatal(err)
	}

	// The final filesize should be equal to the persist length.
	fi, err = os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	filesize = fi.Size()
	if uint64(filesize) != sb3.staticAop.PersistLength() {
		t.Fatalf("Expected file size %v, got %v", sb3.staticAop.PersistLength(), filesize)
	}

	// Verify that the correct number of links were persisted to verify no links
	// are being truncated. Expect 3 links for the Add, remove, Add. Then final
	// add would not be persisted because it already existed.
	if err = checkNumPersistedLinks(filename, 3); err != nil {
		t.Errorf("error verifying correct number of links: %v", err)
	}
}

// TestMarshalSia probes the marshalSia and unmarshalSia methods
func TestMarshalSia(t *testing.T) {
	t.Parallel()
	// Test MarshalSia
	var skylink skymodules.Skylink
	var buf bytes.Buffer
	merkleRoot := skylink.MerkleRoot()
	merkleRootHash := crypto.HashObject(merkleRoot)
	listed := false
	probationaryPeriodEnd := int64(123)
	ll := persistEntry{merkleRootHash, probationaryPeriodEnd, listed}
	writtenBytes := encoding.Marshal(ll)
	buf.Write(writtenBytes)
	if uint64(buf.Len()) != persistSize {
		t.Fatalf("Expected buf to be of size %v but got %v", persistSize, buf.Len())
	}
	ll.Listed = true
	writtenBytes = encoding.Marshal(ll)
	buf.Write(writtenBytes)
	if uint64(buf.Len()) != 2*persistSize {
		t.Fatalf("Expected buf to be of size %v but got %v", 2*persistSize, buf.Len())
	}

	readBytes := buf.Bytes()
	if uint64(len(readBytes)) != 2*persistSize {
		t.Fatalf("Expected %v read bytes but got %v", 2*persistSize, len(readBytes))
	}
	err := encoding.Unmarshal(readBytes[:persistSize], &ll)
	if err != nil {
		t.Fatal(err)
	}
	if merkleRootHash != ll.Hash {
		t.Fatalf("MerkleRoot hashes don't match, expected %v, got %v", merkleRootHash, ll.Hash)
	}
	if probationaryPeriodEnd != ll.ProbationaryPeriodEnd {
		t.Fatalf("Probationary periods don't match, expected %v, got %v", probationaryPeriodEnd, ll.ProbationaryPeriodEnd)
	}
	if ll.Listed {
		t.Fatal("expected persisted link to not be blocked")
	}
	err = encoding.Unmarshal(readBytes[persistSize:2*persistSize], &ll)
	if err != nil {
		t.Fatal(err)
	}
	if merkleRootHash != ll.Hash {
		t.Fatalf("MerkleRoot hashes don't match, expected %v, got %v", merkleRootHash, ll.Hash)
	}
	if probationaryPeriodEnd != ll.ProbationaryPeriodEnd {
		t.Fatalf("Probationary periods don't match, expected %v, got %v", probationaryPeriodEnd, ll.ProbationaryPeriodEnd)
	}
	if !ll.Listed {
		t.Fatal("expected persisted link to be blocked")
	}

	// Test unmarshalBlocklist
	blocklist, err := unmarshalObjects(&buf)
	if err != nil {
		t.Fatal(err)
	}

	// Since the merkleroot is the same the blocklist should only have a length
	// of 1 since the non blocklisted merkleroot was added first
	if len(blocklist) != 1 {
		t.Fatalf("Incorrect number of blocklisted merkleRoots, expected %v, got %v", 1, len(blocklist))
	}
	ppe, ok := blocklist[merkleRootHash]
	if !ok {
		t.Fatal("merkleroot not found in blocklist")
	}
	if ppe != probationaryPeriodEnd {
		t.Fatalf("probationaryPeriodEnd mismatch; found %v, expected %v", ppe, probationaryPeriodEnd)
	}
}
