package renter

import (
	"context"
)

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
	unresolvedWorkers map[string]*projectDownloadUnresolvedWorker

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
func (pcws *projectChunkWorkersSet) registerForWorkerUpdate() chan<- struct{} {
	c := make(chan struct{})
	pcws.workerUpdateChans = append(pcws.workerUpdateChans, c)
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
//
// TODO: Rewrite this whole thing.
func (r *Renter) newPDWSByRoots(ctx context.Context, roots []crypto.Hash, ec modules.ErasureCoder, masterKey crypto.CipherKey, chunkIndex uint64) (*projectDownloadWorkerSet, error) {
	// Check that the number of roots provided is consistent with the erasure
	// coder provided.
	if len(roots) != ec.NumPieces() {
		return nil, fmt.Errorf("%v roots provided, but erasure coder specifies %v pieces", len(roots), ec.NumPieces())
	}

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
