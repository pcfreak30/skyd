package renter

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/NebulousLabs/ratelimit"
	"gitlab.com/SkynetLabs/skyd/siatest/dependencies"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem/siafile"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
)

// testingFileParams generates the ErasureCoder with random dataPieces and
// parityPieces and a random name for a testing file
func testingFileParams() (skymodules.SiaPath, skymodules.ErasureCoder) {
	nData := fastrand.Intn(10) + 1
	nParity := fastrand.Intn(10) + 1
	return testingFileParamsCustom(nData, nParity)
}

// testingFileParamsCustom generates the ErasureCoder from the provided
// dataPieces and parityPices and a random name for a testing file
func testingFileParamsCustom(dataPieces, parityPieces int) (skymodules.SiaPath, skymodules.ErasureCoder) {
	rsc, _ := skymodules.NewRSCode(dataPieces, parityPieces)
	return skymodules.RandomSiaPath(), rsc
}

// equalFiles is a helper function that compares two files for equality.
func equalFiles(f1, f2 *siafile.SiaFile) error {
	if f1 == nil || f2 == nil {
		return fmt.Errorf("one or both files are nil")
	}
	if f1.UID() != f2.UID() {
		return fmt.Errorf("uids do not match: %v %v", f1.UID(), f2.UID())
	}
	if f1.Size() != f2.Size() {
		return fmt.Errorf("sizes do not match: %v %v", f1.Size(), f2.Size())
	}
	mk1 := f1.MasterKey()
	mk2 := f2.MasterKey()
	if !bytes.Equal(mk1.Key(), mk2.Key()) {
		return fmt.Errorf("keys do not match: %v %v", mk1.Key(), mk2.Key())
	}
	if f1.PieceSize() != f2.PieceSize() {
		return fmt.Errorf("pieceSizes do not match: %v %v", f1.PieceSize(), f2.PieceSize())
	}
	return nil
}

// TestRenterSaveLoad probes the save and load methods of the renter type.
func TestRenterSaveLoad(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	rt, err := newRenterTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Check that the default values got set correctly.
	settings, err := rt.renter.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.MaxDownloadSpeed != DefaultMaxDownloadSpeed {
		t.Error("default max download speed not set at init")
	}
	if settings.MaxUploadSpeed != DefaultMaxUploadSpeed {
		t.Error("default max upload speed not set at init")
	}

	// The stats should be seeded.
	trackers := []struct {
		t    *skymodules.DistributionTracker
		seed time.Duration
	}{
		{
			t:    rt.renter.staticRegistryReadStats,
			seed: readRegistryStatsSeed,
		},
		{
			t:    rt.renter.staticRegWriteStats,
			seed: 5 * time.Second,
		},
		{
			t:    rt.renter.staticBaseSectorUploadStats,
			seed: 15 * time.Second,
		},
		{
			t:    rt.renter.staticChunkUploadStats,
			seed: 15 * time.Second,
		},
		{
			t:    rt.renter.staticStreamBufferStats,
			seed: 5 * time.Second,
		},
	}
	for _, tracker := range trackers {
		allNines := tracker.t.Percentiles()
		for i, distribution := range allNines {
			for j, nine := range distribution {
				if nine < (tracker.seed*95/100) || nine > (tracker.seed*105/11) {
					t.Fatalf("registry stats aren't seeded correctly %v != %v -- %v %v", nine, tracker.seed, i, j)
				}
			}
		}
		// Add lots of high datapoints to move the percentiles up.
		for i := 0; i < 1000; i++ {
			tracker.t.AddDataPoint(time.Hour)
		}
	}

	// Update the settings of the renter to have a new stream cache size and
	// download speed.
	newDownSpeed := int64(300e3)
	newUpSpeed := int64(500e3)
	settings.MaxDownloadSpeed = newDownSpeed
	settings.MaxUploadSpeed = newUpSpeed
	err = rt.renter.SetSettings(settings)
	if err != nil {
		t.Fatal(err)
	}

	// Add a file to the renter
	entry, err := rt.renter.newRenterTestFile()
	if err != nil {
		t.Fatal(err)
	}
	siapath := rt.renter.staticFileSystem.FileSiaPath(entry)
	if err := entry.Close(); err != nil {
		t.Fatal(err)
	}

	// Check that SiaFileSet knows of the SiaFile
	entry, err = rt.renter.staticFileSystem.OpenSiaFile(siapath)
	if err != nil {
		t.Fatal("SiaFile not found in the renter's staticFileSet after creation")
	}
	if err := entry.Close(); err != nil {
		t.Fatal(err)
	}

	err = rt.renter.saveSync() // save metadata
	if err != nil {
		t.Fatal(err)
	}
	// Wait for stats to sync.
	time.Sleep(2 * statsPersistInterval)
	err = rt.renter.Close()
	if err != nil {
		t.Fatal(err)
	}

	// load should now load the files into memory.
	var errChan <-chan error
	rl := ratelimit.NewRateLimit(0, 0, 0)
	tus := NewSkynetTUSInMemoryUploadStore()
	rt.renter, errChan = New(rt.gateway, rt.cs, rt.wallet, rt.tpool, rt.mux, tus, rl, filepath.Join(rt.dir, skymodules.RenterDir))
	if err := <-errChan; err != nil {
		t.Fatal(err)
	}

	newSettings, err := rt.renter.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if newSettings.MaxDownloadSpeed != newDownSpeed {
		t.Error("download settings not being persisted correctly")
	}
	if newSettings.MaxUploadSpeed != newUpSpeed {
		t.Error("upload settings not being persisted correctly")
	}

	// Check that SiaFileSet loaded the renter's file
	_, err = rt.renter.staticFileSystem.OpenSiaFile(siapath)
	if err != nil {
		t.Fatal("SiaFile not found in the renter's staticFileSet after load")
	}

	// The stats should be higher than the seed now.
	trackers[0].t = rt.renter.staticRegistryReadStats
	trackers[1].t = rt.renter.staticRegWriteStats
	trackers[2].t = rt.renter.staticBaseSectorUploadStats
	trackers[3].t = rt.renter.staticChunkUploadStats
	trackers[4].t = rt.renter.staticStreamBufferStats
	for _, tracker := range trackers {
		allNines := tracker.t.Percentiles()
		for _, distribution := range allNines {
			for _, nine := range distribution {
				// We added a bunch of 1 hour datapoints before.
				// It's safe to assume that the percentiles were
				// pushed to at least 1 hour.
				if nine < time.Hour {
					t.Fatalf("registry stats should now be strictly greater than the seed %v %v", nine, tracker.seed)
				}
			}
		}
		// Add a very high datapoint.
		tracker.t.AddDataPoint(time.Hour)
	}
}

// TestRenterPaths checks that the renter properly handles nicknames
// containing the path separator ("/").
func TestRenterPaths(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Start renter with background loops disabled to avoid NDFs related to this
	// test creating siafiles directly vs through the staticFileSystem.
	rt, err := newRenterTesterWithDependency(t.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create and save some files.
	// The result of saving these files should be a directory containing:
	//   foo.sia
	//   foo/bar.sia
	//   foo/bar/baz.sia

	siaPath1, err := skymodules.NewSiaPath("foo")
	if err != nil {
		t.Fatal(err)
	}
	siaPath2, err := skymodules.NewSiaPath("foo/bar")
	if err != nil {
		t.Fatal(err)
	}
	siaPath3, err := skymodules.NewSiaPath("foo/bar/baz")
	if err != nil {
		t.Fatal(err)
	}

	// Create the parent dirs manually since we are going to use siafile.New
	// instead of filesystem.NewSiaFile.
	sp3Parent, err := siaPath3.Dir()
	if err != nil {
		t.Fatal(err)
	}
	err = rt.renter.staticFileSystem.NewSiaDir(sp3Parent, skymodules.DefaultDirPerm)
	if err != nil {
		t.Fatal(err)
	}

	wal := rt.renter.staticWAL
	rc, err := skymodules.NewRSSubCode(1, 1, crypto.SegmentSize)
	if err != nil {
		t.Fatal(err)
	}
	sk := crypto.GenerateSiaKey(crypto.TypeThreefish)
	fileSize := uint64(modules.SectorSize)
	fileMode := os.FileMode(0600)
	f1, err := siafile.New(siaPath1.SiaFileSysPath(rt.renter.staticFileSystem.Root()), "", wal, rc, sk, fileSize, fileMode)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := siafile.New(siaPath2.SiaFileSysPath(rt.renter.staticFileSystem.Root()), "", wal, rc, sk, fileSize, fileMode)
	if err != nil {
		t.Fatal(err)
	}
	f3, err := siafile.New(siaPath3.SiaFileSysPath(rt.renter.staticFileSystem.Root()), "", wal, rc, sk, fileSize, fileMode)
	if err != nil {
		t.Fatal(err)
	}

	// Restart the renter to re-do the init cycle.
	err = rt.renter.Close()
	if err != nil {
		t.Fatal(err)
	}
	var errChan <-chan error
	rl := ratelimit.NewRateLimit(0, 0, 0)
	tus := NewSkynetTUSInMemoryUploadStore()
	rt.renter, errChan = New(rt.gateway, rt.cs, rt.wallet, rt.tpool, rt.mux, tus, rl, filepath.Join(rt.dir, skymodules.RenterDir))
	if err := <-errChan; err != nil {
		t.Fatal(err)
	}

	// Check that the files were loaded properly.
	entry1, err := rt.renter.staticFileSystem.OpenSiaFile(siaPath1)
	if err != nil {
		t.Fatal("File not found in renter", err)
	}
	if err := equalFiles(f1, entry1.SiaFile); err != nil {
		t.Fatal(err)
	}
	entry2, err := rt.renter.staticFileSystem.OpenSiaFile(siaPath2)
	if err != nil {
		t.Fatal("File not found in renter", err)
	}
	if err := equalFiles(f2, entry2.SiaFile); err != nil {
		t.Fatal(err)
	}
	entry3, err := rt.renter.staticFileSystem.OpenSiaFile(siaPath3)
	if err != nil {
		t.Fatal("File not found in renter", err)
	}
	if err := equalFiles(f3, entry3.SiaFile); err != nil {
		t.Fatal(err)
	}

	// To confirm that the file structure was preserved, we walk the renter
	// folder and emit the name of each .sia file encountered (filepath.Walk
	// is deterministic; it orders the files lexically).
	var walkStr string
	filepath.Walk(rt.renter.staticFileSystem.Root(), func(path string, _ os.FileInfo, _ error) error {
		// capture only .sia files
		if filepath.Ext(path) != ".sia" {
			return nil
		}
		rel, _ := filepath.Rel(rt.renter.staticFileSystem.Root(), path) // strip testdir prefix
		walkStr += rel
		return nil
	})
	// walk will descend into foo/bar/, reading baz, bar, and finally foo
	sfs := rt.renter.staticFileSystem
	expWalkStr := (sfs.FileSiaPath(entry3).String() + ".sia") + (sfs.FileSiaPath(entry2).String() + ".sia") + (sfs.FileSiaPath(entry1).String() + ".sia")
	if filepath.ToSlash(walkStr) != expWalkStr {
		t.Fatalf("Bad walk string: expected %v, got %v", expWalkStr, walkStr)
	}
}

// TestSiafileCompatibility tests that the renter is able to load v0.4.8 .sia files.
func TestSiafileCompatibility(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	rt, err := newRenterTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Load the compatibility file into the renter.
	path := filepath.Join("..", "..", "compatibility", "siafile_v0.4.8.sia")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var oc []skymodules.RenterContract
	names, err := rt.renter.compatV137loadSiaFilesFromReader(f, make(map[string]v137TrackedFile), oc)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "testfile-183" {
		t.Fatal("nickname not loaded properly:", names)
	}
	// Make sure that we can open the file afterwards.
	siaPath, err := skymodules.UserFolder.Join(names[0])
	if err != nil {
		t.Fatal(err)
	}
	sf, err := rt.renter.staticFileSystem.OpenSiaFile(siaPath)
	if err != nil {
		t.Fatal(err)
	}
	if sf.NumChunks() < 1 {
		t.Fatal("invalid number of chunks in siafile:", sf.NumChunks())
	}
}
