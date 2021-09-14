package renter

import (
	"strings"
	"testing"

	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// TestPCWSGouging checks that the PCWS gouging check is triggering at the right
// times.
func TestPCWSGouging(t *testing.T) {
	// Create some defaults to get some intuitive ideas for gouging.
	//
	// 100 workers and 1e9 expected download means ~2e6 HasSector queries will
	// be performed.
	pt := modules.RPCPriceTable{
		InitBaseCost:          types.NewCurrency64(1e3),
		DownloadBandwidthCost: types.NewCurrency64(1e3),
		UploadBandwidthCost:   types.NewCurrency64(1e3),
		HasSectorBaseCost:     types.NewCurrency64(1e6),
	}
	allowance := skymodules.Allowance{
		MaxDownloadBandwidthPrice: types.NewCurrency64(2e3),
		MaxUploadBandwidthPrice:   types.NewCurrency64(2e3),

		Funds: types.NewCurrency64(1e18),

		ExpectedDownload: 1e9, // 1 GiB
	}
	numWorkers := 100
	numRoots := 30

	// Check that the gouging passes for normal values.
	err := checkPCWSGouging(pt, allowance, numWorkers, numRoots)
	if err != nil {
		t.Error(err)
	}

	// Check with high init base cost.
	pt.InitBaseCost = types.NewCurrency64(1e12)
	err = checkPCWSGouging(pt, allowance, numWorkers, numRoots)
	if err == nil {
		t.Error("bad")
	}
	pt.InitBaseCost = types.NewCurrency64(1e3)

	// Check with high upload bandwidth cost.
	pt.UploadBandwidthCost = types.NewCurrency64(1e12)
	err = checkPCWSGouging(pt, allowance, numWorkers, numRoots)
	if err == nil {
		t.Error("bad")
	}
	pt.UploadBandwidthCost = types.NewCurrency64(1e3)

	// Check with high download bandwidth cost.
	pt.DownloadBandwidthCost = types.NewCurrency64(1e12)
	err = checkPCWSGouging(pt, allowance, numWorkers, numRoots)
	if err == nil {
		t.Error("bad")
	}
	pt.DownloadBandwidthCost = types.NewCurrency64(1e3)

	// Check with high HasSector cost.
	pt.HasSectorBaseCost = types.NewCurrency64(1e12)
	err = checkPCWSGouging(pt, allowance, numWorkers, numRoots)
	if err == nil {
		t.Error("bad")
	}
	pt.HasSectorBaseCost = types.NewCurrency64(1e6)

	// Check with low MaxDownloadBandwidthPrice.
	allowance.MaxDownloadBandwidthPrice = types.NewCurrency64(100)
	err = checkPCWSGouging(pt, allowance, numWorkers, numRoots)
	if err == nil {
		t.Error("bad")
	}
	allowance.MaxDownloadBandwidthPrice = types.NewCurrency64(2e3)

	// Check with low MaxUploadBandwidthPrice.
	allowance.MaxUploadBandwidthPrice = types.NewCurrency64(100)
	err = checkPCWSGouging(pt, allowance, numWorkers, numRoots)
	if err == nil {
		t.Error("bad")
	}
	allowance.MaxUploadBandwidthPrice = types.NewCurrency64(2e3)

	// Check with reduced funds.
	allowance.Funds = types.NewCurrency64(1e15)
	err = checkPCWSGouging(pt, allowance, numWorkers, numRoots)
	if err == nil {
		t.Error("bad")
	}
	allowance.Funds = types.NewCurrency64(1e18)

	// Check with increased expected download.
	allowance.ExpectedDownload = 1e12
	err = checkPCWSGouging(pt, allowance, numWorkers, numRoots)
	if err == nil {
		t.Error("bad")
	}
	allowance.ExpectedDownload = 1e9

	// Check that the base allowanace still passes. (ensures values have been
	// reset correctly)
	err = checkPCWSGouging(pt, allowance, numWorkers, numRoots)
	if err != nil {
		t.Error(err)
	}
}

// TestProjectDownloadGouging checks that `checkProjectDownloadGouging` is
// correctly detecting price gouging from a host.
func TestProjectDownloadGouging(t *testing.T) {
	t.Parallel()

	// allowance contains only the fields necessary to test the price gouging
	hes := modules.DefaultHostExternalSettings()
	allowance := skymodules.Allowance{
		Funds:                     types.SiacoinPrecision.Mul64(1e3),
		MaxDownloadBandwidthPrice: hes.DownloadBandwidthPrice.Mul64(10),
		MaxUploadBandwidthPrice:   hes.UploadBandwidthPrice.Mul64(10),
	}

	// verify happy case
	pt := newDefaultPriceTable()
	err := checkProjectDownloadGouging(pt, allowance)
	if err != nil {
		t.Fatal("unexpected price gouging failure", err)
	}

	// verify max download bandwidth price gouging
	pt = newDefaultPriceTable()
	pt.DownloadBandwidthCost = allowance.MaxDownloadBandwidthPrice.Add64(1)
	err = checkProjectDownloadGouging(pt, allowance)
	if err == nil || !strings.Contains(err.Error(), "download bandwidth price") {
		t.Fatalf("expected download bandwidth price gouging error, instead error was '%v'", err)
	}

	// verify max upload bandwidth price gouging
	pt = newDefaultPriceTable()
	pt.UploadBandwidthCost = allowance.MaxUploadBandwidthPrice.Add64(1)
	err = checkProjectDownloadGouging(pt, allowance)
	if err == nil || !strings.Contains(err.Error(), "upload bandwidth price") {
		t.Fatalf("expected upload bandwidth price gouging error, instead error was '%v'", err)
	}

	// update the expected download to be non zero and verify the default prices
	allowance.ExpectedDownload = 1 << 30 // 1GiB
	pt = newDefaultPriceTable()
	err = checkProjectDownloadGouging(pt, allowance)
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
	err = checkProjectDownloadGouging(pt, allowance)
	if err == nil || !strings.Contains(err.Error(), "combined PDBR pricing of host yields") {
		t.Fatalf("expected PDBR price gouging error, instead error was '%v'", err)
	}

	// verify these checks are ignored if the funds are 0
	allowance.Funds = types.ZeroCurrency
	err = checkProjectDownloadGouging(pt, allowance)
	if err != nil {
		t.Fatal("unexpected price gouging failure", err)
	}

	allowance.Funds = types.SiacoinPrecision.Mul64(1e3) // reset

	// verify bumping every individual cost component to an insane value results
	// in a price gouging error
	pt = newDefaultPriceTable()
	pt.InitBaseCost = types.SiacoinPrecision.Mul64(100)
	err = checkProjectDownloadGouging(pt, allowance)
	if err == nil || !strings.Contains(err.Error(), "combined PDBR pricing of host yields") {
		t.Fatalf("expected PDBR price gouging error, instead error was '%v'", err)
	}

	pt = newDefaultPriceTable()
	pt.ReadBaseCost = types.SiacoinPrecision
	err = checkProjectDownloadGouging(pt, allowance)
	if err == nil || !strings.Contains(err.Error(), "combined PDBR pricing of host yields") {
		t.Fatalf("expected PDBR price gouging error, instead error was '%v'", err)
	}

	pt = newDefaultPriceTable()
	pt.ReadLengthCost = types.SiacoinPrecision
	err = checkProjectDownloadGouging(pt, allowance)
	if err == nil || !strings.Contains(err.Error(), "combined PDBR pricing of host yields") {
		t.Fatalf("expected PDBR price gouging error, instead error was '%v'", err)
	}

	pt = newDefaultPriceTable()
	pt.MemoryTimeCost = types.SiacoinPrecision
	err = checkProjectDownloadGouging(pt, allowance)
	if err == nil || !strings.Contains(err.Error(), "combined PDBR pricing of host yields") {
		t.Fatalf("expected PDBR price gouging error, instead error was '%v'", err)
	}
}
