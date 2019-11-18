package host

import (
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
)

// managedRPCFundEphemeralAccount will process a fund ephemeral account request,
// if successful it will deposit the amount of money received by extract payment
// from the RPC into the given ephemeral account
func (h *Host) managedRPCFundEphemeralAccount(s *modules.Session) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// read request
	var req modules.RPCFundEphemeralAccountRequest
	if err := s.ReadRequest(req, modules.RPCMinLen); err != nil {
		return errors.Compose(s.WriteError(err), err)
	}

	// extract payment
	accepted, amountPaid, err := h.extractPaymentForRPC(s)
	if !accepted {
		err = errors.Compose(err, errors.New("payment was not accepted"))
	}
	if err != nil {
		return errors.Compose(s.WriteError(err), err)
	}

	if err := h.staticAccountManager.callDeposit(req.AccountID, amountPaid); err != nil {
		return errors.Compose(s.WriteError(err), err)
	}

	// TODO build receipt and communicate it back to the caller

	return nil
}
