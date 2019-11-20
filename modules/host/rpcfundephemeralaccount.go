package host

import (
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
)

// managedRPCFundEphemeralAccount will process a fund ephemeral account request,
// if successful it will deposit the amount of money received by extract payment
// from the RPC into the given ephemeral account
func (h *Host) managedRPCFundEphemeralAccount(pm *modules.PeerMux) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// read request
	var req modules.RPCFundEphemeralAccountRequest
	if err := pm.ReadRequest(req, modules.RPCMinLen); err != nil {
		return errors.Compose(pm.WriteError(err), err)
	}

	// extract payment
	accepted, amountPaid, err := h.extractPaymentForRPC(pm)
	if !accepted {
		err = errors.Compose(err, errPaymentNotAccepted)
	}
	if err != nil {
		return errors.AddContext(errors.Compose(pm.WriteError(err), err), "failed extracting payment for RPC")
	}

	// fund the ephemeral account
	if err := h.staticAccountManager.callDeposit(req.AccountID, amountPaid); err != nil {
		return errors.AddContext(errors.Compose(pm.WriteError(err), err), "failed funding ephemeral account"
	}

	// TODO build receipt and communicate it back to the caller

	return nil
}
