package renter

import (
	"fmt"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
)

const (
	// pcwsGougingFractionDenom is used to identify what percentage of the
	// allowance is allowed to be spent on HasSector jobs before a worker is
	// flagged for being too expensive.
	//
	// For example, if the denom is 10, that means that if a worker's HasSector
	// cost multiplied by the total expected number of HasSector jobs to be
	// performed in a period exceeds 10% of the allowance, that worker will be
	// flagged for price gouging. If the denom is 100, the worker will be
	// flagged if the HasSector cost reaches 1% of the total cost of the
	// allowance.
	pcwsGougingFractionDenom = 25
)

var (
	// sectorLookupToDownloadRatio is an arbitrary ratio that resembles the
	// amount of lookups vs downloads. It is used in price gouging checks.
	sectorLookupToDownloadRatio = 16
)

// checkPCWSGouging verifies the cost of grabbing the HasSector information from
// a host is reasonble. The cost of completing the download is not checked.
//
// NOTE: The logic in this function assumes that every pcws results in just one
// download. The reality is that depending on the type of use case, there may be
// significantly less than 1 download per pcws (for single-user nodes that
// frequently open large movies without watching the full movie), or
// significantly more than one download per pcws (for multi-user nodes where
// users most commonly are using the same file over and over).
func checkPCWSGouging(pt modules.RPCPriceTable, allowance skymodules.Allowance, numWorkers int, numRoots int) error {
	// Check whether the download bandwidth price is too high.
	if !allowance.MaxDownloadBandwidthPrice.IsZero() && allowance.MaxDownloadBandwidthPrice.Cmp(pt.DownloadBandwidthCost) < 0 {
		return fmt.Errorf("download bandwidth price of host is %v, which is above the maximum allowed by the allowance: %v - price gouging protection enabled", pt.DownloadBandwidthCost, allowance.MaxDownloadBandwidthPrice)
	}
	// Check whether the upload bandwidth price is too high.
	if !allowance.MaxUploadBandwidthPrice.IsZero() && allowance.MaxUploadBandwidthPrice.Cmp(pt.UploadBandwidthCost) < 0 {
		return fmt.Errorf("upload bandwidth price of host is %v, which is above the maximum allowed by the allowance: %v - price gouging protection enabled", pt.UploadBandwidthCost, allowance.MaxUploadBandwidthPrice)
	}
	// If there is no allowance, price gouging checks have to be disabled,
	// because there is no baseline for understanding what might count as price
	// gouging.
	if allowance.Funds.IsZero() {
		return nil
	}

	// Calculate the cost of a has sector job.
	pb := modules.NewProgramBuilder(&pt, 0)
	for i := 0; i < numRoots; i++ {
		pb.AddHasSectorInstruction(crypto.Hash{})
	}
	programCost, _, _ := pb.Cost(true)
	ulbw, dlbw := hasSectorJobExpectedBandwidth(numRoots)
	bandwidthCost := modules.MDMBandwidthCost(pt, ulbw, dlbw)
	costHasSectorJob := programCost.Add(bandwidthCost)

	// Determine based on the allowance the number of HasSector jobs that would
	// need to be performed under normal conditions to reach the desired amount
	// of total data.
	requiredProjects := allowance.ExpectedDownload / skymodules.StreamDownloadSize
	requiredHasSectorQueries := requiredProjects * uint64(numWorkers)

	// Determine the total amount that we'd be willing to spend on all of those
	// queries before considering the host complicit in gouging.
	totalCost := costHasSectorJob.Mul64(requiredHasSectorQueries)
	reducedAllowance := allowance.Funds.Div64(pcwsGougingFractionDenom)

	// Check that we do not consider the host complicit in gouging.
	if totalCost.Cmp(reducedAllowance) > 0 {
		errStr := fmt.Sprintf("the cost of performing a HasSector job is too high - price gouging protection enabled")
		return errors.New(errStr)
	}
	return nil
}

// checkProjectDownloadGouging verifies the cost of executing the jobs performed
// by the project download are reasonable in relation to the user's allowance
// and the amount of data they intend to download
func checkProjectDownloadGouging(pt modules.RPCPriceTable, allowance skymodules.Allowance) error {
	// Check whether the download bandwidth price is too high.
	if !allowance.MaxDownloadBandwidthPrice.IsZero() && allowance.MaxDownloadBandwidthPrice.Cmp(pt.DownloadBandwidthCost) < 0 {
		return fmt.Errorf("download bandwidth price of host is %v, which is above the maximum allowed by the allowance: %v - price gouging protection enabled", pt.DownloadBandwidthCost, allowance.MaxDownloadBandwidthPrice)
	}

	// Check whether the upload bandwidth price is too high.
	if !allowance.MaxUploadBandwidthPrice.IsZero() && allowance.MaxUploadBandwidthPrice.Cmp(pt.UploadBandwidthCost) < 0 {
		return fmt.Errorf("upload bandwidth price of host is %v, which is above the maximum allowed by the allowance: %v - price gouging protection enabled", pt.UploadBandwidthCost, allowance.MaxUploadBandwidthPrice)
	}

	// If there is no allowance, price gouging checks have to be disabled,
	// because there is no baseline for understanding what might count as price
	// gouging.
	if allowance.Funds.IsZero() {
		return nil
	}

	// In order to decide whether or not the cost of performing a PDBR is too
	// expensive, we make some assumptions with regards to lookup vs download
	// job ratio and avg download size. The total cost is then compared in
	// relation to the allowance, where we verify that a fraction of the cost
	// (which we'll call reduced cost) to download the amount of data the user
	// intends to download does not exceed its allowance.

	// Calculate the cost of a has sector job
	pb := modules.NewProgramBuilder(&pt, 0)
	pb.AddHasSectorInstruction(crypto.Hash{})
	programCost, _, _ := pb.Cost(true)

	ulbw, dlbw := hasSectorJobExpectedBandwidth(1)
	bandwidthCost := modules.MDMBandwidthCost(pt, ulbw, dlbw)
	costHasSectorJob := programCost.Add(bandwidthCost)

	// Calculate the cost of a read sector job, we use StreamDownloadSize as an
	// average download size here which is 64 KiB.
	pb = modules.NewProgramBuilder(&pt, 0)
	pb.AddReadSectorInstruction(skymodules.StreamDownloadSize, 0, crypto.Hash{}, true)
	programCost, _, _ = pb.Cost(true)

	ulbw, dlbw = readSectorJobExpectedBandwidth(skymodules.StreamDownloadSize)
	bandwidthCost = modules.MDMBandwidthCost(pt, ulbw, dlbw)
	costReadSectorJob := programCost.Add(bandwidthCost)

	// Calculate the cost of a project
	costProject := costReadSectorJob.Add(costHasSectorJob.Mul64(uint64(sectorLookupToDownloadRatio)))

	// Now that we have the cost of each job, and we estimate a sector lookup to
	// download ratio of 16, all we need to do is calculate the number of
	// projects necessary to download the expected download amount.
	numProjects := allowance.ExpectedDownload / skymodules.StreamDownloadSize

	// The cost of downloading is considered too expensive if the allowance is
	// insufficient to cover a fraction of the expense to download the amount of
	// data the user intends to download
	totalCost := costProject.Mul64(numProjects)
	reducedCost := totalCost.Div64(downloadGougingFractionDenom)
	if reducedCost.Cmp(allowance.Funds) > 0 {
		return fmt.Errorf("combined PDBR pricing of host yields %v, which is more than the renter is willing to pay for downloads: %v - price gouging protection enabled", reducedCost, allowance.Funds)
	}

	return nil
}
