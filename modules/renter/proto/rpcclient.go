package proto

import (
	"encoding/json"
	"sync"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/threadgroup"
)

// TODO wondering if it is better to move this file to the renter package. That
// was an RPC client can be easier initialised from a renter. It is currently
// part of the proto package only because it directly involves the protocol and
// is quite low-level.

var (
	// errRPCNotAvailable is returned when the requested RPC is not found in the
	// host's price table
	errRPCNotAvailable = errors.New("RPC not avaiable on host")
)

// RPCClient interface lists all possible RPC that can be called on the host
type RPCClient interface {
	UpdatePriceTable() error
	FundEphemeralAccount(id string, amount types.Currency) (types.Currency, error)
}

// HostRPCClient wraps all necessities to communicate with a host
type HostRPCClient struct {
	connection           modules.Connection
	paymentProvider      modules.PaymentProvider
	priceTable           *modules.PriceTable
	priceTableLastUpdate types.BlockHeight
	blockHeight          types.BlockHeight
	log                  *persist.Logger
	tg                   *threadgroup.ThreadGroup
	mu                   sync.Mutex
}

// NewRPCClient returns a new RPC client. The RPC client is in direct
// communication with the host, generally there should only be one client per
// host. It is capable of performing all of the RPCs that the host offers. Aside
// from this it is responsible for keeping the host's price table up-to-date.
func NewRPCClient(c modules.Connection, pp modules.PaymentProvider, cbh types.BlockHeight, tg *threadgroup.ThreadGroup, log *persist.Logger) (RPCClient, error) {
	client := &HostRPCClient{
		connection:      c,
		paymentProvider: pp,
		blockHeight:     cbh,
		log:             log,
		tg:              tg,
	}

	err := client.UpdatePriceTable()
	if err != nil {
		return nil, err
	}

	client.priceTableLastUpdate = cbh
	return client, nil
}

// UpdateBlockHeight is called right after the renter has processed a consensus
// change. It will send the latest blockheight to all its rpc clients so they
// can update their host RPC pricing if necessary. This will depend on the
// expiry set by the host.
func (c *HostRPCClient) UpdateBlockHeight(blockHeight types.BlockHeight) {
	c.mu.Lock()
	c.blockHeight = blockHeight
	c.mu.Unlock()
	go c.threadedUpdatePriceTable()
}

// UpdatePriceTable calls the updatePriceTableRPC on the host. It will do this
// periodically to ensure it always has up-to-date prices. It does this to
// ensure its requests will never be denied due to shortage of payment.
func (c *HostRPCClient) UpdatePriceTable() error {
	rpcID := modules.RPCUpdatePriceTable

	// Calculate the cost of the RPC
	cost, exists := c.priceTable.Costs[rpcID]
	if !exists {
		return errors.AddContext(errRPCNotAvailable, "could not update the price table")
	}

	// Get a stream using the RPC ID
	stream := c.connection.Stream(rpcID)
	defer stream.Close()

	// Send the RPC ID to let the host know we want an updated price table.
	// There's no request object in this case because it is implied from the id.
	if err := stream.WriteObjects(rpcID); err != nil {
		return err
	}

	// provide payment and await response
	_, err := c.paymentProvider.ProvidePaymentForRPC(rpcID, cost, stream, c.blockHeight)
	if err != nil {
		return err
	}

	// receive RPCUpdatePriceTableResponse
	var uptr modules.RPCUpdatePriceTableResponse
	if err := stream.ReadObject(uptr); err != nil {
		return err
	}

	var pt modules.PriceTable
	if err := json.Unmarshal(uptr.PriceTableJSON, &pt); err != nil {
		return err
	}

	c.priceTable = &pt
	return nil
}

// FundEphemeralAccount calls the fundEphemeralAccountRPC on the host
func (c *HostRPCClient) FundEphemeralAccount(id string, amount types.Currency) (types.Currency, error) {
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
	if err := stream.WriteObjects(rpcID, modules.RPCFundEphemeralAccountRequest{
		AccountID: id,
	}); err != nil {
		return types.ZeroCurrency, err
	}

	// provide payment and await response
	paid, err := c.paymentProvider.ProvidePaymentForRPC(rpcID, cost.Add(amount), stream, c.blockHeight)
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

// threadedUpdatePriceTable will fetch the host's updated prices if the current
// blockheight exceeds have of the expiry window. We keep track of the last
// updated block height to be able to find out the expiry window.
func (c *HostRPCClient) threadedUpdatePriceTable() {
	if err := c.tg.Add(); err != nil {
		return
	}
	defer c.tg.Done()

	c.mu.Lock()
	defer c.mu.Unlock()

	period := c.priceTableLastUpdate - c.priceTable.Expiry
	if c.blockHeight < c.priceTableLastUpdate+(period/2) {
		return
	}
	if err := c.UpdatePriceTable(); err != nil {
		c.log.Println("Failed to update the RPC price table", err)
		return
	}
	c.priceTableLastUpdate = c.blockHeight
}
