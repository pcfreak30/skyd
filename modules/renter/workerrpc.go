package renter

import (
	"io"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/siamux"
)

// programResponse is a helper struct that wraps the RPCExecuteProgramResponse
// alongside the data output
type programResponse struct {
	modules.RPCExecuteProgramResponse
	Output []byte
}

// managedExecuteProgram performs the ExecuteProgramRPC on the host
func (w *worker) managedExecuteProgram(p modules.Program, data []byte, fcid types.FileContractID, cost types.Currency) (responses []programResponse, err error) {
	// check host version
	cache := w.staticCache()
	if build.VersionCmp(cache.staticHostVersion, modules.MinimumSupportedNewRenterHostProtocolVersion) < 0 {
		build.Critical("Executing new RHP RPC on host with version", cache.staticHostVersion)
	}

	// track the withdrawal
	// TODO: this is very naive and does not consider refunds at all
	w.staticAccount.managedTrackWithdrawal(cost)
	defer func() {
		w.staticAccount.managedCommitWithdrawal(cost, err == nil)
	}()

	// create a new stream
	var stream siamux.Stream
	stream, err = w.staticNewStream()
	if err != nil {
		err = errors.AddContext(err, "Unable to create a new stream")
		return
	}
	defer func() {
		if err := stream.Close(); err != nil {
			w.renter.log.Println("ERROR: failed to close stream", err)
		}
	}()

	// grab some variables from the worker
	bh := cache.staticBlockHeight

	// write the specifier
	err = modules.RPCWrite(stream, modules.RPCExecuteProgram)
	if err != nil {
		return
	}

	// send price table uid
	pt := w.staticPriceTable().staticPriceTable
	err = modules.RPCWrite(stream, pt.UID)
	if err != nil {
		return
	}

	// prepare the request.
	epr := modules.RPCExecuteProgramRequest{
		FileContractID:    fcid,
		Program:           p,
		ProgramDataLength: uint64(len(data)),
	}

	// provide payment
	err = w.staticAccount.ProvidePayment(stream, w.staticHostPubKey, modules.RPCUpdatePriceTable, cost, w.staticAccount.staticID, bh)
	if err != nil {
		return
	}

	// send the execute program request.
	err = modules.RPCWrite(stream, epr)
	if err != nil {
		return
	}

	// send the programData.
	_, err = stream.Write(data)
	if err != nil {
		return
	}

	// TODO we call this manually here in order to save a RT and write the
	// program instructions and data before reading. Remove when !4446 is
	// merged.

	// receive PayByEphemeralAccountResponse
	var payByResponse modules.PayByEphemeralAccountResponse
	err = modules.RPCRead(stream, &payByResponse)
	if err != nil {
		return
	}

	// read the responses.
	responses = make([]programResponse, len(epr.Program))
	for i := range responses {
		err = modules.RPCRead(stream, &responses[i])
		if err != nil {
			return
		}

		// Read the output data.
		outputLen := responses[i].OutputLength
		responses[i].Output = make([]byte, outputLen, outputLen)
		_, err = io.ReadFull(stream, responses[i].Output)
		if err != nil {
			return
		}

		// If the response contains an error we are done.
		if responses[i].Error != nil {
			break
		}
	}
	return
}

// managedReadSector returns the sector data for given root
func (w *worker) managedReadSector(sectorRoot crypto.Hash, offset, length uint64) ([]byte, error) {
	// create a new stream
	stream, err := w.staticNewStream()
	if err != nil {
		return nil, errors.AddContext(err, "Unable to create a new stream")
	}
	defer func() {
		if err := stream.Close(); err != nil {
			w.renter.log.Println("ERROR: failed to close stream", err)
		}
	}()

	// create the program
	pt := w.staticPriceTable().staticPriceTable
	pb := modules.NewProgramBuilder(&pt)
	pb.AddReadSectorInstruction(length, offset, sectorRoot, true)
	program, programData := pb.Program()
	cost, _, _ := pb.Cost(true)

	// take into account bandwidth costs
	cost = cost.Add(pb.BandwidthCost())

	// TODO temporarily increase budget to ensure it is sufficient to cover the
	// cost, until we have defined the true bandwidth cost of the new protocol
	cost = cost.Mul64(10)

	// exeucte it
	responses, err := w.managedExecuteProgram(program, programData, w.staticHostFCID, cost)
	if err != nil {
		return nil, err
	}

	// return the response
	var sectorData []byte
	for _, resp := range responses {
		if resp.Error != nil {
			return nil, resp.Error
		}
		sectorData = resp.Output
		break
	}
	return sectorData, nil
}

// staticNewStream returns a new stream to the worker's host
func (w *worker) staticNewStream() (siamux.Stream, error) {
	return w.renter.staticMux.NewStream(modules.HostSiaMuxSubscriberName, w.staticHostMuxAddress, modules.SiaPKToMuxPK(w.staticHostPubKey))
}
