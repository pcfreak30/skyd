package renter

import (
	"sync"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
)

// withdrawalNonceSize is the size of the nonce which is sent in the
// WithdrawalMessage
const withdrawalNonceSize = 8

// withdrawalDefaultExpiry decides the WithdrawalMessage expiry. This default is
// added to the current blockheight to make up the expiry blockheight.
const withdrawalDefaultExpiry = 144

// Account interface lists all possible actions that can be executed on an
// ephemeral account on the host. Some of these methods will involve underlying
// RPC calls to the host.
type Account interface {
	GetBalance() types.Currency
	Fund(amount types.Currency) (types.Currency, error)
}

// account represents a renter's ephemeral account on a host
type account struct {
	staticHostKey       types.SiaPublicKey
	staticAccountID     string
	staticSecretKey     crypto.SecretKey
	staticBalanceTarget types.Currency

	balanceLocal   types.Currency
	balancePending types.Currency
	balanceMu      sync.Mutex
	refillChan     chan types.Currency

	contractor hostContractor
	r          *Renter
}

// managedOpenAccount returns a new account for the given host
func (r *Renter) managedOpenAccount(hostKey types.SiaPublicKey, target types.Currency, refillChan chan types.Currency) Account {
	id := r.mu.Lock()
	defer r.mu.Unlock(id)

	hpk := hostKey.String()
	acc, exists := r.accounts[hpk]
	if !exists {
		sk, pk := crypto.GenerateKeyPair()
		spk := types.SiaPublicKey{
			Algorithm: types.SignatureEd25519,
			Key:       pk[:],
		}

		acc = &account{
			staticHostKey:       hostKey,
			staticAccountID:     spk.String(),
			staticSecretKey:     sk,
			staticBalanceTarget: target,
			refillChan:          refillChan,
			contractor:          r.hostContractor,
			r:                   r,
		}
		r.accounts[hpk] = acc
	}

	return acc
}

// ID returns the account id
func (a *account) ID() string {
	return a.staticAccountID
}

// HostKey returns the host's publickey
func (a *account) HostKey() types.SiaPublicKey {
	return a.staticHostKey
}

// GetBalance returns the current account balance
func (a *account) GetBalance() types.Currency {
	a.balanceMu.Lock()
	defer a.balanceMu.Unlock()
	return a.balanceLocal
}

// Fund will fund the account by depositing the amount into the account
func (a *account) Fund(amount types.Currency) (types.Currency, error) {
	c := a.r.managedRPCClient(a.staticHostKey)
	return c.FundEphemeralAccount(a, amount)
}

// ProvidePaymentForRPC implements the RPCPaymentProvider interface. This method
// will abstract if the RPC was paid for using an ephemeral account or an
// underlying file contract.
func (a *account) ProvidePaymentForRPC(rpcID types.Specifier, cost types.Currency, stream modules.Stream, currentBlockHeight types.BlockHeight) (types.Currency, error) {
	switch rpcID {
	// funding an ephemeral account is always paid from a contract
	case modules.RPCFundEphemeralAccount:
		a.managedProcessFundIntent(cost)
		provider, err := a.contractor.PaymentProvider(a.staticHostKey)
		if err != nil {
			return types.ZeroCurrency, err
		}

		payment, err := provider.ProvidePaymentForRPC(rpcID, cost, stream, currentBlockHeight)
		a.managedProcessFundResult(cost, err == nil)
		return payment, err
	// all other RPCs get paid by ephemeral account
	default:
		a.managedProcessPaymentIntent(cost)
		provider := a.paymentProvider()
		payment, err := provider.ProvidePaymentForRPC(rpcID, cost, stream, currentBlockHeight)
		a.managedProcessPaymentResult(cost, err == nil)
		return payment, err
	}
}

// paymentProvider returns an object that adheres to the RPCPaymentProvider
// interface. Not we use an interface adapter here to avoid creating a separate
// object for it. This essentially handles PayByEphemeralAccount.
func (a *account) paymentProvider() modules.RPCPaymentProvider {
	return modules.RPCPaymentProviderFunc(func(rpcID types.Specifier, cost types.Currency, stream modules.Stream, currentBlockHeight types.BlockHeight) (types.Currency, error) {
		// Note that we purposefully do not verify if the account has sufficient
		// funds. It is perfectly ok to provide payment even though the account
		// has insufficient funds at the time. The host will block until the
		// withdrawal until the account is funded, or until it times out.

		// create a withdrawal message and signature that will pay for the RPC
		msg, sig := a.newSignedWithdrawal(cost, currentBlockHeight+withdrawalDefaultExpiry)

		// send PaymentRequest & PayByEphemeralAccountRequest
		pRequest := modules.PaymentRequest{Type: modules.PayByEphemeralAccount}
		pbcRequest := modules.PayByEphemeralAccountRequest{
			Message:   msg,
			Signature: sig,
		}
		if err := stream.WriteRequest(pRequest, pbcRequest); err != nil {
			return types.ZeroCurrency, err
		}

		// receive PayByEphemeralAccountResponse
		var payByResponse modules.PayByEphemeralAccountResponse
		if err := stream.ReadResponse(payByResponse); err != nil {
			return types.ZeroCurrency, err
		}

		// TODO verify response

		// TODO: currently we return amount funded. It is probably more useful
		// to the have the host return its current version of the ephemeral
		// account balance, this way the renter can check more easily if his
		// version of the balance is drifting and act accordingly

		return payByResponse.Amount, nil
	})
}

// newSignedWithdrawal returns a withdrawal message and signature using the
// provided withdrawal input.
func (a *account) newSignedWithdrawal(amount types.Currency, expiry types.BlockHeight) (modules.WithdrawalMessage, crypto.Signature) {
	wm := modules.WithdrawalMessage{
		Id:     a.staticAccountID,
		Expiry: expiry,
		Amount: amount,
		Nonce:  fastrand.Bytes(withdrawalNonceSize),
	}
	sig := crypto.SignHash(crypto.HashObject(wm), a.staticSecretKey)
	return wm, sig
}

// managedProcessedFund will add the amount that was funded to the local
// balance.
func (a *account) managedProcessFundIntent(amount types.Currency) {
	a.balanceMu.Lock()
	a.balancePending = a.balancePending.Add(amount)
	a.balanceMu.Unlock()
}

// managedProcessedFund will add the amount that was funded to the local
// balance.
func (a *account) managedProcessFundResult(amount types.Currency, success bool) {
	a.balanceMu.Lock()
	a.balancePending = a.balancePending.Sub(amount)
	if success {
		a.balanceLocal = a.balanceLocal.Add(amount)
	}
	a.balanceMu.Unlock()
}

// managedProcessPayment will deduct the amount that was paid from the local
// balance. If the balance drops below a certain threshold it triggers a refill.
func (a *account) managedProcessPaymentIntent(amount types.Currency) {
	a.balanceMu.Lock()
	defer a.balanceMu.Unlock()

	// set the threshold at 80% of target, we do not want to refill each and
	// every time we drop 1 hasting below the target
	threshold := a.staticBalanceTarget.Mul64(8).Div64(10)

	// ensure we never exceed balance target
	needed := amount
	if needed.Cmp(a.staticBalanceTarget) >= 0 {
		needed = a.staticBalanceTarget
	}

	// calculate the eventual balance, this is what will be left after payment
	balanceTotal := a.balanceLocal.Add(a.balancePending)
	var eventualBalance types.Currency
	if needed.Cmp(balanceTotal) < 0 {
		eventualBalance = balanceTotal.Sub(needed)
	}

	// if we are below the threshold, schedule a refill
	if eventualBalance.Cmp(threshold) < 0 {
		refill := a.staticBalanceTarget.Sub(eventualBalance)
		a.refillChan <- refill
	}
}

// managedProcessPayment will deduct the amount that was paid from the local
// balance. If the balance drops below a certain threshold it triggers a refill.
func (a *account) managedProcessPaymentResult(amount types.Currency, success bool) {
	a.balanceMu.Lock()
	a.balancePending = a.balancePending.Sub(amount)
	if success {
		a.balanceLocal = a.balanceLocal.Sub(amount)
	}
	a.balanceMu.Unlock()
}
