package renter

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/host/mdm"
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
	DownloadSectorByRoot(offset, length uint64, sectorRoot crypto.Hash, merkleProof bool) ([]byte, error)
	UpdatePriceTable() error
	FundEphemeralAccount(id string, amount types.Currency) error
}

// hostRPCClient wraps all necessities to communicate with a host
type hostRPCClient struct {
	staticPaymentProvider modules.PaymentProvider
	staticHostAddress     string
	staticHostKey         types.SiaPublicKey

	priceTable        modules.RPCPriceTable
	priceTableUpdated time.Time

	// blockHeight is cached on every client and gets updated by the renter when
	// consensus changes. This to avoid fetching the block height from the
	// renter on every RPC call.
	blockHeight types.BlockHeight

	renter *Renter
	log    *persist.Logger
	tg     *threadgroup.ThreadGroup
	mu     sync.Mutex
}

// newRPCClient returns a new RPC client.
func (r *Renter) newRPCClient(pp modules.PaymentProvider, he modules.HostDBEntry) (RPCClient, error) {
	client := hostRPCClient{
		staticPaymentProvider: pp,
		staticHostAddress:     string(he.NetAddress),
		staticHostKey:         he.PublicKey,
		blockHeight:           r.blockHeight,
		log:                   r.log,
		tg:                    &r.tg,
		renter:                r,
	}

	if err := client.UpdatePriceTable(); err != nil {
		// Return nil if we weren't able fetch the host's pricing.
		return nil, err
	}
	return &client, nil
}

// UpdateBlockHeight is called by the renter when it processes a consensus
// change. Every time the block height gets updated we potentially also update
// the RPC price table to get the host's latest prices.
func (c *hostRPCClient) UpdateBlockHeight(blockHeight types.BlockHeight) {
	var updatePriceTable bool
	defer func() {
		if updatePriceTable {
			go c.threadedUpdatePriceTable()
		}
	}()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.blockHeight = blockHeight

	// This is more of a sanity check to prevent underflow. This could only be
	// the case if the renter and host's blockheight differ by a large amount of
	// blocks.
	if c.priceTableUpdated.Unix() > c.priceTable.Expiry {
		updatePriceTable = true
		return
	}

	// Update the price table if the current blockheight has surpassed half of
	// the expiry window. The expiry window is defined as the time (in blocks)
	// since we last updated the RPC price table until its expiry block height.
	window := c.priceTable.Expiry - c.priceTableUpdated.Unix()
	if time.Now().Unix() > (c.priceTableUpdated.Unix())+window/2 {
		updatePriceTable = true
		return
	}
}

// UpdatePriceTable performs the updatePriceTableRPC on the host.
func (c *hostRPCClient) UpdatePriceTable() error {
	// Fetch a stream from the mux
	stream, err := c.renter.staticMux.NewStream(siaMuxSubscriberName, c.staticHostAddress, modules.SiaPKToMuxPK(c.staticHostKey))

	// Write the RPC id on the stream, there's no request object as it's
	// implied from the RPC id.
	_, err = stream.Write(encoding.Marshal(modules.RPCUpdatePriceTable))
	if err != nil {
		return err
	}

	// Receive RPCUpdatePriceTableResponse
	var uptr modules.RPCUpdatePriceTableResponse
	if err := encoding.ReadObject(stream, uptr, uint64(modules.RPCMinLen)); err != nil {
		return err
	}
	var updated modules.RPCPriceTable
	if err := json.Unmarshal(uptr.PriceTableJSON, &updated); err != nil {
		return err
	}

	// Perform gouging check
	allowance := c.renter.hostContractor.Allowance()
	if err := checkPriceTableGouging(allowance, updated); err != nil {
		// TODO: (follow-up) this should negatively affect the host's score
		return err
	}

	// Provide payment for the RPC
	// TODO: don't look at me harry, I'm hideous.
	cost := updated.Costs[types.NewSpecifier(string(modules.RPCFundEphemeralAccount[:]))]
	_, err = c.staticPaymentProvider.ProvidePaymentForRPC(modules.RPCUpdatePriceTable, cost, stream, c.blockHeight)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.priceTable = updated
	c.priceTableUpdated = time.Now()
	c.mu.Unlock()
	return nil
}

// FundEphemeralAccount will deposit the given amount into the account with id
// by calling the fundEphemeralAccountRPC on the host.
func (c *hostRPCClient) FundEphemeralAccount(id string, amount types.Currency) error {
	c.mu.Lock()
	pt := c.priceTable
	bh := c.blockHeight
	c.mu.Unlock()

	// Calculate the cost of the RPC
	// TODO: don't look at me harry, I'm hideous.
	cost, available := pt.Costs[types.NewSpecifier(string(modules.RPCFundEphemeralAccount[:]))]
	if !available {
		return errors.AddContext(errRPCNotAvailable, fmt.Sprintf("Failed to fund ephemeral account %v", id))
	}

	// Get a stream
	stream, err := c.renter.staticMux.NewStream(siaMuxSubscriberName, c.staticHostAddress, modules.SiaPKToMuxPK(c.staticHostKey))
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
	_, err = c.staticPaymentProvider.ProvidePaymentForRPC(modules.RPCFundEphemeralAccount, payment, stream, bh)
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

func (c *hostRPCClient) DownloadSectorByRoot(offset, length uint64, sectorRoot crypto.Hash, merkleProof bool) ([]byte, error) {
	c.mu.Lock()
	pt := c.priceTable
	bh := c.blockHeight
	c.mu.Unlock()

	// Create mdm program.
	instructions, programData := mdm.NewReadSectorProgram(length, offset, sectorRoot, merkleProof)

	// Calculate the cost of the RPC
	dataLen := uint64(len(programData))
	programCost, err := modules.CalculateProgramCost(instructions, dataLen)
	if err != nil {
		return nil, err
	}
	programPrice := modules.ConvertCostToPrice(programCost, &pt)

	// Get a stream
	stream, err := c.renter.staticMux.NewStream(siaMuxSubscriberName, c.staticHostAddress, modules.SiaPKToMuxPK(c.staticHostKey))
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	// Write the RPC id and RPCFundEphemeralAccountRequest object on the stream.
	_, err = stream.Write(encoding.MarshalAll(modules.RPCExecuteProgram, modules.RPCExecuteProgramRequest{FileContractID: types.FileContractID{}}))
	if err != nil {
		return nil, err
	}
	// Provide payment for the RPC
	_, err = c.staticPaymentProvider.ProvidePaymentForRPC(modules.RPCExecuteProgram, programPrice, stream, bh)
	if err != nil {
		return nil, err
	}
	// Send instructions.
	if err := encoding.WriteObject(stream, instructions); err != nil {
		return nil, err
	}
	// Send length of program data.
	if err := encoding.WriteObject(stream, dataLen); err != nil {
		return nil, err
	}
	// Read response.
	var resp mdm.Output
	err = encoding.ReadObject(stream, &resp, 4096) // TODO
	if err != nil {
		return nil, err
	}
	// Check response error.
	if resp.Error != nil {
		return nil, errors.AddContext(err, "failed to download sector")
	}
	// Sanity check length.
	if uint64(len(resp.Output)) != length {
		return nil, fmt.Errorf("expected output to have length %v but was %v", length, len(resp.Output))
	}
	// Validate merkle proof if necessary.
	if merkleProof {
		panic("merkle proof not supported yet")
	}
	return resp.Output, err
}

// threadedUpdatePriceTable will update the RPC price table by fetching the
// host's latest prices.
func (c *hostRPCClient) threadedUpdatePriceTable() {
	if err := c.tg.Add(); err != nil {
		return
	}
	defer c.tg.Done()

	err := c.UpdatePriceTable()
	if err != nil {
		c.log.Println("Failed to update the RPC price table", err)
	}
}

// checkPriceTableGouging checks that the host is not gouging the renter during
// a price table update.
func checkPriceTableGouging(allowance modules.Allowance, priceTable modules.RPCPriceTable) error {
	// TODO
	return nil
}
