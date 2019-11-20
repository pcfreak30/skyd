package host

import (
	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

var (
	errPaymentInvalidMethod = errors.New("invalid payment method, valid methods are 'PayByContract' or 'PayByEphemeralAcc'")
	errPaymentInsufficient  = errors.New("payment was insufficient")
	errPaymentNotAccepted   = errors.New("payment was not accepted")

	// priceTableExpiry defines for how many blocks the price table remains
	// valid (TODO: make host setting (?))
	priceTableExpiry = build.Select(build.Var{
		Dev:      6,
		Standard: 144,
		Testing:  3,
	}).(types.BlockHeight)
)

// buildPriceTable builds the host's price table containing a price for
// every the RPCs on offer
func (h *Host) buildPriceTable() modules.PriceTable {
	prices := make(map[modules.RemoteRPCID]types.Currency)

	// Add cost of updating the rpc price table
	costUPT := modules.NewRemoteRPCID(h.publicKey, modules.RPCUpdatePriceTable)
	prices[costUPT] = h.settings.MinBaseRPCPrice

	return modules.PriceTable{
		RPC:    prices,
		Expiry: h.blockHeight + priceTableExpiry,
	}
}

// ExtractPaymentForRPC will read from the peermux how the caller wants to pay
// and then process that payment. When successful it will return how much has
// been paid so the caller can continue processing the RPC. If unsuccessful the
// RPC will abort
func (h *Host) extractPaymentForRPC(pm *modules.PeerMux) (accepted bool, amountPaid types.Currency, err error) {
	if pm.Closed {
		return false, types.ZeroCurrency, modules.ErrPeerMuxClosed
	}

	var req modules.PaymentRequest
	if err := pm.ReadRequest(&req, modules.RPCMaxLen); err != nil {
		return true, types.ZeroCurrency, err
	}

	switch req.Type {
	case modules.PayByContract:
		var payByReq modules.PayByContractRequest
		if err := pm.ReadRequest(&payByReq, modules.RPCMaxLen); err != nil {
			return true, types.ZeroCurrency, err
		}
		acc, ap, err := h.payByContract(pm, &payByReq)
		return acc, ap, err
	case modules.PayByEphemeralAccount:
		var payByReq modules.PayByEphemeralAccountRequest
		if err := pm.ReadRequest(&payByReq, modules.RPCMaxLen); err != nil {
			return true, types.ZeroCurrency, err
		}
		acc, ap, err := h.payByEphemeralAccount(pm, &payByReq)
		return acc, ap, err
	default:
		return true, types.ZeroCurrency, errPaymentInvalidMethod
	}

}

// payByEphemeralAccount will call upon the ephemeral account manager and try to
// perform the withdrawal
func (h *Host) payByEphemeralAccount(s *modules.PeerMux, req *modules.PayByEphemeralAccountRequest) (accepted bool, amountPaid types.Currency, err error) {
	if err := h.staticAccountManager.callWithdraw(transformToLocalWithdrawMessage(&req.Message), req.Signature); err != nil {
		return true, types.ZeroCurrency, err
	}
	return true, req.Message.Amount, nil
}

// payByContract will process the payByContractRequest
func (h *Host) payByContract(s *modules.PeerMux, req *modules.PayByContractRequest) (accepted bool, amountPaid types.Currency, err error) {
	return true, types.ZeroCurrency, nil // TODO
}

// TODO: probably want to get rid of this and use the shared WithdrawalMessage
// from the modules package. Transform for now to have less merge conflicts.
func transformToLocalWithdrawMessage(w *modules.WithdrawalMessage) *withdrawalMessage {
	return &withdrawalMessage{
		account: w.Id.String(),
		amount:  w.Amount,
		expiry:  w.Expiry,
		nonce:   uint64(w.Nonce),
	}
}
