package accounting

import (
	"reflect"
	"testing"
	"time"

	"gitlab.com/skynetlabs/skyd/modules"
	"gitlab.com/skynetlabs/skyd/node"
	"gitlab.com/skynetlabs/skyd/siatest"
)

// TestAccounting probes the /accounting endpoint
func TestAccounting(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Run Subtest for each module
	t.Run("Accounting", func(t *testing.T) {
		testDir := accountingTestDir(t.Name())
		np := node.Accounting(testDir)
		testAccounting(t, np)
	})
	t.Run("FeeManager", func(t *testing.T) {
		testDir := accountingTestDir(t.Name())
		np := node.FeeManager(testDir)
		np.CreateAccounting = true
		testAccounting(t, np)
	})
	t.Run("Host", func(t *testing.T) {
		testDir := accountingTestDir(t.Name())
		np := node.Host(testDir)
		np.CreateAccounting = true
		testAccounting(t, np)
	})
	t.Run("Miner", func(t *testing.T) {
		testDir := accountingTestDir(t.Name())
		np := node.Miner(testDir)
		np.CreateAccounting = true
		testAccounting(t, np)
	})
	t.Run("Renter", func(t *testing.T) {
		testDir := accountingTestDir(t.Name())
		np := node.Renter(testDir)
		np.CreateAccounting = true
		testAccounting(t, np)
	})
	t.Run("Wallet", func(t *testing.T) {
		testDir := accountingTestDir(t.Name())
		np := node.Wallet(testDir)
		np.CreateAccounting = true
		testAccounting(t, np)
	})
}

// testAccounting probes the accounting for a provided node
func testAccounting(t *testing.T, np node.NodeParams) {
	// Create node
	n, err := siatest.NewCleanNode(np)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err = n.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	// Determine node checks
	//
	// Wallet will always be checked
	//
	// Renter is optional based on the node params
	checkRenter := np.CreateRenter || np.Renter != nil

	// Define check function
	checkAccounting := func(actual, expected modules.AccountingInfo) {
		if !reflect.DeepEqual(actual, expected) {
			t.Logf("Expected:\n%v", siatest.PrintJSON(expected))
			t.Logf("Actual:\n%v", siatest.PrintJSON(actual))
			t.Error("unexpected accounting information")
		}
	}

	// Sleep for 2 seconds. This should mean that the accounting information has
	// been persisted twice
	time.Sleep(2 * time.Second)

	// Get the Node accounting. Submitting 0 0 should result in the end time being
	// set as the default and the entire list being returned.
	ag, err := n.AccountingGet(0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// We should have at least 2 entries
	if len(ag) < 2 {
		t.Errorf("Expected 2 entries got %v", len(ag))
	}

	// Get the wallet information
	wg, err := n.WalletGet()
	if err != nil {
		t.Fatal(err)
	}
	wa := modules.WalletAccounting{
		ConfirmedSiacoinBalance: wg.ConfirmedSiacoinBalance,
		ConfirmedSiafundBalance: wg.SiafundBalance,
	}

	// Get the Renter Information
	var ra modules.RenterAccounting
	if checkRenter {
		rg, err := n.RenterGet()
		if err != nil {
			t.Fatal(err)
		}
		_, _, unspentUnallocated := rg.FinancialMetrics.SpendingBreakdown()
		ra = modules.RenterAccounting{
			UnspentUnallocated: unspentUnallocated,
			WithheldFunds:      rg.FinancialMetrics.WithheldFunds,
		}
	}

	// Quick check that the timestamp is sane and not zero
	if ag[0].Timestamp == 0 {
		t.Error("timestamp is 0")
	}
	// Check Accounting
	expected := modules.AccountingInfo{
		Renter:    ra,
		Wallet:    wa,
		Timestamp: ag[0].Timestamp,
	}
	checkAccounting(ag[0], expected)
}
