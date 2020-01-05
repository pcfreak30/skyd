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
	blockHeight := h.blockHeight
	h.mu.RUnlock()

	// Extract the payment
	pp := h.PaymentProcessor()
	amount, so, err := pp.ProcessPaymentForRPC(stream, blockHeight)
	if so == nil {
		// TODO
	}

	// Fund the ephemeral account
	syncChan := make(chan struct{})
	var fear modules.RPCFundEphemeralAccountRequest
	if err = stream.ReadObject(fear); err != nil {
		return errors.AddContext(err, "could not read request")
	}
	err = h.staticAccountManager.callDeposit(fear.AccountID, amount, syncChan)
	if err != nil {
		return errors.AddContext(err, "could not fund the account")
	}

	// Update the storage obligation
	err = h.modifyStorageObligation(so.(storageObligation), nil, nil, nil)
	if err != nil {
		h.log.Fatal(err) // TODO improve
	}
	close(syncChan)

	return errors.AddContext(err, "RPC fund ephemeral account failed")
}
