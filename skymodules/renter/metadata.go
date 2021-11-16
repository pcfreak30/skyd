package renter

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"go.sia.tech/siad/modules"

	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem/siadir"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem/siafile"
)

// ErrSkylinkUnpinned is the error returned when an unpinned skylink is found
// during a bubble.
var ErrSkylinkUnpinned = errors.New("skylink is unpinned")

// bubbledSiaDirMetadata is a wrapper for siadir.Metadata that also contains the
// siapath for convenience.
type bubbledSiaDirMetadata struct {
	sp skymodules.SiaPath
	siadir.Metadata
}

// bubbledSiaFileMetadata is a wrapper for siafile.BubbledMetadata that also
// contains the siapath for convenience.
type bubbledSiaFileMetadata struct {
	sp skymodules.SiaPath
	bm siafile.BubbledMetadata
}

// callCalculateDirectoryMetadata calculates the new values for the
// directory's metadata and tracks the value, either worst or best, for each to
// be bubbled up
func (r *Renter) callCalculateDirectoryMetadata(siaPath skymodules.SiaPath) (siadir.Metadata, error) {
	// Set default metadata values to start
	now := time.Now()
	metadata := siadir.Metadata{
		AggregateHealth:              siadir.DefaultDirHealth,
		AggregateLastHealthCheckTime: now,
		AggregateMinRedundancy:       math.MaxFloat64,
		AggregateModTime:             time.Time{},
		AggregateNumFiles:            uint64(0),
		AggregateNumLostFiles:        uint64(0),
		AggregateNumStuckChunks:      uint64(0),
		AggregateNumSubDirs:          uint64(0),
		AggregateNumUnfinishedFiles:  uint64(0),
		AggregateRemoteHealth:        siadir.DefaultDirHealth,
		AggregateRepairSize:          uint64(0),
		AggregateSize:                uint64(0),
		AggregateStuckHealth:         siadir.DefaultDirHealth,
		AggregateStuckSize:           uint64(0),

		AggregateSkynetFiles: uint64(0),
		AggregateSkynetSize:  uint64(0),

		Health:              siadir.DefaultDirHealth,
		LastHealthCheckTime: now,
		MinRedundancy:       math.MaxFloat64,
		ModTime:             time.Time{},
		NumFiles:            uint64(0),
		NumLostFiles:        uint64(0),
		NumStuckChunks:      uint64(0),
		NumSubDirs:          uint64(0),
		NumUnfinishedFiles:  uint64(0),
		RemoteHealth:        siadir.DefaultDirHealth,
		RepairSize:          uint64(0),
		Size:                uint64(0),
		StuckHealth:         siadir.DefaultDirHealth,
		StuckSize:           uint64(0),

		SkynetFiles: uint64(0),
		SkynetSize:  uint64(0),
	}
	// Read directory
	fileinfos, err := r.staticFileSystem.ReadDir(siaPath)
	if err != nil {
		r.staticLog.Printf("WARN: Error in reading files in directory %v : %v\n", siaPath.String(), err)
		return siadir.Metadata{}, err
	}

	// Iterate over directory and collect the file and dir siapaths.
	var fileSiaPaths, dirSiaPaths []skymodules.SiaPath
	for _, fi := range fileinfos {
		// Check to make sure renter hasn't been shutdown
		select {
		case <-r.tg.StopChan():
			return siadir.Metadata{}, err
		default:
		}
		// Sort by file and dirs.
		ext := filepath.Ext(fi.Name())
		if ext == skymodules.SiaFileExtension {
			// SiaFile found.
			fName := strings.TrimSuffix(fi.Name(), skymodules.SiaFileExtension)
			fileSiaPath, err := siaPath.Join(fName)
			if err != nil {
				r.staticLog.Println("unable to join siapath with dirpath while calculating directory metadata:", err)
				continue
			}
			fileSiaPaths = append(fileSiaPaths, fileSiaPath)
		} else if fi.IsDir() {
			// Directory is found, read the directory metadata file
			dirSiaPath, err := siaPath.Join(fi.Name())
			if err != nil {
				r.staticLog.Println("unable to join siapath with dirpath while calculating directory metadata:", err)
				continue
			}
			dirSiaPaths = append(dirSiaPaths, dirSiaPath)
		}
	}

	// Grab the Files' bubbleMetadata from the cached metadata first.
	//
	// Note: We don't need to abort on error. It's likely that only one or a few
	// files failed and that the remaining metadatas are good to use.
	bubbledMetadatas, err := r.managedCachedFileMetadatas(fileSiaPaths)
	if err != nil {
		r.staticLog.Printf("failed to calculate file metadata: %v", err)
	}

	// Get all the Directory Metadata
	//
	// Note: We don't need to abort on error. It's likely that only one or a few
	// directories failed and that the remaining metadatas are good to use.
	dirMetadatas, err := r.managedDirectoryMetadatas(dirSiaPaths)
	if err != nil {
		r.staticLog.Printf("failed to calculate file metadata: %v", err)
	}

	for len(bubbledMetadatas)+len(dirMetadatas) > 0 {
		// Aggregate Fields
		var aggregateHealth, aggregateRemoteHealth, aggregateStuckHealth, aggregateMinRedundancy float64
		var aggregateLastHealthCheckTime, aggregateModTime time.Time
		if len(bubbledMetadatas) > 0 {
			// Get next file's metadata.
			bubbledMetadata := bubbledMetadatas[0]
			bubbledMetadatas = bubbledMetadatas[1:]
			fileSiaPath := bubbledMetadata.sp
			fileMetadata := bubbledMetadata.bm

			// Check if the file is unfinished
			if !fileMetadata.Finished {
				// Check if the dependency for a shorted prune
				// duration is enabled. We rely on a dependency
				// instead of a build variable to reduce NDFs in
				// testing.
				if r.staticDeps.Disrupt("ShortUnfinishedFilesPruneDuration") {
					unfinishedFilePruneDuration = UnfinishedFilePruneDurationTestDeps
				}
				/*
					TODO: enable once portals have handled the current file updates.

					// Check if it is time to prune the file
						timeToPrune := time.Since(fileMetadata.CreateTime) > unfinishedFilePruneDuration
						if timeToPrune {
							// Delete the file if it is still unfinished after a month
							err := r.staticFileSystem.DeleteFile(fileSiaPath)
							if err != nil {
								r.staticLog.Printf("Unable to delete unfinished file at %v: %v", fileSiaPath, err)
							}
							// No need to call update on the directory after deleting
							// the file since we are in the process of updating the
							// directory.
							continue
						}
				*/
				// Update the unfinished metadata fields
				metadata.AggregateNumUnfinishedFiles++
				metadata.NumUnfinishedFiles++
				continue
			}

			// If 75% or more of the redundancy is missing, register an alert
			// for the file.
			uid := string(fileMetadata.UID)
			if maxHealth := math.Max(fileMetadata.Health, fileMetadata.StuckHealth); maxHealth >= AlertSiafileLowRedundancyThreshold {
				r.staticAlerter.RegisterAlert(modules.AlertIDSiafileLowRedundancy(uid), AlertMSGSiafileLowRedundancy,
					AlertCauseSiafileLowRedundancy(fileSiaPath, maxHealth, fileMetadata.Redundancy),
					modules.SeverityWarning)
			} else {
				r.staticAlerter.UnregisterAlert(modules.AlertIDSiafileLowRedundancy(uid))
			}

			// If the file's LastHealthCheckTime is still zero, set it as now since it
			// it currently being checked.
			//
			// The LastHealthCheckTime is not a field that is initialized when a file
			// is created, so we can reach this point by one of two ways. If a file is
			// created in the directory after the health loop has decided it needs to
			// be bubbled, or a file is created in a directory that gets a bubble
			// called on it outside of the health loop before the health loop as been
			// able to set the LastHealthCheckTime.
			if fileMetadata.LastHealthCheckTime.IsZero() {
				fileMetadata.LastHealthCheckTime = time.Now()
			}

			// Update repair fields
			metadata.AggregateRepairSize += fileMetadata.RepairBytes
			metadata.AggregateStuckSize += fileMetadata.StuckBytes
			metadata.RepairSize += fileMetadata.RepairBytes
			metadata.StuckSize += fileMetadata.StuckBytes

			// Check if the files is unrecoverable and should be considered lost
			if fileMetadata.Unrecoverable {
				metadata.NumLostFiles++
				metadata.AggregateNumLostFiles++
			}

			// Record Values that compare against sub directories
			aggregateHealth = fileMetadata.Health
			aggregateStuckHealth = fileMetadata.StuckHealth
			aggregateMinRedundancy = fileMetadata.Redundancy
			aggregateLastHealthCheckTime = fileMetadata.LastHealthCheckTime
			aggregateModTime = fileMetadata.ModTime
			if !fileMetadata.OnDisk {
				aggregateRemoteHealth = fileMetadata.Health
			}

			// Update aggregate fields.
			metadata.AggregateNumFiles++
			metadata.AggregateNumStuckChunks += fileMetadata.NumStuckChunks
			metadata.AggregateSize += fileMetadata.Size

			// Update siadir fields.
			metadata.Health = math.Max(metadata.Health, fileMetadata.Health)
			if fileMetadata.LastHealthCheckTime.Before(metadata.LastHealthCheckTime) {
				metadata.LastHealthCheckTime = fileMetadata.LastHealthCheckTime
			}
			if fileMetadata.Redundancy != -1 {
				metadata.MinRedundancy = math.Min(metadata.MinRedundancy, fileMetadata.Redundancy)
			}
			if fileMetadata.ModTime.After(metadata.ModTime) {
				metadata.ModTime = fileMetadata.ModTime
			}
			metadata.NumFiles++
			metadata.NumStuckChunks += fileMetadata.NumStuckChunks
			if !fileMetadata.OnDisk {
				metadata.RemoteHealth = math.Max(metadata.RemoteHealth, fileMetadata.Health)
			}
			metadata.Size += fileMetadata.Size
			metadata.StuckHealth = math.Max(metadata.StuckHealth, fileMetadata.StuckHealth)

			// Update Skynet Fields
			//
			// If the current directory is under the Skynet Folder, or the siafile
			// contains a skylink in the metadata, then we count the file towards the
			// Skynet Stats.
			//
			// For all cases we count the size.
			//
			// We only count the file towards the number of files if it is in the
			// skynet folder and is not extended. We do not count files outside of the
			// skynet folder because they should be treated as an extended file.
			isSkynetDir := skymodules.IsSkynetDir(siaPath)
			isExtended := strings.Contains(fileSiaPath.String(), skymodules.ExtendedSuffix)
			hasSkylinks := fileMetadata.NumSkylinks > 0
			if isSkynetDir || hasSkylinks {
				metadata.AggregateSkynetSize += fileMetadata.Size
				metadata.SkynetSize += fileMetadata.Size
			}
			if isSkynetDir && !isExtended {
				metadata.AggregateSkynetFiles++
				metadata.SkynetFiles++
			}
		} else if len(dirMetadatas) > 0 {
			// Get next dir's metadata.
			dirMetadata := dirMetadatas[0]
			dirMetadatas = dirMetadatas[1:]
			if dirMetadata.AggregateLastHealthCheckTime.IsZero() {
				dirMetadata.AggregateLastHealthCheckTime = time.Now()
				// Check for the dependency to disable the LastHealthCheckTime
				// correction, (LHCT = LastHealthCheckTime).
				if !r.staticDeps.Disrupt("DisableLHCTCorrection") {
					// Queue a bubble to bubble the directory, ignore the return channel
					// as we do not want to block on this update.
					r.staticLog.Debugf("Found zero time for ALHCT at '%v'", dirMetadata.sp)
					r.staticDirUpdateBatcher.callQueueDirUpdate(dirMetadata.sp)
				}
			}
			// Record Values that compare against files
			aggregateHealth = dirMetadata.AggregateHealth
			aggregateStuckHealth = dirMetadata.AggregateStuckHealth
			aggregateMinRedundancy = dirMetadata.AggregateMinRedundancy
			aggregateLastHealthCheckTime = dirMetadata.AggregateLastHealthCheckTime
			aggregateModTime = dirMetadata.AggregateModTime
			aggregateRemoteHealth = dirMetadata.AggregateRemoteHealth

			// Update aggregate fields.
			metadata.AggregateNumFiles += dirMetadata.AggregateNumFiles
			metadata.AggregateNumLostFiles += dirMetadata.AggregateNumLostFiles
			metadata.AggregateNumStuckChunks += dirMetadata.AggregateNumStuckChunks
			metadata.AggregateNumSubDirs += dirMetadata.AggregateNumSubDirs
			metadata.AggregateRepairSize += dirMetadata.AggregateRepairSize
			metadata.AggregateSize += dirMetadata.AggregateSize
			metadata.AggregateStuckSize += dirMetadata.AggregateStuckSize

			// Update aggregate Skynet fields
			metadata.AggregateSkynetFiles += dirMetadata.AggregateSkynetFiles
			metadata.AggregateSkynetSize += dirMetadata.AggregateSkynetSize

			// Add 1 to the AggregateNumSubDirs to account for this subdirectory.
			metadata.AggregateNumSubDirs++

			// Update siadir fields
			metadata.NumSubDirs++
		}
		// Track the max value of aggregate health values
		metadata.AggregateHealth = math.Max(metadata.AggregateHealth, aggregateHealth)
		metadata.AggregateRemoteHealth = math.Max(metadata.AggregateRemoteHealth, aggregateRemoteHealth)
		metadata.AggregateStuckHealth = math.Max(metadata.AggregateStuckHealth, aggregateStuckHealth)
		// Track the min value for AggregateMinRedundancy
		if aggregateMinRedundancy != -1 {
			metadata.AggregateMinRedundancy = math.Min(metadata.AggregateMinRedundancy, aggregateMinRedundancy)
		}
		// Update LastHealthCheckTime
		if aggregateLastHealthCheckTime.Before(metadata.AggregateLastHealthCheckTime) {
			metadata.AggregateLastHealthCheckTime = aggregateLastHealthCheckTime
		}
		// Update ModTime
		if aggregateModTime.After(metadata.AggregateModTime) {
			metadata.AggregateModTime = aggregateModTime
		}
	}

	// Sanity check on ModTime. If mod time is still zero it means there were no
	// files or subdirectories. Set ModTime to now since we just updated this
	// directory
	if metadata.AggregateModTime.IsZero() {
		metadata.AggregateModTime = time.Now()
	}
	if metadata.ModTime.IsZero() {
		metadata.ModTime = time.Now()
	}
	// Sanity check on Redundancy. If MinRedundancy is still math.MaxFloat64
	// then set it to -1 to indicate an empty directory
	if metadata.AggregateMinRedundancy == math.MaxFloat64 {
		metadata.AggregateMinRedundancy = -1
	}
	if metadata.MinRedundancy == math.MaxFloat64 {
		metadata.MinRedundancy = -1
	}

	return metadata, nil
}

// managedCachedFileMetadata returns the cached metadata information of
// a siafiles that needs to be bubbled.
func (r *Renter) managedCachedFileMetadata(siaPath skymodules.SiaPath) (bubbledSiaFileMetadata, error) {
	// Load the siafile.
	sf, err := r.staticFileSystem.OpenSiaFile(siaPath)
	if err != nil {
		return bubbledSiaFileMetadata{}, err
	}
	defer func() {
		err = errors.Compose(err, sf.Close())
	}()

	// First check if the fileNode is blocked. Blocking a file does not remove the
	// file so this is required to ensuring the node is purging blocked files.
	//
	// TODO: This delete/unpin code should be replaced with another system.
	if r.managedIsFileNodeBlocked(sf) && !r.staticDeps.Disrupt("DisableDeleteBlockedFiles") {
		// Delete the file
		r.staticLog.Println("Deleting blocked fileNode at:", siaPath)
		return bubbledSiaFileMetadata{}, errors.Compose(r.staticFileSystem.DeleteFile(siaPath), ErrSkylinkBlocked)
	}
	// Check if there is a pending unpin request
	if r.staticSkylinkManager.callIsUnpinned(sf) {
		// Delete the file
		r.staticLog.Println("Deleting unpinned fileNode at:", siaPath)
		return bubbledSiaFileMetadata{}, errors.Compose(r.staticFileSystem.DeleteFile(siaPath), ErrSkylinkUnpinned)
	}

	// Check if original file is on disk
	md := sf.Metadata()
	_, err = os.Stat(md.LocalPath)
	onDisk := err == nil

	// Check if file is unrecoverable and log it
	maxHealth := math.Max(md.CachedHealth, md.CachedStuckHealth)
	unrecoverable := siafile.Unrecoverable(maxHealth, onDisk)
	if unrecoverable {
		r.staticLog.Debugf("File not found on disk and possibly unrecoverable: LocalPath %v; SiaPath %v", sf.LocalPath(), siaPath.String())
	}

	// Return the metadata
	return bubbledSiaFileMetadata{
		sp: siaPath,
		bm: siafile.BubbledMetadata{
			CreateTime:          md.CreateTime,
			Finished:            md.Finished,
			Health:              md.CachedHealth,
			LastHealthCheckTime: md.LastHealthCheckTime,
			ModTime:             md.ModTime,
			NumSkylinks:         uint64(len(md.Skylinks)),
			NumStuckChunks:      md.NumStuckChunks,
			OnDisk:              onDisk,
			Redundancy:          md.CachedRedundancy,
			RepairBytes:         md.CachedRepairBytes,
			Size:                uint64(md.FileSize),
			StuckHealth:         md.CachedStuckHealth,
			StuckBytes:          md.CachedStuckBytes,
			UID:                 md.UniqueID,
			Unrecoverable:       unrecoverable,
		},
	}, nil
}

// managedCachedFileMetadatas returns the cahced metadata information of
// multiple siafiles that need to be bubbled. Usually the return value of
// a method is ignored when the returned error != nil. For
// managedCachedFileMetadatas we make an exception. The caller can decide
// themselves whether to use the output in case of an error or not.
func (r *Renter) managedCachedFileMetadatas(siaPaths []skymodules.SiaPath) (_ []bubbledSiaFileMetadata, err error) {
	// Define components
	mds := make([]bubbledSiaFileMetadata, 0, len(siaPaths))
	siaPathChan := make(chan skymodules.SiaPath, numBubbleWorkerThreads)
	var errs error
	var errMu, mdMu sync.Mutex

	// Create function for loading SiaFiles and calculating the metadata
	metadataWorker := func() {
		for siaPath := range siaPathChan {
			md, err := r.managedCachedFileMetadata(siaPath)
			if errors.Contains(err, ErrSkylinkBlocked) {
				// If the fileNode is blocked we ignore the error and continue.
				continue
			}
			if errors.Contains(err, ErrSkylinkUnpinned) {
				// If the fileNode is unpinned we ignore the error and continue.
				continue
			}
			if err != nil {
				errMu.Lock()
				errs = errors.Compose(errs, err)
				errMu.Unlock()
				continue
			}
			mdMu.Lock()
			mds = append(mds, md)
			mdMu.Unlock()
		}
	}

	// Launch Metadata workers
	var wg sync.WaitGroup
	for i := 0; i < numBubbleWorkerThreads; i++ {
		wg.Add(1)
		go func() {
			metadataWorker()
			wg.Done()
		}()
	}
	for _, siaPath := range siaPaths {
		select {
		case siaPathChan <- siaPath:
		case <-r.tg.StopChan():
			close(siaPathChan)
			wg.Wait()
			return nil, errors.AddContext(errs, "renter shutdown")
		}
	}
	close(siaPathChan)
	wg.Wait()
	return mds, errs
}

// managedDirectoryMetadatas returns all the metadatas of the SiaDirs for the
// provided siaPaths
func (r *Renter) managedDirectoryMetadatas(siaPaths []skymodules.SiaPath) ([]bubbledSiaDirMetadata, error) {
	// Define components
	mds := make([]bubbledSiaDirMetadata, 0, len(siaPaths))
	siaPathChan := make(chan skymodules.SiaPath, numBubbleWorkerThreads)
	var errs error
	var errMu, mdMu sync.Mutex

	// Create function for getting the directory metadata
	metadataWorker := func() {
		for siaPath := range siaPathChan {
			md, err := r.managedDirectoryMetadata(siaPath)
			if err != nil {
				errMu.Lock()
				errs = errors.Compose(errs, err)
				errMu.Unlock()
				continue
			}
			mdMu.Lock()
			mds = append(mds, bubbledSiaDirMetadata{
				siaPath,
				md,
			})
			mdMu.Unlock()
		}
	}

	// Launch Metadata workers
	var wg sync.WaitGroup
	for i := 0; i < numBubbleWorkerThreads; i++ {
		wg.Add(1)
		go func() {
			metadataWorker()
			wg.Done()
		}()
	}
	for _, siaPath := range siaPaths {
		select {
		case siaPathChan <- siaPath:
		case <-r.tg.StopChan():
			close(siaPathChan)
			wg.Wait()
			return nil, errors.AddContext(errs, "renter shutdown")
		}
	}
	close(siaPathChan)
	wg.Wait()
	return mds, errs
}

// managedDirectoryMetadata reads the directory metadata and returns the bubble
// metadata
func (r *Renter) managedDirectoryMetadata(siaPath skymodules.SiaPath) (_ siadir.Metadata, err error) {
	// Check for bad paths and files
	fi, err := r.staticFileSystem.Stat(siaPath)
	if err != nil {
		return siadir.Metadata{}, err
	}
	if !fi.IsDir() {
		return siadir.Metadata{}, fmt.Errorf("%v is not a directory", siaPath)
	}

	//  Open SiaDir
	siaDir, err := r.staticFileSystem.OpenSiaDirCustom(siaPath, true)
	if err != nil {
		return siadir.Metadata{}, err
	}
	defer func() {
		err = errors.Compose(err, siaDir.Close())
	}()

	// Grab the metadata.
	return siaDir.Metadata()
}
