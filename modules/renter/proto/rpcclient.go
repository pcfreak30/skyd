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
// way an RPC client can be easier initialised from a renter. It is currently
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
	staticPaymentProvider modules.PaymentProvider
	staticPeerMux         *modules.PeerMux

	priceTable        modules.RPCPriceTable
	priceTableUpdated types.BlockHeight

	// The current blockheight is cached on every RPC client and gets updated by
	// the renter when consensus changes. This to avoid fetching the block
	// height from the renter on every RPC call.
	blockHeight types.BlockHeight

	log *persist.Logger
	tg  *threadgroup.ThreadGroup
	mu  sync.Mutex
}

// NewRPCClient returns a new RPC client.
func NewRPCClient(pm *modules.PeerMux, pp modules.PaymentProvider, cbh types.BlockHeight, tg *threadgroup.ThreadGroup, log *persist.Logger) (RPCClient, error) {
	client := &HostRPCClient{
		staticPaymentProvider: pp,
		staticPeerMux:         pm,
		blockHeight:           cbh,
		log:                   log,
		tg:                    tg,
	}

	if err := client.UpdatePriceTable(); err != nil {
		return nil, err
	}
	return client, nil
}

// UpdateBlockHeight is called by the renter when it processes a consensus
// change. Every time the block height gets updated we check if we have to
// update the RPC price table.
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
	c.mu.Lock()
	defer c.mu.Unlock()

	// Fetch a stream from the mux
	stream := c.staticPeerMux.NewStream()
	defer stream.Close()

	// Write the RPC id. There is no other request object because it's implied
	// from the id.
	if err := stream.WriteObjects(modules.RPCUpdatePriceTable); err != nil {
		return err
	}

	// Receive RPCUpdatePriceTableResponse
	var uptr modules.RPCUpdatePriceTableResponse
	if err := stream.ReadObject(uptr); err != nil {
		return err
	}
	var updated modules.RPCPriceTable
	if err := json.Unmarshal(uptr.PriceTableJSON, &updated); err != nil {
		return err
	}

	// TODO: perform gouging check

	// Provide payment
	cost := updated.Costs[modules.RPCUpdatePriceTable]
	_, err := c.staticPaymentProvider.ProvidePaymentForRPC(modules.RPCUpdatePriceTable, cost, stream, c.blockHeight)
	if err != nil {
		return err
	}

	// Update the price table and cache the current block height. We'll use this
	// to figure out when to update the price table again to ensure we never
	// have an expired price table.
	c.priceTable = updated
	c.priceTableUpdated = c.blockHeight
	return nil
}

// FundEphemeralAccount calls the fundEphemeralAccountRPC on the host
func (c *HostRPCClient) FundEphemeralAccount(id string, amount types.Currency) (types.Currency, error) {
	c.mu.Lock()
	pt := c.priceTable
	bh := c.blockHeight
	c.mu.Unlock()

	rpcID := modules.RPCFundEphemeralAccount

	// Calculate the cost of the RPC
	cost, exists := pt.Costs[rpcID]
	if !exists {
		return types.ZeroCurrency, errors.AddContext(errRPCNotAvailable, "could not fund ephemeral account")
	}

	// Get a stream
	stream := c.staticPeerMux.NewStream()
	defer stream.Close()

	// send RPCFundEphemeralAccountRequest
	if err := stream.WriteObjects(rpcID, modules.RPCFundEphemeralAccountRequest{
		AccountID: id,
	}); err != nil {
		return types.ZeroCurrency, err
	}

	// provide payment and await response
	payment := cost.Add(amount)
	_, err := c.staticPaymentProvider.ProvidePaymentForRPC(rpcID, payment, stream, bh)
	if err != nil {
		return types.ZeroCurrency, err
	}

	// receive RPCFundEphemeralAccountResponse
	var fundAccResponse modules.RPCFundEphemeralAccountResponse
	if err := stream.ReadObject(fundAccResponse); err != nil {
		return types.ZeroCurrency, err
	}

	return payment, nil
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

	// If the current blockheight has not yet exceeded half of the expiry
	// window, we postpone updating the price table.
	window := uint64(c.priceTableUpdated - c.priceTable.Expiry)
	if uint64(c.blockHeight) < uint64(c.priceTableUpdated)+window/2 {
		return
	}

	err := c.UpdatePriceTable()
	if err != nil {
		c.log.Println("Failed to update the RPC price table", err)
	}
}
