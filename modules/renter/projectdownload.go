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
	staticWorkerSet *projectDownloadWorkerSet

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

// projectDownloadWorkerSet is an object that tracks workers that are
// potentially able to fetch a piece for a single chunk. The chunkWorkerSet can
// either be initialized with a set of MerkleRoots, where it is unknown which
// hosts have the pieces, or the chunkWorkerSet can be initialized with a set of
// known root-host pairs. In the former case, the chunkWorkerSet will issue a
// set of HasSector calls to the network to figure out which workers can be used
// to fetch pieces of the chunk.
//
// This structure is not intended to be long lived, perhaps a couple of hours at
// most. The major limiting factor is that this structure will not update as
// file repairs happen, meaning that it becomes increasingly stale over time.
//
// One of the major use cases for this data structure is streaming.
type projectDownloadWorkerSet struct {
	// This is a set of workers that may or may not be able to fetch the chunk.
	// The workers in this set are actively running HasSector jobs to figure out
	// whether they can participate in the downloading of the chunk.
	//
	// The set of unresolved workers is a map so that workers can easily be
	// removed from the map when their HasSector job returns.
	//
	// When trying to execute a download, this map should be used to figure out
	// whether or not the best potential worker has returned yet. This means
	// that the downloading code needs to not only consider the workers in the
	// unresolved map, but also all of the workers that have already returned.
	// This consideration needs to include both the expected time remaining for
	// the HasSector job to complete, and also the expected time for any
	// download submission to the worker to complete.
	unresolvedWorkers map[string]*projectDownloadUnresolvedWorker

	// availablePieces contains the list of pieces for the chunk that this
	// worker set is able to download. For each piece, an array of workers
	// indicates who is able to fetch that piece.
	availablePieces [][]*worker

	// workerUpdateChans contains an array of channels for active downloads that
	// are blocking until more workers have returned. When a new unresolved
	// worker returns, the processing thread for the unresolved workers will go
	// through and close all of the workerUpdateChans, notifying those download
	// threads that new information is available.
	//
	// The workerUpdateChans will be closed even if the new information is that
	// one of the unresovled workers has errored out or that one of the
	// unresolved workers does not have a sector. This is necessary because
	// downloads may be waiting for a faster worker. If that faster worker is
	// having issues or otherwise doesn't have any pieces at all, the
	// downloaders may prefer to use existing workers instead of continuing to
	// wait for another piece to show up.
	//
	// NOTE: once 'unresolvedWorkers' has a length of zero, downloaders should
	// stop waiting for updates and workerUpdateChans should be empty
	// forevermore.
	workerUpdateChans []chan struct{}

	// Decoding and decryption information.
	staticChunkIndex   uint64
	staticErasureCoder modules.ErasureCoder
	staticMasterKey    crypto.CipherKey

	// Utilities
	staticRenter *Renter
	mu sync.Mutex
}

// downloadResponse is the reponse returned in the download channel returned by
// the download() method.
type downloadResponse struct {
	data []byte
	err error
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
// This can potentially create a one-time inefficiency where everything after
// the first download is a little bit later than it would have been, depending
// on how slow the final workers are for a chunk, but in practice I do not
// believe it will be more than 50 or 100ms, it will only apply to the second
// download, and it will only apply once.
//
// TODO: Add score hinting as one of the input parameters. Maybe for now it'll
// just be a difference of priority vs. non-priority.
func (pdws *projectDownloadWorkerSet) download(offset, length uint64) (chan *downloadResponse, error) {
	// replaceWorstScore will do an in-place modification of the provided scores
	// to replace the worst score with the new score. Lower is better, but zero
	// is blank / worst.
	//
	// NOTE: This is an n^2 algorithm that could easily be a heap. Because n is
	// typically small, we're just going to do things the easy way. If this code
	// shows up as hot on a profile, it can be re-written as a heap.
	replaceWorstScore(scores []uint64, newScore uint64) {
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
				c := pdws.registerForWorkerUpdate() // TODO: Implement this.
				return c, nil, nil
			}
		}

		// Find the worst score of the best scores that we have accumulated.
		//
		// TODO: Check for the edge case here where the length of best workers
		// is zero.
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
		// of our potentially better workers is expected to return.
		if timeChan == nil {
			timeChan = time.After(waitTime * 5 / 4)
		}
		c := pdws.registerForWorkerUpdate()
		return c, timeChan, nil
	}

	// Loop until waitForWorkers determines that there are enough workers. An
	// error may be returned, which indicates that the download should be
	// aborted. If 'c' is nil, it means that there are enough workers.
	for c, t, err := waitForWorkers();; {
		// There's an error, the download cannot complete.
		if err != nil {
			return nil, err
		}

		// The ideal set of workers has been found.
		if c == nil {
			break
		}

		// Wait until either more workers are available, or until the timeout
		// kicks us out of the loop. If 'c' closes, it means the status has
		// changed and the waitForWorkers logic should be run again. If 't'
		// closes, it means that whatever worker we were waiting for hasn't
		// returned in time, and we should proceed with the downloading using
		// the workers that we already have.
		select {
		case <-c:
			continue
		case <-t:
			break
		}
	}

	// Spin up a background thread to perform the download using the workers
	// that have returned.
	dr := make(chan *downloadResponse)
	go func() {
	}()
	// TODO: Background thread should:
	//
	// 1. Have an understanding of whether overdrive is needed
	//
	// 2. Launch overdrive threads when workers are available and the existing
	// workers are taking too long.
	//
	// 3. collect the pieces, decrypt the pieces, and decode the pieces,
	// returning them.
	return dr, errors.New("implementation not complete")
}

// closeUpdateChans will close all of the update chans and clear out the slice.
func (pdws *projectDownloadWorkerSet) closeUpdateChans() {
	for _, c := range pdws.workerUpdateChans {
		close(c)
	}
	pdws.workerUpdateChans = nil
}

// managedResolveAllWorkers will delete any unresolved workers. Typically this
// is called due to a timeout, and the assumption is that any unresolved workers
// will continue to be unresolved.
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
func (pdws *projectDownloadWorkerSet) threadedProcessResponses(ctx context.Context, responseChan <-chan *jobHasSectorResponse) {
	// Upon exit, clean up the rest of the unresolved workers.
	defer pdws.managedResolveAllWorkers()

	// Helper function to make the loop exit condition more clear.
	responsesRemaining := func() int {
		pdws.mu.Lock()
		rr := len(pdws.unresolvedWorkers)
		pdws.mu.Unlock()
		return rr
	}
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
		delete(pdws.unresolvedWorkers,w.staticHostPubKeyStr)
		if resp.staticErr == nil {
			// Loop through the set of sectors that the worker is claiming, and add
			// that worker to the piece list for any piece that the worker can
			// contribute to.
			for i, available := range resp.staticAvailables {
				if available {
					pdws.availablePieces[i] = append(pdws.availablePieces[i], w)
				}
			}
		}
		pdws.closeUpdateChans()
		pdws.mu.Unlock()
	}
}

// newPDWSByRoots will create a projectDownloadWorkerSet using the set of Merkle
// roots assocaited with a chunk. Some extra information about the chunk is
// required, such as the erasure coding information and the decryption
// information.
//
// The pdws will stop checking for repsonses from the workers once the context
// expires.
func (r *Renter) newPDWSByRoots(ctx context.Context, roots []crypto.Hash, ec modules.ErasureCoder, masterKey crypto.CipherKey, chunkIndex uint64) (*projectDownloadWorkerSet, error) {
	// Check that the number of roots provided is consistent with the erasure
	// coder provided.
	if len(roots) != ec.NumPieces() {
		return nil, fmt.Errorf("%v roots provided, but erasure coder specifies %v pieces", len(roots), ec.NumPieces())
	}

	// Create the worker set.
	pdws := &projectDownloadWorkerSet{
		availablePieces:   make([][]*worker, ec.NumPieces()),
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
			jobGeneric:         newJobGeneric(w.staticJobHasSectorQueue, r.tg.StopCtx().Done()),
		}
		if !w.staticJobHasSectorQueue.callAdd(jhs) {
			continue
		}

		// Create the unresolved worker for this job.
		pduw := &projectDownloadUnresolvedWorker{
			staticWorker: w,

			staticStartTime: time.Now(),

			// TODO: Fill out the other fields, not sure they are exposed at the
			// moment.
		}
		// Add the unresolved worker to the set of unresolved workers.
		pdws.unresolvedWorkers[w.staticHostPubKeyStr] = pduw
	}
	// Processing check - make sure that there are enough workers to complete
	// the job.
	if len(pdws.unresolvedWorkers) < ec.MinPieces() {
		return nil, errors.New("not enough workers available to kick off a piece lookup")
	}

	// Launch the background thread to update the pdws as workers complete.
	go pdws.threadedProcessResponses(ctx, responseChan)

	// Return the worker set.
	return pdws, nil
}
