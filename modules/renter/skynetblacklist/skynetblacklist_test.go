package skynetblacklist

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
)

var (
	testPersistFile = filepath.Join("testdata", persistFile)
)

// testDir is a helper function for creating the testing directory
func testDir(name string) string {
	return build.TempDir("skynetblacklist", name)
}

// checkNumPersistedLinks checks that the expected number of links has been
// persisted on disk by checking the size of the persistence file.
func checkNumPersistedLinks(blacklistPath string, numLinks int) error {
	expectedSize := numLinks*int(persistSize) + int(persist.MetadataPageSize)
	if fi, err := os.Stat(blacklistPath); err != nil {
		return errors.AddContext(err, "failed to get blacklist filesize")
	} else if fi.Size() != int64(expectedSize) {
		return fmt.Errorf("expected %v links and to have a filesize of %v but was %v", numLinks, expectedSize, fi.Size())
	}
	return nil
}

// TestPersist tests the persistence of the Skynet blacklist.
func TestPersist(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a new SkynetBlacklist
	testdir := testDir(t.Name())
	sb, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	filename := filepath.Join(testdir, persistFile)
	if filename != sb.staticAop.FilePath() {
		t.Fatalf("Expected filepath %v, was %v", filename, sb.staticAop.FilePath())
	}

	// There should be no skylinks in the blacklist
	if len(sb.merkleRoots) != 0 {
		t.Fatal("Expected blacklist to be empty but found:", len(sb.merkleRoots))
	}

	// Try adding and removing the same link.
	var skylink modules.Skylink
	add := []modules.Skylink{skylink}
	remove := []modules.Skylink{skylink}
	err = sb.UpdateBlacklist(add, remove)
	if !errors.Contains(err, ErrInexistentSkylink) {
		t.Fatalf("Expected err %v, got %v", ErrInexistentSkylink, err)
	}

	// Blacklist should remain empty.
	if len(sb.merkleRoots) != 0 {
		t.Fatal("Expected blacklist to be empty but found:", len(sb.merkleRoots))
	}

	// Add the skylink
	err = sb.UpdateBlacklist(add, []modules.Skylink{})
	if err != nil {
		t.Fatal(err)
	}

	// There should be 1 element in the blacklist now
	if len(sb.merkleRoots) != 1 {
		t.Fatal("Expected 1 element in the blacklist but found:", len(sb.merkleRoots))
	}
	_, ok := sb.merkleRoots[skylink.MerkleRoot()]
	if !ok {
		t.Fatalf("Expected merkleroot %v to be listed in blacklist", skylink.MerkleRoot())
	}

	if err = sb.Close(); err != nil {
		t.Fatal(err)
	}

	// Load a new Skynet Blacklist to verify the contents from disk get loaded
	// properly
	sb2, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that the correct number of links were persisted to verify no links
	// are being truncated
	if err := checkNumPersistedLinks(filename, 1); err != nil {
		t.Errorf("error verifying correct number of links: %v", err)
	}

	// There should be 1 element in the blacklist
	if len(sb2.merkleRoots) != 1 {
		t.Fatal("Expected 1 element in the blacklist but found:", len(sb2.merkleRoots))
	}
	_, ok = sb2.merkleRoots[skylink.MerkleRoot()]
	if !ok {
		t.Fatalf("Expected merkleroot %v to be listed in blacklist", skylink.MerkleRoot())
	}

	// Try adding a duplicate skylink.
	err = sb2.UpdateBlacklist(add, []modules.Skylink{})
	if !errors.Contains(err, ErrDuplicateAddition) {
		t.Fatalf("Expected err %v, got %v", ErrDuplicateAddition, err)
	}
	mr := crypto.HashObject("asdf")
	skylink, err = modules.NewSkylinkV1(mr, 4096, 4096)
	if err != nil {
		t.Fatal(err)
	}
	err = sb2.UpdateBlacklist([]modules.Skylink{skylink, skylink}, []modules.Skylink{})
	if !errors.Contains(err, ErrDuplicateAddition) {
		t.Fatalf("Expected err %v, got %v", ErrDuplicateAddition, err)
	}

	// Try removing a skylink that doesn't exist.
	err = sb2.UpdateBlacklist([]modules.Skylink{}, []modules.Skylink{skylink})
	if !errors.Contains(err, ErrInexistentSkylink) {
		t.Fatalf("Expected err %v, got %v", ErrInexistentSkylink, err)
	}

	// Add a new skylink.
	err = sb2.UpdateBlacklist([]modules.Skylink{skylink}, []modules.Skylink{})
	if err != nil {
		t.Fatal(err)
	}

	// There should be 2 elements in the blacklist.
	if len(sb2.merkleRoots) != 2 {
		t.Fatal("Expected 2 elements in the blacklist but found:", len(sb2.merkleRoots))
	}
	_, ok = sb2.merkleRoots[skylink.MerkleRoot()]
	if !ok {
		t.Fatalf("Expected merkleroot %v to be listed in blacklist", skylink.MerkleRoot())
	}

	if err = sb2.Close(); err != nil {
		t.Fatal(err)
	}

	// Load another new Skynet Blacklist to verify the contents from disk get loaded
	// properly
	sb3, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that the correct number of links were persisted to verify no links
	// are being truncated
	if err := checkNumPersistedLinks(filename, 2); err != nil {
		t.Errorf("error verifying correct number of links: %v", err)
	}

	// There should be 2 elements in the blacklist
	if len(sb3.merkleRoots) != 2 {
		t.Fatal("Expected 2 elements in the blacklist but found:", len(sb3.merkleRoots))
	}
	_, ok = sb3.merkleRoots[skylink.MerkleRoot()]
	if !ok {
		t.Fatalf("Expected merkleroot %v to be listed in blacklist", skylink.MerkleRoot())
	}

	if err = sb3.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestPersistCompatibility tests the compatibility of a saved Skynet blacklist.
func TestPersistCompatibility(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Copy the test file to the temp testing dir.
	testdir := testDir(t.Name())
	err := os.MkdirAll(testdir, 0700)
	if err != nil {
		t.Fatal(err)
	}
	testfile := filepath.Join(testdir, persistFile)
	err = build.CopyFile(testPersistFile, testfile)
	if err != nil {
		t.Fatal(err)
	}

	// Load the file from the temp testing dir (we'll be making changes).
	sb, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	// There should be a skylink in the blacklist.
	if len(sb.merkleRoots) != 1 {
		t.Fatalf("Expected 1 skylink in blacklist at but found %v", len(sb.merkleRoots))
	}

	// Add an existing skylink, should be an error.
	mr := crypto.HashObject("asdf")
	skylink, err := modules.NewSkylinkV1(mr, 4096, 4096)
	if err != nil {
		t.Fatal(err)
	}
	err = sb.UpdateBlacklist([]modules.Skylink{skylink}, []modules.Skylink{})
	if !errors.Contains(err, ErrDuplicateAddition) {
		t.Fatalf("Expected err %v, got %v", ErrDuplicateAddition, err)
	}

	// Add a new skylink.
	mr = crypto.HashObject("fdsa")
	skylink2, err := modules.NewSkylinkV1(mr, 4096, 4096)
	if err != nil {
		t.Fatal(err)
	}
	err = sb.UpdateBlacklist([]modules.Skylink{skylink2}, []modules.Skylink{})
	if err != nil {
		t.Fatal(err)
	}

	if err = sb.Close(); err != nil {
		t.Fatal(err)
	}

	// Load a new Skynet Blacklist to verify the contents from disk get loaded
	// properly
	sb2, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that the correct number of links were persisted to verify no links
	// are being truncated
	if err := checkNumPersistedLinks(sb2.FilePath(), 2); err != nil {
		t.Errorf("error verifying correct number of links: %v", err)
	}

	// There should be 2 elements in the blacklist
	if len(sb2.merkleRoots) != 2 {
		t.Fatal("Expected 2 elements in the blacklist but found:", len(sb2.merkleRoots))
	}
	_, ok := sb2.merkleRoots[skylink.MerkleRoot()]
	if !ok {
		t.Fatalf("Expected merkleroot %v to be listed in blacklist", skylink.MerkleRoot())
	}
	_, ok = sb2.merkleRoots[skylink2.MerkleRoot()]
	if !ok {
		t.Fatalf("Expected merkleroot %v to be listed in blacklist", skylink2.MerkleRoot())
	}

	if err = sb2.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestPersistCorruption tests the persistence of the Skynet blacklist when corruption occurs.
func TestPersistCorruption(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a new SkynetBlacklist
	testdir := testDir(t.Name())
	sb, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	filename := filepath.Join(testdir, persistFile)
	if filename != sb.staticAop.FilePath() {
		t.Fatalf("Expected filepath %v, was %v", filename, sb.staticAop.FilePath())
	}

	// There should be no skylinks in the blacklist
	if len(sb.merkleRoots) != 0 {
		t.Fatal("Expected blacklist to be empty but found:", len(sb.merkleRoots))
	}

	// Append a bunch of random data to the end of the blacklist file to test
	// corruption
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, modules.DefaultFilePerm)
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

	// Update blacklist
	var skylink modules.Skylink
	add := []modules.Skylink{skylink}
	err = sb.UpdateBlacklist(add, []modules.Skylink{})
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

	// There should be 1 element in the blacklist now
	if len(sb.merkleRoots) != 1 {
		t.Fatal("Expected 1 element in the blacklist but found:", len(sb.merkleRoots))
	}
	_, ok := sb.merkleRoots[skylink.MerkleRoot()]
	if !ok {
		t.Fatalf("Expected merkleroot %v to be listed in blacklist", skylink.MerkleRoot())
	}

	if err = sb.Close(); err != nil {
		t.Fatal(err)
	}

	// Load a new Skynet Blacklist to verify the contents from disk get loaded
	// properly
	sb2, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	// There should be 1 element in the blacklist
	if len(sb2.merkleRoots) != 1 {
		t.Fatal("Expected 1 element in the blacklist but found:", len(sb2.merkleRoots))
	}
	_, ok = sb2.merkleRoots[skylink.MerkleRoot()]
	if !ok {
		t.Fatalf("Expected merkleroot %v to be listed in blacklist", skylink.MerkleRoot())
	}

	// Add a new skylink.
	mr := crypto.HashObject("asdf")
	skylink, err = modules.NewSkylinkV1(mr, 4096, 4096)
	if err != nil {
		t.Fatal(err)
	}
	err = sb2.UpdateBlacklist([]modules.Skylink{skylink}, []modules.Skylink{})
	if err != nil {
		t.Fatal(err)
	}

	// There should be 2 elements in the blacklist.
	if len(sb2.merkleRoots) != 2 {
		t.Fatal("Expected 2 elements in the blacklist but found:", len(sb2.merkleRoots))
	}
	_, ok = sb2.merkleRoots[skylink.MerkleRoot()]
	if !ok {
		t.Fatalf("Expected merkleroot %v to be listed in blacklist", skylink.MerkleRoot())
	}

	if err = sb2.Close(); err != nil {
		t.Fatal(err)
	}

	// Load another new Skynet Blacklist to verify the contents from disk get loaded
	// properly
	sb3, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	// There should be 2 elements in the blacklist.
	if len(sb3.merkleRoots) != 2 {
		t.Fatal("Expected 2 elements in the blacklist but found:", len(sb3.merkleRoots))
	}
	_, ok = sb3.merkleRoots[skylink.MerkleRoot()]
	if !ok {
		t.Fatalf("Expected merkleroot %v to be listed in blacklist", skylink.MerkleRoot())
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
	// are being truncated
	if err = checkNumPersistedLinks(filename, 2); err != nil {
		t.Errorf("error verifying correct number of links: %v", err)
	}

	if err = sb3.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestMarshalSia probes the marshalSia and unmarshalSia methods
func TestMarshalSia(t *testing.T) {
	// Test MarshalSia
	var skylink modules.Skylink
	var buf bytes.Buffer
	merkleRoot := skylink.MerkleRoot()
	listed := false
	ll := persistEntry{merkleRoot, listed}
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
	if merkleRoot != ll.MerkleRoot {
		t.Fatalf("MerkleRoots don't match, expected %v, got %v", merkleRoot, ll.MerkleRoot)
	}
	if ll.Listed {
		t.Fatal("expected persisted link to not be blacklisted")
	}
	err = encoding.Unmarshal(readBytes[persistSize:2*persistSize], &ll)
	if err != nil {
		t.Fatal(err)
	}
	if merkleRoot != ll.MerkleRoot {
		t.Fatalf("MerkleRoots don't match, expected %v, got %v", merkleRoot, ll.MerkleRoot)
	}
	if !ll.Listed {
		t.Fatal("expected persisted link to be blacklisted")
	}

	// Test unmarshalBlacklist
	blacklist, err := unmarshalObjects(&buf)
	if err != nil {
		t.Fatal(err)
	}

	// Since the merkleroot is the same the blacklist should only have a length
	// of 1 since the non blacklisted merkleroot was added first
	if len(blacklist) != 1 {
		t.Fatalf("Incorrect number of blacklisted merkleRoots, expected %v, got %v", 1, len(blacklist))
	}
	_, ok := blacklist[merkleRoot]
	if !ok {
		t.Fatal("merkleroot not found in blacklist")
	}
}
