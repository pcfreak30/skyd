package skymodules

import (
	"bytes"
	"io"
	"io/ioutil"
	"strings"
	"testing"

	"gitlab.com/NebulousLabs/fastrand"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
)

const (
	testSkylink = "AABEKWZ_wc2R9qlhYkzbG8mImFVi08kBu1nsvvwPLBtpEg"
)

// TestBackupAndRestoreSkylink probes the backup and restore skylink methods
func TestBackupAndRestoreSkylink(t *testing.T) {
	t.Parallel()

	// Create common layout and metadata bytes
	sl := newTestSkyfileLayout()
	layoutBytes := sl.Encode()
	var sm SkyfileMetadata
	smBytes, err := SkyfileMetadataBytes(sm)
	if err != nil {
		t.Fatal(err)
	}

	// Small file test
	//
	// Create baseSector
	fileData := []byte("Super interesting skyfile data")
	baseSector, _, _ := BuildBaseSector(layoutBytes, nil, smBytes, fileData)
	// Backup and Restore test with no reader supplied
	testBackupAndRestore(t, baseSector, fileData, nil)
	// Backup and Restore test with reader supplied
	backupReader := bytes.NewReader(fileData)
	testBackupAndRestore(t, baseSector, fileData, backupReader)

	// Large file test
	//
	// Create fanout to mock 500 chunks with 3 pieces each. That way the
	// file will have an extended base sector.
	numChunks := 500
	numPieces := 3
	fanoutBytes := make([]byte, 0, numChunks*numPieces*crypto.HashSize)
	for ci := 0; ci < numChunks; ci++ {
		for pi := 0; pi < numPieces; pi++ {
			root := fastrand.Bytes(crypto.HashSize)
			fanoutBytes = append(fanoutBytes, root...)
		}
	}
	// Create baseSector
	baseSector, _, _ = BuildBaseSector(layoutBytes, fanoutBytes, smBytes, nil)
	// Backup and Restore test
	size := 2 * int(modules.SectorSize)
	fileData = fastrand.Bytes(size)
	backupReader = bytes.NewReader(fileData)
	testBackupAndRestore(t, baseSector, fileData, backupReader)
}

// testBackupAndRestore executes the test code for TestBackupAndRestoreSkylink
func testBackupAndRestore(t *testing.T, baseSector []byte, fileData []byte, backupReader io.Reader) {
	// Create backup
	var buf bytes.Buffer
	err := BackupSkylink(testSkylink, baseSector, backupReader, &buf)
	if err != nil {
		t.Fatal(err)
	}

	// Restore
	restoreReader := bytes.NewBuffer(buf.Bytes())
	skylinkStr, restoreBaseSector, err := RestoreSkylink(restoreReader)
	if err != nil {
		t.Fatal(err)
	}
	if skylinkStr != testSkylink {
		t.Error("Skylink back", skylinkStr)
	}
	if !bytes.Equal(baseSector, restoreBaseSector) {
		t.Log("original baseSector:", baseSector)
		t.Log("restored baseSector:", restoreBaseSector)
		t.Fatal("BaseSector bytes not equal")
	}
	if backupReader == nil {
		// If there was no backupReader then this was a small file and only the
		// basesector was needed so there is no additional file data to compare
		return
	}
	restoredData, err := ioutil.ReadAll(restoreReader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fileData, restoredData) {
		t.Log("original data:", fileData)
		t.Log("backup restoredData:", restoredData)
		t.Fatal("Data bytes not equal")
	}
}

// TestRandomSysPath tests the RandomSysPath functions
func TestRandomSysPath(t *testing.T) {
	t.Parallel()

	// Create a random syspath
	path := RandomSyspath()
	// Split into elements
	elements := strings.Split(path, "/")
	// There should be defaultDirDepth dirs and a filename
	if len(elements) != defaultDirDepth+1 {
		t.Fatal("bad", elements)
	}
	// Each of the dirs should be defaultDirLength
	for i := 0; i < defaultDirDepth; i++ {
		if len(elements[i]) != defaultDirLength {
			t.Fatal("bad", elements[i])
		}
	}
}
