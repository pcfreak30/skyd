package contractor

import (
	"io/ioutil"
	"reflect"
	"testing"

	"gitlab.com/SkynetLabs/skyd/skymodules"
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
