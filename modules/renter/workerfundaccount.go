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
	job    *fundAccountJob
	funded types.Currency
	err    error
}

// callQueueFundAccountJob will add a fund account job to the worker's
// queue. A channel will be returned, this channel will have the result of the
// job returned down it when the job is completed.
func (w *worker) callQueueFundAccount(amount types.Currency) chan fundAccountJobResult {
	w.accountState.mu.Lock()
	w.accountState.pending = w.accountState.pending.Add(amount)
	w.accountState.mu.Unlock()

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
			job: job,
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

	// Try to dequeue a job, return if there's no work to be performed
	w.staticFundAccountJobQueue.mu.Lock()
	if len(w.staticFundAccountJobQueue.queue) == 0 {
		w.staticFundAccountJobQueue.mu.Unlock()
		return
	}
	job := w.staticFundAccountJobQueue.queue[0]
	w.staticFundAccountJobQueue.queue = w.staticFundAccountJobQueue.queue[1:]
	w.staticFundAccountJobQueue.mu.Unlock()

	// Fund the account & return the result
	if err := w.account.Fund(job.amount); err != nil {
		job.resultChan <- fundAccountJobResult{
			job: job,
			err: err,
		}
		return
	}
	job.resultChan <- fundAccountJobResult{
		job:    job,
		funded: job.amount,
	}
}

// managedRefillAccount checks if the account balance drops below a certain
// threshold, if so it will schedule a fundAccountJob to refill the account
func (w *worker) managedRefillAccount() bool {
	w.accountState.mu.Lock()
	total := w.accountState.balance.Add(w.accountState.pending)
	w.accountState.mu.Unlock()

	if total.Cmp(w.staticAccountBalanceTarget.Div64(2)) < 0 {
		refill := w.staticAccountBalanceTarget.Sub(total)
		resultChan := w.callQueueFundAccount(refill)
		go w.threadedProcessFundAccountResult(resultChan)
		return true
	}

	return false
}

// managedProcessWithdrawal will update the account state when other workers
// signal they have spent from the ephemeral account
func (w *worker) managedProcessWithdrawal(amount types.Currency) {
	w.accountState.mu.Lock()
	defer w.accountState.mu.Unlock()

	if w.accountState.balance.Cmp(amount) < 0 {
		w.accountState.balance = types.ZeroCurrency
		return
	} // sanity check

	w.accountState.balance = w.accountState.balance.Sub(amount)
}

// threadedProcessFundAccountResult will update the account state when a
// fundAccountJob completed.
func (w *worker) threadedProcessFundAccountResult(resultChan chan fundAccountJobResult) {
	if err := w.renter.tg.Add(); err != nil {
		return
	}
	defer w.renter.tg.Done()

	for {
		select {
		case result := <-resultChan:
			w.accountState.mu.Lock()
			w.accountState.pending = w.accountState.pending.Sub(result.job.amount)
			w.accountState.balance = w.accountState.balance.Add(result.funded)
			w.accountState.mu.Unlock()
			return
		case <-w.renter.tg.StopChan():
			return
		case <-w.killChan:
			return
		}
	}
}
