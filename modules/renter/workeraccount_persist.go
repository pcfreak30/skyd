package renter

// TODO: Derive the account secret key using the wallet seed. Can use:
// `account specifier || wallet seed || host pubkey` I believe.
//
// If we derive the seeds deterministically, that may mean that we can
// regenerate accounts even we fail to load them from disk. When we make a new
// account with a host, we should always query that host for a balance even if
// we think this is a new account, some previous run on siad may have created
// the account for us.
//
// TODO: How long does the host keep an account open? Does it keep the account
// open for the entire period? If not, we should probably adjust that on the
// host side, otherwise renters that go offline for a while are going to lose
// their accounts because the hosts will expire them. Does the renter track the
// expiration date of the accounts? Will it know upload load that the account is
// missing from the host not because of malice but because they expired?

import (
	"bytes"
	"io"
	"os"
	"path/filepath"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

const (
	// accountSize is the fixed account size in bytes
	accountSize = 1 << 8 // 256 bytes
)

var (
	// accountsFilename is the filename of the accounts persistence file
	accountsFilename = "accounts.dat"

	// Metadata
	metadataHeader  = types.NewSpecifier("Accounts\n")
	metadataVersion = types.NewSpecifier("v1.5.0\n")
	metadataSize    = 2*types.SpecifierLen + 1 // 1 byte for 'clean' flag

	// Metadata validation errors
	errWrongHeader  = errors.New("wrong header")
	errWrongVersion = errors.New("wrong version")

	// Persistence data validation errors
	errInvalidChecksum = errors.New("invalid checksum")
)

type (
	// accountManager tracks the set of accounts known to the renter.
	accountManager struct {
		accounts map[string]*account

		// Utils. The file is global to all accounts, each account looks at a
		// specific offset within the file.
		staticFile modules.File
		mu   sync.Mutex
	}

	// accountsMetadata is the metadata of the accounts persist file
	accountsMetadata struct {
		Header  types.Specifier
		Version types.Specifier
		Clean   bool
	}

	// accountPersistence is the account's persistence object which holds all
	// data that gets persisted for a single account.
	accountPersistence struct {
		AccountID modules.AccountID
		Balance   types.Currency
		HostKey   types.SiaPublicKey
		SecretKey crypto.SecretKey
	}
)

// managedPersist will write the account to the given file at the account's
// offset, without syncing the file.
func (a *account) managedPersist() error {
	a.mu.Lock()
	accountData := accountPersistence{
		AccountID: a.staticID,
		Balance:   a.balance,
		HostKey:   a.staticHostKey,
		SecretKey: a.staticSecretKey,
	}
	a.mu.Unlock()
	_, err := a.staticFile.WriteAt(accountData.bytes(), a.staticOffset)
	return errors.AddContext(err, "unable to write the account to disk")
}

// bytes is a helper method on the persistence object that outputs the bytes to
// put on disk, these include the checksum and the marshaled persistence object.
func (ap *accountPersistence) bytes() []byte {
	accBytes := encoding.Marshal(ap)
	accBytesMaxSize := accountSize - crypto.HashSize // leave room for checksum
	if len(accBytes) > accBytesMaxSize {
		build.Critical("marshaled object is larger than expected size")
	}

	// Calculate checksum on padded account bytes. Upon load, the padding will
	// be ignored by the unmarshaling.
	accBytesPadded := make([]byte, accBytesMaxSize)
	copy(accBytesPadded, accBytes)
	checksum := crypto.HashBytes(accBytesPadded)

	// create final byte slice of account size
	b := make([]byte, accountSize)
	copy(b[:len(checksum)], checksum[:])
	copy(b[len(checksum):], accBytesPadded)
	return b
}

// loadBytes is a helper method that takes a byte slice, containing a checksum
// and the account bytes, and unmarshals them onto the persistence object if the
// checksum is valid.
func (ap *accountPersistence) loadBytes(b []byte) error {
	// extract checksum and verify it
	checksum := b[:crypto.HashSize]
	accBytes := b[crypto.HashSize:]
	accHash := crypto.HashBytes(accBytes)
	if !bytes.Equal(checksum, accHash[:]) {
		return errInvalidChecksum
	}

	// unmarshal the account bytes onto the persistence object
	return errors.AddContext(encoding.Unmarshal(accBytes, ap), "Failed to unmarshal account bytes")
}

// managedOpenAccount returns an account for the given host. If it does not
// exist already one is created.
func (am *accountManager) managedOpenAccount(hostKey types.SiaPublicKey) (acc *account, err error) {
	// Check if we already have an account. Due to a race condition around
	// account creation, we need to check that the account was persisted to disk
	// before we can start using it, this happens with the 'staticReady' and
	// 'externActive' variables of the account. See the rest of this functions
	// implementation to understand how they are used in practice.
	am.mu.Lock()
	acc, ok := am.accounts[hostKey.String()]
	if ok {
		<-acc.staticReady
		if externActive {
			am.mu.Unlock()
			return acc, nil
		}
	}

	// Open a new account.
	offset := (len(am.accounts) + 1) * accountSize // +1 because the first slot in the file is used for metadata
	aid, sk := modules.NewAccountID()
	acc := &account{
		staticID:        aid,
		staticHostKey:   hostKey,
		staticSecretKey: sk,

		staticFile:      r.staticAccountsFile,
		staticOffset:    int64(offset),

		staticReady: make(chan struct{}),
	}
	am.accounts[hostKey.String()] = acc
	am.mu.Unlock()
	// Defer a close on 'staticReady'. By default, 'externActive' is false, so
	// if there is an error, the account will be marked as unusable.
	defer close(acc.staticReady)

	// Defer a function to delete the account if the persistence fails. This is
	// technically a race condition, but the alternative is holding the lock on
	// the account mangager while doing an fsync, which is not ideal.
	defer func() {
		if err != nil {
			am.mu.Lock()
			delete(am.accounts, hostKey.String())
			am.mu.Unlock()
		}
	}()

	// Save the file. After the file gets written to disk, perform a sync
	// because we want to ensure that the secret key of the account can be
	// recovered before we start using the account.
	err = acc.managedPersist()
	if err != nil {
		return nil, errors.AddContext(err, "Failed to persist account")
	}
	err = a.staticFile.Sync()
	if err != nil {
		return nil, errors.AddContext(err, "failed to sync accounts file")
	}

	// Mark the account as usable so that anyone who tried to open the account
	// after this function ran will see that the account is persisted correctly.
	acc.mu.Lock()
	acc.externActive = true
	acc.mu.Unlock()
	return acc, nil
}

// readAccountAt tries to read an account object from the account persist file
// at the given offset
func (am *accountManager) readAccountAt(offset) (*account, error) {
	// read account bytes
	accountBytes := make([]byte, accountSize)
	_, err := r.staticAccountsFile.ReadAt(accountBytes, offset)
	if err != nil {
		return nil, errors.AddContext(err, "Failed to read account bytes")
	}

	// load the account bytes onto the a persistence object
	var accountData accountPersistence
	err = accountData.loadBytes(accountBytes)
	if err != nil {
		return nil, errors.AddContext(err, "Failed to load account bytes")
	}

	return &account{
		staticID:        accountData.AccountID,
		staticHostKey:   accountData.HostKey,
		staticSecretKey: accountData.SecretKey,

		balance:         accountData.Balance,

		staticOffset:    offset,
		staticFile:      r.staticAccountsFile,
	}, nil
}

// managedLoad will pull all of the accounts off of disk and load them into the
// account manager. This should complete before the accountManager is made
// available to other processes.
func (am *accountManager) load() error {
	// Open the accounts file.
	err := am.openFile()
	if err != nil {
		return errors.AddContext(err, "failed to open accounts file")
	}

	// sanity check that the metadata size is not larger than the account size
	// before setting the initial offset
	if metadataSize > accountSize {
		err = errors.New("metadata size is larger than account size, this means the initial offset is too small")
		build.Critical(err)
		return err
	}
	initialOffset := int64(accountSize)

	// read the raw account data and decode them into accounts
	accounts := make(map[string]*account)
	for offset := initialOffset; ; offset += accountSize {
		// read the account at offset
		acc, err := r.readAccountAt(offset)
		if errors.Contains(err, io.EOF) {
			break
		} else if err != nil {
			r.log.Println("ERROR: could not load account", err)
			continue
		}

		// reset the account balances after an unclean shutdown
		if !metadata.Clean {
			acc.balance = types.ZeroCurrency
		}
		accounts[acc.staticHostKey.String()] = acc
	}

	// mark the metadata as 'dirty' and update the metadata on disk, this
	// ensures the account balances will be ignored in the event of an an
	// unclean shutdown
	metadata.Clean = false
	err = r.updateAccountsMetadata(metadata)
	if err != nil {
		return errors.AddContext(err, "Failed to write metadata to accounts file")
	}
	err = r.staticAccountsFile.Sync()
	if err != nil {
		return errors.AddContext(err, "Failed to sync accounts file")
	}

	// load the accounts on to the renter
	id := r.mu.Lock()
	r.accounts = accounts
	r.mu.Unlock(id)
	return nil
}

// openFile will open the file of the account manager and set the account
// manager's file variable.
//
// openFile will return 'true' if the previous shutdown was clean, and 'false'
// if the previous shutdown was not clean.
func (am *accountManager) openFile() (bool, error) {
	// Sanity check that the file isn't already opened.
	if am.staticFile != nil {
		am.staticRenter.log.Critical("double open detected on account manager")
		return false, errors.New("file already open")
	}

	// Check the file health.
	path := filepath.Join(am.staticRenter.persistDir, accountsFilename)
	_, statErr := os.Stat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return false, errors.AddContext(statErr, "error calling stat on file")
	}

	// Open the file, create it if it does not exist yet.
	file, err := am.staticRenter.deps.OpenFile(path, os.O_RDWR|os.O_CREATE, defaultFilePerm)
	if err != nil {
		return false, err
	}
	am.staticFile = file

	// If the stat err was nil, a header already exists. Check that the header
	// matches what we are expecting.
	var cleanClose bool
	if statErr == nil {
		cleanClose = am.checkMetadata()
	} else {
		// This is a new file, represent that the previous shutdown was clean.
		cleanClose = true
	}

	// Whether this is a new file or an existing file, we need to set the header
	// on the metadata. When opening an account, the header should represent an
	// unclean shutdown. This will be flipped to a header that represents a
	// clean shutdown upon closing.
	err = am.updateMetadata(
		Header:  metadataHeader,
		Version: metadataVersion,
		Clean:   false,
	})
	if err != nil {
		return false, errors.AddContext(err, "unable to update the account metadata")
	}
	return cleanClose, nil
}

// loadMetadata loads the metadata from the accounts file.
func (am *accountManager) loadMetadata() (accountsMetadata, error) {
	var metadata accountsMetadata

	// read matadata bytes
	buffer := make([]byte, metadataSize)
	_, err := io.ReadFull(am.staticFile, buffer)
	if err != nil {
		return metadata, errors.AddContext(err, "failed to read metadata from accounts file")
	}

	// unmarshal into accountsMetadata
	err = encoding.Unmarshal(buffer, &metadata)
	if err != nil {
		return metadata, errors.AddContext(err, "failed to decode metadata from accounts file")
	}

	// validate the metadata
	if metadata.Header != metadataHeader {
		return errors.AddContext(errWrongHeader, "failed to verify accounts metadata")
	}
	if metadata.Version != metadataVersion {
		return errors.AddContext(errWrongVersion, "failed to verify accounts metadata")
	}
	return metadata, nil
}

// managedSaveAccounts is called on shutdown and ensures the account data is
// properly persisted to disk
func (r *Renter) managedSaveAccounts() error {
	id := r.mu.Lock()
	if r.accountsClosed {
		r.mu.Unlock(id)
		build.Critical("Trying to save accounts twice")
		return errors.New("Trying to save accounts twice")
	}
	r.mu.Unlock(id)

	// grab the accounts
	id = r.mu.RLock()
	accounts := r.accounts
	r.mu.RUnlock(id)

	// save the account data to disk
	for _, account := range accounts {
		err := account.managedPersist()
		if err != nil {
			r.log.Println("ERROR:", err)
			continue
		}
	}

	// sync before updating the header
	err := r.staticAccountsFile.Sync()
	if err != nil {
		return errors.AddContext(err, "Failed to sync accounts file")
	}

	// update the metadata and mark the file as clean
	if err = r.updateAccountsMetadata(accountsMetadata{
		Header:  metadataHeader,
		Version: metadataVersion,
		Clean:   true,
	}); err != nil {
		return errors.AddContext(err, "Failed to update accounts file metadata")
	}

	// sync and close the accounts file
	return errors.AddContext(errors.Compose(
		r.staticAccountsFile.Sync(),
		r.staticAccountsFile.Close(),
	), "Failed to sync and close the accounts file")
}

// updateAccountsMetadata writes the given metadata to the accounts file.
func (r *Renter) updateAccountsMetadata(am accountsMetadata) error {
	_, err := r.staticAccountsFile.WriteAt(encoding.Marshal(am), 0)
	return err
}
