package renter

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
	"reflect"
	"testing"

	"gitlab.com/NebulousLabs/Sia/modules/renter/filesystem/siafile"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/fastrand"

	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/writeaheadlog"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/renter/filesystem"
)

func TestSiaDirCopy(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", t.Name())
	_, wal, _ := writeaheadlog.New(tmpDir + string(filepath.Separator) + "wal.wal")
	logger := persist.Logger{}
	fs, _ := filesystem.New(tmpDir, &logger, wal)
	ec, err := modules.NewRSSubCode(10, 20, crypto.SegmentSize)
	if err != nil {
		t.Fatal(err)
	}

	// Create some test data in the user's home folder.
	// A file in the top dir.
	file1, err := modules.UserFolder.Join("file1")
	err = fs.NewSiaFile(file1, "", ec, crypto.GenerateSiaKey(crypto.TypeDefaultRenter), uint64(10+fastrand.Intn(100)), persist.DefaultDiskPermissionsTest, false)
	if err != nil {
		t.Fatal(err)
	}
	// A dir that will have a file.
	dir1, err := modules.UserFolder.Join("dir")
	err = fs.NewSiaDir(dir1, modules.DefaultDirPerm)
	if err != nil {
		t.Fatal(err)
	}
	// A file in the dir.
	file2, err := dir1.Join("file2")
	err = fs.NewSiaFile(file2, "", ec, crypto.GenerateSiaKey(crypto.TypeDefaultRenter), uint64(10+fastrand.Intn(100)), persist.DefaultDiskPermissionsTest, false)
	if err != nil {
		t.Fatal(err)
	}
	// A dir that will stay empty.
	dir2, err := modules.UserFolder.Join("dir2")
	err = fs.NewSiaDir(dir2, modules.DefaultDirPerm)
	if err != nil {
		t.Fatal(err)
	}
	// No need to create a non-sia file for testing - the wal file will fill
	// that role.

	// Create a new directory for the backup.
	backupDir, _ := modules.BackupFolder.Join("testBackup")
	if err := fs.NewSiaDir(backupDir, modules.DefaultDirPerm); err != nil {
		t.Fatal(err)
	}
	// Create a backup copy.
	err = siaDirCopy(fs, modules.UserFolder, backupDir)
	if err != nil {
		t.Fatal(err)
	}

	// Check the files.
	if _, err = equalToBackup(file1, fs, backupDir); err != nil {
		t.Fatal(err)
	}
	if _, err = equalToBackup(file2, fs, backupDir); err != nil {
		t.Fatal(err)
	}
}

// equalToBackup compares the content of a siafile to its backup.
func equalToBackup(file modules.SiaPath, fs *filesystem.FileSystem, backupDir modules.SiaPath) (bool, error) {
	// Compare file with its backup.
	fileNode, err := fs.OpenSiaFile(file)
	if err != nil {
		return false, err
	}
	defer fileNode.Close()
	snap, err := fileNode.Snapshot(file)
	if err != nil {
		return false, err
	}
	backup, err := file.Rebase(modules.UserFolder, backupDir)
	if err != nil {
		return false, err
	}
	backupNode, err := fs.OpenSiaFile(backup)
	if err != nil {
		return false, err
	}
	defer backupNode.Close()
	backupSnap, err := fileNode.Snapshot(backup)
	if err != nil {
		return false, err
	}

	if !snapshotsEqual(snap, backupSnap) {
		return false, fmt.Errorf("snapshots not equal:\n%+v\n%+v\n", snap, backupSnap)
	}
	return true, nil
}

// snapshotsEqual compares all fields of two snapshots except their local paths.
func snapshotsEqual(a, b *siafile.Snapshot) bool {
	return a.Mode() == b.Mode() &&
		a.NumChunks() == b.NumChunks() &&
		a.UID() == b.UID() &&
		a.Size() == b.Size() &&
		a.ChunkSize() == b.ChunkSize() &&
		reflect.DeepEqual(a.PartialChunks(), b.PartialChunks()) &&
		reflect.DeepEqual(a.ErasureCode(), b.ErasureCode()) &&
		reflect.DeepEqual(a.MasterKey(), b.MasterKey())
}
