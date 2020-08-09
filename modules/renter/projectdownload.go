package renter

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

import (
	"context"
	// "fmt"
	"math"
	"sync"
	"time"

	// "gitlab.com/NebulousLabs/Sia/crypto"
	// "gitlab.com/NebulousLabs/Sia/modules"
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
	// Completed, launched, and failed are status variables for the piece. If
	// 'launched' is false, it means the piece download has not started yet.
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

	// staticExpectedCompletionTime indicates the time when the download is
	// expected to complete. This is used to determine whether or not a download
	// is late.
	staticExpectedCompletionTime time.Time

	staticWorker *worker
}

// projectDownloadChunk connects to a projectChunkWorkerSet and downloads a
// subset of the chunk from that pcws. A projectDownloadChunk can only be used
// once.
type projectDownloadChunk struct {
	// Parameters for downloading within the chunk.
	staticFetchOffset uint64
	staticFetchLegnth uint64
	staticPricePerMS  types.Currency

	// Values derived from the chunk download parameters. The offset and length
	// specify the offset and length that will be sent to the host, which much
	// be segment aligned.
	staticPieceOffset uint64
	staticPieceLength uint64

	// activiePieces are pieces where there are one or more workers that have
	// been tasked with fetching the piece.
	//
	// TODO: Need to rename this, 'active' has a different/overloaded meaning
	// now.
	activePieces [][]pieceDownload

	// pieces is the buffer that is used to place data as it comes back. There
	// is one piece per chunk, and pieces can be nil. To know if the download is
	// complete, the number of non-nil pieces will be counted.
	pieces [][]byte

	// The completed data gets sent down the response chan once the full
	// download is done.
	mu                 sync.Mutex
	staticResponseChan chan []byte
	staticWorkerSet    *projectChunkWorkerSet
}

// downloadResponse is the reponse returned in the download channel returned by
// the download() method.
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
func (pdc *projectDownloadChunk) findBestWorker() (*worker, time.Duration, chan<- struct{}, error) {
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
	var unresolvedWorkers []pcwsUnresolvedWorker
	var updateChan chan<- struct{}
	ws.mu.Lock()
	for _, uw := range ws.unresolvedWorkers {
		unresolvedWorkers = append(unresolvedWorkers, uw)
	}
	if len(ws.unresolvedWorkers) > 0 {
		// Not allowed to grab the update chan if there are no unresolved
		// workers.
		updateChan = ws.registerForWorkerUpdate()
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
	for _, uw := range unresovledWorkers {
		// Figure how much time is expected to remain until the worker is
		// avaialble. Note that no price penatly is attached to the HasSector
		// call, because that call is being made regardless of the cost.
		hasSectorTime := time.Until(uw.staticExpectedCompletionTime)
		if hasSectorTime < 0 {
			hasSectorTime = 0
		}

		// Figure out how much time is expected until the worker completes the
		// download job.
		readTime := uw.staticJobReadQueue.callExpectedJobTime(pdc.staticPieceLength)
		if readTime < 0 {
			readTime = 0
		}
		expectedRSCompletionCost := uw.staticJobReadQueue.callExpectedJobCost(pdc.staticPieceLength)
		rsPricePenalty := time.Duration(expectedRSCompletionCost.Div(pdc.staticPricePerMSPerWorker).Uint64())
		readTime += rsPricePenalty

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
	piecesCopy := make([][]pieceDownload, len(ws.activePieces))
	for i, activePiece := range ws.activePieces {
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
				build.Critical("rph3 download piece is incoherent")
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
		return nil, nil, nil, errors.New("rph3 chunk download has failed because there are not enough potential workers")
	}
	// Check whether it is possible for new workers to be launched.
	if !unlaunchedWorkersAvailable && len(unresolvedWorkers) == 0 {
		// All 'nil' return values, meaning the download can succeed by waiting
		// for already launched workers to return, but cannot succeed by
		// launching new workers because no new workers are available.
		return nil, nil, nil, nil
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
			_, exists := lateWorkers[pieceDownload.worker.staticHostPubKeyStr]
			if bestKnownRank < workerRankLate && exists {
				continue
			}
			// Skip this worker if it is not good enough.
			w := pieceDownload.staticWorker
			readTime := w.staticJobReadQueue.callExpectedJobTime(pdc.staticPieceLength)
			if readTime < 0 {
				readTime = 0
			}
			expectedRSCompletionCost := w.staticJobReadQueue.callExpectedJobCost(pdc.staticPieceLength)
			rsPricePenalty := time.Duration(expectedRSCompletionCost.Div(pdc.staticPricePerMSPerWorker).Uint64())
			readTime += rsPricePenalty
			if bestKnownDuration < readTime {
				continue
			}

			// This worker is good enough, determine the new rank.
			if exists {
				bestKnownRank = workerRankLate
			} else if pieceDownload.active {
				bestKnownRank = workerRankActivePiece
			} else if pieceDownload.launched {
				bestKnownRank = workerRankLaunchedPiece
			} else {
				bestKnownRank = workerRankUnlaunchedPiece
			}
			bestKnownDuration = readTime
			bestResolvedWorker = pieceDownload.worker
			bestPieceIndex = i
			bestWorkerResolved = true
		}
	}

	// If the best worker is an unresolved worker, return a time that indicates
	// when we should give up waiting for the worker, as well as a channel that
	// indicates when there are new workers that have returned.
	if !bestWorkerResolved {
		pcws.mu.Lock()
		c := pcws.registerForWorkerUpdate()
		pcws.mu.Unlock()
		return nil, bestUnresolvedWaitTime, c, nil
	}
	// Best worker is resolved.
}

// waitForWorker will block until a strong worker is available to add to the
// download. waitForWorker will ignore workers that can only fetch pieces which
// are already being fetched.
//
// If possible, waitForWorker will also ignore workers that can only fetch
// 'launcehdPiecs' as well. This is to avoid queuing multiple workers on the
// same piece. A piece may appear in 'launchedPieces' but not 'activePieces' if
// the piece is late while being fetched.
func (pcws *projectChunkWorkerSet) waitForWorker(activePieces []bool, launchedPieces []bool) {
	// 1. Determine the set of workers that are potentially useful for launch.

	// 2. helper function to figure out whether or not the best unlaunched
	// worker
}

// TODO: Suggestion: use activePieces []bool to track which pieces we believe
// will come back on time, and delayedPiecs []bool to track which pieces have
// workers out but

// download will download a range from a chunk. This call is asynchronous. It
// will return as soon as the sector download requests have been sent to the
// workers. This means that it will block until enough workers have reported
// back with HasSector results that the optimal download request can be made.
//
// pricePerMS is "price per millisecond". To add a price preference to picking
// hosts to download from, the total cost of performing a download will be
// converted into a number of milliseconds based on the pricePerMS. This cost
// will be added to the return time of the host, meaning the host will be
// selected as though it is slower.
func (pcws *projectChunkWorkerSet) managedDownload(ctx context.Context, pricePerMS types.Currency, offset, length uint64) (chan *downloadResponse, error) {
	/*
		// waitForWorkers will run a set of logic to see if the ideal set of workers
		// has been found.
		//
		// The timeChan is declared outside the scope of waitForWorkers because it
		// is only going to be set a single time. Setting it only happens once
		// because there are memory leaks associated with time.After, but if we only
		// leak 1 per call to download(), that is not a huge issue. The leak ends
		// after the wait time elapses.
		var timeChan chan time.Time
		waitForWorkers := func() (chan struct{}, chan time.Time, error) {
			pdws.mu.Lock()
			defer pdws.mu.Unlock()

			// Loop through the set of available pieces, tracking the MinPieces best
			// options.
			bestWorkers := make([]uint64, pdws.staticErasureCoder.MinPieces())
			for _, workers := range pdws.availablePieces {
				if len(workers) == 0 {
					continue
				}

				// Find the best worker in the piece.
				bestPieceScore := ^uint64(0)
				for _, w := range workers {
					if w.Score() < bestPieceScore {
						// TODO: Need to implement an actual scoring algorithm.
						bestPieceScore = score(w)
					}
				}

				// Replace the worst score in the set of scores we have with the
				// best score for this piece.
				replaceWorstScore(bestWorkers, bestPieceScore)
			}

			// If we didn't completely fill out the set of best workers, we need to
			// wait.
			for _, score := range bestWorkers {
				// Check whether we should give up entirely.
				if score == 0 && len(pdws.unresolvedWorkers) == 0 {
					return nil, nil, errors.New("not enough workers for download")
				}
				// There are not enough workers to perform the download, get a
				// channel and block for an update.
				if score == 0 {
					c := pdws.registerForWorkerUpdate()
					return c, nil, nil
				}
			}

			// Find the worst score of the best scores that we have accumulated.
			if len(bestWorkers) == 0 {
				pdws.staticRenter.log.Critical("download created with erasure code that has a min pieces value of 0")
			}
			worstScore := bestWorkers[0]
			for _, score := range bestWorkers {
				if score > worstScore {
					worstScore = score
				}
			}

			// Loop through the unresovled workers to see if any have a better score
			// than the ones that have already returned. Track the longest amount of
			// time that a better scoring worker needs to complete its HasSector
			// job.
			//
			// Longest time is selected as opposed to shortest time beacuse we want
			// a chance to see the result of all of the better workers.
			numBetterWorkers := 0
			waitTime := 0
			for _, uw := range pdws.unresolvedWorkers {
				score, timeRemaining := uw.score()
				if score < worstScore {
					numBetterWorkers++
					if timeRemaining > waitTime {
						waitTime = timeRemaining
					}
				}
			}
			// Check how many better workers are waiting. Because there is no
			// guarantee that a worker will have the piece we want, we require
			// multiple potentially better workers to exist before we will wait for
			// them to complete.
			if numBetterWorkers < 3 {
				// There are not enough better workers to justify waiting. Return
				// all 'nil' to indicate that the full download should proceed.
				return nil, nil, nil
			}
			// If the timeChan was not set by a previous call to waitForWorkers,
			// create a time channel that will fire after waiting until the slowest
			// of our potentially better workers is expected to return. The reason
			// that we don't reset the timeChan is because time.After() is rather
			// computationally expensive, especially in hot loops like this. If we
			// had a better/cheaper way to get a wakeup channel, we could update the
			// channel on every call.
			if timeChan == nil {
				timeChan = time.After(waitTime * 6 / 5)
			}
			c := pdws.registerForWorkerUpdate()
			return c, timeChan, nil
		}

		// Loop until waitForWorkers determines that there are enough workers. An
		// error may be returned, which indicates that the download should be
		// aborted. If 'c' is nil, it means that there are enough workers.
		for waitForWorkerUpdates, timeoutOnWorkerUpdates, err := waitForWorkers(); ; {
			// There's an error, the download cannot complete.
			if err != nil {
				return nil, err
			}

			// The ideal set of workers has been found.
			if waitForWorkerUpdates == nil {
				break
			}

			// Wait until either more information about the workers is available, or
			// until the timeout kicks us out of the loop. If 'c' closes, it means
			// the status has changed and the waitForWorkers logic should be run
			// again. If 't' closes, it means that whatever worker we were waiting
			// for hasn't returned in time, and we should proceed with the
			// downloading using the workers that we already have.
			select {
			case <-waitForWorkerUpdates:
				continue
			case <-timeoutOnWorkerUpdates:
				break
			}
		}

		// Spin up a background thread to perform the download using the workers
		// that have returned.
		dr := make(chan *downloadResponse)
		go func() {
			// Consistency check - the piece segment size must be a multiple of the
			// Sia segment size.
			if ec.PieceSegmentSize() == 0 || ec.PieceSegmentSize%crypto.SegmentSize != 0 {
				pdws.staticRenter.log.Critical("bad piece segment size")
			}
			// Determine the download offset within a single piece. We get this by
			// dividing the chunk offset by the number of pieces and then rounding
			// down to the nearest segment size.
			//
			// This is mathematically equivalent to rounding down the chunk size to
			// the nearest chunk segment size and then dividing by the number of
			// pieces.
			pieceDownloadOffset := offset / ec.MinPieces()
			pieceDownloadOffset = pieceDownloadOffset / ec.PieceSegmentSize()
			pieceDownloadOffset = pieceDownloadOffset * ec.PieceSegmentSize()
			// Determine the length that needs to be downloaded. This happens by
			// rounding the chunk up to the nearest chunk segment size and then
			// dividing by the number of pieces.
			pieceDownloadLength := length
			chunkSegmentSize := ec.PieceSegmentSize() * ec.MinPieces()
			overflow := (offset + length) % chunkSegmentSize
			if overflow != 0 {
				pieceDownloadLength += (chunkSegmentSize - overflow)
			}
			pieceDownloadLength /= ec.MinPieces()
			// Determine the offset within the downloaded portion of the chunk that
			// we use to return the actual data to the caller.
			chunkDownloadOffset := pieceDownloadOffset * ec.MinPieces()
			offsetWithinDownloadedChunk := chunkDownloadOffset - offset

			// Create a channel to track responses from the workers as they complete
			// the downloads. When overdriving, we may need to launch multiple
			// workers on the same piece, so just in case we buffer the channel to
			// be larger than the total number of pieces. This ends up being more
			// relevant for 1-of-N files than any other redundancy.
			channelSize := (ec.NumPieces() * 3) + 5 // TODO: Magic numbers here.
			slotsRemaining := channelSize
			downloadResponseChan := make(chan *jobReadResponse, channelSize)

			// Create a map to track the set of workers that have been attempted,
			// and create an array to track the set of pieces that have been
			// completed.
			//
			// TODO: There is an inefficiency here because we may attempt multiple
			// workers on the same piece if one of the workers is timing out,
			// meaning if both of those workers complete but some other worker
			// doesn't we still can't finish the download. Admittedly, this is a
			// pretty niche edge case and maybe not worth worrying about until we
			// see in the wild that it's slowing down a material number of
			// downloads.
			attemptedWorkers := make(map[string]struct{})
			queuedPieces := make([]bool, ec.NumPieces())

			// Create a helper function to launch a worker. The first bool returned
			// indicates whether a worker was successfully launched, and the second
			// bool indicates whether there are no workers that were able to be
			// selected. The second bool will only be false if the first bool is
			// false.
			launchWorker := func(attemptedWorkers map[string]struct{}, queuedPieces []bool) (time.Duration, bool, bool) {
				// Pick the best worker.
				pieceIndex := 0
				var w *worker
				wScore := 0
				bestIndex := 0
				for i, workers := range pdws.availablePieces {
					// Skip this piece if we already queued a worker for this piece.
					if queuedPiecs[i] {
						continue
					}

					// Iterate over the workers and pick the best one.
					for _, worker := range workers {
						if w == nil {
							w = worker
							wScore = worker.Score()
							bestIndex = i
						}
						if worker.Score() < wScore {
							w = worker
							wScore = worker.Score()
							bestIndex = i
						}
					}
				}
				if w == nil {
					return time.Duration{}, false, false
				}

				// Create the read job.
				jrs := &jobReadSector{
					jobRead: jobRead{
						staticResponseChan: downloadResponseChan,
						staticLength:       pieceDownloadLength,
						jobGeneric:         newJobGeneric(bestWorker.staticJobReadQueue, ctx.Done()), // TODO: Need to fix this context
					},
					staticOffset: pieceDownloadOffset,
				}
				jrs.staticRoot = pdws.staticPieceRoots[bestIndex]
				attemptedWorkers[w.staticHostPubKeyStr] = struct{}{}

				// Submit the job to the worker's queue.
				if !w.staticJobReadQueue.callAdd(jrs) {
					return time.Duration{}, false, true
				}
				queuedPieces[bestIndex] = true

				// TODO: Some overdrive trigger timing here. Basically, record the
				// longest / highest amount of time that any job is expected to come
				// back, and pass that on to the overdrive watcher.
				if w.Latency(jrs) > slowestWorker {
					slowestWorker = w.Latency(jrs)
				}
				return w.Latency(jrs), true, true
			}

			// waitForWorker will wait until there is another worker that a download
			// can be attempted from. The function will return false if all workers
			// that could potentially perform the download fail.
			//
			// TODO: This function isn't currently coded to wait for the best idle
			// worker, it only waits for any idle worker.
			//
			// TODO: Technically should be able to hit an error condition earlier
			// here, we aren't checking whether the number of workers remaining is
			// fewer than the number of pieces we are missing.
			waitForWorker := func(attemptedWorkers map[string]struct{}, queuedPieces []bool) error {
				pdws.mu.Lock()
				defer pdws.mu.Unlock()

				// Loop through the available pieces to find the best worker that is
				// not currently in use.
				bestUnusedScore := 0
				for i, workers := range pdws.availablePieces {
					// Skip any pieces that have queued workers.
					if queuedPieces[i] {
						continue
					}

					// Find the best unused worker.
					for _, worker := range workers {
						_, exists := attemptedWorkers[worker.staticHostPubKeyStr]
						if exists {
							continue
						}
						score := worker.Score()
						if score > bestUnusedScore {
							bestUnusedScore = score
						}
					}
				}

				// Base case: if there are no unresolved workers, and no unused
				// workers that are resovled, return an error.
				if len(pdws.unresolvedWorkers) == 0 && bestUnusedScore == 0 {
					return errors.New("no viable workers remain")
				}

				// Loop through the unresolved workers to determine whether there is
				// an unresovled worker that is potentially better than the current
				// best resolved worker.
				var c chan struct{}
				for _, uw := range pdws.unresolvedWorkers {
					score, _ := uw.score()
					if score > bestUnusedScore {
						c = pdws.registerForWorkerUpdate()
						break
					}
				}
				// Check that there is a worker worth waiting for.
				if c == nil && bestUnusedScore == 0 {
					return errors.New("no good score viable workers remain")
				}
				// If 'c' is nil and also the best unused score is not zero, that
				// means the best worker is an unused worker, therefore we have
				// waiting for a worker sufficiently long.
				if c == nil {
					return nil
				}

				// TODO: Soft sleep this.
				<-c
				// TODO: Make this iterative, not recursive.
				return waitForWorker(attemptedWorekrs, queuedPieces)
			}

			// Launch all of the download jobs.
			var slowestWorker time.Duration
			for x := 0; x < ec.MinPieces(); x++ {
				latency, workerLaunched, noWorkersAvailable := launchWorker(attemptedWorkers, queuedPieces)
				if noWorkersAvailable {
					// TODO: Logic to handle the edge case where there are no more
					// workers available.
					//
					// Likely that's going to mean checking the pdws for whether
					// there are more workers that haven't returned if they have the
					// sector or not. If there are, that's going to mean waiting
					// until there is a wakeup call from the pdws.
					err := waitForWorker(attemptedWorkers, queuedPieces)
					if err != nil {
						// TODO: May need to cancel some contexts here.
						return nil, errors.AddContext(err, "no backup workers available")
					}

					// Decrement the loop so we keep trying new workers.
					x--
					continue
				}
				if !workerLaunched {
					// Decrement the loop so we keep trying new workers.
					//
					// TODO: really the helper function should just automatically
					// keep launching workers.
					x--
					continue
				}

				// Update the tracking of the slowest worker if necessary.
				if latency > slowestWorker {
					slowestWorker = latency
				}
			}

			// TODO: Step 2: Set up the logic to determine whether overdrive is
			// necessary. Provide a channel that times out when the overdrive logic
			// should be run again. The overdrive logic will launch new download
			// jobs if necessary to ensure that the download completes quickly.
			//
			// Overdrive logic will also need to consider whether any download has
			// failed, a failed download immediately merits both launching another
			// worker and also delaying the next overdrive piece to kick off.

			// overdriveTimeout will return a channel that times out when the
			// overdrive code believes that a new worker should be added to the
			// download to ensure that everything completes in time.
			//
			// TODO: This leaks, we should probably switch to some schema that does
			// not leak.
			overdriveTimeout := func(latestLaunchLatency time.Duration, overdriveCalls uint64) chan<- time.Time {
				// Wait a bit longer than the longest expected amount of time.
				overdriveTime := latestLaunchLatency * 4 / 3
				// Wait a bit longer if there have been a couple of calls to
				// overdrive, this suggests things have been slowing down.
				for overdriveCalls > 2 {
					overdriveCalls--
					overdriveTime *= 3
					overdriveTime /= 2
				}
				return time.After(overdriveTime)
			}

			// integrateResponse takes a download response from a worker and
			// integrates it into the project, launching a new worker if necessary.
			// If an error is returned, it means the project has failed and the
			// download will not be successful. If the bool returned is true, it
			// means the project has succeeded.
			integrateResponse := func(resp *jobReadResponse) (bool, error) {
				// Check if the download failed.
				if resp == nil || resp.staticErr != nil {
					// TODO: Find the piece index of the root we attempted to
					// download and then un-queue that piece.

					// TODO: You really didn't finish this code block at all. But I
					// think it is only two steps: 1. dequeue piece, 2. launch a new
					// worker.

					// TODO: Break this into separate logic - we need to do the same
					// loop as is inside the launcher, execpt we are only throwing
					// one out instead of ec.MinPieces(). That should DRY things up
					// a bit as well.
					latency, workerLaunched, noWorkersAvailable := launchWorker(attemptedWorkers, queuedPieces)
					if noWorkersAvailable {
						err := waitForWorker(attemptedWorkers, queuedPieces)
						if err != nil {
							return nil, errors.AddContext(err, "no overdrive backup workers available")
						}
					}
					if !workerLaunched {
						// TODO: Loop to keep launching... or really, the helper
						// function should loop to keep launching.
					}
				}

				// TODO: We have a piece. Do the EC, and then return success if the
				// download is done.
			}

			overdriveCalls := 0
			launchNewWorker := overdriveTimeout(slowsetWorker, 0)
			select {
			case <-launchNewWorker:
				// TODO: Run the launch n workers function with n=1
			case resp := <-downloadResponseChan:
				terminated := integrateResponse(resp)
				if terminated {
					return
				}
			case <-ctx.Done():
				sendDownloadFailed()
				return
			}

		}()
		return dr, nil
	*/
	return nil, errors.New("not implemented")
}
