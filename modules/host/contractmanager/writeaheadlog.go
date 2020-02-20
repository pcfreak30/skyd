package contractmanager

import (
	"fmt"
	"path/filepath"

	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/writeaheadlog"
)

var (
	addStorageFolderUpdateName    = "AddStorageFolderUpdate"
	addPhysicalSectorUpdateName   = "AddPhysicalSectorUpdate"
	addVirtualSectorUpdateName    = "AddVirtualSectorUpdate"
	removeStorageFolderUpdateName = "RemoveStorageFolderUpdate"
	growStorageFolderUpdateName   = "GrowStorageFolderUpdate"
	shrinkStorageFolderUpdateName = "ShrinkStorageFolderUpdate"
	deleteSectorUpdateName        = "DeleteSectorUpdate"
	removeSectorUpdateName        = "RemoveSectorUpdate"
)

type (
	// sectorUpdate is an idempotent update to the sector metadata.
	sectorUpdate struct {
		Count  uint16
		Folder uint16
		ID     sectorID
		Index  uint32
	}
	// walUpdate wraps a writeaheadlog.Update and adds a file to be able to
	// reuse open file handles when applying the update.
	walUpdate struct {
		writeaheadlog.Update
		f modules.File
	}
)

// addStorageFolderUpdate creates a WAL update for adding a new storage folder.
func addStorageFolderUpdate(sf *storageFolder) walUpdate {
	return walUpdate{
		writeaheadlog.Update{
			Name:         addStorageFolderUpdateName,
			Instructions: encoding.MarshalAll(sf.path, uint64(len(sf.usage))),
		},
		nil,
	}
}

func addPhysicalSectorUpate(id sectorID, data []byte, count uint16) walUpdate {
	return walUpdate{
		writeaheadlog.Update{
			Name:         addPhysicalSectorUpdateName,
			Instructions: encoding.MarshalAll(id, data, count),
		},
		nil,
	}
}

func addVirtualSectorUpate(id sectorID, location sectorLocation) walUpdate {
	return walUpdate{
		writeaheadlog.Update{
			Name:         addVirtualSectorUpdateName,
			Instructions: encoding.MarshalAll(id, location),
		},
		nil,
	}
}

func deleteSectorUpdate(id sectorID) walUpdate {
	return walUpdate{
		writeaheadlog.Update{
			Name:         deleteSectorUpdateName,
			Instructions: encoding.MarshalAll(id),
		},
		nil,
	}
}

func removeSectorUpdate(id sectorID) walUpdate {
	return walUpdate{
		writeaheadlog.Update{
			Name:         removeSectorUpdateName,
			Instructions: encoding.MarshalAll(id),
		},
		nil,
	}
}

// truncateUpdate creates a WAL update for the writeaheadlog which truncates a
// file to the specified size.
func truncateUpdate(file modules.File, path string, newSize int64) walUpdate {
	return walUpdate{
		writeaheadlog.TruncateUpdate(path, newSize),
		file,
	}
}

// removeStorageFolderUpdate creates a WAL update for emptying out a storage
// folder on disk.
func removeStorageFolderUpdate(index uint16, path string) walUpdate {
	return walUpdate{
		writeaheadlog.Update{
			Name:         removeStorageFolderUpdateName,
			Instructions: encoding.MarshalAll(index, path),
		},
		nil, // no file needed
	}
}

// growStorageFolderUpdate creates a WAL update for growing out a storage
// folder on disk.
func growStorageFolderUpdate(index uint16, newSectorCount uint32) walUpdate {
	return walUpdate{
		writeaheadlog.Update{
			Name:         growStorageFolderUpdateName,
			Instructions: encoding.MarshalAll(index, newSectorCount),
		},
		nil, // no file needed
	}
}

// shrinkStorageFolderUpdate creates a WAL update for shrinking a storage folder
// on disk.
func shrinkStorageFolderUpdate(index uint16, startingPoint uint32, force bool) walUpdate {
	return walUpdate{
		writeaheadlog.Update{
			Name:         shrinkStorageFolderUpdateName,
			Instructions: encoding.MarshalAll(index, startingPoint, force),
		},
		nil, // no file needed
	}
}

// applyUpdates applies the provided updates one by one.
func (cm *ContractManager) applyUpdates(updates ...walUpdate) error {
	for _, update := range updates {
		var err error
		switch update.Name {
		case addStorageFolderUpdateName:
			err = cm.applyAddStorageFolderUpdate(update)
		case addPhysicalSectorUpdateName:
			err = cm.applyAddPhysicalSectorUpdate(update)
		case addVirtualSectorUpdateName:
			err = cm.applyAddVirtualSectorUpdate(update)
		case removeStorageFolderUpdateName:
			err = cm.applyRemoveStorageFolderUpdate(update)
		case shrinkStorageFolderUpdateName:
			err = cm.applyShrinkStorageFolderUpdate(update)
		case growStorageFolderUpdateName:
			err = cm.applyGrowStorageFolderUpdate(update)
		case deleteSectorUpdateName:
			err = cm.applyDeleteSectorUpdate(update)
		case removeSectorUpdateName:
			err = cm.applyRemoveSectorUpdate(update)
		}
		if err != nil {
			return errors.AddContext(err, "applyUpdates:")
		}
	}
	// Update settings after updates are applied.
	return cm.saveSettings()
}

// createAndApplyTransaction will create a transaction from the provided updates
// and try to apply them in order.
func (cm *ContractManager) createAndApplyTransaction(updates ...walUpdate) error {
	// Create the writeaheadlog transaction.
	wUpdates := make([]writeaheadlog.Update, 0, len(updates))
	for _, update := range updates {
		wUpdates = append(wUpdates, update.Update)
	}
	txn, err := cm.staticWal.NewTransaction(wUpdates)
	if err != nil {
		return errors.AddContext(err, "failed to create wal txn")
	}
	// No extra setup is required. Signal that it is done.
	if err := <-txn.SignalSetupComplete(); err != nil {
		return errors.AddContext(err, "failed to signal setup completion")
	}
	// Apply the updates.
	if err := cm.applyUpdates(updates...); err != nil {
		return errors.AddContext(err, "failed to apply updates")
	}
	// Updates are applied. Let the writeaheadlog know.
	if err := txn.SignalUpdatesApplied(); err != nil {
		return errors.AddContext(err, "failed to signal that updates are applied")
	}
	return nil
}

// applyAddStorageFolderUpdate applies an update which adds a storage folder to
// the contract manager.
func (cm *ContractManager) applyAddStorageFolderUpdate(update walUpdate) error {
	if update.Name != addStorageFolderUpdateName {
		return fmt.Errorf("can't call applyAddStorageFolderUpdate on '%v' update", update.Name)
	}
	// Decode the instructions.
	var path string
	var usageLength uint64
	err := encoding.UnmarshalAll(update.Instructions, &path, &usageLength)
	if err != nil {
		return errors.AddContext(err, "failed to unmarshal addStorageFolderUpdate instructions")
	}
	return cm.managedAddStorageFolder(&storageFolder{
		path:  path,
		usage: make([]uint64, usageLength),

		availableSectors: make(map[sectorID]uint32),
	})
}

// applyEmptyStorageFolderUpdate applies an update to empty a sector's storage
// folder.
func (cm *ContractManager) applyRemoveStorageFolderUpdate(update walUpdate) error {
	if update.Name != removeStorageFolderUpdateName {
		return fmt.Errorf("can't call applyEmptyStorageFolderUpdate on '%v' update", update.Name)
	}
	// Decode the instructions
	var index uint16
	var path string
	err := encoding.UnmarshalAll(update.Instructions, &index, &path)
	if err != nil {
		return errors.AddContext(err, "failed to unmarshal emptyStorageFolderUpdate instructions")
	}
	// Empty storage folder.
	_, err = cm.managedEmptyStorageFolder(index, 0)
	if err != nil {
		cm.log.Printf("ERROR: Unable to empty storage folder %v: %v\n", index, err)
		// atomic.AddUint64(&sf.atomicFailedWrites, 1) // TODO: move to caller
		return errors.AddContext(err, fmt.Sprintf("failed to empty storage folder at index %v", index))
	}
	// Commit the state.
	cm.commitStorageFolderRemoval(index, path)
	return nil
}

// applyShrinkStorageFolderUpdate applies an update to shrink a sector's storage
// folder.
func (cm *ContractManager) applyShrinkStorageFolderUpdate(update walUpdate) error {
	if update.Name != shrinkStorageFolderUpdateName {
		return fmt.Errorf("can't call applyShrinkStorageFolderUpdate on '%v' update", update.Name)
	}
	// Decode the instructions
	var index uint16
	var newSectorCount uint32
	var force bool
	err := encoding.UnmarshalAll(update.Instructions, &index, &newSectorCount, &force)
	if err != nil {
		return errors.AddContext(err, "failed to unmarshal shrinkStorageFolderUpdate instructions")
	}
	// Empty storage folder.
	_, err = cm.managedEmptyStorageFolder(index, newSectorCount)
	if err != nil && !force {
		cm.log.Printf("ERROR: Unable to shrink storage folder %v: %v\n", index, err)
		// atomic.AddUint64(&sf.atomicFailedWrites, 1) // TODO: move to caller
		return errors.AddContext(err, fmt.Sprintf("failed to shrink storage folder at index %v", index))
	}
	// Commit the change to the state.
	cm.commitStorageFolderReduction(index, newSectorCount)
	return nil
}

// growStorageFolderUpdate applies an update to grow a sector's storage
// folder.
func (cm *ContractManager) applyGrowStorageFolderUpdate(update walUpdate) error {
	if update.Name != growStorageFolderUpdateName {
		return fmt.Errorf("can't call applyGrowStorageFolderUpdate on '%v' update", update.Name)
	}
	// Decode the instructions
	var index uint16
	var newSectorCount uint32
	err := encoding.UnmarshalAll(update.Instructions, &index, &newSectorCount)
	if err != nil {
		return errors.AddContext(err, "failed to unmarshal shrinkStorageFolderUpdate instructions")
	}
	// Empty storage folder.
	err = cm.managedGrowStorageFolder(index, newSectorCount)
	if err != nil {
		cm.log.Printf("ERROR: Unable to grow storage folder %v: %v\n", index, err)
		// atomic.AddUint64(&sf.atomicFailedWrites, 1) // TODO: move to caller
		return errors.AddContext(err, fmt.Sprintf("failed to grow storage folder at index %v", index))
	}
	// Commit the change to the state.
	cm.commitStorageFolderExtension(index, newSectorCount)
	return nil
}

// applyDeleteSectorUpdate applies an update which deletes a sector from the
// contract manager.
func (cm *ContractManager) applyDeleteSectorUpdate(update walUpdate) error {
	if update.Name != deleteSectorUpdateName {
		return fmt.Errorf("can't call applyDeleteSectorUpdate on '%v' update", update.Name)
	}
	// Decode the instructions.
	var id sectorID
	err := encoding.UnmarshalAll(update.Instructions, &id)
	if err != nil {
		return errors.AddContext(err, "failed to unmarshal deleteSectorUpdate instructions")
	}
	return cm.managedDeleteSector(id)
}

// applyRemoveSectorUpdate applies an update which removes a sector from the
// contract manager.
func (cm *ContractManager) applyRemoveSectorUpdate(update walUpdate) error {
	if update.Name != removeSectorUpdateName {
		return fmt.Errorf("can't call applyRemoveSectorUpdate on '%v' update", update.Name)
	}
	// Decode the instructions.
	var id sectorID
	err := encoding.UnmarshalAll(update.Instructions, &id)
	if err != nil {
		return errors.AddContext(err, "failed to unmarshal removeSectorUpdate instructions")
	}
	return cm.managedRemoveSector(id)
}

// applyAddStorageFolderUpdate applies an update which adds a storage folder to
// the contract manager.
func (cm *ContractManager) applyAddPhysicalSectorUpdate(update walUpdate) error {
	if update.Name != addPhysicalSectorUpdateName {
		return fmt.Errorf("can't call applyAddStorageFolderUpdate on '%v' update", update.Name)
	}
	// Decode the instructions.
	var id sectorID
	var data []byte
	var count uint16
	err := encoding.UnmarshalAll(update.Instructions, &id, &data, &count)
	if err != nil {
		return errors.AddContext(err, "failed to unmarshal addAddPhysicalSectorUpdate instructions")
	}
	return cm.managedAddPhysicalSector(id, data, count)
}

// applyAddVirtualFolderUpdate applies an update which adds a storage folder to
// the contract manager.
func (cm *ContractManager) applyAddVirtualSectorUpdate(update walUpdate) error {
	if update.Name != addVirtualSectorUpdateName {
		return fmt.Errorf("can't call applyAddStorageFolderUpdate on '%v' update", update.Name)
	}
	// Decode the instructions.
	var id sectorID
	var location sectorLocation
	err := encoding.UnmarshalAll(update.Instructions, &id, &location)
	if err != nil {
		return errors.AddContext(err, "failed to unmarshal addAddVirtualSectorUpdate instructions")
	}
	return cm.managedAddVirtualSector(id, location)
}

// loadWal loads the wal and applies any unfinished transactions to disk.
func (cm *ContractManager) loadWal() error {
	// Try opening the WAL file.
	walFileName := filepath.Join(cm.persistDir, walFile)
	txns, wal, err := writeaheadlog.New(walFileName)
	if err != nil {
		return err
	}
	cm.staticWal = wal
	// Apply the unfinished transactions.
	for _, txn := range txns {
		updates := make([]walUpdate, 0, len(txn.Updates))
		for _, u := range txn.Updates {
			updates = append(updates, walUpdate{u, nil})
		}
		err := cm.applyUpdates(updates...)
		if err != nil && !errors.Contains(err, errBadStorageFolderIndex) {
			return err
		}
		if err := txn.SignalUpdatesApplied(); err != nil {
			return err
		}
	}
	return nil
}
