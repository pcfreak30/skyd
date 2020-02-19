package contractmanager

import (
	"path/filepath"
)

// commitStorageFolderRemoval will finalize a storage folder removal from the
// contract manager.
func (cm *ContractManager) commitStorageFolderRemoval(index uint16, path string) {
	// Close any open file handles.
	sf, exists := cm.storageFolders[index]
	if exists {
		delete(cm.storageFolders, index)
	}
	if exists && sf.metadataFile != nil {
		err := sf.metadataFile.Close()
		if err != nil {
			cm.log.Printf("Error: unable to close metadata file as storage folder %v is removed\n", sf.path)
		}
	}
	if exists && sf.sectorFile != nil {
		err := sf.sectorFile.Close()
		if err != nil {
			cm.log.Printf("Error: unable to close sector file as storage folder %v is removed\n", sf.path)
		}
	}

	// Delete the files.
	err := cm.dependencies.RemoveFile(filepath.Join(path, metadataFile))
	if err != nil {
		cm.log.Printf("Error: unable to remove metadata file as storage folder %v is removed\n", path)
	}
	err = cm.dependencies.RemoveFile(filepath.Join(path, sectorFile))
	if err != nil {
		cm.log.Printf("Error: unable to reomve sector file as storage folder %v is removed\n", path)
	}
}

// RemoveStorageFolder will delete a storage folder from the contract manager,
// moving all of the sectors in the storage folder to new storage folders.
func (cm *ContractManager) RemoveStorageFolder(index uint16, force bool) error {
	err := cm.tg.Add()
	if err != nil {
		return err
	}
	defer cm.tg.Done()

	// Retrieve the specified storage folder.
	cm.mu.Lock()
	sf, exists := cm.storageFolders[index]
	if !exists {
		cm.mu.Unlock()
		return errStorageFolderNotFound
	}
	cm.mu.Unlock()

	// Lock the storage folder for the duration of the operation.
	sf.mu.Lock()
	defer sf.mu.Unlock()

	// Clear out the sectors in the storage folder.
	update := removeStorageFolderUpdate(index, sf.path)

	// Submit a storage folder removal to the WAL and wait until the update is
	// synced.
	return cm.createAndApplyTransaction(update)
}
