package renter

// TODO: ??? Combined chunks need to be deleted once they have reached full redundancy
// TODO: Support repairing a partial chunk if redundancy dropped below 1x, the
// complete chunk was deleted but the source is available locally
// TODO: how to figure out which combined chunks are no longer useful?
// TODO: how to prune the mega files?
// TODO: force snapshots not to use partial uploads

import (
	"sync"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/renter/siafile"
)

type (
	// partialChunkSet is a set used by the repair code to combine partial chunks
	// into full chunks. Chunks won't be combined right away but instead the set
	// waits for enough requests to build the best chunk and only then creates the
	// full chunk on disk.
	// NOTE: Currently the implementation assumes that every file will have at most
	// 1 CombinedChunk and that's the chunk at the end.
	partialChunkSet struct {
		mu                sync.Mutex
		requests          map[modules.ErasureCoderIdentifier]chunkRequestSet
		combinedChunkRoot string
	}
)

type (
	// chunkRequest is a request to include a certain chunk of a specific file into
	// a combined chunk.
	chunkRequest struct {
	}

	// chunkRequestSet is a map of chunkRequests to avoid duplicates.
	chunkRequestSet map[siafile.SiafileUID]*chunkRequest
)

// newPartialChunkSet creates a partial chunk set ready to combine partial
// chunks.
func newPartialChunkSet() *partialChunkSet {
	return &partialChunkSet{}
}

// FetchLogicalCombinedChunk fetches the logical data for an
// unfinishedUploadChunk. It does so by checking if enough partial chunks are
// waiting to be repaired and combining them. If not enough partial chunks exist
// at the moment, it remembers the request and returns 'false'.
func (pcs *partialChunkSet) FetchLogicalCombinedChunk(chunk *unfinishedUploadChunk) (bool, error) {
	pcs.mu.Lock()
	defer pcs.mu.Unlock()
	if true {
		return false, nil // TODO: Remove this
	}

	// File has a combined chunk assigned to it. Load it from disk.
	entry := chunk.fileEntry
	if entry.CombinedChunkStatus() == siafile.CombinedChunkStatusCompleted {
		return true, pcs.fetchLogicalCombinedChunk(entry.Metadata().CombinedChunkID)
	}
	// Add a request for the file if it doesn't exist yet.
	ec := entry.ErasureCode()
	ecid := ec.Identifier()
	if _, exists := pcs.requests[ecid]; !exists {
		pcs.requests[ecid] = make(chunkRequestSet)
	}
	pcs.requests[ecid][entry.UID()] = &chunkRequest{}
	// See if we can combine multiple requests into a chunk.
	crs := pcs.requests[ecid]
	requests := crs.combineRequests()
	if requests == nil {
		return false, nil // not enough requests yet
	}
	//	If we can, build combined chunk.
	chunkID, chunkData, err := crs.buildChunk(requests)
	if err != nil {
		return false, err
	}
	// Let the SiaFile know about the combined chunk.
	// TODO: Also inform other siafiles.
	if err := siafile.SetCombinedChunk([]siafile.PartialChunkInfo{}, chunkID, chunkData, pcs.combinedChunkRoot); err != nil {
		return false, err
	}
	// Return the new combined chunk.
	return true, pcs.fetchLogicalCombinedChunk(chunkID)
}

// loadCombinedChunk loads a CombinedChunk from disk using it's chunkID.
func (pcs *partialChunkSet) fetchLogicalCombinedChunk(chunkID string) error {
	panic("not implemented yet")
}

// combineRequests tries to combine multiple requests with the same erasure code
// id into a full chunk.
func (crs chunkRequestSet) combineRequests() []chunkRequest {
	panic("not implemented yet")
}

// buildChunk builds a chunk and stores it on disk using the given requests.
func (crs chunkRequestSet) buildChunk(requests []chunkRequest) (string, []byte, error) {
	panic("not implemented yet")
}
