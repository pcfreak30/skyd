package contractmanager

import (
	"errors"
	"sync"
	"sync/atomic"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
)

// applyUpdateSector will commit a sector update to the contract manager,
// writing in metadata and usage info if the sector still exists, and deleting
// the usage info if the sector does not exist. The update is idempotent.
func (cm *ContractManager) applyUpdateSector(su sectorUpdate) {
	sf, exists := cm.storageFolders[su.Folder]
	if !exists || atomic.LoadUint64(&sf.atomicUnavailable) == 1 {
		cm.log.Printf("ERROR: unable to locate storage folder for a committed sector update.")
		return
	}

	// If the sector is being cleaned from disk, unset the usage flag.
	if su.Count == 0 {
		sf.clearUsage(su.Index)
		return
	}

	// Set the usage flag and update the on-disk metadata. Abort if the
	// metadata write fails.
	err := cm.writeSectorMetadata(sf, su)
	if err != nil {
		cm.log.Printf("ERROR: unable to write sector metadata for %v: %v\n", sf.path, err)
		return
	}
	sf.setUsage(su.Index)
}

// managedAddPhysicalSector is a WAL operation to add a physical sector to the
// contract manager.
func (cm *ContractManager) managedAddPhysicalSector(id sectorID, data []byte, count uint16) error {
	// Sanity check - data should have modules.SectorSize bytes.
	if uint64(len(data)) != modules.SectorSize {
		cm.log.Critical("sector has the wrong size", modules.SectorSize, len(data))
		return errors.New("malformed sector")
	}

	// Find a committed storage folder that has enough space to receive
	// this sector. Keep trying new storage folders if some return
	// errors during disk operations.
	cm.mu.Lock()
	storageFolders := cm.availableStorageFolders()
	cm.mu.Unlock()
	for len(storageFolders) >= 1 {
		var storageFolderIndex int
		err := func() (err error) {
			// NOTE: Convention is broken when working with WAL lock here, due
			// to the complexity required with managing both the WAL lock and
			// the storage folder lock. Pay close attention when reviewing and
			// modifying.

			// Grab a vacant storage folder.
			cm.mu.Lock()
			var sf *storageFolder
			sf, storageFolderIndex = vacancyStorageFolder(storageFolders)
			if sf == nil {
				// None of the storage folders have enough room to house the
				// sector.
				cm.mu.Unlock()
				return errors.New(modules.V1420HostOutOfStorageErrString)
			}
			defer sf.mu.RUnlock()

			// Grab a sector from the storage folder. WAL lock cannot be
			// released between grabbing the storage folder and grabbing a
			// sector lest another thread request the final available sector in
			// the storage folder.
			sectorIndex, err := randFreeSector(sf.usage)
			if err != nil {
				cm.mu.Unlock()
				cm.log.Critical("a storage folder with full usage was returned from emptiestStorageFolder")
				return err
			}
			// Set the usage, but mark it as uncommitted.
			sf.setUsage(sectorIndex)
			sf.availableSectors[id] = sectorIndex
			cm.mu.Unlock()

			// The usage has been set, in the event of failure the usage
			// must be cleared.
			defer func() {
				if err != nil {
					cm.mu.Lock()
					sf.clearUsage(sectorIndex)
					delete(sf.availableSectors, id)
					cm.mu.Unlock()
				}
			}()

			// Prepare writing the new sector to disk.
			sectorDataUpdate := sectorDataUpdate(path, sectorIndex, data)

			// Prepare writing the sector metadata to disk.
			su := sectorUpdate{
				Count:  count,
				ID:     id,
				Folder: sf.index,
				Index:  sectorIndex,
			}
			sectorMetadataUpdate := sectorMetadataUpdate(sf, su)

			// Apply updates.
			if err := cm.createAndApplyTransaction(sectorDataUpdate, sectorMetadataUpdate); err != nil {
				return err
			}

			// Sector added successfully, update the WAL and the state.
			sl := sectorLocation{
				index:         sectorIndex,
				storageFolder: sf.index,
				count:         count,
			}
			cm.mu.Lock()
			delete(cm.storageFolders[su.Folder].availableSectors, id)
			cm.sectorLocations[id] = sl
			cm.mu.Unlock()
			return nil
		}()
		if err != nil {
			// End the loop if no storage folder proved suitable.
			if storageFolderIndex == -1 {
				storageFolders = nil
				break
			}

			// Remove the storage folder that failed and try the next one.
			storageFolders = append(storageFolders[:storageFolderIndex], storageFolders[storageFolderIndex+1:]...)
			continue
		}
		// Sector added successfully, break.
		break
	}
	if len(storageFolders) < 1 {
		return errors.New(modules.V1420HostOutOfStorageErrString)
	}
	return nil
}

// managedAddVirtualSector will add a virtual sector to the contract manager.
func (cm *ContractManager) managedAddVirtualSector(id sectorID, location sectorLocation) error {
	// Update the location count.
	if location.count == 65535 {
		return errMaxVirtualSectors
	}
	location.count++

	// Prepare the sector update.
	su := sectorUpdate{
		Count:  location.count,
		ID:     id,
		Folder: location.storageFolder,
		Index:  location.index,
	}

	// Append the sector update to the WAL.
	cm.mu.Lock()
	sf, exists := cm.storageFolders[su.Folder]
	if !exists || atomic.LoadUint64(&sf.atomicUnavailable) == 1 {
		// Need to check that the storage folder exists before syncing the
		// commit that increases the virtual sector count.
		cm.mu.Unlock()
		return errStorageFolderNotFound
	}
	wal.appendChange(stateChange{
		SectorUpdates: []sectorUpdate{su},
	})
	cm.sectorLocations[id] = location
	cm.mu.Unlock()

	// Update the metadata on disk. Metadata is updated on disk after the sync
	// so that there is no risk of obliterating the previous count in the event
	// that the change is not fully committed during unclean shutdown.
	err := cm.writeSectorMetadata(sf, su)
	if err != nil {
		// Revert the sector update in the WAL to reflect the fact that adding
		// the sector has failed.
		su.Count--
		location.count--
		cm.mu.Lock()
		wal.appendChange(stateChange{
			SectorUpdates: []sectorUpdate{su},
		})
		cm.sectorLocations[id] = location
		cm.mu.Unlock()
		return build.ExtendErr("unable to write sector metadata during addSector call", err)
	}
	return nil
}

// managedDeleteSector will delete a sector (physical) from the contract manager.
func (cm *ContractManager) managedDeleteSector(id sectorID) error {
	// Write the sector delete to the WAL.
	var location sectorLocation
	var sf *storageFolder
	err := func() error {
		cm.mu.Lock()
		defer cm.mu.Unlock()

		// Fetch the metadata related to the sector.
		var exists bool
		location, exists = cm.sectorLocations[id]
		if !exists {
			return ErrSectorNotFound
		}
		sf, exists = cm.storageFolders[location.storageFolder]
		if !exists || atomic.LoadUint64(&sf.atomicUnavailable) == 1 {
			cm.log.Critical("deleting a sector from a storage folder that does not exist?")
			return errStorageFolderNotFound
		}

		// Inform the WAL of the sector update.
		wal.appendChange(stateChange{
			SectorUpdates: []sectorUpdate{{
				Count:  0,
				ID:     id,
				Folder: location.storageFolder,
				Index:  location.index,
			}},
		})

		// Delete the sector and mark the usage as available.
		delete(cm.sectorLocations, id)
		sf.availableSectors[id] = location.index

		// Block until the change has been committed.
		return nil
	}()
	if err != nil {
		return err
	}

	// Only update the usage after the sector delete has been committed to disk
	// fully.
	cm.mu.Lock()
	delete(sf.availableSectors, id)
	sf.clearUsage(location.index)
	cm.mu.Unlock()
	return nil
}

// managedRemoveSector will remove a sector (virtual or physical) from the
// contract manager.
func (cm *ContractManager) managedRemoveSector(id sectorID) error {
	// Inform the WAL of the removed sector.
	var location sectorLocation
	var su sectorUpdate
	var sf *storageFolder
	err := func() error {
		cm.mu.Lock()
		defer cm.mu.Unlock()

		// Grab the number of virtual sectors that have been committed with
		// this root.
		var exists bool
		location, exists = cm.sectorLocations[id]
		if !exists {
			return ErrSectorNotFound
		}
		sf, exists = cm.storageFolders[location.storageFolder]
		if !exists || atomic.LoadUint64(&sf.atomicUnavailable) == 1 {
			cm.log.Critical("deleting a sector from a storage folder that does not exist?")
			return errStorageFolderNotFound
		}

		// Inform the WAL of the sector update.
		location.count--
		su = sectorUpdate{
			Count:  location.count,
			ID:     id,
			Folder: location.storageFolder,
			Index:  location.index,
		}
		wal.appendChange(stateChange{
			SectorUpdates: []sectorUpdate{su},
		})

		// Update the in-memeory representation of the sector.
		if location.count == 0 {
			// Delete the sector and mark it as available.
			delete(cm.sectorLocations, id)
			sf.availableSectors[id] = location.index
		} else {
			// Reduce the sector usage.
			cm.sectorLocations[id] = location
		}
		return nil
	}()
	if err != nil {
		return err
	}

	// Update the metadata, and the usage.
	if location.count != 0 {
		err = cm.writeSectorMetadata(sf, su)
		if err != nil {
			// Revert the previous change.
			cm.mu.Lock()
			su.Count++
			location.count++
			wal.appendChange(stateChange{
				SectorUpdates: []sectorUpdate{su},
			})
			cm.sectorLocations[id] = location
			cm.mu.Unlock()
			return build.ExtendErr("failed to write sector metadata", err)
		}
	}

	// Only update the usage after the sector removal has been committed to
	// disk entirely. The usage is not updated until after the commit has
	// completed to prevent the actual sector data from being overwritten in
	// the event of unclean shutdown.
	if location.count == 0 {
		cm.mu.Lock()
		sf.clearUsage(location.index)
		delete(sf.availableSectors, id)
		cm.mu.Unlock()
	}
	return nil
}

// writeSectorMetadata will take a sector update and write the related metadata
// to disk.
func (cm *ContractManager) writeSectorMetadata(sf *storageFolder, su sectorUpdate) error {
	err := writeSectorMetadata(sf.metadataFile, su.Index, su.ID, su.Count)
	if err != nil {
		cm.log.Printf("ERROR: unable to write sector metadata to folder %v when adding sector: %v\n", su.Folder, err)
		atomic.AddUint64(&sf.atomicFailedWrites, 1)
		return err
	}
	atomic.AddUint64(&sf.atomicSuccessfulWrites, 1)
	return nil
}

// AddSector will add a sector to the contract manager.
func (cm *ContractManager) AddSector(root crypto.Hash, sectorData []byte) error {
	var registerHostDiskTrouble bool
	defer func() {
		if registerHostDiskTrouble {
			cm.staticAlerter.RegisterAlert(modules.AlertIDHostDiskTrouble, AlertMSGHostDiskTrouble, "", modules.SeverityCritical)
		}
	}()

	// Prevent shutdown until this function completes.
	err := cm.tg.Add()
	if err != nil {
		return err
	}
	defer cm.tg.Done()

	// Allow disk trouble simulation, for testing purposes
	if cm.dependencies.Disrupt("diskTrouble") {
		cm.staticAlerter.RegisterAlert(modules.AlertIDHostDiskTrouble, AlertMSGHostDiskTrouble, "", modules.SeverityCritical)
		return errDiskTrouble
	}

	// Hold a sector lock throughout the duration of the function, but release
	// before syncing.
	id := cm.managedSectorID(root)
	cm.managedLockSector(id)
	defer cm.managedUnlockSector(id)

	// Determine whether the sector is virtual or physical.
	cm.mu.Lock()
	location, exists := cm.sectorLocations[id]
	cm.mu.Unlock()
	if exists {
		err = cm.managedAddVirtualSector(id, location)
	} else {
		err = cm.managedAddPhysicalSector(id, sectorData, 1)
	}
	if err == errDiskTrouble {
		cm.staticAlerter.RegisterAlert(modules.AlertIDHostDiskTrouble, AlertMSGHostDiskTrouble, "", modules.SeverityCritical)
	}
	if err != nil {
		cm.log.Println("ERROR: Unable to add sector:", err)
		return err
	}
	return nil
}

// AddSectorBatch is a non-ACID call to add a bunch of sectors at once.
// Necessary for compatibility with old renters.
//
// TODO: Make ACID, and definitely improve the performance as well.
func (cm *ContractManager) AddSectorBatch(sectorRoots []crypto.Hash) error {
	// Prevent shutdown until this function completes.
	err := cm.tg.Add()
	if err != nil {
		return err
	}
	defer cm.tg.Done()

	go func() {
		// Ensure only 'maxSectorBatchThreads' goroutines are running at a time.
		semaphore := make(chan struct{}, maxSectorBatchThreads)
		for _, root := range sectorRoots {
			semaphore <- struct{}{}
			go func(root crypto.Hash) {
				defer func() {
					<-semaphore
				}()

				// Hold a sector lock throughout the duration of the function, but release
				// before syncing.
				id := cm.managedSectorID(root)
				cm.managedLockSector(id)
				defer cm.managedUnlockSector(id)

				// Add the sector as virtual.
				cm.mu.Lock()
				location, exists := cm.sectorLocations[id]
				cm.mu.Unlock()
				if exists {
					cm.managedAddVirtualSector(id, location)
				}
			}(root)
		}
	}()
	return nil
}

// DeleteSector will delete a sector from the contract manager. If multiple
// copies of the sector exist, all of them will be removed. This should only be
// used to remove offensive data, as it will cause corruption in the contract
// manager. This corruption puts the contract manager at risk of failing
// storage proofs. If the amount of data removed is small, the risk is small.
// This operation will not destabilize the contract manager.
func (cm *ContractManager) DeleteSector(root crypto.Hash) error {
	err := cm.tg.Add()
	if err != nil {
		return err
	}
	defer cm.tg.Done()
	id := cm.managedSectorID(root)
	cm.managedLockSector(id)
	defer cm.managedUnlockSector(id)

	return cm.managedDeleteSector(id)
}

// RemoveSector will remove a sector from the contract manager. If multiple
// copies of the sector exist, only one will be removed.
func (cm *ContractManager) RemoveSector(root crypto.Hash) error {
	err := cm.tg.Add()
	if err != nil {
		return err
	}
	defer cm.tg.Done()
	id := cm.managedSectorID(root)
	cm.managedLockSector(id)
	defer cm.managedUnlockSector(id)

	return cm.managedRemoveSector(id)
}

// RemoveSectorBatch is a non-ACID call to remove a bunch of sectors at once.
// Necessary for compatibility with old renters.
//
// TODO: Make ACID, and definitely improve the performance as well.
func (cm *ContractManager) RemoveSectorBatch(sectorRoots []crypto.Hash) error {
	// Prevent shutdown until this function completes.
	err := cm.tg.Add()
	if err != nil {
		return err
	}
	defer cm.tg.Done()

	// Add each sector in a separate goroutine.
	var wg sync.WaitGroup
	// Ensure only 'maxSectorBatchThreads' goroutines are running at a time.
	semaphore := make(chan struct{}, maxSectorBatchThreads)
	for _, root := range sectorRoots {
		wg.Add(1)
		semaphore <- struct{}{}
		go func(root crypto.Hash) {
			id := cm.managedSectorID(root)
			cm.managedLockSector(id)
			cm.managedRemoveSector(id) // Error is ignored.
			cm.managedUnlockSector(id)
			<-semaphore
			wg.Done()
		}(root)
	}
	wg.Wait()
	return nil
}
