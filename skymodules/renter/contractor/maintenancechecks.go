package contractor

import (
	"math"
	"math/big"

	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/proto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/persist"
	"go.sia.tech/siad/types"
)

type utilityUpdateStatus int

const (
	_ = iota
	noUpdate
	suggestedUtilityUpdate
	necessaryUtilityUpdate
)

// managedCheckHostScore checks host scorebreakdown against minimum accepted
// scores.  forceUpdate is true if the utility change must be taken.
func (c *Contractor) managedCheckHostScore(contract skymodules.RenterContract, sb skymodules.HostScoreBreakdown, minScoreGFR, minScoreGFU types.Currency) (skymodules.ContractUtility, utilityUpdateStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()

	u := contract.Utility

	// Check whether the contract is a payment contract. Payment contracts
	// cannot be marked !GFR for poor score.
	var size uint64
	if len(contract.Transaction.FileContractRevisions) > 0 {
		size = contract.Transaction.FileContractRevisions[0].NewFileSize
	}
	paymentContract := !c.allowance.PaymentContractInitialFunding.IsZero() && size == 0

	// Contract has no utility if the score is poor. Cannot be marked as bad if
	// the contract is a payment contract.
	badScore := !minScoreGFR.IsZero() && sb.Score.Cmp(minScoreGFR) < 0
	if badScore && !paymentContract {
		// Log if the utility has changed.
		if u.GoodForUpload || u.GoodForRenew {
			c.staticLog.Printf("Marking contract as having no utility because of host score: %v", contract.ID)
			c.staticLog.Println("Min Score:", minScoreGFR)
			c.staticLog.Println("Score:    ", sb.Score)
			c.staticLog.Println("Age Adjustment:        ", sb.AgeAdjustment)
			c.staticLog.Println("Base Price Adjustment: ", sb.BasePriceAdjustment)
			c.staticLog.Println("Burn Adjustment:       ", sb.BurnAdjustment)
			c.staticLog.Println("Collateral Adjustment: ", sb.CollateralAdjustment)
			c.staticLog.Println("Duration Adjustment:   ", sb.DurationAdjustment)
			c.staticLog.Println("Interaction Adjustment:", sb.InteractionAdjustment)
			c.staticLog.Println("Price Adjustment:      ", sb.PriceAdjustment)
			c.staticLog.Println("Storage Adjustment:    ", sb.StorageRemainingAdjustment)
			c.staticLog.Println("Uptime Adjustment:     ", sb.UptimeAdjustment)
			c.staticLog.Println("Version Adjustment:    ", sb.VersionAdjustment)
		}
		u.GoodForUpload = false
		u.GoodForRenew = false

		c.staticLog.Println("Adding contract utility update to churnLimiter queue")
		return u, suggestedUtilityUpdate
	}

	// Contract should not be used for uploading if the score is poor.
	if !minScoreGFU.IsZero() && sb.Score.Cmp(minScoreGFU) < 0 {
		if u.GoodForUpload {
			c.staticLog.Printf("Marking contract as not good for upload because of a poor score: %v", contract.ID)
			c.staticLog.Println("Min Score:", minScoreGFU)
			c.staticLog.Println("Score:    ", sb.Score)
			c.staticLog.Println("Age Adjustment:        ", sb.AgeAdjustment)
			c.staticLog.Println("Base Price Adjustment: ", sb.BasePriceAdjustment)
			c.staticLog.Println("Burn Adjustment:       ", sb.BurnAdjustment)
			c.staticLog.Println("Collateral Adjustment: ", sb.CollateralAdjustment)
			c.staticLog.Println("Duration Adjustment:   ", sb.DurationAdjustment)
			c.staticLog.Println("Interaction Adjustment:", sb.InteractionAdjustment)
			c.staticLog.Println("Price Adjustment:      ", sb.PriceAdjustment)
			c.staticLog.Println("Storage Adjustment:    ", sb.StorageRemainingAdjustment)
			c.staticLog.Println("Uptime Adjustment:     ", sb.UptimeAdjustment)
			c.staticLog.Println("Version Adjustment:    ", sb.VersionAdjustment)
		}
		if !u.GoodForRenew {
			c.staticLog.Println("Marking contract as being good for renew", contract.ID)
		}
		u.GoodForUpload = false
		u.GoodForRenew = true
		return u, necessaryUtilityUpdate
	}
	return u, noUpdate
}

// managedCriticalUtilityChecks performs critical checks on a contract that
// would require, with no exceptions, marking the contract as !GFR and/or !GFU.
// Returns true if and only if and of the checks passed and require the utility
// to be updated.
//
// NOTE: 'needsUpdate' should return 'true' if the contract should be marked as
// !GFR and !GFU, even if the contract is already marked as such. If
// 'needsUpdate' is set to true, other checks which may change those values will
// be ignored and the contract will remain marked as having no utility.
func (c *Contractor) managedCriticalUtilityChecks(sc *proto.SafeContract, host skymodules.HostDBEntry, sb skymodules.HostScoreBreakdown) (skymodules.ContractUtility, bool) {
	contract := sc.Metadata()

	c.mu.RLock()
	allowance := c.allowance
	blockHeight := c.blockHeight
	renewWindow := c.allowance.RenewWindow
	period := c.allowance.Period
	_, renewed := c.renewedTo[contract.ID]
	c.mu.RUnlock()

	// A contract with a dead score should not be used for anything.
	u, needsUpdate := deadScoreCheck(contract.Utility, sb.Score)
	if needsUpdate {
		return u, needsUpdate
	}

	// A contract that has been renewed should be set to !GFU and !GFR.
	u, needsUpdate = renewedCheck(contract.Utility, renewed)
	if needsUpdate {
		return u, needsUpdate
	}

	u, needsUpdate = maxRevisionCheck(contract.Utility, sc.LastRevision().NewRevisionNumber)
	if needsUpdate {
		return u, needsUpdate
	}

	u, needsUpdate = badContractCheck(contract.Utility)
	if needsUpdate {
		return u, needsUpdate
	}

	u, needsUpdate = offlineCheck(contract, host, c.staticLog)
	if needsUpdate {
		return u, needsUpdate
	}

	u, needsUpdate = upForRenewalCheck(contract, renewWindow, blockHeight, c.staticLog)
	if needsUpdate {
		return u, needsUpdate
	}

	u, needsUpdate = sufficientFundsCheck(contract, host, period, c.staticLog)
	if needsUpdate {
		return u, needsUpdate
	}

	u, needsUpdate = outOfStorageCheck(contract, blockHeight, c.staticLog)
	if needsUpdate {
		return u, needsUpdate
	}

	u, needsUpdate = storageGougingCheck(allowance, host, sc.LastRevision().NewFileSize)
	if needsUpdate {
		return u, needsUpdate
	}

	return contract.Utility, false
}

// managedBasicUtilityChecks handles all utility checks which don't necessarily
// set both gfu and gfr to false.
func (c *Contractor) managedBasicUtilityChecks(sc *proto.SafeContract, host skymodules.HostDBEntry, sb skymodules.HostScoreBreakdown, minScoreGFR, minScoreGFU types.Currency) (skymodules.ContractUtility, utilityUpdateStatus) {
	contract := sc.Metadata()

	c.mu.RLock()
	allowance := c.allowance
	c.mu.RUnlock()

	// Check the host scorebreakdown against the minimum accepted scores.
	u, utilityUpdateStatus := c.managedCheckHostScore(contract, sb, minScoreGFR, minScoreGFU)

	// Check the storage price.
	if !allowance.MaxStoragePrice.IsZero() && host.StoragePrice.Cmp(allowance.MaxStoragePrice) > 0 {
		u.GoodForUpload = false
		utilityUpdateStatus = necessaryUtilityUpdate
	}

	return u, utilityUpdateStatus
}

// managedHostInHostDBCheck checks if the host is in the hostdb and not
// filtered.  Returns true if a check fails and the utility returned must be
// used to update the contract state.
func (c *Contractor) managedHostInHostDBCheck(contract skymodules.RenterContract) (skymodules.HostDBEntry, skymodules.ContractUtility, bool) {
	u := contract.Utility
	host, exists, err := c.staticHDB.Host(contract.HostPublicKey)
	// Contract has no utility if the host is not in the database. Or is
	// filtered by the blacklist or whitelist. Or if there was an error
	if !exists || host.Filtered || err != nil {
		// Log if the utility has changed.
		if u.GoodForUpload || u.GoodForRenew {
			c.staticLog.Printf("Marking contract as having no utility because found in hostDB: %v, or host is Filtered: %v - %v", exists, host.Filtered, contract.ID)
		}
		u.GoodForUpload = false
		u.GoodForRenew = false
		return host, u, true
	}

	// TODO: If the host is not in the hostdb, we need to do some sort of rescan
	// to recover the host. The hostdb is not supposed to be dropping hosts that
	// we have formed contracts with. We should do what we can to get the host
	// back.

	return host, u, false
}

// badContractCheck checks whether the contract has been marked as bad. If the
// contract has been marked as bad, GoodForUpload and GoodForRenew need to be
// set to false to prevent the renter from using this contract.
func badContractCheck(u skymodules.ContractUtility) (skymodules.ContractUtility, bool) {
	if u.BadContract {
		u.GoodForUpload = false
		u.GoodForRenew = false
		return u, true
	}
	return u, false
}

// maxRevisionCheck will return a locked utility if the contract has reached its
// max revision.
func maxRevisionCheck(u skymodules.ContractUtility, revisionNumber uint64) (skymodules.ContractUtility, bool) {
	if revisionNumber == math.MaxUint64 {
		u.GoodForUpload = false
		u.GoodForRenew = false
		u.Locked = true
		return u, true
	}
	return u, false
}

// offLineCheck checks if the host for this contract is offline.
// Returns true if a check fails and the utility returned must be used to update
// the contract state.
func offlineCheck(contract skymodules.RenterContract, host skymodules.HostDBEntry, log *persist.Logger) (skymodules.ContractUtility, bool) {
	u := contract.Utility
	// Contract has no utility if the host is offline.
	if isOffline(host) {
		// Log if the utility has changed.
		if u.GoodForUpload || u.GoodForRenew {
			log.Println("Marking contract as having no utility because of host being offline", contract.ID)
		}
		u.GoodForUpload = false
		u.GoodForRenew = false
		return u, true
	}
	return u, false
}

// outOfStorageCheck checks if the host is running out of storage.
// Returns true if a check fails and the utility returned must be used to update
// the contract state.
func outOfStorageCheck(contract skymodules.RenterContract, blockHeight types.BlockHeight, log *persist.Logger) (skymodules.ContractUtility, bool) {
	u := contract.Utility
	// If LastOOSErr has never been set, return false.
	if u.LastOOSErr == 0 {
		return u, false
	}
	// Contract should not be used for uploading if the host is out of storage.
	if blockHeight-u.LastOOSErr <= oosRetryInterval {
		if u.GoodForUpload {
			log.Println("Marking contract as not being good for upload due to the host running out of storage:", contract.ID)
		}
		if !u.GoodForRenew {
			log.Println("Marking contract as being good for renew:", contract.ID)
		}
		u.GoodForUpload = false
		u.GoodForRenew = true
		return u, true
	}
	return u, false
}

// deadScoreCheck will return a contract with no utility and a required update
// if the contract has a score <= 1.
func deadScoreCheck(u skymodules.ContractUtility, score types.Currency) (skymodules.ContractUtility, bool) {
	if score.Cmp(types.NewCurrency64(1)) <= 0 {
		u.GoodForUpload = false
		u.GoodForRenew = false
		return u, true
	}
	return u, false
}

// renewedCheck will return a contract with no utility and a required update if
// the contract has been renewed, no changes otherwise.
func renewedCheck(u skymodules.ContractUtility, renewed bool) (skymodules.ContractUtility, bool) {
	if renewed {
		u.GoodForUpload = false
		u.GoodForRenew = false
		return u, true
	}
	return u, false
}

// storageGougingCheck makes sure the host's storage price isn't too expensive.
func storageGougingCheck(allowance skymodules.Allowance, host skymodules.HostDBEntry, contractSize uint64) (skymodules.ContractUtility, bool) {
	if !allowance.MaxStoragePrice.IsZero() && host.StoragePrice.Cmp(allowance.MaxStoragePrice) > 0 {
		// If the contract contains data don't renew to make sure we
		// don't pay for its storage.
		if contractSize > 0 {
			return skymodules.ContractUtility{
				GoodForUpload: false,
				GoodForRenew:  false,
			}, true
		}
		// If it's empty, we keep the contract the way it is for now.
		// managedBasicUtilityChecks will handle setting it !gfu later.
	}
	return skymodules.ContractUtility{}, false
}

// sufficientFundsCheck checks if there are enough funds left in the contract
// for uploads.
// Returns true if a check fails and the utility returned must be used to update
// the contract state.
func sufficientFundsCheck(contract skymodules.RenterContract, host skymodules.HostDBEntry, period types.BlockHeight, log *persist.Logger) (skymodules.ContractUtility, bool) {
	u := contract.Utility

	// Contract should not be used for uploading if the contract does
	// not have enough money remaining to perform the upload.
	blockBytes := types.NewCurrency64(modules.SectorSize * uint64(period))
	sectorStoragePrice := host.StoragePrice.Mul(blockBytes)
	sectorUploadBandwidthPrice := host.UploadBandwidthPrice.Mul64(modules.SectorSize)
	sectorDownloadBandwidthPrice := host.DownloadBandwidthPrice.Mul64(modules.SectorSize)
	sectorBandwidthPrice := sectorUploadBandwidthPrice.Add(sectorDownloadBandwidthPrice)
	sectorPrice := sectorStoragePrice.Add(sectorBandwidthPrice)
	percentRemaining, _ := big.NewRat(0, 1).SetFrac(contract.RenterFunds.Big(), contract.TotalCost.Big()).Float64()
	if contract.RenterFunds.Cmp(sectorPrice.Mul64(3)) < 0 || percentRemaining < MinContractFundUploadThreshold {
		if u.GoodForUpload {
			log.Printf("Marking contract as not good for upload because of insufficient funds: %v vs. %v - %v", contract.RenterFunds.Cmp(sectorPrice.Mul64(3)) < 0, percentRemaining, contract.ID)
		}
		if !u.GoodForRenew {
			log.Println("Marking contract as being good for renew:", contract.ID)
		}
		u.GoodForUpload = false
		u.GoodForRenew = true
		return u, true
	}
	return u, false
}

// upForRenewalCheck checks if this contract is up for renewal.
// Returns true if a check fails and the utility returned must be used to update
// the contract state.
func upForRenewalCheck(contract skymodules.RenterContract, renewWindow, blockHeight types.BlockHeight, log *persist.Logger) (skymodules.ContractUtility, bool) {
	u := contract.Utility
	// Contract should not be used for uploading if it's halfway through the
	// renew window. That way we don't lose all upload contracts as soon as
	// we hit the renew window and give them some time to be renewed while
	// still uploading. If uploading blocks renews for half a window,
	// uploading will be prevented and the contract will have the remaining
	// window to update.
	if blockHeight+renewWindow/2 >= contract.EndHeight {
		if u.GoodForUpload {
			log.Println("Marking contract as not good for upload because it is time to renew the contract", contract.ID)
		}
		if !u.GoodForRenew {
			log.Println("Marking contract as being good for renew:", contract.ID)
		}
		u.GoodForUpload = false
		u.GoodForRenew = true
		return u, true
	}
	return u, false
}
