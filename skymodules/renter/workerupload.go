package renter

import (
	"fmt"
	"strings"
	"time"

	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem/siafile"

	"gitlab.com/NebulousLabs/errors"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

const (
	// uploadGougingFractionDenom sets the gouging fraction to 1/4 based on the
	// idea that the user should be able to hit at least some fraction of their
	// desired upload volume using some fraction of hosts.
	uploadGougingFractionDenom = 4
)

// checkUploadGouging looks at the current renter allowance and the active
// settings for a host and determines whether an upload should be halted due to
// price gouging.
//
// NOTE: Currently this function treats all uploads as being the stream upload
// size and assumes that data is actually being appended to the host. As the
// worker gains more modification actions on the host, this check can be split
// into different checks that vary based on the operation being performed.
func checkUploadGouging(allowance skymodules.Allowance, hostSettings modules.HostExternalSettings) error {
	// Check whether the base RPC price is too high.
	if !allowance.MaxRPCPrice.IsZero() && allowance.MaxRPCPrice.Cmp(hostSettings.BaseRPCPrice) < 0 {
		errStr := fmt.Sprintf("rpc price of host is %v, which is above the maximum allowed by the allowance: %v", hostSettings.BaseRPCPrice, allowance.MaxRPCPrice)
		return errors.New(errStr)
	}
	// Check whether the sector access price is too high.
	if !allowance.MaxSectorAccessPrice.IsZero() && allowance.MaxSectorAccessPrice.Cmp(hostSettings.SectorAccessPrice) < 0 {
		errStr := fmt.Sprintf("sector access price of host is %v, which is above the maximum allowed by the allowance: %v", hostSettings.SectorAccessPrice, allowance.MaxSectorAccessPrice)
		return errors.New(errStr)
	}
	// Check whether the storage price is too high.
	if !allowance.MaxStoragePrice.IsZero() && allowance.MaxStoragePrice.Cmp(hostSettings.StoragePrice) < 0 {
		errStr := fmt.Sprintf("storage price of host is %v, which is above the maximum allowed by the allowance: %v", hostSettings.StoragePrice, allowance.MaxStoragePrice)
		return errors.New(errStr)
	}
	// Check whether the upload bandwidth price is too high.
	if !allowance.MaxUploadBandwidthPrice.IsZero() && allowance.MaxUploadBandwidthPrice.Cmp(hostSettings.UploadBandwidthPrice) < 0 {
		errStr := fmt.Sprintf("upload bandwidth price of host is %v, which is above the maximum allowed by the allowance: %v", hostSettings.UploadBandwidthPrice, allowance.MaxUploadBandwidthPrice)
		return errors.New(errStr)
	}
	// Check whether the download bandwidth price is too high.
	if !allowance.MaxDownloadBandwidthPrice.IsZero() && allowance.MaxDownloadBandwidthPrice.Cmp(hostSettings.DownloadBandwidthPrice) < 0 {
		errStr := fmt.Sprintf("download bandwidth price of host is %v, which is above the maximum allowed by the allowance: %v", hostSettings.UploadBandwidthPrice, allowance.MaxDownloadBandwidthPrice)
		return errors.New(errStr)
	}

	// If there is no allowance, general price gouging checks have to be
	// disabled, because there is no baseline for understanding what might count
	// as price gouging.
	if allowance.Funds.IsZero() {
		return nil
	}

	// Check that the combined prices make sense in the context of the overall
	// allowance. The general idea is to compute the total cost of performing
	// the same action repeatedly until a fraction of the desired total resource
	// consumption established by the allowance has been reached. The fraction
	// is determined on a case-by-case basis. If the host is too expensive to
	// even satisfy a fraction of the user's total desired resource consumption,
	// the action will be blocked for price gouging.
	singleUploadCost := hostSettings.SectorAccessPrice.Add(hostSettings.BaseRPCPrice).Add(hostSettings.UploadBandwidthPrice.Mul64(skymodules.StreamUploadSize)).Add(hostSettings.StoragePrice.Mul64(uint64(allowance.Period)).Mul64(skymodules.StreamUploadSize))
	fullCostPerByte := singleUploadCost.Div64(skymodules.StreamUploadSize)
	allowanceStorageCost := fullCostPerByte.Mul64(allowance.ExpectedStorage)
	reducedCost := allowanceStorageCost.Div64(uploadGougingFractionDenom)
	if reducedCost.Cmp(allowance.Funds) > 0 {
		errStr := fmt.Sprintf("combined upload pricing of host yields %v, which is more than the renter is willing to pay for storage: %v - price gouging protection enabled", reducedCost, allowance.Funds)
		return errors.New(errStr)
	}

	return nil
}

// checkUploadGouging looks at the current renter allowance and the active price
// table for a host and determines whether an upload should be halted due to
// price gouging.
func checkUploadGougingPT(pt modules.RPCPriceTable, allowance skymodules.Allowance) error {
	// Check MemoryTimeCost. This is unused by hosts right now and should be set to 1H.
	if types.NewCurrency64(1).Cmp(pt.MemoryTimeCost) < 0 {
		errStr := fmt.Sprintf("MemoryTimeCost of host is %v, which is above the maximum allowed by the allowance: %v", pt.MemoryTimeCost, types.NewCurrency64(1))
		return errors.New(errStr)
	}
	// Check WriteLengthCost. This is unused by hosts right now and should be set to 1H.
	if types.NewCurrency64(1).Cmp(pt.WriteLengthCost) < 0 {
		errStr := fmt.Sprintf("WriteLengthCost of host is %v, which is above the maximum allowed by the allowance: %v", pt.WriteLengthCost, types.NewCurrency64(1))
		return errors.New(errStr)
	}
	// Check InitBaseCost. This equals the BaseRPCPrice in the host settings.
	if !allowance.MaxRPCPrice.IsZero() && allowance.MaxRPCPrice.Cmp(pt.InitBaseCost) < 0 {
		errStr := fmt.Sprintf("InitBaseCost of host is %v, which is above the maximum allowed by the allowance: %v", pt.InitBaseCost, allowance.MaxRPCPrice)
		return errors.New(errStr)
	}
	// Check WriteBaseCost. This equals the SectorAccessPrice in the host settings.
	if !allowance.MaxSectorAccessPrice.IsZero() && allowance.MaxSectorAccessPrice.Cmp(pt.WriteBaseCost) < 0 {
		errStr := fmt.Sprintf("WriteBaseCost of host is %v, which is above the maximum allowed by the allowance: %v", pt.WriteBaseCost, allowance.MaxSectorAccessPrice)
		return errors.New(errStr)
	}
	// Check WriteStoreCost. This equals the StoragePrice in the host settings.
	if !allowance.MaxStoragePrice.IsZero() && allowance.MaxStoragePrice.Cmp(pt.WriteStoreCost) < 0 {
		errStr := fmt.Sprintf("WriteStoreCost of host is %v, which is above the maximum allowed by the allowance: %v", pt.WriteStoreCost, allowance.MaxStoragePrice)
		return errors.New(errStr)
	}
	// Check whether the upload bandwidth price is too high.
	if !allowance.MaxUploadBandwidthPrice.IsZero() && allowance.MaxUploadBandwidthPrice.Cmp(pt.UploadBandwidthCost) < 0 {
		errStr := fmt.Sprintf("UploadBandwidthPrice price of host is %v, which is above the maximum allowed by the allowance: %v", pt.UploadBandwidthCost, allowance.MaxUploadBandwidthPrice)
		return errors.New(errStr)
	}
	// Check whether the download bandwidth price is too high.
	if !allowance.MaxDownloadBandwidthPrice.IsZero() && allowance.MaxDownloadBandwidthPrice.Cmp(pt.DownloadBandwidthCost) < 0 {
		errStr := fmt.Sprintf("DownloadBandwidthPrice price of host is %v, which is above the maximum allowed by the allowance: %v", pt.DownloadBandwidthCost, allowance.MaxDownloadBandwidthPrice)
		return errors.New(errStr)
	}

	// If there is no allowance, general price gouging checks have to be
	// disabled, because there is no baseline for understanding what might count
	// as price gouging.
	if allowance.Funds.IsZero() {
		return nil
	}

	// Cost of initializing the MDM.
	cost := modules.MDMInitCost(&pt, modules.SectorSize, 1)

	// Cost of executing a single sector append.
	memory := modules.MDMInitMemory() + modules.MDMAppendMemory()
	cost = cost.Add(modules.MDMMemoryCost(&pt, memory, modules.MDMTimeAppend))

	// Finalize the program.
	cost = cost.Add(modules.MDMMemoryCost(&pt, memory, modules.MDMTimeCommit))

	// Cost of storage and bandwidth.
	storageCost, _ := modules.MDMAppendCost(&pt, allowance.Period)
	bandwidthCost := modules.MDMBandwidthCost(pt, modules.SectorSize+2920, 2920) // assume sector data + 2 packets of overhead
	cost = cost.Add(storageCost).Add(bandwidthCost)

	// Cost of full upload.
	allowanceStorageCost := cost.Mul64(allowance.ExpectedStorage).Div64(modules.SectorSize)
	reducedCost := allowanceStorageCost.Div64(uploadGougingFractionDenom)
	if reducedCost.Cmp(allowance.Funds) > 0 {
		errStr := fmt.Sprintf("combined upload pricing of host yields %v, which is more than the renter is willing to pay for storage: %v - price gouging protection enabled", reducedCost, allowance.Funds)
		return errors.New(errStr)
	}
	return nil
}

// managedDropChunk will remove a worker from the responsibility of tracking a chunk.
//
// This function is managed instead of static because it is against convention
// to be calling functions on other objects (in this case, the renter) while
// holding a lock.
func (w *worker) managedDropChunk(uc *unfinishedUploadChunk) {
	uc.mu.Lock()
	uc.workersRemaining--
	uc.mu.Unlock()
	w.staticRenter.managedCleanUpUploadChunk(uc)
}

// managedDropUploadChunks will release all of the upload chunks that the worker
// has received.
func (w *worker) managedDropUploadChunks() {
	w.mu.Lock()
	chunksToDrop := w.unprocessedChunks
	w.unprocessedChunks = newUploadChunks()
	w.mu.Unlock()

	for chunkToDrop := chunksToDrop.Pop(); chunkToDrop != nil; chunkToDrop = chunksToDrop.Pop() {
		w.managedDropChunk(chunkToDrop)
		w.staticRenter.staticRepairLog.Debugf("dropping chunk %v of %s because the worker is dropping all chunks", chunkToDrop.staticIndex, chunkToDrop.staticSiaPath)
	}
}

// managedKillUploading will disable all uploading for the worker.
func (w *worker) managedKillUploading() {
	// Mark the worker as disabled so that incoming chunks are rejected.
	w.mu.Lock()
	w.uploadTerminated = true
	w.mu.Unlock()

	// After the worker is marked as disabled, clear out all of the chunks.
	w.managedDropUploadChunks()
}

// callQueueUploadChunk will take a chunk and add it to the worker's repair
// stack.
func (w *worker) callQueueUploadChunk(uc *unfinishedUploadChunk) bool {
	// Check that the worker is allowed to be uploading before grabbing the
	// worker lock.
	cache := w.staticCache()
	uc.mu.Lock()
	_, candidateHost := uc.unusedHosts[w.staticHostPubKeyStr]
	uc.mu.Unlock()
	goodForUpload := cache.staticContractUtility.GoodForUpload
	w.mu.Lock()
	onCooldown, _ := w.onUploadCooldown()
	uploadTerminated := w.uploadTerminated
	if !goodForUpload || uploadTerminated || onCooldown || !candidateHost {
		// The worker should not be uploading, remove the chunk.
		w.mu.Unlock()
		w.managedDropChunk(uc)
		return false
	}
	w.unprocessedChunks.PushBack(uc)
	w.mu.Unlock()

	// Send a signal informing the work thread that there is work.
	w.staticWake()
	return true
}

// managedHasUploadJob returns true if there is upload work available for the
// worker.
func (w *worker) managedHasUploadJob() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.unprocessedChunks.Len() > 0
}

// managedPerformUploadChunkJob will perform some upload work.
func (w *worker) managedPerformUploadChunkJob() {
	// Fetch any available chunk for uploading. If no chunk is found, return
	// false.
	w.mu.Lock()
	nextChunk := w.unprocessedChunks.Pop()
	w.mu.Unlock()
	if nextChunk == nil {
		return
	}

	// Make sure the chunk wasn't canceled.
	nextChunk.cancelMU.Lock()
	if nextChunk.canceled {
		nextChunk.cancelMU.Unlock()
		// If the chunk was canceled then we drop the chunk. This will decrement the
		// chunk's remainingWorkers and perform any clean up work necessary
		w.managedDropChunk(nextChunk)
		return
	}
	// Add this worker to the chunk's cancelWG for the duration of this method.
	nextChunk.cancelWG.Add(1)
	defer nextChunk.cancelWG.Done()
	nextChunk.cancelMU.Unlock()

	// Check if this particular chunk is necessary. If not, return 'true'
	// because there may be more chunks in the queue.
	uc, pieceIndex := w.managedProcessUploadChunk(nextChunk)
	if uc == nil {
		return
	}
	// Open an editing connection to the host.
	s, err := w.staticRenter.staticHostContractor.Session(w.staticHostPubKey, w.staticTG.StopChan())
	if err != nil {
		failureErr := fmt.Errorf("Worker failed to acquire an editor: %v", err)
		w.managedUploadFailed(uc, pieceIndex, failureErr)
		return
	}
	defer func() {
		if err := s.Close(); err != nil {
			w.staticRenter.staticLog.Print("managedPerformUploadChunkJob: failed to close editor", err)
		}
	}()

	// Before performing the upload, check for price gouging.
	allowance := w.staticRenter.staticHostContractor.Allowance()
	hostSettings := s.HostSettings()
	err = checkUploadGouging(allowance, hostSettings)
	if err != nil && !w.staticRenter.staticDeps.Disrupt("DisableUploadGouging") {
		failureErr := errors.AddContext(err, "worker uploader is not being used because price gouging was detected")
		w.managedUploadFailed(uc, pieceIndex, failureErr)
		return
	}

	// Perform the upload, and update the failure stats based on the success of
	// the upload attempt.
	//
	// Ignore the error if it's a ErrMaxVirtualSectors coming from a pre-1.5.5
	// host.
	root, err := s.Upload(uc.physicalChunkData[pieceIndex])
	ignoreErr := build.VersionCmp(hostSettings.Version, "1.5.5") < 0 && err != nil && strings.Contains(err.Error(), modules.ErrMaxVirtualSectors.Error())
	if err != nil && !ignoreErr {
		failureErr := fmt.Errorf("Worker failed to upload root %v via the editor: %v", root, err)
		w.managedUploadFailed(uc, pieceIndex, failureErr)
		return
	}
	w.mu.Lock()
	w.uploadConsecutiveFailures = 0
	w.mu.Unlock()

	// Add piece to renterFile
	err = uc.fileEntry.AddPiece(w.staticHostPubKey, uc.staticIndex, pieceIndex, root)
	if err != nil {
		failureErr := fmt.Errorf("Worker failed to add new piece to SiaFile: %v", err)
		w.managedUploadFailed(uc, pieceIndex, failureErr)
		return
	}
	id := w.staticRenter.mu.Lock()
	w.staticRenter.mu.Unlock(id)

	// Upload is complete. Update the state of the chunk and the renter's memory
	// available to reflect the completed upload.
	uc.mu.Lock()
	releaseSize := len(uc.physicalChunkData[pieceIndex])
	uc.piecesRegistered--
	uc.piecesCompleted++
	uc.physicalChunkData[pieceIndex] = nil
	uc.memoryReleased += uint64(releaseSize)
	uc.chunkSuccessProcessTimes = append(uc.chunkSuccessProcessTimes, time.Now())
	uc.mu.Unlock()
	uc.staticMemoryManager.Return(uint64(releaseSize))
	w.staticRenter.managedCleanUpUploadChunk(uc)
}

// onUploadCooldown returns true if the worker is on cooldown from failed
// uploads and the amount of cooldown time remaining for the worker.
func (w *worker) onUploadCooldown() (bool, time.Duration) {
	requiredCooldown := uploadFailureCooldown
	for i := 0; i < w.uploadConsecutiveFailures && i < maxConsecutivePenalty; i++ {
		requiredCooldown *= 2
	}
	return time.Now().Before(w.uploadRecentFailure.Add(requiredCooldown)), w.uploadRecentFailure.Add(requiredCooldown).Sub(time.Now())
}

// managedProcessUploadChunk will process a chunk from the worker chunk queue.
func (w *worker) managedProcessUploadChunk(uc *unfinishedUploadChunk) (nextChunk *unfinishedUploadChunk, pieceIndex uint64) {
	// Determine the usability value of this worker.
	cache := w.staticCache()
	w.mu.Lock()
	onCooldown, _ := w.onUploadCooldown()
	w.mu.Unlock()
	goodForUpload := cache.staticContractUtility.GoodForUpload

	// Determine what sort of help this chunk needs.
	uc.mu.Lock()
	_, candidateHost := uc.unusedHosts[w.staticHostPubKey.String()]
	chunkComplete := uc.staticPiecesNeeded <= uc.piecesCompleted
	// If the chunk does not need help from this worker, release the chunk.
	if chunkComplete || !candidateHost || !goodForUpload || onCooldown {
		// This worker no longer needs to track this chunk.
		uc.mu.Unlock()
		w.managedDropChunk(uc)

		// Extra check - if a worker is unusable, drop all the queued chunks.
		if onCooldown || !goodForUpload {
			w.managedDropUploadChunks()
		}
		return nil, 0
	}

	// If the worker does not need help, add the worker to the set of standby
	// chunks.
	needsHelp := uc.staticPiecesNeeded > uc.piecesCompleted+uc.piecesRegistered
	if !needsHelp {
		uc.workersStandby = append(uc.workersStandby, w)
		uc.mu.Unlock()
		w.staticRenter.managedCleanUpUploadChunk(uc)
		return nil, 0
	}

	// If the chunk needs help from this worker, find a piece to upload and
	// return the stats for that piece.
	//
	// Select a piece and mark that a piece has been selected.
	index := -1
	for i := 0; i < len(uc.pieceUsage); i++ {
		if !uc.pieceUsage[i] {
			index = i
			uc.pieceUsage[i] = true
			break
		}
	}
	if index == -1 {
		build.Critical("worker was supposed to upload but couldn't find unused piece:", len(uc.pieceUsage))
		uc.mu.Unlock()
		w.managedDropChunk(uc)
		return nil, 0
	}
	delete(uc.unusedHosts, w.staticHostPubKey.String())
	uc.piecesRegistered++
	uc.workersRemaining--
	uc.mu.Unlock()
	return uc, uint64(index)
}

// managedUploadFailed is called if a worker failed to upload part of an unfinished
// chunk.
func (w *worker) managedUploadFailed(uc *unfinishedUploadChunk, pieceIndex uint64, failureErr error) {
	w.staticRenter.staticRepairLog.Printf("Worker upload failed. Worker: %v, Chunk: %v of %s, Error: %v", w.staticHostPubKey, uc.staticIndex, uc.staticSiaPath, failureErr)
	// Mark the failure in the worker if the gateway says we are online. It's
	// not the worker's fault if we are offline.
	if w.staticRenter.staticGateway.Online() && !(strings.Contains(failureErr.Error(), siafile.ErrDeleted.Error()) || errors.Contains(failureErr, siafile.ErrDeleted)) {
		w.mu.Lock()
		w.uploadRecentFailure = time.Now()
		w.uploadRecentFailureErr = failureErr
		w.uploadConsecutiveFailures++
		w.mu.Unlock()
	}

	// Unregister the piece from the chunk and hunt for a replacement.
	uc.mu.Lock()
	uc.piecesRegistered--
	uc.pieceUsage[pieceIndex] = false
	uc.chunkFailedProcessTimes = append(uc.chunkFailedProcessTimes, time.Now())
	uc.mu.Unlock()

	// Notify the standby workers of the chunk
	uc.managedNotifyStandbyWorkers()
	w.staticRenter.managedCleanUpUploadChunk(uc)

	// Because the worker is now on cooldown, drop all remaining chunks.
	w.managedDropUploadChunks()
}
