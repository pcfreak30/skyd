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
	modules.RPCPaymentProvider
}

// RPCClient interface lists all possible RPC that can be called on the host
type RPCClient interface {
	FundEphemeralAccount(a Account, amount types.Currency) (types.Currency, error)
}

// HostRPCClient wraps all necessities to communicate with a host
type HostRPCClient struct {
	connection         modules.Connection
	priceTable         *modules.RPCPriceTable
	currentBlockHeight types.BlockHeight // TODO subscribe to consensus or have access to something that does
}

// NewRPCClient returns a new RPC client. The RPC client is in direct
// communication with the host, generally there should only be one client per
// host. It is capable of performing all of the RPCs that the host offers. Aside
// from this it is responsible for keeping the host's price table up-to-date.
func NewRPCClient(c modules.Connection) RPCClient {
	// the RPC price table is going to be decided by the host, it will
	// communicate it after the peermux is set up
	pt := &modules.RPCPriceTable{}

	// TODO background thread keeping the prices up-to-date

	return &HostRPCClient{
		connection: c,
		priceTable: pt,
	}
}

// FundEphemeralAccount calls the fundEphemeralAccountRPC on the host
func (c *HostRPCClient) FundEphemeralAccount(acc Account, amount types.Currency) (types.Currency, error) {
	rpcID := modules.RPCFundEphemeralAccount

	// Find the cost of the RPC
	cost, err := c.costOfRPC(acc.HostKey(), rpcID)
	if err != nil {
		return types.ZeroCurrency, errors.AddContext(err, "RPC not available on host")
	}

	// Get a stream using the RPC ID
	stream := c.connection.Stream(rpcID)
	defer stream.Close()

	// send RPCFundEphemeralAccountRequest
	if err := stream.WriteRequest(modules.RPCFundEphemeralAccountRequest{
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
	if err := stream.ReadResponse(fundAccResponse); err != nil {
		return types.ZeroCurrency, err
	}

	return paid, nil
}

// costOfRPC uses the RPC table to figure out the cost of the given RPC on the
// host with hostKey
func (c *HostRPCClient) costOfRPC(hostKey types.SiaPublicKey, rpcID types.Specifier) (types.Currency, error) {
	remoteRPCID := modules.NewRemoteRPCID(hostKey, rpcID)
	cost, ok := c.priceTable.Costs[remoteRPCID]
	if !ok {
		return types.ZeroCurrency, errRPCNotAvailable
	}
	return cost, nil
}
