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
const withdrawalDefaultExpiry = 6

// account represents a renter's ephemeral account on a host
type account struct {
	staticHostKey       types.SiaPublicKey
	staticAccountID     string
	staticSecretKey     crypto.SecretKey
	staticBalanceTarget types.Currency
	staticBalanceMax    types.Currency

	pendingSpends   types.Currency
	pendingDeposits types.Currency
	balanceLocal    types.Currency
	balanceMu       sync.Mutex

	refillChan chan types.Currency

	contractor hostContractor
	r          *Renter
}

// managedOpenAccount returns a new account for the given host
func (r *Renter) managedOpenAccount(host modules.HostDBEntry, refillChan chan types.Currency) *account {
	id := r.mu.Lock()
	defer r.mu.Unlock(id)

	hpk := host.PublicKey.String()
	acc, exists := r.accounts[hpk]
	if exists {
		return acc
	}

	sk, pk := crypto.GenerateKeyPair()
	spk := types.SiaPublicKey{
		Algorithm: types.SignatureEd25519,
		Key:       pk[:],
	}

	acc = &account{
		staticHostKey:       host.PublicKey,
		staticAccountID:     spk.String(),
		staticSecretKey:     sk,
		staticBalanceTarget: types.SiacoinPrecision.Div64(2), // TODO
		staticBalanceMax:    types.SiacoinPrecision,          // TODO
		refillChan:          refillChan,
		contractor:          r.hostContractor,
		r:                   r,
	}
	r.accounts[hpk] = acc
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

// ProvidePaymentForRPC implements the PaymentProvider interface. This way, the
// account object can be used to pay for any RPC, regardless of whether it is
// paid through an ephemeral account or file contract.
func (a *account) ProvidePaymentForRPC(rpcID types.Specifier, cost types.Currency, stream modules.Stream, currentBlockHeight types.BlockHeight) (payment types.Currency, err error) {
	var provider modules.PaymentProvider
	switch rpcID {
	// funding an ephemeral account is always paid from a contract
	case modules.RPCFundEphemeralAccount:
		provider, err = a.contractor.PaymentProvider(a.staticHostKey)
		if err != nil {
			return
		}
		a.managedProcessFundIntent(cost)
		payment, err = provider.ProvidePaymentForRPC(rpcID, cost, stream, currentBlockHeight)
		a.managedProcessFundResult(cost, err == nil)
	// all other RPCs get paid by ephemeral account
	default:
		provider = a.paymentProvider()
		a.managedProcessPaymentIntent(cost)
		payment, err = provider.ProvidePaymentForRPC(rpcID, cost, stream, currentBlockHeight)
		a.managedProcessPaymentResult(cost, err == nil)
	}
	return
}

// paymentProvider returns an object that implements the PaymentProvider
// interface. Note that we use an interface adapter here to avoid creating a
// separate object for it. This essentially handles PayByEphemeralAccount.
func (a *account) paymentProvider() modules.PaymentProvider {
	return modules.PaymentProviderFunc(func(rpcID types.Specifier, payment types.Currency, stream modules.Stream, currentBlockHeight types.BlockHeight) (types.Currency, error) {
		// Note that we purposefully do not verify if the account has sufficient
		// funds. It is perfectly ok to provide payment even though the account
		// has insufficient funds at the time. The host will block until the
		// withdrawal until the account is funded, or until it times out.

		// create a withdrawal message and signature that will pay for the RPC
		msg, sig := a.newSignedWithdrawal(payment, currentBlockHeight+withdrawalDefaultExpiry)

		// send PaymentRequest & PayByEphemeralAccountRequest
		pRequest := modules.PaymentRequest{Type: modules.PayByEphemeralAccount}
		pbcRequest := modules.PayByEphemeralAccountRequest{
			Message:   msg,
			Signature: sig,
		}
		if err := stream.WriteObjects(pRequest, pbcRequest); err != nil {
			return types.ZeroCurrency, err
		}

		// receive PayByEphemeralAccountResponse
		var payByResponse modules.PayByEphemeralAccountResponse
		if err := stream.ReadObject(payByResponse); err != nil {
			return types.ZeroCurrency, err
		}

		return payByResponse.Amount, payByResponse.AccountManagerResponse
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

// managedProcessFundIntent will add the amount that is being funded to the
// pending balance.
func (a *account) managedProcessFundIntent(amount types.Currency) {
	a.balanceMu.Lock()
	a.pendingDeposits = a.pendingDeposits.Add(amount)
	a.balanceMu.Unlock()
}

// managedProcessFundResult will properly adjust the balance after the fund was
// executed.
func (a *account) managedProcessFundResult(amount types.Currency, success bool) {
	a.balanceMu.Lock()
	a.pendingDeposits = a.pendingDeposits.Sub(amount)
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
	defer a.refill()
	a.pendingSpends = a.pendingSpends.Add(amount)

}

// managedProcessPaymentResult will properly adjust the balance after the RPC
// has executed.
func (a *account) managedProcessPaymentResult(amount types.Currency, success bool) {
	a.balanceMu.Lock()
	a.pendingSpends = a.pendingSpends.Sub(amount)
	if success {
		a.balanceLocal = a.balanceLocal.Sub(amount)
	}
	a.balanceMu.Unlock()
}

// refill will use the account state to see if a refill is necessary. It will do
// so if the eventual account balance drops below a certain threshold.
func (a *account) refill() {
	// Calculate the eventual account balance of the account on the host, note
	// that this takes into account the amount of money that is in the process
	// of being spent.
	var eventual types.Currency
	if a.pendingSpends.Cmp(a.balanceLocal.Add(a.pendingDeposits)) < 0 {
		eventual = a.balanceLocal.Add(a.pendingDeposits).Sub(a.pendingSpends)
	}

	// If the account's eventual balance is below the threshold, we want to
	// schedule a refill. The amount to refill is the difference between the
	// eventual balance and our target balance. We only refill if we drop below
	// a threshold because we want to avoid refilling every time we drop 1
	// hasting below the target.
	threshold := a.staticBalanceTarget.Mul64(8).Div64(10)
	if eventual.Cmp(threshold) < 0 {
		refill := a.staticBalanceTarget.Sub(eventual)
		a.refillChan <- refill
	}
}
