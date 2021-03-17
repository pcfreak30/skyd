package renter

import (
	"encoding/json"
	"io"
	"sync"

	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

// TODO: To improve reliability, keep the txns in memory until they are
// confirmed and reapply them regularly. After failing too many times, send a
// new txn.

type (
	// spendingHistory tracks the history of the renter spending relevant to
	// skynet fees.
	spendingHistory struct {
		recentSpending spendingEntry

		staticAop *persist.AppendOnlyPersist
		mu        sync.Mutex
	}

	// spendingEntry is the definition of a persisted entry.
	spendingEntry struct {
		// Value resembles the total spending at the time of persisting the
		// entry.
		Value types.Currency `json:"value"`

		// Txn is the txn that was used to pay the delta between the previous
		// spending entry and this one. It's currently not used but in the future
		// it can be used for rebroadcasting the txn.
		Txn []types.Transaction `json:"txn"`

		// Height is the height at which the last entry was saved. That way we
		// can determine whether or not a txn is old enough to be replaced.
		Height types.BlockHeight `json:"height"`
	}
)

var (
	// spendingHistoryMDHeader is the header of the metadata for the persist file
	spendingHistoryMDHeader = types.NewSpecifier("SpendingHistory")
)

// loadSpendingHistory loads the spending history from the reader.
func loadSpendingHistory(r io.Reader) (spendingEntry, error) {
	decoder := json.NewDecoder(r)

	var recentSpending spendingEntry
	for {
		var spending spendingEntry
		err := decoder.Decode(&spending)
		if errors.Contains(err, io.EOF) {
			break
		} else if err != nil {
			return spendingEntry{}, err
		}
		recentSpending = spending
	}
	return recentSpending, nil
}

// NewSpendingHistory creates a new spending history or loads an existing one
// from disk.
func NewSpendingHistory(dir, filename string) (*spendingHistory, error) {
	// Open persistence.
	aop, r, err := persist.NewAppendOnlyPersist(dir, filename, spendingHistoryMDHeader, persist.MetadataVersionv156)
	if err != nil {
		return nil, err
	}
	// Load existing accumulated fees.
	spending, err := loadSpendingHistory(r)
	if err != nil {
		return nil, err
	}
	// TODO: handle init
	return &spendingHistory{
		staticAop:      aop,
		recentSpending: spending,
	}, nil
}

// Close closes the underlying persistence.
func (sh *spendingHistory) Close() error {
	return sh.staticAop.Close()
}

// AddSpending adds a new entry. This includes the value and the txn used to pay
// for the delta since the last value.
func (sh *spendingHistory) AddSpending(spending types.Currency, txn []types.Transaction, bh types.BlockHeight) error {
	sh.mu.Lock()
	defer sh.mu.Unlock()

	// Marshal the entry.
	entry := spendingEntry{
		Height: bh,
		Txn:    txn,
		Value:  spending,
	}
	entryBytes, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	// Write it to disk.
	_, err = sh.staticAop.Write(entryBytes)
	if err != nil {
		return err
	}
	// Update it in memory.
	sh.recentSpending = entry
	return nil
}

// LastSpending returns the last saved spending entry.
func (sh *spendingHistory) LastSpending() (types.Currency, types.BlockHeight) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.recentSpending.Value, sh.recentSpending.Height
}
