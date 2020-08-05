package renter

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
