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

	// Process the request
	pp := h.PaymentProcessor()
	_, err := pp.ProcessFundEphemeralAccountRPC(stream, blockHeight)
	return errors.AddContext(err, "Could not fund ephemeral account")
}
