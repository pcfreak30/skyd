package contractor

import (
	"io/ioutil"
	"reflect"
	"testing"

	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/persist"
	"go.sia.tech/siad/types"
)

// TestDeadScoreCheck is a unit test for deadScoreCheck.
func TestDeadScoreCheck(t *testing.T) {
	t.Parallel()

	goodUtility := skymodules.ContractUtility{
		GoodForUpload: true,
		GoodForRenew:  true,
	}
	badUtility := skymodules.ContractUtility{}

	utility, update := deadScoreCheck(goodUtility, types.NewCurrency64(0))
	if !update {
		t.Fatal(update)
	}
	if !reflect.DeepEqual(utility, badUtility) {
		t.Fatal("wrong utility")
	}
	utility, update = deadScoreCheck(goodUtility, types.NewCurrency64(1))
	if !update {
		t.Fatal(update)
	}
	if !reflect.DeepEqual(utility, badUtility) {
		t.Fatal("wrong utility")
	}
	utility, update = deadScoreCheck(goodUtility, types.NewCurrency64(2))
	if update {
		t.Fatal(update)
	}
	if !reflect.DeepEqual(utility, goodUtility) {
		t.Fatal("wrong utility")
	}
}

// TestStorageGougingCheck is a unit test for storageGougingCheck.
func TestStorageGougingCheck(t *testing.T) {
	t.Parallel()

	allowance := skymodules.DefaultAllowance
	allowance.MaxStoragePrice = types.SiacoinPrecision
	host := skymodules.HostDBEntry{
		HostExternalSettings: modules.HostExternalSettings{
			StoragePrice: allowance.MaxStoragePrice,
		},
	}
	goodUtility := skymodules.ContractUtility{
		GoodForUpload: true,
		GoodForRenew:  true,
	}
	badUtility := skymodules.ContractUtility{}
	goodContract := skymodules.RenterContract{Utility: goodUtility}

	// Below max price cases first.
	u, update := storageGougingCheck(goodContract, allowance, host, 0)
	if update {
		t.Fatal("wrong update", update)
	}
	if !reflect.DeepEqual(u, goodUtility) {
		t.Fatal("wrong utility", u, goodUtility)
	}
	u, update = storageGougingCheck(goodContract, allowance, host, 1)
	if update {
		t.Fatal("wrong update", update)
	}
	if !reflect.DeepEqual(u, goodUtility) {
		t.Fatal("wrong utility", u)
	}

	// Above max price cases.
	host.StoragePrice = host.StoragePrice.Add64(1)
	u, update = storageGougingCheck(goodContract, allowance, host, 0)
	if update {
		t.Fatal("wrong update", update)
	}
	if !reflect.DeepEqual(u, goodUtility) {
		t.Fatal("wrong utility", u)
	}
	u, update = storageGougingCheck(goodContract, allowance, host, 1)
	if !update {
		t.Fatal("wrong update", update)
	}
	if !reflect.DeepEqual(u, badUtility) {
		t.Fatal("wrong utility", u)
	}
}

// TestUpForRenewalCheck is a unit test for upForRenwalCheck.
func TestUpForRenewalCheck(t *testing.T) {
	t.Parallel()

	logger, err := persist.NewLogger(ioutil.Discard)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		renewWindow types.BlockHeight
		blockHeight types.BlockHeight
		endHeight   types.BlockHeight

		gfu    bool
		gfr    bool
		update bool
	}{
		// Not renewing.
		{
			blockHeight: 0,
			endHeight:   100,
			renewWindow: 10,

			gfu:    true,
			gfr:    true,
			update: false,
		},
		// One block before second half of renew window.
		{
			blockHeight: 0,
			endHeight:   11,
			renewWindow: 20,

			gfu:    true,
			gfr:    true,
			update: false,
		},
		// Beginning of second half.
		{
			blockHeight: 1,
			endHeight:   11,
			renewWindow: 20,

			gfu:    false,
			gfr:    true,
			update: true,
		},
		// One block in second half.
		{
			blockHeight: 2,
			endHeight:   11,
			renewWindow: 20,

			gfu:    false,
			gfr:    true,
			update: true,
		},
	}

	for i, test := range tests {
		u, update := upForRenewalCheck(skymodules.RenterContract{
			EndHeight: test.endHeight,
			Utility: skymodules.ContractUtility{
				GoodForUpload: true,
				GoodForRenew:  true,
			},
		}, test.renewWindow, test.blockHeight, logger)

		if update != test.update {
			t.Errorf("%v (update): %v != %v", i, update, test.update)
		}
		if u.GoodForRenew != test.gfr {
			t.Errorf("%v (gfr): %v != %v", i, u.GoodForRenew, test.gfr)
		}
		if u.GoodForUpload != test.gfu {
			t.Errorf("%v (gfu): %v != %v", i, u.GoodForUpload, test.gfu)
		}
	}
}

// TestBasicUtilityChecks is a unit test for basicUtilityChecks.
func TestBasicUtilityChecks(t *testing.T) {
	t.Parallel()

	log, _ := persist.NewLogger(ioutil.Discard)
	c := &Contractor{staticLog: log}

	goodContract := skymodules.RenterContract{
		ID: types.FileContractID{},
		Utility: skymodules.ContractUtility{
			GoodForUpload: true,
			GoodForRenew:  true,
		},
		Transaction: types.Transaction{
			FileContractRevisions: []types.FileContractRevision{
				{
					NewFileSize: 0,
				},
			},
		},
	}

	tests := []struct {
		storagePrice    types.Currency
		maxStoragePrice types.Currency
		minScoreGFR     types.Currency
		minScoreGFU     types.Currency
		score           types.Currency
		contractSize    uint64

		expectedUtilities    skymodules.ContractUtility
		expectedUpdateStatus utilityUpdateStatus
	}{
		{
			// No update needed
			storagePrice:    types.SiacoinPrecision,
			maxStoragePrice: types.SiacoinPrecision,
			minScoreGFR:     types.SiacoinPrecision,
			minScoreGFU:     types.SiacoinPrecision,
			score:           types.SiacoinPrecision,
			contractSize:    1,

			expectedUtilities:    goodContract.Utility,
			expectedUpdateStatus: noUpdate,
		},
		{
			// Not GFU and not GFR - payment contract
			storagePrice:    types.SiacoinPrecision,
			maxStoragePrice: types.SiacoinPrecision,
			minScoreGFR:     types.SiacoinPrecision.Add64(1),
			minScoreGFU:     types.SiacoinPrecision.Add64(1),
			score:           types.SiacoinPrecision,
			contractSize:    0,

			expectedUtilities: skymodules.ContractUtility{
				GoodForUpload: false,
				GoodForRenew:  false,
			},
			expectedUpdateStatus: suggestedUtilityUpdate,
		},
		{
			// GFU but not GFR
			storagePrice:    types.SiacoinPrecision,
			maxStoragePrice: types.SiacoinPrecision,
			minScoreGFR:     types.SiacoinPrecision.Add64(1),
			minScoreGFU:     types.SiacoinPrecision,
			score:           types.SiacoinPrecision,
			contractSize:    1,

			expectedUtilities: skymodules.ContractUtility{
				GoodForUpload: false,
				GoodForRenew:  false,
			},
			expectedUpdateStatus: suggestedUtilityUpdate,
		},
		{
			// GFR but not GFU
			storagePrice:    types.SiacoinPrecision,
			maxStoragePrice: types.SiacoinPrecision,
			minScoreGFR:     types.SiacoinPrecision,
			minScoreGFU:     types.SiacoinPrecision.Add64(1),
			score:           types.SiacoinPrecision,
			contractSize:    1,

			expectedUtilities: skymodules.ContractUtility{
				GoodForUpload: false,
				GoodForRenew:  true,
			},
			expectedUpdateStatus: necessaryUtilityUpdate,
		},
		{
			// Storage price too high
			storagePrice:    types.SiacoinPrecision.Add64(1),
			maxStoragePrice: types.SiacoinPrecision,
			minScoreGFR:     types.SiacoinPrecision,
			minScoreGFU:     types.SiacoinPrecision,
			score:           types.SiacoinPrecision,
			contractSize:    1,

			expectedUtilities: skymodules.ContractUtility{
				GoodForUpload: false,
				GoodForRenew:  true,
			},
			expectedUpdateStatus: necessaryUtilityUpdate,
		},
		{
			// GFU and not GFR but also expensive.
			storagePrice:    types.SiacoinPrecision.Add64(1),
			maxStoragePrice: types.SiacoinPrecision,
			minScoreGFR:     types.SiacoinPrecision.Add64(1),
			minScoreGFU:     types.SiacoinPrecision,
			score:           types.SiacoinPrecision,
			contractSize:    1,

			expectedUtilities: skymodules.ContractUtility{
				GoodForUpload: false,
				GoodForRenew:  false,
			},
			expectedUpdateStatus: necessaryUtilityUpdate,
		},
	}

	// Run tests.
	for i, test := range tests {
		sb := skymodules.HostScoreBreakdown{
			Score: test.score,
		}
		host := skymodules.HostDBEntry{
			HostExternalSettings: modules.HostExternalSettings{
				StoragePrice: test.storagePrice,
			},
		}
		c.allowance = skymodules.Allowance{
			MaxStoragePrice: test.maxStoragePrice,
		}
		goodContract.Transaction.FileContractRevisions[0].NewFileSize = test.contractSize

		u, us := c.managedBasicUtilityChecks(goodContract, host, sb, test.minScoreGFR, test.minScoreGFU)
		if !reflect.DeepEqual(u, test.expectedUtilities) {
			t.Fatalf("%v: wrong utilities have %v but want %v", i, u, test.expectedUtilities)
		}
		if !reflect.DeepEqual(us, test.expectedUpdateStatus) {
			t.Fatalf("%v: wrong status have %v but want %v", i, us, test.expectedUpdateStatus)
		}
	}
}
