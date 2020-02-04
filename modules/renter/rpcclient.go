package renter

// TODO: The RPC client is used by the worker to interact with the host. It
// holds the RPC price table and can be seen as a renter RPC session. For now
// this is extracted in a separate object, quite possible though this state will
// move to the worker, and the RPCs will be exposed as static functions,
// callable by the worker.

import (
	"encoding/json"
	"fmt"
	"sync"

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
	UpdateBlockHeight(bh types.BlockHeight)
	UpdatePriceTable(pp modules.PaymentProvider) (modules.RPCPriceTable, error)
	FundEphemeralAccount(pp modules.PaymentProvider, pt modules.RPCPriceTable, id string, amount types.Currency) error
	DownloadSectorByRoot(pp modules.PaymentProvider, pt modules.RPCPriceTable, offset, length uint64, sectorRoot crypto.Hash, merkleProof bool, fcid types.FileContractID) ([]byte, error)
}

// hostRPCClient wraps all necessities to communicate with a host
type hostRPCClient struct {
	staticHostAddress string
	staticMuxAddress  string
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
func (r *Renter) newRPCClient(he modules.HostDBEntry, bh types.BlockHeight) RPCClient {
	muxAddress := he.NetAddress.Host() + fmt.Sprintf(":%d", he.HostExternalSettings.SiaMuxPort)

	return &hostRPCClient{
		staticHostAddress: string(he.NetAddress),
		staticMuxAddress:  muxAddress,
		staticHostKey:     he.PublicKey,
		blockHeight:       bh,
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
	stream, err := c.r.staticMux.NewStream(modules.HostSiaMuxSubscriberName, c.staticMuxAddress, modules.SiaPKToMuxPK(c.staticHostKey))
	if err != nil {
		return modules.RPCPriceTable{}, err
	}
	defer stream.Close()

	// Write the RPC id on the stream, there's no request object as it's
	// implied from the RPC id.
	err = encoding.WriteObject(stream, modules.RPCUpdatePriceTable)
	if err != nil {
		return modules.RPCPriceTable{}, err
	}

	// Receive RPCUpdatePriceTableResponse
	var uptr modules.RPCUpdatePriceTableResponse
	// TODO if this blocks siad dies
	if err := encoding.ReadObject(stream, &uptr, uint64(modules.RPCMinLen)); err != nil {
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
	// TODO: don't look at me harry, I'm hideous.
	cost := updated.Costs[modules.RPCFundEphemeralAccount.DontLookAtMeHarryImHideous()]
	_, err = pp.ProvidePaymentForRPC(modules.RPCUpdatePriceTable, cost, stream, c.blockHeight)
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
	// TODO: don't look at me harry, I'm hideous.
	cost, available := pt.Costs[modules.RPCFundEphemeralAccount.DontLookAtMeHarryImHideous()]
	if !available {
		return errors.AddContext(errRPCNotAvailable, fmt.Sprintf("Failed to fund ephemeral account %v", id))
	}

	// Get a stream
	stream, err := c.r.staticMux.NewStream(modules.HostSiaMuxSubscriberName, c.staticMuxAddress, modules.SiaPKToMuxPK(c.staticHostKey))
	if err != nil {
		return err
	}
	defer stream.Close()

	// Identify the RPC by writing the RPC id and price table UUID
	err = encoding.WriteObject(stream, modules.RPCFundEphemeralAccount)
	if err != nil {
		return err
	}
	err = encoding.WriteObject(stream, pt.UUID)
	if err != nil {
		return err
	}
	// Send the request
	err = encoding.WriteObject(stream, modules.RPCFundEphemeralAccountRequest{AccountID: id})
	if err != nil {
		return err
	}

	// Provide payment for the RPC and await response
	payment := amount.Add(cost)
	_, err = pp.ProvidePaymentForRPC(modules.RPCFundEphemeralAccount, payment, stream, bh)
	if err != nil {
		return err
	}

	// Receive RPCFundEphemeralAccountResponse
	var fundAccResponse modules.RPCFundEphemeralAccountResponse
	if err := encoding.ReadObject(stream, &fundAccResponse, uint64(modules.RPCMinLen)); err != nil {
		return err
	}

	return nil
}

func (c *hostRPCClient) DownloadSectorByRoot(pp modules.PaymentProvider, pt modules.RPCPriceTable, offset, length uint64, sectorRoot crypto.Hash, merkleProof bool, fcid types.FileContractID) ([]byte, error) {
	c.mu.Lock()
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
	stream, err := c.r.staticMux.NewStream(modules.HostSiaMuxSubscriberName, c.staticMuxAddress, modules.SiaPKToMuxPK(c.staticHostKey))
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	// Identify the RPC by writing the RPC id and price table UUID
	err = encoding.WriteObject(stream, modules.RPCExecuteProgram)
	if err != nil {
		return nil, err
	}
	err = encoding.WriteObject(stream, pt.UUID)
	if err != nil {
		return nil, err
	}

	// Send the request
	err = encoding.WriteObject(stream, modules.RPCExecuteProgramRequest{FileContractID: fcid})
	if err != nil {
		return nil, err
	}

	// Provide payment for the RPC
	_, err = pp.ProvidePaymentForRPC(modules.RPCExecuteProgram, programPrice, stream, bh)
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

	// Send program data (Note: do not prefix w/length).
	_, err = stream.Write(programData)
	if err != nil {
		return nil, err
	}

	// Read response.
	var resp modules.MDMInstructionResponse
	err = encoding.ReadObject(stream, &resp, 4096) // TODO
	if err != nil {
		return nil, err
	}

	// Check response error.
	if resp.Error != "TODO" {
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

// checkPriceTableGouging checks that the host is not gouging the renter during
// a price table update.
func checkPriceTableGouging(allowance modules.Allowance, priceTable modules.RPCPriceTable) error {
	// TODO
	return nil
}
