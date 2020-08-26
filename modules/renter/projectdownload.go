package renter

// TODO: Need have price per ms per worker set somewhere by the user, with some
// sane default.

// TODO: Better handling of the time.After calls.

import (
	"bytes"
	"context"
	"math"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"

	"gitlab.com/NebulousLabs/errors"
)

// pieceDownload tracks a worker downloading a piece, whether that piece has
// returned, and what time the piece is/was expected to return.
//
// NOTE: The actual piece data is stored in the projectDownloadChunk after the
// download completes.
type pieceDownload struct {
	// 'completed', 'launched', and 'failed' are status variables for the piece.
	// If 'launched' is false, it means the piece download has not started yet.
	// Both 'completed' and 'failed' will also be false.
	//
	// If 'launched' is true and neither 'completed' nor 'failed' is true, it
	// means the download is in progress and the result is not known.
	//
	// Only one of 'completed' and 'failed' can be set to true. 'completed'
	// means the download was successful and the piece data was added to the
	// projectDownloadChunk. 'failed' means the download was unsuccessful.
	completed bool
	failed    bool
	launched  bool

	// expectedCompletionTime indicates the time when the download is expected
	// to complete. This is used to determine whether or not a download is late.
	expectedCompletionTime time.Time

	worker *worker
}

// projectDownloadChunk is a bunch of state that helps to orchestrate a download
// from a projectChunkWorkerSet.
//
// The projectDownloadChunk is only ever accessed by a single thread which
// orchestrates the download, which means that it does not need to be thread
// safe.
type projectDownloadChunk struct {
	// Parameters for downloading within the chunk.
	chunkLength uint64
	chunkOffset uint64
	pricePerMS  types.Currency

	// Values derived from the chunk download parameters. The offset and length
	// specify the offset and length that will be sent to the host, which much
	// be segment aligned.
	pieceLength uint64
	pieceOffset uint64

	// availablePieces are pieces where there are one or more workers that have
	// been tasked with fetching the piece.
	//
	// workersConsidered is a map of which workers have been moved from the
	// worker set's list of available pieces to the download chunk's list of
	// available pieces. This enables the worker selection code to realize which
	// pieces in the worker set have been resolved since the last check.
	availablePieces        [][]pieceDownload
	workersConsideredIndex uint64

	// dataPieces is the buffer that is used to place data as it comes back.
	// There is one piece per chunk, and pieces can be nil. To know if the
	// download is complete, the number of non-nil pieces will be counted.
	dataPieces [][]byte

	// The completed data gets sent down the response chan once the full
	// download is done.
	ctx                  context.Context
	downloadResponseChan chan *downloadResponse
	workerResponseChan   chan *jobReadResponse
	workerSet            *projectChunkWorkerSet
	workerState          *pcwsWorkerState
}

// downloadResponse is sent via a channel to the caller of
// 'projectChunkWorkerSet.managedDownload'.
type downloadResponse struct {
	data []byte
	err  error
}

// unresolvedWorkers will return the set of unresolved workers from the worker
// state of the pdc. This operation will also update the set of available pieces
// within the pdc to reflect any previously unresolved workers that are now
// available workers.
//
// A channel will also be returned which will be closed when there are new
// unresolved workers available.
func (pdc *projectDownloadChunk) unresolvedWorkers() ([]*pcwsUnresolvedWorker, <-chan struct{}) {
	ws := pdc.workerState
	ws.mu.Lock()
	defer ws.mu.Unlock()

	var unresolvedWorkers []*pcwsUnresolvedWorker
	for _, uw := range ws.unresolvedWorkers {
		unresolvedWorkers = append(unresolvedWorkers, uw)
	}
	// Add any new resolved workers to the pdc's list of available pieces.
	for i := pdc.workersConsideredIndex; i < len(ws.resolvedWorkers); i++ {
		// Add the returned worker to available pieces for each piece that the
		// resolved worker has.
		resp := ws.resolveedWorkers[i]
		for _, pieceIndex := range resp.pieceIndices {
			pdc.availablePieces[pieceIndex] = append(pdc.availablePieces[pieceIndex], pieceDownload{
				worker: resp.worker,
			})
		}
	}
	pdc.workersConsideredIndex = len(pdc.workerState.reseolvedWorkers)

	// If there are more unresolved workers, fetch a channel that will be closed
	// when more results from unresolved workers are available.
	return unresolvedWorkers, ws.registerForWorkerUpdate()
}

// bestOverdriveUnresolvedWorker will scan through a proveded list of unresolved
// workers and find the best one to use as an overdrive worker.
//
// Three values are returned. The first signifies whether the best worker is
// late. If so, any worker that is resolved should be preferred over any worker
// that is unresolved.
//
// The second return value is the unresolved duration. This is a modified
// duration based on the combination of the amount of time until the worker has
// completed its task plus the amount of time penalty the worker incurs for
// being expensive.
//
// The final return value is a wait duration, which indicates how much time
// needs to elapse before the best unresolved worker flips over into being a
// late worker.
func (pdc *projectDownloadChunk) bestOverdriveUnresolvedWorker(puws []*pcwsUnresolvedWorker) (exists, late bool, duration, waitDuration time.Duration) {
	// Set the duration and late status to the most pessimistic value.
	exists = false
	late = true
	duration = time.Duration(math.MaxInt64)
	waitDuration = time.Duration(math.MaxInt64)

	// Loop through the unresovled workers and find the best unresovled worker.
	for _, uw := range puws {
		// Figure how much time is expected to remain until the worker is
		// avaialble. Note that no price penatly is attached to the HasSector
		// call, because that call is being made regardless of the cost.
		hasSectorTime := time.Until(uw.staticExpectedCompletionTime)
		if hasSectorTime < 0 {
			hasSectorTime = 0
		}
		// Skip this worker if the best is not late but this worker is late.
		late := hasSectorTime <= 0
		if late && !bestWorkerLate {
			continue
		}

		// Figure out how much time is expected until the worker completes the
		// download job.
		//
		// TODO: Break into helper function and unit test.
		readTime := uw.staticWorker.staticJobReadQueue.callExpectedJobTime(pdc.pieceLength)
		if readTime < 0 {
			readTime = 0
		}
		// Add a penalty to performance based on the cost of the job. Need to be
		// careful with the underflow cases.
		//
		// TODO: Break into helper function and unit test.
		if pdc.pricePerMS.IsZero() {
			pdc.pricePerMS = types.NewCurrency64(1)
		}
		expectedRSCompletionCost := uw.staticWorker.staticJobReadQueue.callExpectedJobCost(pdc.pieceLength)
		rsPricePenalty, err := expectedRSCompletionCost.Div(pdc.pricePerMS).Uint64()
		if err != nil || rsPricePenalty > math.MaxInt64 {
			readTime = time.Duration(math.MaxInt64)
		} else if reduced := math.MaxInt64 - int64(rsPricePenalty); int64(readTime) > reduced {
			readTime = time.Duration(math.MaxInt64)
		} else {
			readTime += time.Duration(rsPricePenalty)
		}
		adjustedTotalDuration := hasSectorTime + readTime

		// Compare the total time (including price preference) to the current
		// best time. Workers that are not late get preference over workers that
		// are late.
		betterLateStatus := bestWorkerLate && !late
		betterDuration := adjustedTotalDuration < bestUnresolvedDuration
		if betterLateStatus || betterDuration {
			bestWorkerExists = true
			bestUnresolvedDuration = adjustedTotalDuration
			if !late {
				bestUnresolvedWaitDuration = hasSectorTime
				bestWorkerLate = false
			}
		}
	}
	return bestWorkerLate, bestUnresolvedDuration, bestUnresolvedWaitDuration
}

// findBestWorker will look at all of the workers that can help fetch a new
// piece. If a good worker is found, that worker is returned along with the
// index of the piece it should fetch. If no good worker is found but a good
// worker is in the queue, two channels will be returned, either of which can
// signal that a new worker may be available.
//
// An error will only be returned if there are not enough workers to complete
// the download. If there are enough workers to complete the download, but all
// potential workers have already been launched, all of the return values will
// be nil.
//
// If there are no workers at all that may be able to contribute, an error is
// returned, which means the download must either proceed with its current set
// of workers or must fail.
//
// NOTE: There is an edge case where all of the pieces are already active. In
// this case, a worker may be returned that overlaps with another worker. From
// an erasure coding perspective, this is inefficient, but can be useful if
// there is an expectation that lots of existing workers will fail.
//
// TODO: Need to migrate over any resolved workers. Currently we assume that has
// already happened.
func (pdc *projectDownloadChunk) findBestOverdriveWorker() (*worker, uint64, time.Duration, <-chan struct{}) {
	// Find the best unresolved worker. The return values include an 'adjusted
	// duration', which indicates how long the worker takes accounting for
	// pricing, and the 'wait duration', which is the max amount of time that we
	// should wait for this worker to return with a result from HasSector.
	//
	// If the best unresolved worker is late, the wait duration will be zero.
	// Technically these values are redundant but the code felt cleaner to
	// return them explicitly.
	//
	// buw = bestUnresolvedWorker
	unresolvedWorkers := pdc.unresolvedWorkers()
	buwExists, buwLate, buwAdjustedDuration, buwWaitDuration := pdc.bestUnresolvedWorker(unresolvedWorkers)

	// Loop through the set of pieces to find the fastest worker that can be
	// launched. Because this function is only called for overdrive workers, we
	// can assume that any launched piece is already late.
	//
	// baw = bestAvailableWorker
	bawExists := false
	bawAdjustedDuration := time.Duration(math.MaxInt64)
	bawPieceIndex = 0
	var baw *worker
	for i, activePiece := range pdc.availablePieces {
		// This piece should be skipped if it is completed.
		complete := false
		for _, pieceDownload := range activePiece {
			// No need to check the rest of the pieces if this piece is
			// complete.
			if pieceDownload.completed {
				complete = true
				break
			}

			// Determine if this worker is better than any existing worker.
			workerAdjustedDuration := pdc.getAdjustedDuration(pieceDownload.worker)
			if workerAdjustedDuration < bawAdjustedDuration {
				bawExists = true
				bawAdjustedDuration = workerAdjustedDuration
				bawPieceIndex = i
				baw = pieceDownload.worker
			}
		}
		// Don't consider any workers from this piece if the piece is completed.
		if complete {
			continue
		}
	}

	// Return nil if there are no workers that can be launched.
	if !buwExists && !bawExists {
		// All 'nil' return values, meaning the download can succeed by waiting
		// for already launched workers to return, but cannot succeed by
		// launching new workers because no new workers are available.
		return nil, 0, 0, nil
	}

	// Return the buw values unconditionally if there is no baw. Also return the
	// buw if the buw is not late and has a better duration than the baw.
	//
	// TODO: The registration here is a race condition because new workers may
	// have come back since the list of unresolved workers was requested. Need
	// to move this to inside of the lock where we grab the list of unresolved
	// workers.
	buwNoBaw = buwExists && !bawExists
	buwBetter = !buwLate && buwAdjustedDuration < bawAdjustedDuration
	if buwNoBaw || buwBetter {
		ws.mu.Lock()
		c := ws.registerForWorkerUpdate()
		ws.mu.Unlock()
		return nil, 0, buwWaitDuration, c
	}

	// Retrun the baw.
	return baw, bawPieceIndex, 0, nil
}

// waitForWorker will block until one of the best workers is available to be
// used for download, along with the index of the piece that should be
// downloaded. An error will be returned if there are no new workers available
// that can be launched.
//
// TODO: Need thorough testing to ensure that repeated calls to findBestWorker
// eventually fail. I guess the context timeout actually handles most of this.
//
// TODO: Do something about that time.After
func (pdc *projectDownloadChunk) waitForWorker() (*worker, uint64, bool) {
	for {
		worker, pieceIndex, sleepTime, wakeChan := pdc.findBestWorker()
		// If there was a worker found, return that worker.
		if worker != nil {
			return worker, pieceIndex, true
		}
		if wakeChan == nil {
			return nil, 0, false
		}

		// If there was no worker found, sleep until we should call
		// findBestWorker again.
		maxSleep := time.After(sleepTime)
		select {
		case <-maxSleep:
		case <-wakeChan:
		case <-pdc.ctx.Done():
			return nil, 0, false
		}
	}
}

// launchWorker will launch a worker for the download project. An error
// will be returned if there is no worker to launch.
func (pdc *projectDownloadChunk) launchWorker() bool {
	// Loop until either a worker succeeds in launching a job, or until there
	// are no more workers to return. The exit condition for this loop depends
	// on waitForWorker() being guaranteed to return an error if the workers
	// keep failing. When a worker fails, we set 'pieceDownload.failed' to true,
	// which causes waitForWorker to ignore that worker option in the future.
	// There are a finite number of worker options total.
	for {
		// An error here means that no more workers are available at all.
		w, pieceIndex, workerAvailable := pdc.waitForWorker()
		if !workerAvailable {
			return false
		}

		// Create the read sector job for the worker.
		//
		// TODO: The launch process should minimally have as input the ctx of
		// the pdc, that way if the pdc closes we know to garbage collect the
		// channel and not send down it. Ideally we can even cancel the job if
		// it is in-flight.
		jrs := &jobReadSector{
			jobRead: jobRead{
				staticResponseChan: pdc.workerResponseChan,
				staticLength:       pdc.pieceLength,

				staticSector: pdc.workerSet.staticPieceRoots[pieceIndex],

				jobGeneric: newJobGeneric(w.staticJobReadQueue, pdc.ctx.Done()),
			},
			staticOffset: pdc.pieceOffset,
		}

		// Launch the job and then update the status of the worker. Either way,
		// the worker should be marked as 'launched'. If the job is not
		// successfully queued, the worker should be marked as 'failed' as well.
		//
		// NOTE: We don't break out of the loop when we find a piece/worker
		// match. If all is going well, each worker should appear at most once
		// in this piece, but for the sake of defensive programming we check all
		// elements anyway.
		expectedCompletionTime, added := w.staticJobReadQueue.callAddWithEstimate(jrs)
		for _, pieceDownload := range pdc.availablePieces[pieceIndex] {
			if w.staticHostPubKeyStr == pieceDownload.worker.staticHostPubKeyStr {
				pieceDownload.launched = true
				if added {
					pieceDownload.expectedCompletionTime = expectedCompletionTime
				} else {
					pieceDownload.failed = true
				}
			}
		}
		// If there was no error in adding the worker, a worker has successfully
		// been launched. Otherwise, try to grab another worker.
		if added {
			return true
		}
	}
}

// handleJobReadResponse will take a jobReadResponse from a worker job
// and integrate it into the set of pieces.
func (pdc *projectDownloadChunk) handleJobReadResponse(jrr *jobReadResponse) {
	// Prevent a production panic.
	if jrr == nil {
		pdc.workerSet.staticRenter.log.Critical("received nil job read response in handleJobReadResponse")
		return
	}

	// Figure out which index this read corresponds to.
	pieceIndex := 0
	for i, root := range pdc.workerSet.staticPieceRoots {
		if jrr.staticSectorRoot == root {
			pieceIndex = i
			break
		}
	}

	// Check whether the job failed.
	if jrr.staticErr != nil {
		// TODO: Log? - we should probably have toggle-able log levels for stuff
		// like this. Maybe a worker.log which allows us to turn on logging just
		// for specific workers.
		//
		// The download failed, update the pdc available pieces to reflect the
		// failure.
		for i := 0; i < len(pdc.availablePieces[pieceIndex]); i++ {
			if pdc.availablePieces[pieceIndex][i].worker.staticHostPubKeyStr == jrr.staticWorker.staticHostPubKeyStr {
				pdc.availablePieces[pieceIndex][i].failed = true
			}
		}
		return
	}

	// The download succeeded, add the piece to the appropriate index.
	pdc.dataPieces[pieceIndex] = jrr.staticData
	jrr.staticData = nil // Just in case there's a reference to the job reponse elsewhere.
	for i := 0; i < len(pdc.availablePieces[pieceIndex]); i++ {
		if pdc.availablePieces[pieceIndex][i].worker.staticHostPubKeyStr == jrr.staticWorker.staticHostPubKeyStr {
			pdc.availablePieces[pieceIndex][i].completed = true
		}
	}
}

// fail will send an error down the download response channel.
func (pdc *projectDownloadChunk) fail(err error) {
	dr := &downloadResponse{
		data: nil,
		err:  err,
	}
	pdc.downloadResponseChan <- dr
}

// finalize will take the completed pieces of the download, decode them,
// and then send the result down the response channel. If there is an error
// during decode, 'pdc.fail()' will be called.
func (pdc *projectDownloadChunk) finalize() {
	// Helper variable.
	ec := pdc.workerSet.staticErasureCoder

	// The chunk download offset and chunk download length are different from
	// the requested offset and length because the chunk download offset and
	// length are required to be
	//
	// NOTE: This is one of the places where we assume we are using maximum
	// distance separable erasure codes.
	chunkDLOffset := pdc.pieceOffset * uint64(ec.MinPieces())
	chunkDLLength := pdc.pieceLength * uint64(ec.MinPieces())

	// Recover the pieces in to a single byte slice.
	buf := bytes.NewBuffer(nil)
	err := pdc.workerSet.staticErasureCoder.Recover(pdc.dataPieces, chunkDLOffset+chunkDLLength, buf)
	if err != nil {
		pdc.fail(errors.AddContext(err, "unable to complete erasure decode of download"))
		return
	}
	data := buf.Bytes()

	// The full set of data is recovered, truncate it down to just the pieces of
	// data requested by the user and return.
	//
	// TODO: Unit test this.
	chunkStartWithinData := pdc.chunkOffset - chunkDLOffset
	chunkEndWithinData := pdc.chunkLength + chunkStartWithinData
	data = data[chunkStartWithinData:chunkEndWithinData]

	// Return the data to the caller.
	dr := &downloadResponse{
		data: data,
		err:  nil,
	}
	pdc.downloadResponseChan <- dr
}

// finished returns true if the download is finished, and returns an error if
// the download is unable to complete.
//
// TODO: Unit test this.
func (pdc *projectDownloadChunk) finished() (bool, error) {
	// Convenience variables.
	ws := pdc.workerState
	ec := pdc.workerSet.staticErasureCoder

	// Count the number of completed pieces and hopefuly pieces in our list of
	// potential downloads.
	completedPieces := 0
	hopefulPieces := 0
	for _, piece := range pdc.availablePieces {
		// Only count one piece as hopeful per set.
		hopeful := false
		for _, pieceDownload := range piece {
			// If this piece is completed, count it both as hopeful and
			// completed, no need to look at other pieces.
			if pieceDownload.completed {
				hopeful = true
				completedPieces++
				break
			}
			// If this piece has not yet failed, it is hopeful. Keep looking
			// through the pieces in case there is a completed piece.
			if !pieceDownload.failed {
				hopeful = true
			}
		}
		if hopeful {
			hopefulPieces++
		}
	}
	if completedPieces >= ec.MinPieces() {
		return true, nil
	}

	// Count the number of workers that haven't completed their results yet.
	//
	// TODO: Is using this here a race condition?
	ws.mu.Lock()
	hopefulPieces += len(ws.unresolvedWorkers)
	ws.mu.Unlock()

	// Ensure that there are enough pieces that could potentially become
	// completed to finish the download.
	if hopefulPieces < ec.MinPieces() {
		return false, errors.New("not enough pieces to complete download")
	}
	return false, nil
}

// needsOverdrive returns true if the function determines that another piece
// should be launched to assist with the current download.
//
// TODO: Unit test this.
func (pdc *projectDownloadChunk) needsOverdrive() (time.Duration, bool) {
	// Go through the pieces, determining how many pieces are launched without
	// fail, and what the longest return time is of all the workers that have
	// already been launched.
	numLWF := 0
	var latestReturn time.Time
	for _, piece := range pdc.availablePieces {
		launchedWithoutFail := false
		for _, pieceDownload := range piece {
			if pieceDownload.launched && !pieceDownload.failed {
				launchedWithoutFail = true
				if !pieceDownload.completed && latestReturn.Before(pieceDownload.expectedCompletionTime) {
					latestReturn = pieceDownload.expectedCompletionTime
				}
			}
		}
		if launchedWithoutFail {
			numLWF++
		}
	}

	// If the number of pieces that have launched without fail is less than the
	// minimum number of pieces need to complete a download, overdrive is
	// required.
	if numLWF < pdc.workerSet.staticErasureCoder.MinPieces() {
		// No need to return a time, need to launch a worker immediately.
		return 0, true
	}

	// If the latest worker should have already returned, signal than an
	// overdrive worker should be launched immediately.
	untilLatest := time.Until(latestReturn)
	if untilLatest <= 0 {
		return 0, true
	}

	// There are enough workers out, and it is expected that not all of them
	// have returned yet. Signal that we should check again 50 milliseconds
	// after the latest worker has failed to return.
	//
	// Note that doing things this way means that launching new workers will
	// cause the latest time returned to reflect their latest time - each time
	// an overdrive worker is launched, we will wait the full return period
	// before launching another one.
	return (untilLatest + time.Millisecond*50), false
}

// threadedCollectAndOverdrivePieces is the maintenance function of the download
// process.
//
// NOTE: One potential optimization here is to pre-emptively launch a few
// overdrive pieces, in a similar fashion that the other code does. We can do it
// more intelligently here though, tracking over time what percentage of
// downloads comlete without failure, and what percentage of downloads end up
// needing overdrive pieces. Then we can use that failure rate to determine how
// often we should pre-emtively launch some overdrive pieces rather than wait
// for the failures to happen. This many not be necessary at all if the failure
// rates are low enough.
//
// TODO: WaitForWorker currently blocks our ability to recieve new jobs that
// come back while we are waiting for overdrive to complete... which means that
// we actually need to launch the next worker in a background thread.
func (pdc *projectDownloadChunk) threadedCollectAndOverdrivePieces() {
	// Loop until the download has either failed or completed.
	for {
		// Check whether the download is comlete. An error means that the
		// download has failed and can no longer make progress.
		completed, err := pdc.finished()
		if completed {
			pdc.finalize()
			return
		}
		if err != nil {
			pdc.fail(err)
			return
		}

		// Drain the response chan of any results that have been submitted.
		select {
		case jrr := <-pdc.workerResponseChan:
			pdc.handleJobReadResponse(jrr)
			continue
		case <-pdc.ctx.Done():
			pdc.fail(errors.New("download failed while waiting for responses"))
			return
		}

		// Run logic to determine whether or not we should kick off an overdrive
		// worker. We skip checking the error on the launch because
		// 'pdc.finished()' will catch the error on the next iteration of the
		// outer loop.
		overdriveTimeout, needsOverdrive := pdc.needsOverdrive()
		if needsOverdrive {
			// If the launch is unsuccessful, it means that there are no more
			// workers which can be launched.
			pdc.launchWorker()
		}

		// Determine when the next overdrive check needs to run.
		//
		// TODO: Should be able to create a cache for the timer in the pdc,
		// which hopefully allows us to avoid the memory allocation cost
		// associated with calling time.After() a bunch of times.
		overdriveTimeoutChan := time.After(overdriveTimeout)
		select {
		case jrr := <-pdc.workerResponseChan:
			pdc.handleJobReadResponse(jrr)
			continue
		case <-pdc.ctx.Done():
			pdc.fail(errors.New("download failed while waiting for responses"))
			return
		case <-overdriveTimeoutChan:
			continue
		}
	}
}

// getPieceOffsetAndLen is a helper function to compute the piece offset and
// length of a chunk download, given the erasure coder for the chunk, the offset
// within the chunk, and the length within the chunk.
func getPieceOffsetAndLen(ec modules.ErasureCoder, offset, length uint64) (pieceOffset, pieceLength uint64) {
	// Fetch the segment size of the ec.
	pieceSegmentSize, partialsSupported := ec.SupportsPartialEncoding()
	if !partialsSupported {
		// If partials are not supported, the full piece needs to be downloaded.
		pieceSegmentSize = modules.SectorSize
	}

	// Consistency check some of the erasure coder values. If the check fails,
	// return that the whole piece must be downloaded.
	if pieceSegmentSize == 0 || pieceSegmentSize%crypto.SegmentSize != 0 {
		build.Critical("pcws has a bad erasure coder")
		return 0, modules.SectorSize
	}

	// Determine the download offset within a single piece. We get this by
	// dividing the chunk offset by the number of pieces and then rounding
	// down to the nearest segment size.
	//
	// This is mathematically equivalent to rounding down the chunk size to
	// the nearest chunk segment size and then dividing by the number of
	// pieces.
	pieceOffset = offset / uint64(ec.MinPieces())
	pieceOffset = pieceOffset / pieceSegmentSize
	pieceOffset = pieceOffset * pieceSegmentSize

	// Determine the length that needs to be downloaded. This is done by
	// determining the offset that the download needs to reach, and then
	// subtracting the pieceOffset from the termination offset.
	chunkSegmentSize := pieceSegmentSize * uint64(ec.MinPieces())
	chunkTerminationOffset := offset + length
	overflow := chunkTerminationOffset % chunkSegmentSize
	if overflow != 0 {
		chunkTerminationOffset += chunkSegmentSize - overflow
	}
	pieceTerminationOffset := chunkTerminationOffset / pieceSegmentSize
	pieceLength = pieceTerminationOffset - pieceOffset
	return pieceOffset, pieceLength
}

// 'managedDownload' will download a range from a chunk. This call is
// asynchronous. It will return as soon as the sector download requests have
// been sent to the workers. This means that it will block until enough workers
// have reported back with HasSector results that the optimal download request
// can be made. Where possible, the projectChunkWorkerSet should be created in
// advance of the download call, so that the HasSector calls have as long as
// possible to complete, cutting significant latency off of the download.
//
// Blocking until all of the piece downloads have been put into job queues
// ensures that the workers will account for the bandwidth overheads associated
// with these jobs before new downloads are requested. Multiple calls to
// 'managedDownload' from the same thread will generally follow the rule that
// the first calls will return first. This rule cannot be enforced if the call
// to managedDownload returns before the download jobs are queued into the
// workers.
//
// pricePerMS is "price per millisecond". To add a price preference to picking
// hosts to download from, the total cost of performing a download will be
// converted into a number of milliseconds based on the pricePerMS. This cost
// will be added to the return time of the host, meaning the host will be
// selected as though it is slower.
//
// NOTE: A lot of this code is not future proof against certain classes of
// encryption algorithms and erasure coding algorithms, however I believe that
// the properties we have in our current set (maximum distance separable erasure
// codes, tweakable encryption algorithms) are not likely to change in the
// future.
func (pcws *projectChunkWorkerSet) managedDownload(ctx context.Context, pricePerMS types.Currency, offset, length uint64) (chan *downloadResponse, error) {
	// Convenience variables.
	ec := pcws.staticErasureCoder

	// Check encryption type. If the encryption overhead is not zero, the piece
	// offset and length need to download the full chunk. This is due to the
	// overhead being a checksum that has to be verified against the entire
	// piece.
	//
	// NOTE: These checks assume that any upload with encryption overhead needs
	// to be downloaded as full sectors. This feels reasonable because smaller
	// sectors were not supported when encryption schemes with overhead were
	// being suggested.
	if pcws.staticMasterKey.Type().Overhead() != 0 && (offset != 0 || length != modules.SectorSize*uint64(ec.MinPieces())) {
		return nil, errors.New("invalid request performed - this chunk has encryption overhead and therefore the full chunk must be downloaded")
	}

	// Determine the offset and length that needs to be downloaded from the
	// pieces. This is non-trivial because both the network itself and also the
	// erasure coder have required segment sizes.
	pieceOffset, pieceLength := getPieceOffsetAndLen(ec, offset, length)

	// Refresh the pcws. This will only cause a refresh if one is necessary.
	err := pcws.managedTryUpdateWorkerState()
	if err != nil {
		return nil, errors.AddContext(err, "unable to initiate download")
	}
	// After refresh, grab the worker state.
	pcws.mu.Lock()
	ws := pcws.workerState
	pcws.mu.Unlock()

	// Create the workerResponseChan.
	//
	// The worker response chan is allocated to be quite large. This is because
	// in the worst case, the total number of jobs submitted will be equal to
	// the number of workers multiplied by the number of pieces. We do not want
	// workers blocking when they are trying to send down the channel, so a very
	// large buffered channel is used. Each element in the channel is only 8
	// bytes (it is just a pointer), so allocating a large buffer doesn't
	// actually have too much overhead. Instead of buffering for a full
	// workers*pieces slots, we buffer for pieces*5 slots, under the assumption
	// that the overdrive code is not going to be so aggressive that 5x or more
	// overhead on download will be needed.
	//
	// TODO: If this ends up being a problem, we could implement the jobs
	// process to send the result down a channel in goroutine if the first
	// attempt to send the job fails. Then we could probably get away with a
	// smaller buffer, since exceeding the limit currently would cause a worker
	// to stall, where as with the goroutine-on-block method, exceeding the
	// limit merely causes extra goroutines to be spawned.
	workerResponseChan := make(chan *jobReadResponse, ec.NumPieces()*5)

	// Build the full pdc.
	pdc := &projectDownloadChunk{
		chunkOffset: offset,
		chunkLength: length,
		pricePerMS:  pricePerMS,

		pieceOffset: pieceOffset,
		pieceLength: pieceLength,

		availablePieces:   make([][]pieceDownload, ec.NumPieces()),

		dataPieces: make([][]byte, ec.NumPieces()),

		ctx:                  ctx,
		workerResponseChan:   workerResponseChan,
		downloadResponseChan: make(chan *downloadResponse, 1),
		workerSet:            pcws,
		workerState:          ws,
	}

	// Launch enough workers to complete the download. The overdrive code will
	// determine whether more pieces need to be launched.
	//
	// TODO: Probably change this to launch all of the workers at once.
	for i := 0; i < pcws.staticErasureCoder.MinPieces(); i++ {
		// Try launching a worker. If the launch fails, it means that no workers
		// can be launched, and therefore the download cannot complete.
		err := pdc.launchWorker()
		if err != nil {
			return nil, errors.AddContext(err, "not enough workers to kick off the download")
		}
	}

	// All initial workers have been launched. The function can return now,
	// unblocking the caller. A background thread will be launched to collect
	// the reponses and launch overdrive workers when necessary.
	go pdc.threadedCollectAndOverdrivePieces()
	return pdc.downloadResponseChan, nil
}
