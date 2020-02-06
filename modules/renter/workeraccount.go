package renter

import (
	"fmt"

	"sync"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"

	"gitlab.com/NebulousLabs/siamux"
)

// TODO: try to load account from persistence
//
// TODO: for now the account is a separate object that sits as first class
// object on the worker, most probably though this will move as to not have two
// separate mutex domains.

// withdrawalValidityPeriod defines the period (in blocks) a withdrawal message
// remains spendable after it has been created. Together with the current block
// height at time of creation, this period makes up the WithdrawalMessage's
// expiry block height.
const withdrawalValidityPeriod = 6

// account represents a renter's ephemeral account on a host.
type account struct {
	staticID        string
	staticHostKey   types.SiaPublicKey
	staticSecretKey crypto.SecretKey

	pendingSpends types.Currency
	pendingFunds  types.Currency
	balance       types.Currency

	mu sync.Mutex
	c  hostContractor
}

// openAccount returns an account for the given host.
func openAccount(hostKey types.SiaPublicKey, contractor hostContractor) *account {
	// generate a new key pair
	sk, pk := crypto.GenerateKeyPair()
	spk := types.SiaPublicKey{
		Algorithm: types.SignatureEd25519,
		Key:       pk[:],
	}

	// create the account
	return &account{
		staticID:        spk.String(),
		staticHostKey:   hostKey,
		staticSecretKey: sk,
		c:               contractor,
	}
}

// AvailableBalance returns the amount of money that is available to spend. It
// is calculated by taking into account pending spends and pending funds.
func (a *account) AvailableBalance() types.Currency {
	a.mu.Lock()
	defer a.mu.Unlock()

	total := a.balance.Add(a.pendingFunds)
	if a.pendingSpends.Cmp(total) < 0 {
		return total.Sub(a.pendingSpends)
	}
	return types.ZeroCurrency
}

// ProvidePaymentForRPC is the implementation of the PaymentProvider interface.
// The account can be used to pay for RPCs. Depending on which RPC, payment will
// be made from an ephemeral account, or from a file contract. Typically, only
// funding an ephemeral account is paid from a file contract.
func (a *account) ProvidePaymentForRPC(rpcID types.Specifier, amount types.Currency, stream siamux.Stream, blockHeight types.BlockHeight) (payment types.Currency, err error) {
	// Depending on the RPC we'll want to make payment from an ephemeral account
	// or from a file contract. Typically, only funding the ephemeral account is
	// paid from a file contract. All other RPCs are paid using an ephemeral
	// account.
	//
	// Note that the type of payment method decides:
	// - where we fetch the payment provider from
	// - which fields we update on the account
	if rpcID == modules.RPCFundEphemeralAccount {
		provider, err := a.c.PaymentProvider(a.staticHostKey)
		if err != nil {
			err = errors.AddContext(err, fmt.Sprintf("Could not create a (contract) payment provider for RPC %v", rpcID))
			return types.ZeroCurrency, err
		}

		a.managedProcessFundIntent(amount)
		payment, err = provider.ProvidePaymentForRPC(rpcID, amount, stream, blockHeight)
		a.managedProcessFundResult(amount, err == nil)

		return payment, errors.AddContext(err, fmt.Sprintf("Could not provide payment for RPC %s", rpcID))
	}

	provider := a.paymentProvider()
	a.managedProcessPaymentIntent(amount)
	payment, err = provider.ProvidePaymentForRPC(rpcID, amount, stream, blockHeight)
	a.managedProcessPaymentResult(amount, err == nil)

	return payment, errors.AddContext(err, fmt.Sprintf("Could not provide payment for RPC %s", rpcID))
}

// paymentProvider returns an object that implements the PaymentProvider
// interface. This method wraps an interface adapter and thus avoids creating a
// separate object that implements the PaymentProvider interface. This way the
// account object can be used to handle PayByEphemeralAccount.
func (a *account) paymentProvider() modules.PaymentProvider {
	return modules.PaymentProviderFunc(func(rpcID types.Specifier, payment types.Currency, stream siamux.Stream, blockHeight types.BlockHeight) (types.Currency, error) {
		// NOTE: we purposefully do not verify if the account has sufficient
		// funds. Seeing as spends are a blocking action on the host, it is
		// perfectly ok to trigger spends from an account with insufficient
		// balance. If it is succeeded by a fund in due time, the RPCs will
		// successfully execute as soon as funds are available.

		// create a withdrawal message and signature that will pay for the RPC
		msg, sig := a.newSignedWithdrawal(payment, blockHeight+withdrawalValidityPeriod)

		// send PaymentRequest & PayByEphemeralAccountRequest
		pRequest := modules.PaymentRequest{Type: modules.PayByEphemeralAccount}
		pbcRequest := modules.PayByEphemeralAccountRequest{
			Message:   msg,
			Signature: sig,
		}
		err := modules.RPCWriteAll(stream, pRequest, pbcRequest)
		if err != nil {
			return types.ZeroCurrency, err
		}

		// receive PayByEphemeralAccountResponse
		var payByResponse modules.PayByEphemeralAccountResponse
		if err := modules.RPCRead(stream, &payByResponse); err != nil {
			return types.ZeroCurrency, err
		}

		// TODO: redo the eaErr thing
		var eaErr error
		if payByResponse.AccountManagerResponse != "ok" {
			eaErr = errors.New(payByResponse.AccountManagerResponse)
		}

		return payByResponse.Amount, eaErr
	})
}

// managedProcessFundIntent tracks the amount of money that is being funded,
// however is not yet processed and added to the balance on the host.
func (a *account) managedProcessFundIntent(amount types.Currency) {
	a.mu.Lock()
	a.pendingFunds = a.pendingFunds.Add(amount)
	a.mu.Unlock()
}

// managedProcessFundResult adjusts the account balance depending on the outcome
// of the call to fund the account on the host.
func (a *account) managedProcessFundResult(amount types.Currency, success bool) {
	a.mu.Lock()
	a.pendingFunds = a.pendingFunds.Sub(amount)
	if success {
		a.balance = a.balance.Add(amount)
	}
	a.mu.Unlock()

}

// managedProcessPaymentIntent tracks the amount of money that is being spent,
// however is not yet processed and deducted from the account's balance on the
// host.
func (a *account) managedProcessPaymentIntent(amount types.Currency) {
	a.mu.Lock()
	a.pendingSpends = a.pendingSpends.Add(amount)
	a.mu.Unlock()
}

// managedProcessPaymentResult adjusts the account balance depending on the
// outcome of the RPC call that spent money.
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
	// generate a nonce
	var nonce [modules.WithdrawalNonceSize]byte
	copy(nonce[:], fastrand.Bytes(len(nonce)))

	// create a new WithdrawalMessage
	wm := modules.WithdrawalMessage{
		Account: a.staticID,
		Expiry:  expiry,
		Amount:  amount,
		Nonce:   nonce,
	}

	// sign it
	sig := crypto.SignHash(crypto.HashObject(wm), a.staticSecretKey)
	return wm, sig
}
