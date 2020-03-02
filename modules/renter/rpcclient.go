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
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/host/mdm"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

// RPCClient interface lists all possible RPC that can be called on the host
type RPCClient interface {
	UpdatePriceTable(pp modules.PaymentProvider) (modules.RPCPriceTable, error)
	FundEphemeralAccount(pp modules.PaymentProvider, pt modules.RPCPriceTable, id string, amount types.Currency) error
	DownloadSectorByRoot(pp modules.PaymentProvider, pt modules.RPCPriceTable, offset, length uint64, sectorRoot crypto.Hash, merkleProof bool, fcid types.FileContractID) ([]byte, error)
	UpdateBlockHeight(bh types.BlockHeight)
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

	mu sync.Mutex
	r  *Renter
}

// newRPCClient returns a new RPC client.
func (r *Renter) newRPCClient(he modules.HostDBEntry, bh types.BlockHeight) RPCClient {
	muxAddress := fmt.Sprintf("%s:%d", he.NetAddress.Host(), he.HostExternalSettings.SiaMuxPort)

	return &hostRPCClient{
		staticHostAddress: string(he.NetAddress),
		staticMuxAddress:  muxAddress,
		staticHostKey:     he.PublicKey,
		blockHeight:       bh,
		r:                 r,
	}
}

// UpdatePriceTable performs the updatePriceTableRPC on the host.
func (c *hostRPCClient) UpdatePriceTable(pp modules.PaymentProvider) (modules.RPCPriceTable, error) {
	// fetch a stream from the mux
	stream, err := c.r.staticMux.NewStream(modules.HostSiaMuxSubscriberName, c.staticMuxAddress, modules.SiaPKToMuxPK(c.staticHostKey))
	if err != nil {
		return modules.RPCPriceTable{}, err
	}
	defer stream.Close()

	// write the RPC id on the stream, there's no request object as it's
	// implied from the RPC id
	err = modules.RPCWrite(stream, modules.RPCUpdatePriceTable)
	if err != nil {
		return modules.RPCPriceTable{}, err
	}

	// receive RPCUpdatePriceTableResponse
	var uptr modules.RPCUpdatePriceTableResponse
	if err := modules.RPCRead(stream, &uptr); err != nil {
		return modules.RPCPriceTable{}, err
	}
	var updated modules.RPCPriceTable
	if err := json.Unmarshal(uptr.PriceTableJSON, &updated); err != nil {
		return modules.RPCPriceTable{}, err
	}

	// perform gouging check
	allowance := c.r.hostContractor.Allowance()
	if err := checkPriceTableGouging(allowance, updated); err != nil {
		// TODO: (follow-up) this should negatively affect the host's score
		return modules.RPCPriceTable{}, err
	}

	// provide payment for the RPC
	cost := updated.FundEphemeralAccountCost
	_, err = pp.ProvidePaymentForRPC(modules.RPCUpdatePriceTable, cost, stream, c.managedBlockHeight())
	if err != nil {
		return modules.RPCPriceTable{}, err
	}

	return updated, nil
}

// FundEphemeralAccount will call the fundEphemeralAccountRPC on the host and if
// successful will deposit the given amount into the specified account.
func (c *hostRPCClient) FundEphemeralAccount(pp modules.PaymentProvider, pt modules.RPCPriceTable, id string, amount types.Currency) error {
	// calculate the cost of the RPC
	cost := pt.FundEphemeralAccountCost

	// get a stream
	stream, err := c.r.staticMux.NewStream(modules.HostSiaMuxSubscriberName, c.staticMuxAddress, modules.SiaPKToMuxPK(c.staticHostKey))
	if err != nil {
		return err
	}
	defer stream.Close()

	// send all necessary request objects, this consists out of the rpc
	// identifier, the price table identifier and the actual rpc request
	err = modules.RPCWriteAll(stream, modules.RPCFundEphemeralAccount, pt.UUID, modules.RPCFundEphemeralAccountRequest{AccountID: id})
	if err != nil {
		return err
	}

	// provide payment
	payment := amount.Add(cost)
	_, err = pp.ProvidePaymentForRPC(modules.RPCFundEphemeralAccount, payment, stream, c.managedBlockHeight())
	if err != nil {
		return err
	}

	// read response
	var fundAccResponse modules.RPCFundEphemeralAccountResponse
	return modules.RPCRead(stream, &fundAccResponse)
}

// DownloadSectorByRoot will create a ReadSectorProgram and execute that by
// calling the ExecuteMDMProgram RPC on the host. This will effectively download
// the specified sector.
func (c *hostRPCClient) DownloadSectorByRoot(pp modules.PaymentProvider, pt modules.RPCPriceTable, offset, length uint64, sectorRoot crypto.Hash, merkleProof bool, fcid types.FileContractID) ([]byte, error) {
	// create mdm program
	// TODO handle refund
	programPrice, _ := modules.MDMReadCost(pt, length)
	instructions, programData := mdm.NewReadSectorProgram(length, offset, sectorRoot, merkleProof)

	// get a stream
	stream, err := c.r.staticMux.NewStream(modules.HostSiaMuxSubscriberName, c.staticMuxAddress, modules.SiaPKToMuxPK(c.staticHostKey))
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	// send all necessary request objects, this consists out of the rpc
	// identifier, the price table identifier and the actual rpc request
	err = modules.RPCWriteAll(stream, modules.RPCExecuteMDMProgram, pt.UUID, modules.RPCExecuteProgramRequest{FileContractID: fcid})
	if err != nil {
		return nil, err
	}

	// provide payment
	_, err = pp.ProvidePaymentForRPC(modules.RPCExecuteMDMProgram, programPrice, stream, c.managedBlockHeight())
	if err != nil {
		return nil, err
	}

	// send instructions and the length of program data
	dataLen := uint64(len(programData))
	if err := modules.RPCWriteAll(stream, instructions, dataLen); err != nil {
		return nil, err
	}
	_, err = stream.Write(programData) // send programdata without length prefix
	if err != nil {
		return nil, err
	}

	// read response
	var resp modules.MDMInstructionResponse
	err = modules.RPCRead(stream, &resp)
	if err != nil {
		return nil, err
	}

	// check response error
	if resp.Error != "TODO" {
		return nil, errors.AddContext(err, "failed to download sector")
	}
	// sanity check length
	if uint64(len(resp.Output)) != length {
		return nil, fmt.Errorf("expected output to have length %v but was %v", length, len(resp.Output))
	}
	// validate merkle proof if necessary
	if merkleProof {
		panic("merkle proof not supported yet")
	}
	return resp.Output, err
}

// UpdateBlockHeight is called by the renter when it processes a consensus
// change. The RPC client keeps the current block height as state to avoid
// fetching it from the renter on every RPC call.
func (c *hostRPCClient) UpdateBlockHeight(bh types.BlockHeight) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blockHeight = bh
}

// managedBlockHeight returns the cached blockheight
func (c *hostRPCClient) managedBlockHeight() types.BlockHeight {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.blockHeight
}

// checkPriceTableGouging checks that the host is not gouging the renter during
// a price table update.
func checkPriceTableGouging(allowance modules.Allowance, priceTable modules.RPCPriceTable) error {
	// TODO
	return nil
}
