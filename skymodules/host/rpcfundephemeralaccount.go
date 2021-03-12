package host

import (
	"time"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/siamux"
	"gitlab.com/skynetlabs/skyd/skymodules"
)

// managedRPCFundEphemeralAccount handles the RPC request from the renter to
// fund its ephemeral account.
func (h *Host) managedRPCFundEphemeralAccount(stream siamux.Stream) error {
	// read the price table
	pt, err := h.staticReadPriceTableID(stream)
	if err != nil {
		return errors.AddContext(err, "Failed to negotiate a valid price table")
	}

	// read the FundAccountRequest
	var far skymodules.FundAccountRequest
	err = skymodules.RPCRead(stream, &far)
	if err != nil {
		return errors.AddContext(err, "Could not read FundEphemeralAccountRequest")
	}

	// read the PaymentRequest and ensure it's a PayByContract request; for now
	// it does not make sense to fund an ephemeral account by anything but a
	// file contract - we might enable this in the future
	var pr skymodules.PaymentRequest
	err = skymodules.RPCRead(stream, &pr)
	if err != nil {
		return errors.AddContext(err, "Could not read PaymentRequest")
	}
	if pr.Type != skymodules.PayByContract {
		return errors.AddContext(skymodules.ErrInvalidPaymentMethod, "Funding an ephemeral account is done through PayByContract")
	}

	// fund the account
	funded, err := h.managedFundAccount(stream, far, pt.FundAccountCost)
	if err != nil {
		return errors.AddContext(err, "Funding ephemeral failed")
	}

	// There's no need to verify payment here. The account gets funded by the
	// amount paid minus the cost of the RPC. If the amount paid did not cover
	// the cost of the RPC, an error will have been returned.

	// create the receipt and sign it
	receipt := skymodules.Receipt{
		Host:      h.PublicKey(),
		Account:   far.Account,
		Amount:    funded,
		Timestamp: time.Now().Unix(),
	}
	signature := crypto.SignHash(crypto.HashObject(receipt), h.secretKey)

	// send the FundAccountResponse
	err = skymodules.RPCWrite(stream, skymodules.FundAccountResponse{
		Balance:   h.staticAccountManager.callAccountBalance(far.Account),
		Receipt:   receipt,
		Signature: signature,
	})
	if err != nil {
		return errors.AddContext(err, "Failed to send FundAccountResponse")
	}

	return nil
}
