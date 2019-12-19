package host

import (
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
)

// managedRPCFundEphemeralAccount is responsible for handling an RPC request
// from the renter to fund his ephemeral account.
func (h *Host) managedRPCFundEphemeralAccount(stream modules.Stream) error {
	// Grab some variables
	h.mu.RLock()
	hostKey := h.publicKey
	blockHeight := h.blockHeight
	h.mu.RUnlock()

	// Extract the payment
	pe := h.PaymentProcessor(hostKey)
	amount, done, err := pe.ProcessPaymentForRPC(stream, blockHeight)
	if err != nil {
		return errors.AddContext(err, "could not process payment")
	}

	// Commit the deposit when the FC is fsynced to disk
	go func() {
		err := <-done
		if err == nil {
			h.staticAccountManager.callCommitDeposit(amount)
		}
	}()

	// Fund the ephemeral account
	var fear modules.RPCFundEphemeralAccountRequest
	if err = stream.ReadObject(fear); err != nil {
		return errors.AddContext(err, "could not read request")
	}
	err = h.staticAccountManager.callDeposit(fear.AccountID, amount)
	if err != nil {
		return errors.AddContext(err, "could not fund the account")
	}

	return nil
}
