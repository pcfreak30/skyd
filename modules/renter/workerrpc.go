package renter

import (
	"bytes"
	"io"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/siamux/mux"

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
//
// Note that the bandwidth costs are added to the cost, so the given cost should
// not already include any bandwidth cost.
func (w *worker) managedExecuteProgram(p modules.Program, data []byte, fcid types.FileContractID, cost types.Currency, expectedUploadBandwidth, expectedDownloadBandwidth uint64) (responses []programResponse, limit mux.BandwidthLimit, err error) {
	// check host version
	cache := w.staticCache()
	if build.VersionCmp(cache.staticHostVersion, minAsyncVersion) < 0 {
		build.Critical("Executing new RHP RPC on host with version", cache.staticHostVersion)
	}

	// add the expected bandwidth cost
	pt := w.staticPriceTable().staticPriceTable
	bandwidthCost := modules.MDMBandwidthCost(pt, expectedUploadBandwidth, expectedDownloadBandwidth)
	cost = cost.Add(bandwidthCost)

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

	// set the stream's limit
	limit = stream.Limit()

	// increase host interactions depending on whether the consumed bandwidth
	// equals the expected amount of up-and download bandwidth.
	defer func() {
		go func() {
			if limit.Downloaded() != expectedDownloadBandwidth ||
				limit.Uploaded() != expectedUploadBandwidth ||
				w.renter.deps.Disrupt("InterruptUpdateInteractions") {
				w.renter.hostDB.IncrementFailedInteractions(w.staticHostPubKey)
			} else {
				w.renter.hostDB.IncrementSuccessfulInteractions(w.staticHostPubKey)
			}
		}()
	}()

	// prepare a buffer so we can optimize our writes
	buffer := bytes.NewBuffer(nil)

	// write the specifier
	err = modules.RPCWrite(buffer, modules.RPCExecuteProgram)
	if err != nil {
		return
	}

	// send price table uid
	err = modules.RPCWrite(buffer, pt.UID)
	if err != nil {
		return
	}

	// provide payment
	err = w.staticAccount.ProvidePayment(buffer, w.staticHostPubKey, modules.RPCUpdatePriceTable, cost, w.staticAccount.staticID, cache.staticBlockHeight)
	if err != nil {
		return
	}

	// prepare the request.
	epr := modules.RPCExecuteProgramRequest{
		FileContractID:    fcid,
		Program:           p,
		ProgramDataLength: uint64(len(data)),
	}

	// send the execute program request.
	err = modules.RPCWrite(buffer, epr)
	if err != nil {
		return
	}

	// send the programData.
	_, err = buffer.Write(data)
	if err != nil {
		return
	}

	// write contents of the buffer to the stream
	_, err = stream.Write(buffer.Bytes())
	if err != nil {
		return
	}

	// read the cancellation token.
	var ct modules.MDMCancellationToken
	err = modules.RPCRead(stream, &ct)
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
		responses[i].Output = make([]byte, outputLen)
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

// staticNewStream returns a new stream to the worker's host
func (w *worker) staticNewStream() (siamux.Stream, error) {
	if build.VersionCmp(w.staticCache().staticHostVersion, minAsyncVersion) < 0 {
		w.renter.log.Critical("calling staticNewStream on a host that doesn't support the new protocol")
		return nil, errors.New("host doesn't support this")
	}
	stream, err := w.renter.staticMux.NewStream(modules.HostSiaMuxSubscriberName, w.staticHostMuxAddress, modules.SiaPKToMuxPK(w.staticHostPubKey))
	if err != nil {
		return nil, err
	}
	return stream, nil
}
