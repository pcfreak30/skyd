package accounting

import (
	"fmt"
	"reflect"
	"testing"

	"gitlab.com/SkynetLabs/skyd/siatest/dependencies"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/modules"
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
	h, m, r, w, _ := testingParams()
	a, err := NewCustomAccounting(h, m, r, w, testDir, &dependencies.AccountingDisablePersistLoop{})
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
	lenHistroy := len(a.history)
	a.mu.Unlock()
	if lenHistroy != 0 {
		t.Error("history should be empty")
	}

	// Check accounting
	ais, err := a.Accounting(0, DefaultEndRangeTime)
	if err != nil {
		t.Fatal(err)
	}
	ai := ais[0]

	// Check renter explicitly
	if reflect.DeepEqual(ai.Renter, skymodules.RenterAccounting{}) {
		t.Error("renter accounting information is empty")
	}
	// Check wallet explicitly
	if reflect.DeepEqual(ai.Wallet, skymodules.WalletAccounting{}) {
		t.Error("wallet accounting information is empty")
	}
	// Check timestamp explicitly
	if ai.Timestamp == 0 {
		t.Error("timestamp not set")
	}

	// The history should still be empty as a call to Accounting does not persist
	// the update to disk.
	a.mu.Lock()
	lenHistroy = len(a.history)
	a.mu.Unlock()
	if lenHistroy != 0 {
		t.Error("history should be empty")
	}
}

// testAccountingRange probes the accountingRange function
func testAccountingRange(t *testing.T) {
	// Create a history
	var first, mid, last int64 = 2, 4, 10
	history := []skymodules.AccountingInfo{
		{Timestamp: first},
		{Timestamp: 3},
		{Timestamp: mid},
		{Timestamp: mid},
		{Timestamp: mid},
		{Timestamp: last},
	}
	all := len(history)

	// Create range tests
	before := first - 1
	after := last + 1
	var rangeTests = []struct {
		start      int64
		end        int64
		numEntries int
	}{
		// Conditions that should return all
		{0, after, all},
		{0, DefaultEndRangeTime, all},
		{0, last, all},
		{before, after, all},
		{before, last, all},
		{first, last, all},
		{first, after, all},

		// Conditions that should return none
		{0, 0, 0},
		{0, before, 0},
		{before, before, 0},
		{mid + 1, last - 1, 0},
		{after, after, 0},

		// Conditions that should return a set amount
		{0, first, 1},
		{first, first, 1},
		{before, mid, 5},
		{first, mid, 5},
		{mid, mid, 3},
		{mid, last, 4},
		{mid, after, 4},
		{last, last, 1},
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

	// Test build.Critical for end < start
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected build.critical")
		}
	}()
	accountingRange(nil, 1, 0)
}

// testNewCustomAccounting probes the NewCustomAccounting function
func testNewCustomAccounting(t *testing.T) {
	// checkNew is a helper function to check NewCustomAccounting
	checkNew := func(h modules.Host, m modules.Miner, r skymodules.Renter, w modules.Wallet, dir string, deps modules.Dependencies, expectedErr error) {
		a, err := NewCustomAccounting(h, m, r, w, dir, deps)
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
	h, m, r, w, deps := testingParams()

	// Base Case
	checkNew(nil, nil, nil, w, testDir, deps, nil)

	// Check for nil wallet
	checkNew(nil, nil, nil, nil, testDir, deps, errNilWallet)

	// Check for blank persistDir
	checkNew(nil, nil, nil, w, "", deps, errNilPersistDir)

	// Check for nil Dependencies
	checkNew(nil, nil, nil, w, testDir, nil, errNilDeps)

	// Test optional modules
	//
	// Host
	checkNew(h, nil, nil, w, testDir, deps, nil)
	// Miner
	checkNew(nil, m, nil, w, testDir, deps, nil)
	// Renter
	checkNew(nil, nil, r, w, testDir, deps, nil)
}
