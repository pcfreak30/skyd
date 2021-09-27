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

// TestUtilityUpdateStatusMerge is a unit test for the utilityUpdateStatus.Merge
// method.
func TestUtilityUpdateStatusMerge(t *testing.T) {
	t.Parallel()

	// Declare testcases.
	tests := []struct {
		us1    utilityUpdateStatus
		us2    utilityUpdateStatus
		result utilityUpdateStatus
	}{
		{
			us1:    noUpdate,
			us2:    suggestedUtilityUpdate,
			result: suggestedUtilityUpdate,
		},
		{
			us1:    suggestedUtilityUpdate,
			us2:    necessaryUtilityUpdate,
			result: necessaryUtilityUpdate,
		},
		{
			us1:    noUpdate,
			us2:    necessaryUtilityUpdate,
			result: necessaryUtilityUpdate,
		},
		{
			us1:    noUpdate,
			us2:    noUpdate,
			result: noUpdate,
		},
		{
			us1:    suggestedUtilityUpdate,
			us2:    suggestedUtilityUpdate,
			result: suggestedUtilityUpdate,
		},
		{
			us1:    necessaryUtilityUpdate,
			us2:    necessaryUtilityUpdate,
			result: necessaryUtilityUpdate,
		},
	}

	// Run tests.
	for _, test := range tests {
		us1 := test.us1
		us2 := test.us2
		result := us1.Merge(us2)
		if result != test.result {
			t.Fatal("wrong resulte", result, test.result)
		}
		result = us2.Merge(us1)
		if result != test.result {
			t.Fatal("wrong resulte", result, test.result)
		}
	}
}

// TestDeadScoreCheck is a unit test for deadScoreCheck.
func TestDeadScoreCheck(t *testing.T) {
	t.Parallel()

	goodUtility := skymodules.ContractUtility{
		GoodForUpload: true,
		GoodForRenew:  true,
	}
	badUtility := skymodules.ContractUtility{}

	utility, uus := deadScoreCheck(goodUtility, types.NewCurrency64(0))
	if uus != necessaryUtilityUpdate {
		t.Fatal(uus)
	}
	if !reflect.DeepEqual(utility, badUtility) {
		t.Fatal("wrong utility")
	}
	utility, uus = deadScoreCheck(goodUtility, types.NewCurrency64(1))
	if uus != necessaryUtilityUpdate {
		t.Fatal(uus)
	}
	if !reflect.DeepEqual(utility, badUtility) {
		t.Fatal("wrong utility")
	}
	utility, uus = deadScoreCheck(goodUtility, types.NewCurrency64(2))
	if uus != noUpdate {
		t.Fatal(uus)
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
	u, uus := storageGougingCheck(goodContract, allowance, host, 0)
	if uus != noUpdate {
		t.Fatal("wrong uus", uus)
	}
	if !reflect.DeepEqual(u, goodUtility) {
		t.Fatal("wrong utility", u, goodUtility)
	}
	u, uus = storageGougingCheck(goodContract, allowance, host, 1)
	if uus != noUpdate {
		t.Fatal("wrong uus", uus)
	}
	if !reflect.DeepEqual(u, goodUtility) {
		t.Fatal("wrong utility", u)
	}

	// Above max price cases.
	host.StoragePrice = host.StoragePrice.Add64(1)
	u, uus = storageGougingCheck(goodContract, allowance, host, 0)
	if uus != necessaryUtilityUpdate {
		t.Fatal("wrong uus", uus)
	}
	if !reflect.DeepEqual(u, skymodules.ContractUtility{GoodForRenew: true}) {
		t.Fatal("wrong utility", u)
	}
	u, uus = storageGougingCheck(goodContract, allowance, host, 1)
	if uus != necessaryUtilityUpdate {
		t.Fatal("wrong uus", uus)
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

		gfu bool
		gfr bool
		uus utilityUpdateStatus
	}{
		// Not renewing.
		{
			blockHeight: 0,
			endHeight:   100,
			renewWindow: 10,

			gfu: true,
			gfr: true,
			uus: noUpdate,
		},
		// One block before second half of renew window.
		{
			blockHeight: 0,
			endHeight:   11,
			renewWindow: 20,

			gfu: true,
			gfr: true,
			uus: noUpdate,
		},
		// Beginning of second half.
		{
			blockHeight: 1,
			endHeight:   11,
			renewWindow: 20,

			gfu: false,
			gfr: true,
			uus: necessaryUtilityUpdate,
		},
		// One block in second half.
		{
			blockHeight: 2,
			endHeight:   11,
			renewWindow: 20,

			gfu: false,
			gfr: true,
			uus: necessaryUtilityUpdate,
		},
	}

	for i, test := range tests {
		u, uus := upForRenewalCheck(skymodules.RenterContract{
			EndHeight: test.endHeight,
			Utility: skymodules.ContractUtility{
				GoodForUpload: true,
				GoodForRenew:  true,
			},
		}, test.renewWindow, test.blockHeight, logger)

		if uus != test.uus {
			t.Errorf("%v (update): %v != %v", i, uus, test.uus)
		}
		if u.GoodForRenew != test.gfr {
			t.Errorf("%v (gfr): %v != %v", i, u.GoodForRenew, test.gfr)
		}
		if u.GoodForUpload != test.gfu {
			t.Errorf("%v (gfu): %v != %v", i, u.GoodForUpload, test.gfu)
		}
	}
}
