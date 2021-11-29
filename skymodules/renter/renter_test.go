package renter

import (
	"bufio"
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/NebulousLabs/ratelimit"
	"gitlab.com/NebulousLabs/siamux"

	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/contractor"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/hostdb"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/proto"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/modules/consensus"
	"go.sia.tech/siad/modules/gateway"
	"go.sia.tech/siad/modules/host"
	"go.sia.tech/siad/modules/miner"
	"go.sia.tech/siad/modules/transactionpool"
	"go.sia.tech/siad/modules/wallet"
	"go.sia.tech/siad/persist"
	"go.sia.tech/siad/types"
)

type (
	// testSiacoinSender is a implementation of the SiacoinSender interface
	// which remembers the arguments of the last call to SendSiacoins.
	testSiacoinSender struct {
		lastSend     types.Currency
		lastSendAddr types.UnlockHash
	}
)

// LastSend returns the arguments of the last call to SendSiacoins.
func (tss *testSiacoinSender) LastSend() (types.Currency, types.UnlockHash) {
	return tss.lastSend, tss.lastSendAddr
}

// SendSiacoins implements the SiacoinSender interface.
func (tss *testSiacoinSender) SendSiacoins(amt types.Currency, addr types.UnlockHash) ([]types.Transaction, error) {
	tss.lastSend = amt
	tss.lastSendAddr = addr
	return nil, nil
}

// renterTester contains all of the modules that are used while testing the renter.
type renterTester struct {
	cs      modules.ConsensusSet
	gateway modules.Gateway
	miner   modules.TestMiner
	tpool   modules.TransactionPool
	wallet  modules.Wallet

	mux *siamux.SiaMux

	renter *Renter
	dir    string
}

// Close shuts down the renter tester.
func (rt *renterTester) Close() error {
	err1 := rt.cs.Close()
	err2 := rt.gateway.Close()
	err3 := rt.miner.Close()
	err4 := rt.tpool.Close()
	err5 := rt.wallet.Close()
	err6 := rt.mux.Close()
	err7 := rt.renter.Close()
	return errors.Compose(err1, err2, err3, err4, err5, err6, err7)
}

// addCustomHost adds a host to the test group so that it appears in the host db
func (rt *renterTester) addCustomHost(testdir string, deps modules.Dependencies) (modules.Host, error) {
	// create a siamux for this particular host
	siaMuxDir := filepath.Join(testdir, modules.SiaMuxDir)
	mux, err := modules.NewSiaMux(siaMuxDir, testdir, "localhost:0", "localhost:0")
	if err != nil {
		return nil, err
	}

	h, err := host.NewCustomHost(deps, rt.cs, rt.gateway, rt.tpool, rt.wallet, mux, "localhost:0", filepath.Join(testdir, modules.HostDir))
	if err != nil {
		return nil, err
	}

	// configure host to accept contracts and to have a registry.
	settings := h.InternalSettings()
	settings.AcceptingContracts = true
	settings.RegistrySize = 640 * modules.RegistryEntrySize
	err = h.SetInternalSettings(settings)
	if err != nil {
		return nil, err
	}

	// add storage to host
	storageFolder := filepath.Join(testdir, "storage")
	err = os.MkdirAll(storageFolder, 0700)
	if err != nil {
		return nil, err
	}
	err = h.AddStorageFolder(storageFolder, 1<<20) // 1 MiB
	if err != nil {
		return nil, err
	}

	// announce the host
	err = h.Announce()
	if err != nil {
		return nil, build.ExtendErr("error announcing host", err)
	}

	// mine a block, processing the announcement
	_, err = rt.miner.AddBlock()
	if err != nil {
		return nil, err
	}

	// wait for hostdb to scan host
	activeHosts, err := rt.renter.ActiveHosts()
	if err != nil {
		return nil, err
	}
	for i := 0; i < 50 && len(activeHosts) == 0; i++ {
		time.Sleep(time.Millisecond * 100)
	}
	activeHosts, err = rt.renter.ActiveHosts()
	if err != nil {
		return nil, err
	}
	if len(activeHosts) == 0 {
		return nil, errors.New("host did not make it into the contractor hostdb in time")
	}

	return h, nil
}

// addHost adds a host to the test group so that it appears in the host db
func (rt *renterTester) addHost(name string) (modules.Host, error) {
	return rt.addCustomHost(filepath.Join(rt.dir, name), modules.ProdDependencies)
}

// addRenter adds a renter to the renter tester and then make sure there is
// money in the wallet
func (rt *renterTester) addRenter(r *Renter) error {
	rt.renter = r
	// Mine blocks until there is money in the wallet.
	for i := types.BlockHeight(0); i <= types.MaturityDelay; i++ {
		_, err := rt.miner.AddBlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// createZeroByteFileOnDisk creates a 0 byte file on disk so that a Stat of the
// local path won't return an error
func (rt *renterTester) createZeroByteFileOnDisk() (string, error) {
	path := filepath.Join(rt.renter.staticFileSystem.Root(), persist.RandomSuffix())
	err := ioutil.WriteFile(path, []byte{}, 0600)
	if err != nil {
		return "", err
	}
	return path, nil
}

// newTestSiaFile creates and returns a new siafile for testing. This file is
// marked as finished for backwards compatibility in testing.
func (rt *renterTester) newTestSiaFile(siaPath skymodules.SiaPath, source string, rc skymodules.ErasureCoder, size uint64) (*filesystem.FileNode, error) {
	// Create the siafile
	err := rt.renter.staticFileSystem.NewSiaFile(siaPath, source, rc, crypto.GenerateSiaKey(crypto.RandomCipherType()), size, persist.DefaultDiskPermissionsTest)
	if err != nil {
		return nil, err
	}
	// Open the file
	f, err := rt.renter.staticFileSystem.OpenSiaFile(siaPath)
	if err != nil {
		return nil, err
	}
	// Mark it as finished for backwards compatibility in testing
	err = f.SetFinished(0)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// reloadRenter closes the given renter and then re-adds it, effectively
// reloading the renter.
func (rt *renterTester) reloadRenter(r *Renter) (*Renter, error) {
	return rt.reloadRenterWithDependency(r, r.staticDeps)
}

// reloadRenterWithDependency closes the given renter and recreates it using the
// given dependency, it then re-adds the renter on the renter tester effectively
// reloading it.
func (rt *renterTester) reloadRenterWithDependency(r *Renter, deps skymodules.SkydDependencies) (*Renter, error) {
	err := r.Close()
	if err != nil {
		return nil, err
	}

	r, err = newRenterWithDependency(rt.gateway, rt.cs, rt.wallet, rt.tpool, rt.mux, filepath.Join(rt.dir, skymodules.RenterDir), deps)
	if err != nil {
		return nil, err
	}

	err = rt.addRenter(r)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// newRenterTester creates a ready-to-use renter tester with money in the
// wallet.
func newRenterTester(name string) (*renterTester, error) {
	testdir := build.TempDir("renter", name)
	rt, err := newRenterTesterNoRenter(testdir)
	if err != nil {
		return nil, err
	}

	rl := ratelimit.NewRateLimit(0, 0, 0)
	tus := NewSkynetTUSInMemoryUploadStore()
	r, errChan := New(rt.gateway, rt.cs, rt.wallet, rt.tpool, rt.mux, tus, rl, filepath.Join(testdir, skymodules.RenterDir))
	if err := <-errChan; err != nil {
		return nil, err
	}
	err = rt.addRenter(r)
	if err != nil {
		return nil, err
	}
	return rt, nil
}

// newRenterTesterNoRenter creates all the modules for the renter tester except
// the renter. A renter will need to be added and blocks mined to add money to
// the wallet.
func newRenterTesterNoRenter(testdir string) (*renterTester, error) {
	// Create the siamux
	siaMuxDir := filepath.Join(testdir, modules.SiaMuxDir)
	mux, err := modules.NewSiaMux(siaMuxDir, testdir, "localhost:0", "localhost:0")
	if err != nil {
		return nil, err
	}

	// Create the skymodules.
	g, err := gateway.New("localhost:0", false, filepath.Join(testdir, modules.GatewayDir))
	if err != nil {
		return nil, err
	}
	cs, errChan := consensus.New(g, false, filepath.Join(testdir, modules.ConsensusDir))
	if err := <-errChan; err != nil {
		return nil, err
	}
	tp, err := transactionpool.New(cs, g, filepath.Join(testdir, modules.TransactionPoolDir))
	if err != nil {
		return nil, err
	}
	w, err := wallet.New(cs, tp, filepath.Join(testdir, modules.WalletDir))
	if err != nil {
		return nil, err
	}
	key := crypto.GenerateSiaKey(crypto.TypeDefaultWallet)
	_, err = w.Encrypt(key)
	if err != nil {
		return nil, err
	}
	err = w.Unlock(key)
	if err != nil {
		return nil, err
	}
	m, err := miner.New(cs, tp, w, filepath.Join(testdir, modules.MinerDir))
	if err != nil {
		return nil, err
	}

	// Assemble all pieces into a renter tester.
	return &renterTester{
		mux: mux,

		cs:      cs,
		gateway: g,
		miner:   m,
		tpool:   tp,
		wallet:  w,

		dir: testdir,
	}, nil
}

// newRenterTesterWithDependency creates a ready-to-use renter tester with money
// in the wallet.
func newRenterTesterWithDependency(name string, deps skymodules.SkydDependencies) (*renterTester, error) {
	testdir := build.TempDir("renter", name)
	rt, err := newRenterTesterNoRenter(testdir)
	if err != nil {
		return nil, err
	}

	// Create the siamux
	siaMuxDir := filepath.Join(testdir, modules.SiaMuxDir)
	mux, err := modules.NewSiaMux(siaMuxDir, testdir, "localhost:0", "localhost:0")
	if err != nil {
		return nil, err
	}

	r, err := newRenterWithDependency(rt.gateway, rt.cs, rt.wallet, rt.tpool, mux, filepath.Join(testdir, skymodules.RenterDir), deps)
	if err != nil {
		return nil, err
	}
	err = rt.addRenter(r)
	if err != nil {
		return nil, err
	}
	return rt, nil
}

// newRenterWithDependency creates a Renter with custom dependency
func newRenterWithDependency(g modules.Gateway, cs modules.ConsensusSet, wallet modules.Wallet, tpool modules.TransactionPool, mux *siamux.SiaMux, persistDir string, deps skymodules.SkydDependencies) (*Renter, error) {
	hdb, errChan := hostdb.NewCustomHostDB(g, cs, tpool, mux, persistDir, deps)
	if err := <-errChan; err != nil {
		return nil, err
	}
	rl := ratelimit.NewRateLimit(0, 0, 0)
	contractSet, err := proto.NewContractSet(filepath.Join(persistDir, "contracts"), rl, modules.ProdDependencies)
	if err != nil {
		return nil, err
	}

	logger, err := persist.NewFileLogger(filepath.Join(persistDir, "contractor.log"))
	if err != nil {
		return nil, err
	}

	hc, errChan := contractor.NewCustomContractor(cs, wallet, tpool, hdb, persistDir, contractSet, logger, deps)
	if err := <-errChan; err != nil {
		return nil, err
	}
	tus := NewSkynetTUSInMemoryUploadStore()
	renter, errChan := NewCustomRenter(g, cs, tpool, hdb, wallet, hc, mux, tus, persistDir, rl, deps)
	return renter, <-errChan
}

// TestRenterCanAccessEphemeralAccountHostSettings verifies that the renter has
// access to the host's external settings and that they include the new
// ephemeral account setting fields.
func TestRenterCanAccessEphemeralAccountHostSettings(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	rt, err := newRenterTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Add a host to the test group
	h, err := rt.addHost(t.Name())
	if err != nil {
		t.Fatal(err)
	}

	hostEntry, found, err := rt.renter.staticHostDB.Host(h.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("Expected the newly added host to be found in the hostDB")
	}

	if hostEntry.EphemeralAccountExpiry != modules.DefaultEphemeralAccountExpiry {
		t.Fatal("Unexpected account expiry")
	}

	if !hostEntry.MaxEphemeralAccountBalance.Equals(modules.DefaultMaxEphemeralAccountBalance) {
		t.Fatal("Unexpected max account balance")
	}
}

// TestRenterPricesDivideByZero verifies that the Price Estimation catches
// divide by zero errors.
func TestRenterPricesDivideByZero(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	rt, err := newRenterTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Confirm price estimation returns error if there are no hosts available
	_, _, err = rt.renter.PriceEstimation(skymodules.Allowance{})
	if err == nil {
		t.Fatal("Expected error due to no hosts")
	}

	// Add a host to the test group
	_, err = rt.addHost(t.Name())
	if err != nil {
		t.Fatal(err)
	}

	// Confirm price estimation does not return an error now that there is a
	// host available
	_, _, err = rt.renter.PriceEstimation(skymodules.Allowance{})
	if err != nil {
		t.Fatal(err)
	}
}

// TestPaySkynetFee is a unit test for paySkynetFee.
func TestPaySkynetFee(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	testDir := build.TempDir("renter", t.Name())
	fileName := "test"

	// Create a new history.
	sh, err := NewSpendingHistory(testDir, fileName)
	if err != nil {
		t.Fatal(err)
	}

	// Declare helper for contract creation.
	randomContract := func() skymodules.RenterContract {
		return skymodules.RenterContract{
			DownloadSpending:    types.NewCurrency64(fastrand.Uint64n(100)),
			FundAccountSpending: types.NewCurrency64(fastrand.Uint64n(100)),
			MaintenanceSpending: skymodules.MaintenanceSpending{
				AccountBalanceCost:   types.NewCurrency64(fastrand.Uint64n(100)),
				FundAccountCost:      types.NewCurrency64(fastrand.Uint64n(100)),
				UpdatePriceTableCost: types.NewCurrency64(fastrand.Uint64n(100)),
			},
			StorageSpending: types.NewCurrency64(fastrand.Uint64n(100)),
			UploadSpending:  types.NewCurrency64(fastrand.Uint64n(100)),
		}
	}

	// Create a contract.
	contracts := []skymodules.RenterContract{
		randomContract(),
	}

	// Helper to compute spending of contracts.
	spending := func(contracts []skymodules.RenterContract) types.Currency {
		var spending types.Currency
		for _, c := range contracts {
			spending = spending.Add(c.SkynetSpending())
		}
		return spending
	}

	// Helper to compute the fee from a given spending delta.
	fee := func(delta types.Currency) types.Currency {
		return delta.Div64(5) // 20%
	}

	// Create an address.
	var uh types.UnlockHash
	fastrand.Read(uh[:])

	// Create a test sender.
	ts := &testSiacoinSender{}

	// Dummy log.
	log, err := persist.NewLogger(ioutil.Discard)
	if err != nil {
		t.Fatal(err)
	}

	// Pay a skynet fee but the time is now so not enough time has passed.
	threshold := types.ZeroCurrency
	expectedTime := time.Now()
	sh.AddSpending(types.ZeroCurrency, nil, expectedTime)
	err = paySkynetFee(sh, ts, contracts, uh, threshold, log)
	if err != nil {
		t.Fatal(err)
	}
	if ls, lsa := ts.LastSend(); !ls.IsZero() || lsa != (types.UnlockHash{}) {
		t.Fatal("money was paid even though it shouldn't", ls, lsa)
	}
	if ls, lsbd := sh.LastSpending(); !ls.IsZero() || lsbd != expectedTime {
		t.Fatal("history shouldn't have been updated", ls, lsbd, expectedTime)
	}

	// Last spending was 48 hours ago which is enough.
	expectedTime = time.Now().AddDate(0, 0, -2)
	sh.AddSpending(types.ZeroCurrency, nil, expectedTime)
	err = paySkynetFee(sh, ts, contracts, uh, threshold, log)
	if err != nil {
		t.Fatal(err)
	}
	expectedSpending := spending(contracts)
	expectedFee := fee(expectedSpending)
	if ls, lsa := ts.LastSend(); !ls.Equals(expectedFee) || lsa != uh {
		t.Fatal("wrong payment", ls, expectedFee, lsa, uh)
	}
	if ls, lsbd := sh.LastSpending(); !ls.Equals(expectedSpending) || lsbd.Before(expectedTime) {
		t.Fatal("wrong history", ls, expectedFee, lsbd, expectedTime)
	}

	// Add another contract.
	contracts = append(contracts, randomContract())

	// Time is 48 hours ago but spending didn't change. Nothing happens.
	expectedTime = time.Now().AddDate(0, 0, -2)
	sh.AddSpending(expectedSpending, nil, expectedTime)
	oldContracts := contracts[:1]
	err = paySkynetFee(sh, ts, oldContracts, uh, threshold, log)
	if err != nil {
		t.Fatal(err)
	}
	if ls, lsa := ts.LastSend(); !ls.Equals(expectedFee) || lsa != uh {
		t.Fatal("wrong payment", ls, expectedFee, lsa, uh)
	}
	if ls, lsbd := sh.LastSpending(); !ls.Equals(expectedSpending) || lsbd != expectedTime {
		t.Fatal("wrong history", ls, expectedSpending, lsbd, expectedTime)
	}

	// Spending increased. Payment expected.
	err = paySkynetFee(sh, ts, contracts, uh, threshold, log)
	if err != nil {
		t.Fatal(err)
	}
	expectedSpending = spending(contracts)
	expectedFee = fee(expectedSpending.Sub(spending(oldContracts)))
	if ls, lsa := ts.LastSend(); !ls.Equals(expectedFee) || lsa != uh {
		t.Fatal("wrong payment", ls, expectedFee, lsa, uh)
	}
	if ls, lsbd := sh.LastSpending(); !ls.Equals(expectedSpending) || lsbd.Before(expectedTime) {
		t.Fatal("wrong history", ls, expectedSpending, lsbd, expectedTime)
	}

	// Add another contract.
	contracts = append(contracts, randomContract())
	oldContracts = contracts[:2]

	// Spending increased again but the threshold is too low.
	oldExpectedSpending := expectedSpending
	oldExpectedFee := expectedFee
	expectedSpending = spending(contracts)
	expectedFee = fee(expectedSpending.Sub(spending(oldContracts)))
	threshold = expectedFee.Add64(1)
	err = paySkynetFee(sh, ts, contracts, uh, threshold, log)
	if err != nil {
		t.Fatal(err)
	}
	if ls, lsa := ts.LastSend(); !ls.Equals(oldExpectedFee) || lsa != uh {
		t.Fatal("wrong payment", ls, oldExpectedFee, lsa, uh)
	}
	if ls, lsbd := sh.LastSpending(); !ls.Equals(oldExpectedSpending) || lsbd.Before(expectedTime) {
		t.Fatal("wrong history", ls, oldExpectedSpending, lsbd, expectedTime)
	}
}

// TestRenterLogsDistributionTrackers is a unit test that verifies the renter
// periodically logs the contents of some distribution trackers of interest to a
// log file
func TestRenterLogsDistributionTrackers(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	rt, err := newRenterTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// create the log file path
	logFilePath := filepath.Join(filepath.Join(rt.dir, skymodules.RenterDir), distributionsLogFile)

	// read from the log file in a retry loop and assert we can find the DT
	// snapshot for the RegistryRead DT, this asserts the log file exists and
	// contains meaningful data
	err = build.Retry(60, time.Second, func() error {
		// open file
		file, err := os.Open(logFilePath)
		if err != nil {
			return err
		}
		defer func() {
			err := file.Close()
			if err != nil {
				t.Fatal(err)
			}
		}()

		// create a scanner to read the file line per line
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			// extract the json
			line := scanner.Text()

			// unmarshal it
			var dts skymodules.DistributionTrackerSnapshot
			err = json.Unmarshal([]byte(line), &dts)
			if err != nil {
				return err
			}

			// look for the RegistryRead snapshot
			if dts.Name == "RegistryRead" {
				return nil
			}
		}

		// check whether we encountered an error scanning
		return scanner.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}
