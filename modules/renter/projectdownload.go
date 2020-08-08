package renter

// TODO: Need to write a test for grabbing a PDWS, and then checking that it
// actually queries all of the workers correctly.

// TODO: I'm not sure throughout this function whether higher score is better or
// higher score is worse, I think that I have been inconsistent.
//
// Switching to higher score being better.

// TODO: The score function on the worker is going to need to take as an
// argument a timestamp indicating how long we have been waiting for the worker
// to return on the HasSector operation.

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
// building the download chunk.

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

// pieceDownload tracks a worker downloading a piece, whether that piece has
// returned, and what time the piece is/was expected to return.
//
// NOTE: The actual piece data is stored in the projectDownloadChunk after the
// download completes.
type pieceDownload struct {
	launched  bool
	completed bool
	failed    bool

	staticWorker                 *worker
	staticExpectedCompletionTime time.Time
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
// signal that a new best worker may be available.
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
// TODO: Need to make sure the time preference is accounted for correctly.
func (pdc *projectDownloadChunk) findBestWorker() (*worker, chan<- time.Time, chan<- struct{}, error) {
	// Helper variable.
	ws := pdc.staticWorkerSet

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
	nullDuration := time.Duration(math.MaxInt64)
	bestUnresolvedDuration := nullDuration
	for _, uw := range unresovledWorkers {
		// Figure how much time is expected to remain until the worker is
		// avaialble.
		hasSectorTime := time.Until(uw.staticExpectedCompletionTime)
		if hasSectorTime < 0 {
			hasSectorTime = 0
		}

		// Figure out how much time is expected until the worker completes the
		// download job.
		readTime := uw.staticJobReadQueue.callExpectedJobTime(pdc.staticPieceLength)
		if hasSectorTime+readTime < bestUnresolvedDuration {
			bestUnresolvedDuration = hasSectorTime + readTime
		}
	}

	// Find the best duration of any resolved worker. Resolved workers may have
	// status, which impacts how they are preferred:
	//
	// A worker that is unresolved has preference over any resovled workers with
	// the below status. This status is rank 0. Resolved workers with no status
	// are also rank 0.
	//
	// A worker that can only serve pieces which have launched has preference
	// over workers with all below statuses. This status is rank 1e6.
	//
	// A worker that can only serve pieces which are active has preference over
	// workers with all below statuses. This status is rank 2e6.
	//
	// A worker that has a job already which is overdue has the lowest
	// preference of all statuses. This status is rank 3e6.
	//
	// TODO: Consts on the ranks. Can just enum these as well... there's no
	// compat sillyness here.

	// Copy the list of resolved workers. Save a list of which workers are
	// currently late so they are not considered on the second pass unless no
	// good workers exist.
	//
	// TODO: Might be good to break this into a sub-func so it can be unit
	// tested.
	lateWorkers := make(map[string]struct{})
	now := time.Now() // So we aren't calling it a ton. It's a little expensive.
	unlaunchedWorkersAvailable := 0
	piecesLauchedWithoutFail := 0
	newWorker := false
	ws.mu.Lock()
	piecesCopy := make([][]pieceDownload, len(ws.activePieces))
	for i, activePiece := range ws.activePieces {
		// A piece that has launched multiple times without fail should not be
		// counted multiple times when considering after the outer loop whether
		// the download can be completed.
		launchedWithoutFail := false
		unlaunchedWorkerAvailable := false
		piecesCopy[i] = make([]pieceDownload, len(activePiece))
		for j, pieceDownload := range activePiece {
			// Consistency check - piece should not be failed and not launched.
			if pieceDownload.failed && !pieceDownload.launched {
				build.Critical("rph3 download piece is incoherent")
			}
			piecesCopy[i][j] = pieceDownload

			// Check if this piece is available to be launched.
			if !pieceDownload.launched {
				unlaunchedWorkerAvailable = true
				continue
			}
			// Check if this piece has failed already.
			if !pieceDownload.failed {
				launchedWithoutFail = true
			}
			// Check if this piece is late or if the piece failed altogether, if
			// so, mark the worker as a late worker.
			if pieceDownload.failed || pieceDownload.staticExpectedCompletionTime.Before(now) {
				lateWorkers[pieceDownload.staticWorker.staticHostPubKeyStr] = struct{}{}
			}
		}
		if launchedWithoutFail {
			piecesLaunchedWithoutFail++
		}
		if unlaunchedWorkerAvailable {
			unlaunchedWorkersAvailable++
		}
	}
	ws.mu.Unlock()

	// Check whether the download can feasibly be finished.
	potentialPieces := unlaunchedWorkersAvailable + piecesLaunchedWithoutFail + len(unresolvedWorkers)
	if potentialPieces < pdc.staticWorkerSet.staticErasureCoder.MinPieces() {
		return nil, nil, nil, errors.New("rph3 chunk download has failed because there are not enough potential workers")
	}
	// Check whether it is possible for new workers to be launched.
	if unlaunchedWorkersAvailable + len(unresolvedWorkers) == 0 {
		// All 'nil' return values, meaning the download can succeed by waiting
		// for already launched workers to return, but cannot succeed by
		// launching new workers because no new workers are available.
		return nil, nil, nil, nil
	}

	// Iterate through the piece copy, finding the best worker in the piece
	// copy. We know from the previous check that at least one of the unresolved
	// workers or at least one of the resolved workers is available.
	bestWorkerResolved := true
	bestKnownRank := int(3e6)
	bestKnownDuration := nullDuration
	unlaunchedWorkerAvailable := false
	if len(unresolvedWorkers) > 0 {
		bestKnownRank = 0
		bestWorkerResolved = false
		bestKnownDuration = bestUnresolvedDuration
	}
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
		}

		// Skip this piece if the piece has already been completed.
		if pieceCompleted {
			continue
		}
		// Skip this piece if it is not a rank 0 piece and the best known
		// piece is rank 0.
		if bestKnownRank <= 0 && pieceLaunched {
			continue
		}
		// Skip this piece if it is not a rank 1 piece and the best known
		// piece is rank 1.
		if bestKnownRank <= 1e6 && pieceActive {
			continue
		}

		// Look for any workers of good enough rank.
		for i, pieceDownload := range activePiece {
			// Skip any workers that are late if the best known rank is not
			// late.
			_, exists := lateWorkers[pieceDownload.worker.staticHostPubKeyStr]
			if bestKnownRank <= 2e6 && exists {
				continue
			}
			// Skip this worker if it is not good enough.
			readTime := pieceDownload.worker.staticJobReadQueue.callExpectedJobTime(pdc.staticPieceLength)
			if bestKnownDuration < readTime {
				continue
			}

			// This worker is good enough, determine the new rank.
			if exists {
				bestKnownRank = 3e6
			} else if pieceDownload.active {
				bestKnownRank = 2e6
			} else if pieceDownload.launched {
				bestKnownRank = 1e6
			} else {
				bestKnownRank = 0
			}
			bestKnownDuration = readTime
			bestKnownWorker = pieceDownload.worker
			bestKnownIndex = i
			unlaunchedWorkerAvailable = true
			bestWorkerResolved = true
		}
	}
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
