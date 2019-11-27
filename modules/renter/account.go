package renter

import (
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
)

// Account interface lists all possible actions which can be done on the
// renter's account on the host
type Account interface {
	AccountBalance() (types.Currency, error)
	FundAccount(amount types.Currency) error
}

// account represents a renter's ephemeral account on a host
type account struct {
	hostKey    types.SiaPublicKey
	accountKey types.SiaPublicKey
	secretKey  crypto.SecretKey
	r          *Renter
}

// newAccount returns a new account for the given host
func (r *Renter) newAccount(hostKey types.SiaPublicKey) Account {
	sk, pk := crypto.GenerateKeyPair()
	spk := types.SiaPublicKey{
		Algorithm: types.SignatureEd25519,
		Key:       pk[:],
	}
	return &account{
		hostKey:    hostKey,
		accountKey: spk,
		secretKey:  sk,
		r:          r,
	}
}

// AccountBalance returns the current account balance
func (a *account) AccountBalance() (types.Currency, error) {
	// TODO fetching the account balance costs money, we can pay for this by
	// using a contract, or using an ephemeral account. For now we assume we
	// always use a file contract to make payments
	acc, err := a.r.hostContractor.Account(a.hostKey, a.accountKey)
	if err != nil {
		return types.ZeroCurrency, err
	}
	return acc.AccountBalance()
}

// FundAccount will deposit given amount into the account
func (a *account) FundAccount(amount types.Currency) error {
	// TODO funding the account balance requires money, we can pay for this by
	// using a contract, or using an ephemeral account. For now we assume we
	// always use a file contract to make payments
	acc, err := a.r.hostContractor.Account(a.hostKey, a.accountKey)
	if err != nil {
		return err
	}
	return acc.FundAccount(amount)
}
