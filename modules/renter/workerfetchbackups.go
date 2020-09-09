package renter

// workerfetchbackups.go contains all of the code related to using the worker to
// fetch the list of snapshot backups available on a particular host.

// TODO: Currently the backups are fetched using a separate session, when the
// worker code is switched over to having a common session we should start using
// that common session. Implementation in managedPerformFetchBackupsJob.
//
// TODO: The conversion from the []snapshotEntry to the []modules.UploadedBackup
// is a conversion that should probably happen in the snapshot subsystem, or at
// least use a helper method from the snapshot subsystem.

import (
	"fmt"
	"sync"

	"gitlab.com/NebulousLabs/errors"

	"gitlab.com/NebulousLabs/Sia/modules"
)

// fetchBackupsJobQueue is the primary structure for managing fetch backup jobs
// from the worker.
type fetchBackupsJobQueue struct {
	queue []chan fetchBackupsJobResult
	mu    sync.Mutex
}

// fetchBackupsJobResult contains the result from fetching a bunch of backups
// from the host.
type fetchBackupsJobResult struct {
	err             error
	uploadedBackups []modules.UploadedBackup
}

// managedHasJob returns true if there is a job in the queue that the worker
// could potentially perform.
func (queue *fetchBackupsJobQueue) managedHasJob() bool {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return len(queue.queue) > 0
}

// managedLen returns the length of the fetchBackupsJobQueue queue
func (queue *fetchBackupsJobQueue) managedLen() int {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return len(queue.queue)
}

// callQueueFetchBackupsJob will add the fetch backups job to the worker's
// queue. A channel will be returned, this channel will have the result of the
// job returned down it when the job is completed.
//
// Testing happens via an integration test. siatest/renter/TestRemoteBackup has
// a test where a backup is fetched from a host, an action which reaches this
// code.
func (w *worker) callQueueFetchBackupsJob() chan fetchBackupsJobResult {
	resultChan := make(chan fetchBackupsJobResult)
	w.staticFetchBackupsJobQueue.mu.Lock()
	w.staticFetchBackupsJobQueue.queue = append(w.staticFetchBackupsJobQueue.queue, resultChan)
	w.staticFetchBackupsJobQueue.mu.Unlock()
	w.staticWake()
	return resultChan
}

// managedKillFetchBackupsJobs will throw an error for all queued backup jobs,
// as they will not complete due to the worker being shut down.
func (w *worker) managedKillFetchBackupsJobs() {
	w.staticFetchBackupsJobQueue.mu.Lock()
	for _, job := range w.staticFetchBackupsJobQueue.queue {
		result := fetchBackupsJobResult{
			err: errors.New("worker was killed before backups could be retrieved"),
		}
		job <- result
	}
	w.staticFetchBackupsJobQueue.mu.Unlock()
}

// managedPerformFetchBackupsJob will fetch the list of backups from the host
// and return them down the provided struct.
//
// Testing happens via an integration test. siatest/renter/TestRemoteBackup has
// a test where a backup is fetched from a host, an action which reaches this
// code.
func (w *worker) managedPerformFetchBackupsJob() {
	// Check whether there is any work to be performed.
	var resultChan chan fetchBackupsJobResult
	w.staticFetchBackupsJobQueue.mu.Lock()
	if len(w.staticFetchBackupsJobQueue.queue) == 0 {
		w.staticFetchBackupsJobQueue.mu.Unlock()
		return
	}
	resultChan = w.staticFetchBackupsJobQueue.queue[0]
	w.staticFetchBackupsJobQueue.queue = w.staticFetchBackupsJobQueue.queue[1:]
	w.staticFetchBackupsJobQueue.mu.Unlock()

	// Fetch a session to use in retrieving the backups.
	// TODO: move this to RHP3
	session, err := w.renter.hostContractor.Session(w.staticHostPubKey, w.renter.tg.StopChan())
	if err != nil {
		result := fetchBackupsJobResult{
			err: errors.AddContext(err, "unable to acquire session"),
		}
		resultChan <- result
		return
	}
	defer session.Close()

	// Check for price gouging before completing the job.
	allowance := w.renter.hostContractor.Allowance()
	gc := modules.CheckHostSettingsGouging(allowance, session.HostSettings())
	if gc.FetchBackups.IsGouging {
		result := fetchBackupsJobResult{
			err: fmt.Errorf("price gouging check failed for fetch backups job, %s", gc.FetchBackups.Reason),
		}
		resultChan <- result
		return
	}

	backups, err := w.renter.callFetchHostBackups(session)
	result := fetchBackupsJobResult{
		uploadedBackups: backups,
		err:             errors.AddContext(err, "unable to download snapshot table"),
	}
	resultChan <- result
}
