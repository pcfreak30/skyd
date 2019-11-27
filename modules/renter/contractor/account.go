package contractor

import (
	"errors"

	"gitlab.com/NebulousLabs/Sia/modules/renter/proto"
	"gitlab.com/NebulousLabs/Sia/types"
)

// TODO: the account interface is duplicated, figure a clean way to dedupe it
// Account interface lists all possible actions which can be done on the
// renter's account on the host
type Account interface {
	AccountBalance() (types.Currency, error)
	FundAccount(amount types.Currency) error
}

// A hostAccount allows to interact with the renter's ephemeral account on a
// host. If money needs to be deposited into that account it will revise the
// file contract to do so.
type hostAccount struct {
	hostKey    types.SiaPublicKey
	accountKey types.SiaPublicKey
	contractID types.FileContractID
	client     *proto.RPCClient
	c          *Contractor
}

// Account returns a new hostAccount for the given host and account key
func (c *Contractor) Account(host, account types.SiaPublicKey) (Account, error) {
	h, exists, err := c.hdb.Host(host)
	if !exists || err != nil {
		return nil, errors.New("host not found")
	}

	contract, exists := c.ContractByPublicKey(host)
	if !exists {
		return nil, errors.New("contract not found")
	}

	client := proto.NewRPCClient(string(h.NetAddress))
	return &hostAccount{
		hostKey:    host,
		accountKey: account,
		contractID: contract.ID,
		client:     client,
		c:          c,
	}, nil
}

// AccountBalance returns the account balance
func (a *hostAccount) AccountBalance() (types.Currency, error) {
	contract, exists := a.c.staticContracts.Acquire(a.contractID)
	if !exists {
		return types.ZeroCurrency, errors.New("contract not present in contract set")
	}
	defer a.c.staticContracts.Return(contract)
	return a.client.EphemeralAccountBalance(a.accountKey.String(), contract, a.c.blockHeight)
}

// FundAccount will use the underlying contract to move money from the renter to
// the host, hereby funding the ephemeral account
func (a *hostAccount) FundAccount(amount types.Currency) error {
	contract, exists := a.c.staticContracts.Acquire(a.contractID)
	if !exists {
		return errors.New("contract not present in contract set")
	}
	defer a.c.staticContracts.Return(contract)
	return a.client.FundEphemeralAccount(a.accountKey.String(), contract, amount, a.c.blockHeight)
}
