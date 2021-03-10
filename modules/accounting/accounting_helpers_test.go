package accounting

import (
	"math"
	"os"
	"path/filepath"

	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/skynetlabs/skyd/build"
	"gitlab.com/skynetlabs/skyd/modules"
	"gitlab.com/skynetlabs/skyd/modules/feemanager"
	"gitlab.com/skynetlabs/skyd/modules/host"
	"gitlab.com/skynetlabs/skyd/modules/miner"
	"gitlab.com/skynetlabs/skyd/modules/renter"
	"gitlab.com/skynetlabs/skyd/modules/wallet"
	"gitlab.com/skynetlabs/skyd/persist"
	"gitlab.com/skynetlabs/skyd/types"
)

// accountingTestDir joins the provided directories and prefixes them with the
// Sia testing directory, removing any files or directories that previously
// existed at that location.
func accountingTestDir(dirs ...string) string {
	path := build.TempDir("accounting", filepath.Join(dirs...))
	err := os.RemoveAll(path)
	if err != nil {
		panic(err)
	}
	err = os.MkdirAll(path, persist.DefaultDiskPermissionsTest)
	if err != nil {
		panic(err)
	}
	return path
}

// newTestAccounting creates a new Accounting module for testing
func newTestAccounting(testDir string) (*Accounting, error) {
	fm, h, m, r, w, deps := testingParams()
	a, err := NewCustomAccounting(fm, h, m, r, w, testDir, deps)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// randomCurrency is a helper that returns a random currency value
func randomCurrency() types.Currency {
	return types.NewCurrency64(fastrand.Uint64n(math.MaxUint64))
}

// testingParams returns the minimum required parameters for creating an
// Accounting module for testing.
func testingParams() (modules.FeeManager, modules.Host, modules.Miner, modules.Renter, modules.Wallet, modules.Dependencies) {
	fm := &feemanager.FeeManager{}
	h := &host.Host{}
	m := &miner.Miner{}
	r := &mockRenter{}
	w := &mockWallet{}
	deps := &modules.ProductionDependencies{}
	return fm, h, m, r, w, deps
}

// mockRenter is a helper for Accounting unit tests
type mockRenter struct {
	*renter.Renter
}

// PeriodSpending mocks the Renter's PeriodSpending
func (mr *mockRenter) PeriodSpending() (modules.ContractorSpending, error) {
	return modules.ContractorSpending{
		ContractFees:     randomCurrency(),
		DownloadSpending: randomCurrency(),
		StorageSpending:  randomCurrency(),
		TotalAllocated:   randomCurrency(),
		UploadSpending:   randomCurrency(),
		Unspent:          randomCurrency(),
		WithheldFunds:    randomCurrency(),
	}, nil
}

// mockWallet is a helper for Accounting unit tests
type mockWallet struct {
	*wallet.Wallet
}

// ConfirmedBalance mocks the Wallet's ConfirmedBalance
func (mw *mockWallet) ConfirmedBalance() (types.Currency, types.Currency, types.Currency, error) {
	sc := randomCurrency()
	sf := randomCurrency()
	return sc, sf, types.ZeroCurrency, nil
}
