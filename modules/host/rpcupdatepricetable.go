package host

import (
	"encoding/json"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
)

// managedRPCUpdatePriceTable will calculate the host's new pricing table and
// send it in response to the caller's update request
func (h *Host) managedRPCUpdatePriceTable(s *modules.Session) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// build price table
	s.Costs = h.buildPriceTable()
	ptBytes, err := json.Marshal(s.Costs)
	if err != nil {
		return errors.Compose(s.WriteError(err), err)
	}

	// communicate it back to the caller
	if err := s.WriteResponse(&modules.RPCUpdatePriceTableResponse{
		PriceTableJSONEncoded: ptBytes,
	}); err != nil {
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

	// verify amount paid
	if amountPaid.Cmp(s.Costs.RPC[modules.NewRemoteRPCID(h.publicKey, modules.RPCUpdatePriceTable)]) < 0 {
		err = errors.New("insufficient payment")
		return errors.Compose(s.WriteError(err), err)
	}

	return nil
}
