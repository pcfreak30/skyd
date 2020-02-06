package host

import (
	"time"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/siamux"
)

// managedRPCFundEphemeralAccount handles the RPC request from the renter to
// fund its ephemeral account.
func (h *Host) managedRPCFundEphemeralAccount(stream siamux.Stream, pt *modules.RPCPriceTable) error {
	// read the FundEphemeralAccountRequest
	var fear modules.RPCFundEphemeralAccountRequest
	maxLen := uint64(modules.RPCMinLen)
	if err := encoding.ReadObject(stream, &fear, maxLen); err != nil {
		return errors.AddContext(err, "Failed to read FundEphemeralAccountRequest")
	}

	// processing the RPC
	amount, err := h.staticPP.ProcessFundEphemeralAccountRPC(stream, *pt, fear.AccountID)
	if err != nil {
		return errors.AddContext(err, "Failed to fund ephemeral account")
	}

	// There's no need to verify payment here. The account get funded by the
	// amount paid minus the cost of the RPC. If the amount paid did not cover
	// the cost of the RPC, an error will have been returned.

	// create the receipt and sign it
	receipt := modules.Receipt{
		Host:      h.PublicKey(),
		Account:   fear.AccountID,
		Amount:    amount,
		Timestamp: time.Now().Unix(),
	}
	signature := crypto.SignHash(crypto.HashObject(receipt), h.secretKey)

	// send the FundEphemeralAccountResponse
	err = encoding.WriteObject(stream, modules.RPCFundEphemeralAccountResponse{
		Receipt:   receipt,
		Signature: signature[:],
	})
	if err := encoding.ReadObject(stream, &fear, maxLen); err != nil {
		return errors.AddContext(err, "Failed to send FundEphemeralAccountResponse")
	}

	return nil
}

// managedCalculateUpdatePriceTableRPCPrice calculates the price for the
// FundEphemeralAccountRPC. The price can be dependant on numerous factors.
// Note: for now this is a fixed cost equaling the base RPC price.
func (h *Host) managedCalculateFundEphemeralAccountPrice() types.Currency {
	hIS := h.InternalSettings()
	return hIS.MinBaseRPCPrice
}
