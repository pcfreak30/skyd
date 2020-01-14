package host

import (
	"fmt"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/bolt"
	"gitlab.com/NebulousLabs/errors"
)

var errUnknownPaymentMethod = errors.New("unknown payment method")
var errInvalidPaymentMethod = errors.New("invalid payment method")

// paymentProcessor fulfills the PaymentProcessor interface on the host. It is
// used by the RPCs to extract a payment from the stream. Once payment is
// extracted the RPC can continue processing.
type paymentProcessor struct {
	staticSecretKey crypto.SecretKey
	h               *Host
}

// PaymentProcessor returns a new PaymentProcessor.
func (h *Host) PaymentProcessor() modules.PaymentProcessor {
	return &paymentProcessor{
		staticSecretKey: h.secretKey,
		h:               h,
	}
}

// ProcessPaymentForRPC reads a payment request from the stream, depending on
// the type of payment it will either update the file contract or call upon the
// ephemeral account manager to process the payment.
func (p *paymentProcessor) ProcessPaymentForRPC(stream modules.Stream, priceTable modules.RPCPriceTable) (types.Currency, error) {
	// read the PaymentRequest
	var pr modules.PaymentRequest
	if err := stream.ReadObject(pr); err != nil {
		return failTo("ProcessPaymentForRPC", "read PaymentRequest", err)
	}

	// process payment depending on the payment method
	switch pr.Type {
	case modules.PayByContract:
		// read the PayByContractRequest
		var pbcr modules.PayByContractRequest
		if err := stream.ReadObject(pbcr); err != nil {
			return failTo("ProcessPaymentForRPC", "read PayByContractRequest", err)
		}

		// lock the storage obligation
		so, currentRevision, _, err := p.lockStorageObligation(pbcr.ContractID)
		defer p.h.managedUnlockStorageObligation(so.id())
		if err != nil {
			return failTo("ProcessPaymentForRPC", "lock storage obligation", err)
		}

		// extract the proposed revision and the signature from the request
		// object, using the existing revision
		renterRevision := revisionFromRequest(currentRevision, pbcr)
		renterSignature := signatureFromRequest(currentRevision, pbcr)

		// sign the revision
		blockHeight := p.h.BlockHeight()
		txn, err := createRevisionSignature(renterRevision, renterSignature, p.staticSecretKey, blockHeight)
		if err != nil {
			return failTo("ProcessPaymentForRPC", "verify revision", err)
		}

		// extract the payment output & update the storage obligation with the
		// host's signature
		amount := currentRevision.NewValidProofOutputs[0].Value.Sub(renterRevision.NewValidProofOutputs[0].Value)
		so.RevisionTransactionSet = []types.Transaction{{
			FileContractRevisions: []types.FileContractRevision{renterRevision},
			TransactionSignatures: []types.TransactionSignature{renterSignature, txn.TransactionSignatures[1]},
		}}

		// TODO: update so finincial metrics

		// update the storage obligation
		err = p.h.modifyStorageObligation(so, nil, nil, nil)
		if err != nil {
			return failTo("ProcessPaymentForRPC", "modify storage obligation", err)
		}

		return amount, nil
	case modules.PayByEphemeralAccount:
		// read the PayByEphemeralAccountRequest
		var pbear modules.PayByEphemeralAccountRequest
		if err := stream.ReadObject(pbear); err != nil {
			return failTo("ProcessPaymentForRPC", "read PayByEphemeralAccountRequest", err)
		}

		// process the request
		err := p.h.staticAccountManager.callWithdraw(&pbear.Message, pbear.Signature, pbear.Priority)
		if err != nil {
			return failTo("ProcessPaymentForRPC", "withdraw from ephemeral account", err)
		}

		return pbear.Message.Amount, nil
	default:
		return failTo("ProcessPaymentForRPC", "handle payment method", errUnknownPaymentMethod)
	}
}

// ProcessFundEphemeralAccountRPC handles the special case where an ephemeral
// account is funded by making payment through a contract. This is a special
// case because it requires some coordination between the FC fsync and the EA
// fsync. See callDeposit in accountmanager.go for more details.
func (p *paymentProcessor) ProcessFundEphemeralAccountRPC(stream modules.Stream, priceTable modules.RPCPriceTable) (types.Currency, error) {
	// read the PaymentRequest
	var pr modules.PaymentRequest
	if err := stream.ReadObject(pr); err != nil {
		return failTo("ProcessFundEphemeralAccountRPC", "read PaymentRequest", err)
	}

	// ensure it's a PayByContract request
	if pr.Type != modules.PayByContract {
		return failTo("ProcessFundEphemeralAccountRPC", "handle payment method", errInvalidPaymentMethod)
	}

	// read the PayByContractRequest
	var pbcr modules.PayByContractRequest
	if err := stream.ReadObject(pbcr); err != nil {
		return failTo("ProcessFundEphemeralAccountRPC", "read PayByContractRequest", err)
	}

	// read the FundEphemeralAccountRequest
	var fear modules.RPCFundEphemeralAccountRequest
	if err := stream.ReadObject(fear); err != nil {
		return failTo("ProcessFundEphemeralAccountRPC", "read FundEphemeralAccountRequest", err)
	}

	// lock the storage obligation
	so, currentRevision, _, err := p.lockStorageObligation(pbcr.ContractID)
	defer p.h.managedUnlockStorageObligation(so.id())
	if err != nil {
		return failTo("ProcessFundEphemeralAccountRPC", "lock storage obligation", err)
	}

	// extract the proposed revision and the signature from the request
	// object, using the existing revision
	renterRevision := revisionFromRequest(currentRevision, pbcr)
	renterSignature := signatureFromRequest(currentRevision, pbcr)

	// sign the revision
	blockHeight := p.h.BlockHeight()
	txn, err := createRevisionSignature(renterRevision, renterSignature, p.staticSecretKey, blockHeight)
	if err != nil {
		return failTo("ProcessFundEphemeralAccountRPC", "verify revision", err)
	}

	amount := currentRevision.NewValidProofOutputs[0].Value.Sub(renterRevision.NewValidProofOutputs[0].Value)

	// create a sync chan to pass to the account manager, once the FC is fully
	// fsynced we'll close this so the account manager can properly lower the
	// host's outstanding risk induced by the (immediate) deposit.
	syncChan := make(chan struct{})
	err = p.h.staticAccountManager.callDeposit(fear.AccountID, amount, syncChan)
	if err != nil {
		return failTo("ProcessFundEphemeralAccountRPC", "verify revision", err)
	}

	// update the storage obligation
	so.RevisionTransactionSet = []types.Transaction{{
		FileContractRevisions: []types.FileContractRevision{renterRevision},
		TransactionSignatures: []types.TransactionSignature{renterSignature, txn.TransactionSignatures[1]},
	}}

	// TODO: update so finincial metrics

	err = p.h.modifyStorageObligation(so, nil, nil, nil)
	if err != nil {
		p.h.log.Fatalf("Host incurred a loss of %v, ephemeral account funded but could not modify storage obligation, error %v", amount.HumanString(), err)
	}
	close(syncChan)

	return amount, nil
}

// lockStorageObligation will call upon the host to lock the storage obligation
// for given ID. It returns the most recent revision and its signatures.
func (p *paymentProcessor) lockStorageObligation(fcid types.FileContractID) (so storageObligation, recentRevision types.FileContractRevision, revisionSigs []types.TransactionSignature, err error) {
	p.h.managedLockStorageObligation(fcid)

	// fetch the storage obligation, which has the revision, which has the
	// renter's public key.
	p.h.mu.RLock()
	defer p.h.mu.RUnlock()
	err = p.h.db.View(func(tx *bolt.Tx) error {
		so, err = getStorageObligation(tx, fcid)
		return err
	})
	if err != nil {
		err = extendErr("could not fetch "+fcid.String()+": ", ErrorInternal(err.Error()))
		return storageObligation{}, types.FileContractRevision{}, nil, err
	}

	// pull out the file contract revision and the revision's signatures from
	// the transaction.
	revisionTxn := so.RevisionTransactionSet[len(so.RevisionTransactionSet)-1]
	recentRevision = revisionTxn.FileContractRevisions[0]
	for _, sig := range revisionTxn.TransactionSignatures {
		// checking for just the parent id is sufficient, an over-signed file
		// contract is invalid.
		if sig.ParentID == crypto.Hash(fcid) {
			revisionSigs = append(revisionSigs, sig)
		}
	}

	return
}

// revisionFromRequest creates a copy of current and fills in the suggested
// revision values provided through the request object.
func revisionFromRequest(current types.FileContractRevision, pbcr modules.PayByContractRequest) types.FileContractRevision {
	rev := current

	rev.NewRevisionNumber = pbcr.NewRevisionNumber
	rev.NewValidProofOutputs = make([]types.SiacoinOutput, len(pbcr.NewValidProofValues))
	for i, v := range pbcr.NewValidProofValues {
		rev.NewValidProofOutputs[i] = types.SiacoinOutput{
			Value:      v,
			UnlockHash: current.NewValidProofOutputs[i].UnlockHash,
		}
	}

	rev.NewMissedProofOutputs = make([]types.SiacoinOutput, len(pbcr.NewMissedProofValues))
	for i, v := range pbcr.NewMissedProofValues {
		rev.NewMissedProofOutputs[i] = types.SiacoinOutput{
			Value:      v,
			UnlockHash: current.NewMissedProofOutputs[i].UnlockHash,
		}
	}

	return rev
}

// signatureFromRequest creates a copy of current and fills in the suggested
// revision values provided through the request object.
func signatureFromRequest(rev types.FileContractRevision, pbcr modules.PayByContractRequest) types.TransactionSignature {
	txn := types.NewTransaction(rev, 0)
	txn.TransactionSignatures[0].Signature = pbcr.Signature
	return txn.TransactionSignatures[0]
}

// failTo is a helper function that provides consistent context to the given
// error.
func failTo(method, step string, err error) (types.Currency, error) {
	return types.ZeroCurrency, errors.AddContext(err, fmt.Sprintf("Failed to %s, could not %s", method, step))
}
