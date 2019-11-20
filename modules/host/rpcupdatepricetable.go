package host

import (
	"encoding/json"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
)

// managedRPCUpdatePriceTable will calculate the host's new pricing table and
// send it in response to the caller's update request
func (h *Host) managedRPCUpdatePriceTable(pm *modules.PeerMux) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// build price table
	pm.Costs = h.buildPriceTable()
	ptBytes, err := json.Marshal(pm.Costs)
	if err != nil {
		return errors.Compose(pm.WriteError(err), err)
	}

	// communicate it back to the caller
	if err := pm.WriteResponse(&modules.RPCUpdatePriceTableResponse{
		PriceTableJSONEncoded: ptBytes,
	}); err != nil {
		return errors.Compose(pm.WriteError(err), err)
	}

	// extract payment
	accepted, amountPaid, err := h.extractPaymentForRPC(pm)
	if !accepted {
		err = errors.Compose(err, errors.New("payment was not accepted"))
	}
	if err != nil {
		return errors.Compose(pm.WriteError(err), err)
	}

	// verify amount paid
	if amountPaid.Cmp(pm.Costs.RPC[modules.NewRemoteRPCID(h.publicKey, modules.RPCUpdatePriceTable)]) < 0 {
		err = errors.New("insufficient payment")
		return errors.Compose(pm.WriteError(err), err)
	}

	return nil
}
