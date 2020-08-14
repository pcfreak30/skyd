package renter

// TODO: Need to pick up in the worker launch function, we don't update the pdc
// at the moment to reflect that a piece has been launched.

// TODO: We don't actually make use of the available pieces at all in the
// projectChunkWorkerSet. So the updates don't ever actually propagate from the
// pcws, meaning that the project download literally cannot make progress.

// TODO: Uncertain: how do we prevent one worker from getting a huge backlog and
// consuming a ton of memory? At some point do we just declare that the worker
// is overwhlemed and unable to take on more work? Do we have requests block?
// How do we know when we should block vs. just ignore a worker? If there's just
// one worker that is overwhelmed, we want to start ignoring that worker. If a
// bunch of workers are overwhelmed though, we want to actually just back off
// and block while they clear out. Or rather, we just care about keeping the
// queues reasonably balanced. If a bunch of workers are full, just keep filling
// them and let them do their thing. If a small number of workers is full, then
// just have those workers start rejecting things from their queue.
//
// Similar vein here - we may want workers to be able to re-order their queues
// to put high priority stuff through faster, not sure though. Well, basically
// instead of having this memory roll-through like we do currently, what we
// really want to do is just block until a sufficient number of workers have a
// low queue. Then for high priority requests we just slam those right through.

// TODO: Remember to establish the piece parameters / derived parameters when
// building the download chunk. This includes init'ing fields like pricePerMS.

// TODO: We should probably bake in a reliability penalty. Workers that
// sometimes fail should get some automatic milliseconds tagged to them because
// they will sometimes cause downloads to be late. Perhaps a super-linear curve,
// for example a 30% reliable worker is just not waiting for at all. We can do
// this after we ship the first draft of everything.

// TODO: Need have price per ms per worker set somewhere by the user.

// TODO: Do we need a mutex on the pdc at all? I think it is only accessed by
// one thread ever.

// TODO: I was lazy and used time.After everywhere.

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"

	"gitlab.com/NebulousLabs/errors"
)

// workerRank is a system for ranking workers based on how well the owrker has
// been performing and what pieces of a download a worker is capable of
// fetching.
type workerRank uint64

var (
	// workerRankErr implies that the workerRank value was initialized
	// incorrectly.
	workerRankErr = 0

	// workerRankUnlaunchedPiece means that the worker is in good standing and
	// can fetch a piece for which no other worker has launched.
	workerRankUnlaunchedPiece = 1

	// workerRankLaunchedPiece means that the worker is in good standing and can
	// fetch a piece that another worker launched to fetch. That other worker
	// however is not in good standing.
	workerRankLaunchedPiece = 2

	// workerRankActivePiece means that the worker is in good standing and can
	// fetch a piece that another worker launched to fetch. That other worker is
	// still in good standing.
	workerRankActivePiece = 3

	// workerRankLate means that the worker was launched to fetch another piece,
	// and the worker is late in returning that piece.
	workerRankLate = 4
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
	chunkOffset uint64
	chunkLegnth uint64
	pricePerMS  types.Currency

	// Values derived from the chunk download parameters. The offset and length
	// specify the offset and length that will be sent to the host, which much
	// be segment aligned.
	pieceOffset uint64
	pieceLength uint64

	// activiePieces are pieces where there are one or more workers that have
	// been tasked with fetching the piece.
	//
	// TODO: Need to rename this, 'active' has a different/overloaded meaning
	// now.
	//
	// TODO: Need to rethink a bit the relationship here between the
	// activePieces (to be renamed) and the pcws.availablePieces.
	activePieces [][]pieceDownload

	// pieces is the buffer that is used to place data as it comes back. There
	// is one piece per chunk, and pieces can be nil. To know if the download is
	// complete, the number of non-nil pieces will be counted.
	pieces [][]byte

	// The completed data gets sent down the response chan once the full
	// download is done.
	ctx                  context.Context
	downloadResponseChan chan *downloadResponse
	workerResponseChan   chan *jobReadResponse
	workerSet            *projectChunkWorkerSet
}

// downloadResponse is sent via a channel to the caller of
// 'projectChunkWorkerSet.managedDownload'.
type downloadResponse struct {
	data []byte
	err  error
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
func (pdc *projectDownloadChunk) findBestWorker() (*worker, int, time.Duration, chan<- struct{}, error) {
	// Helper variables.
	//
	// NOTE: pricePerMSPerWorker is actually going to be an over-estimate in
	// most cases. In the worst case, a renter is going to need to pay every
	// worker to speed up, but in a typical case going X milliseconds faster
	// only requires cycling out 1-2 slow workers rather than completely
	// choosing a different set of workers. The code to make this more accurate
	// in the typical case is more complex than is worth implementing at this
	// time.
	ws := pdc.staticWorkerSet
	pricePerMSPerWorker := pdc.staticPricePerMS.Mul64(uint64(ws.staticErasureCoder.MinPieces()))

	// For lock safety, need to fetch the list of unresolved workers separately
	// from learning the best time. For thread safety, the update channel needs
	// to be created at the moment that we observe the set of unresovled
	// workers.
	var unresolvedWorkers []*pcwsUnresolvedWorker
	ws.mu.Lock()
	for _, uw := range ws.unresolvedWorkers {
		unresolvedWorkers = append(unresolvedWorkers, uw)
	}
	ws.mu.Unlock()

	// Find the best duration of any unresolved worker. This will be compared to
	// the best duration of any resolved worker which could be put to work on a
	// piece.
	//
	// bestUnresolvedWaitTime is the amount of time that the renter should wait
	// to expect to hear back from the worker.
	nullDuration := time.Duration(math.MaxInt64)
	bestUnresolvedDuration := nullDuration
	bestWorkerLate := true
	var bestUnresolvedWaitTime time.Duration
	for _, uw := range unresolvedWorkers {
		// Figure how much time is expected to remain until the worker is
		// avaialble. Note that no price penatly is attached to the HasSector
		// call, because that call is being made regardless of the cost.
		hasSectorTime := time.Until(uw.staticExpectedCompletionTime)
		if hasSectorTime < 0 {
			hasSectorTime = 0
		}

		// Figure out how much time is expected until the worker completes the
		// download job.
		readTime := uw.staticWorker.staticJobReadQueue.callExpectedJobTime(pdc.staticPieceLength)
		if readTime < 0 {
			readTime = 0
		}
		// Add a penalty to performance based on the cost of the job. Need to be
		// careful with the underflow cases.
		expectedRSCompletionCost := uw.staticWorker.staticJobReadQueue.callExpectedJobCost(pdc.staticPieceLength)
		rsPricePenalty, err := expectedRSCompletionCost.Div(pricePerMSPerWorker).Uint64()
		if err != nil || rsPricePenalty > math.MaxInt64 {
			readTime = time.Duration(math.MaxInt64)
		} else if reduced := math.MaxInt64 - int64(rsPricePenalty); int64(readTime) > reduced {
			readTime = time.Duration(math.MaxInt64)
		} else {
			readTime += time.Duration(rsPricePenalty)
		}

		// Compare the total time (including price preference) to the current
		// best time. Workers that are not late get preference over workers that
		// are late.
		adjustedTotalDuration := hasSectorTime + readTime
		betterLateStatus := bestWorkerLate && hasSectorTime > 0
		betterDuration := adjustedTotalDuration < bestUnresolvedDuration
		if betterLateStatus || betterDuration {
			bestUnresolvedDuration = adjustedTotalDuration
		}
		if hasSectorTime > 0 && betterDuration {
			// bestUnresolvedWaitTime should be set to 0 unless the best
			// unresolved worker is not late.
			bestUnresolvedWaitTime = hasSectorTime
		}
		// The first time we find a worker that is not late, the best worker is
		// not late. Marking this several times is not an issue.
		if hasSectorTime > 0 {
			bestWorkerLate = false
		}
	}

	// TODO: May want to adjust bestUnresolvedWaitTime to account for noise in
	// the return time of the worker. This may require some additional machinery
	// being put along site the wait estimate, or maybe just requires the wait
	// estimate to be conservative.

	// Copy the list of resolved workers.
	now := time.Now() // So we aren't calling time.Now() a ton. It's a little expensive.
	// Count how many pieces could be fetched where no unfailed workers have
	// been launched for the piece yet.
	piecesAvailableToLaunch := 0
	// Track whether there are any unlaunched workers at all. If this is false
	// and there are also no unresolved workers, then no additional workers can
	// be launched at all, and the find workers function should terminate.
	unlaunchedWorkersAvailable := false
	// Track whether there are any pieces that don't have a worker launched
	// which hasn't failed. This is to determine the rank of any unresolved
	// workers. If there are unlaunched pieces, the rank of any unresolved
	// workers is 'workerRankUnlaunchedPiece'.
	unlaunchedPieces := false
	// Track whether there are any pieces where there are workers that have
	// launched and not failed, but all workers that have launched and not
	// failed are late. This is to determine the rank of any unresovled workers.
	// If there are inactive pieces and no unlaunched pieces, the rank of any
	// unresolved workers is 'workerRankLaunchedPiece'. If there are no inactive
	// pieces and also no unlaunched pieces, the rank of any unresolved workers
	// is 'workerRankActivePiece'.
	inactivePieces := false
	// Save a list of which workers are currently late.
	lateWorkers := make(map[string]struct{})
	ws.mu.Lock()
	piecesCopy := make([][]pieceDownload, len(pdc.activePieces))
	for i, activePiece := range pdc.activePieces {
		// Track whether there are new workers available for this piece.
		unlaunchedWorkerAvailable := false
		// Track whether any of the workers have launched a job and have not yet
		// failed.
		launchedWithoutFail := false
		// Track whether the piece has no launched workers at all.
		unlaunchedPiece := true
		// Track whether the piece has any late workers.
		pieceHasLateWorkers := false
		// Track whether the piece has any workers that are not yet late.
		pieceHasActiveWorkers := false
		piecesCopy[i] = make([]pieceDownload, len(activePiece))
		for j, pieceDownload := range activePiece {
			// Consistency check - failed and completed are mutally exclusive,
			// and neither should be set unless launched is set.
			if (!pieceDownload.launched && (pieceDownload.completed || pieceDownload.failed)) || (pieceDownload.failed && pieceDownload.completed) {
				ws.staticRenter.log.Critical("rph3 download piece is incoherent")
			}
			piecesCopy[i][j] = pieceDownload

			// If this worker has not launched, there are workers that can fetch
			// this piece. That also means there are more workers that can be
			// launched if this download is struggling.
			if !pieceDownload.launched {
				unlaunchedWorkerAvailable = true
				unlaunchedWorkersAvailable = true
			}
			// If this worker has launched and not yet failed, this piece is not
			// an unlaunched piece.
			if pieceDownload.launched && !pieceDownload.failed {
				unlaunchedPiece = false
			}
			// If there is a worker that has launched and not yet failed, this
			// piece can be counted as a piece which has launched without fail -
			// it is a piece that may contribute to redundancy in the future.
			if pieceDownload.launched && !pieceDownload.failed {
				launchedWithoutFail = true
			}
			// Check if this piece is late or if the piece failed altogether, if
			// so, mark the worker as a late worker.
			if pieceDownload.launched && (pieceDownload.failed || pieceDownload.staticExpectedCompletionTime.Before(now)) {
				lateWorkers[pieceDownload.staticWorker.staticHostPubKeyStr] = struct{}{}
				pieceHasLateWorkers = true
			} else if pieceDownload.launched {
				pieceHasActiveWorkers = true
			}
		}
		// Count the piece as able to contribute to redundancy if there is a
		// worker that has launched for the piece which has not failed.
		if launchedWithoutFail || unlaunchedWorkerAvailable {
			piecesAvailableToLaunch++
		}
		// If this piece does not have any workers that have launched without
		// fail, this piece counts as an unlaunched piece.
		if unlaunchedPiece {
			unlaunchedPieces = true
		}
		// If this piece has late workers, and also has no workers that are
		// launched and not yet late, then this piece counts as inactive.
		if pieceHasLateWorkers && !pieceHasActiveWorkers {
			inactivePieces = true
		}
	}
	ws.mu.Unlock()

	// Check whether it is still possible for the download to complete.
	potentialPieces := piecesAvailableToLaunch + len(unresolvedWorkers)
	if potentialPieces < pdc.staticWorkerSet.staticErasureCoder.MinPieces() {
		return nil, 0, 0, nil, errors.New("rph3 chunk download has failed because there are not enough potential workers")
	}
	// Check whether it is possible for new workers to be launched.
	if !unlaunchedWorkersAvailable && len(unresolvedWorkers) == 0 {
		// All 'nil' return values, meaning the download can succeed by waiting
		// for already launched workers to return, but cannot succeed by
		// launching new workers because no new workers are available.
		return nil, 0, 0, nil, nil
	}

	// We know from the previous check that at least one of the unresolved
	// workers or at least one of the resolved workers is available. Initialize
	// the variables as though there are no unresolved workers, and then if
	// there is an unresolved worker, set the rank and duration appropriately.
	//
	// TODO: We have the worker selection process complete, but we don't
	// actually have the code to finish the return values. Also, we should
	// probably return a time.Time instead of a time channel, because not every
	// worker that gets returned is a blocking element.
	bestWorkerResolved := true
	bestKnownRank := workerRankLate
	bestKnownDuration := nullDuration
	if len(unresolvedWorkers) > 0 {
		// Determine the rank of the unresolved workers. We rank unresolved
		// workers optimistically, meaning we assume that they will fill the
		// most convenient / important possible role.
		if unlaunchedPieces {
			// If there are any pieces that have not yet launched workers, then
			// the rank of any unresolved workers is going to be 'unlaunched'.
			bestKnownRank = workerRankUnlaunchedPiece
		} else if inactivePieces {
			// If there are no unlaunched pieces, but there are inactive pieces,
			// the rank of any unresolved workers is going to be 'launched'.
			bestKnownRank = workerRankLaunchedPiece
		} else {
			// If there are no unlaunched pieces and no inactive pieces, the
			// rank of any unlaunched workers is going to be 'active'.
			bestKnownRank = workerRankActivePiece
		}
		bestWorkerResolved = false
		bestKnownDuration = bestUnresolvedDuration
	}
	// Iterate through the piecesCopy, finding the best unlaunched worker in the
	// set. Only count workers that are better than the best unresolved worker.
	var bestResolvedWorker *worker
	var bestPieceIndex int
	for _, activePiece := range piecesCopy {
		pieceCompleted := false
		pieceActive := false
		pieceLaunched := false
		for _, pieceDownload := range activePiece {
			if pieceCompleted {
				pieceCompleted = true
				break
			}
			if pieceDownload.launched {
				pieceLaunched = true
			}
			if pieceDownload.launched && now.Before(pieceDownload.staticExpectedCompletionTime) {
				pieceActive = true
			}
		}
		// Skip this piece if the piece has already been completed.
		if pieceCompleted {
			continue
		}
		// Skip this piece if it the rank of the piece is worse than the best
		// known rank.
		if bestKnownRank < workerRankLaunchedPiece && pieceLaunched {
			continue
		}
		// Skip this piece if it the rank of the piece is worse than the best
		// known rank.
		if bestKnownRank < workerRankActivePiece && pieceActive {
			continue
		}

		// Look for any workers of good enough rank.
		for i, pieceDownload := range activePiece {
			// Skip any workers that are late if the best known rank is not
			// late.
			_, isLate := lateWorkers[pieceDownload.staticWorker.staticHostPubKeyStr]
			if bestKnownRank < workerRankLate && isLate {
				continue
			}
			// Skip this worker if it is not good enough.
			w := pieceDownload.staticWorker
			readTime := w.staticJobReadQueue.callExpectedJobTime(pdc.staticPieceLength)
			if readTime < 0 {
				readTime = 0
			}
			expectedRSCompletionCost := w.staticJobReadQueue.callExpectedJobCost(pdc.staticPieceLength)
			rsPricePenalty, err := expectedRSCompletionCost.Div(pricePerMSPerWorker).Uint64()
			if err != nil || rsPricePenalty > math.MaxInt64 {
				readTime = time.Duration(math.MaxInt64)
			} else if reduced := math.MaxInt64 - int64(rsPricePenalty); int64(readTime) > reduced {
				readTime = time.Duration(math.MaxInt64)
			} else {
				readTime += time.Duration(rsPricePenalty)
			}
			if bestKnownDuration < readTime {
				continue
			}

			// This worker is good enough, determine the new rank.
			if isLate {
				bestKnownRank = workerRankLate
			} else if pieceActive {
				bestKnownRank = workerRankActivePiece
			} else if pieceLaunched {
				bestKnownRank = workerRankLaunchedPiece
			} else {
				bestKnownRank = workerRankUnlaunchedPiece
			}
			bestKnownDuration = readTime
			bestResolvedWorker = pieceDownload.staticWorker
			bestPieceIndex = i
			bestWorkerResolved = true
		}
	}

	// If the best worker is an unresolved worker, return a time that indicates
	// when we should give up waiting for the worker, as well as a channel that
	// indicates when there are new workers that have returned.
	if !bestWorkerResolved {
		ws.mu.Lock()
		c := ws.registerForWorkerUpdate()
		ws.mu.Unlock()
		checkAgainTime := bestUnresolvedWaitTime
		if bestWorkerLate {
			checkAgainTime = 0
		}
		return nil, 0, checkAgainTime, c, nil
	}

	// Best worker is resolved.
	if bestResolvedWorker == nil {
		ws.staticRenter.log.Critical("there is no best resolved worker and also no best unresolved worker")
	}
	return bestResolvedWorker, bestPieceIndex, 0, nil, nil
}

// waitForWorker will block until one of the best workers is available to be
// used for download. waitForWorker will return an error if there are no new
// workers available that can be launched.
//
// TODO: Do we need the piece index, or can we figure that out some other way
// after the fact? We may not need it as a return value. Which means it can be
// removed from the call signature all the way through.
//
// TODO: Need thorough testing to ensure that repeated calls to findBestWorker
// eventually fail. I guess the context timeout actually handles most of this.
//
// TODO: We assume that this call will return an error if there are no workers
// available.
//
// TODO: Something about that time.After
func (pdc *projectDownloadChunk) waitForWorker() (*worker, pieceIndex, error) {
	for {
		worker, pieceIndex, sleepTime, wakeChan, err := pdc.findBestWorker()
		if err != nil {
			return nil, 0, errors.AddContext(err, "no good worker could be found")
		}
		// If there was a worker found, return that worker.
		if worker != nil {
			return worker, pieceIndex, nil
		}

		// If there was no worker found, sleep until we should call
		// findBestWorker again.
		maxSleep := time.After(sleepTime)
		select {
		case <-maxSleep:
		case <-wakeChan:
		case <-pdc.ctx.Done():
			return nil, 0, errors.New("timed out waiting for a good worker")
		}
	}
}

// launchWorker will launch a worker for the download project. An error
// will be returned if there is no worker to launch.
func (pdc *projectDownloadChunk) launchWorker() (time.Time, error) {
	// Loop until either a worker succeeds in launching a job, or until there
	// are no more workers to return.
	var expectedCompletionTime time.Time
	for {
		// An error here means that no more workers are available at all.
		w, pieceIndex, err := pdc.waitForWorker()
		if err != nil {
			return time.Time{}, errors.AddContext(err, "unable to launch a new worker")
		}

		// Create the read sector job for the worker.
		jrs := &jobReadSector{
			jobRead: jobRead{
				staticResponseChan: pdc.staticWorkerResponseChan,
				staticLength:       pdc.staticPieceLength,
				jobGeneric:         newJobGeneric(w.staticJobReadQueue, pdc.staticCtx.Done()),
			},
			staticOffset: pdc.staticPieceOffset,
			staticSector: pdc.staticWorkerSet.staticPieceRoots[pieceIndex],
		}

		// Launch the job and then update the status of the worker. Either way,
		// the worker should be marked as 'launched'. If the job is not
		// successfully queued, the worker should be marked as 'failed' as well.
		expectedCompletionTime, err = w.staticJobReadQueue.callAddWithEstimate(jrs)
		pdc.mu.Lock()
		for _, pieceDownload := range pdc.activePieces[pieceIndex] {
			if w == pieceDownload.staticWorker {
				pieceDownload.launched = true
				if err != nil {
					pieceDownload.failed = true
				}
			}
		}
		pdc.mu.Unlock()

		// If there was no error, return the expected completion time.
		// Otherwise, try grabbing a new worker.
		if err == nil {
			return expectedCompletionTime, nil
		}
	}
}

// handleJobReadResponse will take a jobReadResponse from a worker job
// and integrate it into the set of pieces.
func (pdc *projectDownloadChunk) handleJobReadResponse(jrr *jobReadResponse) (bool, error) {
	// TODO: Need a helper function to determine whether the download is doomed
	// to fail.
	if readSectorResponse == nil || resdSectorResp.staticErr != nil {
		// This download failed,
	}

}

// fail will send an error down the download response channel.
func (pdc *prjectDownloadChunk) fail(err error) {
	dr := &downloadResponse{
		data:  nil,
		error: err,
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
	chunkDLOffset := pdc.pieceOffset * ec.MinPieces()
	chunkDLLength := pdc.pieceOffset * ec.MinPieces()

	buf := bytes.NewBuffer(nil)
	err := pdc.workerSet.staticErasureCoder.Recover(pdc.pieces, chunkDLOffset+chunkDLLength, buf)
	if err != nil {
		pdc.fail(errors.AddContext(err, "unable to complete erasure decode of download"))
		return
	}

	// Data is all in, truncate the chunk accordingly.
	//
	// TODO: Unit test this.
	data := buf.Bytes()
	chunkStartWithinData := pdc.chunkOffset - chunkDLOffset
	chunkEndWithinData := pdc.chunkLength + chunkStartWithinData
	data = data[chunkStartWithinData:chunkEndWithinData]

	// The data is all set.
	dr := &downloadResponse{
		data: data,
		err:  nil,
	}
	pdc.downloadResponseChan <- dr
}

// finished returns true if the download is finished, and returns an error if
// the download is unable to complete.
func (pdc *projectDownloadChunk) finished() (bool, error) {
	// Convenience variables.
	ws := pdc.workerSet
	ec := pdc.workerSet.staticErasureCoder

	// Count the number of completed pieces and hopefuly pieces in our list of
	// potential downloads.
	completedPieces := 0
	hopefulPieces := 0
	for _, pieceDownload := range activePieces {
		if pieceDownload.completed {
			completedPieces++
		}
		if !pieceDownload.failed {
			hopefulPieces++
		}
	}
	if completedPiecs >= ec.MinPieces() {
		return true, nil
	}

	// Count the number of workers that haven't completed their results yet.
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

		// Run logic to determine whether or not we should kick off overdrive
		// workers.
		if pdc.needsOverdrive() {
			_ = pdc.launchWorker() // Err is ignored, nothing to do.
		}

		// Determine when the next overdrive check needs to run.
		overdriveTimeout := pdc.overdriveTimeout()
		select {
		case jrr := pdc.workerResponseChan:
			pdc.handleJobReadResponse(jrr)
			continue
		case <-pdc.ctx.Done():
			pdc.fail(errors.New("download failed while waiting for responses"))
			return
		case <-overdriveTimeout:
			continue
		}
	}
}

// 'managedDownload' will download a range from a chunk. This call is
// asynchronous. It will return as soon as the sector download requests have
// been sent to the workers. This means that it will block until enough workers
// have reported back with HasSector results that the optimal download request
// can be made.
//
// Blocking until all of the piece downloads have been put into job queues
// ensures that the workers will account for the bandwidth overheads associated
// with these jobs before new downloads are requested. Multiple calls to
// 'managedDownload' from the same thread will generally follow the rule that
// the first calls will return first, though no ordering can be guaranteed due
// to the highly parallel and asynchronous nature of the download.
//
// pricePerMS is "price per millisecond". To add a price preference to picking
// hosts to download from, the total cost of performing a download will be
// converted into a number of milliseconds based on the pricePerMS. This cost
// will be added to the return time of the host, meaning the host will be
// selected as though it is slower.
func (pcws *projectChunkWorkerSet) managedDownload(ctx context.Context, pricePerMS types.Currency, offset, length uint64) (chan *downloadResponse, error) {
	// Convenience variables.
	ec := pcws.staticErasureCoder

	// Consistency check some of the erasure coder values.
	if ec.PieceSegmentSize() == 0 || ec.PieceSegmentSize%crypto.SegmentSize != 0 {
		pdws.staticRenter.log.Critical("pcws has a bad erasure coder")
	}

	// TODO: Need to check the encryption type. This type of download cannot
	// support twofish encryption at the moment because it cannot handle the
	// decryption overhead.

	// Determine the download offset within a single piece. We get this by
	// dividing the chunk offset by the number of pieces and then rounding
	// down to the nearest segment size.
	//
	// This is mathematically equivalent to rounding down the chunk size to
	// the nearest chunk segment size and then dividing by the number of
	// pieces.
	pieceOffest := offset / ec.MinPieces()
	pieceOffset = pieceOffset / ec.PieceSegmentSize()
	pieceOffset = pieceOffset * ec.PieceSegmentSize()

	// Determine the length that needs to be downloaded. This is done by
	// determining the offset that the download needs to reach, and then
	// subtracting the pieceOffset from the termination offset.
	chunkSegmentSize := ec.PieceSegmentSize() * ec.MinPieces() // TODO: Maybe make a function for this on the EC interface.
	chunkTerminationOffset := offset + length
	overflow := chunkTerminationOffset % chunkSegmentSize
	if overflow != 0 {
		chunkTerminationOffset += chunkSegmentSize - overflow
	}
	pieceTerminationOffset = chunkTerminationOffset / ec.PieceSegmentSize()
	pieceLength := pieceTerminationOffset - pieceOffset

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
	// redundancy will be at play.
	workerResponseChan := make(chan *jobReadResponse, ec.NumPieces()*5)

	// Build the full pdc.
	pdc := &projectDownloadChunk{
		chunkOffset: offset,
		chunkLength: length,
		pricePerMS:  pricePerMS,

		pieceOffset: pieceOffset,
		pieceLength: pieceLength,

		activePieces: make([][]pieceDownload, ec.NumPieces()),

		pieces: make([][]byte, ec.NumPieces()),

		ctx:                  ctx,
		workerResponseChan:   workerResponseChan,
		downloadResponseChan: make(chan *downloadResponse, 1),
		workerSet:            pcws,
	}

	// Launch enough workers to complete the download. The overdrive code will
	// determine whether more pieces need to be launched.
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
