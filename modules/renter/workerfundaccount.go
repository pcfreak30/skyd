package renter

import (
	"sync"

	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

// fundAccountJobQueue is the primary structure for managing fund ephemeral
// account jobs from the worker.
type fundAccountJobQueue struct {
	queue []*fundAccountJob
	mu    sync.Mutex
}

// fundAccountJob contains the details of which ephemeral account to fund and
// with how much.
type fundAccountJob struct {
	amount     types.Currency
	resultChan chan fundAccountJobResult
}

// fundAccountJobResult contains the result from funding an ephemeral account
// on the host.
type fundAccountJobResult struct {
	funded types.Currency
	err    error
}

// callQueueFundAccountJob will add a fund account job to the worker's
// queue. A channel will be returned, this channel will have the result of the
// job returned down it when the job is completed.
func (w *worker) callQueueFundAccount(amount types.Currency) chan fundAccountJobResult {
	resultChan := make(chan fundAccountJobResult)
	w.staticFundAccountJobQueue.mu.Lock()
	w.staticFundAccountJobQueue.queue = append(w.staticFundAccountJobQueue.queue, &fundAccountJob{
		amount:     amount,
		resultChan: resultChan,
	})
	w.staticFundAccountJobQueue.mu.Unlock()
	w.staticWake()
	return resultChan
}

// managedKillFundAccountJobs will throw an error for all queued fund account
// jobs, as they will not complete due to the worker being shut down.
//
// TODO: Need to write testing around the Kill functions for workers.
func (w *worker) managedKillFundAccountJobs() {
	w.staticFundAccountJobQueue.mu.Lock()
	for _, job := range w.staticFundAccountJobQueue.queue {
		result := fundAccountJobResult{
			err: errors.New("worker was killed before account could be funded"),
		}
		job.resultChan <- result
	}
	w.staticFundAccountJobQueue.mu.Unlock()
}

// threadedPerformFundAcountJob will try and execute a fund account job if there
// is one in the queue.
func (w *worker) threadedPerformFundAcountJob() {
	// Register ourselves with the threadgroup
	if err := w.renter.tg.Add(); err != nil {
		return
	}
	defer w.renter.tg.Done()

	// Check whether there is any work to be performed.
	w.staticFundAccountJobQueue.mu.Lock()
	if len(w.staticFundAccountJobQueue.queue) == 0 {
		w.staticFundAccountJobQueue.mu.Unlock()
		return
	}

	// Dequeue a job
	job := w.staticFundAccountJobQueue.queue[0]
	w.staticFundAccountJobQueue.queue = w.staticFundAccountJobQueue.queue[1:]
	w.staticFundAccountJobQueue.mu.Unlock()

	if err := w.account.FundAccount(job.amount); err != nil {
		job.resultChan <- fundAccountJobResult{err: err}
		return
	}

	job.resultChan <- fundAccountJobResult{funded: job.amount}
}

// managedRefillAccount will check if the account balance is lower than the
// target and schedule a fund account job to fill the potential deficit
func (w *worker) managedRefillAccount() {
	w.mu.Lock()
	var deficit types.Currency
	if w.accountBalance.Cmp(w.staticAccountBalanceTarget.Div64(2)) < 0 {
		deficit = w.staticAccountBalanceTarget.Sub(w.accountBalance)
		w.accountBalance = w.staticAccountBalanceTarget
	}
	defer w.mu.Unlock()

	if !deficit.Equals(types.ZeroCurrency) {
		w.callQueueFundAccount(deficit)
	}
}
