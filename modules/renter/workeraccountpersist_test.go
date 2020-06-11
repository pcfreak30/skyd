package renter

import (
	"bytes"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/siatest/dependencies"
	"gitlab.com/NebulousLabs/Sia/types"
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
	t.Run("OverwriteFile", testOverwriteFile)
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

	// reload it to trigger the unclean shutdown
	r, err = rt.reloadRenter(r)
	if err != nil {
		t.Fatal(err)
	}

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
		if acc.lastUsed != 0 {
			t.Error("expected `lastUsed` property of a compat account to be initialized to 0", acc.lastUsed)
		}
	}
	numAccounts := len(am.accounts)
	am.mu.Unlock()
	if numAccounts != 163 {
		t.Fatalf("Expected 163 accounts to be loaded, however %v were found", numAccounts)
	}

	// verify the tmp file got cleaned up
	tmp := filepath.Join(rt.dir, modules.RenterDir, accountsFilename+".tmp")
	_, err = os.Stat(tmp)
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
	tmp = filepath.Join(rt.dir, modules.RenterDir, accountsFilename+".tmp")
	_, err = os.Stat(tmp)
	if err != nil {
		t.Fatal("Expected tmp file to be found")
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

// testOverwriteFile verifies the functionality of the overwriteFile helper.
func testOverwriteFile(t *testing.T) {
	t.Parallel()

	src := filepath.Join(os.TempDir(), "src.dat")
	dst := filepath.Join(os.TempDir(), "dst.dat")
	defer func() {
		if err := errors.Compose(
			os.Remove(src),
			os.Remove(dst),
		); err != nil {
			t.Fatal(err)
		}
	}()

	// write some random data to both files
	srcData := fastrand.Bytes(100)
	err := ioutil.WriteFile(src, srcData, defaultFilePerm)
	if err != nil {
		t.Fatal(err)
	}

	dstData := fastrand.Bytes(200)
	err = ioutil.WriteFile(dst, dstData, defaultFilePerm)
	if err != nil {
		t.Fatal(err)
	}

	// open both files
	srcFile, err := os.OpenFile(src, os.O_RDWR|os.O_CREATE, modules.DefaultFilePerm)
	if err != nil {
		t.Fatal(err)
	}
	dstFile, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE, modules.DefaultFilePerm)
	if err != nil {
		t.Fatal(err)
	}

	// overwrite the contents of the destination file with the contents of the
	// source file
	err = overwriteFile(dstFile, srcFile)
	if err != nil {
		t.Fatal(err)
	}

	// verify the dst file has the src data
	newDstData, err := ioutil.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(newDstData, srcData) {
		t.Fatal("The destination file did not contain the expected source data")
	}

	// verify the src file was not altered
	srcDataOnDisk, err := ioutil.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(srcDataOnDisk, srcData) {
		t.Fatal("The source file did not contain the same source data, we expected this file to be untouched")
	}
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

// openRandomTestAccountsOnRenter is a helper function that creates a random
// number of accounts by calling 'managedOpenAccount' on the given renter
func openRandomTestAccountsOnRenter(r *Renter) ([]*account, error) {
	accounts := make([]*account, 0)
	for i := 0; i < fastrand.Intn(10)+1; i++ {
		hostKey := types.SiaPublicKey{
			Algorithm: types.SignatureEd25519,
			Key:       fastrand.Bytes(crypto.PublicKeySize),
		}
		account, err := r.staticAccountManager.managedOpenAccount(hostKey)
		if err != nil {
			return nil, errors.AddContext(err, "failed to create random accounts")
		}
		accounts = append(accounts, account)
	}
	return accounts, nil
}
