package modules

import (
	"strings"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/types"
)

// TestUpdatePriceTableGouging checks that the price table gouging is correctly
// detecting price gouging from a host.
func TestUpdatePriceTableGouging(t *testing.T) {
	t.Parallel()

	// allowance contains only the fields necessary to test the price gouging
	allowance := Allowance{
		Funds:  types.SiacoinPrecision,
		Period: types.BlockHeight(6),
	}

	// verify happy case
	pt := newDefaultPriceTable()
	err := checkUpdatePriceTableGouging(allowance, pt)
	if err != nil {
		t.Fatal("unexpected price gouging failure")
	}

	// verify gouging case, first calculate how many times we need to update the
	// PT over the duration of the allowance period
	pt = newDefaultPriceTable()
	durationInS := int64(pt.Validity.Seconds())
	periodInS := int64(allowance.Period) * 10 * 60 // period times 10m blocks
	numUpdates := periodInS / durationInS

	// increase the update price table cost so that the total cost of updating
	// it for the entire allowance period exceeds the allowed percentage of the
	// total allowance.
	pt.UpdatePriceTableCost = allowance.Funds.MulFloat(updatePriceTableGougingPercentageThreshold * 2).Div64(uint64(numUpdates))
	err = checkUpdatePriceTableGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "update price table cost") {
		t.Fatalf("expected update price table cost gouging error, instead error was '%v'", err)
	}

	// verify unacceptable validity case
	pt = newDefaultPriceTable()
	pt.Validity = 0
	err = checkUpdatePriceTableGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "update price table validity") {
		t.Fatalf("expected update price table validity gouging error, instead error was '%v'", err)
	}
	pt.Validity = minAcceptedPriceTableValidity
	err = checkUpdatePriceTableGouging(allowance, pt)
	if err != nil {
		t.Fatalf("unexpected update price table validity gouging error: %v", err)
	}
}

// TestPDBRGouging checks that `checkPDBRGouging` is correctly detecting price
// gouging from a host.
func TestPDBRGouging(t *testing.T) {
	t.Parallel()

	// allowance contains only the fields necessary to test the price gouging
	hes := DefaultHostExternalSettings()
	allowance := Allowance{
		Funds:                     types.SiacoinPrecision.Mul64(1e3),
		MaxDownloadBandwidthPrice: hes.DownloadBandwidthPrice.Mul64(10),
		MaxUploadBandwidthPrice:   hes.UploadBandwidthPrice.Mul64(10),
	}

	// verify happy case
	pt := newDefaultPriceTable()
	err := checkPDBRGouging(allowance, pt)
	if err != nil {
		t.Fatal("unexpected price gouging failure", err)
	}

	// verify max download bandwidth price gouging
	pt = newDefaultPriceTable()
	pt.DownloadBandwidthCost = allowance.MaxDownloadBandwidthPrice.Add64(1)
	err = checkPDBRGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "download bandwidth price") {
		t.Fatalf("expected download bandwidth price gouging error, instead error was '%v'", err)
	}

	// verify max upload bandwidth price gouging
	pt = newDefaultPriceTable()
	pt.UploadBandwidthCost = allowance.MaxUploadBandwidthPrice.Add64(1)
	err = checkPDBRGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "upload bandwidth price") {
		t.Fatalf("expected upload bandwidth price gouging error, instead error was '%v'", err)
	}

	// update the expected download to be non zero and verify the default prices
	allowance.ExpectedDownload = 1 << 30 // 1GiB
	pt = newDefaultPriceTable()
	err = checkPDBRGouging(allowance, pt)
	if err != nil {
		t.Fatal("unexpected price gouging failure", err)
	}

	// verify gouging of MDM related costs, in order to verify if gouging
	// detection kicks in we need to ensure the cost of executing enough PDBRs
	// to fulfil the expected download exceeds the allowance

	// we do this by maxing out the upload and bandwidth costs and setting all
	// default cost components to 250 pS, note that this value is arbitrary,
	// setting those costs at 250 pS simply proved to push the price per PDBR
	// just over the allowed limit.
	//
	// Cost breakdown:
	// - cost per PDBR 266.4 mS
	// - total cost to fulfil expected download 4.365 KS
	// - reduced cost after applying downloadGougingFractionDenom: 1.091 KS
	// - exceeding the allowance of 1 KS, which is what we are after
	pt.UploadBandwidthCost = allowance.MaxUploadBandwidthPrice
	pt.DownloadBandwidthCost = allowance.MaxDownloadBandwidthPrice
	pS := types.SiacoinPrecision.MulFloat(1e-12)
	pt.InitBaseCost = pt.InitBaseCost.Add(pS.Mul64(250))
	pt.ReadBaseCost = pt.ReadBaseCost.Add(pS.Mul64(250))
	pt.MemoryTimeCost = pt.MemoryTimeCost.Add(pS.Mul64(250))
	err = checkPDBRGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "combined PDBR pricing of host yields") {
		t.Fatalf("expected PDBR price gouging error, instead error was '%v'", err)
	}

	// verify these checks are ignored if the funds are 0
	allowance.Funds = types.ZeroCurrency
	err = checkPDBRGouging(allowance, pt)
	if err != nil {
		t.Fatal("unexpected price gouging failure", err)
	}

	allowance.Funds = types.SiacoinPrecision.Mul64(1e3) // reset

	// verify bumping every individual cost component to an insane value results
	// in a price gouging error
	pt = newDefaultPriceTable()
	pt.InitBaseCost = types.SiacoinPrecision.Mul64(100)
	err = checkPDBRGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "combined PDBR pricing of host yields") {
		t.Fatalf("expected PDBR price gouging error, instead error was '%v'", err)
	}

	pt = newDefaultPriceTable()
	pt.ReadBaseCost = types.SiacoinPrecision
	err = checkPDBRGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "combined PDBR pricing of host yields") {
		t.Fatalf("expected PDBR price gouging error, instead error was '%v'", err)
	}

	pt = newDefaultPriceTable()
	pt.ReadLengthCost = types.SiacoinPrecision
	err = checkPDBRGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "combined PDBR pricing of host yields") {
		t.Fatalf("expected PDBR price gouging error, instead error was '%v'", err)
	}

	pt = newDefaultPriceTable()
	pt.MemoryTimeCost = types.SiacoinPrecision
	err = checkPDBRGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "combined PDBR pricing of host yields") {
		t.Fatalf("expected PDBR price gouging error, instead error was '%v'", err)
	}
}

// TestCheckFundAccountGouging checks that `checkFundAccountGouging` is
// correctly detecting price gouging from a host.
func TestCheckFundAccountGouging(t *testing.T) {
	t.Parallel()

	// allowance contains only the fields necessary to test the price gouging
	allowance := Allowance{
		Funds: types.SiacoinPrecision.Mul64(1e3),
	}

	// set the target balance to 1SC, this is necessary because this decides how
	// frequently we refill the account, which is a required piece of knowledge
	// in order to estimate the total cost of refilling
	targetBalance := types.SiacoinPrecision

	// verify happy case
	pt := newDefaultPriceTable()
	err := checkFundAccountGouging(allowance, pt, targetBalance)
	if err != nil {
		t.Fatal("unexpected price gouging failure")
	}

	// verify gouging case, in order to do so we have to set the fund account
	// cost to an unreasonable amount, empirically we found 75mS to be such a
	// value for the given parameters (1000SC funds and TB of 1SC)
	pt = newDefaultPriceTable()
	pt.FundAccountCost = types.SiacoinPrecision.MulFloat(0.075)
	err = checkFundAccountGouging(allowance, pt, targetBalance)
	if err == nil || !strings.Contains(err.Error(), "fund account cost") {
		t.Fatalf("expected fund account cost gouging error, instead error was '%v'", err)
	}
}

// TODO (PJ) move
//
// newDefaultPriceTable is a helper function that returns a price table with
// default prices for all fields
func newDefaultPriceTable() RPCPriceTable {
	hes := DefaultHostExternalSettings()
	oneCurrency := types.NewCurrency64(1)
	return RPCPriceTable{
		Validity:             time.Minute,
		FundAccountCost:      oneCurrency,
		UpdatePriceTableCost: oneCurrency,

		HasSectorBaseCost: oneCurrency,
		InitBaseCost:      oneCurrency,
		MemoryTimeCost:    oneCurrency,
		ReadBaseCost:      oneCurrency,
		ReadLengthCost:    oneCurrency,
		SwapSectorCost:    oneCurrency,

		DownloadBandwidthCost: hes.DownloadBandwidthPrice,
		UploadBandwidthCost:   hes.UploadBandwidthPrice,
	}
}
