package renter

// TODO: Some of the pieces of workeraccount.go probably belong here.

// TODO: May be able to abstract some of the cooldown code and re-use it across
// jobs. This should probably happen sooner than later.

import (
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/siamux"
)

// managedAccountNeedsRefill will check whether the worker's account needs to be
// refilled. This function will return false if any conditions are met which
// are likely to prevent the refill from being successful.
func (w *worker) managedAccountNeedsRefill() bool {
	// Check if the host version is compatible with accounts.
	cache := w.staticCache()
	// TODO: Change '!=' to a '<=' once the protocol has solidified more.
	if build.VersionCmp(cache.staticHostVersion, minAsyncVersion) != 0 {
		return false
	}

	// Check if the price table is valid.
	if !w.staticPriceTable().staticValid() {
		return false
	}

	// Query some protected state.
	w.staticAccount.mu.Lock()
	cooldownUntil := w.staticAccount.cooldownUntil
	balance := w.staticAccount.availableBalance()
	w.staticAccount.mu.Unlock()

	// Check if there is a cooldown in place.
	if time.Now().Before(cooldownUntil) {
		return false
	}

	// Check if the balance is high enough that a refill is not needed.
	if balance.Cmp(w.staticBalanceTarget.Div64(2)) >= 0 {
		return false
	}

	// A refill is needed.
	return true
}

// managedTryRefillAccount will check if the account needs to be refilled
func (w *worker) managedRefillAccount() {
	// the account balance dropped to below half the balance target, refill
	balance := w.staticAccount.managedAvailableBalance()
	amount := w.staticBalanceTarget.Sub(balance)

	// track the deposit
	//
	// TODO: Should probably explain here how the tracking works.
	w.staticAccount.managedTrackDeposit(amount)
	var err error
	defer func() {
		w.staticAccount.managedCommitDeposit(amount, err == nil)

		// If the error is not nil, increment the cooldown.
		if err != nil {
			w.staticAccount.mu.Lock()
			w.staticAccount.cooldownUntil = cooldownUntil(w.staticAccount.consecutiveFailures)
			w.staticAccount.consecutiveFailures++
			w.staticAccount.recentErr = err
			w.staticAccount.mu.Unlock()
		}
	}()

	// create a new stream
	var stream siamux.Stream
	stream, err = w.staticNewStream()
	if err != nil {
		err = errors.AddContext(err, "Unable to create a new stream")
		return
	}
	defer func() {
		closeErr := stream.Close()
		if closeErr != nil {
			w.renter.log.Println("ERROR: failed to close stream", closeErr)
		}
	}()

	// write the specifier
	//
	// TODO: AddContext on all of these errors.
	err = modules.RPCWrite(stream, modules.RPCFundAccount)
	if err != nil {
		return
	}

	// send price table uid
	pt := w.staticPriceTable().staticPriceTable
	err = modules.RPCWrite(stream, pt.UID)
	if err != nil {
		return
	}

	// send fund account request
	err = modules.RPCWrite(stream, modules.FundAccountRequest{Account: w.staticAccount.staticID})
	if err != nil {
		return
	}

	// provide payment
	err = w.renter.hostContractor.ProvidePayment(stream, w.staticHostPubKey, modules.RPCFundAccount, amount.Add(pt.FundAccountCost), modules.ZeroAccountID, w.staticCache().staticBlockHeight)
	if err != nil {
		return
	}

	// receive FundAccountResponse
	//
	// TODO: Do something with resp.
	var resp modules.FundAccountResponse
	err = modules.RPCRead(stream, &resp)
	if err != nil {
		return
	}
	return
}
