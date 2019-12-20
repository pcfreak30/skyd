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
	staticID        string
	staticHostKey   types.SiaPublicKey
	staticSecretKey crypto.SecretKey

	pendingSpends types.Currency
	pendingFunds  types.Currency
	balance       types.Currency

	mu sync.Mutex
	c  hostContractor
	r  *Renter
}

// managedOpenAccount returns a new account for the given host
func (r *Renter) managedOpenAccount(hostKey types.SiaPublicKey) *account {
	id := r.mu.Lock()
	defer r.mu.Unlock(id)

	hpk := hostKey.String()
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
		staticID:        spk.String(),
		staticHostKey:   hostKey,
		staticSecretKey: sk,
		c:               r.hostContractor,
		r:               r,
	}
	r.accounts[hpk] = acc
	return acc
}

// ID returns the account id
func (a *account) ID() string {
	return a.staticID
}

// HostKey returns the host's publickey
func (a *account) HostKey() types.SiaPublicKey {
	return a.staticHostKey
}

// Balance returns the account balance. Note that this returns the eventual
// account balance. This balance is calculated taking into account pending
// spends and funds. It is used by the worker to figure out whether it should
// schedule an account refill.
func (a *account) Balance() types.Currency {
	total := a.balance.Add(a.pendingFunds)
	var eventual types.Currency
	if a.pendingSpends.Cmp(total) < 0 {
		eventual = total.Sub(a.pendingSpends)
	}
	return eventual
}

// ProvidePaymentForRPC implements the PaymentProvider interface. This way, the
// account object can be used to pay for any RPC, regardless of whether it is
// paid through an ephemeral account or file contract.
func (a *account) ProvidePaymentForRPC(rpcID types.Specifier, amount types.Currency, stream modules.Stream, currentBlockHeight types.BlockHeight) (payment types.Currency, err error) {
	var provider modules.PaymentProvider
	switch rpcID {
	// funding an ephemeral account is always paid from a contract
	case modules.RPCFundEphemeralAccount:
		provider, err = a.c.PaymentProvider(a.staticHostKey)
		if err != nil {
			return
		}
		a.managedProcessFundIntent(amount)
		payment, err = provider.ProvidePaymentForRPC(rpcID, amount, stream, currentBlockHeight)
		a.managedProcessFundResult(amount, err == nil)
	// all other RPCs get paid by ephemeral account
	default:
		provider = a.paymentProvider()
		a.managedProcessPaymentIntent(amount)
		payment, err = provider.ProvidePaymentForRPC(rpcID, amount, stream, currentBlockHeight)
		a.managedProcessPaymentResult(amount, err == nil)
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

// managedProcessFundIntent will add the amount that is being funded to the
// pending balance.
func (a *account) managedProcessFundIntent(amount types.Currency) {
	a.mu.Lock()
	a.pendingFunds = a.pendingFunds.Add(amount)
	a.mu.Unlock()
}

// managedProcessFundResult will properly adjust the balance after the fund was
// executed.
func (a *account) managedProcessFundResult(amount types.Currency, success bool) {
	a.mu.Lock()
	a.pendingFunds = a.pendingFunds.Sub(amount)
	if success {
		a.balance = a.balance.Add(amount)
	}
	a.mu.Unlock()
}

// managedProcessPayment will deduct the amount that was paid from the local
// balance. If the balance drops below a certain threshold it triggers a refill.
func (a *account) managedProcessPaymentIntent(amount types.Currency) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pendingSpends = a.pendingSpends.Add(amount)
}

// managedProcessPaymentResult will properly adjust the balance after the RPC
// has executed.
func (a *account) managedProcessPaymentResult(amount types.Currency, success bool) {
	a.mu.Lock()
	a.pendingSpends = a.pendingSpends.Sub(amount)
	if success {
		a.balance = a.balance.Sub(amount)
	}
	a.mu.Unlock()
}

// newSignedWithdrawal returns a withdrawal message and signature using the
// provided withdrawal input.
func (a *account) newSignedWithdrawal(amount types.Currency, expiry types.BlockHeight) (modules.WithdrawalMessage, crypto.Signature) {
	wm := modules.WithdrawalMessage{
		Id:     a.staticID,
		Expiry: expiry,
		Amount: amount,
		Nonce:  fastrand.Bytes(withdrawalNonceSize),
	}
	sig := crypto.SignHash(crypto.HashObject(wm), a.staticSecretKey)
	return wm, sig
}
