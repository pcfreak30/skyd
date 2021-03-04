package accounting

import (
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/threadgroup"
)

var (
	// errNilDeps is the error returned when no dependencies are provided
	errNilDeps = errors.New("dependencies cannot be nil")

	// errNilPersistDir is the error returned when no persistDir is provided
	errNilPersistDir = errors.New("persistDir cannot by blank")

	// errNilWallet is the error returned when the wallet is nil
	errNilWallet = errors.New("wallet cannot be nil")

	// errNoEntriesFound is the error returned when no entries are found for
	// a given range
	errNoEntriesFound = errors.New("no entries found for given range")
)

// Accounting contains the information needed for providing accounting
// information about a Sia node.
type Accounting struct {
	// Modules whose accounting information is tracked
	staticFeeManager modules.FeeManager
	staticHost       modules.Host
	staticMiner      modules.Miner
	staticRenter     modules.Renter
	staticWallet     modules.Wallet

	// currentInfo is the current in memory accounting information. This may or
	// may not have been persisted yet.
	currentInfo persistence

	// history is the entire persisted history of the accounting information
	//
	// NOTE: We only persist a small amount of data daily so this is OK and we are
	// not concerned with this struct taking up much memory.
	history []persistence

	// staticPersistDir is the accounting persist location on disk
	staticPersistDir string

	// Utilities
	staticAOP  *persist.AppendOnlyPersist
	staticDeps modules.Dependencies
	staticLog  *persist.Logger
	staticTG   threadgroup.ThreadGroup

	mu sync.Mutex
}

// NewCustomAccounting initializes the accounting module with custom
// dependencies
func NewCustomAccounting(fm modules.FeeManager, h modules.Host, m modules.Miner, r modules.Renter, w modules.Wallet, persistDir string, deps modules.Dependencies) (*Accounting, error) {
	// Check that at least the wallet is not nil
	if w == nil {
		return nil, errNilWallet
	}

	// Check required parameters
	if persistDir == "" {
		return nil, errNilPersistDir
	}
	if deps == nil {
		return nil, errNilDeps
	}

	// Initialize the accounting
	a := &Accounting{
		staticFeeManager: fm,
		staticHost:       h,
		staticMiner:      m,
		staticRenter:     r,
		staticWallet:     w,

		staticPersistDir: persistDir,

		staticDeps: deps,
	}

	// Initialize the persistence
	err := a.initPersist()
	if err != nil {
		return nil, errors.AddContext(err, "unable to initialize the persistence")
	}

	// Launch background thread to persist the accounting information
	if !a.staticDeps.Disrupt("DisablePersistLoop") {
		go a.callThreadedPersistAccounting()
	}
	return a, nil
}

// Accounting returns the current accounting information
func (a *Accounting) Accounting(start, end int64) ([]modules.AccountingInfo, error) {
	err := a.staticTG.Add()
	if err != nil {
		return nil, err
	}
	defer a.staticTG.Done()

	// Update the accounting information
	ai, err := a.callUpdateAccounting()
	if err != nil {
		return nil, errors.AddContext(err, "unable to update the accounting information")
	}

	// If no start of end time is provided, then return the current snapshot
	if start == 0 && end == 0 {
		return []modules.AccountingInfo{ai}, nil
	}

	// Return the requested range
	a.mu.Lock()
	history := a.history
	a.mu.Unlock()
	ais := accountingRange(history, start, end)
	if len(ais) == 0 {
		return nil, errNoEntriesFound
	}
	return ais, nil
}

// Close closes the accounting module
//
// NOTE: It will not call close on any of the modules it is tracking. Those
// modules are responsible for closing themselves independently.
func (a *Accounting) Close() error {
	return a.staticTG.Stop()
}

// accountingRange returns a range of accounting information from a provided
// history
func accountingRange(history []persistence, start, end int64) []modules.AccountingInfo {
	// Find the range of entries requested
	var ais []modules.AccountingInfo
	for _, entry := range history {
		// Break if we reach a Timestamp that is older than end if end is provided
		if end != 0 && entry.Timestamp > end {
			break
		}
		// If the Timestamp is before start then continue
		if entry.Timestamp < start {
			continue
		}
		// Entry found that is within the range. Append it to the list.
		ais = append(ais, modules.AccountingInfo{
			Renter: entry.Renter,
			Wallet: entry.Wallet,
		})
	}
	return ais
}

// callUpdateAccounting updates the accounting information
func (a *Accounting) callUpdateAccounting() (modules.AccountingInfo, error) {
	var ai modules.AccountingInfo

	// Get Renter information
	//
	// NOTE: renter is optional so can be nil
	var renterErr error
	if a.staticRenter != nil {
		var spending modules.ContractorSpending
		spending, renterErr = a.staticRenter.PeriodSpending()
		if renterErr == nil {
			_, _, unspentUnallocated := spending.SpendingBreakdown()
			ai.Renter.UnspentUnallocated = unspentUnallocated
			ai.Renter.WithheldFunds = spending.WithheldFunds
		}
	}

	// Get Wallet information
	sc, sf, _, walletErr := a.staticWallet.ConfirmedBalance()
	if walletErr == nil {
		ai.Wallet.ConfirmedSiacoinBalance = sc
		ai.Wallet.ConfirmedSiafundBalance = sf
	}

	// Update the Accounting state
	err := errors.Compose(renterErr, walletErr)
	if err == nil {
		a.mu.Lock()
		a.currentInfo.Renter = ai.Renter
		a.currentInfo.Wallet = ai.Wallet
		a.currentInfo.Timestamp = time.Now().Unix()
		a.mu.Unlock()
	}
	return ai, err
}

// Enforce that Accounting satisfies the modules.Accounting interface.
var _ modules.Accounting = (*Accounting)(nil)
