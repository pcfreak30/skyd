package renter

import (
	"io/ioutil"
	"path/filepath"
	"testing"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules/renter/filesystem/siafile"
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
	ec, err := siafile.NewRSSubCode(10, 20, crypto.SegmentSize)
	if err != nil {
		t.Fatal(err)
	}

	// Create some test data in the user's home folder.
	// A file in the top dir.
	sf1, err := modules.UserFolder.Join("file1")
	err = fs.NewSiaFile(sf1, "", ec, crypto.GenerateSiaKey(crypto.TypeDefaultRenter), uint64(10+fastrand.Intn(100)), persist.DefaultDiskPermissionsTest, false)
	if err != nil {
		t.Fatal(err)
	}
	// A dir that will have a file.
	sd1, err := modules.UserFolder.Join("dir")
	err = fs.NewSiaDir(sd1, modules.DefaultDirPerm)
	if err != nil {
		t.Fatal(err)
	}
	// A file in the dir.
	sf2, err := sd1.Join("file2")
	err = fs.NewSiaFile(sf2, "", ec, crypto.GenerateSiaKey(crypto.TypeDefaultRenter), uint64(10+fastrand.Intn(100)), persist.DefaultDiskPermissionsTest, false)
	if err != nil {
		t.Fatal(err)
	}
	// A dir that will stay empty.
	sd2, err := modules.UserFolder.Join("dir2")
	err = fs.NewSiaDir(sd2, modules.DefaultDirPerm)
	if err != nil {
		t.Fatal(err)
	}
	// No need to create a non-sia file for testing - the wal file will fill
	// that role.

	// TODO How do I force the dir's metadata to be calculated? Do I need to bubble something?

	// Create a new directory for the backup.
	backupDir, _ := modules.BackupFolder.Join("testBackup")
	if err := fs.NewSiaDir(backupDir, modules.DefaultDirPerm); err != nil {
		t.Fatal(err)
	}
	err = siaDirCopy(fs, modules.UserFolder, backupDir)
	if err != nil {
		t.Fatal(err)
	}

	// Compare file1.
	fn1, err := fs.OpenSiaFile(sf1)
	if err != nil {
		t.Fatal(err)
	}
	defer fn1.Close()
	snap, err := fn1.Snapshot(sf1)
	if err != nil {
		t.Fatal(err)
	}
	bsf1, err := sf1.Rebase(modules.UserFolder, backupDir)
	if err != nil {
		t.Fatal(err)
	}
	bfn1, err := fs.OpenSiaFile(bsf1)
	if err != nil {
		t.Fatal(err)
	}
	defer bfn1.Close()
	backupSnap, err := fn1.Snapshot(bsf1)
	if err != nil {
		t.Fatal(err)
	}

	if snap.Size() != backupSnap.Size() {
		t.Fatal()
	}
}
