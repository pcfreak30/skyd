package renter

import (
	"bytes"
	"context"
	"io/ioutil"
	"time"

	"github.com/opentracing/opentracing-go"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/encoding"
	"gitlab.com/SkynetLabs/skyd/build"

	"gitlab.com/NebulousLabs/errors"
)

const (
	// ethernetMTU is the minimum transferable size for ethernet networks. It's
	// used as the estimated upper limit for the frame size of the SiaMux.
	ethernetMTU = 1500

	// jobReadRegistryPerformanceDecay defines how much the average
	// performance is decayed each time a new datapoint is added. The jobs use
	// an exponential weighted average.
	jobReadRegistryPerformanceDecay = 0.9

	// minReadRegistrySIDVersion is the minimum host version that supports
	// reading registry entries by subscription id.
	minReadRegistrySIDVersion = "1.5.6"
)

type (
	// jobReadRegistry contains information about a ReadRegistry query.
	jobReadRegistry struct {
		staticRegistryEntryID modules.RegistryEntryID
		staticSiaPublicKey    *types.SiaPublicKey
		staticSpan            opentracing.Span
		staticTweak           *crypto.Hash

		staticResponseChan chan *jobReadRegistryResponse // Channel to send a response down

		*jobGeneric
	}

	// jobReadRegistryQueue is a list of ReadRegistry jobs that have been
	// assigned to the worker.
	jobReadRegistryQueue struct {
		// These variables contain an exponential weighted average of the
		// worker's recent performance for jobReadRegistryQueue.
		weightedJobTime float64

		*jobGenericQueue
	}

	// jobReadRegistryResponse contains the result of a ReadRegistry query.
	jobReadRegistryResponse struct {
		staticSignedRegistryValue *modules.SignedRegistryValue
		staticErr                 error
		staticCompleteTime        time.Time
		staticExecuteTime         time.Time
		staticEID                 modules.RegistryEntryID
		staticSPK                 *types.SiaPublicKey
		staticTweak               *crypto.Hash
		staticWorker              *worker
	}
)

// parseSignedRegistryValueResponse is a helper function to parse a response
// containing a signed registry value.
func parseSignedRegistryValueResponse(resp []byte, needPKAndTweak bool) (spk types.SiaPublicKey, tweak crypto.Hash, data []byte, rev uint64, sig crypto.Signature, err error) {
	dec := encoding.NewDecoder(bytes.NewReader(resp), encoding.DefaultAllocLimit)
	if needPKAndTweak {
		err = dec.DecodeAll(&spk, &tweak, &sig, &rev)
	} else {
		err = dec.DecodeAll(&sig, &rev)
	}
	if err != nil {
		return
	}
	data, err = ioutil.ReadAll(dec)
	return
}

// lookupsRegistry looks up a registry on the host and verifies its signature.
func lookupRegistry(w *worker, sid modules.RegistryEntryID, spk *types.SiaPublicKey, tweak *crypto.Hash) (*modules.SignedRegistryValue, error) {
	// Create the program.
	pt := w.staticPriceTable().staticPriceTable
	pb := modules.NewProgramBuilder(&pt, 0) // 0 duration since ReadRegistry doesn't depend on it.
	needPKAndTweak := spk == nil || tweak == nil

	var refund types.Currency
	var err error
	if build.VersionCmp(w.staticCache().staticHostVersion, minRegistryVersion) < 0 {
		err = errors.New("lookupRegistry called on host with version < minRegistryVersion")
		build.Critical(err)
		return nil, err
	} else if build.VersionCmp(w.staticCache().staticHostVersion, minReadRegistrySIDVersion) < 0 {
		refund, err = pb.AddReadRegistryInstruction(*spk, *tweak)
	} else {
		refund, err = pb.AddReadRegistryEIDInstruction(sid, needPKAndTweak)
	}
	if err != nil {
		return nil, errors.AddContext(err, "Unable to add read registry instruction")
	}
	program, programData := pb.Program()
	cost, _, _ := pb.Cost(true)

	// take into account bandwidth costs
	ulBandwidth, dlBandwidth := readRegistryJobExpectedBandwidth()
	bandwidthCost := modules.MDMBandwidthCost(pt, ulBandwidth, dlBandwidth)
	cost = cost.Add(bandwidthCost)

	// Execute the program and parse the responses.
	responses, _, err := w.managedExecuteProgram(program, programData, types.FileContractID{}, categoryRegistryRead, cost)
	if err != nil {
		return nil, errors.AddContext(err, "Unable to execute program")
	}
	for _, resp := range responses {
		if resp.Error != nil {
			return nil, errors.AddContext(resp.Error, "Output error")
		}
		break
	}
	if len(responses) != len(program) {
		return nil, errors.New("received invalid number of responses but no error")
	}

	// Check if entry was found.
	resp := responses[0]
	if resp.OutputLength == 0 {
		// If the entry wasn't found, we are issued a refund.
		w.staticAccount.managedTrackDeposit(refund)
		w.staticAccount.managedCommitDeposit(refund, true)
		return nil, nil
	}

	// Parse response.
	spkHost, tweakHost, data, revision, sig, err := parseSignedRegistryValueResponse(resp.Output, needPKAndTweak)
	if err != nil {
		return nil, errors.AddContext(err, "failed to parse signed revision response")
	}

	// If we don't have both the key and tweak, use the ones returned by the
	// host.
	if needPKAndTweak {
		if sid != modules.DeriveRegistryEntryID(spkHost, tweakHost) {
			return nil, errors.New("spk and tweak returned by host don't match requested sid")
		}
		spk = &spkHost
		tweak = &tweakHost
	}
	rv := modules.NewSignedRegistryValue(*tweak, data, revision, sig)

	// Verify signature.
	if rv.Verify(spk.ToPublicKey()) != nil {
		return nil, errors.New("failed to verify returned registry value's signature")
	}
	return &rv, nil
}

// newJobReadRegistry is a helper method to create a new ReadRegistry job.
func (w *worker) newJobReadRegistry(ctx context.Context, span opentracing.Span, responseChan chan *jobReadRegistryResponse, spk types.SiaPublicKey, tweak crypto.Hash) *jobReadRegistry {
	sid := modules.DeriveRegistryEntryID(spk, tweak)
	return w.newJobReadRegistrySID(ctx, span, responseChan, sid, &spk, &tweak)
}

// newJobReadRegistry is a helper method to create a new ReadRegistry job.
func (w *worker) newJobReadRegistrySID(ctx context.Context, span opentracing.Span, responseChan chan *jobReadRegistryResponse, sid modules.RegistryEntryID, spk *types.SiaPublicKey, tweak *crypto.Hash) *jobReadRegistry {
	jobSpan := opentracing.StartSpan("ReadRegistryJob", opentracing.ChildOf(span.Context()))
	jobSpan.SetTag("Host", w.staticHostPubKeyStr)
	return &jobReadRegistry{
		staticSiaPublicKey:    spk,
		staticRegistryEntryID: sid,
		staticTweak:           tweak,
		staticResponseChan:    responseChan,
		staticSpan:            jobSpan,
		jobGeneric:            newJobGeneric(ctx, w.staticJobReadRegistryQueue, nil),
	}
}

// callDiscard will discard a job, sending the provided error.
func (j *jobReadRegistry) callDiscard(err error) {
	// Log info and finish span.
	j.staticSpan.LogKV("callDiscard", err)
	j.staticSpan.SetTag("success", false)
	defer j.staticSpan.Finish()

	w := j.staticQueue.staticWorker()
	errLaunch := w.staticRenter.tg.Launch(func() {
		response := &jobReadRegistryResponse{
			staticErr:          errors.Extend(err, ErrJobDiscarded),
			staticCompleteTime: time.Now(),
			staticEID:          j.staticRegistryEntryID,
			staticSPK:          j.staticSiaPublicKey,
			staticTweak:        j.staticTweak,
			staticWorker:       w,
		}
		select {
		case j.staticResponseChan <- response:
		case <-j.staticCtx.Done():
		case <-w.staticRenter.tg.StopChan():
		}
	})
	if errLaunch != nil {
		w.staticRenter.staticLog.Debugln("callDiscard: launch failed", err)
	}
}

// callExecute will run the ReadRegistry job.
func (j *jobReadRegistry) callExecute() {
	start := time.Now()
	w := j.staticQueue.staticWorker()

	// Finish job span at the end.
	defer j.staticSpan.Finish()

	// Capture callExecute in new span.
	span := opentracing.GlobalTracer().StartSpan("callExecute", opentracing.ChildOf(j.staticSpan.Context()))
	defer span.Finish()

	// Prepare a method to send a response asynchronously.
	sendResponse := func(srv *modules.SignedRegistryValue, err error) {
		errLaunch := w.staticRenter.tg.Launch(func() {
			response := &jobReadRegistryResponse{
				staticCompleteTime:        time.Now(),
				staticSignedRegistryValue: srv,
				staticErr:                 err,
				staticExecuteTime:         start,
				staticEID:                 j.staticRegistryEntryID,
				staticSPK:                 j.staticSiaPublicKey,
				staticTweak:               j.staticTweak,
				staticWorker:              w,
			}
			select {
			case j.staticResponseChan <- response:
			case <-j.staticCtx.Done():
			case <-w.staticRenter.tg.StopChan():
			}
		})
		if errLaunch != nil {
			w.staticRenter.staticLog.Debugln("callExececute: launch failed", err)
		}
	}

	// Pubkey and tweak should be set for hosts that don't support fetching the
	// entry by subscription id yet.
	spk, tweak := j.staticSiaPublicKey, j.staticTweak
	if build.VersionCmp(w.staticCache().staticHostVersion, minReadRegistrySIDVersion) < 0 && (spk == nil || tweak == nil) {
		err := errors.New("can't call lookupRegistry without pubkey/tweak on legacy hosts")
		build.Critical(err)
		sendResponse(nil, err)
		j.staticQueue.callReportFailure(err)
		span.LogKV("error", err)
		j.staticSpan.SetTag("success", false)
		return
	}
	// If both are set, they should match the subscription id.
	if spk != nil && tweak != nil {
		sid := modules.DeriveRegistryEntryID(*spk, *tweak)
		if sid != j.staticRegistryEntryID {
			err := errors.New("subscription id doesn't match provided pubkey and tweak")
			build.Critical(err)
			sendResponse(nil, err)
			j.staticQueue.callReportFailure(err)
			span.LogKV("error", err)
			j.staticSpan.SetTag("success", false)
			return
		}
	}

	// Read the value.
	srv, err := lookupRegistry(w, j.staticRegistryEntryID, spk, tweak)
	if err != nil {
		sendResponse(nil, err)
		j.staticQueue.callReportFailure(err)
		span.LogKV("error", err)
		j.staticSpan.SetTag("success", false)
		return
	}

	// Check if we have a cached version of the looked up entry. If the new entry
	// has a higher revision number we update it. If it has a lower one we know that
	// the host should be punished for losing it or trying to cheat us.
	// TODO: update the cache to store the hash in addition to the revision
	// number for verifying the pow.
	if srv != nil {
		cachedRevision, cached := w.staticRegistryCache.Get(j.staticRegistryEntryID)
		if cached && cachedRevision > srv.Revision {
			sendResponse(nil, errHostLowerRevisionThanCache)
			j.staticQueue.callReportFailure(errHostLowerRevisionThanCache)
			span.LogKV("error", errHostLowerRevisionThanCache)
			j.staticSpan.SetTag("success", false)
			w.staticRegistryCache.Set(j.staticRegistryEntryID, *srv, true) // adjust the cache
			return
		} else if !cached || srv.Revision > cachedRevision {
			w.staticRegistryCache.Set(j.staticRegistryEntryID, *srv, false) // adjust the cache
		}
	}

	// Success.
	jobTime := time.Since(start)
	j.staticSpan.SetTag("success", true)

	// Send the response and report success.
	sendResponse(srv, nil)
	j.staticQueue.callReportSuccess()

	// Update the performance stats on the queue.
	jq := j.staticQueue.(*jobReadRegistryQueue)
	jq.mu.Lock()
	jq.weightedJobTime = expMovingAvg(jq.weightedJobTime, float64(jobTime), jobReadRegistryPerformanceDecay)
	jq.mu.Unlock()
}

// callExpectedBandwidth returns the bandwidth that is expected to be consumed
// by the job.
func (j *jobReadRegistry) callExpectedBandwidth() (ul, dl uint64) {
	return readRegistryJobExpectedBandwidth()
}

// initJobReadRegistryQueue will init the queue for the ReadRegistry jobs.
func (w *worker) initJobReadRegistryQueue() {
	// Sanity check that there is no existing job queue.
	if w.staticJobReadRegistryQueue != nil {
		w.staticRenter.staticLog.Critical("incorret call on initJobReadRegistryQueue")
		return
	}

	w.staticJobReadRegistryQueue = &jobReadRegistryQueue{
		jobGenericQueue: newJobGenericQueue(w),
	}
}

// ReadRegistry is a helper method to run a ReadRegistry job on a worker.
func (w *worker) ReadRegistry(ctx context.Context, spk types.SiaPublicKey, tweak crypto.Hash) (*modules.SignedRegistryValue, error) {
	readRegistryRespChan := make(chan *jobReadRegistryResponse)
	span := opentracing.GlobalTracer().StartSpan("ReadRegistry")
	defer span.Finish()

	jur := w.newJobReadRegistry(ctx, span, readRegistryRespChan, spk, tweak)

	// Add the job to the queue.
	if !w.staticJobReadRegistryQueue.callAdd(jur) {
		return nil, errors.New("worker unavailable")
	}

	// Wait for the response.
	var resp *jobReadRegistryResponse
	select {
	case <-ctx.Done():
		return nil, errors.New("ReadRegistry interrupted")
	case resp = <-readRegistryRespChan:
	}

	// Sanity check that the finish time was set.
	if resp.staticCompleteTime.IsZero() {
		build.Critical("finish time wasn't set")
	}
	return resp.staticSignedRegistryValue, resp.staticErr
}

// ReadRegistryEID is a helper method to run a ReadRegistry job on a worker
// without a pubkey or tweak.
func (w *worker) ReadRegistryEID(ctx context.Context, sid modules.RegistryEntryID) (*modules.SignedRegistryValue, error) {
	readRegistryRespChan := make(chan *jobReadRegistryResponse)
	span := opentracing.GlobalTracer().StartSpan("ReadRegistry")
	defer span.Finish()

	jur := w.newJobReadRegistrySID(ctx, span, readRegistryRespChan, sid, nil, nil)

	// Add the job to the queue.
	if !w.staticJobReadRegistryQueue.callAdd(jur) {
		return nil, errors.New("worker unavailable")
	}

	// Wait for the response.
	var resp *jobReadRegistryResponse
	select {
	case <-ctx.Done():
		return nil, errors.New("ReadRegistry interrupted")
	case resp = <-readRegistryRespChan:
	}

	// Sanity check that the finish time was set.
	if resp.staticCompleteTime.IsZero() {
		build.Critical("finish time wasn't set")
	}
	return resp.staticSignedRegistryValue, resp.staticErr
}

// readRegistryJobExpectedBandwidth is a helper function that returns the
// expected bandwidth consumption of a ReadRegistry job. This helper function
// enables getting at the expected bandwidth without having to instantiate a
// job.
func readRegistryJobExpectedBandwidth() (ul, dl uint64) {
	return ethernetMTU, ethernetMTU // a single frame each for upload and download
}
