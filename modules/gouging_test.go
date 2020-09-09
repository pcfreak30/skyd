package modules

import (
	"strings"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/types"
)

var (
	DefaultTargetBalance = types.SiacoinPrecision

	DefaultPriceTable = RPCPriceTable{
		Validity:             time.Minute,
		FundAccountCost:      types.NewCurrency64(1),
		UpdatePriceTableCost: types.NewCurrency64(1),

		HasSectorBaseCost: types.NewCurrency64(1),
		InitBaseCost:      types.NewCurrency64(1),
		MemoryTimeCost:    types.NewCurrency64(1),
		ReadBaseCost:      types.NewCurrency64(1),
		ReadLengthCost:    types.NewCurrency64(1),
		SwapSectorCost:    types.NewCurrency64(1),

		DownloadBandwidthCost: DefaultHostExternalSettings.DownloadBandwidthPrice,
		UploadBandwidthCost:   DefaultHostExternalSettings.UploadBandwidthPrice,
	}
)

// TestGouging verifies the functionality of the gouging checks, performed both
// on the RPC price table and the host's external settings object.
func TestGouging(t *testing.T) {
	t.Parallel()
	t.Run("CheckPriceTableGouging", testCheckPriceTableGouing)
	t.Run("CheckHostSettingsGouging", testCheckHostSettingsGouging)
}

// testCheckPriceTableGouing verifies the gouging checks performed on the host's
// price table.
func testCheckPriceTableGouing(t *testing.T) {
	t.Parallel()

	// verify all gouging checks are present in the PriceTableGougingChecks
	gc := CheckPriceTableGouging(
		DefaultAllowance,
		DefaultPriceTable,
		DefaultTargetBalance,
	)
	if gc.FundAccount == nil ||
		gc.PDBR == nil ||
		gc.UpdatePriceTable == nil {
		t.Fatal("PriceTableGougingChecks is missing a gouging check")
	}

	// verify all checks individually
	t.Run("FundAccount", testFundAccountGouging)
	t.Run("HardcodedCosts", testHardcodedCostsGouging)
	t.Run("PDBR", testPDBRGouging)
	t.Run("UpdatePriceTable", testUpdatePriceTableGouging)
}

// testCheckHostSettingsGouging verifies the gouging checks performed on the
// host's external settings object.
func testCheckHostSettingsGouging(t *testing.T) {
	t.Parallel()

	// verify all gouging checks are present in the PriceTableGougingChecks
	gc := CheckHostSettingsGouging(DefaultAllowance, HostExternalSettings{})
	if gc.Download == nil ||
		gc.FetchBackups == nil ||
		gc.FormContract == nil ||
		gc.FormPaymentContract == nil ||
		gc.Upload == nil ||
		gc.UploadSnapshot == nil {
		t.Fatal("HostSettingsGougingChecks is missing a gouging check")
	}

	// verify all checks individually
	t.Run("Download", testDownloadGouging)
	t.Run("FetchBackups", testFetchBackupsGouging)
	t.Run("FormContract", testFormContractGouging)
	t.Run("FormPaymentContract", testFormPaymentContractGouging)
	t.Run("Upload", testUploadGouging)
	t.Run("UploadSnapshot", testUploadSnapshotGouging)
}

// testFundAccountGouging checks that `checkFundAccountGouging` is
// correctly detecting price gouging from a host.
func testFundAccountGouging(t *testing.T) {
	// verify happy case
	err := checkFundAccountGouging(
		DefaultAllowance,
		DefaultPriceTable,
		DefaultTargetBalance,
	)
	if err != nil {
		t.Fatal("unexpected price gouging failure")
	}

	//
	// verify gouging case, in order to do so we have to set the fund account
	// cost to an unreasonable amount, empirically we found 75mS to be such a
	// value for the given parameters (1000SC funds and TB of 1SC)
	pt := DefaultPriceTable
	pt.FundAccountCost = types.SiacoinPrecision.MulFloat(0.075)
	err = checkFundAccountGouging(DefaultAllowance, pt, DefaultTargetBalance)
	if err == nil || !strings.Contains(err.Error(), "fund account cost") {
		t.Fatalf("expected fund account cost gouging error, instead error was '%v'", err)
	}
}

func testHardcodedCostsGouging(t *testing.T) {
	// TODO
}

// testPDBRGouging checks for gouging when performing a PDBR.
func testPDBRGouging(t *testing.T) {
	// allowance contains only the fields necessary to test the price gouging
	hes := DefaultHostExternalSettings
	allowance := Allowance{
		Funds:                     types.SiacoinPrecision.Mul64(1e3),
		MaxDownloadBandwidthPrice: hes.DownloadBandwidthPrice.Mul64(10),
		MaxUploadBandwidthPrice:   hes.UploadBandwidthPrice.Mul64(10),
	}

	// verify happy case
	pt := DefaultPriceTable
	err := checkPDBRGouging(allowance, pt)
	if err != nil {
		t.Fatal("unexpected price gouging failure", err)
	}

	// verify max download bandwidth price gouging
	pt = DefaultPriceTable
	pt.DownloadBandwidthCost = allowance.MaxDownloadBandwidthPrice.Add64(1)
	err = checkPDBRGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "download bandwidth price") {
		t.Fatalf("expected download bandwidth price gouging error, instead error was '%v'", err)
	}

	// verify max upload bandwidth price gouging
	pt = DefaultPriceTable
	pt.UploadBandwidthCost = allowance.MaxUploadBandwidthPrice.Add64(1)
	err = checkPDBRGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "upload bandwidth price") {
		t.Fatalf("expected upload bandwidth price gouging error, instead error was '%v'", err)
	}

	// update the expected download to be non zero and verify the default prices
	allowance.ExpectedDownload = 1 << 30 // 1GiB
	pt = DefaultPriceTable
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
	pt = DefaultPriceTable
	pt.InitBaseCost = types.SiacoinPrecision.Mul64(100)
	err = checkPDBRGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "combined PDBR pricing of host yields") {
		t.Fatalf("expected PDBR price gouging error, instead error was '%v'", err)
	}

	pt = DefaultPriceTable
	pt.ReadBaseCost = types.SiacoinPrecision
	err = checkPDBRGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "combined PDBR pricing of host yields") {
		t.Fatalf("expected PDBR price gouging error, instead error was '%v'", err)
	}

	pt = DefaultPriceTable
	pt.ReadLengthCost = types.SiacoinPrecision
	err = checkPDBRGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "combined PDBR pricing of host yields") {
		t.Fatalf("expected PDBR price gouging error, instead error was '%v'", err)
	}

	pt = DefaultPriceTable
	pt.MemoryTimeCost = types.SiacoinPrecision
	err = checkPDBRGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "combined PDBR pricing of host yields") {
		t.Fatalf("expected PDBR price gouging error, instead error was '%v'", err)
	}
}

// testUpdatePriceTableGouging checks for gouging specific for the
// UpdatePriceTable RPC call.
func testUpdatePriceTableGouging(t *testing.T) {
	// allowance contains only the fields necessary to test the price gouging
	allowance := Allowance{
		Funds:  types.SiacoinPrecision,
		Period: types.BlockHeight(6),
	}

	// verify happy case
	pt := DefaultPriceTable
	err := checkUpdatePriceTableGouging(allowance, pt)
	if err != nil {
		t.Fatal("unexpected price gouging failure")
	}

	// verify gouging case, first calculate how many times we need to update the
	// PT over the duration of the allowance period
	pt = DefaultPriceTable
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
	pt = DefaultPriceTable
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

// testDownloadGouging checks that the fetch backups price gouging
// checker is correctly detecting price gouging from a host.
func testDownloadGouging(t *testing.T) {
	oneCurrency := types.NewCurrency64(1)

	// minAllowance contains only the fields necessary to test the price gouging
	// function. The min allowance doesn't include any of the max prices,
	// because the function will ignore them if they are not set.
	minAllowance := Allowance{
		// Funds is set such that the tests come out to an easy, round number.
		// One siacoin is multiplied by the number of elements that are checked
		// for gouging, and then divided by the gounging denominator.
		Funds: types.SiacoinPrecision.Mul64(3).Div64(downloadGougingFractionDenom).Sub(oneCurrency),

		ExpectedDownload: StreamDownloadSize, // 1 stream download operation.
	}
	// minHostSettings contains only the fields necessary to test the price
	// gouging function.
	//
	// The cost is set to be exactly equal to the price gouging limit, such that
	// slightly decreasing any of the values evades the price gouging detector.
	minHostSettings := HostExternalSettings{
		BaseRPCPrice:           types.SiacoinPrecision,
		DownloadBandwidthPrice: types.SiacoinPrecision.Div64(StreamDownloadSize),
		SectorAccessPrice:      types.SiacoinPrecision,
	}

	err := checkDownloadGouging(minAllowance, minHostSettings)
	if err == nil {
		t.Fatal("expecting price gouging check to fail:", err)
	}

	// Drop the host prices one field at a time.
	newHostSettings := minHostSettings
	newHostSettings.BaseRPCPrice = minHostSettings.BaseRPCPrice.Mul64(100).Div64(101)
	err = checkDownloadGouging(minAllowance, newHostSettings)
	if err != nil {
		t.Fatal(err)
	}
	newHostSettings = minHostSettings
	newHostSettings.DownloadBandwidthPrice = minHostSettings.DownloadBandwidthPrice.Mul64(100).Div64(101)
	err = checkDownloadGouging(minAllowance, newHostSettings)
	if err != nil {
		t.Fatal(err)
	}
	newHostSettings = minHostSettings
	newHostSettings.SectorAccessPrice = minHostSettings.SectorAccessPrice.Mul64(100).Div64(101)
	err = checkDownloadGouging(minAllowance, newHostSettings)
	if err != nil {
		t.Fatal(err)
	}

	// Set min settings on the allowance that are just below what should be
	// acceptable.
	maxAllowance := minAllowance
	maxAllowance.Funds = maxAllowance.Funds.Add(oneCurrency)
	maxAllowance.MaxRPCPrice = types.SiacoinPrecision.Add(oneCurrency)
	maxAllowance.MaxContractPrice = oneCurrency
	maxAllowance.MaxDownloadBandwidthPrice = types.SiacoinPrecision.Div64(StreamDownloadSize).Add(oneCurrency)
	maxAllowance.MaxSectorAccessPrice = types.SiacoinPrecision.Add(oneCurrency)
	maxAllowance.MaxStoragePrice = oneCurrency
	maxAllowance.MaxUploadBandwidthPrice = oneCurrency

	// The max allowance should have no issues with price gouging.
	err = checkDownloadGouging(maxAllowance, minHostSettings)
	if err != nil {
		t.Fatal(err)
	}

	// Should fail if the MaxRPCPrice is dropped.
	failAllowance := maxAllowance
	failAllowance.MaxRPCPrice = types.SiacoinPrecision.Sub(oneCurrency)
	err = checkDownloadGouging(failAllowance, minHostSettings)
	if err == nil {
		t.Fatal("expecting price gouging check to fail")
	}

	// Should fail if the MaxDownloadBandwidthPrice is dropped.
	failAllowance = maxAllowance
	failAllowance.MaxDownloadBandwidthPrice = types.SiacoinPrecision.Div64(StreamDownloadSize).Sub(oneCurrency)
	err = checkDownloadGouging(failAllowance, minHostSettings)
	if err == nil {
		t.Fatal("expecting price gouging check to fail")
	}

	// Should fail if the MaxSectorAccessPrice is dropped.
	failAllowance = maxAllowance
	failAllowance.MaxSectorAccessPrice = types.SiacoinPrecision.Sub(oneCurrency)
	err = checkDownloadGouging(failAllowance, minHostSettings)
	if err == nil {
		t.Fatal("expecting price gouging check to fail")
	}
}

// testFetchBackupsGouging checks that the fetch backups price gouging checker
// is correctly detecting price gouging from a host.
func testFetchBackupsGouging(t *testing.T) {
	oneCurrency := types.NewCurrency64(1)

	// minAllowance contains only the fields necessary to test the price gouging
	// function. The min allowance doesn't include any of the max prices,
	// because the function will ignore them if they are not set.
	minAllowance := Allowance{
		// Funds is set such that the tests come out to an easy, round number.
		// One siacoin is multiplied by the number of elements that are checked
		// for gouging, and then divided by the gounging denominator.
		Funds: types.SiacoinPrecision.Mul64(3).Div64(fetchBackupsGougingFractionDenom).Sub(oneCurrency),

		ExpectedDownload: StreamDownloadSize, // 1 stream download operation.
	}
	// minHostSettings contains only the fields necessary to test the price
	// gouging function.
	//
	// The cost is set to be exactly equal to the price gouging limit, such that
	// slightly decreasing any of the values evades the price gouging detector.
	minHostSettings := HostExternalSettings{
		BaseRPCPrice:           types.SiacoinPrecision,
		DownloadBandwidthPrice: types.SiacoinPrecision.Div64(StreamDownloadSize),
		SectorAccessPrice:      types.SiacoinPrecision,
	}

	err := checkFetchBackupsGouging(minAllowance, minHostSettings)
	if err == nil {
		t.Fatal("expecting price gouging check to fail:", err)
	}

	// Drop the host prices one field at a time.
	newHostSettings := minHostSettings
	newHostSettings.BaseRPCPrice = minHostSettings.BaseRPCPrice.Mul64(100).Div64(101)
	err = checkFetchBackupsGouging(minAllowance, newHostSettings)
	if err != nil {
		t.Fatal(err)
	}
	newHostSettings = minHostSettings
	newHostSettings.DownloadBandwidthPrice = minHostSettings.DownloadBandwidthPrice.Mul64(100).Div64(101)
	err = checkFetchBackupsGouging(minAllowance, newHostSettings)
	if err != nil {
		t.Fatal(err)
	}
	newHostSettings = minHostSettings
	newHostSettings.SectorAccessPrice = minHostSettings.SectorAccessPrice.Mul64(100).Div64(101)
	err = checkFetchBackupsGouging(minAllowance, newHostSettings)
	if err != nil {
		t.Fatal(err)
	}

	// Set min settings on the allowance that are just below that should be
	// acceptable.
	maxAllowance := minAllowance
	maxAllowance.Funds = maxAllowance.Funds.Add(oneCurrency)
	maxAllowance.MaxRPCPrice = types.SiacoinPrecision.Add(oneCurrency)
	maxAllowance.MaxContractPrice = oneCurrency
	maxAllowance.MaxDownloadBandwidthPrice = types.SiacoinPrecision.Div64(StreamDownloadSize).Add(oneCurrency)
	maxAllowance.MaxSectorAccessPrice = types.SiacoinPrecision.Add(oneCurrency)
	maxAllowance.MaxStoragePrice = oneCurrency
	maxAllowance.MaxUploadBandwidthPrice = oneCurrency

	// The max allowance should have no issues with price gouging.
	err = checkFetchBackupsGouging(maxAllowance, minHostSettings)
	if err != nil {
		t.Fatal(err)
	}

	// Should fail if the MaxRPCPrice is dropped.
	failAllowance := maxAllowance
	failAllowance.MaxRPCPrice = types.SiacoinPrecision.Sub(oneCurrency)
	err = checkFetchBackupsGouging(failAllowance, minHostSettings)
	if err == nil {
		t.Fatal("expecting price gouging check to fail")
	}

	// Should fail if the MaxDownloadBandwidthPrice is dropped.
	failAllowance = maxAllowance
	failAllowance.MaxDownloadBandwidthPrice = types.SiacoinPrecision.Div64(StreamDownloadSize).Sub(oneCurrency)
	err = checkFetchBackupsGouging(failAllowance, minHostSettings)
	if err == nil {
		t.Fatal("expecting price gouging check to fail")
	}

	// Should fail if the MaxSectorAccessPrice is dropped.
	failAllowance = maxAllowance
	failAllowance.MaxSectorAccessPrice = types.SiacoinPrecision.Sub(oneCurrency)
	err = checkFetchBackupsGouging(failAllowance, minHostSettings)
	if err == nil {
		t.Fatal("expecting price gouging check to fail")
	}
}

// testFormContractGouging checks that the upload price gouging checker is
// correctly detecting price gouging from a host.
//
// Test looks a bit funny because it was adapated from the other price gouging
// tests.
func testFormContractGouging(t *testing.T) {
	oneCurrency := types.NewCurrency64(1)

	// minAllowance contains only the fields necessary to test the price gouging
	// function. The min allowance doesn't include any of the max prices,
	// because the function will ignore them if they are not set.
	minAllowance := Allowance{}
	// minHostSettings contains only the fields necessary to test the price
	// gouging function.
	//
	// The cost is set to be exactly equal to the price gouging limit, such that
	// slightly decreasing any of the values evades the price gouging detector.
	minHostSettings := HostExternalSettings{
		BaseRPCPrice:  types.SiacoinPrecision,
		ContractPrice: types.SiacoinPrecision,
	}

	// Set min settings on the allowance that are just below that should be
	// acceptable.
	maxAllowance := minAllowance
	maxAllowance.Funds = maxAllowance.Funds.Add(oneCurrency)
	maxAllowance.MaxRPCPrice = types.SiacoinPrecision.Add(oneCurrency)
	maxAllowance.MaxContractPrice = types.SiacoinPrecision.Add(oneCurrency)
	maxAllowance.MaxDownloadBandwidthPrice = oneCurrency
	maxAllowance.MaxSectorAccessPrice = oneCurrency
	maxAllowance.MaxStoragePrice = oneCurrency
	maxAllowance.MaxUploadBandwidthPrice = oneCurrency

	// The max allowance should have no issues with price gouging.
	err := checkFormContractGouging(maxAllowance, minHostSettings)
	if err != nil {
		t.Fatal(err)
	}

	// Should fail if the MaxRPCPrice is dropped.
	failAllowance := maxAllowance
	failAllowance.MaxRPCPrice = types.SiacoinPrecision.Sub(oneCurrency)
	err = checkFormContractGouging(failAllowance, minHostSettings)
	if err == nil {
		t.Fatal("expecting price gouging check to fail")
	}

	// Should fail if the MaxContractPrice is dropped.
	failAllowance = maxAllowance
	failAllowance.MaxContractPrice = types.SiacoinPrecision.Sub(oneCurrency)
	err = checkFormContractGouging(failAllowance, minHostSettings)
	if err == nil {
		t.Fatal("expecting price gouging check to fail")
	}
}

func testFormPaymentContractGouging(t *testing.T) {
	// TODO
}

// testUploadGouging checks that the upload price gouging checker is correctly
// detecting price gouging from a host.
func testUploadGouging(t *testing.T) {
	oneCurrency := types.NewCurrency64(1)

	// minAllowance contains only the fields necessary to test the price gouging
	// function. The min allowance doesn't include any of the max prices,
	// because the function will ignore them if they are not set.
	minAllowance := Allowance{
		// Funds is set such that the tests come out to an easy, round number.
		// One siacoin is multiplied by the number of elements that are checked
		// for gouging, and then divided by the gounging denominator.
		Funds:  types.SiacoinPrecision.Mul64(4).Div64(uploadGougingFractionDenom).Sub(oneCurrency),
		Period: 1, // 1 block.

		ExpectedStorage: StreamUploadSize, // 1 stream upload operation.
	}
	// minHostSettings contains only the fields necessary to test the price
	// gouging function.
	//
	// The cost is set to be exactly equal to the price gouging limit, such that
	// slightly decreasing any of the values evades the price gouging detector.
	minHostSettings := HostExternalSettings{
		BaseRPCPrice:         types.SiacoinPrecision,
		SectorAccessPrice:    types.SiacoinPrecision,
		UploadBandwidthPrice: types.SiacoinPrecision.Div64(StreamUploadSize),
		StoragePrice:         types.SiacoinPrecision.Div64(StreamUploadSize),
	}

	err := checkUploadGouging(minAllowance, minHostSettings)
	if err == nil {
		t.Fatal("expecting price gouging check to fail:", err)
	}

	// Drop the host prices one field at a time.
	newHostSettings := minHostSettings
	newHostSettings.BaseRPCPrice = minHostSettings.BaseRPCPrice.Mul64(100).Div64(101)
	err = checkUploadGouging(minAllowance, newHostSettings)
	if err != nil {
		t.Fatal(err)
	}
	newHostSettings = minHostSettings
	newHostSettings.SectorAccessPrice = minHostSettings.SectorAccessPrice.Mul64(100).Div64(101)
	err = checkUploadGouging(minAllowance, newHostSettings)
	if err != nil {
		t.Fatal(err)
	}
	newHostSettings = minHostSettings
	newHostSettings.UploadBandwidthPrice = minHostSettings.UploadBandwidthPrice.Mul64(100).Div64(101)
	err = checkUploadGouging(minAllowance, newHostSettings)
	if err != nil {
		t.Fatal(err)
	}
	newHostSettings = minHostSettings
	newHostSettings.StoragePrice = minHostSettings.StoragePrice.Mul64(100).Div64(101)
	err = checkUploadGouging(minAllowance, newHostSettings)
	if err != nil {
		t.Fatal(err)
	}

	// Set min settings on the allowance that are just below that should be
	// acceptable.
	maxAllowance := minAllowance
	maxAllowance.Funds = maxAllowance.Funds.Add(oneCurrency)
	maxAllowance.MaxRPCPrice = types.SiacoinPrecision.Add(oneCurrency)
	maxAllowance.MaxContractPrice = oneCurrency
	maxAllowance.MaxDownloadBandwidthPrice = oneCurrency
	maxAllowance.MaxSectorAccessPrice = types.SiacoinPrecision.Add(oneCurrency)
	maxAllowance.MaxStoragePrice = types.SiacoinPrecision.Div64(StreamUploadSize).Add(oneCurrency)
	maxAllowance.MaxUploadBandwidthPrice = types.SiacoinPrecision.Div64(StreamUploadSize).Add(oneCurrency)

	// The max allowance should have no issues with price gouging.
	err = checkUploadGouging(maxAllowance, minHostSettings)
	if err != nil {
		t.Fatal(err)
	}

	// Should fail if the MaxRPCPrice is dropped.
	failAllowance := maxAllowance
	failAllowance.MaxRPCPrice = types.SiacoinPrecision.Sub(oneCurrency)
	err = checkUploadGouging(failAllowance, minHostSettings)
	if err == nil {
		t.Error("expecting price gouging check to fail")
	}

	// Should fail if the MaxSectorAccessPrice is dropped.
	failAllowance = maxAllowance
	failAllowance.MaxSectorAccessPrice = types.SiacoinPrecision.Sub(oneCurrency)
	err = checkUploadGouging(failAllowance, minHostSettings)
	if err == nil {
		t.Error("expecting price gouging check to fail")
	}

	// Should fail if the MaxStoragePrice is dropped.
	failAllowance = maxAllowance
	failAllowance.MaxStoragePrice = types.SiacoinPrecision.Div64(StreamUploadSize).Sub(oneCurrency)
	err = checkUploadGouging(failAllowance, minHostSettings)
	if err == nil {
		t.Error("expecting price gouging check to fail")
	}

	// Should fail if the MaxUploadBandwidthPrice is dropped.
	failAllowance = maxAllowance
	failAllowance.MaxUploadBandwidthPrice = types.SiacoinPrecision.Div64(StreamUploadSize).Sub(oneCurrency)
	err = checkUploadGouging(failAllowance, minHostSettings)
	if err == nil {
		t.Error("expecting price gouging check to fail")
	}
}

func testUploadSnapshotGouging(t *testing.T) {
	// TODO
}
