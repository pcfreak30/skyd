package renter

import (
	"bytes"
	"context"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

const (
	// snapshotDownloadGougingFractionDenom sets the fraction to 1/100 because
	// downloading snapshots is important, so there is less sensitivity to
	// gouging. Also, this is a rare operation.
	snapshotDownloadGougingFractionDenom = 100
)

type (
	// jobDownloadSnapshot is a job for the worker to download a snapshot from
	// its respective host.
	jobDownloadSnapshot struct {
		staticResponseChan chan *jobDownloadSnapshotResponse

		*jobGeneric
	}

	// jobDownloadSnapshotQueue contains the download jobs.
	jobDownloadSnapshotQueue struct {
		*jobGenericQueue
	}

	// jobDownloadSnapshotResponse contains the response to an upload snapshot
	// job.
	jobDownloadSnapshotResponse struct {
		staticSnapshots []snapshotEntry
		staticErr       error
	}
)

// callDiscard will discard this job, sending an error down the response
// channel.
func (j *jobDownloadSnapshot) callDiscard(err error) {
	resp := &jobDownloadSnapshotResponse{
		staticErr: errors.Extend(err, ErrJobDiscarded),
	}
	w := j.staticQueue.staticWorker()
	w.renter.tg.Launch(func() {
		select {
		case j.staticResponseChan <- resp:
		case <-j.staticCtx.Done():
		case <-w.renter.tg.StopChan():
		}
	})
}

// callExecute will perform an upload snapshot job for the worker.
func (j *jobDownloadSnapshot) callExecute() {
	w := j.staticQueue.staticWorker()

	// Defer a function to send the result down a channel.
	var snapshots []snapshotEntry
	var err error
	defer func() {
		resp := &jobDownloadSnapshotResponse{
			staticErr:       err,
			staticSnapshots: snapshots,
		}
		w.renter.tg.Launch(func() {
			select {
			case j.staticResponseChan <- resp:
			case <-j.staticCtx.Done():
			case <-w.renter.tg.StopChan():
			}
		})

		// Report a failure to the queue if this job had an error.
		if resp.staticErr != nil {
			j.staticQueue.callReportFailure(resp.staticErr)
		} else {
			j.staticQueue.callReportSuccess()
		}
	}()

	// Check for gouging
	gc := w.staticCache().staticGougingChecks
	if gc.DownloadSnapshot.IsGouging() {
		err = errors.AddContext(gc.DownloadSnapshot, "price gouging check failed for download snapshot job")
		return
	}

	// Perform the actual download
	snapshots, err = w.renter.managedDownloadSnapshotTable(w)
	if err != nil && errors.Contains(err, errEmptyContract) {
		err = nil
	}

	return
}

// callExpectedBandwidth returns the amount of bandwidth this job is expected to
// consume.
func (j *jobDownloadSnapshot) callExpectedBandwidth() (ul, dl uint64) {
	// Estimate 50kb in overhead for upload and download, and then 4 MiB
	// necessary to send the actual full sector payload.
	return 50e3 + 1<<22, 50e3
}

// initJobUploadSnapshotQueue will initialize the upload snapshot job queue for
// the worker.
func (w *worker) initJobDownloadSnapshotQueue() {
	if w.staticJobDownloadSnapshotQueue != nil {
		w.renter.log.Critical("should not be double initializng the upload snapshot queue")
		return
	}

	w.staticJobDownloadSnapshotQueue = &jobDownloadSnapshotQueue{
		jobGenericQueue: newJobGenericQueue(w),
	}
}

// FetchBackups is a convenience method to runs a DownloadSnapshot job on a
// worker and formats the response to a list of UploadedBackup objects.
func (w *worker) FetchBackups(ctx context.Context) ([]modules.UploadedBackup, error) {
	downloadSnapshotRespChan := make(chan *jobDownloadSnapshotResponse)
	jus := &jobDownloadSnapshot{
		staticResponseChan: downloadSnapshotRespChan,

		jobGeneric: newJobGeneric(ctx, w.staticJobDownloadSnapshotQueue),
	}

	// Add the job to the queue.
	if !w.staticJobDownloadSnapshotQueue.callAdd(jus) {
		return nil, errors.New("worker unavailable")
	}

	// Wait for the response.
	var resp *jobDownloadSnapshotResponse
	select {
	case <-ctx.Done():
		return nil, errors.New("FetchBackups interrupted")
	case resp = <-downloadSnapshotRespChan:
	}

	if resp.staticErr != nil {
		return nil, errors.AddContext(resp.staticErr, "FetchBackups failed")
	}

	// Format the response and return the response to the requester.
	uploadedBackups := make([]modules.UploadedBackup, len(resp.staticSnapshots))
	for i, e := range resp.staticSnapshots {
		uploadedBackups[i] = modules.UploadedBackup{
			Name:           string(bytes.TrimRight(e.Name[:], types.RuneToString(0))),
			UID:            e.UID,
			CreationDate:   e.CreationDate,
			Size:           e.Size,
			UploadProgress: 100,
		}
	}
	return uploadedBackups, nil
}
