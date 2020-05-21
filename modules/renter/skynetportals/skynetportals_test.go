package skynetportals

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/NebulousLabs/Sia/build"
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
	return build.TempDir("skynetportals", name)
}

// checkNumPersistedPortals checks that the expected number of portals has been
// persisted on disk by checking the size of the persistence file.
func checkNumPersistedPortals(portalsPath string, numPortals int) error {
	expectedSize := numPortals*int(persistSize) + int(persist.MetadataPageSize)
	if fi, err := os.Stat(portalsPath); err != nil {
		return errors.AddContext(err, "failed to get portal list filesize")
	} else if fi.Size() != int64(expectedSize) {
		return fmt.Errorf("expected %v portals to have a filesize of %v but was %v", numPortals, expectedSize, fi.Size())
	}
	return nil
}

// TestPersist tests the persistence of the Skynet portals list.
func TestPersist(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a new SkynetPortals
	testdir := testDir(t.Name())
	sp, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	filename := filepath.Join(testdir, persistFile)
	if filename != sp.staticAop.FilePath() {
		t.Fatalf("Expected filepath %v, was %v", filename, sp.staticAop.FilePath())
	}

	// There should be no portals in the list
	if len(sp.portals) != 0 {
		t.Fatal("Expected portals list to be empty but found:", len(sp.portals))
	}

	// Try adding and removing the same address.
	portal := modules.SkynetPortal{
		Address: "localhost:9980",
		Public:  true,
	}
	add := []modules.SkynetPortal{portal}
	upperAddress := modules.NetAddress(strings.ToUpper(string(portal.Address)))
	remove := []modules.NetAddress{upperAddress}
	err = sp.UpdatePortals(add, remove)
	if !errors.Contains(err, ErrInexistentPortal) {
		t.Fatalf("Expected err %v, got %v", ErrInexistentPortal, err)
	}

	// Portals list should remain empty.
	if len(sp.portals) != 0 {
		t.Fatal("Expected portals list to be empty but found:", len(sp.portals))
	}

	// Add the portal.
	err = sp.UpdatePortals(add, []modules.NetAddress{})
	if err != nil {
		t.Fatal(err)
	}

	// There should be 1 element in the portals list now
	if len(sp.portals) != 1 {
		t.Fatal("Expected 1 element in the portals list but found:", len(sp.portals))
	}
	public, ok := sp.portals[portal.Address]
	if public != portal.Public {
		t.Fatalf("Expected publicness of portal listed in portals list to be %v but was %v", portal.Public, public)
	}
	if !ok {
		t.Fatalf("Expected address %v to be listed in portals list", portal.Address)
	}

	if err = sp.Close(); err != nil {
		t.Fatal(err)
	}

	// Load a new Skynet Portals List to verify the contents from disk get loaded
	// properly
	sp2, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that the correct number of portals were persisted to verify no
	// portals are being truncated
	if err := checkNumPersistedPortals(filename, 1); err != nil {
		t.Fatalf("error verifying correct number of portals: %v", err)
	}

	// There should be 1 element in the portals list
	if len(sp2.portals) != 1 {
		t.Fatal("Expected 1 element in the portals list but found:", len(sp2.portals))
	}
	public, ok = sp2.portals[portal.Address]
	if public != portal.Public {
		t.Fatalf("Expected publicness of portal listed in portals list to be %v but was %v", portal.Public, public)
	}
	if !ok {
		t.Fatalf("Expected address %v to be listed in portals list", portal.Address)
	}

	// Try adding a duplicate portal.
	err = sp2.UpdatePortals(add, []modules.NetAddress{})
	if !errors.Contains(err, ErrDuplicateAddition) {
		t.Fatalf("Expected err %v, got %v", ErrDuplicateAddition, err)
	}
	portal = modules.SkynetPortal{
		Address: "LOCALHOST:990",
		Public:  false,
	}
	err = sp2.UpdatePortals([]modules.SkynetPortal{portal, portal}, []modules.NetAddress{})
	if !errors.Contains(err, ErrDuplicateAddition) {
		t.Fatalf("Expected err %v, got %v", ErrDuplicateAddition, err)
	}

	// Try removing a portal that doesn't exist.
	err = sp2.UpdatePortals([]modules.SkynetPortal{}, []modules.NetAddress{portal.Address})
	if !errors.Contains(err, ErrInexistentPortal) {
		t.Fatalf("Expected err %v, got %v", ErrInexistentPortal, err)
	}

	// Add a new portal.
	err = sp2.UpdatePortals([]modules.SkynetPortal{portal}, []modules.NetAddress{})
	if err != nil {
		t.Fatal(err)
	}

	// There should be 2 elements in the portal list.
	if len(sp2.portals) != 2 {
		t.Fatal("Expected 2 elements in the portal list but found:", len(sp2.portals))
	}
	address := modules.NetAddress(strings.ToLower(string(portal.Address)))
	public, ok = sp2.portals[address]
	if public != portal.Public {
		t.Fatalf("Expected publicness of portal listed in portals list to be %v but was %v", portal.Public, public)
	}
	if !ok {
		t.Fatalf("Expected address %v to be listed in portals list", address)
	}

	// Try adding the same portal with different case.
	portal2 := modules.SkynetPortal{
		Address: "Localhost:990",
		Public:  false,
	}
	err = sp2.UpdatePortals([]modules.SkynetPortal{portal2, portal2}, []modules.NetAddress{})
	if !errors.Contains(err, ErrDuplicateAddition) {
		t.Fatalf("Expected err %v, got %v", ErrDuplicateAddition, err)
	}

	if err = sp2.Close(); err != nil {
		t.Fatal(err)
	}

	// Load another new Skynet Portals List to verify the contents from disk get
	// loaded properly
	sp3, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that the correct number of portals were persisted to verify no
	// portals are being truncated
	if err := checkNumPersistedPortals(filename, 2); err != nil {
		t.Fatalf("error verifying correct number of portals: %v", err)
	}

	// There should be 2 elements in the portals list
	if len(sp3.portals) != 2 {
		t.Fatal("Expected 2 elements in the portals list but found:", len(sp3.portals))
	}
	public, ok = sp3.portals[address]
	if !ok {
		t.Fatalf("Expected address %v to be listed in portals list", address)
	}

	if err = sp3.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestPersistCompatibility tests the compatibility of a saved Skynet portal
// list.
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
	sp, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	// There should be a portal in the list.
	if len(sp.portals) != 1 {
		t.Fatalf("Expected 1 portal in list at but found %v", len(sp.portals))
	}

	// Add an existing portal, should be an error.
	portal := modules.SkynetPortal{
		Address: "siasky.net:443",
		Public:  true,
	}
	err = sp.UpdatePortals([]modules.SkynetPortal{portal}, []modules.NetAddress{})
	if !errors.Contains(err, ErrDuplicateAddition) {
		t.Fatalf("Expected err %v, got %v", ErrDuplicateAddition, err)
	}

	// Add a new portal.
	portal2 := modules.SkynetPortal{
		Address: "localhost:990",
		Public:  false,
	}
	err = sp.UpdatePortals([]modules.SkynetPortal{portal2}, []modules.NetAddress{})
	if err != nil {
		t.Fatal(err)
	}

	if err = sp.Close(); err != nil {
		t.Fatal(err)
	}

	// Load a new Skynet portal list to verify the contents from disk get loaded
	// properly.
	sp2, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that the correct number of portals were persisted to verify no
	// portals are being truncated.
	if err := checkNumPersistedPortals(sp2.FilePath(), 2); err != nil {
		t.Errorf("error verifying correct number of portals: %v", err)
	}

	// There should be 2 elements in the blacklist
	if len(sp2.portals) != 2 {
		t.Fatal("Expected 2 elements in the portal list but found:", len(sp2.portals))
	}
	_, ok := sp2.portals[portal.Address]
	if !ok {
		t.Fatalf("Expected address %v to be listed", portal.Address)
	}
	_, ok = sp2.portals[portal2.Address]
	if !ok {
		t.Fatalf("Expected address %v to be listed", portal2.Address)
	}

	if err = sp2.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestPersistCorruption tests the persistence of the Skynet portal list when
// corruption occurs.
func TestPersistCorruption(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a new SkynetPortalList
	testdir := testDir(t.Name())
	sp, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	filename := filepath.Join(testdir, persistFile)
	if filename != sp.staticAop.FilePath() {
		t.Fatalf("Expected filepath %v, was %v", filename, sp.staticAop.FilePath())
	}

	// There should be no portals in the list
	if len(sp.portals) != 0 {
		t.Fatal("Expected portals list to be empty but found:", len(sp.portals))
	}

	// Append a bunch of random data to the end of the portals list file to test
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
	if uint64(filesize) <= sp.staticAop.PersistLength() {
		t.Fatalf("Expected file size greater than %v, got %v", sp.staticAop.PersistLength(), filesize)
	}

	// Update portals list
	portal := modules.SkynetPortal{
		Address: "localhost:9980",
		Public:  true,
	}
	add := []modules.SkynetPortal{portal}
	err = sp.UpdatePortals(add, []modules.NetAddress{})
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
	if uint64(filesize) != sp.staticAop.PersistLength() {
		t.Fatalf("Expected file size %v, got %v", sp.staticAop.PersistLength(), filesize)
	}

	// There should be 1 element in the portals list now
	if len(sp.portals) != 1 {
		t.Fatal("Expected 1 element in the portals list but found:", len(sp.portals))
	}
	public, ok := sp.portals[portal.Address]
	if public != portal.Public {
		t.Fatalf("Expected publicity of portal listed in portals list to be %v but was %v", portal.Public, public)
	}
	if !ok {
		t.Fatalf("Expected address %v to be listed in portals list", portal.Address)
	}

	if err = sp.Close(); err != nil {
		t.Fatal(err)
	}

	// Load a new Skynet Portals List to verify the contents from disk get loaded
	// properly
	sp2, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	// There should be 1 element in the portals list
	if len(sp2.portals) != 1 {
		t.Fatal("Expected 1 element in the portals list but found:", len(sp2.portals))
	}
	public, ok = sp2.portals[portal.Address]
	if public != portal.Public {
		t.Fatalf("Expected publicness of portal listed in portals list to be %v but was %v", portal.Public, public)
	}
	if !ok {
		t.Fatalf("Expected address %v to be listed in portals list", portal.Address)
	}

	// Add a new portal.
	portal = modules.SkynetPortal{
		Address: "test.com:990",
		Public:  false,
	}
	err = sp2.UpdatePortals([]modules.SkynetPortal{portal}, []modules.NetAddress{})
	if err != nil {
		t.Fatal(err)
	}

	// There should be 2 elements in the portal list
	if len(sp2.portals) != 2 {
		t.Fatal("Expected 2 elements in the portal list but found:", len(sp2.portals))
	}
	public, ok = sp2.portals[portal.Address]
	if public != portal.Public {
		t.Fatalf("Expected publicity of portal listed in portals list to be %v but was %v", portal.Public, public)
	}
	if !ok {
		t.Fatalf("Expected address %v to be listed in portals list", portal.Address)
	}

	if err = sp2.Close(); err != nil {
		t.Fatal(err)
	}

	// Load another new Skynet Portals List to verify the contents from disk get
	// loaded properly
	sp3, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	// There should be 2 elements in the portals list.
	if len(sp3.portals) != 2 {
		t.Fatal("Expected 2 elements in the portals list but found:", len(sp3.portals))
	}
	public, ok = sp3.portals[portal.Address]
	if !ok {
		t.Fatalf("Expected address %v to be listed in portals list", portal.Address)
	}

	// The final filesize should be equal to the persist length.
	fi, err = os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	filesize = fi.Size()
	if uint64(filesize) != sp3.staticAop.PersistLength() {
		t.Fatalf("Expected file size %v, got %v", sp3.staticAop.PersistLength(), filesize)
	}

	// Verify that the correct number of portals were persisted to verify no
	// portals are being truncated
	if err := checkNumPersistedPortals(filename, 2); err != nil {
		t.Fatalf("error verifying correct number of portals: %v", err)
	}

	if err = sp3.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestMarshalSia probes the marshalSia and unmarshalSia methods
func TestMarshalSia(t *testing.T) {
	// Test MarshalSia
	portal := modules.SkynetPortal{
		Address: modules.NetAddress("localhost:9980"),
		Public:  true,
	}
	var buf bytes.Buffer
	address := portal.Address
	listed := false
	public := portal.Public
	pe := persistEntry{address, public, listed}
	err := pe.MarshalSia(&buf)
	if err != nil {
		t.Fatal(err)
	}
	pe.listed = true
	err = pe.MarshalSia(&buf)
	if err != nil {
		t.Fatal(err)
	}

	// Test UnmarshalSia, portals should unmarshal in the order they were
	// marshalled.
	r := bytes.NewBuffer(buf.Bytes())
	err = pe.UnmarshalSia(r)
	if err != nil {
		t.Fatal(err)
	}
	if address != pe.address {
		t.Fatalf("Addresses don't match, expected %v, got %v", address, pe.address)
	}
	if public != pe.public {
		t.Fatalf("Publicness doesn't match, expected %v, got %v", public, pe.public)
	}
	if pe.listed {
		t.Fatal("expected persisted portal to not be listed")
	}
	err = pe.UnmarshalSia(r)
	if err != nil {
		t.Fatal(err)
	}
	if public != pe.public {
		t.Fatalf("Publicness doesn't match, expected %v, got %v", public, pe.public)
	}
	if address != pe.address {
		t.Fatalf("Addresses don't match, expected %v, got %v", address, pe.address)
	}
	if !pe.listed {
		t.Fatal("expected persisted portal to be listed")
	}

	// Test unmarshalPersistPortals
	portals, err := unmarshalObjects(&buf)
	if err != nil {
		t.Fatal(err)
	}

	// Since the address is the same the portals list should only have a length
	// of 1 since the non listed address was added first.
	if len(portals) != 1 {
		t.Fatalf("Incorrect number of listed addresses, expected %v, got %v", 1, len(portals))
	}
	_, ok := portals[address]
	if !ok {
		t.Fatal("address not found in portals list")
	}
}
