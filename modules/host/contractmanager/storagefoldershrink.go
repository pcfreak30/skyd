package contractmanager

import (
	"sync/atomic"

	"gitlab.com/NebulousLabs/Sia/modules"
)

// managedCommitStorageFolderReduction commits a storage folder reduction to the
// state and filesystem.
func (cm *ContractManager) managedCommitStorageFolderReduction(index uint16, newSectorCount uint32) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	sf, exists := cm.storageFolders[index]
	if !exists {
		cm.log.Critical("ERROR: storage folder reduction established for a storage folder that does not exist")
		return
	}
	if atomic.LoadUint64(&sf.atomicUnavailable) == 1 {
		// Cannot complete the storage folder reduction - storage folder is not
		// available.
		return
	}

	// Shrink the sector usage, but only if the sector usage is not already
	// smaller.
	if uint32(len(sf.usage)) > newSectorCount/storageFolderGranularity {
		// Unset the usage in all bits
		for i := newSectorCount; i < uint32(len(sf.usage))*storageFolderGranularity; i++ {
			sf.clearUsage(i)
		}
		// Truncate the usage field.
		sf.usage = sf.usage[:newSectorCount/storageFolderGranularity]
	}

	// Truncate the storage folder.
	err := sf.metadataFile.Truncate(int64(newSectorCount * sectorMetadataDiskSize))
	if err != nil {
		cm.log.Printf("Error: unable to truncate metadata file as storage folder %v is resized\n", sf.path)
	}
	err = sf.sectorFile.Truncate(int64(modules.SectorSize * uint64(newSectorCount)))
	if err != nil {
		cm.log.Printf("Error: unable to truncate sector file as storage folder %v is resized\n", sf.path)
	}
}
