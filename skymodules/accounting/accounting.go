package accounting

import (
	"math"
	"sort"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/threadgroup"
	"gitlab.com/skynetlabs/skyd/build"
	"gitlab.com/skynetlabs/skyd/skymodules"
)

const (
	// DefaultEndRangeTime is the default end time used when one isn't provided
	// by the user.
	DefaultEndRangeTime = math.MaxInt64
)

var (
	// ErrInvalidRange is the error returned if the end time is before the start
	// time.
	ErrInvalidRange = errors.New("invalid range, end must be after start")

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
	staticRenter     skymodules.Renter
	staticWallet     modules.Wallet

	// history is the entire persisted history of the accounting information
	//
	// NOTE: We only persist a small amount of data daily so this is OK and we are
	// not concerned with this struct taking up much memory.
	history []skymodules.AccountingInfo

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
func NewCustomAccounting(fm modules.FeeManager, h modules.Host, m modules.Miner, r skymodules.Renter, w modules.Wallet, persistDir string, deps modules.Dependencies) (*Accounting, error) {
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
func (a *Accounting) Accounting(start, end int64) ([]skymodules.AccountingInfo, error) {
	err := a.staticTG.Add()
	if err != nil {
		return nil, err
	}
	defer a.staticTG.Done()

	// Check start and end range
	if end < start {
		return nil, ErrInvalidRange
	}

	// Update the accounting information
	ai, err := a.callUpdateAccounting()
	if err != nil {
		return nil, errors.AddContext(err, "unable to update the accounting information")
	}

	// Return the requested range
	a.mu.Lock()
	history := append(a.history, ai)
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
func accountingRange(history []skymodules.AccountingInfo, start, end int64) []skymodules.AccountingInfo {
	// Sanity check
	if end < start {
		build.Critical(ErrInvalidRange)
		return nil
	}

	// Find Start and end indexes
	startIndex := sort.Search(len(history), func(i int) bool {
		return history[i].Timestamp >= start
	})
	endIndex := sort.Search(len(history), func(i int) bool {
		return history[i].Timestamp > end
	})

	// Return range
	if endIndex == len(history) {
		return history[startIndex:]
	}
	return history[startIndex:endIndex]
}

// callUpdateAccounting updates the accounting information
func (a *Accounting) callUpdateAccounting() (skymodules.AccountingInfo, error) {
	var ai skymodules.AccountingInfo
	ai.Timestamp = time.Now().Unix()

	// Get Renter information
	//
	// NOTE: renter is optional so can be nil
	var renterErr error
	if a.staticRenter != nil {
		var spending skymodules.ContractorSpending
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

	return ai, errors.Compose(renterErr, walletErr)
}

// Enforce that Accounting satisfies the skymodules.Accounting interface.
var _ skymodules.Accounting = (*Accounting)(nil)
