package contractmanager

import (
	"path/filepath"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/writeaheadlog"
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

func sectorMetadataUpdate(sf *storageFolder, su sectorUpdate) walUpdate {
	panic("not implemented yet")
}

func sectorDataUpdate(file modules.File, path string, sectorIndex uint32, data []byte) walUpdate {
	panic("not implemented yet")
}

func truncateUpdate(file modules.File, newSize int64) walUpdate {
	panic("not implemented yet")
}

func emptyStorageFolderUpdate(index uint16, startingPoint uint32) walUpdate {
	panic("not implemented yet")
	//	_, err := cm.managedEmptyStorageFolder(index, newSectorCount)
	//	if err != nil && !force {
	//		return err
	//	}
}

func (cm *ContractManager) createAndApplyTransaction(updates ...walUpdate) error {
	panic("not implemented yet")
}

func (cm *ContractManager) applySectorDataUpdate(update writeaheadlog.Update) error {
	panic("not implemented yet")
	//	err = writeSector(sf.sectorFile, sectorIndex, data)
	//	if err != nil {
	//		cm.log.Printf("ERROR: Unable to write sector for folder %v: %v\n", sf.path, err)
	//		atomic.AddUint64(&sf.atomicFailedWrites, 1)
	//		return errDiskTrouble
	//	}
}

func (cm *ContractManager) applySectorMetadataUpdate(update writeaheadlog.Update) error {
	panic("not implemented yet")
	//	err = cm.writeSectorMetadata(sf, su)
	//	if err != nil {
	//		cm.log.Printf("ERROR: Unable to write sector metadata for folder %v: %v\n", sf.path, err)
	//		atomic.AddUint64(&sf.atomicFailedWrites, 1)
	//		return errDiskTrouble
	//	}
}

//func addStorageFolderUpdate(sf *storageFolder) writeaheadlog.Update {
//	panic("not implemented yet")
//	//	wal.appendChange(stateChange{
//	//		UnfinishedStorageFolderAdditions: []savedStorageFolder{sf.savedStorageFolder()},
//	//	})
//}
//
//func modifySectorUpdate(su sectorUpdate) writeaheadlog.Update {
//	panic("not implemented yet")
//}
//
//func (cm *ContractManager) prepareWalTxn(updates ...writeaheadlog.Update) (*writeaheadlog.Transaction, error) {
//	// Create the writeaheadlog transaction.
//	txn, err := cm.wal.NewTransaction(updates)
//	if err != nil {
//		return nil, errors.AddContext(err, "failed to create wal txn")
//	}
//	// No extra setup is required. Signal that it is done.
//	if err := <-txn.SignalSetupComplete(); err != nil {
//		return nil, errors.AddContext(err, "failed to signal setup completion")
//	}
//	return txn, nil
//}

//type (
//	// sectorUpdate is an idempotent update to the sector metadata.
//	sectorUpdate struct {
//		Count  uint16
//		Folder uint16
//		ID     sectorID
//		Index  uint32
//	}
//
//	// stateChange defines an idempotent change to the state that has not yet
//	// been applied to the contract manager. The state change is a single
//	// transaction in the WAL.
//	//
//	// All changes in the stateChange object need to be idempotent, as it's
//	// possible that consecutive unclean shutdowns will result in changes being
//	// committed to the state multiple times.
//	stateChange struct {
//		// These fields relate to adding a storage folder. Adding a storage
//		// folder happens in several stages.
//		//
//		// First the storage folder is added as an
//		// 'UnfinishedStorageFolderAddition', because there is large amount of
//		// I/O preprocessing that is performed when adding a storage folder.
//		// This I/O must be nonblocking and must resume in the event of unclean
//		// or early shutdown.
//		//
//		// When the preprocessing is complete, the storage folder is moved to a
//		// 'StorageFolderAddition', which can be safely applied to the contract
//		// manager but hasn't yet.
//		//
//		// ErroredStorageFolderAdditions are signals to the WAL that an
//		// unfinished storage folder addition has failed and can be cleared
//		// out. The WAL is append-only, which is why an error needs to be
//		// logged instead of just automatically clearning out the unfinished
//		// storage folder addition.
//		ErroredStorageFolderAdditions     []uint16
//		ErroredStorageFolderExtensions    []uint16
//		StorageFolderAdditions            []savedStorageFolder
//		StorageFolderExtensions           []storageFolderExtension
//		StorageFolderRemovals             []storageFolderRemoval
//		StorageFolderReductions           []storageFolderReduction
//		UnfinishedStorageFolderAdditions  []savedStorageFolder
//		UnfinishedStorageFolderExtensions []unfinishedStorageFolderExtension
//
//		// Updates to the sector metadata. Careful ordering of events ensures
//		// that a sector update will not make it into the synced WAL unless the
//		// sector data is already on-disk and synced.
//		SectorUpdates []sectorUpdate
//	}

func applyUpdates(updates ...writeaheadlog.Update) error {
	panic("not implemented yet")
}

func (cm *ContractManager) loadWal() error {
	// Try opening the WAL file.
	walFileName := filepath.Join(cm.persistDir, walFile)
	txns, wal, err := writeaheadlog.New(walFileName)
	if err != nil {
		return err
	}
	cm.wal = wal
	// Apply the unfinished transactions.
	for _, txn := range txns {
		if err := applyUpdates(txn.Updates...); err != nil {
			return err
		}
	}
	return nil
	//	walFile, err := cm.dependencies.OpenFile(walFileName, os.O_RDONLY, 0600)
	//	if err == nil {
	//		// err == nil indicates that there is a WAL file, which means that the
	//		// previous shutdown was not clean. Re-commit the changes in the WAL to
	//		// bring the program back to consistency.
	//		cm.log.Println("WARN: WAL file detected, performing recovery after unclean shutdown.")
	//		err = wal.recoverWAL(walFile)
	//		if err != nil {
	//			return build.ExtendErr("failed to recover WAL", err)
	//		}
	//		err = walFile.Close()
	//		if err != nil {
	//			return build.ExtendErr("error closing WAL after performing a recovery", err)
	//		}
	//	} else if !os.IsNotExist(err) {
	//		return build.ExtendErr("walFile was not opened successfully", err)
	//	}
	//	// err == os.IsNotExist, suggesting a successful, clean shutdown. No action
	//	// is taken.
	//
	//	// Create the tmp settings file and initialize the first write to it. This
	//	// is necessary before kicking off the sync loop.
	//	wal.fileSettingsTmp, err = wal.cm.dependencies.CreateFile(filepath.Join(wal.cm.persistDir, settingsFileTmp))
	//	if err != nil {
	//		return build.ExtendErr("unable to prepare the settings temp file", err)
	//	}
	//	wal.cm.tg.AfterStop(func() {
	//		wal.mu.Lock()
	//		defer wal.mu.Unlock()
	//		if wal.fileSettingsTmp == nil {
	//			return
	//		}
	//		err := wal.fileSettingsTmp.Close()
	//		if err != nil {
	//			wal.cm.log.Println("ERROR: unable to close settings temporary file")
	//			return
	//		}
	//		err = wal.cm.dependencies.RemoveFile(filepath.Join(wal.cm.persistDir, settingsFileTmp))
	//		if err != nil {
	//			wal.cm.log.Println("ERROR: unable to remove settings temporary file")
	//			return
	//		}
	//	})
	//	ss := cm.savedSettings()
	//	b, err := json.MarshalIndent(ss, "", "\t")
	//	if err != nil {
	//		build.ExtendErr("unable to marshal settings data", err)
	//	}
	//	enc := json.NewEncoder(wal.fileSettingsTmp)
	//	if err := enc.Encode(settingsMetadata.Header); err != nil {
	//		build.ExtendErr("unable to write header to settings temp file", err)
	//	}
	//	if err := enc.Encode(settingsMetadata.Version); err != nil {
	//		build.ExtendErr("unable to write version to settings temp file", err)
	//	}
	//	if _, err = wal.fileSettingsTmp.Write(b); err != nil {
	//		build.ExtendErr("unable to write data settings temp file", err)
	//	}
	//	return nil
}
