package modules

import (
	"reflect"
	"strings"
	"testing"

	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

var (
	// DefaultTargetBalance is the target balance used in the workers
	DefaultTargetBalance = types.SiacoinPrecision
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

	// verify PriceTableGougingChecks contains all gouging checks
	gc := CheckPriceTableGouging(
		DefaultAllowance,
		NewPriceTable(DefaultHostExternalSettings),
		DefaultTargetBalance,
	)
	if gc.Download == nil ||
		gc.FundAccount == nil ||
		gc.PDBR == nil ||
		gc.UpdatePriceTable == nil {
		t.Fatal("PriceTableGougingChecks is missing a gouging check")
	}

	// verify every gouging check function individually
	t.Run("Download", testDownloadGouging)
	t.Run("FundAccount", testCheckFundAccountGouging)
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
	if gc.FetchBackups == nil ||
		gc.FormContract == nil ||
		gc.Upload == nil ||
		gc.UploadSnapshot == nil {
		t.Fatal("HostSettingsGougingChecks is missing a gouging check")
	}

	// verify all checks individually
	t.Run("FetchBackups", testFetchBackupsGouging)
	t.Run("FormContract", testFormContractGouging)
	t.Run("Upload", func(t *testing.T) { testUploadGouging(t, uploadGougingFractionDenom) })
	t.Run("UploadSnapshot", func(t *testing.T) { testUploadGouging(t, uploadSnapshotGougingFractionDenom) })
}

// testCheckFundAccountGouging verifies the outcome of
// `checkFundAccountGouging`.
func testCheckFundAccountGouging(t *testing.T) {
	// verify the basic case
	err := checkFundAccountGouging(DefaultAllowance, NewPriceTable(DefaultHostExternalSettings), DefaultTargetBalance)
	if err != nil {
		t.Fatal("unexpected outcome", err)
	}

	// verify FundAccountCost not equal to hardcoded constant of 1H
	pt := NewPriceTable(DefaultHostExternalSettings)
	pt.FundAccountCost = types.NewCurrency64(2)
	err = checkFundAccountGouging(DefaultAllowance, pt, DefaultTargetBalance)
	if !errors.Contains(err, ErrGougingDetected) {
		t.Fatal("unexpected outcome", err)
	}

	// verify overflow
	lowTB := types.NewCurrency64(2)
	err = checkFundAccountGouging(DefaultAllowance, NewPriceTable(DefaultHostExternalSettings), lowTB)
	if err == nil || !strings.Contains(err.Error(), "result is an overflow") {
		t.Fatal("unexpected error", err)
	}

	// verify total cost of funding account exceeding the pct threshold
	pt = NewPriceTable(DefaultHostExternalSettings)
	pt.FundAccountCost = types.SiacoinPrecision
	err = checkFundAccountGouging(DefaultAllowance, pt, DefaultTargetBalance)
	if !errors.Contains(err, ErrGougingDetected) {
		t.Fatal("unexpected outcome", err)
	}
}

// testCheckFundAccountGouging verifies the outcome of
// `checkHardcodedConstants`.
func testHardcodedCostsGouging(t *testing.T) {
	// verify basic case
	pt := NewPriceTable(DefaultHostExternalSettings)
	err := checkHardcodedConstants(pt)
	if err != nil {
		t.Fatal("unexpected outcome", err)
	}

	// verify we detect a change for every hardcoded field
	for _, field := range []string{
		"AccountBalanceCost",
		"FundAccountCost",
		"UpdatePriceTableCost",
		"HasSectorBaseCost",
		"MemoryTimeCost",
		"DropSectorsBaseCost",
		"DropSectorsUnitCost",
		"SwapSectorCost",
		"ReadLengthCost",
		"WriteBaseCost",
		"WriteLengthCost",
	} {
		pt = NewPriceTable(DefaultHostExternalSettings)
		reflect.ValueOf(&pt).Elem().FieldByName(field).Set(reflect.ValueOf(types.SiacoinPrecision))
		err := checkHardcodedConstants(pt)
		if err == nil {
			t.Fatal("unexpected outcome", err)
		}
	}
}

// testCheckFundAccountGouging verifies the outcome of `checkPDBRGouging`.
func testPDBRGouging(t *testing.T) {
	hes := DefaultHostExternalSettings

	allowance := DefaultAllowance
	allowance.Funds = types.SiacoinPrecision.Mul64(1e3)
	allowance.MaxDownloadBandwidthPrice = hes.DownloadBandwidthPrice.Mul64(2)
	allowance.MaxUploadBandwidthPrice = hes.UploadBandwidthPrice.Mul64(2)
	allowance.ExpectedDownload = 1 << 30 // 1GiB

	// verify basic case
	err := checkPDBRGouging(DefaultAllowance, NewPriceTable(hes))
	if err != nil {
		t.Fatal("unexpected outcome", err)
	}

	// verify bandwidth gouging - max download
	pt := NewPriceTable(hes)
	pt.DownloadBandwidthCost = allowance.MaxDownloadBandwidthPrice.Mul64(2)
	err = checkPDBRGouging(allowance, pt)
	if !errors.Contains(err, ErrGougingDetected) || !strings.Contains(err.Error(), "download bandwidth price") {
		t.Fatal("unexpected outcome", err)
	}

	// verify bandwidth gouging - max upload
	pt = NewPriceTable(hes)
	pt.UploadBandwidthCost = allowance.MaxUploadBandwidthPrice.Mul64(2)
	err = checkPDBRGouging(allowance, pt)
	if !errors.Contains(err, ErrGougingDetected) || !strings.Contains(err.Error(), "upload bandwidth price") {
		t.Fatal("unexpected outcome", err)
	}

	// verify insane values for individual cost components related to PDBR
	for _, field := range []string{
		"InitBaseCost",
		"ReadLengthCost",
		"MemoryTimeCost",
		"HasSectorBaseCost",
	} {
		pt = NewPriceTable(DefaultHostExternalSettings)
		reflect.ValueOf(&pt).Elem().FieldByName(field).Set(reflect.ValueOf(types.SiacoinPrecision))
		err = checkPDBRGouging(allowance, pt)
		if err == nil {
			t.Fatalf("unexpected outcome for field %s, %v", field, err)
		}
	}

	// verify these checks are ignored if the funds are 0
	pt = NewPriceTable(DefaultHostExternalSettings)
	pt.InitBaseCost = types.SiacoinPrecision
	allowance.Funds = types.ZeroCurrency
	err = checkPDBRGouging(allowance, pt)
	if err != nil {
		t.Fatal("unexpected outcome", err)
	}
}

// testUpdatePriceTableGouging checks for gouging specific for the
// UpdatePriceTable RPC call.
func testUpdatePriceTableGouging(t *testing.T) {
	hes := DefaultHostExternalSettings

	allowance := DefaultAllowance
	allowance.Funds = types.SiacoinPrecision.Mul64(1e3)
	allowance.Period = types.BlockHeight(6)

	pt := NewPriceTable(hes)
	pt.Validity = minAcceptedPriceTableValidity

	// verify basic case
	err := checkUpdatePriceTableGouging(allowance, pt)
	if err != nil {
		t.Fatal("unexpected outcome", err)
	}

	// verify low validity
	pt.Validity = minAcceptedPriceTableValidity - 1
	err = checkUpdatePriceTableGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "price table validity") {
		t.Fatal("unexpected outcome", err)
	}

	// verify high UpdatePriceTableCost
	pt = NewPriceTable(hes)
	pt.Validity = minAcceptedPriceTableValidity
	durationInS := int64(pt.Validity.Seconds())
	periodInS := int64(allowance.Period) * 10 * 60 // period times 10m blocks
	numUpdates := periodInS / durationInS

	pt.UpdatePriceTableCost = allowance.Funds.MulFloat(updatePriceTableGougingPercentageThreshold * 2).Div64(uint64(numUpdates))
	err = checkUpdatePriceTableGouging(allowance, pt)
	if err == nil || !strings.Contains(err.Error(), "UpdatePriceTableCost") {
		t.Fatal("unexpected outcome", err)
	}
}

// testDownloadGouging checks that the fetch backups price gouging
// checker is correctly detecting price gouging from a host.
func testDownloadGouging(t *testing.T) {
	hes := DefaultHostExternalSettings

	allowance := DefaultAllowance
	allowance.Funds = types.SiacoinPrecision.Mul64(1e3)
	allowance.MaxDownloadBandwidthPrice = hes.DownloadBandwidthPrice.Mul64(2)
	allowance.MaxUploadBandwidthPrice = hes.UploadBandwidthPrice.Mul64(2)
	allowance.ExpectedDownload = 1 << 30 // 1GiB

	// verify basic case
	pt := NewPriceTable(hes)
	err := checkDownloadGouging(allowance, pt)
	if err != nil {
		t.Fatal("unexpected outcome", err)
	}

	// verify high init costs
	pt = NewPriceTable(hes)
	pt.InitBaseCost = types.SiacoinPrecision
	pt.ReadBaseCost = types.SiacoinPrecision
	err = checkDownloadGouging(allowance, pt)
	if !errors.Contains(err, ErrGougingDetected) {
		t.Fatal("unexpected outcome", err)
	}

	// verify DL bandwidth gouging
	pt = NewPriceTable(hes)
	pt.DownloadBandwidthCost = allowance.MaxDownloadBandwidthPrice.Mul64(2)
	err = checkDownloadGouging(allowance, pt)
	if !errors.Contains(err, ErrGougingDetected) || !strings.Contains(err.Error(), "download bandwidth price") {
		t.Fatal("unexpected outcome", err)
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

// testUploadGouging checks that the upload price gouging checker is correctly
// detecting price gouging from a host.
func testUploadGouging(t *testing.T, fractionDenom uint64) {
	oneCurrency := types.NewCurrency64(1)

	// minAllowance contains only the fields necessary to test the price gouging
	// function. The min allowance doesn't include any of the max prices,
	// because the function will ignore them if they are not set.
	minAllowance := Allowance{
		// Funds is set such that the tests come out to an easy, round number.
		// One siacoin is multiplied by the number of elements that are checked
		// for gouging, and then divided by the gounging denominator.
		Funds:  types.SiacoinPrecision.Mul64(4).Div64(fractionDenom).Sub(oneCurrency),
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
	err = checkUploadGougingWithDenominator(minAllowance, newHostSettings, fractionDenom)
	if err != nil {
		t.Fatal(err)
	}
	newHostSettings = minHostSettings
	newHostSettings.SectorAccessPrice = minHostSettings.SectorAccessPrice.Mul64(100).Div64(101)
	err = checkUploadGougingWithDenominator(minAllowance, newHostSettings, fractionDenom)
	if err != nil {
		t.Fatal(err)
	}
	newHostSettings = minHostSettings
	newHostSettings.UploadBandwidthPrice = minHostSettings.UploadBandwidthPrice.Mul64(100).Div64(101)
	err = checkUploadGougingWithDenominator(minAllowance, newHostSettings, fractionDenom)
	if err != nil {
		t.Fatal(err)
	}
	newHostSettings = minHostSettings
	newHostSettings.StoragePrice = minHostSettings.StoragePrice.Mul64(100).Div64(101)
	err = checkUploadGougingWithDenominator(minAllowance, newHostSettings, fractionDenom)
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
	err = checkUploadGougingWithDenominator(maxAllowance, minHostSettings, fractionDenom)
	if err != nil {
		t.Fatal(err)
	}

	// Should fail if the MaxRPCPrice is dropped.
	failAllowance := maxAllowance
	failAllowance.MaxRPCPrice = types.SiacoinPrecision.Sub(oneCurrency)
	err = checkUploadGougingWithDenominator(failAllowance, minHostSettings, fractionDenom)
	if err == nil {
		t.Error("expecting price gouging check to fail")
	}

	// Should fail if the MaxSectorAccessPrice is dropped.
	failAllowance = maxAllowance
	failAllowance.MaxSectorAccessPrice = types.SiacoinPrecision.Sub(oneCurrency)
	err = checkUploadGougingWithDenominator(failAllowance, minHostSettings, fractionDenom)
	if err == nil {
		t.Error("expecting price gouging check to fail")
	}

	// Should fail if the MaxStoragePrice is dropped.
	failAllowance = maxAllowance
	failAllowance.MaxStoragePrice = types.SiacoinPrecision.Div64(StreamUploadSize).Sub(oneCurrency)
	err = checkUploadGougingWithDenominator(failAllowance, minHostSettings, fractionDenom)
	if err == nil {
		t.Error("expecting price gouging check to fail")
	}

	// Should fail if the MaxUploadBandwidthPrice is dropped.
	failAllowance = maxAllowance
	failAllowance.MaxUploadBandwidthPrice = types.SiacoinPrecision.Div64(StreamUploadSize).Sub(oneCurrency)
	err = checkUploadGougingWithDenominator(failAllowance, minHostSettings, fractionDenom)
	if err == nil {
		t.Error("expecting price gouging check to fail")
	}
}
