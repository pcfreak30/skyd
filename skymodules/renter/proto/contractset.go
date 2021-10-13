package proto

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gitlab.com/NebulousLabs/encoding"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/ratelimit"
	"gitlab.com/NebulousLabs/writeaheadlog"

	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// A ContractSet provides safe concurrent access to a set of contracts. Its
// purpose is to serialize modifications to individual contracts, as well as
// to provide operations on the set as a whole.
type ContractSet struct {
	contracts  map[types.FileContractID]*SafeContract
	pubKeys    map[string]types.FileContractID
	staticDeps modules.Dependencies
	staticDir  string
	mu         sync.Mutex
	staticRL   *ratelimit.RateLimit
	staticWal  *writeaheadlog.WAL
}

// Acquire looks up the contract for the specified host key and locks it before
// returning it. If the contract is not present in the set, Acquire returns
// false and a zero-valued RenterContract.
func (cs *ContractSet) Acquire(id types.FileContractID) (*SafeContract, bool) {
	cs.mu.Lock()
	safeContract, ok := cs.contracts[id]
	cs.mu.Unlock()
	if !ok {
		return nil, false
	}
	safeContract.revisionMu.Lock()
	// We need to check if the contract is still in the map or if it has been
	// deleted in the meantime.
	cs.mu.Lock()
	_, ok = cs.contracts[id]
	cs.mu.Unlock()
	if !ok {
		safeContract.revisionMu.Unlock()
		return nil, false
	}
	return safeContract, true
}

// Delete removes a contract from the set. The contract must have been
// previously acquired by Acquire. If the contract is not present in the set,
// Delete is a no-op.
func (cs *ContractSet) Delete(c *SafeContract) {
	cs.mu.Lock()
	_, ok := cs.contracts[c.header.ID()]
	if !ok {
		cs.mu.Unlock()
		build.Critical("Delete called on already deleted contract")
		return
	}
	delete(cs.contracts, c.header.ID())
	delete(cs.pubKeys, c.header.HostPublicKey().String())
	unappliedTxns := c.unappliedTxns
	cs.mu.Unlock()
	c.revisionMu.Unlock()
	// delete contract file
	headerPath := filepath.Join(cs.staticDir, c.header.ID().String()+contractHeaderExtension)
	rootsPath := filepath.Join(cs.staticDir, c.header.ID().String()+contractRootsExtension)
	// close header and root files.
	err := errors.Compose(c.staticHeaderFile.Close(), c.merkleRoots.rootsFile.Close())
	// remove the files.
	err = errors.Compose(err, os.Remove(headerPath), os.Remove(rootsPath))
	if err != nil {
		build.Critical("Failed to delete SafeContract from disk:", err)
	}
	for _, txn := range unappliedTxns {
		err = txn.SignalUpdatesApplied()
		if err != nil {
			build.Critical("Delete: failed to signal applied updates for contract", c.header.ID())
		}
	}
}

// IDs returns the fcid of each contract with in the set. The contracts are not
// locked.
func (cs *ContractSet) IDs() []types.FileContractID {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	pks := make([]types.FileContractID, 0, len(cs.contracts))
	for fcid := range cs.contracts {
		pks = append(pks, fcid)
	}
	return pks
}

// InsertContract inserts an existing contract into the set.
func (cs *ContractSet) InsertContract(rc skymodules.RecoverableContract, revTxn types.Transaction, roots []crypto.Hash, sk crypto.SecretKey) (skymodules.RenterContract, error) {
	// Estimate the totalCost.
	// NOTE: The actual totalCost is the funding amount. Which means
	// renterPayout + txnFee + basePrice + contractPrice.
	// Since we don't know the basePrice and contractPrice, we don't add them.
	var totalCost types.Currency
	totalCost = totalCost.Add(rc.FileContract.ValidRenterPayout())
	totalCost = totalCost.Add(rc.TxnFee)
	return cs.managedInsertContract(contractHeader{
		Transaction: revTxn,
		SecretKey:   sk,
		StartHeight: rc.StartHeight,
		TotalCost:   totalCost,
		TxnFee:      rc.TxnFee,
		SiafundFee:  types.Tax(rc.StartHeight, rc.Payout),
	}, roots)
}

// Len returns the number of contracts in the set.
func (cs *ContractSet) Len() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return len(cs.contracts)
}

// Return returns a locked contract to the set and unlocks it. The contract
// must have been previously acquired by Acquire. If the contract is not
// present in the set, Return panics.
func (cs *ContractSet) Return(c *SafeContract) {
	cs.mu.Lock()
	_, ok := cs.contracts[c.header.ID()]
	if !ok {
		cs.mu.Unlock()
		build.Critical("no contract with that key")
	}
	cs.mu.Unlock()
	c.revisionMu.Unlock()
}

// View returns a copy of the contract with the specified host key. The contract
// is not locked. Certain fields, including the MerkleRoots, are set to nil for
// safety reasons. If the contract is not present in the set, View returns false
// and a zero-valued RenterContract.
func (cs *ContractSet) View(id types.FileContractID) (skymodules.RenterContract, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	safeContract, ok := cs.contracts[id]
	if !ok {
		return skymodules.RenterContract{}, false
	}
	return safeContract.Metadata(), true
}

// PublicKey returns the public key capable of verifying the renter's signature
// on a contract.
func (cs *ContractSet) PublicKey(id types.FileContractID) (crypto.PublicKey, bool) {
	cs.mu.Lock()
	safeContract, ok := cs.contracts[id]
	cs.mu.Unlock()
	if !ok {
		return crypto.PublicKey{}, false
	}
	return safeContract.PublicKey(), true
}

// ViewAll returns the metadata of each contract in the set. The contracts are
// not locked.
func (cs *ContractSet) ViewAll() []skymodules.RenterContract {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	contracts := make([]skymodules.RenterContract, 0, len(cs.contracts))
	for _, safeContract := range cs.contracts {
		contracts = append(contracts, safeContract.Metadata())
	}
	return contracts
}

// Close closes all contracts in a contract set, this means rendering it unusable for I/O
func (cs *ContractSet) Close() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	var err error
	for _, c := range cs.contracts {
		err = errors.Compose(err, c.staticHeaderFile.Close())
		err = errors.Compose(err, c.merkleRoots.rootsFile.Close())
	}
	_, errWal := cs.staticWal.CloseIncomplete()
	return errors.Compose(err, errWal)
}

// NewContractSet returns a ContractSet storing its contracts in the specified
// dir.
func NewContractSet(dir string, rl *ratelimit.RateLimit, deps modules.Dependencies) (*ContractSet, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	d, err := os.Open(dir)
	if err != nil {
		return nil, err
	} else if stat, err := d.Stat(); err != nil {
		return nil, err
	} else if !stat.IsDir() {
		return nil, errors.New("not a directory")
	}
	if err := d.Close(); err != nil {
		return nil, err
	}

	// Load the WAL. Any recovered updates will be applied after loading
	// contracts.
	//
	// COMPATv1.3.1RC2 Rename old wals to have the 'wal' extension if new file
	// doesn't exist.
	if err := v131RC2RenameWAL(dir); err != nil {
		return nil, err
	}
	walTxns, wal, err := writeaheadlog.New(filepath.Join(dir, "contractset.wal"))
	if err != nil {
		return nil, err
	}

	cs := &ContractSet{
		contracts: make(map[types.FileContractID]*SafeContract),
		pubKeys:   make(map[string]types.FileContractID),

		staticDeps: deps,
		staticDir:  dir,
		staticRL:   rl,
		staticWal:  wal,
	}
	// Set the initial rate limit to 'unlimited' bandwidth with 4kib packets.
	cs.staticRL = ratelimit.NewRateLimit(0, 0, 0)

	// Some vars for unmarshaling. These are pulled out of the loop to
	// prevent rapid allocations.
	var ush updateSetHeader
	var usr updateSetRoot

	// Before loading the contract files apply the updates which were meant to
	// create new contracts and filter them out.
	unappliedWalTxns := make(map[types.FileContractID][]*unappliedWalTxn)
	for _, txn := range walTxns {
		if len(txn.Updates) != 1 {
			// Invalid txns are applied.
			build.Critical("txn has wrong number of updates", len(txn.Updates))
			if err := txn.SignalUpdatesApplied(); err != nil {
				return nil, errors.AddContext(err, "failed to apply invalid update")
			}
			continue
		}
		switch update := txn.Updates[0]; update.Name {
		case updateNameInsertContract:
			// Apply unfinished insert contract updates.
			_, err := cs.managedApplyInsertContractUpdate(txn.Updates[0])
			if err != nil {
				return nil, errors.AddContext(err, "failed to apply insertContractUpdate on startup")
			}
			err = txn.SignalUpdatesApplied()
			if err != nil {
				return nil, errors.AddContext(err, "failed to apply insertContractUpdate on startup")
			}
		case updateNameSetHeader:
			// Unfinished set header updates are collected.
			if err := unmarshalHeader(update.Instructions, &ush); err != nil {
				return nil, errors.AddContext(err, "unable to unmarshal the contract header during wal txn recovery")
			}
			unappliedWalTxns[ush.ID] = append(unappliedWalTxns[ush.ID], newUnappliedWalTxn(txn))
		case updateNameSetRoot:
			// Unfinished set root updates are collected.
			if err := encoding.Unmarshal(update.Instructions, &usr); err != nil {
				return nil, errors.AddContext(err, "unable to unmarshal the update root set during wal txn recovery")
			}
			unappliedWalTxns[usr.ID] = append(unappliedWalTxns[usr.ID], newUnappliedWalTxn(txn))
		default:
			// Unknown updates are applied.
			build.Critical("unknown update", update.Name)
			err = txn.SignalUpdatesApplied()
			if err != nil {
				return nil, errors.AddContext(err, "failed to apply unknown update")
			}
		}
	}

	// Check for legacy contracts and split them up.
	if err := cs.managedV146SplitContractHeaderAndRoots(dir); err != nil {
		return nil, err
	}

	// Load the contract files.
	fis, err := ioutil.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, fi := range fis {
		filename := fi.Name()
		if filepath.Ext(filename) != contractHeaderExtension {
			continue
		}
		nameNoExt := strings.TrimSuffix(filename, contractHeaderExtension)
		headerPath := filepath.Join(dir, filename)
		rootsPath := filepath.Join(dir, nameNoExt+contractRootsExtension)
		refCounterPath := filepath.Join(dir, nameNoExt+refCounterExtension)

		if err := cs.loadSafeContract(headerPath, rootsPath, refCounterPath, unappliedWalTxns); err != nil {
			extErr := fmt.Errorf("failed to load safecontract for header %v", headerPath)
			return nil, errors.Compose(extErr, err)
		}
	}

	// Apply all the txns we don't have contracts for.
	for fcid, txns := range unappliedWalTxns {
		_, exists := cs.contracts[fcid]
		if exists {
			continue
		}
		if build.Release == "testing" {
			build.Critical("regular testing should never leave txns to unknown contracts", fcid)
		}
		for _, txn := range txns {
			err = txn.SignalUpdatesApplied()
			if err != nil {
				return nil, errors.AddContext(err, "failed to apply unused wal txn")
			}
		}
	}
	return cs, nil
}

// v131RC2RenameWAL renames an existing old wal file from contractset.log to
// contractset.wal
func v131RC2RenameWAL(dir string) error {
	oldPath := filepath.Join(dir, "contractset.log")
	newPath := filepath.Join(dir, "contractset.wal")
	_, errOld := os.Stat(oldPath)
	_, errNew := os.Stat(newPath)
	if !os.IsNotExist(errOld) && os.IsNotExist(errNew) {
		return build.ExtendErr("failed to rename contractset.log to contractset.wal",
			os.Rename(oldPath, newPath))
	}
	return nil
}

// managedV146SplitContractHeaderAndRoots goes through all the legacy contracts
// in a directory and splits the file up into a header and roots file.
func (cs *ContractSet) managedV146SplitContractHeaderAndRoots(dir string) error {
	// Load the contract files.
	fis, err := ioutil.ReadDir(dir)
	if err != nil {
		return err
	}

	oldHeaderSize := 4088 // declared here to avoid cluttering of non-legacy codebase
	for _, fi := range fis {
		filename := fi.Name()
		if filepath.Ext(filename) != v146ContractExtension {
			continue
		}
		path := filepath.Join(cs.staticDir, filename)
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		rootsSection := newFileSection(f, int64(oldHeaderSize), -1)

		// Load header.
		header, err := loadSafeContractHeader(f, oldHeaderSize*decodeMaxSizeMultiplier)
		if err != nil {
			return errors.Compose(err, f.Close())
		}
		// Load roots.
		roots, unappliedTxns, err := loadExistingMerkleRootsFromSection(rootsSection)
		if err != nil {
			return errors.Compose(err, f.Close())
		}
		if unappliedTxns {
			build.Critical("can't upgrade contractset after an unclean shutdown, please downgrade Sia, start it, stop it cleanly and then try to upgrade again")
			return errors.Compose(errors.New("upgrade failed due to unclean shutdown"), f.Close())
		}
		merkleRoots, err := roots.merkleRoots()
		if err != nil {
			return errors.Compose(err, f.Close())
		}
		// Insert contract into the set.
		_, err = cs.managedInsertContract(header, merkleRoots)
		if err != nil {
			return errors.Compose(err, f.Close())
		}
		// Close the file.
		err = f.Close()
		if err != nil {
			return err
		}
		// Delete the file.
		err = os.Remove(path)
		if err != nil {
			return err
		}
	}
	// Delete the contract from memory again. We only needed to split them up on
	// disk. They will be correctly loaded with the non-legacy contracts during
	// the regular startup.
	cs.mu.Lock()
	cs.contracts = make(map[types.FileContractID]*SafeContract)
	cs.mu.Unlock()
	return nil
}
