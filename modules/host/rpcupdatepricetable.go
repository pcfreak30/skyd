package host

import (
	"encoding/json"
	"fmt"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

// managedRPCUpdatePriceTable handles the RPC request from the renter to fetch
// the host's latest RPC price table.
func (h *Host) managedRPCUpdatePriceTable(stream modules.Stream, pt modules.RPCPriceTable) (modules.RPCPriceTable, error) {
	// take a snapshot of the host's price table
	h.mu.RLock()
	updated := h.priceTable
	h.mu.RUnlock()

	// encode it as JSON and send it to the renter. Note that we send the price
	// table before we process payment. This allows the renter to close the
	// stream if it does not agree with the host's prices.
	encoded, err := json.Marshal(updated)
	if err != nil {
		return modules.RPCPriceTable{}, errors.AddContext(err, "Failed to JSON encode the price table")
	}

	uptResponse := modules.RPCUpdatePriceTableResponse{PriceTableJSON: encoded}
	if err := stream.WriteObjects(uptResponse); err != nil {
		return modules.RPCPriceTable{}, errors.AddContext(err, "Failed to write response")
	}

	// process payment for this RPC call.
	pp := h.PaymentProcessor()
	amountPaid, err := pp.ProcessPaymentForRPC(stream, updated)
	if err != nil {
		return modules.RPCPriceTable{}, errors.AddContext(err, "Failed to process payment")
	}

	// verify the renter payment was sufficient.
	expected := updated.Costs[modules.RPCUpdatePriceTable]
	if amountPaid.Cmp(expected) < 0 {
		return modules.RPCPriceTable{}, errors.AddContext(modules.ErrInsufficientPaymentForRPC, fmt.Sprintf("The renter did not supply sufficient payment to cover the cost of the  UpdatePriceTableRPC. Expected: %v Actual: %v", expected.HumanString(), amountPaid.HumanString()))
	}

	return updated, nil
}

// managedCalculateUpdatePriceTableRPCPrice calculates the price for the
// UpdatePriceTableRPC. The price can be dependant on numerous factors, for now
// this is a fixed cost equaling the base RPC price.
func (h *Host) managedCalculateUpdatePriceTableRPCPrice() types.Currency {
	hIS := h.InternalSettings()
	return hIS.MinBaseRPCPrice
}
