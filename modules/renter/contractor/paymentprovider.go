package contractor

import (
	"errors"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/renter/proto"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/siamux"
	"gitlab.com/NebulousLabs/writeaheadlog"
)

// errors
var (
	errHostNotFound              = errors.New("host not found")
	errContractNotFound          = errors.New("contract not found")
	errContractInsufficientFunds = errors.New("contract has insufficient funds")
)

// paymentProviderContract contains a contract through which payment can be
// made. It will implement the PaymentProvider interface and pay for these
// RPC calls using the underlying contract.
type paymentProviderContract struct {
	contractID  types.FileContractID
	contractSet *proto.ContractSet
}

// PaymentProvider returns a new PaymentProvider for the given host, it
// allows payments to be made from the contract the renter has with the host.
func (c *Contractor) PaymentProvider(host types.SiaPublicKey) (modules.PaymentProvider, error) {
	_, exists, err := c.hdb.Host(host)
	if !exists || err != nil {
		return nil, errHostNotFound
	}

	contract, exists := c.ContractByPublicKey(host)
	if !exists {
		return nil, errContractNotFound
	}

	return &paymentProviderContract{
		contractID:  contract.ID,
		contractSet: c.staticContracts,
	}, nil
}

// ProvidePaymentForRPC fulfills the PaymentProvider interface. It uses the
// paymentProvider's underlying contract to make payment for an RPC call.
func (p *paymentProviderContract) ProvidePaymentForRPC(rpcID types.Specifier, payment types.Currency, stream siamux.Stream, blockHeight types.BlockHeight) (types.Currency, error) {
	// acquire a safe contract
	sc, exists := p.contractSet.Acquire(p.contractID)
	if !exists {
		return types.ZeroCurrency, errContractNotFound
	}
	defer p.contractSet.Return(sc)

	// verify the contract has enough funds
	metadata := sc.Metadata()
	if metadata.RenterFunds.Cmp(payment) < 0 {
		return types.ZeroCurrency, errContractInsufficientFunds
	}

	// create a new revision
	current := metadata.Transaction.FileContractRevisions[0]
	rev := newPaymentRevision(current, payment)

	// create transaction containing the revision
	signedTxn := types.NewTransaction(rev, 0)
	sig := sc.Sign(signedTxn.SigHash(0, blockHeight))
	signedTxn.TransactionSignatures[0].Signature = sig[:]

	// record the payment intent
	walTxn, err := recordIntent(rpcID, sc, rev, payment)
	if err != nil {
		return types.ZeroCurrency, err
	}

	// send PaymentRequest & PayByContractRequest
	pRequest := modules.PaymentRequest{Type: modules.PayByContract}
	pbcRequest := buildPayByContractRequest(rev, sig)
	err = encoding.WriteObject(stream, pRequest)
	if err != nil {
		return types.ZeroCurrency, err
	}
	err = encoding.WriteObject(stream, pbcRequest)
	if err != nil {
		return types.ZeroCurrency, err
	}

	// receive PayByContractResponse
	var payByResponse modules.PayByContractResponse
	maxLen := uint64(modules.RPCMinLen)
	if err := encoding.ReadObject(stream, &payByResponse, maxLen); err != nil {
		return types.ZeroCurrency, err
	}

	// verify the host's signature
	// TODO
	hash := crypto.HashAll(rev)
	var pk crypto.PublicKey
	copy(pk[:], metadata.HostPublicKey.Key)
	if err := crypto.VerifyHash(hash, pk, payByResponse.Signature); err != nil {
		return types.ZeroCurrency, errors.New("could not verify host's signature")
	}
	// commit the intent
	err = commitIntent(rpcID, sc, walTxn, signedTxn, payment)
	if err != nil {
		return types.ZeroCurrency, err
	}

	return payment, nil
}

// TODO
func recordIntent(rpcID types.Specifier, sc *proto.SafeContract, rev types.FileContractRevision, amount types.Currency) (*writeaheadlog.Transaction, error) {
	switch rpcID {
	case modules.RPCFundEphemeralAccount:
		return sc.RecordFundEphemeralAccountIntent(rev, amount)
	}
	return nil, nil
}

// TODO
func commitIntent(rpcID types.Specifier, sc *proto.SafeContract, t *writeaheadlog.Transaction, signedTxn types.Transaction, amount types.Currency) error {
	switch rpcID {
	case modules.RPCFundEphemeralAccount:
		return sc.CommitFundEphemeralAccountIntent(t, signedTxn, amount)
	}
	return nil
}

// buildPayByContractRequest uses a revision and signature to build the
// PayBycontractRequest
func buildPayByContractRequest(rev types.FileContractRevision, sig crypto.Signature) modules.PayByContractRequest {
	var req modules.PayByContractRequest

	req.ContractID = rev.ID()
	req.NewRevisionNumber = rev.NewRevisionNumber
	req.NewValidProofValues = make([]types.Currency, len(rev.NewValidProofOutputs))
	for i, o := range rev.NewValidProofOutputs {
		req.NewValidProofValues[i] = o.Value
	}
	req.NewMissedProofValues = make([]types.Currency, len(rev.NewMissedProofOutputs))
	for i, o := range rev.NewMissedProofOutputs {
		req.NewMissedProofValues[i] = o.Value
	}
	req.Signature = sig[:]

	return req
}

// newPaymentRevision will make a new revision that transfers the funds from the
// renter to the host.
func newPaymentRevision(current types.FileContractRevision, payment types.Currency) types.FileContractRevision {
	rev := current

	// need to manually copy slice memory
	rev.NewValidProofOutputs = make([]types.SiacoinOutput, 2)
	rev.NewMissedProofOutputs = make([]types.SiacoinOutput, 3)
	copy(rev.NewValidProofOutputs, current.NewValidProofOutputs)
	copy(rev.NewMissedProofOutputs, current.NewMissedProofOutputs)

	// move valid payout from renter to host
	rev.NewValidProofOutputs[0].Value = current.NewValidProofOutputs[0].Value.Sub(payment)
	rev.NewValidProofOutputs[1].Value = current.NewValidProofOutputs[1].Value.Add(payment)

	// move missed payout from renter to void
	rev.NewMissedProofOutputs[0].Value = current.NewMissedProofOutputs[0].Value.Sub(payment)
	rev.NewMissedProofOutputs[2].Value = current.NewMissedProofOutputs[2].Value.Add(payment)

	// increment revision number
	rev.NewRevisionNumber++

	return rev
}
