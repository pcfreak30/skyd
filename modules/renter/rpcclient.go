package renter

// TODO: The RPC client is used by the worker to interact with the host. It
// holds the RPC price table and can be seen as a renter RPC session. For now
// this is extracted in a separate object, quite possible though this state will
// move to the worker, and the RPCs will be exposed as static functions,
// callable by the worker.

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"

	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/threadgroup"
)

var (
	// errRPCNotAvailable is returned when the requested RPC is not available on
	// the host. This is possible when a host runs an older version, or when it
	// is not fully synced and disables its ephemeral account manager.
	errRPCNotAvailable = errors.New("RPC not available on host")
)

// RPCClient interface lists all possible RPC that can be called on the host
type RPCClient interface {
	UpdateBlockHeight(bh types.BlockHeight)
	UpdatePriceTable(pp modules.PaymentProvider) (modules.RPCPriceTable, error)
	FundEphemeralAccount(pp modules.PaymentProvider, pt modules.RPCPriceTable, id string, amount types.Currency) error
}

// hostRPCClient wraps all necessities to communicate with a host
type hostRPCClient struct {
	staticHostAddress string
	staticHostKey     types.SiaPublicKey

	// The current block height is cached on the client and gets updated by the
	// renter when consensus changes. This to avoid fetching the block height
	// from the renter on every RPC call.
	blockHeight types.BlockHeight

	log *persist.Logger
	tg  *threadgroup.ThreadGroup
	mu  sync.Mutex
	r   *Renter
}

// newRPCClient returns a new RPC client.
func (r *Renter) newRPCClient(he modules.HostDBEntry) RPCClient {
	return &hostRPCClient{
		staticHostAddress: string(he.NetAddress),
		staticHostKey:     he.PublicKey,
		log:               r.log,
		tg:                &r.tg,
		r:                 r,
	}
}

// UpdateBlockHeight is called by the renter when it processes a consensus
// change. The RPC client keeps the current block height as state to avoid
// fetching it from the renter on every RPC call.
func (c *hostRPCClient) UpdateBlockHeight(bh types.BlockHeight) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blockHeight = bh
}

// UpdatePriceTable performs the updatePriceTableRPC on the host.
func (c *hostRPCClient) UpdatePriceTable(pp modules.PaymentProvider) (modules.RPCPriceTable, error) {
	// Fetch a stream from the mux
	stream, err := c.r.staticMux.NewStream(siaMuxSubscriberName, c.staticHostAddress, modules.SiaPKToMuxPK(c.staticHostKey))

	// Write the RPC id on the stream, there's no request object as it's
	// implied from the RPC id.
	_, err = stream.Write(encoding.Marshal(modules.RPCUpdatePriceTable))
	if err != nil {
		return modules.RPCPriceTable{}, err
	}

	// Receive RPCUpdatePriceTableResponse
	var uptr modules.RPCUpdatePriceTableResponse
	if err := encoding.ReadObject(stream, uptr, uint64(modules.RPCMinLen)); err != nil {
		return modules.RPCPriceTable{}, err
	}
	var updated modules.RPCPriceTable
	if err := json.Unmarshal(uptr.PriceTableJSON, &updated); err != nil {
		return modules.RPCPriceTable{}, err
	}

	// Perform gouging check
	allowance := c.r.hostContractor.Allowance()
	if err := checkPriceTableGouging(allowance, updated); err != nil {
		// TODO: (follow-up) this should negatively affect the host's score
		return modules.RPCPriceTable{}, err
	}

	// Provide payment for the RPC
	cost := updated.Costs[modules.RPCUpdatePriceTable]
	_, err = pp.ProvidePaymentForRPC(modules.RPCUpdatePriceTable, cost, stream.(net.Conn), c.blockHeight)
	if err != nil {
		return modules.RPCPriceTable{}, err
	}

	return updated, nil
}

// FundEphemeralAccount will deposit the given amount into the account with id
// by calling the fundEphemeralAccountRPC on the host.
func (c *hostRPCClient) FundEphemeralAccount(pp modules.PaymentProvider, pt modules.RPCPriceTable, id string, amount types.Currency) error {
	c.mu.Lock()
	bh := c.blockHeight
	c.mu.Unlock()

	// Calculate the cost of the RPC
	cost, available := pt.Costs[modules.RPCFundEphemeralAccount]
	if !available {
		return errors.AddContext(errRPCNotAvailable, fmt.Sprintf("Failed to fund ephemeral account %v", id))
	}

	// Get a stream
	stream, err := c.r.staticMux.NewStream(siaMuxSubscriberName, c.staticHostAddress, modules.SiaPKToMuxPK(c.staticHostKey))
	if err != nil {
		return err
	}
	defer stream.Close()

	// Write the RPC id and RPCFundEphemeralAccountRequest object on the stream.
	_, err = stream.Write(encoding.MarshalAll(modules.RPCFundEphemeralAccount, modules.RPCFundEphemeralAccountRequest{AccountID: id}))
	if err != nil {
		return err
	}

	// Provide payment for the RPC and await response
	payment := amount.Add(cost)
	_, err = pp.ProvidePaymentForRPC(modules.RPCFundEphemeralAccount, payment, stream.(net.Conn), bh)
	if err != nil {
		return err
	}

	// Receive RPCFundEphemeralAccountResponse
	var fundAccResponse modules.RPCFundEphemeralAccountResponse
	if err := encoding.ReadObject(stream, fundAccResponse, uint64(modules.RPCMinLen)); err != nil {
		return err
	}

	return nil
}

// checkPriceTableGouging checks that the host is not gouging the renter during
// a price table update.
func checkPriceTableGouging(allowance modules.Allowance, priceTable modules.RPCPriceTable) error {
	// TODO
	return nil
}
