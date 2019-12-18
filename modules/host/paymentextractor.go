package host

import (
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/sync"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/bolt"
	"gitlab.com/NebulousLabs/errors"
)

var errUnknownPaymentMethod = errors.New("unkown payment method")

// paymentExtractor fulfills the PaymentExtractor interface on the host. It is
// used by the RPCs to extract a payment from the stream. Once payment is
// extracted the RPC can continue processing.
type paymentExtractor struct {
	am            accountManager
	hostSecretKey crypto.SecretKey

	tg *sync.ThreadGroup
	h  *Host
}

// PaymentExtractor returns a new PaymentExtractor.
func (h *Host) PaymentExtractor(host types.SiaPublicKey) modules.PaymentExtractor {
	return &paymentExtractor{
		am:            h.staticAccountManager,
		hostSecretKey: h.secretKey,
		tg:            &h.tg,
		h:             h,
	}
}

// ExtractPaymentForRPC reads a payment request from the stream, depending on
// the type of payment it will either update the file contract revision or call
// upon the ephemeral account manager to process the payment.
func (p *paymentExtractor) ExtractPaymentForRPC(stream modules.Stream, currentBlockHeight types.BlockHeight) (amount types.Currency, accepted bool, err error) {
	// Read the PaymentRequest
	var pr modules.PaymentRequest
	if err = stream.ReadObject(pr); err != nil {
		return
	}

	// Process payment depending on the payment method
	switch pr.Type {
	case modules.PayByContract:
		// Read the PayByContractRequest
		var pbcr modules.PayByContractRequest
		if err = stream.ReadObject(pbcr); err != nil {
			return
		}

		// Process the request
		accepted = true
		amount, err = p.payByContract(pbcr, currentBlockHeight)
	case modules.PayByEphemeralAccount:
		// Read the PayByEphemeralAccountRequest
		var pbear modules.PayByEphemeralAccountRequest
		if err = stream.ReadObject(pbear); err != nil {
			return
		}

		// Process the request
		accepted = true
		amount, err = p.payByEphemeralAccount(pbear)
	default:
		err = errUnknownPaymentMethod
	}

	return
}

// payByContract processes the payment request by verifying the renter's payment
// revision. If accepted it will modify the storage obligation. Note that this
// happens in a different thread to allow immediate release of funds, without
// having to wait for the FC fsync.
func (p *paymentExtractor) payByContract(req modules.PayByContractRequest, cbh types.BlockHeight) (types.Currency, error) {
	// Lock the storage obligation
	so, currentRevision, _, err := p.lockStorageObligation(req.ContractID)
	defer p.h.managedUnlockStorageObligation(req.ContractID)
	if err != nil {
		err = errors.AddContext(err, "Could not lock storage obligation")
		return types.ZeroCurrency, err
	}

	// Extract the proposed revision and the signature from the request
	// object, using the existing revision
	renterRevision := revisionFromRequest(currentRevision, req)
	renterSignature := signatureFromRequest(currentRevision, req)

	// Sign the revision
	txn, err := createRevisionSignature(renterRevision, renterSignature, p.hostSecretKey, cbh)
	if err != nil {
		err = errors.AddContext(err, "Could not verify revision")
		return types.ZeroCurrency, err
	}

	// Update the storage obligation.
	so.RevisionTransactionSet = []types.Transaction{{
		FileContractRevisions: []types.FileContractRevision{renterRevision},
		TransactionSignatures: []types.TransactionSignature{renterSignature, txn.TransactionSignatures[1]},
	}}

	amount := currentRevision.NewValidProofOutputs[0].Value.Sub(renterRevision.NewValidProofOutputs[0].Value)

	// Modify the storage obligation in a separate thread. This allows payment
	// to be processed and funds to become available immediately.
	go p.threadedModifyStorageObligation(so, amount)

	return amount, nil
}

// payByEphemeralAccount process the payment request by calling the account
// manager. The account manager will try to withdraw the request amount from the
// renter's ephemeral account balance.
func (p *paymentExtractor) payByEphemeralAccount(req modules.PayByEphemeralAccountRequest) (types.Currency, error) {
	err := p.am.callWithdraw(req.Message, req.Signature, req.Priority)
	if err != nil {
		return types.ZeroCurrency, err
	}
	return req.Message.Amount, nil
}

// threadedModifyStorageObligation modifies the given storage obligation.
func (p *paymentExtractor) threadedModifyStorageObligation(so storageObligation, amount types.Currency) {
	err := p.tg.Add()
	if err != nil {
		return
	}
	defer p.tg.Done()

	// TODO: we used to update the so.XRevenue field - we can't any more
	// because we don't know yet what the payment is for

	p.h.mu.Lock()
	defer p.h.mu.Unlock()

	err = p.h.modifyStorageObligation(so, nil, nil, nil)
	if err != nil {
		// TODO: update the ephemeral account to reflect this failure
		p.h.log.Fatal("Could not modify storage obligation", err)
	}
}

// lockStorageObligation will call upon the host to lock the storage obligation
// for given ID. It returns the most recent revision and its signatures.
func (p *paymentExtractor) lockStorageObligation(fcid types.FileContractID) (so storageObligation, recentRevision types.FileContractRevision, revisionSigs []types.TransactionSignature, err error) {
	p.h.managedLockStorageObligation(fcid)

	// Fetch the storage obligation, which has the revision, which has the
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

	// Pull out the file contract revision and the revision's signatures from
	// the transaction.
	revisionTxn := so.RevisionTransactionSet[len(so.RevisionTransactionSet)-1]
	recentRevision = revisionTxn.FileContractRevisions[0]
	for _, sig := range revisionTxn.TransactionSignatures {
		// Checking for just the parent id is sufficient, an over-signed file
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
