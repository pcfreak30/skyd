package renter

// TODO: Should we set up some sort of retry mechanism for workers that fail
// their HasSector tasks? It's possible that whatever the error was is a
// temporary netowrk error. We might want to retry every once in a while as well
// if the context is still open. just to stay current with all the workers that
// have a chunk. Maybe once an hour or so? Note need to be careful with the
// garbage collection here, ensuring that whoever opens the pcws closes it
// properly via cancelling the context.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"

	"gitlab.com/NebulousLabs/errors"
)

// pcwsUnreseovledWorker tracks an unresolved worker that is associated with a
// specific projectChunkWorkerSet. The timestamp indicates when the unresolved
// worker is expected to have a resolution, and is an estimate based on historic
// performance from the worker.
type pcwsUnresolvedWorker struct {
	// The worker that is performing the HasSector job.
	staticWorker *worker

	// The expected time that the HasSector job will finish, and the worker will
	// be able to resolve.
	staticExpectedCompletionTime time.Time
}

// projectChunkWorkerSet is an object that contains a set of workers that can be
// used to download a single chunk. This object is a project because it can be
// instantiated knowing only the Merkle roots of the pieces in the chunk, it
// does not need to know in advance which workers have each chunk.
//
// If the pcws is initialized with only a set of roots, it will immediately spin
// up a bunch of worker jobs to locate those roots on the network using
// HasSector programs.
//
// Once the pwcs has been initialized, it can be used repeatedly to download
// data from the chunk, and it will not need to repeat the network lookups.
type projectChunkWorkerSet struct {
	// unresolvedWorkers is the set of workers that are currently running
	// HasSector programs and have not yet finished.
	//
	// A map is used so that workers can be removed from the set in constant
	// time as they complete their HasSector jobs.
	unresolvedWorkers map[string]*pcwsUnresolvedWorker

	// availablePieces is an array of pieces with an array of workers for each
	// piece. If a worker is in the array for a piece, that worker is able to
	// download the piece.
	//
	// The same worker may appear in the arrays for multiple pieces, if the
	// worker is storing multiple pieces for one chunk.
	availablePieces [][]*worker

	// workerUpdateChans is used by download objects to block until more
	// information about the unresolved workers is available. All of the worker
	// update chans will be closed each time an unresolved worker returns a
	// response (regardless of whether the response is positive or negative).
	// The array will then be cleared.
	//
	// NOTE: Once 'unresolvedWorkers' has a length of zero, any attempt to add a
	// channel to the set of workerUpdateChans should fail, as there will be no
	// more updates.
	workerUpdateChans []chan struct{}

	// Decoding and decryption information.
	staticChunkIndex   uint64
	staticErasureCoder modules.ErasureCoder
	staticMasterKey    crypto.CipherKey
	staticPieceRoots   []crypto.Hash

	// Utilities
	staticCtx    context.Context
	staticRenter *Renter
	mu           sync.Mutex
}

// closeUpdateChans will close all of the update chans and clear out the slice.
// This will cause any threads waiting for more results from the unresovled
// workers to unblock.
//
// Typically there will be a small number of channels, often 0 and often just 1.
func (pcws *projectChunkWorkerSet) closeUpdateChans() {
	for _, c := range pcws.workerUpdateChans {
		close(c)
	}
	pcws.workerUpdateChans = nil
}

// registerForWorkerUpdate will create a channel and append it to the list of
// update chans in the pcws. When there is more information available about
// which worker is the best worker to select, the channel will be closed.
func (pcws *projectChunkWorkerSet) registerForWorkerUpdate() chan<- struct{} {
	c := make(chan struct{})
	pcws.workerUpdateChans = append(pcws.workerUpdateChans, c)
	return c
}

// managedResolveAllWorkers will delete any unresolved workers. Typically this
// is called due to the context of a pcws being cancelled, and the assumption is
// that any unresolved workers will continue to be unresolved.
func (pcws *projectChunkWorkerSet) managedResolveAllWorkers() {
	pcws.mu.Lock()
	defer pcws.mu.Unlock()

	// Wipe out the set of unresolved workers, we are assuming at this point
	// that they will not resolve.
	pcws.unresolvedWorkers = make(map[string]*pcwsUnresolvedWorker)
	pcws.closeUpdateChans()
}

// threadedProcessResponses will wait for worker responses to come back, and
// update the pcws as the responses come in.
//
// This thread should be the only one with access to the responseChan.
func (pcws *projectChunkWorkerSet) threadedProcessResponses(responseChan <-chan *jobHasSectorResponse) {
	// Upon exit, clean up the rest of the unresolved workers. This is
	// particularly helpful for situations where the context of the pcws is
	// closed.
	defer pcws.managedResolveAllWorkers()

	// Helper function to make the loop exit condition more clear.
	responsesRemaining := func() int {
		pcws.mu.Lock()
		rr := len(pcws.unresolvedWorkers)
		pcws.mu.Unlock()
		return rr
	}

	piecesFound := 0
	for responsesRemaining() > 0 {
		// Block until there is a worker response.
		var resp *jobHasSectorResponse
		select {
		case resp = <-responseChan:
		case <-pcws.staticCtx.Done():
			return
		}
		// Consistency check - should not be getting nil responses from the workers.
		if resp == nil {
			pcws.staticRenter.log.Critical("nil response received")
			return
		}

		// Delete the worker from the set of unresolved workers. If the response
		// is not an error response, add the worker to any pieces that it
		// supports.
		w := resp.staticWorker
		pcws.mu.Lock()
		delete(pcws.unresolvedWorkers, w.staticHostPubKeyStr)
		if resp.staticErr == nil {
			// Loop through the set of sectors that the worker is claiming, and add
			// that worker to the piece list for any piece that the worker can
			// contribute to.
			for i, available := range resp.staticAvailables {
				if available {
					if len(pcws.availablePieces[i]) == 0 {
						piecesFound++
					}
					pcws.availablePieces[i] = append(pcws.availablePieces[i], w)
				}
			}
		}
		pcws.closeUpdateChans()
		// Check whether this download has failed and can be aborted. This
		// should cause all in-progress downloads to update and fail.
		if len(pcws.unresolvedWorkers)+piecesFound < pcws.staticErasureCoder.MinPieces() {
			pcws.mu.Unlock()
			return
		}
		pcws.mu.Unlock()
	}
}

// newPCWSByRoots will create a worker set to download a chunk given just the
// set of sector roots associated with the pieces. The hosts that correspond to
// the roots will be determined by scanning the network with a large number of
// HasSector queries. Once opened, the projectChunkWorkerSet can be used to
// initiate many downloads.
func (r *Renter) newPCWSByRoots(ctx context.Context, roots []crypto.Hash, ec modules.ErasureCoder, masterKey crypto.CipherKey, chunkIndex uint64) (*projectChunkWorkerSet, error) {
	// Check that the number of roots provided is consistent with the erasure
	// coder provided.
	if len(roots) != ec.NumPieces() {
		return nil, fmt.Errorf("%v roots provided, but erasure coder specifies %v pieces", len(roots), ec.NumPieces())
	}

	// Create the worker set.
	pcws := &projectChunkWorkerSet{
		unresolvedWorkers: make(map[string]*pcwsUnresolvedWorker),

		availablePieces: make([][]*worker, ec.NumPieces()),

		staticChunkIndex:   chunkIndex,
		staticErasureCoder: ec,
		staticMasterKey:    masterKey,
		staticPieceRoots:   roots,

		staticCtx:    ctx,
		staticRenter: r,
	}

	// Launch all of the HasSector jobs for each worker. A channel is needed to
	// receive the responses, and the channel needs to be buffered to be equal
	// in size to the number of queries so that none of the workers sending
	// reponses get blocked sending down the channel.
	workers := r.staticWorkerPool.callWorkers()
	responseChan := make(chan *jobHasSectorResponse, len(workers))
	for _, w := range workers {
		// Check for gouging.
		//
		// TODO: May want to use a different gouging function.
		cache := w.staticCache()
		pt := w.staticPriceTable().staticPriceTable
		err := checkPDBRGouging(pt, cache.staticRenterAllowance, len(roots))
		if err != nil {
			r.log.Debugf("price gouging for chunk worker set detected in worker %v, err %v", w.staticHostPubKeyStr, err)
			continue
		}

		// Create and launch the job.
		jhs := &jobHasSector{
			staticSectors:      roots,
			staticResponseChan: responseChan,
			jobGeneric:         newJobGeneric(w.staticJobHasSectorQueue, ctx.Done()),
		}
		// TODO: Make this a feature of generic jobs rather than having a custom
		// implementation for just the has sector job.
		expectedCompletionTime, err := w.staticJobHasSectorQueue.callAddWithEstimate(jhs)
		if err != nil {
			r.log.Debugf("unable to add has sector job to %v, err %v", w.staticHostPubKeyStr, err)
			continue
		}

		// Create the unresolved worker for this job.
		uw := &pcwsUnresolvedWorker{
			staticWorker: w,

			staticExpectedCompletionTime: expectedCompletionTime,
		}
		// Add the unresolved worker to the set of unresolved workers.
		pcws.unresolvedWorkers[w.staticHostPubKeyStr] = uw
	}

	// Processing check - make sure that there are enough workers to complete
	// the job.
	if len(pcws.unresolvedWorkers) < ec.MinPieces() {
		return nil, errors.New("not enough workers available to kick off a piece lookup")
	}

	// Launch the background thread to update the pcws as workers complete.
	go pcws.threadedProcessResponses(responseChan)

	// Return the worker set.
	return pcws, nil
}
