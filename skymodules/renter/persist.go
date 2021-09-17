package renter

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/writeaheadlog"

	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem/siafile"
	"go.sia.tech/siad/persist"
	"go.sia.tech/siad/types"
)

// PersistedStats contains the information about the renter's stats which is
// persisted to disk.
type PersistedStats struct {
	RegistryReadStats     skymodules.PersistedDistributionTracker `json:"registryreadstats"`
	RegistryWriteStats    skymodules.PersistedDistributionTracker `json:"registrywritestats"`
	BaseSectorUploadStats skymodules.PersistedDistributionTracker `json:"basesectoruploadstats"`
	ChunkUploadStats      skymodules.PersistedDistributionTracker `json:"chunkuploadstats"`
	StreamBufferStats     skymodules.PersistedDistributionTracker `json:"streambufferstats"`
}

const (
	logFile       = skymodules.RenterDir + ".log"
	repairLogFile = "repair.log"
	// PersistFilename is the filename to be used when persisting renter
	// information to a JSON file
	PersistFilename = "renter.json"
	// SiaDirMetadata is the name of the metadata file for the sia directory
	SiaDirMetadata = ".siadir"
	// StatsFilename is the name of the file persisting the stats of the
	// renter.
	StatsFilename = "stats.json"
	// walFile is the filename of the renter's writeaheadlog's file.
	walFile = skymodules.RenterDir + ".wal"
)

var (
	// ErrBadFile is an error when a file does not qualify as .sia file
	ErrBadFile = errors.New("not a .sia file")
	// ErrIncompatible is an error when file is not compatible with current
	// version
	ErrIncompatible = errors.New("file is not compatible with current version")
	// ErrNoNicknames is an error when no nickname is given
	ErrNoNicknames = errors.New("at least one nickname must be supplied")
	// ErrNonShareSuffix is an error when the suffix of a file does not match
	// the defined share extension
	ErrNonShareSuffix = errors.New("suffix of file must be " + skymodules.SiaFileExtension)

	settingsMetadata = persist.Metadata{
		Header:  "Renter Persistence",
		Version: persistVersion,
	}

	shareHeader  = [15]byte{'S', 'i', 'a', ' ', 'S', 'h', 'a', 'r', 'e', 'd', ' ', 'F', 'i', 'l', 'e'}
	shareVersion = "0.4"

	// statsPersistInterval defines the interval the renter uses for
	// persisting its stats.
	statsPersistInterval = build.Select(build.Var{
		Dev:      time.Minute,
		Standard: 30 * time.Minute,
		Testing:  2 * time.Second,
	}).(time.Duration)

	// statsMetadata is the metadata used when persisting the renter stats.
	statsMetadata = persist.Metadata{
		Header:  "Stats",
		Version: "1.5.7",
	}

	// Persist Version Numbers
	persistVersion040 = "0.4"
	persistVersion133 = "1.3.3"
	persistVersion140 = "1.4.0"
	persistVersion142 = "1.4.2"
)

type (
	// persist contains all of the persistent renter data.
	persistence struct {
		MaxDownloadSpeed int64
		MaxUploadSpeed   int64
		UploadedBackups  []skymodules.UploadedBackup
		SyncedContracts  []types.FileContractID

		ConversionRates  map[string]types.Currency
		MonetizationBase types.Currency
	}
)

// saveSync stores the current renter data to disk and then syncs to disk.
func (r *Renter) saveSync() error {
	return persist.SaveJSON(settingsMetadata, r.persist, filepath.Join(r.persistDir, PersistFilename))
}

// threadedStatsPersister periodically persists the renter's collected stats.
func (r *Renter) threadedStatsPersister() {
	if err := r.tg.Add(); err != nil {
		return
	}
	defer r.tg.Done()

	ticker := time.NewTicker(statsPersistInterval)
	for {
		statsPath := filepath.Join(r.persistDir, StatsFilename)
		err := persist.SaveJSON(statsMetadata, PersistedStats{
			RegistryReadStats:     r.staticRegistryReadStats.Persist(),
			RegistryWriteStats:    r.staticRegWriteStats.Persist(),
			BaseSectorUploadStats: r.staticBaseSectorUploadStats.Persist(),
			ChunkUploadStats:      r.staticChunkUploadStats.Persist(),
			StreamBufferStats:     r.staticStreamBufferStats.Persist(),
		}, statsPath)
		if err != nil {
			r.staticLog.Print("Failed to persist stats object:", err)
		}

		// Sleep
		select {
		case <-r.tg.StopCtx().Done():
			return // shutdown
		case <-ticker.C:
		}
	}
}

// managedLoadSettings fetches the saved renter data from disk.
func (r *Renter) managedLoadSettings() error {
	r.persist = persistence{}
	err := persist.LoadJSON(settingsMetadata, &r.persist, filepath.Join(r.persistDir, PersistFilename))
	if os.IsNotExist(err) {
		// No persistence yet, set the defaults and continue.
		r.persist.MaxDownloadSpeed = DefaultMaxDownloadSpeed
		r.persist.MaxUploadSpeed = DefaultMaxUploadSpeed
		id := r.mu.Lock()
		err = r.saveSync()
		r.mu.Unlock(id)
		if err != nil {
			return err
		}
	} else if errors.Contains(err, persist.ErrBadVersion) {
		// Outdated version, try the 040 to 133 upgrade.
		err = convertPersistVersionFrom040To133(filepath.Join(r.persistDir, PersistFilename))
		if err != nil {
			r.staticLog.Println("WARNING: 040 to 133 renter upgrade failed, trying 133 to 140 next", err)
		}
		// Then upgrade from 133 to 140.
		oldContracts := r.staticHostContractor.OldContracts()
		err = r.convertPersistVersionFrom133To140(filepath.Join(r.persistDir, PersistFilename), oldContracts)
		if err != nil {
			r.staticLog.Println("WARNING: 133 to 140 renter upgrade failed", err)
		}
		// Then upgrade from 140 to 142.
		err = r.convertPersistVersionFrom140To142(filepath.Join(r.persistDir, PersistFilename))
		if err != nil {
			r.staticLog.Println("WARNING: 140 to 142 renter upgrade failed", err)
			// Nothing left to try.
			return err
		}
		r.staticLog.Println("Renter upgrade successful")
		// Re-load the settings now that the file has been upgraded.
		return r.managedLoadSettings()
	} else if err != nil {
		return err
	}

	// Make sure the usd conversion rate is set to 0 if it isn't set yet.
	if r.persist.ConversionRates == nil {
		r.persist.ConversionRates = make(map[string]types.Currency)
	}
	if _, exists := r.persist.ConversionRates[skymodules.CurrencyUSD]; !exists {
		r.persist.ConversionRates[skymodules.CurrencyUSD] = types.ZeroCurrency
	}

	// Set the bandwidth limits on the contractor, which was already initialized
	// without bandwidth limits.
	return r.staticSetBandwidthLimits(r.persist.MaxDownloadSpeed, r.persist.MaxUploadSpeed)
}

// managedInitPersist handles all of the persistence initialization, such as creating
// the persistence directory and starting the logger.
func (r *Renter) managedInitPersist() error {
	// Create the persist and filesystem directories if they do not yet exist.
	//
	// Note: the os package needs to be used here instead of the renter's
	// CreateDir method because the staticDirSet has not been initialized yet.
	// The directory is needed before the staticDirSet can be initialized
	// because the wal needs the directory to be created and the staticDirSet
	// needs the wal.
	fsRoot := filepath.Join(r.persistDir, skymodules.FileSystemRoot)
	err := os.MkdirAll(fsRoot, skymodules.DefaultDirPerm)
	if err != nil {
		return err
	}

	// Initialize the writeaheadlog.
	options := writeaheadlog.Options{
		StaticLog: r.staticLog.Logger,
		Path:      filepath.Join(r.persistDir, walFile),
	}
	txns, wal, err := writeaheadlog.NewWithOptions(options)
	if err != nil {
		return err
	}
	if err := r.tg.AfterStop(wal.Close); err != nil {
		return err
	}

	// Apply unapplied wal txns before loading the persistence structure to
	// avoid loading potentially corrupted files.
	if len(txns) > 0 {
		r.staticLog.Println("Wal initialized", len(txns), "transactions to apply")
	}
	for _, txn := range txns {
		applyTxn := true
		r.staticLog.Println("applying transaction with", len(txn.Updates), "updates")
		for _, update := range txn.Updates {
			if siafile.IsSiaFileUpdate(update) {
				r.staticLog.Println("Applying a siafile update:", update.Name)
				if err := siafile.ApplyUpdates(update); err != nil {
					return errors.AddContext(err, "failed to apply SiaFile update")
				}
			} else {
				r.staticLog.Println("wal update not applied, marking transaction as not applied")
				applyTxn = false
			}
		}
		if applyTxn {
			if err := txn.SignalUpdatesApplied(); err != nil {
				return err
			}
		}
	}

	// Create the filesystem.
	fs, err := filesystem.New(fsRoot, r.staticLog, wal)
	if err != nil {
		return err
	}

	// Initialize the wal, staticFileSet and the staticDirSet. With the
	// staticDirSet finish the initialization of the files directory
	r.staticWAL = wal
	r.staticFileSystem = fs

	// Load the prior persistence structures.
	if err := r.managedLoadSettings(); err != nil {
		return errors.AddContext(err, "failed to load renter's persistence structrue")
	}

	// Load the stats.
	if err := r.managedInitStats(); err != nil {
		return errors.AddContext(err, "failed to initialize the renter's distribution trackers")
	}

	// Create the essential dirs in the filesystem.
	err = fs.NewSiaDir(skymodules.HomeFolder, skymodules.DefaultDirPerm)
	if err != nil && !errors.Contains(err, filesystem.ErrExists) {
		return err
	}
	err = fs.NewSiaDir(skymodules.UserFolder, skymodules.DefaultDirPerm)
	if err != nil && !errors.Contains(err, filesystem.ErrExists) {
		return err
	}
	err = fs.NewSiaDir(skymodules.BackupFolder, skymodules.DefaultDirPerm)
	if err != nil && !errors.Contains(err, filesystem.ErrExists) {
		return err
	}
	err = fs.NewSiaDir(skymodules.SkynetFolder, skymodules.DefaultDirPerm)
	if err != nil && !errors.Contains(err, filesystem.ErrExists) {
		return err
	}
	return nil
}

// managedInitStats initializes the distribution trackers of the renter.
func (r *Renter) managedInitStats() error {
	// Init the trackers.
	r.staticRegistryReadStats = skymodules.NewDistributionTrackerStandard()
	r.staticRegWriteStats = skymodules.NewDistributionTrackerStandard()
	r.staticBaseSectorUploadStats = skymodules.NewDistributionTrackerStandard()
	r.staticChunkUploadStats = skymodules.NewDistributionTrackerStandard()
	r.staticStreamBufferStats = skymodules.NewDistributionTrackerStandard()

	// Load the existing stats.
	statsPath := filepath.Join(r.persistDir, StatsFilename)
	var stats PersistedStats
	err := persist.LoadJSON(statsMetadata, &stats, statsPath)
	if os.IsNotExist(err) {
		// No persistence yet. Seed the trackers.
		r.staticRegistryReadStats.AddDataPoint(readRegistryStatsSeed) // Seed the stats so that startup doesn't say 0.
		r.staticRegWriteStats.AddDataPoint(5 * time.Second)           // Seed the stats so that startup doesn't say 0.
		r.staticBaseSectorUploadStats.AddDataPoint(15 * time.Second)  // Seed the stats so that startup doesn't say 0.
		r.staticChunkUploadStats.AddDataPoint(15 * time.Second)       // Seed the stats so that startup doesn't say 0.
		r.staticStreamBufferStats.AddDataPoint(5 * time.Second)       // Seed the stats so that startup doesn't say 0.
		return nil
	} else if err != nil {
		if build.Release == "testing" {
			build.Critical(err)
		}
		fmt.Println("WARN: reset stats after failing to load them", err)
		return nil // ignore and overwrite
	}

	// Found stats. Seed with existing values.
	err1 := r.staticRegistryReadStats.Load(stats.RegistryReadStats)
	err2 := r.staticRegWriteStats.Load(stats.RegistryWriteStats)
	err3 := r.staticBaseSectorUploadStats.Load(stats.BaseSectorUploadStats)
	err4 := r.staticChunkUploadStats.Load(stats.ChunkUploadStats)
	err5 := r.staticStreamBufferStats.Load(stats.StreamBufferStats)
	if err := errors.Compose(err1, err2, err3, err4, err5); err != nil {
		if build.Release == "testing" {
			build.Critical(err)
		}
		fmt.Println("WARN: failed to load one or more distribution trackers")
		return nil // ignore and overwrite
	}
	return nil
}
