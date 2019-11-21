package contractor

import (
	"errors"

	"gitlab.com/NebulousLabs/Sia/modules/renter/proto"
	"gitlab.com/NebulousLabs/Sia/types"
)

type Account interface {
	AccountBalance() (types.Currency, error)
	FundAccount(amount types.Currency) error
}

type hostAccount struct {
	hostKey    types.SiaPublicKey
	contractor *Contractor
	account    *proto.Account
}

func (c *Contractor) Account(hostKey types.SiaPublicKey) (Account, error) {
	c.mu.RLock()
	cID, cExists := c.pubKeysToContractID[hostKey.String()]
	account, aExists := c.accounts[contractID]
	c.mu.RUnlock()

	if !cExists {
		return nil, errors.New("No contract found for host")
	}
	if aExists {
		return account, nil
	}

	// Create account
	account = &hostAccount{
		hostKey:    hostKey,
		contractor: c,
		account:    c.staticContracts.NewAccount(contractID),
	}

	// Cache account
	c.mu.Lock()
	c.accounts[contractID] = account
	c.mu.Unlock()

	return account, nil
}

func (a *hostAccount) AccountBalance() (types.Currency, error) {
	return a.account.GetBalance()
}

func (a *hostAccount) FundAccount(amount types.Currency) error {
	return a.account.Fund(amount, a.contractor.blockHeight)
}
