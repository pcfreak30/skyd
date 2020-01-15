package host

import (
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

// managedRPCFundEphemeralAccount is responsible for handling an RPC request
// from the renter to fund his ephemeral account.
func (h *Host) managedRPCFundEphemeralAccount(stream *modules.Stream, pt modules.RPCPriceTable) error {
	pp := h.PaymentProcessor()
	_, err := pp.ProcessFundEphemeralAccountRPC(stream, pt)

	// There's no need to verify payment here. The account get funded by the
	// amount paid minus the cost of the RPC. If the amount paid did not cover
	// the cost of the RPC, an error will have been returned.

	return errors.AddContext(err, "Failed to fund ephemeral account")
}

// managedCalculateFundEphemeralAccountRPCPrice calculates the price for the
// FundEphemeralAccountRPC. The price can be dependant on numerous factors, for
// now this is a fixed cost equaling the base RPC price.
func (h *Host) managedCalculateFundEphemeralAccountRPCPrice() types.Currency {
	hIS := h.InternalSettings()
	return hIS.MinBaseRPCPrice
}
