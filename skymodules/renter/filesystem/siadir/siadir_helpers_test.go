package siadir

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/fastrand"
	"go.sia.tech/siad/persist"
)

// newRootDir creates a root directory for the test and removes old test files
func newRootDir(t *testing.T) (string, error) {
	dir := filepath.Join(os.TempDir(), "siadirs", t.Name())
	err := os.RemoveAll(dir)
	if err != nil {
		return "", err
	}
	return dir, nil
}

// randomMetadata returns a siadir Metadata struct with random values set
func randomMetadata() Metadata {
	md := Metadata{
		AggregateHealth:              float64(fastrand.Intn(100)),
		AggregateLastHealthCheckTime: time.Now(),
		AggregateMinRedundancy:       float64(fastrand.Intn(100)),
		AggregateModTime:             time.Now(),
		AggregateNumFiles:            fastrand.Uint64n(100),
		AggregateNumLostFiles:        fastrand.Uint64n(100),
		AggregateNumStuckChunks:      fastrand.Uint64n(100),
		AggregateNumSubDirs:          fastrand.Uint64n(100),
		AggregateNumUnfinishedFiles:  fastrand.Uint64n(100),
		AggregateRemoteHealth:        float64(fastrand.Intn(100)),
		AggregateRepairSize:          fastrand.Uint64n(100),
		AggregateSize:                fastrand.Uint64n(100),
		AggregateStuckHealth:         float64(fastrand.Intn(100)),
		AggregateStuckSize:           fastrand.Uint64n(100),

		AggregateSkynetFiles: fastrand.Uint64n(100),
		AggregateSkynetSize:  fastrand.Uint64n(100),

		Health:              float64(fastrand.Intn(100)),
		LastHealthCheckTime: time.Now(),
		MinRedundancy:       float64(fastrand.Intn(100)),
		ModTime:             time.Now(),
		NumFiles:            fastrand.Uint64n(100),
		NumLostFiles:        fastrand.Uint64n(100),
		NumStuckChunks:      fastrand.Uint64n(100),
		NumSubDirs:          fastrand.Uint64n(100),
		NumUnfinishedFiles:  fastrand.Uint64n(100),
		RemoteHealth:        float64(fastrand.Intn(100)),
		RepairSize:          fastrand.Uint64n(100),
		Size:                fastrand.Uint64n(100),
		StuckHealth:         float64(fastrand.Intn(100)),
		StuckSize:           fastrand.Uint64n(100),

		SkynetFiles: fastrand.Uint64n(100),
		SkynetSize:  fastrand.Uint64n(100),
	}
	return md
}

// newSiaDirTestDir creates a test directory for a siadir test
func newSiaDirTestDir(testDir string) (string, error) {
	rootPath := filepath.Join(os.TempDir(), "siadirs", testDir)
	if err := os.RemoveAll(rootPath); err != nil {
		return "", err
	}
	return rootPath, os.MkdirAll(rootPath, persist.DefaultDiskPermissionsTest)
}
