package renter

import (
	"fmt"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
)

// projectDownloadWorkerSet is an object that tracks workers that are
// potentially able to fetch a piece for a single chunk. The chunkWorkerSet can
// either be initialized with a set of MerkleRoots, where it is unknown which
// hosts have the pieces, or the chunkWorkerSet can be initialized with a set of
// known root-host pairs. In the former case, the chunkWorkerSet will issue a
// set of HasSector calls to the network to figure out which workers can be used
// to fetch pieces of the chunk.
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

	// usableWorkers is the set of workers that are known to have a piece for
	// this chunk.
	usableWorkers map[string]*projectDownloadUsableWorker

	// Decoding and decryption information.
	chunkIndex   uint64
	erasureCoder modules.ErasureCoder
	masterKey    crypto.CipherKey

	// Utilities
	mu sync.Mutex
}

// projectDownloadUsableWorker is a pairing of a worker and the indices of the
// pieces that the worker is able to fetch for the chunk.
type projectDownloadUsableWorker struct {
	worker       *worker
	pieceIndices []uint64
}

// projectDownloadUnresolvedWorker pairs a worker with a timestamp to indicate
// the moment that HasSector is expected to return. If that time is in the past,
// the worker is overdue and should get a result any moment, but may also be in
// an error state and therefore may not get a result for a while.
type projectDownloadUnresolvedWorker struct {
	// The worker that is performing the HasSector job.
	worker *worker

	// The time that the HasSector job started.
	startTime time.Time

	// The amount of bandwidth in the upload queue and download queue when the
	// HasSector request was added to the queue.
	startDLQueue uint64
	startULQueue uint64

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

// newPDWSByRoots will create a projectDownloadWorkerSet using the set of Merkle
// roots assocaited with a chunk. Some extra information about the chunk is
// required, such as the erasure coding information and the decryption
// information.
func (r *Renter) newPDWSByRoots(roots []crypto.Hash, ec modules.ErasureCoder, masterKey crypto.CipherKey, chunkIndex uint64) (*projectDownloadWorkerSet, error) {
	// Check that the number of roots provided is consistent with the erasure
	// coder provided.
	if len(roots) != ec.NumPieces() {
		return nil, fmt.Errorf("%v roots provided, but erasure coder specifies %v pieces", len(roots), ec.NumPieces())
	}
	// TODO: Remove this check once HasSector supports checking multiple roots.
	if len(roots) > 1 {
		return nil, fmt.Errorf("only 1-of-N redundancy is currently supported")
	}

	// Create the worker set.
	pdws := &projectDownloadWorkerSet{
		unresolvedWorkers: make(map[string]*projectDownloadUnresolvedWorker),
		usableWorkers:     make(map[string]*projectDownloadUsableWorker),

		chunkIndex:   chunkIndex,
		erasureCoder: ec,
		masterKey:    masterKey,
	}

	// Launch all of the HasSector jobs for each worker.
	workers := r.staticWorkerPool.callWorkers()
	for _, w := range workers {
		// Check for gouging.
		cache := w.staticCache()
		pt := w.staticPriceTable().staticPriceTable
		err := checkPDBRGouging(pt, cache.staticRenterAllowance)
		if err != nil {
			r.log.Debugf("price gouging detected in worker %v, err %v", w.staticHostPubKeyStr, err)
			continue
		}

		// TODO: set up the receiver for when the job is done.

		jhs := &jobHasSector{
			// TODO: pass in all the roots
			staticSector:       roots[0],
			staticResponseChan: staticResponseChan,
			jobGeneric:         newJobGeneric(w.staticJobHasSectorQueue, r.tg.StopCtx().Done()),
		}
		if !w.staticJobHasSectorQueue.callAdd(jhs) {
			continue
		}
	}

	// TODO: Launch a goroutine to recieve the HasSector results.
}
