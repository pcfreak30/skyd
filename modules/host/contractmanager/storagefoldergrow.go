package contractmanager

import (
	"errors"
	"sync"
	"sync/atomic"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules"
)

var errIncompleteGrowFolder = errors.New("incompleteGrowStorageFolder")

// commitStorageFolderExtension will apply a storage folder extension to the
// state.
func (cm *ContractManager) managedCommitStorageFolderExtension(index uint16, newSectorCount uint32) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	sf, exists := cm.storageFolders[index]
	if !exists || atomic.LoadUint64(&sf.atomicUnavailable) == 1 {
		cm.log.Critical("ERROR: storage folder extension provided for storage folder that does not exist")
		return
	}

	newUsageSize := newSectorCount / storageFolderGranularity
	appendUsage := make([]uint64, int(newUsageSize)-len(sf.usage))
	sf.usage = append(sf.usage, appendUsage...)
}

// managedGrowStorageFolder will extend the storage folder files so that they
// may hold more sectors.
func (cm *ContractManager) managedGrowStorageFolder(index uint16, newSectorCount uint32) error {
	// Retrieve the specified storage folder.
	cm.mu.Lock()
	sf, exists := cm.storageFolders[index]
	cm.mu.Unlock()
	if !exists || atomic.LoadUint64(&sf.atomicUnavailable) == 1 {
		return errStorageFolderNotFound
	}

	// Simulate power failure at this point for some testing scenarios. The
	// growth operation has been added to the WAL but was not yet executed.
	if cm.dependencies.Disrupt("incompleteGrowStorageFolder") {
		return errIncompleteGrowFolder
	}

	// Lock the storage folder for the duration of the operation.
	sf.mu.Lock()
	defer sf.mu.Unlock()

	// Write the intention to increase the storage folder size to the WAL,
	// providing enough information to allow a truncation if the growing fails.
	oldSectorCount := uint32(len(sf.usage)) * storageFolderGranularity

	// Prepare variables for growing the storage folder.
	currentHousingSize := int64(len(sf.usage)) * int64(modules.SectorSize) * storageFolderGranularity
	currentMetadataSize := int64(len(sf.usage)) * sectorMetadataDiskSize * storageFolderGranularity
	newHousingSize := int64(newSectorCount) * int64(modules.SectorSize)
	newMetadataSize := int64(newSectorCount) * sectorMetadataDiskSize
	if newHousingSize <= currentHousingSize || newMetadataSize <= currentMetadataSize {
		cm.log.Critical("growStorageFolder called without size increase", newHousingSize, currentHousingSize, newMetadataSize, currentMetadataSize)
		return errors.New("unable to make the requested change, please notify the devs that there is a bug")
	}
	housingWriteSize := newHousingSize - currentHousingSize
	metadataWriteSize := newMetadataSize - currentMetadataSize

	// If there's an error in the rest of the function, reset the storage
	// folders to their original size.
	var err error
	defer func(sf *storageFolder, housingSize, metadataSize int64) {
		if err != nil {
			// Remove the leftover files from the failed operation.
			err = build.ComposeErrors(err, sf.metadataFile.Truncate(housingSize))
			err = build.ComposeErrors(err, sf.sectorFile.Truncate(metadataSize))
			_, errEmpty := cm.managedEmptyStorageFolder(index, oldSectorCount)
			err = build.ComposeErrors(err, errEmpty)
		}
	}(sf, currentMetadataSize, currentHousingSize)

	// Extend the sector file and metadata file on disk.
	atomic.StoreUint64(&sf.atomicProgressDenominator, uint64(housingWriteSize+metadataWriteSize))

	stepCount := housingWriteSize / folderAllocationStepSize
	for i := int64(0); i < stepCount; i++ {
		err = sf.sectorFile.Truncate(currentHousingSize + (folderAllocationStepSize * (i + 1)))
		if err != nil {
			return err
		}
		// After each iteration, update the progress numerator.
		// TODO: this is no longer accurate
		atomic.AddUint64(&sf.atomicProgressNumerator, folderAllocationStepSize)
	}
	err = sf.sectorFile.Truncate(currentHousingSize + housingWriteSize)
	if err != nil {
		return err
	}

	// Write the metadata file.
	err = sf.metadataFile.Truncate(currentMetadataSize + metadataWriteSize)
	if err != nil {
		return err
	}

	// The file creation process is essentially complete at this point, report
	// complete progress.
	atomic.StoreUint64(&sf.atomicProgressNumerator, uint64(housingWriteSize+metadataWriteSize))

	// Sync the files.
	var err1, err2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		err1 = sf.metadataFile.Sync()
		if err != nil {
			cm.log.Println("could not synchronize allocated sector metadata file:", err)
		}
	}()
	go func() {
		defer wg.Done()
		err2 = sf.sectorFile.Sync()
		if err != nil {
			cm.log.Println("could not synchronize allocated sector data file:", err)
		}
	}()
	wg.Wait()
	if err1 != nil || err2 != nil {
		err = build.ComposeErrors(err1, err2)
		cm.log.Println("cound not synchronize storage folder extensions:", err)
		return build.ExtendErr("unable to synchronize storage folder extensions", err)
	}

	// Storage folder growth has completed successfully.
	cm.mu.Lock()
	cm.storageFolders[sf.index] = sf
	cm.mu.Unlock()

	// Set the progress back to '0'.
	atomic.StoreUint64(&sf.atomicProgressNumerator, 0)
	atomic.StoreUint64(&sf.atomicProgressDenominator, 0)
	return nil
}
