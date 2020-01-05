package host

import (
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
)

// managedRPCDownload2 is responsible for handling an RPC request
// from the renter to fund his ephemeral account.
func (h *Host) managedRPCDownload2(stream modules.Stream) error {
	// Grab some variables
	h.mu.RLock()
	blockHeight := h.blockHeight
	h.mu.RUnlock()

	// Extract the payment
	pp := h.PaymentProcessor()
	_, _, err := pp.ProcessPaymentForRPC(stream, blockHeight)

	// TODO implement download

	return errors.AddContext(err, "fund ephemeral account failed")
}
