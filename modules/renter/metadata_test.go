package renter

import (
	"fmt"
	"path/filepath"
	"testing"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/renter/filesystem/siafile"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/Sia/siatest/dependencies"
)

// BenchmarkBubbleMetadata runs a benchmark on the bubble metadata method
func BenchmarkBubbleMetadata(b *testing.B) {
	r, err := newBenchmarkRenterWithDependency(b.Name(), &dependencies.DependencyDisableRepairAndHealthLoops{})
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()

	// Create Directory
	dirSiaPath, err := modules.NewSiaPath("root")
	if err != nil {
		b.Fatal(err)
	}
	err = r.CreateDir(dirSiaPath, modules.DefaultDirPerm)
	if err != nil {
		b.Fatal(err)
	}

	// Create add 5 files
	rsc, _ := siafile.NewRSCode(1, 1)
	for i := 0; i < 5; i++ {
		fileSiaPath, err := dirSiaPath.Join(fmt.Sprintf("file%v", i))
		if err != nil {
			b.Fatal(err)
		}
		up := modules.FileUploadParams{
			Source:      "",
			SiaPath:     fileSiaPath,
			ErasureCode: rsc,
		}
		err = r.staticFileSystem.NewSiaFile(up.SiaPath, up.Source, up.ErasureCode, crypto.GenerateSiaKey(crypto.RandomCipherType()), 100, persist.DefaultDiskPermissionsTest, up.DisablePartialChunk)
		if err != nil {
			b.Log("Dir", dirSiaPath)
			b.Log("File", fileSiaPath)
			b.Fatal(err)
		}
	}
	// Reset Timer
	b.ResetTimer()

	// Run Benchmark
	for n := 0; n < b.N; n++ {
		err := r.managedBubbleMetadata(dirSiaPath)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// newBenchmarkRenterWithDependency creates a renter to be used for benchmarks
// on renter methods
func newBenchmarkRenterWithDependency(name string, deps modules.Dependencies) (*Renter, error) {
	testdir := build.TempDir("renter", name)
	rt, err := newRenterTesterNoRenter(testdir)
	if err != nil {
		return nil, err
	}
	r, err := newRenterWithDependency(rt.gateway, rt.cs, rt.wallet, rt.tpool, rt.mux, filepath.Join(testdir, modules.RenterDir), deps)
	if err != nil {
		return nil, err
	}
	return r, nil
}
