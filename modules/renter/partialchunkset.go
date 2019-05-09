package renter

// TODO: Figure out how to include combined chunks in snapshots
// TODO: Combined chunks need to be deleted once they have reached full redundancy

import (
	"sync"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/renter/siafile"
)

type (
	// PartialChunkSet is a set used by the repair code to combine partial chunks
	// into full chunks. Chunks won't be combined right away but instead the set
	// waits for enough requests to build the best chunk and only then creates the
	// full chunk on disk.
	// NOTE: Currently the implementation assumes that every file will have at most
	// 1 CombinedChunk and that's the chunk at the end.
	PartialChunkSet struct {
		mu       sync.Mutex
		uh       *uploadHeap
		requests map[modules.ErasureCoderIdentifier]chunkRequestSet
	}

	// CombinedChunk is a chunk that was combined by the PartialChunkSet out of
	// multiple chunkRequests.
	CombinedChunk struct {
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

// NewPartialChunkSet creates a partial chunk set ready to combine partial
// chunks.
func NewPartialChunkSet() *PartialChunkSet {
	panic("not implemented yet")
}

// CombinedChunk accepts a new request for including a partial chunk in a
// combined chunk and if enough requests were already registered, or if a
// Combined chunk has previously been built for the specified partial chunk, it
// will return the combined chunk.
func (pcs *PartialChunkSet) CombinedChunk(entry *siafile.SiaFileSetEntry) (*CombinedChunk, error) {
	pcs.mu.Lock()
	defer pcs.mu.Unlock()

	// Check if the file already has a combined chunk assigned to it.
	if cid := entry.CombinedChunkID(); cid != "" {
		return pcs.loadCombinedChunk(cid)
	}

	// Check if a request already exists for that file.
	ec := entry.ErasureCode()
	ecid := ec.Identifier()
	if _, exists := pcs.requests[ecid]; exists {
		return nil, nil
	}

	// If not, add a new request.
	if _, exists := pcs.requests[ecid]; !exists {
		pcs.requests[ecid] = make(chunkRequestSet)
	}
	pcs.requests[ecid][entry.UID()] = &chunkRequest{}

	// See if we can combine multiple requests into a chunk.
	crs := pcs.requests[ecid]
	requests := crs.combineRequests()
	if requests == nil {
		return nil, nil // not enough requests yet
	}

	// Build combined chunk and return it.
	return crs.buildChunk(requests)
}

// loadCombinedChunk loads a CombinedChunk from disk using it's id.
func (pcs *PartialChunkSet) loadCombinedChunk(id siafile.CombinedChunkID) (*CombinedChunk, error) {
	panic("not implemented yet")
}

// combineRequests tries to combine multiple requests with the same erasure code
// id into a full chunk.
func (crs chunkRequestSet) combineRequests() []chunkRequest {
	panic("not implemented yet")
}

// buildChunk builds a chunk and stores it on disk using the given requests.
func (crs chunkRequestSet) buildChunk(requests []chunkRequest) (*CombinedChunk, error) {
	panic("not implemented yet")
}
