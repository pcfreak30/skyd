package modules

import (
	"fmt"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

const (
	// downloadGougingFractionDenom sets the fraction to 1/4 because the renter
	// should have enough money to download at least a fraction of the amount of
	// data they intend to download. In practice, this ends up being a fairly
	// weak gouging filter because a massive portion of the allowance tends to
	// be assigned to storage, and this does not account for that.
	downloadGougingFractionDenom = 4

	// downloadSnapshotGougingFractionDenom sets the fraction to 1/100 because
	// fetching backups is important, so there is less sensitivity to gouging.
	// Also, this is a rare operation.
	downloadSnapshotGougingFractionDenom = 100

	// fundAccountGougingPercentageThreshold is the percentage threshold, in
	// relation to the allowance, at which we consider the cost of funding an
	// account to be too expensive. E.g. the cost of funding the account as many
	// times as necessary to spend the total allowance should never exceed 1% of
	// the total allowance.
	fundAccountGougingPercentageThreshold = .01

	// sectorLookupToDownloadRatio is an arbitrary ratio that resembles the
	// amount of lookups vs downloads. It is used in price gouging checks.
	sectorLookupToDownloadRatio = 16

	// updatePriceTableGougingPercentageThreshold is the percentage threshold,
	// in relation to the allowance, at which we consider the cost of updating
	// the price table to be too expensive. E.g. the cost of updating the price
	// table over the total allowance period should never exceed 1% of the total
	// allowance.
	updatePriceTableGougingPercentageThreshold = .01

	// uploadGougingFractionDenom sets the gouging fraction to 1/4 based on the
	// idea that the user should be able to hit at least some fraction of their
	// desired upload volume using some fraction of hosts.
	uploadGougingFractionDenom = 4

	// uploadSnapshotGougingFractionDenom sets the fraction to 1/100 because
	// uploading backups is important, so there is less sensitivity to gouging.
	// Also, this is a rare operation.
	uploadSnapshotGougingFractionDenom = 100
)

var (
	// ErrGougingDetected is returned when price gouging was detected in one of
	// the fields of either the price table or the host's external settings.
	ErrGougingDetected = errors.New("price gouging detected")

	// minAcceptedPriceTableValidity is the minimum price table validity
	// the renter will accept.
	minAcceptedPriceTableValidity = build.Select(build.Var{
		Standard: 5 * time.Minute,
		Dev:      1 * time.Minute,
		Testing:  10 * time.Second,
	}).(time.Duration)
)

type (
	// PriceTableGougingChecks contains a series of gouging checks that apply
	// to the host's price table.
	PriceTableGougingChecks struct {
		Download         GougingCheck
		DownloadSnapshot GougingCheck
		FundAccount      GougingCheck
		PDBR             GougingCheck
		UpdatePriceTable GougingCheck
	}

	// HostSettingsGougingChecks contains a series of gouging checks that apply
	// to the host's external settings
	HostSettingsGougingChecks struct {
		FormContract   GougingCheck
		Upload         GougingCheck
		UploadSnapshot GougingCheck
	}

	// GougingCheck is an interface that returns for every type of check whether
	// or not the host is gouging and why it is considered gouging.
	GougingCheck interface {
		IsGouging() bool
		error
	}

	// check is a small helper struct that implements the GougingCheck interface
	// by wrapping the error object returned by the gouging check.
	check struct {
		err error
	}
)

// IsGouging returns true if and only if the gouging check returned an error.
func (c *check) IsGouging() bool {
	return c.err != nil
}

// Error returns why the host was considered to be gouging in case it was
// gouging and an empty string if not.
func (c *check) Error() string {
	if c.err != nil {
		return c.err.Error()
	}
	return ""
}

// CheckPriceTableGouging performs a series of checks that verify whether or not
// the given price table indicates price gouging.
func CheckPriceTableGouging(a Allowance, pt RPCPriceTable, tb types.Currency) PriceTableGougingChecks {
	if a.IsUnset() {
		build.Critical("CheckPriceTableGouging should not be executed when the allowance has not been set yet.")
	}
	return PriceTableGougingChecks{
		Download:         &check{err: checkDownloadGouging(a, pt)},
		DownloadSnapshot: &check{err: checkDownloadSnapshotGouging(a, pt)},
		FundAccount:      &check{err: checkFundAccountGouging(a, pt, tb)},
		PDBR:             &check{err: checkPDBRGouging(a, pt)},
		UpdatePriceTable: &check{err: checkUpdatePriceTableGouging(a, pt)},
	}
}

// CheckHostSettingsGouging performs a series of checks that verify whether or
// not the given host settings indicates price gouging.
func CheckHostSettingsGouging(a Allowance, hes HostExternalSettings) HostSettingsGougingChecks {
	if a.IsUnset() {
		build.Critical("CheckHostSettingsGouging should not be executed when the allowance has not been set yet.")
	}
	return HostSettingsGougingChecks{
		FormContract:   &check{err: checkFormContractGouging(a, hes)},
		Upload:         &check{err: checkUploadGouging(a, hes)},
		UploadSnapshot: &check{err: checkUploadSnapshotGouging(a, hes)},
	}
}

// calculateExpectedDownloadCost is a helper function that returns the cost of
// downloading the expected download amount as dictated by the allowance and the
// host's price table.
//
// NOTE: we treat all downloads being the StreamDownloadSize
func calculateExpectedDownloadCost(a Allowance, pt RPCPriceTable) types.Currency {
	rpcCost := MDMInitCost(&pt, 48, 1).Add(MDMReadCost(&pt, StreamDownloadSize)) // 48 bytes is the length of a read program with 1 instruction
	bandwidthCost := pt.DownloadBandwidthCost.Mul64(StreamDownloadSize)
	downloadCostPerByte := rpcCost.Add(bandwidthCost).Div64(StreamDownloadSize)
	return downloadCostPerByte.Mul64(a.ExpectedDownload)
}

// calculateExpectedUploadCost is a helper function that returns the cost of
// uploading the expected upload amount as dictated by the allowance.
//
// NOTE: we treat all uploads being the StreamUploadSize
func calculateExpectedUploadCost(a Allowance, hes HostExternalSettings) types.Currency {
	rpcCost := hes.BaseRPCPrice.Add(hes.SectorAccessPrice)
	bandwidthCost := hes.UploadBandwidthPrice.Mul64(StreamUploadSize).Add(hes.StoragePrice.Mul64(uint64(a.Period)).Mul64(StreamUploadSize))
	uploadCostPerByte := rpcCost.Add(bandwidthCost).Div64(StreamUploadSize)
	return uploadCostPerByte.Mul64(a.ExpectedStorage)
}

// checkDownloadGouging looks at the current renter allowance and the host's
// price table and determines whether a download should be halted due to price
// gouging.
func checkDownloadGouging(a Allowance, pt RPCPriceTable) error {
	// Check gouging for all related price components.
	if err := errors.Compose(
		checkHardcodedConstants(pt),
		checkDLBandwidthGouging(a, pt),
	); err != nil {
		return err
	}

	// If there is no allowance we have no baseline for understanding what might
	// count as price gouging.
	if a.Funds.IsZero() {
		return nil
	}

	// Check that the cost makes sense in the context of the overall allowance.
	// The general idea is to compute the total cost of performing the same
	// action repeatedly until a fraction of the desired total resource
	// consumption established by the allowance has been reached.
	totalCost := calculateExpectedDownloadCost(a, pt)
	downloadCost := totalCost.Div64(downloadGougingFractionDenom)
	if downloadCost.Cmp(a.Funds) > 0 {
		return errors.AddContext(ErrGougingDetected,
			fmt.Sprintf("combined download pricing of host yields %v, which is more than the renter is willing to pay for downloads: %v", downloadCost, a.Funds))
	}

	return nil
}

// checkFetchBackupsGouging looks at the current renter allowance and the active
// settings for a host and determines whether an backup fetch should be halted
// due to price gouging.
func checkDownloadSnapshotGouging(a Allowance, pt RPCPriceTable) error {
	// Check gouging for all related price components.
	if err := errors.Compose(
		checkHardcodedConstants(pt),
		checkDLBandwidthGouging(a, pt),
	); err != nil {
		return err
	}

	// If there is no allowance we have no baseline for understanding what might
	// count as price gouging.
	if a.Funds.IsZero() {
		return nil
	}

	// Check that the cost makes sense in the context of the overall allowance.
	// The general idea is to compute the total cost of performing the same
	// action repeatedly until a fraction of the desired total resource
	// consumption established by the allowance has been reached.
	totalCost := calculateExpectedDownloadCost(a, pt)
	downloadCost := totalCost.Div64(downloadSnapshotGougingFractionDenom)
	if downloadCost.Cmp(a.Funds) > 0 {
		return fmt.Errorf("combined download pricing of host yields %v, which is more than the renter is willing to pay for download a snapshot: %v - price gouging protection enabled", downloadCost, a.Funds)
	}

	return nil
}

// checkFormContractGouging will check whether the pricing for forming
// this contract triggers any price gouging warnings.
func checkFormContractGouging(a Allowance, hes HostExternalSettings) error {
	return errors.Compose(
		checkBaseRPCGouging(a, hes),
		checkContractPriceGouging(a, hes),
		checkSectorAccessPriceGouging(a, hes),
		checkStoragePriceGouging(a, hes),
	)
}

// checkFundAccountGouging verifies the cost of funding an ephemeral account on
// the host is reasonable, if deemed unreasonable we will block the refill and
// the worker will eventually be put into cooldown.
func checkFundAccountGouging(a Allowance, pt RPCPriceTable, targetBalance types.Currency) error {
	// Check gouging for all related price components.
	err := checkHardcodedConstants(pt)
	if err != nil {
		return err
	}

	// If there is no allowance we have no baseline for understanding what might
	// count as price gouging.
	if a.Funds.IsZero() {
		return nil
	}

	// In order to decide whether or not the fund account cost is too expensive,
	// we first calculate how many times we can refill the account, taking into
	// account the refill amount and the cost to effectively fund the account.
	//
	// Note: we divide the target balance by two because we refill when we drop
	// below half of the target.
	costOfRefill := targetBalance.Div64(2).Add(pt.FundAccountCost)
	numRefills, err := a.Funds.Div(costOfRefill).Uint64()
	if err != nil {
		return errors.AddContext(err, "unable to check fund account gouging, could not calculate the amount of refills")
	}

	// The cost of funding is considered too expensive if the total cost is
	// above a certain % of the allowance.
	totalFundAccountCost := pt.FundAccountCost.Mul64(numRefills)
	if totalFundAccountCost.Cmp(a.Funds.MulFloat(fundAccountGougingPercentageThreshold)) > 0 {
		return errors.AddContext(ErrGougingDetected,
			fmt.Sprintf("fund account cost of %s is considered too high, the total cost of refilling the account exceeds %v%% of the allowance", pt.FundAccountCost.HumanString(), fundAccountGougingPercentageThreshold))
	}

	return nil
}

// checkPDBRGouging verifies the cost of performing a PDBR on the host is
// reasonable.
func checkPDBRGouging(a Allowance, pt RPCPriceTable) error {
	// Check gouging for all related price components.
	err := errors.Compose(
		checkBandwidthGouging(a, pt),
		checkHardcodedConstants(pt),
	)
	if err != nil {
		return err
	}

	// If there is no allowance we have no baseline for understanding what might
	// count as price gouging.
	if a.Funds.IsZero() {
		return nil
	}

	// In order to decide whether or not the cost of performing a PDBR is too
	// expensive, we make some assumptions with regards to lookup vs download
	// job ratio and avg download size. The total cost is then compared in
	// relation to the allowance, where we verify that a fraction of the cost
	// (which we'll call reduced cost) to download the amount of data the user
	// intends to download does not exceed its a.

	// Calculate the cost of a has sector job
	pb := NewProgramBuilder(&pt, 0)
	pb.AddHasSectorInstruction(crypto.Hash{})
	programCost, _, _ := pb.Cost(true)

	ulbw, dlbw := HasSectorJobExpectedBandwidth()
	bandwidthCost := MDMBandwidthCost(pt, ulbw, dlbw)
	costHasSectorJob := programCost.Add(bandwidthCost)

	// Calculate the cost of a read sector job, we use StreamDownloadSize as an
	// average download size here which is 64 KiB.
	pb = NewProgramBuilder(&pt, 0)
	pb.AddReadSectorInstruction(StreamDownloadSize, 0, crypto.Hash{}, true)
	programCost, _, _ = pb.Cost(true)

	ulbw, dlbw = ReadSectorJobExpectedBandwidth(StreamDownloadSize)
	bandwidthCost = MDMBandwidthCost(pt, ulbw, dlbw)
	costReadSectorJob := programCost.Add(bandwidthCost)

	// Calculate the cost of a project
	costProject := costReadSectorJob.Add(costHasSectorJob.Mul64(uint64(sectorLookupToDownloadRatio)))

	// Now that we have the cost of each job, and we estimate a sector lookup to
	// download ratio of 16, all we need to do is calculate the number of
	// projects necessary to download the expected download amount.
	numProjects := a.ExpectedDownload / StreamDownloadSize

	// The cost of downloading is considered too expensive if the allowance is
	// insufficient to cover a fraction of the expense to download the amount of
	// data the user intends to download
	totalCost := costProject.Mul64(numProjects)
	reducedCost := totalCost.Div64(downloadGougingFractionDenom)
	if reducedCost.Cmp(a.Funds) > 0 {
		return errors.AddContext(ErrGougingDetected, fmt.Sprintf("combined PDBR pricing of host yields %v, which is more than the renter is willing to pay for downloads: %v", reducedCost, a.Funds))
	}

	return nil
}

// checkUpdatePriceTableGouging verifies the cost of updating the price table is
// reasonable, if deemed unreasonable we will reject it and this worker will be
// put into cooldown.
func checkUpdatePriceTableGouging(a Allowance, pt RPCPriceTable) error {
	// Check RPC cost
	err := checkHardcodedConstants(pt)
	if err != nil {
		return err
	}

	// Verify the validity is reasonable
	if pt.Validity < minAcceptedPriceTableValidity {
		return fmt.Errorf("update price table validity %v is considered too low, the minimum accepted validity is %v", pt.Validity, minAcceptedPriceTableValidity)
	}

	// If there is no allowance, price gouging checks have to be disabled,
	// because there is no baseline for understanding what might count as price
	// gouging.
	if a.Funds.IsZero() {
		return nil
	}

	// In order to decide whether or not the update price table cost is too
	// expensive, we first have to calculate how many times we'll need to update
	// the price table over the entire allowance period
	durationInS := int64(pt.Validity.Seconds())
	periodInS := int64(a.Period) * 10 * 60 // period times 10m blocks
	numUpdates := periodInS / durationInS

	// The cost of updating is considered too expensive if the total cost is
	// above a certain % of the allowance.
	totalUpdateCost := pt.UpdatePriceTableCost.Mul64(uint64(numUpdates))
	if totalUpdateCost.Cmp(a.Funds.MulFloat(updatePriceTableGougingPercentageThreshold)) > 0 {
		return fmt.Errorf("update price table cost %v is considered too high, the total cost over the entire duration of the allowance periods exceeds %v%% of the allowance - price gouging protection enabled", pt.UpdatePriceTableCost, updatePriceTableGougingPercentageThreshold)
	}

	return nil
}

// checkUploadGouging looks at the current renter allowance and the active
// settings for a host and determines whether an upload should be halted due to
// price gouging.
func checkUploadGouging(a Allowance, hes HostExternalSettings) error {
	return checkUploadGougingWithDenominator(a, hes, uploadGougingFractionDenom)
}

// checkUploadSnapshotGouging looks at the current renter allowance and the
// active settings for a host and determines whether a snapshot upload should be
// halted due to price gouging.
func checkUploadSnapshotGouging(a Allowance, hes HostExternalSettings) error {
	return checkUploadGougingWithDenominator(a, hes, uploadSnapshotGougingFractionDenom)
}

// checkUploadGougingWithDenominator looks at the current renter allowance and
// the active settings for a host and determines whether an upload should be
// halted due to price gouging. It is custom because it takes an upload cost
// fraction denominator.
//
// NOTE: Currently this function treats all uploads as being the stream upload
// size and assumes that data is actually being appended to the host. As the
// worker gains more modification actions on the host, this check can be split
// into different checks that vary based on the operation being performed.
func checkUploadGougingWithDenominator(a Allowance, hes HostExternalSettings, fractionDenom uint64) error {
	// Check gouging for all related price components.
	if err := errors.Compose(
		checkBaseRPCGouging(a, hes),
		checkUploadBandwidthGouging(a, hes),
		checkSectorAccessPriceGouging(a, hes),
		checkStoragePriceGouging(a, hes),
	); err != nil {
		return err
	}

	// If there is no allowance, general price gouging checks have to be
	// disabled, because there is no baseline for understanding what might count
	// as price gouging.
	if a.Funds.IsZero() {
		return nil
	}

	// Check that the cost makes sense in the context of the overall allowance.
	// The general idea is to compute the total cost of performing the same
	// action repeatedly until a fraction of the desired total resource
	// consumption established by the allowance has been reached.
	totalCost := calculateExpectedUploadCost(a, hes)
	uploadCost := totalCost.Div64(fractionDenom)
	if uploadCost.Cmp(a.Funds) > 0 {
		return fmt.Errorf("combined upload pricing of host yields %v, which is more than the renter is willing to pay for uploads: %v - price gouging protection enabled", uploadCost, a.Funds)
	}

	return nil
}

// checkBaseRPCGouging checks for gouging in the host's base RPC pricing.
func checkBaseRPCGouging(a Allowance, hes HostExternalSettings) error {
	if !a.MaxRPCPrice.IsZero() && a.MaxRPCPrice.Cmp(hes.BaseRPCPrice) < 0 {
		return fmt.Errorf("RPC price of host is %v, which is above the maximum allowed by the allowance: %v", hes.BaseRPCPrice, a.MaxRPCPrice)
	}
	return nil
}

// checkContractPriceGouging checks for gouging in the host's contract price
// settings.
func checkContractPriceGouging(a Allowance, hes HostExternalSettings) error {
	if !a.MaxContractPrice.IsZero() && a.MaxContractPrice.Cmp(hes.ContractPrice) < 0 {
		return errors.New("contract price of host is too high - price gouging protection enabled")
	}

	// Check whether the form contract price does not leave enough room for
	// uploads and downloads. At least half of the payment contract's funds need
	// to remain for download bandwidth.
	if !a.PaymentContractInitialFunding.IsZero() && a.PaymentContractInitialFunding.Div64(2).Cmp(hes.ContractPrice) <= 0 {
		return errors.New("contract price of host is too high - extortion protection enabled")
	}

	return nil
}

// checkDownloadBandwidthGouging checks for gouging in the host's download
// pricing.
func checkDownloadBandwidthGouging(a Allowance, hes HostExternalSettings) error {
	if !a.MaxDownloadBandwidthPrice.IsZero() && a.MaxDownloadBandwidthPrice.Cmp(hes.DownloadBandwidthPrice) < 0 {
		return errors.AddContext(ErrGougingDetected, fmt.Sprintf("download bandwidth price of host is %v, which is above the maximum allowed by the allowance: %v", hes.DownloadBandwidthPrice, a.MaxDownloadBandwidthPrice))
	}
	return nil
}

// checkDLBandwidthGouging checks for gouging in the host's DL bandwidth
// pricing.
func checkDLBandwidthGouging(a Allowance, pt RPCPriceTable) error {
	if !a.MaxDownloadBandwidthPrice.IsZero() && a.MaxDownloadBandwidthPrice.Cmp(pt.DownloadBandwidthCost) < 0 {
		return errors.AddContext(ErrGougingDetected, fmt.Sprintf("download bandwidth price of host is %v, which is above the maximum allowed by the allowance: %v", pt.DownloadBandwidthCost, a.MaxDownloadBandwidthPrice))
	}
	return nil
}

// checkBandwidthGouging checks for gouging in the host's bandwidth pricing.
func checkBandwidthGouging(a Allowance, pt RPCPriceTable) error {
	if !a.MaxDownloadBandwidthPrice.IsZero() && a.MaxDownloadBandwidthPrice.Cmp(pt.DownloadBandwidthCost) < 0 {
		return errors.AddContext(ErrGougingDetected, fmt.Sprintf("download bandwidth price of host is %v, which is above the maximum allowed by the allowance: %v", pt.DownloadBandwidthCost, a.MaxDownloadBandwidthPrice))
	}

	if !a.MaxUploadBandwidthPrice.IsZero() && a.MaxUploadBandwidthPrice.Cmp(pt.UploadBandwidthCost) < 0 {
		return errors.AddContext(ErrGougingDetected, fmt.Sprintf("upload bandwidth price of host is %v, which is above the maximum allowed by the allowance: %v", pt.UploadBandwidthCost, a.MaxUploadBandwidthPrice))
	}
	return nil
}

// checkHardcodedConstants checks for gouging in the fields from the host's
// price table that should be equal to the hardcoded constant of 1H.
//
// TODO: these hardcoded constants should at one point be covered by more
// intricate gouging checks, for now though it suffices to compare them to the
// hardcoded constant value since hosts can't update these fields without
// rebuilding anyway.
func checkHardcodedConstants(pt RPCPriceTable) error {
	buildErr := func(field string, value types.Currency) error {
		return errors.AddContext(ErrGougingDetected, fmt.Sprintf("'%v' of %v is not equal to expected hardcoded constant", field, value.HumanString()))
	}

	if !pt.AccountBalanceCost.Equals64(1) {
		return buildErr("AccountBalanceCost", pt.AccountBalanceCost)
	}
	if !pt.DropSectorsBaseCost.Equals64(1) {
		return buildErr("DropSectorsBaseCost", pt.DropSectorsBaseCost)
	}
	if !pt.DropSectorsUnitCost.Equals64(1) {
		return buildErr("DropSectorsUnitCost", pt.DropSectorsUnitCost)
	}
	if !pt.FundAccountCost.Equals64(1) {
		return buildErr("FundAccountCost", pt.FundAccountCost)
	}
	if !pt.HasSectorBaseCost.Equals64(1) {
		return buildErr("HasSectorBaseCost", pt.HasSectorBaseCost)
	}
	if !pt.MemoryTimeCost.Equals64(1) {
		return buildErr("MemoryTimeCost", pt.MemoryTimeCost)
	}
	if !pt.ReadLengthCost.Equals64(1) {
		return buildErr("ReadLengthCost", pt.ReadLengthCost)
	}
	if !pt.SwapSectorCost.Equals64(1) {
		return buildErr("SwapSectorCost", pt.SwapSectorCost)
	}
	if !pt.UpdatePriceTableCost.Equals64(1) {
		return buildErr("UpdatePriceTableCost", pt.UpdatePriceTableCost)
	}
	if !pt.WriteBaseCost.Equals64(1) {
		return buildErr("WriteBaseCost", pt.WriteBaseCost)
	}
	if !pt.WriteLengthCost.Equals64(1) {
		return buildErr("WriteLengthCost", pt.WriteLengthCost)
	}

	return nil
}

// checkUploadBandwidthGouging checks for gouging in the host's upload pricing.
func checkUploadBandwidthGouging(a Allowance, hes HostExternalSettings) error {
	if !a.MaxUploadBandwidthPrice.IsZero() && a.MaxUploadBandwidthPrice.Cmp(hes.UploadBandwidthPrice) < 0 {
		return fmt.Errorf("upload bandwidth price of host is %v, which is above the maximum allowed by the allowance: %v", hes.UploadBandwidthPrice, a.MaxUploadBandwidthPrice)
	}
	return nil
}

// checkSectorAccessPriceGouging checks sector access in the host's storage
// pricing.
func checkSectorAccessPriceGouging(a Allowance, hes HostExternalSettings) error {
	if !a.MaxSectorAccessPrice.IsZero() && a.MaxSectorAccessPrice.Cmp(hes.SectorAccessPrice) < 0 {
		return fmt.Errorf("sector access price of host is %v, which is above the maximum allowed by the allowance: %v", hes.SectorAccessPrice, a.MaxSectorAccessPrice)
	}
	return nil
}

// checkStoragePriceGouging checks gouging in the host's storage pricing.
func checkStoragePriceGouging(a Allowance, hes HostExternalSettings) error {
	if !a.MaxStoragePrice.IsZero() && a.MaxStoragePrice.Cmp(hes.StoragePrice) < 0 {
		return fmt.Errorf("storage price of host is %v, which is above the maximum allowed by the allowance: %v", hes.StoragePrice, a.MaxStoragePrice)
	}
	return nil
}
