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
	// data they intend to download. In practice, this ends up being a farily
	// weak gouging filter because a massive portion of the allowance tends to
	// be assigned to storage, and this does not account for that.
	downloadGougingFractionDenom = 4

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
)

var (
	// minAcceptedPriceTableValidity is the minimum price table validity
	// the renter will accept.
	minAcceptedPriceTableValidity = build.Select(build.Var{
		Standard: 5 * time.Minute,
		Dev:      1 * time.Minute,
		Testing:  10 * time.Second,
	}).(time.Duration)
)

type (
	PriceTableGougingChecks struct {
		FundAccount      *GougingCheck
		PDBR             *GougingCheck
		UpdatePriceTable *GougingCheck
	}

	HostSettingsGougingChecks struct {
		Download            *GougingCheck
		FetchBackups        *GougingCheck
		FormContract        *GougingCheck
		FormPaymentContract *GougingCheck
		Upload              *GougingCheck
		UploadSnapshot      *GougingCheck
	}

	GougingCheck struct {
		IsGouging bool
		Reason    string
	}
)

func CheckPriceTableGouging(a Allowance, pt RPCPriceTable, tb types.Currency) PriceTableGougingChecks {
	return PriceTableGougingChecks{
		FundAccount:      newGougingCheck(checkFundAccountGouging(a, pt, tb)),
		PDBR:             newGougingCheck(checkPDBRGouging(a, pt)),
		UpdatePriceTable: newGougingCheck(checkUpdatePriceTableGouging(a, pt)),
		// TODO (PJ) add checkBaseCostGouging
	}
}

func CheckHostSettingsGouging(a Allowance, hes HostExternalSettings) HostSettingsGougingChecks {
	return HostSettingsGougingChecks{}
}

// checkFundAccountGouging verifies the cost of funding an ephemeral account on
// the host is reasonable, if deemed unreasonable we will block the refill and
// the worker will eventually be put into cooldown.
func checkFundAccountGouging(a Allowance, pt RPCPriceTable, targetBalance types.Currency) error {
	// If there is no allowance, price gouging checks have to be disabled,
	// because there is no baseline for understanding what might count as price
	// gouging.
	if a.Funds.IsZero() {
		return nil
	}

	// In order to decide whether or not the fund account cost is too expensive,
	// we first calculate how many times we can refill the account, taking into
	// account the refill amount and the cost to effectively fund the account.
	//
	// Note: we divide the target balance by two because more often than not the
	// refill happens the moment we drop below half of the target, this means
	// that we actually refill half the target amount most of the time.
	costOfRefill := targetBalance.Div64(2).Add(pt.FundAccountCost)
	numRefills, err := a.Funds.Div(costOfRefill).Uint64()
	if err != nil {
		return errors.AddContext(err, "unable to check fund account gouging, could not calculate the amount of refills")
	}

	// The cost of funding is considered too expensive if the total cost is
	// above a certain % of the allowance.
	totalFundAccountCost := pt.FundAccountCost.Mul64(numRefills)
	if totalFundAccountCost.Cmp(a.Funds.MulFloat(fundAccountGougingPercentageThreshold)) > 0 {
		return fmt.Errorf("fund account cost %v is considered too high, the total cost of refilling the account to spend the total allowance exceeds %v%% of the allowance - price gouging protection enabled", pt.FundAccountCost, fundAccountGougingPercentageThreshold)
	}

	return nil
}

func checkPDBRGouging(a Allowance, pt RPCPriceTable) error {
	// Check whether the download bandwidth price is too high.
	if !a.MaxDownloadBandwidthPrice.IsZero() && a.MaxDownloadBandwidthPrice.Cmp(pt.DownloadBandwidthCost) < 0 {
		return fmt.Errorf("download bandwidth price of host is %v, which is above the maximum allowed by the allowance: %v - price gouging protection enabled", pt.DownloadBandwidthCost, a.MaxDownloadBandwidthPrice)
	}

	// Check whether the upload bandwidth price is too high.
	if !a.MaxUploadBandwidthPrice.IsZero() && a.MaxUploadBandwidthPrice.Cmp(pt.UploadBandwidthCost) < 0 {
		return fmt.Errorf("upload bandwidth price of host is %v, which is above the maximum allowed by the allowance: %v - price gouging protection enabled", pt.UploadBandwidthCost, a.MaxUploadBandwidthPrice)
	}

	// If there is no allowance, price gouging checks have to be disabled,
	// because there is no baseline for understanding what might count as price
	// gouging.
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
		return fmt.Errorf("combined PDBR pricing of host yields %v, which is more than the renter is willing to pay for downloads: %v - price gouging protection enabled", reducedCost, a.Funds)
	}

	return nil
}

// checkUpdatePriceTableGouging verifies the cost of updating the price table is
// reasonable, if deemed unreasonable we will reject it and this worker will be
// put into cooldown.
func checkUpdatePriceTableGouging(a Allowance, pt RPCPriceTable) error {
	// If there is no allowance, price gouging checks have to be disabled,
	// because there is no baseline for understanding what might count as price
	// gouging.
	if a.Funds.IsZero() {
		return nil
	}

	// Verify the validity is reasonable
	if pt.Validity < minAcceptedPriceTableValidity {
		return fmt.Errorf("update price table validity %v is considered too low, the minimum accepted validity is %v", pt.Validity, minAcceptedPriceTableValidity)
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

// newGougingCheck turns the given error into a gouging check object
func newGougingCheck(err error) *GougingCheck {
	return &GougingCheck{
		IsGouging: err != nil,
		Reason:    err.Error(),
	}
}
