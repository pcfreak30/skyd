package renter

// TODO: Since there are situations where a projectChunkWorkerSet can be around
// for long periods of time - hours or more - there should be a re-scan
// mechanism of some sort on this object, where after a few hours the object may
// choose to re-do the HasSector scan of the network.
//
// When doing this, all that should have to happen is replacing the empty
// unresolvedWorkers map with a new, fresh map, and then kicking off another
// round of 'threadedProcessResponses'.
//
// The object should only hang around for hours if it is being used
// continuously, meaning that these periodic rescans are not a waste of
// resources.
//
// The frequency of refreshing the worker set should be longer by a significant
// margin than the timeout of the HasSector worker job. There should be a test
// written to ensure that. This is to ensure that multiple
// 'threadedProcessResponses' calls are never running simultaneously. Can
// probably also write a sanity check using a common variable to ensure this.

// TODO: Add a method to initialize a projectChunkWorkerSet with a set of
// merkleroot + host pairs, allowing the object to skip the initial HasSector
// scan of the network. This allows the projectChunkWorkerSet to replace legacy
// downloads.
//
// Replacing legacy downloads is desirable because the projectChunkWorkerSet has
// intelligent host selection logic based on latency and pricing.

// TODO: Need some way to prune workers from the set of available pieces, or at
// least mark the workers as consistently unsucecssful for a given piece. And
// then perhaps feed that back into the repair system so that it knows a host is
// flaky on a particular piece, so maybe that piece can be repaired for the
// host.

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
// used to download a single chunk. The object can be initialized with ei
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
	//
	// NOTE: Workers can be added or reomved from the set of available pieces
	// arbitrarily, particularly when the worker set is refreshing itself in the
	// background.
	availablePieces [][]*worker

	// workerUpdateChans is used by download objects to block until more
	// information about the unresolved workers is available. All of the worker
	// update chans will be closed each time an unresolved worker returns a
	// response (regardless of whether the response is positive or negative).
	// The array will then be cleared.
	//
	// NOTE: Once 'unresolvedWorkers' has a length of zero, any attempt to add a
	// channel to the set of workerUpdateChans should fail, as there will be no
	// more updates. NOTE: Once refreshes are happening, it will be possible for
	// the unresolvedWorkers list to go from zero back to having elements again,
	// but it still should be considered an error to register for an update once
	// the length is zero because a long time can transpire.
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
	// Consistency check - this method is not supposed to be called unless there
	// are unresolved workers that can return.
	if len(pcws.unresolvedWorkers) == 0 {
		pcws.staticRenter.log.Critical("call to 'registerForWorkerUpdate' when there are no unresolved workers")
	}

	c := make(chan struct{})
	pcws.workerUpdateChans = append(pcws.workerUpdateChans, c)
	return c
}

// threadedProcessResponses will wait for worker responses to come back, and
// update the pcws as the responses come in. Note that it will continue to wait
// for responses until its context is canceled, even in situations where there
// are not enough workers remaining for any downloads using the worker set to
// succeed.
//
// This thread should be the only one with access to the responseChan.
func (pcws *projectChunkWorkerSet) threadedProcessResponses(responseChan <-chan *jobHasSectorResponse) {
	// Helper function to make the loop exit condition more clear.
	responsesRemaining := func() int {
		pcws.mu.Lock()
		rr := len(pcws.unresolvedWorkers)
		pcws.mu.Unlock()
		return rr
	}

	// Because there are timeouts on the HasSector programs, the longest that
	// this loop should be active is a little bit longer than the full timeout
	// for a single HasSector job.
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
		pcws.closeUpdateChans()
		if resp.staticErr != nil {
			pcws.mu.Unlock()
			continue
		}
		// Loop through the set of sectors that the worker is claiming, and add
		// that worker to the piece list for any piece that the worker can
		// contribute to.
		for i, available := range resp.staticAvailables {
			if available {
				pcws.availablePieces[i] = append(pcws.availablePieces[i], w)
			}
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
		// implementation for just the has sector job. Maybe rename to
		// callAddWithTimeEstimate.
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
