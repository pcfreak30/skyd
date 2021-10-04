package contractor

import (
	"math"
	"math/big"

	"gitlab.com/SkynetLabs/skyd/skymodules"
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

// Merge merges two update statuses into one. Whichever has the higher priority
// wins.
func (us utilityUpdateStatus) Merge(us2 utilityUpdateStatus) utilityUpdateStatus {
	if us > us2 {
		return us
	}
	return us2
}

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
		return u, necessaryUtilityUpdate
	}
	return u, noUpdate
}

// managedUtilityChecks performs checks on a contract that
// might require marking the contract as !GFR and/or !GFU.
// Returns the new utility and corresponding update status.
func (c *Contractor) managedUtilityChecks(contract skymodules.RenterContract, host skymodules.HostDBEntry, sb skymodules.HostScoreBreakdown, minScoreGFU, minScoreGFR types.Currency) (newUtility skymodules.ContractUtility, uus utilityUpdateStatus) {
	revision := contract.Transaction.FileContractRevisions[0]
	c.mu.RLock()
	allowance := c.allowance
	blockHeight := c.blockHeight
	renewWindow := c.allowance.RenewWindow
	period := c.allowance.Period
	_, renewed := c.renewedTo[contract.ID]
	c.mu.RUnlock()

	// Init uus to no update and the utility with the contract's utility.
	// We assume that the contract is good when we start.
	uus = noUpdate
	newUtility = contract.Utility
	newUtility.GoodForRenew = true
	newUtility.GoodForUpload = true

	// A contract with a dead score should not be used for anything.
	u, needsUpdate := deadScoreCheck(newUtility, sb.Score)
	uus = uus.Merge(needsUpdate)
	newUtility = newUtility.Merge(u)

	// A contract that has been renewed should be set to !GFU and !GFR.
	u, needsUpdate = renewedCheck(contract.Utility, renewed)
	uus = uus.Merge(needsUpdate)
	newUtility = newUtility.Merge(u)

	u, needsUpdate = maxRevisionCheck(contract.Utility, revision.NewRevisionNumber)
	uus = uus.Merge(needsUpdate)
	newUtility = newUtility.Merge(u)

	u, needsUpdate = badContractCheck(contract.Utility)
	uus = uus.Merge(needsUpdate)
	newUtility = newUtility.Merge(u)

	u, needsUpdate = offlineCheck(contract, host, c.staticLog)
	uus = uus.Merge(needsUpdate)
	newUtility = newUtility.Merge(u)

	u, needsUpdate = upForRenewalCheck(contract, renewWindow, blockHeight, c.staticLog)
	uus = uus.Merge(needsUpdate)
	newUtility = newUtility.Merge(u)

	u, needsUpdate = sufficientFundsCheck(contract, host, period, c.staticLog)
	uus = uus.Merge(needsUpdate)
	newUtility = newUtility.Merge(u)

	u, needsUpdate = outOfStorageCheck(contract, blockHeight, c.staticLog)
	uus = uus.Merge(needsUpdate)
	newUtility = newUtility.Merge(u)

	u, needsUpdate = storageGougingCheck(contract, allowance, host, revision.NewFileSize)
	uus = uus.Merge(needsUpdate)
	newUtility = newUtility.Merge(u)

	u, needsUpdate = c.managedCheckHostScore(contract, sb, minScoreGFR, minScoreGFU)
	uus = uus.Merge(needsUpdate)
	newUtility = newUtility.Merge(u)

	return newUtility, uus
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
func badContractCheck(u skymodules.ContractUtility) (skymodules.ContractUtility, utilityUpdateStatus) {
	if u.BadContract {
		u.GoodForUpload = false
		u.GoodForRenew = false
		return u, necessaryUtilityUpdate
	}
	return u, noUpdate
}

// maxRevisionCheck will return a locked utility if the contract has reached its
// max revision.
func maxRevisionCheck(u skymodules.ContractUtility, revisionNumber uint64) (skymodules.ContractUtility, utilityUpdateStatus) {
	if revisionNumber == math.MaxUint64 {
		u.GoodForUpload = false
		u.GoodForRenew = false
		u.Locked = true
		return u, necessaryUtilityUpdate
	}
	return u, noUpdate
}

// offLineCheck checks if the host for this contract is offline.
// Returns true if a check fails and the utility returned must be used to update
// the contract state.
func offlineCheck(contract skymodules.RenterContract, host skymodules.HostDBEntry, log *persist.Logger) (skymodules.ContractUtility, utilityUpdateStatus) {
	u := contract.Utility
	// Contract has no utility if the host is offline.
	if isOffline(host) {
		// Log if the utility has changed.
		if u.GoodForUpload || u.GoodForRenew {
			log.Println("Marking contract as having no utility because of host being offline", contract.ID)
		}
		u.GoodForUpload = false
		u.GoodForRenew = false
		return u, necessaryUtilityUpdate
	}
	return u, noUpdate
}

// outOfStorageCheck checks if the host is running out of storage.
// Returns true if a check fails and the utility returned must be used to update
// the contract state.
func outOfStorageCheck(contract skymodules.RenterContract, blockHeight types.BlockHeight, log *persist.Logger) (skymodules.ContractUtility, utilityUpdateStatus) {
	u := contract.Utility
	// If LastOOSErr has never been set, return false.
	if u.LastOOSErr == 0 {
		return u, noUpdate
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
		return u, necessaryUtilityUpdate
	}
	return u, noUpdate
}

// deadScoreCheck will return a contract with no utility and a required update
// if the contract has a score <= 1.
func deadScoreCheck(u skymodules.ContractUtility, score types.Currency) (skymodules.ContractUtility, utilityUpdateStatus) {
	if score.Cmp(types.NewCurrency64(1)) <= 0 {
		u.GoodForUpload = false
		u.GoodForRenew = false
		return u, necessaryUtilityUpdate
	}
	return u, noUpdate
}

// renewedCheck will return a contract with no utility and a required update if
// the contract has been renewed, no changes otherwise.
func renewedCheck(u skymodules.ContractUtility, renewed bool) (skymodules.ContractUtility, utilityUpdateStatus) {
	if renewed {
		u.GoodForUpload = false
		u.GoodForRenew = false
		return u, necessaryUtilityUpdate
	}
	return u, noUpdate
}

// storageGougingCheck makes sure the host's storage price isn't too expensive.
func storageGougingCheck(contract skymodules.RenterContract, allowance skymodules.Allowance, host skymodules.HostDBEntry, contractSize uint64) (skymodules.ContractUtility, utilityUpdateStatus) {
	u := contract.Utility

	if !allowance.MaxStoragePrice.IsZero() && host.StoragePrice.Cmp(allowance.MaxStoragePrice) > 0 {
		// If the contract contains data don't renew to make sure we
		// don't pay for its storage.
		u.GoodForUpload = false
		if contractSize > 0 {
			u.GoodForRenew = false
		}
		return u, necessaryUtilityUpdate
	}
	return u, noUpdate
}

// sufficientFundsCheck checks if there are enough funds left in the contract
// for uploads.
// Returns true if a check fails and the utility returned must be used to update
// the contract state.
func sufficientFundsCheck(contract skymodules.RenterContract, host skymodules.HostDBEntry, period types.BlockHeight, log *persist.Logger) (skymodules.ContractUtility, utilityUpdateStatus) {
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
		return u, necessaryUtilityUpdate
	}
	return u, noUpdate
}

// upForRenewalCheck checks if this contract is up for renewal.
// Returns true if a check fails and the utility returned must be used to update
// the contract state.
func upForRenewalCheck(contract skymodules.RenterContract, renewWindow, blockHeight types.BlockHeight, log *persist.Logger) (skymodules.ContractUtility, utilityUpdateStatus) {
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
		return u, necessaryUtilityUpdate
	}
	return u, noUpdate
}
