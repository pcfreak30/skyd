package contractor

import (
	"errors"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/renter/proto"
	"gitlab.com/NebulousLabs/Sia/types"
)

// errors
var (
	errHostNotFound              = errors.New("host not found")
	errContractNotFound          = errors.New("contract not found")
	errContractInsufficientFunds = errors.New("contract has insufficient funds")
)

// paymentProvider contains a contract through which payment can be made. It
// will implement the RPCPaymentProvider interface and pay for these RPC calls
// using the underlying contract.
type paymentProvider struct {
	contractID  types.FileContractID
	contractSet *proto.ContractSet
}

// PaymentProvider returns a new PaymentProvider for the given host, it allows
// payments to be made from the contract the renter has with the host
func (c *Contractor) PaymentProvider(host types.SiaPublicKey) (modules.RPCPaymentProvider, error) {
	_, exists, err := c.hdb.Host(host)
	if !exists || err != nil {
		return nil, errHostNotFound
	}

	contract, exists := c.ContractByPublicKey(host)
	if !exists {
		return nil, errContractNotFound
	}

	return &paymentProvider{
		contractID:  contract.ID,
		contractSet: c.staticContracts,
	}, nil
}

// ProvidePaymentForRPC fulfills the RPCPaymentProvider interface. It uses the
// paymentProvider's underlying contract to make payment for an RPC call.
func (p *paymentProvider) ProvidePaymentForRPC(rpcID types.Specifier, cost types.Currency, stream modules.Stream, currentBlockHeight types.BlockHeight) (types.Currency, error) {
	// acquire a safe contract
	sc, exists := p.contractSet.Acquire(p.contractID)
	if !exists {
		return types.ZeroCurrency, errContractNotFound
	}
	defer p.contractSet.Return(sc)

	// verify the contract has enough funds
	metadata := sc.Metadata()
	if metadata.RenterFunds.Cmp(cost) < 0 {
		return types.ZeroCurrency, errContractInsufficientFunds
	}

	// create a new revision
	current := metadata.Transaction.FileContractRevisions[0]
	rev := newPaymentRevision(current, cost)

	// create transaction containing the revision
	signedTxn := types.NewTransaction(rev, 0)
	sig := sc.Sign(signedTxn.SigHash(0, currentBlockHeight))

	// TODO: do we want to record this intent? We have no way of knowing what
	// the money will be spent on eventually (download, upload, etc) but the
	// contract header should reflect that the money was 'spent' on something

	// record the intent to fund the ephemeral account
	walTxn, err := sc.CallRecordFundEphemeralAccountIntent(rev, cost)
	if err != nil {
		return types.ZeroCurrency, err
	}

	// send PaymentRequest & PayByContractRequest
	pRequest := modules.PaymentRequest{Type: modules.PayByContract}
	pbcRequest := modules.PayByContractRequest{
		Revision:  rev,
		Signature: sig,
	}
	if err := stream.WriteRequest(pRequest, pbcRequest); err != nil {
		return types.ZeroCurrency, err
	}

	// receive PayByContractResponse
	var payByResponse modules.PayByContractResponse
	if err := stream.ReadResponse(payByResponse); err != nil {
		return types.ZeroCurrency, err
	}

	// verify the host's signature
	hash := crypto.HashAll(pRequest, pbcRequest)
	var pk crypto.PublicKey
	copy(pk[:], metadata.HostPublicKey.Key)
	if err := crypto.VerifyHash(hash, pk, payByResponse.Signature); err != nil {
		return types.ZeroCurrency, errors.New("could not verify host's signature")
	}

	// commit the intent
	err = sc.CallCommitFundEphemeralAccountIntent(walTxn, signedTxn, cost)
	if err != nil {
		return types.ZeroCurrency, err
	}

	// TODO: currently we return amount funded. It is probably more useful to
	// the have the host return its current version of the ephemeral account
	// balance, this way the renter can check more easily if his version of the
	// balance is drifting and act accordingly

	return payByResponse.Amount, nil
}

// newPaymentRevision creates a copy of current with its revision number
// incremented, and with cost transferred from the renter to the host.
func newPaymentRevision(current types.FileContractRevision, cost types.Currency) types.FileContractRevision {
	rev := current

	// need to manually copy slice memory
	rev.NewValidProofOutputs = make([]types.SiacoinOutput, 2)
	rev.NewMissedProofOutputs = make([]types.SiacoinOutput, 3)
	copy(rev.NewValidProofOutputs, current.NewValidProofOutputs)
	copy(rev.NewMissedProofOutputs, current.NewMissedProofOutputs)

	// move valid payout from renter to host
	rev.NewValidProofOutputs[0].Value = current.NewValidProofOutputs[0].Value.Sub(cost)
	rev.NewValidProofOutputs[1].Value = current.NewValidProofOutputs[1].Value.Add(cost)

	// move missed payout from renter to void
	rev.NewMissedProofOutputs[0].Value = current.NewMissedProofOutputs[0].Value.Sub(cost)
	rev.NewMissedProofOutputs[2].Value = current.NewMissedProofOutputs[2].Value.Add(cost)

	// increment revision number
	rev.NewRevisionNumber++

	return rev
}
