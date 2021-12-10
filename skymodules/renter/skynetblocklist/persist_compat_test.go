package skynetblocklist

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"gitlab.com/NebulousLabs/encoding"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/persist"
	"go.sia.tech/siad/types"
)

// TestPersistCompat tests the compat code for the skynet blocklist
// persistence.
func TestPersistCompat(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Starting file, v1.4.3
	t.Run("V143ToV150", testPersistCompatv143Tov150)
	t.Run("V143ToV151", testPersistCompatv143Tov151)
	t.Run("V143ToV1510", testPersistCompatv143Tov1510)
	// Starting file, v1.5.0
	t.Run("V150ToV151", testPersistCompatv150Tov151)
	t.Run("V150ToV1510", testPersistCompatv150Tov1510)
	// Starting file, v1.5.1
	t.Run("V151ToV1510", testPersistCompatv150Tov1510)

	// Regression Test
	t.Run("BadCompatTwoFiles", testPersistCompatTwoFiles)
}

// testPersistCompatTwoFiles tests the handling of the persist code when a
// blocklist persist file was created without converting the blacklist
// persistence
//
// This occurred when there was a v150 blacklist file and a v151 blocklist file.
func testPersistCompatTwoFiles(t *testing.T) {
	t.Parallel()

	// Create Test directory
	testdir := testDir(t.Name())

	// Load a v151 aop to add to the persist file
	aop, _, err := persist.NewAppendOnlyPersist(testdir, persistFile, metadataHeader, metadataVersionV151)
	if err != nil {
		t.Fatal(err)
	}

	// Add links to it
	hash1 := crypto.HashObject("link1")
	hash2 := crypto.HashObject("link2")
	additions := []crypto.Hash{hash1, hash2}

	// NOTE: can't use UpdateBlocklist method because this is a historical
	// compat test. So we manually do the marshalling.
	//
	// Create buffer for encoder
	var buf bytes.Buffer
	// Create and encode the persist links
	for _, hash := range additions {
		// Marshal the update
		pe := persistEntryV151{hash, true}
		data := encoding.Marshal(pe)
		_, err := buf.Write(data)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = aop.Write(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	// Close
	err = aop.Close()
	if err != nil {
		t.Fatal(err)
	}

	// Add Blacklist file and load it
	err = loadCompatPersistFile(testdir, persist.MetadataVersionv150)
	if err != nil {
		t.Fatal(err)
	}
	oldPersistence, err := loadOldPersistenceV151(testdir, blacklistPersistFile, blacklistMetadataHeader, persist.MetadataVersionv150)
	if err != nil {
		t.Fatal(err)
	}

	// Load SkynetBlocklist. This should pick up the blacklist file and and
	// remove it.
	sb, err := New(testdir)
	if err != nil {
		t.Fatal(err)
	}

	// Blacklist persist file should be gone
	_, err = os.Stat(filepath.Join(testdir, blacklistPersistFile))
	if !os.IsNotExist(err) {
		t.Fatal("blacklist file still exists")
	}

	// Verify blocklist was not overwritten
	sb.mu.Lock()
	defer sb.mu.Unlock()
	// Old blacklisted links should be in the blocklist
	for hash := range oldPersistence {
		_, ok := sb.hashes[hash]
		if !ok {
			t.Fatal("old hash not found in new persistence")
		}
	}
	// Newly blocked links should be in the blocklist
	for _, hash := range additions {
		_, ok := sb.hashes[hash]
		if !ok {
			t.Fatal("added hash not found in new persistence")
		}
	}
}

// testPersistCompatv143Tov150 tests converting the skynet blocklist persistence
// from v1.4.3 to v1.5.0
func testPersistCompatv143Tov150(t *testing.T) {
	t.Parallel()
	testdir := testDir(t.Name())
	testPersistCompat(t, testdir, blacklistPersistFile, blacklistPersistFile, blacklistMetadataHeader, blacklistMetadataHeader, metadataVersionV143, persist.MetadataVersionv150)
}

// testPersistCompatv143Tov151 tests converting the skynet blacklist persistence
// from v1.4.3 to v1.5.1
func testPersistCompatv143Tov151(t *testing.T) {
	t.Parallel()
	testdir := testDir(t.Name())
	testPersistCompat(t, testdir, blacklistPersistFile, persistFile, blacklistMetadataHeader, metadataHeader, metadataVersionV143, metadataVersionV151)
}

// testPersistCompatv143Tov1510 tests converting the skynet blacklist persistence
// from v1.4.3 to v1.5.10
func testPersistCompatv143Tov1510(t *testing.T) {
	t.Parallel()
	testdir := testDir(t.Name())
	testPersistCompat(t, testdir, blacklistPersistFile, persistFile, blacklistMetadataHeader, metadataHeader, metadataVersionV143, metadataVersion)
}

// testPersistCompatv150Tov151 tests converting the skynet blacklist persistence
// from v1.5.0 to v1.5.1
func testPersistCompatv150Tov151(t *testing.T) {
	t.Parallel()
	testdir := testDir(t.Name())
	testPersistCompat(t, testdir, blacklistPersistFile, persistFile, blacklistMetadataHeader, metadataHeader, persist.MetadataVersionv150, metadataVersionV151)
}

// testPersistCompatv150Tov1510 tests converting the skynet blacklist persistence
// from v1.5.0 to v1.5.10
func testPersistCompatv150Tov1510(t *testing.T) {
	t.Parallel()
	testdir := testDir(t.Name())
	testPersistCompat(t, testdir, blacklistPersistFile, persistFile, blacklistMetadataHeader, metadataHeader, persist.MetadataVersionv150, metadataVersion)
}

// testPersistCompatv151Tov1510 tests converting the skynet blocklist persistence
// from v1.5.1 to v1.5.10
func testPersistCompatv151Tov1510(t *testing.T) {
	t.Parallel()
	testdir := testDir(t.Name())
	testPersistCompat(t, testdir, persistFile, persistFile, metadataHeader, metadataHeader, metadataVersionV151, metadataVersion)
}

// testPersistCompat tests the persist compat code going between two versions
func testPersistCompat(t *testing.T, testdir, oldPersistFile, newPersistFile string, oldHeader, newHeader, oldVersion, newVersion types.Specifier) {
	t.Run("Clean", func(t *testing.T) {
		testPersistCompatClean(t, testdir, oldPersistFile, newPersistFile, oldHeader, newHeader, oldVersion, newVersion)
	})
	t.Run("TempFile", func(t *testing.T) {
		testPersistCompatTempFile(t, testdir, oldPersistFile, newPersistFile, oldHeader, newHeader, oldVersion, newVersion)
	})
	// This Test is broken because there is a bug in the code. The old test
	// was not properly testing this scenario. Since the temp file doesn't
	// have the header or version number, we can't verify which version a
	// valid temp file belongs too. This means we would automatically go
	// through all compat version which would most likely corrupt the data.
	// t.Run("ValidChecksum", func(t *testing.T) {
	// 	testPersistCompatValidCheckSum(t, testdir, oldPersistFile, newPersistFile, oldHeader, newHeader, oldVersion, newVersion)
	// })
}

// testPersistCompatClean tests the expected execution of the persist compat
// code
func testPersistCompatClean(t *testing.T, testdir, oldPersistFile, newPersistFile string, oldHeader, newHeader, oldVersion, newVersion types.Specifier) {
	// Test 1: Clean conversion

	// Create sub test directory
	subTestDir := filepath.Join(testdir, "CleanConvert")
	err := os.MkdirAll(subTestDir, skymodules.DefaultDirPerm)
	if err != nil {
		t.Fatal(err)
	}

	// Initialize the directory with the old version persist file
	err = loadCompatPersistFile(subTestDir, oldVersion)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the persistence
	err = loadAndVerifyPersistence(subTestDir, oldPersistFile, newPersistFile, oldHeader, newHeader, oldVersion, newVersion)
	if err != nil {
		t.Fatal(err)
	}

	// Test 2: Clean conversion just calling loadPersist. This always test
	// to the latest version

	// Create sub test directory
	subTestDir = filepath.Join(testdir, "CleanConvertB")
	err = os.MkdirAll(subTestDir, skymodules.DefaultDirPerm)
	if err != nil {
		t.Fatal(err)
	}

	// Initialize the directory with the old version persist file
	err = loadCompatPersistFile(subTestDir, oldVersion)
	if err != nil {
		t.Fatal(err)
	}

	// Load old persistence for comparison
	oldPersistence, err := loadOldPersistenceV151(subTestDir, oldPersistFile, oldHeader, oldVersion)
	if err != nil {
		t.Fatal(err)
	}

	// Load the persistence
	aop, reader, err := loadPersist(subTestDir)
	if err != nil {
		t.Fatal(err)
	}

	// Compare the persistence
	// NOTE: this is where the latest version is always referenced
	err = readAndComparePersistence(reader, oldVersion, metadataVersion, oldPersistence)
	if err != nil {
		t.Fatal(err)
	}

	// Close the AOP
	err = aop.Close()
	if err != nil {
		t.Fatal(err)
	}
}

// testPersistCompatTempFile tests the persist compat code for the case when
// there was an unclean shutdown that left an invalid temp file
func testPersistCompatTempFile(t *testing.T, testdir, oldPersistFile, newPersistFile string, oldHeader, newHeader, oldVersion, newVersion types.Specifier) {
	// Test 1: Empty Temp File Exists

	// Create sub test directory
	subTestDir := filepath.Join(testdir, "EmptyTempFile")
	err := os.MkdirAll(subTestDir, skymodules.DefaultDirPerm)
	if err != nil {
		t.Fatal(err)
	}

	// Initialize the directory with the old version persist file
	err = loadCompatPersistFile(subTestDir, oldVersion)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a crash during the creation a temporary file by creating an empty
	// temp file
	f, err := os.Create(filepath.Join(subTestDir, tempPersistFileName(oldPersistFile)))
	if err != nil {
		t.Fatal(err)
	}
	err = f.Close()
	if err != nil {
		t.Fatal(err)
	}

	// Verify the persistence
	err = loadAndVerifyPersistence(subTestDir, oldPersistFile, newPersistFile, oldHeader, newHeader, oldVersion, newVersion)
	if err != nil {
		t.Fatal(err)
	}

	// Test 2: Temp File Exists with an invalid checksum

	// Create sub test directory
	subTestDir = filepath.Join(testdir, "InvalidChecksum")
	err = os.MkdirAll(subTestDir, skymodules.DefaultDirPerm)
	if err != nil {
		t.Fatal(err)
	}

	// Initialize the directory with the old version persist file
	err = loadCompatPersistFile(subTestDir, oldVersion)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a crash during the creation a temporary file by creating a temp
	// file with random bytes
	f, err = os.Create(filepath.Join(subTestDir, tempPersistFileName(oldPersistFile)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(fastrand.Bytes(100))
	if err != nil {
		t.Fatal(err)
	}
	err = f.Close()
	if err != nil {
		t.Fatal(err)
	}

	// Verify the persistence
	err = loadAndVerifyPersistence(subTestDir, oldPersistFile, newPersistFile, oldHeader, newHeader, oldVersion, newVersion)
	if err != nil {
		t.Fatal(err)
	}
}

// testPersistCompatValidCheckSum tests the persist compat code for the case
// when there was an unclean shutdown that left a temp file with a valid
// checksum
func testPersistCompatValidCheckSum(t *testing.T, testdir, oldPersistFile, newPersistFile string, oldHeader, newHeader, oldVersion, newVersion types.Specifier) {
	// Create sub test directory
	subTestDir := filepath.Join(testdir, "ValidChecksum")
	err := os.MkdirAll(subTestDir, skymodules.DefaultDirPerm)
	if err != nil {
		t.Fatal(err)
	}

	// Initialize the directory with the old version persist file
	err = loadCompatPersistFile(subTestDir, oldVersion)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a crash after creating a temporary file
	_, err = createTempFileFromPersistFile(subTestDir, oldPersistFile, oldHeader, oldVersion)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the persistence
	err = loadAndVerifyPersistence(subTestDir, oldPersistFile, newPersistFile, oldHeader, newHeader, oldVersion, newVersion)
	if err != nil {
		t.Fatal(err)
	}
}

// copyFileToTestDir copies the file at fromFilePath and writes it at toFilePath
func copyFileToTestDir(fromFilePath, toFilePath string) error {
	f, err := os.Open(fromFilePath)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Compose(err, f.Close())
	}()
	bytes, err := ioutil.ReadAll(f)
	if err != nil {
		return err
	}
	pf, err := os.Create(toFilePath)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Compose(err, pf.Close())
	}()
	_, err = pf.Write(bytes)
	if err != nil {
		return err
	}
	return nil
}

// loadAndVerifyPersistence loads the persistence and verifies that the
// conversion updated the persistence as expected
func loadAndVerifyPersistence(testDir, oldPersistFile, newPersistFile string, oldHeader, newHeader, oldVersion, newVersion types.Specifier) (err error) {
	// Load Old Persistence
	var oldPersistence map[crypto.Hash]struct{}
	switch oldVersion {
	case metadataVersionV143, persist.MetadataVersionv150, metadataVersionV151:
		oldPersistence, err = loadOldPersistenceV151(testDir, oldPersistFile, oldHeader, oldVersion)
		if err != nil {
			return errors.AddContext(err, "unable to load old persistence")
		}
	default:
		return fmt.Errorf("%v is not a valid old metadata version, method needs to be updated", oldVersion)
	}

	// Convert the persistence.
	switch newVersion {
	case metadataVersion:
		errv143 := convertPersistVersionFromv143Tov150(testDir)
		errv150 := convertPersistVersionFromv150Tov151(testDir)
		errv151 := convertPersistVersionFromv151Tov1510(testDir)
		if errv151 != nil {
			err = errors.Compose(errv143, errv150, errv151)
		}
	case metadataVersionV151:
		errv143 := convertPersistVersionFromv143Tov150(testDir)
		errv150 := convertPersistVersionFromv150Tov151(testDir)
		if errv150 != nil {
			err = errors.Compose(errv143, errv150)
		}
	case persist.MetadataVersionv150:
		errv143 := convertPersistVersionFromv143Tov150(testDir)
		if errv143 != nil {
			err = errv143
		}
	default:
		err = fmt.Errorf("%v is now a valid new metadata version", newVersion)
	}
	if err != nil {
		return errors.AddContext(err, "unable to convert persistence")
	}

	// Load the new persistence
	aop, reader, err := persist.NewAppendOnlyPersist(testDir, newPersistFile, newHeader, newVersion)
	if err != nil {
		return errors.AddContext(err, "unable to open new persistence")
	}
	defer func() {
		err = errors.Compose(err, aop.Close())
	}()

	return readAndComparePersistence(reader, oldVersion, newVersion, oldPersistence)
}

// loadCompatPersistFile loads the persist file for the supplied version into
// the testDir
func loadCompatPersistFile(testDir string, version types.Specifier) error {
	switch version {
	case metadataVersionV143:
		return loadV143CompatPersistFile(testDir)
	case persist.MetadataVersionv150:
		return loadV150CompatPersistFile(testDir)
	case metadataVersionV151:
		return loadV151CompatPersistFile(testDir)
	default:
	}
	return errors.New("invalid error")
}

// loadOldPersistenceV151 loads the persistence from the old persist file up through compat version v1.5.1
func loadOldPersistenceV151(testDir, oldPersistFile string, oldHeader, oldVersion types.Specifier) (_ map[crypto.Hash]struct{}, err error) {
	// Verify that loading the older persist file works
	aop, reader, err := persist.NewAppendOnlyPersist(testDir, oldPersistFile, oldHeader, oldVersion)
	if err != nil {
		return nil, errors.AddContext(err, "unable to open old persist file")
	}
	defer func() {
		err = errors.Compose(err, aop.Close())
	}()

	// Grab the old persistence
	oldPersistence, err := unmarshalObjectsV151(reader)
	if err != nil {
		return nil, errors.AddContext(err, "unable to unmarshal old persistence")
	}
	if len(oldPersistence) == 0 {
		return nil, errors.New("no data in old version's persist file")
	}
	return oldPersistence, nil
}

// loadV143CompatPersistFile loads the v1.4.3 persist file into the testDir
func loadV143CompatPersistFile(testDir string) error {
	v143FileName := filepath.Join("..", "..", "..", "compatibility", blacklistPersistFile+"_v143")
	return copyFileToTestDir(v143FileName, filepath.Join(testDir, blacklistPersistFile))
}

// loadV150CompatPersistFile loads the v1.5.0 persist file into the testDir
func loadV150CompatPersistFile(testDir string) error {
	v150FileName := filepath.Join("..", "..", "..", "compatibility", blacklistPersistFile+"_v150")
	return copyFileToTestDir(v150FileName, filepath.Join(testDir, blacklistPersistFile))
}

// loadV151CompatPersistFile loads the v1.5.1 persist file into the testDir
func loadV151CompatPersistFile(testDir string) error {
	v151FileName := filepath.Join("..", "..", "..", "compatibility", persistFile+"_v151")
	return copyFileToTestDir(v151FileName, filepath.Join(testDir, persistFile))
}

// readAndComparePersistence reads the persistence from the reader and compares
// it to the provided oldPersistence
func readAndComparePersistence(reader io.Reader, oldVersion, newVersion types.Specifier, oldPersistence map[crypto.Hash]struct{}) (err error) {
	// Grab the new persistence
	var newPersistence map[crypto.Hash]int64
	var newPersistenceV151 map[crypto.Hash]struct{}
	var newPersistLength int
	switch newVersion {
	case persist.MetadataVersionv150, metadataVersionV151:
		newPersistenceV151, err = unmarshalObjectsV151(reader)
		if err != nil {
			return errors.AddContext(err, "unable to unmarshal new persistence v151 and below")
		}
		newPersistLength = len(newPersistenceV151)
	case metadataVersion:
		newPersistence, err = unmarshalObjects(reader)
		if err != nil {
			return errors.AddContext(err, "unable to unmarshal new persistence")
		}
		newPersistLength = len(newPersistence)
	default:
		return fmt.Errorf("%v is not a valid newVersion", newVersion)
	}
	if newPersistLength == 0 {
		return errors.New("no data in new version's persist file")
	}

	// Verify that the original persistence was properly updated
	if len(oldPersistence) != newPersistLength {
		return fmt.Errorf("Expected %v hashes but got %v", newPersistLength, len(oldPersistence))
	}
	for p := range oldPersistence {
		var hash crypto.Hash
		switch oldVersion {
		case metadataVersionV143:
			hash = crypto.HashObject(p)
		case persist.MetadataVersionv150, metadataVersionV151:
			hash = p
		default:
			return errors.New("invalid version")
		}
		switch newVersion {
		case persist.MetadataVersionv150, metadataVersionV151:
			_, ok := newPersistenceV151[hash]
			if !ok {
				return fmt.Errorf("Original persistence: %v \nLoaded persistence: %v \n Persist hash not found in list of hashes", oldPersistence, newPersistenceV151)
			}
		case metadataVersion:
			ppe, ok := newPersistence[hash]
			if !ok {
				return fmt.Errorf("Original persistence: %v \nLoaded persistence: %v \n Persist hash not found in list of hashes", oldPersistence, newPersistence)
			}
			if ppe == 0 {
				return errors.New("uninitialized probationaryPeriodEnd")
			}
		default:
			return fmt.Errorf("%v is not a valid new version", newVersion)
		}
	}
	return nil
}
