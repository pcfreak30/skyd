package siadir

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"go.sia.tech/siad/modules"

	"gitlab.com/SkynetLabs/skyd/skymodules"
)

type (
	// SiaDir contains the metadata information about a renter directory
	SiaDir struct {
		metadata Metadata

		// path is the path of the SiaDir folder.
		path string

		// Utility fields
		deleted bool
		deps    modules.Dependencies
		mu      sync.Mutex
	}

	// Metadata is the metadata that is saved to disk as a .siadir file
	Metadata struct {
		// For each field in the metadata there is an aggregate value and a
		// siadir specific value. If a field has the aggregate prefix it means
		// that the value takes into account all the siafiles and siadirs in the
		// sub tree. The definition of aggregate and siadir specific values is
		// otherwise the same.
		//
		// Health is the health of the most in need siafile that is not stuck
		//
		// LastHealthCheckTime is the oldest LastHealthCheckTime of any of the
		// siafiles in the siadir and is the last time the health was calculated
		// by the health loop
		//
		// MinRedundancy is the minimum redundancy of any of the siafiles in the
		// siadir
		//
		// ModTime is the last time any of the siafiles in the siadir was
		// updated
		//
		// NumFiles is the total number of siafiles in a siadir
		//
		// NumLostFiles is the total number of siafiles in a siadir that are not on
		// disk and have a health of <= 0
		//
		// NumStuckChunks is the sum of all the Stuck Chunks of any of the
		// siafiles in the siadir
		//
		// NumSubDirs is the number of sub-siadirs in a siadir
		//
		// NumUnfinishedFiles is the total number of unfinished siafiles in a
		// siadir
		//
		// Size is the total amount of data stored in the siafiles of the siadir
		//
		// StuckHealth is the health of the most in need siafile in the siadir,
		// stuck or not stuck

		// The following fields are aggregate values of the siadir. These values are
		// the totals of the siadir and any sub siadirs, or are calculated based on
		// all the values in the subtree
		AggregateHealth              float64   `json:"aggregatehealth"`
		AggregateLastHealthCheckTime time.Time `json:"aggregatelasthealthchecktime"`
		AggregateMinRedundancy       float64   `json:"aggregateminredundancy"`
		AggregateModTime             time.Time `json:"aggregatemodtime"`
		AggregateNumFiles            uint64    `json:"aggregatenumfiles"`
		AggregateNumLostFiles        uint64    `json:"aggregatenumlostfiles"`
		AggregateNumStuckChunks      uint64    `json:"aggregatenumstuckchunks"`
		AggregateNumSubDirs          uint64    `json:"aggregatenumsubdirs"`
		AggregateNumUnfinishedFiles  uint64    `json:"aggregatenumunfinishedfiles"`
		AggregateRemoteHealth        float64   `json:"aggregateremotehealth"`
		AggregateRepairSize          uint64    `json:"aggregaterepairsize"`
		AggregateSize                uint64    `json:"aggregatesize"`
		AggregateStuckHealth         float64   `json:"aggregatestuckhealth"`
		AggregateStuckSize           uint64    `json:"aggregatestucksize"`

		// Aggregate Skynet Specific Stats
		AggregateSkynetFiles uint64 `json:"aggregateskynetfiles"`
		AggregateSkynetSize  uint64 `json:"aggregateskynetsize"`

		// The following fields are information specific to the siadir that is not
		// an aggregate of the entire sub directory tree
		Health              float64     `json:"health"`
		LastHealthCheckTime time.Time   `json:"lasthealthchecktime"`
		MinRedundancy       float64     `json:"minredundancy"`
		Mode                os.FileMode `json:"mode"`
		ModTime             time.Time   `json:"modtime"`
		NumFiles            uint64      `json:"numfiles"`
		NumLostFiles        uint64      `json:"numlostfiles"`
		NumStuckChunks      uint64      `json:"numstuckchunks"`
		NumSubDirs          uint64      `json:"numsubdirs"`
		NumUnfinishedFiles  uint64      `json:"numunfinishedfiles"`
		RemoteHealth        float64     `json:"remotehealth"`
		RepairSize          uint64      `json:"repairsize"`
		Size                uint64      `json:"size"`
		StuckHealth         float64     `json:"stuckhealth"`
		StuckSize           uint64      `json:"stucksize"`

		// Skynet Specific Stats
		SkynetFiles uint64 `json:"skynetfiles"`
		SkynetSize  uint64 `json:"skynetsize"`

		// Version is the used version of the header file.
		Version string `json:"version"`
	}
)

// EqualMetadatas compares two Metadatas. If using this function to check
// persistence the time fields should be checked separately  due to how time is
// persisted
func EqualMetadatas(md1, md2 Metadata) (err error) {
	// Check Aggregate Fields
	if md1.AggregateHealth != md2.AggregateHealth {
		err = errors.Compose(err, fmt.Errorf("AggregateHealth not equal, %v and %v", md1.AggregateHealth, md2.AggregateHealth))
	}
	if md1.AggregateLastHealthCheckTime != md2.AggregateLastHealthCheckTime {
		err = errors.Compose(err, fmt.Errorf("AggregateLastHealthCheckTimes not equal, %v and %v", md1.AggregateLastHealthCheckTime, md2.AggregateLastHealthCheckTime))
	}
	if md1.AggregateMinRedundancy != md2.AggregateMinRedundancy {
		err = errors.Compose(err, fmt.Errorf("AggregateMinRedundancy not equal, %v and %v", md1.AggregateMinRedundancy, md2.AggregateMinRedundancy))
	}
	if md1.AggregateModTime != md2.AggregateModTime {
		err = errors.Compose(err, fmt.Errorf("AggregateModTimes not equal, %v and %v", md1.AggregateModTime, md2.AggregateModTime))
	}
	if md1.AggregateNumFiles != md2.AggregateNumFiles {
		err = errors.Compose(err, fmt.Errorf("AggregateNumFiles not equal, %v and %v", md1.AggregateNumFiles, md2.AggregateNumFiles))
	}
	if md1.AggregateNumLostFiles != md2.AggregateNumLostFiles {
		err = errors.Compose(err, fmt.Errorf("AggregateNumLostFiles not equal, %v and %v", md1.AggregateNumLostFiles, md2.AggregateNumLostFiles))
	}
	if md1.AggregateNumStuckChunks != md2.AggregateNumStuckChunks {
		err = errors.Compose(err, fmt.Errorf("AggregateNumStuckChunks not equal, %v and %v", md1.AggregateNumStuckChunks, md2.AggregateNumStuckChunks))
	}
	if md1.AggregateNumSubDirs != md2.AggregateNumSubDirs {
		err = errors.Compose(err, fmt.Errorf("AggregateNumSubDirs not equal, %v and %v", md1.AggregateNumSubDirs, md2.AggregateNumSubDirs))
	}
	if md1.AggregateNumUnfinishedFiles != md2.AggregateNumUnfinishedFiles {
		err = errors.Compose(err, fmt.Errorf("AggregateNumUnfinishedFiles not equal, %v and %v", md1.AggregateNumUnfinishedFiles, md2.AggregateNumUnfinishedFiles))
	}
	if md1.AggregateRemoteHealth != md2.AggregateRemoteHealth {
		err = errors.Compose(err, fmt.Errorf("AggregateRemoteHealth not equal, %v and %v", md1.AggregateRemoteHealth, md2.AggregateRemoteHealth))
	}
	if md1.AggregateRepairSize != md2.AggregateRepairSize {
		err = errors.Compose(err, fmt.Errorf("AggregateRepairSize not equal, %v and %v", md1.AggregateRepairSize, md2.AggregateRepairSize))
	}
	if md1.AggregateSize != md2.AggregateSize {
		err = errors.Compose(err, fmt.Errorf("AggregateSize not equal, %v and %v", md1.AggregateSize, md2.AggregateSize))
	}
	if md1.AggregateStuckHealth != md2.AggregateStuckHealth {
		err = errors.Compose(err, fmt.Errorf("AggregateStuckHealth not equal, %v and %v", md1.AggregateStuckHealth, md2.AggregateStuckHealth))
	}
	if md1.AggregateStuckSize != md2.AggregateStuckSize {
		err = errors.Compose(err, fmt.Errorf("AggregateStuckSize not equal, %v and %v", md1.AggregateStuckSize, md2.AggregateStuckSize))
	}

	// Aggregate Skynet Fields
	if md1.AggregateSkynetFiles != md2.AggregateSkynetFiles {
		err = errors.Compose(err, fmt.Errorf("AggregateSkynetFiles not equal, %v and %v", md1.AggregateSkynetFiles, md2.AggregateSkynetFiles))
	}
	if md1.AggregateSkynetSize != md2.AggregateSkynetSize {
		err = errors.Compose(err, fmt.Errorf("AggregateSkynetSize not equal, %v and %v", md1.AggregateSkynetSize, md2.AggregateSkynetSize))
	}

	// Check SiaDir Fields
	if md1.Health != md2.Health {
		err = errors.Compose(err, fmt.Errorf("Healths not equal, %v and %v", md1.Health, md2.Health))
	}
	if md1.LastHealthCheckTime != md2.LastHealthCheckTime {
		err = errors.Compose(err, fmt.Errorf("LastHealthCheckTime not equal, %v and %v", md1.LastHealthCheckTime, md2.LastHealthCheckTime))
	}
	if md1.MinRedundancy != md2.MinRedundancy {
		err = errors.Compose(err, fmt.Errorf("MinRedundancy not equal, %v and %v", md1.MinRedundancy, md2.MinRedundancy))
	}
	if md1.ModTime != md2.ModTime {
		err = errors.Compose(err, fmt.Errorf("ModTime not equal, %v and %v", md1.ModTime, md2.ModTime))
	}
	if md1.NumFiles != md2.NumFiles {
		err = errors.Compose(err, fmt.Errorf("NumFiles not equal, %v and %v", md1.NumFiles, md2.NumFiles))
	}
	if md1.NumLostFiles != md2.NumLostFiles {
		err = errors.Compose(err, fmt.Errorf("NumLostFiles not equal, %v and %v", md1.NumLostFiles, md2.NumLostFiles))
	}
	if md1.NumStuckChunks != md2.NumStuckChunks {
		err = errors.Compose(err, fmt.Errorf("NumStuckChunks not equal, %v and %v", md1.NumStuckChunks, md2.NumStuckChunks))
	}
	if md1.NumSubDirs != md2.NumSubDirs {
		err = errors.Compose(err, fmt.Errorf("NumSubDirs not equal, %v and %v", md1.NumSubDirs, md2.NumSubDirs))
	}
	if md1.NumUnfinishedFiles != md2.NumUnfinishedFiles {
		err = errors.Compose(err, fmt.Errorf("NumUnfinishedFiles not equal, %v and %v", md1.NumUnfinishedFiles, md2.NumUnfinishedFiles))
	}
	if md1.RemoteHealth != md2.RemoteHealth {
		err = errors.Compose(err, fmt.Errorf("RemoteHealth not equal, %v and %v", md1.RemoteHealth, md2.RemoteHealth))
	}
	if md1.RepairSize != md2.RepairSize {
		err = errors.Compose(err, fmt.Errorf("RepairSize not equal, %v and %v", md1.RepairSize, md2.RepairSize))
	}
	if md1.Size != md2.Size {
		err = errors.Compose(err, fmt.Errorf("Sizes not equal, %v and %v", md1.Size, md2.Size))
	}
	if md1.StuckHealth != md2.StuckHealth {
		err = errors.Compose(err, fmt.Errorf("StuckHealth not equal, %v and %v", md1.StuckHealth, md2.StuckHealth))
	}
	if md1.StuckSize != md2.StuckSize {
		err = errors.Compose(err, fmt.Errorf("StuckSize not equal, %v and %v", md1.StuckSize, md2.StuckSize))
	}

	// Skynet Fields
	if md1.SkynetFiles != md2.SkynetFiles {
		err = errors.Compose(err, fmt.Errorf("SkynetFiles not equal, %v and %v", md1.SkynetFiles, md2.SkynetFiles))
	}
	if md1.SkynetSize != md2.SkynetSize {
		err = errors.Compose(err, fmt.Errorf("SkynetSize not equal, %v and %v", md1.SkynetSize, md2.SkynetSize))
	}
	return
}

// VerifyMetadataInit verifies that metadata was properly initialized.
func VerifyMetadataInit(md Metadata) error {
	// Check that the modTimes are not Zero
	if md.AggregateModTime.IsZero() {
		return errors.New("AggregateModTime not initialized")
	}
	if md.ModTime.IsZero() {
		return errors.New("ModTime not initialized")
	}

	// All the rest of the metadata should be default values
	initMetadata := Metadata{
		AggregateHealth:        DefaultDirHealth,
		AggregateMinRedundancy: DefaultDirRedundancy,
		AggregateModTime:       md.AggregateModTime,
		AggregateRemoteHealth:  DefaultDirHealth,
		AggregateStuckHealth:   DefaultDirHealth,

		Health:        DefaultDirHealth,
		MinRedundancy: DefaultDirRedundancy,
		ModTime:       md.ModTime,
		RemoteHealth:  DefaultDirHealth,
		StuckHealth:   DefaultDirHealth,
	}

	return EqualMetadatas(md, initMetadata)
}

// mdPath returns the path of the SiaDir's metadata on disk.
func (sd *SiaDir) mdPath() string {
	return filepath.Join(sd.path, skymodules.SiaDirExtension)
}

// Deleted returns the deleted field of the siaDir
func (sd *SiaDir) Deleted() bool {
	sd.mu.Lock()
	defer sd.mu.Unlock()
	return sd.deleted
}

// Metadata returns the metadata of the SiaDir
func (sd *SiaDir) Metadata() Metadata {
	sd.mu.Lock()
	defer sd.mu.Unlock()
	return sd.metadata
}

// Path returns the path of the SiaDir on disk.
func (sd *SiaDir) Path() string {
	sd.mu.Lock()
	defer sd.mu.Unlock()
	return sd.path
}

// MDPath returns the path of the SiaDir's metadata on disk.
func (sd *SiaDir) MDPath() string {
	sd.mu.Lock()
	defer sd.mu.Unlock()
	return sd.mdPath()
}
