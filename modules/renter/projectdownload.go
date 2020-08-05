package renter

// TODO: Make a pdws that can be initialized with siafile chunk data. Basically
// the same thing, except instead of filling out the pieces using HasSector
// calls, we get to fill out the pieces immediately using information we have in
// advance.

// TODO: The async stuff should be more aggressive about launching jobs, I think
// concerns of overloading one host too heavily are overblown. Probably also
// contributing to certain issues.

// TODO: Need to write a test for grabbing a PDWS, and then checking that it
// actually queries all of the workers correctly.

// TODO: Should we set up some sort of retry mechanism for workers that fail
// their HasSector tasks? It's possible that whatever the error was is a
// temporary netowrk error.

// TODO: I'm not sure throughout this function whether higher score is better or
// higher score is worse, I think that I have been inconsistent.

// TODO: The score function on the worker is going to need to take as an
// argument a timestamp indicating how long we have been waiting for the worker
// to return.

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

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"

	"gitlab.com/NebulousLabs/errors"
)

// projectDownloadChunk helps to coordinate downloading a portion of data within
// a chunk. projectDownloadChunk needs to be connected to a
// projectDownloadWorkerSet.
type projectDownloadChunk struct {
	staticFetchOffset uint64
	staticFetchLegnth uint64

	// pieces is the buffer that is used to place data as it comes back. There
	// is one piece per chunk, and pieces can be nil. To know if the download is
	// complete, the number of non-nil pieces will be counted.
	pieces [][]byte

	// The completed data gets sent down the response chan once the full
	// download is done.
	staticResponseChan chan []byte
	staticWorkerSet    *projectDownloadWorkerSet

	mu sync.Mutex
}

// projectDownloadUnresolvedWorker pairs a worker with a timestamp to indicate
// the moment that HasSector is expected to return. If that time is in the past,
// the worker is overdue and should get a result any moment, but may also be in
// an error state and therefore may not get a result for a while.
type projectDownloadUnresolvedWorker struct {
	// The worker that is performing the HasSector job.
	staticWorker *worker

	// The time that the HasSector job started.
	staticStartTime time.Time

	// The amount of bandwidth in the upload queue and download queue when the
	// HasSector request was added to the queue.
	staticStartDLQueue uint64
	staticStartULQueue uint64

	// The slowest measured upload and download speed of the worker throughout
	// the lifetime of the HasSector request. This is updated to recalibrate
	// expectations for how long the job is expected to take. These values are
	// compared against the start time, the queue sizes, and the measured
	// inherent latency of the worker to get an expected finish time, which can
	// then be compared to the current time to understand when the worker should
	// be returning.
	slowestDLSpeed uint64
	slowestULSpeed uint64
}

// downloadResponse is the reponse returned in the download channel returned by
// the download() method.
type downloadResponse struct {
	data []byte
	err  error
}

// download will download a range from a chunk. This call is asynchronous. It
// will return as soon as the sector download requests have been sent to the
// workers. This means that it will block until enough workers have reported
// back with HasSector results.
//
// The reason for doing this is to facilitate streaming. If multiple downloads
// for a stream are being queued at once, we want to enforce that the ealier
// parts of the stream are being downloaded faster than the later parts of the
// stream. This also allows us to return an error without queing any downloads
// if we learn that not enough workers have the desired sector to complete the
// download.
//
// TODO: Add score hinting as one of the input parameters. Maybe for now it'll
// just be a difference of priority vs. non-priority. Score hinting meaning
// favor cheaper hosts or lower latency hosts, etc.
//
// TODO: Change this function so that a buffer can be passed in to receive the
// download result.
//
// TODO: Need to pass in a context of some sort?
func (pdws *projectDownloadWorkerSet) managedDownload(offset, length uint64) (chan *downloadResponse, error) {
	// replaceWorstScore will do an in-place modification of the provided scores
	// to replace the worst score with the new score. Lower is better, but zero
	// is blank / worst.
	//
	// NOTE: This is an n^2 algorithm that could easily be a heap. Because n is
	// typically small, we're just going to do things the easy way. If this code
	// shows up as hot on a profile, it can be re-written as a heap.
	replaceWorstScore := func(scores []uint64, newScore uint64) {
		// Handle the edge case of a low new score.
		if newScore == 0 {
			newScore = 1
		}
		// Edge case: if there are no scores, nothing to replace.
		if len(scores) == 0 {
			return
		}

		// Locate the worst score in the array.
		worst := scores[0]
		worsti := 0
		for i, score := range scores {
			// Exit early, this element hasn't been initialized.
			if score == 0 {
				scores[i] = newScore
				return
			}

			// If this is worse than the current worst, update the current
			// worst.
			if score > worst {
				worst = score
				worsti = i
			}
		}

		// Replace the worst score with the new score, only if the new score is
		// better.
		if worst > newScore {
			scores[worsti] = newScore
		}
	}

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
}

// managedResolveAllWorkers will delete any unresolved workers. Typically this
// is called due to a timeout, and the assumption is that any unresolved workers
// will continue to be unresolved.
//
// TODO: I'm not 100% sure at the moment what the idea was behind this
// function... right now it's only used in places where it doesn't seem to
// matter.
func (pdws *projectDownloadWorkerSet) managedResolveAllWorkers() {
	pdws.mu.Lock()
	defer pdws.mu.Unlock()

	// Wipe out the set of unresolved workers, we are assuming at this point
	// that they will not resolve.
	pdws.unresolvedWorkers = make(map[string]*projectDownloadUnresolvedWorker)
	pdws.closeUpdateChans()
}

// threadedProcessResponses will wait for worker responses to come back, and
// update the pdws as the responses come in.
//
// This thread should be the only one with access to the responseChan.
func (pdws *projectDownloadWorkerSet) threadedProcessResponses(ctx context.Context, cancelCtx context.CancelFunc, responseChan <-chan *jobHasSectorResponse) {
	// Upon exit, clean up the rest of the unresolved workers.
	defer pdws.managedResolveAllWorkers()

	// Helper function to make the loop exit condition more clear.
	responsesRemaining := func() int {
		pdws.mu.Lock()
		rr := len(pdws.unresolvedWorkers)
		pdws.mu.Unlock()
		return rr
	}
	piecesFound := uint8(0)
	for responsesRemaining() > 0 {
		// Block until there is a worker response.
		var resp *jobHasSectorResponse
		select {
		case resp = <-responseChan:
		case <-ctx.Done():
			return
		}
		// Sanity check - should not be getting nil responses from the workers.
		if resp == nil {
			pdws.staticRenter.log.Critical("nil response received")
			return
		}

		// Delete the worker from the set of unresolved workers. If the response
		// is not an error response, add the worker to any pieces that it
		// supports.
		w := resp.staticWorker
		pdws.mu.Lock()
		delete(pdws.unresolvedWorkers, w.staticHostPubKeyStr)
		if resp.staticErr == nil {
			// Loop through the set of sectors that the worker is claiming, and add
			// that worker to the piece list for any piece that the worker can
			// contribute to.
			for i, available := range resp.staticAvailables {
				if available {
					if len(pdws.availablePieces) == 0 {
						piecesFound++
					}
					pdws.availablePieces[i] = append(pdws.availablePieces[i], w)
				}
			}
		}
		pdws.closeUpdateChans()
		// Check whether this download has failed and can be aborted.
		exit := false
		if len(pdws.unresolvedWorkers)+piecesFound < pdws.staticErasureCoder.MinPieces() {
			exit = true
		}
		pdws.mu.Unlock()
		if exit {
			cancelCtx()
			return
		}
	}
}

// newPDWSByRoots will create a projectDownloadWorkerSet using the set of Merkle
// roots assocaited with a chunk. Some extra information about the chunk is
// required, such as the erasure coding information and the decryption
// information.
//
// This will cause a large number of 'hasSector' jobs to be initiated in the
// background, detemrining which workers are able to serve a piece of the chunk
// for this PDWS. The 'download' method can be used to download pieces of the
// chunk, and will intelligently select the best workers for the job.
//
// 'download' can be called many times on the same PDWS, and each successful
// call will be more efficient and faster because more will be known about which
// workers should be used to fetch the data. A suggested optimization for larger
// files is to immediately create a PDWS for all chunks when a stream is opened
// on that file, because doing so is relatively cheap and also will
// substantially reduce the seek time of that file.
//
// The pdws will stop checking for repsonses from the workers once the context
// expires.
func (r *Renter) newPDWSByRoots(ctx context.Context, roots []crypto.Hash, ec modules.ErasureCoder, masterKey crypto.CipherKey, chunkIndex uint64) (*projectDownloadWorkerSet, error) {
	// Check that the number of roots provided is consistent with the erasure
	// coder provided.
	if len(roots) != ec.NumPieces() {
		return nil, fmt.Errorf("%v roots provided, but erasure coder specifies %v pieces", len(roots), ec.NumPieces())
	}

	// Create a sub-context that we can cancel in the event that the job launch
	// fails.
	pdwsCtx, pdwsCancel := context.WithCancel(ctx)

	// Create the worker set.
	pdws := &projectDownloadWorkerSet{
		availablePieces:   make([][]*worker, ec.NumPieces()),
		pieceRoots:        roots,
		unresolvedWorkers: make(map[string]*projectDownloadUnresolvedWorker),

		staticChunkIndex:   chunkIndex,
		staticErasureCoder: ec,
		staticMasterKey:    masterKey,

		staticRenter: r,
	}

	// Launch all of the HasSector jobs for each worker.
	workers := r.staticWorkerPool.callWorkers()
	// Make the channel to receive the responses. Channel needs to be buffered
	// so  that when a worker tries to send down the channel, there is not going
	// to be any blocking.
	responseChan := make(chan *jobHasSectorResponse, len(workers))
	for _, w := range workers {
		// Check for gouging.
		//
		// TODO: May want to use a different gouging function.
		cache := w.staticCache()
		pt := w.staticPriceTable().staticPriceTable
		err := checkPDBRGouging(pt, cache.staticRenterAllowance)
		if err != nil {
			r.log.Debugf("price gouging detected in worker %v, err %v", w.staticHostPubKeyStr, err)
			continue
		}

		// Create and launch the job.
		jhs := &jobHasSector{
			staticSectors:      roots,
			staticResponseChan: responseChan,
			jobGeneric:         newJobGeneric(w.staticJobHasSectorQueue, pdwsCtx.Done()),
		}
		if !w.staticJobHasSectorQueue.callAdd(jhs) {
			continue
		}

		// Create the unresolved worker for this job.
		pduw := &projectDownloadUnresolvedWorker{
			staticWorker: w,

			staticStartTime: time.Now(),

			// TODO: Fill out the other fields that will help us with
			// performance data or whatever.
		}
		// Add the unresolved worker to the set of unresolved workers.
		pdws.unresolvedWorkers[w.staticHostPubKeyStr] = pduw
	}
	// Processing check - make sure that there are enough workers to complete
	// the job.
	if len(pdws.unresolvedWorkers) < ec.MinPieces() {
		// Cancel the context so that the workers stop trying to perform
		// HasSector tasks.
		pdwsCancel()
		return nil, errors.New("not enough workers available to kick off a piece lookup")
	}

	// Launch the background thread to update the pdws as workers complete.
	go pdws.threadedProcessResponses(pdwsCtx, pdwsCancel, responseChan)

	// Return the worker set.
	return pdws, nil
}
