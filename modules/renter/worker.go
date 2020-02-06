package renter

// worker.go defines a worker with a work loop. Each worker is connected to a
// single host, and the work loop will listen for jobs and then perform them.
//
// The worker has a set of jobs that it is capable of performing. The standard
// functions for a job are Queue, Kill, and Perform. Queue will add a job to the
// queue of work of that type. Kill will empty the queue and close out any work
// that will not be completed. Perform will grab a job from the queue if one
// exists and complete that piece of work. See workerfetchbackups.go for a clean
// example.
//
// The worker has an ephemeral account on the host. It can use this account to
// pay for downloads and uploads. In order to ensure the account's balance does
// not run out, it maintains a balance target by refilling it when necessary.
//
// TODO: A single session should be added to the worker that gets maintained
// within the work loop. All jobs performed by the worker will use the worker's
// single session.
//
// TODO: The upload and download code needs to be moved into properly separated
// subsystems.
//
// TODO: Need to write testing around the kill functions in the worker, to clean
// up any queued jobs after a worker has been killed.

import (
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

// A worker listens for work on a certain host.
//
// The mutex of the worker only protects the 'unprocessedChunks' and the
// 'standbyChunks' fields of the worker. The rest of the fields are only
// interacted with exclusively by the primary worker thread, and only one of
// those ever exists at a time.
//
// The workers have a concept of 'cooldown' for uploads and downloads. If a
// download or upload operation fails, the assumption is that future attempts
// are also likely to fail, because whatever condition resulted in the failure
// will still be present until some time has passed. Without any cooldowns,
// uploading and downloading with flaky hosts in the worker sets has
// substantially reduced overall performance and throughput.
type worker struct {
	// The host pub key also serves as an id for the worker, as there is only
	// one worker per host.
	staticHostPubKey    types.SiaPublicKey
	staticHostPubKeyStr string

	// Download variables that are not protected by a mutex, but also do not
	// need to be protected by a mutex, as they are only accessed by the master
	// thread for the worker.
	//
	// The 'owned' prefix here indicates that only the master thread for the
	// object (in this case, 'threadedWorkLoop') is allowed to access these
	// variables. Because only that thread is allowed to access the variables,
	// that thread is able to access these variables without a mutex.
	ownedDownloadConsecutiveFailures int       // How many failures in a row?
	ownedDownloadRecentFailure       time.Time // How recent was the last failure?

	// Download variables related to queuing work. They have a separate mutex to
	// minimize lock contention.
	downloadChunks     []*unfinishedDownloadChunk // Yet unprocessed work items.
	downloadMu         sync.Mutex
	downloadTerminated bool // Has downloading been terminated for this worker?

	// Job queues for the worker.
	staticFetchBackupsJobQueue   fetchBackupsJobQueue
	staticJobQueueDownloadByRoot jobQueueDownloadByRoot
	staticFundAccountJobQueue    fundAccountJobQueue

	// Upload variables.
	unprocessedChunks         []*unfinishedUploadChunk // Yet unprocessed work items.
	uploadConsecutiveFailures int                      // How many times in a row uploading has failed.
	uploadRecentFailure       time.Time                // How recent was the last failure?
	uploadRecentFailureErr    error                    // What was the reason for the last failure?
	uploadTerminated          bool                     // Have we stopped uploading?

	// The staticAccount represent the renter's ephemeral account on the host.
	// It keeps track of the available balance in the account, the worker has a
	// refill mechanism that keeps the account balance filled up until the
	// staticBalanceTarget.
	staticAccount       *account
	staticBalanceTarget types.Currency

	// Every worker has an RPC client it uses to interact with the host. The RPC
	// client interface exposes all RPC calls the renter can perform on the
	// host.
	staticRPCClient RPCClient

	// The worker has to keep track of the host's RPC costs. When a worker is
	// created, it will call an RPC on the host to fetch his prices. After that
	// the worker will ensure it has up-to-date prices by continuously fetching
	// a new update before the price table expires.
	priceTable        modules.RPCPriceTable
	priceTableUpdated int64

	// Utilities.
	//
	// The mutex is only needed when interacting with 'downloadChunks' and
	// 'unprocessedChunks', as everything else is only accessed from the single
	// master thread.
	killChan chan struct{} // Worker will shut down if a signal is sent down this channel.
	mu       sync.Mutex
	renter   *Renter
	wakeChan chan struct{} // Worker will check queues if given a wake signal.
}

// managedBlockUntilReady will block until the worker has internet connectivity.
// 'false' will be returned if a kill signal is received or if the renter is
// shut down before internet connectivity is restored. 'true' will be returned
// if internet connectivity is successfully restored.
func (w *worker) managedBlockUntilReady() bool {
	// Check if the worker has received a kill signal, or if the renter has
	// received a stop signal.
	select {
	case <-w.renter.tg.StopChan():
		return false
	case <-w.killChan:
		return false
	default:
	}

	// Check internet connectivity. If the worker does not have internet
	// connectivity, block until connectivity is restored.
	for !w.renter.g.Online() {
		select {
		case <-w.renter.tg.StopChan():
			return false
		case <-w.killChan:
			return false
		case <-time.After(offlineCheckFrequency):
		}
	}

	timestamp := func() int64 {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.priceTableUpdated
	}

	// Check the price table updated timestamp. If the worker has not yet
	// received the host's prices, we block until it has.
	for timestamp() == 0 {
		select {
		case <-w.renter.tg.StopChan():
			return false
		case <-w.killChan:
			return false
		case <-time.After(priceTableCheckFrequency):
		}
	}

	return true
}

// staticKilled is a convenience function to determine if a worker has been
// killed or not.
func (w *worker) staticKilled() bool {
	select {
	case <-w.killChan:
		return true
	default:
		return false
	}
}

// staticWake needs to be called any time that a job queued.
func (w *worker) staticWake() {
	select {
	case w.wakeChan <- struct{}{}:
	default:
	}
}

// threadedWorkLoop continually checks if work has been issued to a worker. The
// work loop checks for different types of work in a specific order, forming a
// priority queue for the various types of work. It is possible for continuous
// requests for one type of work to drown out a worker's ability to perform
// other types of work.
//
// If no work is found, the worker will sleep until woken up. Because each
// iteration is stateless, it may be possible to reduce the goroutine count in
// Sia by spinning down the worker / expiring the thread when there is no work,
// and then checking if the thread exists and creating a new one if not when
// alerting / waking the worker. This will not interrupt any connections that
// the worker has because the worker object will be kept in memory via the
// worker map.
func (w *worker) threadedWorkLoop() {
	// Ensure that all queued jobs are gracefully cleaned up when the worker is
	// shut down.
	//
	// TODO: Need to write testing around these kill functions and ensure they
	// are executing correctly.
	defer w.managedKillUploading()
	defer w.managedKillDownloading()
	defer w.managedKillFetchBackupsJobs()
	defer w.managedKillFundAccountJobs()
	defer w.managedKillJobsDownloadByRoot()

	// Primary work loop. There are several types of jobs that the worker can
	// perform, and they are attempted with a specific priority. If any type of
	// work is attempted, the loop resets to check for higher priority work
	// again. This means that a stream of higher priority tasks can starve a
	// building set of lower priority tasks.
	//
	// 'workAttempted' indicates that there was a job to perform, and that a
	// nontrivial amount of time was spent attempting to perform the job. The
	// job may or may not have been successful, that is irrelevant.
	for {
		// There are certain conditions under which the worker should either
		// block or exit. This function will block until those conditions are
		// met, returning 'true' when the worker can proceed and 'false' if the
		// worker should exit.
		if !w.managedBlockUntilReady() {
			return
		}

		// Check if the account needs to be refilled.
		w.scheduleRefillAccount()

		// Perform any job to fund the account
		workAttempted := w.managedPerformFundAcountJob()
		if workAttempted {
			continue
		}

		// Perform any job to fetch the list of backups from the host.
		workAttempted = w.managedPerformFetchBackupsJob()
		if workAttempted {
			continue
		}
		// Perform any job to fetch data by its sector root. This is given
		// priority because it is only used by viewnodes, which are service
		// operators that need to have good performance for their customers.
		workAttempted = w.managedLaunchJobDownloadByRoot()
		if workAttempted {
			continue
		}
		// Perform any job to help download a chunk.
		workAttempted = w.managedPerformDownloadChunkJob()
		if workAttempted {
			continue
		}
		// Perform any job to help upload a chunk.
		workAttempted = w.managedPerformUploadChunkJob()
		if workAttempted {
			continue
		}

		// Block until new work is received via the upload or download channels,
		// or until a kill or stop signal is received.
		select {
		case <-w.wakeChan:
			continue
		case <-w.killChan:
			return
		case <-w.renter.tg.StopChan():
			return
		}
	}
}

// scheduleRefillAccount will check if the account needs to be refilled,
// and will schedule a fund account job if so. This is called every time the
// worker spends from the account.
func (w *worker) scheduleRefillAccount() {
	// Calculate the threshold, if the account's available balance is below this
	// threshold, we want to trigger a refill.We only refill if we drop below a
	// threshold because we want to avoid refilling every time we drop 1 hasting
	// below the target.
	threshold := w.staticBalanceTarget.Mul64(8).Div64(10)

	// Fetch the account's available balance and skip if it's above the
	// threshold
	balance := w.staticAccount.AvailableBalance()
	if balance.Cmp(threshold) >= 0 {
		return
	}

	// If it's below the threshold, calculate the refill amount and enqueue a
	// new fund account job
	refill := w.staticBalanceTarget.Sub(balance)

	w.staticAccount.managedProcessFundIntent(refill)
	resultChan := w.callQueueFundAccount(refill)
	if err := w.renter.tg.Add(); err != nil {
		return
	}
	go func() {
		defer w.renter.tg.Done()

		select {
		case result := <-resultChan:
			w.staticAccount.managedProcessFundResult(result)
		case <-w.killChan:
			return
		case <-w.renter.tg.StopChan():
			return
		}
	}()

	// TODO: handle result chan
	// TODO: add cooldown in case of failure
}

// newWorker will create and return a worker that is ready to receive jobs.
func (r *Renter) newWorker(hostPubKey types.SiaPublicKey, bh types.BlockHeight) (*worker, error) {
	he, ok, err := r.hostDB.Host(hostPubKey)
	if err != nil {
		return nil, errors.AddContext(err, "could not find host entry")
	}
	if !ok {
		return nil, errors.New("host does not exist")
	}

	// TODO: calc balance target using host settings + renter imposed maximum

	w := &worker{
		staticHostPubKey:    hostPubKey,
		staticHostPubKeyStr: hostPubKey.String(),

		staticAccount:       openAccount(hostPubKey, r.hostContractor),
		staticBalanceTarget: types.SiacoinPrecision,
		staticRPCClient:     r.newRPCClient(he, bh),

		killChan: make(chan struct{}),
		wakeChan: make(chan struct{}, 1),
		renter:   r,
	}

	// Initialize the worker in a separate goroutine. This process will fetch
	// the host's price table and kickstart the periodic price table updates.
	if err := w.renter.tg.Add(); err != nil {
		return w, nil
	}
	go func() {
		defer w.renter.tg.Done()
		w.threadedInit()
	}()

	return w, nil
}

// UpdateBlockHeight is called by the renter when it processes a consensus
// change. The worker forwards this to the RPC client as it contains the RPC
// price table. To ensure the client has up-to-date pricing, it needs to be
// alerted when consensus changes as it might want to update its pricing table.
func (w *worker) UpdateBlockHeight(blockHeight types.BlockHeight) {
	w.staticRPCClient.UpdateBlockHeight(blockHeight)
}

// threadedInit runs in the background and fetches the host's RPC price table
// for the first time. Without it, the worker can't process any jobs. This
// process will also kickstart the periodic price table updates, depending on
// the expiry.
func (w *worker) threadedInit() {
	if err := w.managedUpdatePriceTable(); err != nil {
		return // TODO (follow-up) alert? retry?
	}

	// The frequency with which we update the RPC price table is dependant on
	// the host's epxiry. We ensure prices are up-to-date by fetching new prices
	// at half the expiry window.
	w.mu.Lock()
	frequency := (w.priceTable.Expiry - w.priceTableUpdated) / 2
	w.mu.Unlock()
	go w.threadedUpdatePriceTable(frequency)
}

// threadedUpdatePriceTable will periodically update the RPC price table to
// fetch the host's latest prices. This is necessary as the host will refuse
// renters that have an outdated price table.
func (w *worker) threadedUpdatePriceTable(frequency int64) {
	for {
		select {
		case <-w.renter.tg.StopChan():
			break
		case <-time.After(time.Duration(frequency) * time.Second):
		}

		func() {
			if err := w.renter.tg.Add(); err != nil {
				return
			}
			w.renter.tg.Done()

			if err := w.managedUpdatePriceTable(); err != nil {
				// TODO: error handling
				return
			}
		}()

	}
}

// managedUpdatePriceTable calls the updatePriceTable RPC on the host
func (w *worker) managedUpdatePriceTable() error {
	pp, err := w.paymentProvider()
	if err != nil {
		return err
	}

	pt, err := w.staticRPCClient.UpdatePriceTable(pp)
	if err != nil {
		return err
	}

	w.mu.Lock()
	w.priceTable = pt
	w.priceTableUpdated = time.Now().Unix()
	w.mu.Unlock()
	return nil
}

// paymentProvider returns a payment provider, if the account has available
// balance, we will always pay from ephemeral account, otherwise use we pay by
// contract
func (w *worker) paymentProvider() (modules.PaymentProvider, error) {
	if w.staticAccount.AvailableBalance().IsZero() {
		return w.renter.hostContractor.PaymentProvider(w.staticHostPubKey)
	}
	return w.staticAccount, nil
}
