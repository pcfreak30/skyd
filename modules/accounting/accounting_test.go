package accounting

import (
	"fmt"
	"reflect"
	"testing"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/siatest/dependencies"
)

// TestAccounting tests the basic functionality of the accounting package
func TestAccounting(t *testing.T) {
	t.Run("AccountingRange", testAccountingRange)

	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Specific Methods
	t.Run("Accounting", testAccounting)
	t.Run("NewCustomAccounting", testNewCustomAccounting)
}

// testAccounting probes the Accounting method
func testAccounting(t *testing.T) {
	// Create new accounting
	testDir := accountingTestDir(t.Name())
	fm, h, m, r, w, _ := testingParams()
	a, err := NewCustomAccounting(fm, h, m, r, w, testDir, &dependencies.AccountingDisablePersistLoop{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err = a.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	// Initial persistence should be empty
	a.mu.Lock()
	p := a.currentInfo
	lenHistroy := len(a.history)
	a.mu.Unlock()
	if !reflect.DeepEqual(p, persistence{}) {
		t.Error("initial persistence should be empty")
	}
	if lenHistroy != 0 {
		t.Error("history should be empty")
	}

	// Check accounting
	ais, err := a.Accounting(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ai := ais[0]
	// Check for a returned value
	expected := modules.AccountingInfo{
		Renter: ai.Renter,
		Wallet: ai.Wallet,
	}
	if !reflect.DeepEqual(ai, expected) {
		t.Error("accounting information is incorrect")
	}
	// Check renter explicitly
	if reflect.DeepEqual(ai.Renter, modules.RenterAccounting{}) {
		t.Error("renter accounting information is empty")
	}
	// Check wallet explicitly
	if reflect.DeepEqual(ai.Wallet, modules.WalletAccounting{}) {
		t.Error("wallet accounting information is empty")
	}

	// Persistence should have been updated but the history should still be empty
	// as a call to Accounting does not persist the update to disk.
	a.mu.Lock()
	p = a.currentInfo
	lenHistroy = len(a.history)
	a.mu.Unlock()
	ep := persistence{
		Renter: p.Renter,
		Wallet: p.Wallet,

		Timestamp: p.Timestamp,
	}
	if !reflect.DeepEqual(p, ep) {
		t.Error("persistence information is incorrect")
	}
	if !reflect.DeepEqual(p.Renter, ai.Renter) {
		t.Error("renter accounting persistence not updated")
	}
	if !reflect.DeepEqual(p.Wallet, ai.Wallet) {
		t.Error("wallet accounting persistence not updated")
	}
	if lenHistroy != 0 {
		t.Error("history should be empty")
	}
}

// testAccountingRange probes the accountingRange function
func testAccountingRange(t *testing.T) {
	// Create a history
	history := []persistence{
		{Timestamp: 2},
		{Timestamp: 3},
		{Timestamp: 4},
		{Timestamp: 5},
		{Timestamp: 6},
	}
	all := len(history)

	// Create range tests
	var start, mid, end int64 = 1, 4, 7
	var rangeTests = []struct {
		start      int64
		end        int64
		numEntries int
	}{
		{start, 0, all},
		{0, end, all},
		{0, start, 0},
		{end, 0, 0},
		{start, mid, 3},
		{mid, end, 3},
		{start, end, all},
	}

	// Run range tests
	for _, rt := range rangeTests {
		// Grab accounting information range
		ais := accountingRange(history, rt.start, rt.end)
		if len(ais) != rt.numEntries {
			test := fmt.Sprintf("Testing start %v, end %v, expected %v", rt.start, rt.end, rt.numEntries)
			t.Log(test)
			t.Errorf("expected %v got %v", rt.numEntries, len(ais))
		}
	}
}

// testNewCustomAccounting probes the NewCustomAccounting function
func testNewCustomAccounting(t *testing.T) {
	// checkNew is a helper function to check NewCustomAccounting
	checkNew := func(fm modules.FeeManager, h modules.Host, m modules.Miner, r modules.Renter, w modules.Wallet, dir string, deps modules.Dependencies, expectedErr error) {
		a, err := NewCustomAccounting(fm, h, m, r, w, dir, deps)
		if err != expectedErr {
			t.Errorf("Expected %v, got %v", expectedErr, err)
		}
		if a == nil {
			return
		}
		err = a.Close()
		if err != nil {
			t.Error(err)
		}
	}

	// Create testing parameters
	testDir := accountingTestDir(t.Name())
	fm, h, m, r, w, deps := testingParams()

	// Base Case
	checkNew(nil, nil, nil, nil, w, testDir, deps, nil)

	// Check for nil wallet
	checkNew(nil, nil, nil, nil, nil, testDir, deps, errNilWallet)

	// Check for blank persistDir
	checkNew(nil, nil, nil, nil, w, "", deps, errNilPersistDir)

	// Check for nil Dependencies
	checkNew(nil, nil, nil, nil, w, testDir, nil, errNilDeps)

	// Test optional modules
	//
	// FeeManager
	checkNew(fm, nil, nil, nil, w, testDir, deps, nil)
	// Host
	checkNew(nil, h, nil, nil, w, testDir, deps, nil)
	// Miner
	checkNew(nil, nil, m, nil, w, testDir, deps, nil)
	// Renter
	checkNew(nil, nil, nil, r, w, testDir, deps, nil)
}
