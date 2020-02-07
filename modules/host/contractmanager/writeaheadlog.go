package contractmanager

import (
	"gitlab.com/NebulousLabs/errors"
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
)

func addStorageFolderUpdate(sf *storageFolder) writeaheadlog.Update {
	panic("not implemented yet")
	//	wal.appendChange(stateChange{
	//		UnfinishedStorageFolderAdditions: []savedStorageFolder{sf.savedStorageFolder()},
	//	})
}

func modifySectorUpdate(su sectorUpdate) writeaheadlog.Update {
	panic("not implemented yet")
}

func (cm *ContractManager) prepareWalTxn(updates ...writeaheadlog.Update) (*writeaheadlog.Transaction, error) {
	// Create the writeaheadlog transaction.
	txn, err := cm.wal.NewTransaction(updates)
	if err != nil {
		return nil, errors.AddContext(err, "failed to create wal txn")
	}
	// No extra setup is required. Signal that it is done.
	if err := <-txn.SignalSetupComplete(); err != nil {
		return nil, errors.AddContext(err, "failed to signal setup completion")
	}
	return txn, nil
}

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
