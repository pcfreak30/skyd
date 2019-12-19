package proto

import (
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

var (
	// errRPCNotAvailable is returned when the requested RPC is not found in the
	// host's price table
	errRPCNotAvailable = errors.New("RPC not avaiable on host")
)

type Account interface {
	ID() string
	HostKey() types.SiaPublicKey
	modules.PaymentProvider
}

// RPCClient interface lists all possible RPC that can be called on the host
type RPCClient interface {
	FundEphemeralAccount(a Account, amount types.Currency) (types.Currency, error)
}

// HostRPCClient wraps all necessities to communicate with a host
type HostRPCClient struct {
	connection modules.Connection

	priceTable *modules.PriceTable
	// TODO subscribe to consensus or have access to something that does
	currentBlockHeight types.BlockHeight
}

// NewRPCClient returns a new RPC client. The RPC client is in direct
// communication with the host, generally there should only be one client per
// host. It is capable of performing all of the RPCs that the host offers. Aside
// from this it is responsible for keeping the host's price table up-to-date.
func NewRPCClient(c modules.Connection) RPCClient {
	// the RPC price table is going to be decided by the host, it will
	// communicate it after the peermux is set up
	pt := &modules.PriceTable{}

	client := &HostRPCClient{
		connection: c,
		priceTable: pt,
	}
	// TODO background thread keeping the prices up-to-date

	return client
}

// FundEphemeralAccount calls the fundEphemeralAccountRPC on the host
func (c *HostRPCClient) FundEphemeralAccount(acc Account, amount types.Currency) (types.Currency, error) {
	rpcID := modules.RPCFundEphemeralAccount

	// Calculate the cost of the RPC
	cost, exists := c.priceTable.Costs[rpcID]
	if !exists {
		return types.ZeroCurrency, errors.AddContext(errRPCNotAvailable, "could not fund ephemeral account")
	}

	// Get a stream using the RPC ID
	stream := c.connection.Stream(rpcID)
	defer stream.Close()

	// send RPCFundEphemeralAccountRequest
	if err := stream.WriteObjects(modules.RPCFundEphemeralAccountRequest{
		AccountID: acc.ID(),
	}); err != nil {
		return types.ZeroCurrency, err
	}

	// provide payment and await response
	paid, err := acc.ProvidePaymentForRPC(rpcID, cost.Add(amount), stream, c.currentBlockHeight)
	if err != nil {
		return types.ZeroCurrency, err
	}

	// receive RPCFundEphemeralAccountResponse
	var fundAccResponse modules.RPCFundEphemeralAccountResponse
	if err := stream.ReadObject(fundAccResponse); err != nil {
		return types.ZeroCurrency, err
	}

	return paid, nil
}
