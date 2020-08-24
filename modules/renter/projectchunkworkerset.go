package renter

// TODO: Add a method to initialize a projectChunkWorkerSet with a set of
// merkleroot + host pairs, allowing the object to skip the initial HasSector
// scan of the network. This allows the projectChunkWorkerSet to replace legacy
// downloads.
//
// Replacing legacy downloads is desirable because the projectChunkWorkerSet has
// intelligent host selection logic based on latency and pricing.
//
// Open question whether that route should be refreshed over time. Either we can
// keep the corresponding siafile open and keep checking if there are updates,
// or we can do the network based checking over time.

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

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"

	"gitlab.com/NebulousLabs/errors"
)

var (
	// pcwsWorkerStateResetTime defines the amount of time that the pcws will
	// wait before resetting / refreshing the worker state.
	pcwsWorkerStateResetTime = build.Select(build.Var{
		Dev:      time.Minute * 10,
		Standard: time.Minute * 60 * 3,
		Testing:  time.Second * 15,
	}).(time.Duration)
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

// pcwsWorkerResponse contains a worker's response to a HasSector query. There
// is a list of piece indexes where the worker responded that they had the piece
// at that index.
//
// This structure is chosen so that chunk downloads can update in constant time
// as new workers report back with HasSector results.
type pcwsWorkerResponse struct {
	worker       *worker
	pieceIndices []uint64
}

// pcwsWorkerState contains the worker state for a single thread that is
// resolving which workers
type pcwsWorkerState struct {
	// unresolvedWorkers is the set of workers that are currently running
	// HasSector programs and have not yet finished.
	//
	// A map is used so that workers can be removed from the set in constant
	// time as they complete their HasSector jobs.
	unresolvedWorkers map[string]*pcwsUnresolvedWorker

	// ResolvedWorkers is an array that tracks which workers have responded to
	// HasSector queries and which sectors are available. This array is only
	// appended to as workers come back, meaning that chunk downloads can track
	// internally which elements of the array they have already looked at,
	// saving computational  time when updating.
	resolvedWorkers []*pcwsWorkerResponse

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

	// Utilities.
	staticRenter *Renter
	mu           sync.Mutex
}

// projectChunkWorkerSet is an object that contains a set of workers that can be
// used to download a single chunk. The object can be initialized with either a
// set of roots (for Skynet downloads) or with a siafile where the host-root
// pairs are already known.
//
// If the pcws is initialized with only a set of roots, it will immediately spin
// up a bunch of worker jobs to locate those roots on the network using
// HasSector programs.
//
// Once the pwcs has been initialized, it can be used repeatedly to download
// data from the chunk, and it will not need to repeat the network lookups.
type projectChunkWorkerSet struct {
	// workerState is a pointer to a single pcwsWorkerState, specifically the
	// most recent worker state that has launched.
	//
	// workerStateLaunch indicates when the workerState was launched, which is
	// used to figure out when the next worker state should be launched.
	updateInProgress      bool
	workerState           *pcwsWorkerState
	workerStateLaunchTime time.Time

	// Decoding and decryption information.
	staticChunkIndex   uint64
	staticErasureCoder modules.ErasureCoder
	staticMasterKey    crypto.CipherKey
	staticPieceRoots   []crypto.Hash

	// Utilities
	staticCtx              context.Context
	staticHasSectorTimeout time.Duration
	staticRenter           *Renter
	mu                     sync.Mutex
}

// closeUpdateChans will close all of the update chans and clear out the slice.
// This will cause any threads waiting for more results from the unresovled
// workers to unblock.
//
// Typically there will be a small number of channels, often 0 and often just 1.
func (ws *pcwsWorkerState) closeUpdateChans() {
	for _, c := range ws.workerUpdateChans {
		close(c)
	}
	ws.workerUpdateChans = nil
}

// registerForWorkerUpdate will create a channel and append it to the list of
// update chans in the worker state. When there is more information available
// about which worker is the best worker to select, the channel will be closed.
func (ws *pcwsWorkerState) registerForWorkerUpdate() <-chan struct{} {
	// Create the channel that will be closed upon update.
	c := make(chan struct{})

	// Consistency check - this method is not supposed to be called unless there
	// are unresolved workers that can return.
	if len(ws.unresolvedWorkers) == 0 {
		ws.staticRenter.log.Critical("call to 'registerForWorkerUpdate' when there are no unresolved workers")
		close(c) // Nothing else is going to close 'c', so we close it here.
		return c
	}

	ws.workerUpdateChans = append(ws.workerUpdateChans, c)
	return c
}

// managedHandleResponse will handle a HasSector response from a worker,
// updating the workerState accordingly.
func (ws *pcwsWorkerState) managedHandleResponse(resp *jobHasSectorResponse) {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	// Delete the worker from the set of unresolved workers. If the response
	// is not an error response, add the worker to any pieces that it
	// supports.
	w := resp.staticWorker
	delete(ws.unresolvedWorkers, w.staticHostPubKeyStr)
	ws.closeUpdateChans()
	if resp.staticErr != nil {
		return
	}

	// Create the list of pieces that the worker supports and add it to the
	// worker set.
	var indices []uint64
	for i, available := range resp.staticAvailables {
		if available {
			indices = append(indices, uint64(i))
		}
	}
	if len(indices) > 0 {
		ws.resolvedWorkers = append(ws.resolvedWorkers, &pcwsWorkerResponse{
			worker:       w,
			pieceIndices: indices,
		})
	}
}

// managedLaunchWorker will launch a job to determine which sectors of a chunk
// are available through that worker. The resulting unresolved worker is
// returned so it can be added to the pending worker state.
func (pcws *projectChunkWorkerSet) managedLaunchWorker(ctx context.Context, w *worker, responseChan chan *jobHasSectorResponse, ws *pcwsWorkerState) {
	// Check for gouging.
	//
	// TODO: May want to use a different gouging function.
	cache := w.staticCache()
	pt := w.staticPriceTable().staticPriceTable
	err := checkPDBRGouging(pt, cache.staticRenterAllowance, len(pcws.staticPieceRoots))
	if err != nil {
		pcws.staticRenter.log.Debugf("price gouging for chunk worker set detected in worker %v, err %v", w.staticHostPubKeyStr, err)
		return
	}

	// Create and launch the job.
	jhs := &jobHasSector{
		staticSectors:      pcws.staticPieceRoots,
		staticResponseChan: responseChan,
		jobGeneric:         newJobGeneric(w.staticJobHasSectorQueue, ctx.Done()),
	}
	// TODO: Make this a feature of generic jobs rather than having a custom
	// implementation for just the has sector job. Maybe rename to
	// callAddWithTimeEstimate.
	expectedCompletionTime, err := w.staticJobHasSectorQueue.callAddWithEstimate(jhs)
	if err != nil {
		pcws.staticRenter.log.Debugf("unable to add has sector job to %v, err %v", w.staticHostPubKeyStr, err)
		return
	}

	// Create the unresolved worker for this job.
	uw := &pcwsUnresolvedWorker{
		staticWorker: w,

		staticExpectedCompletionTime: expectedCompletionTime,
	}
	// Technically this doesn't need to be wrapped in a lock because the ws
	// hasn't been released to any other threads yet, but we grab the lock any
	// way because that's not obvious from the context of this function, and
	// there's not really a clean way to make it obvious. It shouldn't have much
	// performance overhead because the mutex will never have contention at this
	// point.
	ws.mu.Lock()
	ws.unresolvedWorkers[w.staticHostPubKeyStr] = uw
	ws.mu.Unlock()
}

// threadedFindWorkers will spin up a bunch of jobs to determine which workers
// have what pieces for the pcws, and then update the input worker state with
// the results.
func (pcws *projectChunkWorkerSet) threadedFindWorkers(allWorkersLaunchedChan chan<- struct{}, ws *pcwsWorkerState) {
	// Create a context for finding jobs which has a timeout for waiting on
	// HasSector requests to return.
	ctx, cancel := context.WithTimeout(pcws.staticCtx, pcws.staticHasSectorTimeout)
	defer cancel()

	// Launch all of the HasSector jobs for each worker. A channel is needed to
	// receive the responses, and the channel needs to be buffered to be equal
	// in size to the number of queries so that none of the workers sending
	// reponses get blocked sending down the channel.
	workers := ws.staticRenter.staticWorkerPool.callWorkers()
	responseChan := make(chan *jobHasSectorResponse, len(workers))
	for _, w := range workers {
		pcws.managedLaunchWorker(ctx, w, responseChan, ws)
	}
	// Signal that all of the workers have launched.
	close(allWorkersLaunchedChan)

	// Helper function to get the number of responses remaining. This allows the
	// for loop below to be a lot cleaner.
	responsesRemaining := func() int {
		ws.mu.Lock()
		rr := len(ws.unresolvedWorkers)
		ws.mu.Unlock()
		return rr
	}
	// Because there are timeouts on the HasSector programs, the longest that
	// this loop should be active is a little bit longer than the full timeout
	// for a single HasSector job.
	for responsesRemaining() > 0 {
		// Block until there is a worker response. Give up if the context times
		// out.
		var resp *jobHasSectorResponse
		select {
		case resp = <-responseChan:
		case <-ctx.Done():
			return
		}
		// Consistency check - should not be getting nil responses from the workers.
		if resp == nil {
			ws.staticRenter.log.Critical("nil response received")
			continue
		}

		// Parse the response.
		ws.managedHandleResponse(resp)
	}
}

// managedTryUpdateWorkerState will check whether the worker state needs to be
// refreshed. If so, it will refresh the worker state.
func (pcws *projectChunkWorkerSet) managedTryUpdateWorkerState() error {
	// The worker state does not need to be refreshed if it is recent or if
	// there is another refresh currently in progress.
	//
	// If multiple threads call 'managedTryUpdateWorkerState' at the same time,
	// the first will block while a new state is created, and the rest will not
	// block, which likely ends in the caller using the old state. This is fine,
	// because the state gets refreshed long before the refresh is actually
	// necessary.
	pcws.mu.Lock()
	if pcws.updateInProgress || time.Since(pcws.workerStateLaunchTime) < pcwsWorkerStateResetTime {
		pcws.mu.Unlock()
		return nil
	}
	// An update is needed. Set the flag that an update is in progress.
	pcws.updateInProgress = true
	pcws.mu.Unlock()

	// Create the new worker state and launch the thread that will create worker
	// jobs and collect responses from the workers.
	//
	// The concurrency here is a bit awkward because jobs cannot be launched
	// while the pcws lock is held, the workerState of the pcws cannot be set
	// until all the jobs are launched, and the context for timing out the
	// worker jobs needs to be created in the same thread that listens for the
	// responses. Though there are a lot of concurrency patterns at play here,
	// it was the cleanest thing I could come up with.
	allWorkersLaunchedChan := make(chan struct{})
	ws := &pcwsWorkerState{
		unresolvedWorkers: make(map[string]*pcwsUnresolvedWorker),

		staticRenter: pcws.staticRenter,
	}
	// Create an anonymous function to launch the thread to find workers, since
	// we need to provide input to the find workers function and tg.Launch does
	// not support that.
	findWorkers := func() {
		pcws.threadedFindWorkers(allWorkersLaunchedChan, ws)
	}
	// Launch the thread to find the workers for this launch state.
	err := pcws.staticRenter.tg.Launch(findWorkers)
	if err != nil {
		return errors.AddContext(err, "unable to launch worker set")
	}

	// Wait for the thread to indicate that all jobs are launched, the worker
	// state is not ready for use until all jobs have been launched. After that,
	// update the pcws so that the workerState in the pcws is the newest worker
	// state.
	<-allWorkersLaunchedChan
	pcws.mu.Lock()
	pcws.updateInProgress = false
	pcws.workerState = ws
	pcws.workerStateLaunchTime = time.Now()
	pcws.mu.Unlock()
	return nil
}

// newPCWSByRoots will create a worker set to download a chunk given just the
// set of sector roots associated with the pieces. The hosts that correspond to
// the roots will be determined by scanning the network with a large number of
// HasSector queries. Once opened, the projectChunkWorkerSet can be used to
// initiate many downloads.
func (r *Renter) newPCWSByRoots(ctx context.Context, hasSectorTimeout time.Duration, roots []crypto.Hash, ec modules.ErasureCoder, masterKey crypto.CipherKey, chunkIndex uint64) (*projectChunkWorkerSet, error) {
	// Check that the number of roots provided is consistent with the erasure
	// coder provided.
	if len(roots) != ec.NumPieces() {
		return nil, fmt.Errorf("%v roots provided, but erasure coder specifies %v pieces", len(roots), ec.NumPieces())
	}

	// Create the worker set.
	pcws := &projectChunkWorkerSet{
		staticChunkIndex:   chunkIndex,
		staticErasureCoder: ec,
		staticMasterKey:    masterKey,
		staticPieceRoots:   roots,

		staticCtx:              ctx,
		staticHasSectorTimeout: hasSectorTimeout,
		staticRenter:           r,
	}

	// The worker state is blank, ensure that everything can get started.
	err := pcws.managedTryUpdateWorkerState()
	if err != nil {
		return nil, errors.AddContext(err, "cannot create a new PCWS")
	}

	// Return the worker set.
	return pcws, nil
}
