package renter

import (
	"time"

	"gitlab.com/SkynetLabs/skyd/skymodules"
)

// callStatus returns the status of the worker.
func (w *worker) callStatus() skymodules.WorkerStatus {
	downloadQueue := w.staticJobLowPrioReadQueue
	downloadQueue.mu.Lock()
	downloadOnCoolDown := downloadQueue.onCooldown()
	downloadTerminated := downloadQueue.killed
	downloadQueueSize := downloadQueue.jobs.Len()
	downloadCoolDownTime := downloadQueue.cooldownUntil.Sub(time.Now())

	var downloadCoolDownErr string
	if downloadQueue.recentErr != nil {
		downloadCoolDownErr = downloadQueue.recentErr.Error()
	}
	downloadQueue.mu.Unlock()

	w.mu.Lock()
	defer w.mu.Unlock()

	uploadOnCoolDown, uploadCoolDownTime := w.onUploadCooldown()
	var uploadCoolDownErr string
	if w.uploadRecentFailureErr != nil {
		uploadCoolDownErr = w.uploadRecentFailureErr.Error()
	}

	maintenanceOnCooldown, maintenanceCoolDownTime, maintenanceCoolDownErr := w.staticMaintenanceState.managedMaintenanceCooldownStatus()
	var mcdErr string
	if maintenanceCoolDownErr != nil {
		mcdErr = maintenanceCoolDownErr.Error()
	}

	// Update the worker cache before returning a status.
	w.staticTryUpdateCache()
	cache := w.staticCache()
	return skymodules.WorkerStatus{
		// Contract Information
		ContractID:      cache.staticContractID,
		ContractUtility: cache.staticContractUtility,
		HostPubKey:      w.staticHostPubKey,

		// Download information
		DownloadCoolDownError: downloadCoolDownErr,
		DownloadCoolDownTime:  downloadCoolDownTime,
		DownloadOnCoolDown:    downloadOnCoolDown,
		DownloadQueueSize:     downloadQueueSize,
		DownloadTerminated:    downloadTerminated,

		// Upload information
		UploadCoolDownError: uploadCoolDownErr,
		UploadCoolDownTime:  uploadCoolDownTime,
		UploadOnCoolDown:    uploadOnCoolDown,
		UploadQueueSize:     w.unprocessedChunks.Len(),
		UploadTerminated:    w.uploadTerminated,

		// Job Queues
		DownloadSnapshotJobQueueSize: int(w.staticJobDownloadSnapshotQueue.callStatus().size),
		UploadSnapshotJobQueueSize:   int(w.staticJobUploadSnapshotQueue.callStatus().size),

		// Maintenance Cooldown Information
		MaintenanceOnCooldown:    maintenanceOnCooldown,
		MaintenanceCoolDownError: mcdErr,
		MaintenanceCoolDownTime:  maintenanceCoolDownTime,

		// Account Information
		AccountBalanceTarget: w.staticBalanceTarget,
		AccountStatus:        w.staticAccount.managedStatus(),

		// Price Table Information
		PriceTableStatus: w.staticPriceTableStatus(),

		// Read Job Information
		ReadJobsStatus: w.callReadJobStatus(),

		// HasSector Job Information
		HasSectorJobsStatus: w.callHasSectorJobStatus(),

		// ReadRegistry Job Information
		ReadRegistryJobsStatus: w.callReadRegistryJobsStatus(),

		// UpdateRegistry Job Information
		UpdateRegistryJobsStatus: w.callUpdateRegistryJobsStatus(),
	}
}

// staticPriceTableStatus returns the status of the worker's price table
func (w *worker) staticPriceTableStatus() skymodules.WorkerPriceTableStatus {
	pt := w.staticPriceTable()

	var recentErrStr string
	if pt.staticRecentErr != nil {
		recentErrStr = pt.staticRecentErr.Error()
	}

	return skymodules.WorkerPriceTableStatus{
		ExpiryTime: pt.staticExpiryTime,
		UpdateTime: pt.staticUpdateTime,

		Active: time.Now().Before(pt.staticExpiryTime),

		RecentErr:     recentErrStr,
		RecentErrTime: pt.staticRecentErrTime,
	}
}

// callReadJobStatus returns the status of the read job queue
func (w *worker) callReadJobStatus() skymodules.WorkerReadJobsStatus {
	jrq := w.staticJobReadQueue
	status := jrq.callStatus()

	var recentErrString string
	if status.recentErr != nil {
		recentErrString = status.recentErr.Error()
	}

	toMS := func(t float64) uint64 {
		if d := time.Duration(t); d > 0 {
			return uint64(d.Milliseconds())
		}
		return 0
	}
	avgJobTimeInMs := func(l uint64) uint64 {
		// TODO: Same here. This was the EMA and is now Max.
		if d := jrq.callExpectedJobTime(l); d.Max() > 0 {
			return uint64(d.Max().Milliseconds())
		}
		return 0
	}

	return skymodules.WorkerReadJobsStatus{
		AvgJobTime64k: avgJobTimeInMs(size64K),
		AvgJobTime1m:  avgJobTimeInMs(size1M),
		AvgJobTime4m:  avgJobTimeInMs(size4M),

		AvgEarlyDelta64k: toMS(jrq.weightedEarlyJobs64kDelta),
		AvgEarlyDelta1m:  toMS(jrq.weightedEarlyJobs1mDelta),
		AvgEarlyDelta4m:  toMS(jrq.weightedEarlyJobs4mDelta),

		AvgLateDelta64k: toMS(jrq.weightedLateJobs64kDelta),
		AvgLateDelta1m:  toMS(jrq.weightedLateJobs1mDelta),
		AvgLateDelta4m:  toMS(jrq.weightedLateJobs4mDelta),

		NumEarlyJobs64k: jrq.nEarlyJobs64k,
		NumEarlyJobs1m:  jrq.nEarlyJobs1m,
		NumEarlyJobs4m:  jrq.nEarlyJobs4m,

		NumLateJobs64k: jrq.nLateJobs64k,
		NumLateJobs1m:  jrq.nLateJobs1m,
		NumLateJobs4m:  jrq.nLateJobs4m,

		Variance64k: toMS(jrq.weightedJobs64kSquaredDelta) / (jrq.nEarlyJobs64k + jrq.nLateJobs64k - 1),
		Variance1m:  toMS(jrq.weightedJobs1mSquaredDelta) / (jrq.nEarlyJobs1m + jrq.nLateJobs1m - 1),
		Variance4m:  toMS(jrq.weightedJobs4mSquaredDelta) / (jrq.nEarlyJobs4m + jrq.nLateJobs4m - 1),

		ConsecutiveFailures: status.consecutiveFailures,
		JobQueueSize:        status.size,
		RecentErr:           recentErrString,
		RecentErrTime:       status.recentErrTime,
	}
}

// callHasSectorJobStatus returns the status of the has sector job queue
func (w *worker) callHasSectorJobStatus() skymodules.WorkerHasSectorJobsStatus {
	hsq := w.staticJobHasSectorQueue
	status := hsq.callStatus()

	var recentErrStr string
	if status.recentErr != nil {
		recentErrStr = status.recentErr.Error()
	}

	// TODO: what should we return here now that it's no longer the EMA?
	// Is the max fine? That's the p999.
	avgJobTimeInMs := uint64(hsq.callExpectedJobTime().Max().Milliseconds())

	return skymodules.WorkerHasSectorJobsStatus{
		AvgJobTime:          avgJobTimeInMs,
		ConsecutiveFailures: status.consecutiveFailures,
		JobQueueSize:        status.size,
		RecentErr:           recentErrStr,
		RecentErrTime:       status.recentErrTime,
	}
}

// callGenericWorkerJobStatus returns the status for the generic job queue.
func callGenericWorkerJobStatus(queue *jobGenericQueue) skymodules.WorkerGenericJobsStatus {
	status := queue.callStatus()

	var recentErrStr string
	if status.recentErr != nil {
		recentErrStr = status.recentErr.Error()
	}

	return skymodules.WorkerGenericJobsStatus{
		ConsecutiveFailures: status.consecutiveFailures,
		JobQueueSize:        status.size,
		OnCooldown:          time.Now().Before(status.cooldownUntil),
		OnCooldownUntil:     status.cooldownUntil,
		RecentErr:           recentErrStr,
		RecentErrTime:       status.recentErrTime,
	}
}

// callUpdateRegistryJobsStatus returns the status for the ReadRegistry queue.
func (w *worker) callReadRegistryJobsStatus() skymodules.WorkerReadRegistryJobStatus {
	return skymodules.WorkerReadRegistryJobStatus{
		WorkerGenericJobsStatus: callGenericWorkerJobStatus(w.staticJobReadRegistryQueue.jobGenericQueue),
	}
}

// callUpdateRegistryJobsStatus returns the status for the UpdateRegistry queue.
func (w *worker) callUpdateRegistryJobsStatus() skymodules.WorkerUpdateRegistryJobStatus {
	return skymodules.WorkerUpdateRegistryJobStatus{
		WorkerGenericJobsStatus: callGenericWorkerJobStatus(w.staticJobUpdateRegistryQueue.jobGenericQueue),
	}
}
