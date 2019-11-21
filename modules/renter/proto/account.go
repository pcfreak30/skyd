package proto

import (
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

type Account struct {
	id         string
	secretKey  crypto.SecretKey
	cs         *ContractSet
	contractID types.FileContractID
}

func (cs *ContractSet) NewAccount(contractID types.FileContractID) *Account {
	sk, pk := crypto.GenerateKeyPair()
	accountPubKey := types.SiaPublicKey{
		Algorithm: types.SignatureEd25519,
		Key:       pk[:],
	}

	return &Account{
		id:         accountPubKey.String(),
		secretKey:  sk,
		cs:         cs,
		contractID: contractID,
	}
}

// GetBalance returns the ephemeral account balance
func (a *Account) GetBalance() (types.Currency, error) {
	return types.ZeroCurrency, nil // TODO
}

// Fund funds the ephemeral account
func (a *Account) Fund(amount types.Currency, currentBlockHeight types.BlockHeight) error {
	// acquire a lock on the contract
	sc, acquired := a.cs.Acquire(a.contractID)
	if !acquired {
		return errors.New("coudl not find file contract")
	}
	defer a.cs.Return(sc)

	// verify the contract has enough money in it to make the payment
	metadata := sc.Metadata()
	if metadata.RenterFunds.Cmp(amount) < 0 {
		return errors.New("contract has insufficient funds")
	}

	// create a new revision
	current := metadata.Transaction.FileContractRevisions[0]
	rev := newPaymentRevision(current, amount)

	// create transaction containing the revision
	signedTxn := types.Transaction{
		FileContractRevisions: []types.FileContractRevision{rev},
		TransactionSignatures: []types.TransactionSignature{{
			ParentID:       crypto.Hash(rev.ParentID),
			CoveredFields:  types.CoveredFields{FileContractRevisions: []uint64{0}},
			PublicKeyIndex: 0, // renter key is always first -- see formContract
		}},
	}
	// TODO signature := sc.Sign(signedTxn.SigHash(0, currentBlockHeight))

	// TODO send RPCFundEphemeralAccountRequest
	walTxn, err := sc.managedRecordFundAccountIntent(rev, amount)
	if err != nil {
		return err
	}

	// TODO send PaymentRequest
	// TODO send PayByContractRequest
	err = sc.managedCommitFundAccountIntent(walTxn, signedTxn, amount)
	if err != nil {
		return err
	}

	return nil
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
