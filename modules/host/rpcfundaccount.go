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
	return errors.AddContext(err, "Could not fund ephemeral account")
}

// managedCalculateFundEphemeralAccountRPCPrice calculates the price for the
// FundEphemeralAccountRPC. The price can be dependant on numerous factors, for
// now this is a fixed cost equaling the base RPC price.
func (h *Host) managedCalculateFundEphemeralAccountRPCPrice() types.Currency {
	hIS := h.InternalSettings()
	return hIS.MinBaseRPCPrice
}
