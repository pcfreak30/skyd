package proto

import (
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

type RPCClient struct {
	peerMux       *modules.PeerMux
	rpcPriceTable *modules.RPCPriceTable
}

func NewRPCClient(address string) *RPCClient {
	pm := modules.NewPeerMux(address)
	pt := &modules.RPCPriceTable{} // TODO: this 'll probably be set on the peermux itself right after the handshake was completed
	return &RPCClient{peerMux: pm, rpcPriceTable: pt}
}

// EphemeralAccountBalance calls the ephemeralAccountBalanceRPC on the host
func (c *RPCClient) EphemeralAccountBalance(accountID string, sc *SafeContract, currentBlockHeight types.BlockHeight) (types.Currency, error) {
	return types.ZeroCurrency, nil
}

// FundEphemeralAccount calls the fundEphemeralAccountRPC on the host
func (c *RPCClient) FundEphemeralAccount(accountID string, sc *SafeContract, amount types.Currency, currentBlockHeight types.BlockHeight) error {
	metadata := sc.Metadata()

	// lookup the host's RPC price
	rpcCost, err := c.getRPCCost(metadata.HostPublicKey, modules.RPCFundEphemeralAccount)
	if err != nil {
		return errors.AddContext(err, "RPC not available on host")
	}

	// verify the contract has enough money in it
	totalCost := rpcCost.Add(amount)
	if metadata.RenterFunds.Cmp(totalCost) < 0 {
		return errors.New("contract has insufficient funds")
	}

	// create a new revision
	current := metadata.Transaction.FileContractRevisions[0]
	rev := newPaymentRevision(current, totalCost)

	// create transaction containing the revision
	signedTxn := types.Transaction{
		FileContractRevisions: []types.FileContractRevision{rev},
		TransactionSignatures: []types.TransactionSignature{{
			ParentID:       crypto.Hash(rev.ParentID),
			CoveredFields:  types.CoveredFields{FileContractRevisions: []uint64{0}},
			PublicKeyIndex: 0, // renter key is always first -- see formContract
		}},
	}
	sig := sc.Sign(signedTxn.SigHash(0, currentBlockHeight))

	// send RPCFundEphemeralAccountRequest
	stream := c.peerMux.Stream(modules.RPCFundEphemeralAccount)
	if _, err := stream.Write(encoding.Marshal(modules.RPCFundEphemeralAccountRequest{
		AccountID: accountID,
	})); err != nil {
		return err
	}

	// record the intent (TODO: should we do this for accounts? should we then
	// also change the contract header to reflect this?)
	walTxn, err := sc.managedRecordFundAccountIntent(rev, amount)
	if err != nil {
		return err
	}

	// send PaymentRequest & PayByContractRequest
	if _, err := stream.Write(encoding.MarshalAll(modules.PaymentRequest{Type: modules.PayByContract},
		modules.PayByContractRequest{
			Revision:  rev,
			Signature: sig,
		})); err != nil {
		return err
	}

	// receive PayByContractResponse
	var payByResponse modules.PayByContractResponse
	if _, err := stream.Read(payByResponse); err != nil {
		return err
	}

	// receive RPCFundEphemeralAccountResponse (which is the receipt)
	var fundAccResponse modules.RPCFundEphemeralAccountResponse
	if _, err := stream.Read(fundAccResponse); err != nil {
		return err
	}

	// TODO verify both responses to see if payment was made. If successful we
	// want to commit the intent
	err = sc.managedCommitFundAccountIntent(walTxn, signedTxn, amount)
	if err != nil {
		return err
	}

	return nil
}

// getRPCCost returns the cost of the given RPC on the given host
func (c *RPCClient) getRPCCost(hostKey types.SiaPublicKey, rpcID types.Specifier) (types.Currency, error) {
	remoteRPCID := modules.NewRemoteRPCID(hostKey, rpcID)
	cost, ok := c.rpcPriceTable.Costs[remoteRPCID]
	if !ok {
		// TODO return errRPCNotAvailable
		return types.ZeroCurrency, nil
	}
	return cost, nil
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
