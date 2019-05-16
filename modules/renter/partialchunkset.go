package renter

// TODO: ??? Combined chunks need to be deleted once they have reached full redundancy
// TODO: Support repairing a partial chunk if redundancy dropped below 1x, the
// complete chunk was deleted but the source is available locally

import (
	"fmt"
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
		mu       sync.Mutex
		requests map[modules.ErasureCoderIdentifier]chunkRequestSet
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
	fmt.Println("not implemented yet")
	return &partialChunkSet{}
}

// FetchLogicalCombinedChunk fetches the logical data for an
// unfinishedUploadChunk. It does so by checking if enough partial chunks are
// waiting to be repaired and combining them. If not enough partial chunks exist
// at the moment, it remembers the request and returns 'false'.
func (pcs *partialChunkSet) FetchLogicalCombinedChunk(chunk *unfinishedUploadChunk) (bool, error) {
	fmt.Println("not implemented yet")
	pcs.mu.Lock()
	defer pcs.mu.Unlock()
	return false, nil

	// Check if the file already has a combined chunk assigned to it.
	//	if cid := entry.CombinedChunkID(); cid != "" {
	//		return pcs.loadCombinedChunk(cid)
	//	}
	//
	//	// Check if a request already exists for that file.
	//	ec := entry.ErasureCode()
	//	ecid := ec.Identifier()
	//	if _, exists := pcs.requests[ecid]; exists {
	//		return nil, nil
	//	}
	//
	//	// If not, add a new request.
	//	if _, exists := pcs.requests[ecid]; !exists {
	//		pcs.requests[ecid] = make(chunkRequestSet)
	//	}
	//	pcs.requests[ecid][entry.UID()] = &chunkRequest{}
	//
	//	// See if we can combine multiple requests into a chunk.
	//	crs := pcs.requests[ecid]
	//	requests := crs.combineRequests()
	//	if requests == nil {
	//		return nil, nil // not enough requests yet
	//	}
	//
	//	// Build combined chunk and return it.
	//	return crs.buildChunk(requests)
}

// loadCombinedChunk loads a CombinedChunk from disk using it's chunkIndex.
func (pcs *partialChunkSet) loadCombinedChunk(chunkIndex uint64) error {
	panic("not implemented yet")
}

// combineRequests tries to combine multiple requests with the same erasure code
// id into a full chunk.
func (crs chunkRequestSet) combineRequests() []chunkRequest {
	panic("not implemented yet")
}

// buildChunk builds a chunk and stores it on disk using the given requests.
func (crs chunkRequestSet) buildChunk(requests []chunkRequest) error {
	panic("not implemented yet")
}
