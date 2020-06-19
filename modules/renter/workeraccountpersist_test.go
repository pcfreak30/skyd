package renter

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/siatest/dependencies"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/encoding"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
)

// newRandomAccountPersistence is a helper function that returns an
// accountPersistence object, initialised with random values
func newRandomAccountPersistence() accountPersistence {
	aid, sk := modules.NewAccountID()
	return accountPersistence{
		AccountID: aid,
		Balance:   types.NewCurrency64(fastrand.Uint64n(1e3)),
		HostKey:   types.SiaPublicKey{},
		SecretKey: sk,
		LastUsed:  time.Now().Unix(),
	}
}

// TestAccountPersistence verifies the functionality of the account persistence
func TestAccountPersistence(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	rt, err := newRenterTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := rt.Close()
		if err != nil {
			t.Log(err)
		}
	}()

	// create some random accounts
	_, err = openRandomTestAccountsOnRenter(rt.renter)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("Marshalling", testAccountMarshaling)
	t.Run("LoadBytesCompatV150", testLoadBytesCompatV150)
	t.Run("VerifyChecksum", testVerifyChecksum)

	t.Run("Save", func(t *testing.T) { testAccountSave(t, rt) })
	t.Run("Corrupted", func(t *testing.T) { testAccountCorrupted(t, rt) })
	t.Run("CompatV150", func(t *testing.T) { testAccountCompatV150(t, rt) })
}

// testAccountSave verifies accounts are properly saved and loaded onto the
// renter when it goes through a graceful shutdown and reboot.
func testAccountSave(t *testing.T, rt *renterTester) {
	r := rt.renter
	am := r.staticAccountManager

	// verify accounts file was loaded and set
	if r.staticAccountManager.staticFile == nil {
		t.Fatal("Accounts persistence file not set on the Renter after startup")
	}

	// grab some information about the accounts
	hostKeyToAccountID := make(map[string]string)
	am.mu.Lock()
	for _, a := range am.accounts {
		hostKeyToAccountID[a.staticHostKey.String()] = a.staticID.SPK().String()
	}
	am.mu.Unlock()

	// close the renter and reload it with a dependency that interrupts the
	// accounts save on shutdown
	deps := &dependencies.DependencyInterruptAccountSaveOnShutdown{}
	r, err := rt.reloadRenterWithDependency(r, deps)
	if err != nil {
		t.Fatal(err)
	}

	// ensure this test leaves a clean renter
	defer func() {
		_, err = rt.reloadRenterClean(r)
		if err != nil {
			t.Error("Failed to reload the renter with clean deps", err)
		}
	}()

	// verify the accounts got reloaded properly
	am.mu.Lock()
	if len(am.accounts) != len(hostKeyToAccountID) {
		t.Errorf("Unexpected amount of accounts, %v != %v", len(am.accounts), len(hostKeyToAccountID))
	}
	am.mu.Unlock()
	for hostKeyStr, accountID := range hostKeyToAccountID {
		var hostKey types.SiaPublicKey
		err := hostKey.LoadString(hostKeyStr)
		if err != nil {
			t.Error(err)
		}

		reloaded, err := am.managedOpenAccount(hostKey)
		if err != nil {
			t.Error(err)
		}

		if accountID != reloaded.staticID.SPK().String() {
			t.Error("Unexpected account ID")
		}
	}

	// reload it to trigger the unclean shutdown, we reload it with a dependency
	// that prevents any workers from being added to the workerpool, ensuring
	// the worker account can not be used
	rdeps := &dependencies.DependencyInterruptAddWorker{}
	r, err = rt.reloadRenterWithDependency(r, rdeps)
	if err != nil {
		t.Fatal(err)
	}
	am = r.staticAccountManager

	// verify the accounts were reloaded but the balances were cleared due to
	// the unclean shutdown
	am.mu.Lock()
	if len(am.accounts) != len(hostKeyToAccountID) {
		t.Errorf("Unexpected amount of accounts, %v != %v", len(am.accounts), len(hostKeyToAccountID))
	}
	am.mu.Unlock()
	for hostKeyStr, accountID := range hostKeyToAccountID {
		var hostKey types.SiaPublicKey
		err := hostKey.LoadString(hostKeyStr)
		if err != nil {
			t.Error(err)
		}

		reloaded, err := am.managedOpenAccount(hostKey)
		if err != nil {
			t.Error(err)
		}
		if accountID != reloaded.staticID.SPK().String() {
			t.Error("Unexpected account ID")
		}

		if !reloaded.balance.IsZero() {
			t.Fatal("Unexpected reloaded account balance")
		}
	}
}

// testAccountCorrupted verifies accounts that are corrupted are not reloaded
func testAccountCorrupted(t *testing.T, rt *renterTester) {
	r := rt.renter
	am := r.staticAccountManager

	// select a random account of which we'll corrupt data on disk
	var corrupted *account
	am.mu.Lock()
	accountsLen := len(am.accounts)
	for _, account := range am.accounts {
		corrupted = account
		break
	}
	am.mu.Unlock()

	// manually close the renter and corrupt the data at that offset
	err := r.Close()
	if err != nil {
		t.Fatal(err)
	}
	file, err := r.deps.OpenFile(filepath.Join(r.persistDir, accountsFilename), os.O_RDWR, defaultFilePerm)
	if err != nil {
		t.Fatal(err)
	}

	rN := fastrand.Intn(5) + 1
	rOffset := corrupted.staticOffset + int64(fastrand.Intn(accountSize-rN))
	n, err := file.WriteAt(fastrand.Bytes(rN), rOffset)
	if n != rN {
		t.Fatalf("Unexpected amount of bytes written, %v != %v", n, rN)
	}
	if err != nil {
		t.Fatal("Could not write corrupted account data")
	}

	// reopen the renter
	persistDir := filepath.Join(rt.dir, modules.RenterDir)
	r, errChan := New(rt.gateway, rt.cs, rt.wallet, rt.tpool, rt.mux, persistDir)
	if err := <-errChan; err != nil {
		t.Fatal(err)
	}
	err = rt.addRenter(r)
	if err != nil {
		t.Fatal(err)
	}
	am = r.staticAccountManager

	// verify only the non corrupted accounts got reloaded properly
	am.mu.Lock()
	// verify the amount of accounts reloaded is one less
	expected := accountsLen - 1
	if len(am.accounts) != expected {
		t.Errorf("Unexpected amount of accounts, %v != %v", len(am.accounts), expected)
	}
	for _, account := range am.accounts {
		if account.staticID.SPK().Equals(corrupted.staticID.SPK()) {
			t.Error("Corrupted account was not properly skipped")
		}
	}
	am.mu.Unlock()
}

// testAccountCompatV150 verifies that the account bytes of an account
// persistence object before it had the `lastUsed` property can be loaded into
// the current persistence object without corrupting it.
func testAccountCompatV150(t *testing.T, rt *renterTester) {
	r := rt.renter

	// close the renter
	err := r.Close()
	if err != nil {
		t.Fatal(err)
	}

	// copy the compat file to the location of the accounts file
	src := filepath.Join("..", "..", "compatibility", "accountsV150.dat")
	dst := filepath.Join(rt.dir, modules.RenterDir, accountsFilename)
	err = build.CopyFile(src, dst)
	if err != nil {
		t.Fatal(err)
	}

	// reopen the renter
	r, err = newRenterWithDependency(rt.gateway, rt.cs, rt.wallet, rt.tpool, rt.mux, filepath.Join(rt.dir, modules.RenterDir), &modules.ProductionDependencies{})
	if err != nil {
		t.Fatal(err)
	}
	rt.addRenter(r)

	// verify it can properly load all 163 compat accounts and their lastUsed
	// prop is initialized to 0 - no need to perform any extra validation, the
	// renter will have doen that during bootstrap using the checksum
	am := rt.renter.staticAccountManager
	am.mu.Lock()
	for _, acc := range am.accounts {
		if acc.lastUsed.Unix() != 0 {
			t.Error("expected `lastUsed` property of a compat account to be initialized to 0", acc.lastUsed)
		}
	}
	numAccounts := len(am.accounts)
	am.mu.Unlock()
	if numAccounts != 163 {
		t.Fatalf("Expected 163 accounts to be loaded, however %v were found", numAccounts)
	}

	// verify the tmp file got cleaned up
	_, err = os.Stat(am.tmpAccountsFilePath())
	if !os.IsNotExist(err) {
		t.Fatal("Expected 'NotExist' error, instead err was", err)
	}

	// verify the version in the metadata got bumped to the correct version
	accountsFile, err := os.Open(dst)
	if err != nil {
		t.Fatal("Failed to open accounts file after upgrade")
	}
	metadata, err := readMetadata(accountsFile)
	if err != nil {
		t.Fatal("Failed to read metadata after upgrade")
	}
	if metadata.Version != metadataVersion {
		t.Fatal("Expected metadata version to be bumped after upgrade")
	}

	// manually clear all of the accounts
	am.mu.Lock()
	am.accounts = nil
	am.mu.Unlock()

	// close the renter
	err = r.Close()
	if err != nil {
		t.Fatal(err)
	}

	// write the old version so we trigger compat flow again
	accountsFile, err = os.OpenFile(dst, os.O_RDWR, modules.DefaultFilePerm)
	if err != nil {
		t.Fatal("Failed to open accounts file metadata")
	}
	metadata.Version = compatV150MetadataVersion
	_, err = accountsFile.WriteAt(encoding.Marshal(metadata), 0)
	if err != nil {
		t.Fatal("Failed to write metadata, err", err)
	}

	// reopen the renter with a dependency that prevents the tmp file from being
	// cleaned up
	r, err = newRenterWithDependency(rt.gateway, rt.cs, rt.wallet, rt.tpool, rt.mux, filepath.Join(rt.dir, modules.RenterDir), &dependencies.DependencyDisableTmpFileCleanup{})
	if err != nil {
		t.Fatal(err)
	}
	rt.addRenter(r)

	// close the renter
	err = r.Close()
	if err != nil {
		t.Fatal(err)
	}

	// verify the tmp file is still there
	_, err = os.Stat(am.tmpAccountsFilePath())
	if err != nil {
		t.Fatal("Expected tmp file to be found")
	}

	// verify the version is the correct one
	accountsFile, err = os.OpenFile(dst, os.O_RDWR, modules.DefaultFilePerm)
	if err != nil {
		t.Fatal("Failed to open accounts file metadata")
	}
	metadata, err = readMetadata(accountsFile)
	if err != nil {
		t.Fatal("Failed to read metadata")
	}
	if metadata.Version != metadataVersion {
		t.Fatal("Unexpected version")
	}

	// check we still have the tmp file
	_, err = os.Stat(am.tmpAccountsFilePath())
	if err != nil {
		t.Fatal("Expected tmp file to be found")
	}

	// truncate the accounts file (after the header) simulating a crash after
	// the good version got written
	err = accountsFile.Truncate(int64(accountSize + fastrand.Intn(accountSize)))
	if err != nil {
		t.Fatal("Failed to truncate accounts file")
	}

	// reopen the renter again with a dependency to leave the tmp file - the
	// difference here is that the accounts file will have the correct version,
	// but it will also have been truncated, and yet we expected all accounts to
	// be there after reloading
	r, err = newRenterWithDependency(rt.gateway, rt.cs, rt.wallet, rt.tpool, rt.mux, filepath.Join(rt.dir, modules.RenterDir), &dependencies.DependencyDisableTmpFileCleanup{})
	if err != nil {
		t.Fatal(err)
	}
	rt.addRenter(r)

	// verify the accounts are there
	am = rt.renter.staticAccountManager
	am.mu.Lock()
	numAccounts = len(am.accounts)
	am.mu.Unlock()
	if numAccounts != 163 {
		t.Fatalf("Expected 163 accounts to be loaded, however %v were found", numAccounts)
	}

	// close the renter
	err = r.Close()
	if err != nil {
		t.Fatal(err)
	}

	// reopen the renter with a dependency that signals we've gone through the
	// tmp file recovery flow
	_, err = newRenterWithDependency(rt.gateway, rt.cs, rt.wallet, rt.tpool, rt.mux, filepath.Join(rt.dir, modules.RenterDir), &dependencies.DependencyRecoveredFromTmpFile{})
	if !errors.Contains(err, errTestTmpFileRecovery) {
		t.Fatal("Expected 'errTestTmpFileRecovery', instead err was", err)
	}
}

// testAccountMarshaling verifies the functionality of the `bytes` and
// `loadBytes` method on the accountPersistence object
func testAccountMarshaling(t *testing.T) {
	t.Parallel()

	// create a random persistence object and get its bytes
	ap := newRandomAccountPersistence()
	accountBytes := ap.bytes()
	if len(accountBytes) != accountSize {
		t.Fatal("Unexpected account bytes")
	}

	// load the bytes onto a new persistence object and compare for equality
	var uMar accountPersistence
	err := uMar.loadBytes(accountBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !ap.AccountID.SPK().Equals(uMar.AccountID.SPK()) {
		t.Fatal("Unexpected AccountID")
	}
	if !ap.Balance.Equals(uMar.Balance) {
		t.Fatal("Unexpected balance")
	}
	if !ap.HostKey.Equals(uMar.HostKey) {
		t.Fatal("Unexpected hostkey")
	}
	if ap.LastUsed != uMar.LastUsed {
		t.Fatal("Unexpected last used")
	}
	if !bytes.Equal(ap.SecretKey[:], uMar.SecretKey[:]) {
		t.Fatal("Unexpected secretkey")
	}

	// corrupt the checksum of the account bytes
	corruptedBytes := accountBytes
	corruptedBytes[fastrand.Intn(crypto.HashSize)] += 1
	err = uMar.loadBytes(corruptedBytes)
	if err != errInvalidChecksum {
		t.Fatalf("Expected error '%v', instead '%v'", errInvalidChecksum, err)
	}

	// corrupt the account data bytes
	corruptedBytes2 := accountBytes
	corruptedBytes2[fastrand.Intn(accountSize-crypto.HashSize)+crypto.HashSize] += 1
	err = uMar.loadBytes(corruptedBytes2)
	if err != errInvalidChecksum {
		t.Fatalf("Expected error '%v', instead '%v'", errInvalidChecksum, err)
	}
}

// testLoadBytesCompatV150 unit tests the functionality of the
// loadBytesCompatV150 function
func testLoadBytesCompatV150(t *testing.T) {
	t.Parallel()

	// create a v1.5.0 persistence object with random values
	random := newRandomAccountPersistence()
	ap := new(compatV150AccountPersistence)
	ap.AccountID = random.AccountID
	ap.Balance = random.Balance
	ap.HostKey = random.HostKey
	ap.SecretKey = random.SecretKey

	// create a byte slice storing the checksum and account bytes
	accBytesPadded := make([]byte, compatV150AccountSize-crypto.HashSize)
	copy(accBytesPadded, encoding.Marshal(*ap))
	checksum := crypto.HashBytes(accBytesPadded)

	accBytes := make([]byte, compatV150AccountSize)
	copy(accBytes[:crypto.HashSize], checksum[:])
	copy(accBytes[crypto.HashSize:], accBytesPadded)

	// load those bytes onto a compat persistence object just as we would during
	// the update flow
	compat := new(accountPersistence)
	err := compat.loadBytesCompatV150(accBytes)
	if err != nil {
		t.Fatal(err)
	}

	// validate the fields
	if !compat.AccountID.SPK().Equals(ap.AccountID.SPK()) {
		t.Fatal("Unexpected AccountID")
	}
	if !compat.Balance.Equals(ap.Balance) {
		t.Fatal("Unexpected balance")
	}
	if !compat.HostKey.Equals(ap.HostKey) {
		t.Fatal("Unexpected hostkey")
	}
	if !bytes.Equal(compat.SecretKey[:], ap.SecretKey[:]) {
		t.Fatal("Unexpected secretkey")
	}
	if compat.LastUsed != 0 {
		t.Fatal("Unexpected last used")
	}

	// verify the compat function asserts the length of the given byte slice -
	// this will build.Critical so we defer a recovery
	accBytes = make([]byte, compatV150AccountSize+1)
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(fmt.Sprintf("%v", r), "account bytes are not the expected length") {
			t.Error("Expected build.Critical")
			t.Log(r)
		}
	}()
	compat.loadBytesCompatV150(accBytes)
}

// testVerifyChecksum verifies the functionality of the verifyChecksum helper.
func testVerifyChecksum(t *testing.T) {
	t.Parallel()

	// create some random data and its checksum
	data := fastrand.Bytes(100)
	checksum := crypto.HashBytes(data)

	// copy them into a byte slice
	bytes := make([]byte, 100+crypto.HashSize)
	copy(bytes[:crypto.HashSize], checksum[:])
	copy(bytes[crypto.HashSize:], data)

	// write to disk
	fp := filepath.Join(os.TempDir(), "tmp.dat")
	f, err := os.OpenFile(fp, os.O_RDWR|os.O_CREATE, modules.DefaultFilePerm)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Remove(fp); err != nil {
			t.Fatal(err)
		}
	}()
	_, err = f.Write(bytes)
	if err != nil {
		t.Fatal(err)
	}

	// verify valid checksum
	validChecksum, err := verifyChecksum(fp)
	if !validChecksum || err != nil {
		t.Fatal("unexpected output", validChecksum, err)
	}

	// corrupt the checksum
	fastrand.Read(bytes[:5])
	_, err = f.WriteAt(bytes, 0)
	if err != nil {
		t.Fatal(err)
	}

	// verify invalid checksum
	validChecksum, err = verifyChecksum(fp)
	if validChecksum || err != nil {
		t.Fatal("unexpected output", validChecksum, err)
	}
}
