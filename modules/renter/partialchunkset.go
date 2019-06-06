package renter

// TODO: ??? Combined chunks need to be deleted once they have reached full redundancy
// TODO: Support repairing a partial chunk if redundancy dropped below 1x, the
// complete chunk was deleted but the source is available locally
// TODO: how to figure out which combined chunks are no longer useful?
// TODO: how to prune the mega files?
// TODO: force snapshots not to use partial uploads
// TODO: make sure we don't push the same combined chunk into the repair heap multiple times in parallel

import (
	"encoding/hex"
	"fmt"
	"sort"
	"sync"

	"gitlab.com/NebulousLabs/fastrand"

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
		sf *siafile.SiaFileSetEntry
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
	pcs.requests[ecid][entry.UID()] = &chunkRequest{
		sf: chunk.fileEntry.CopyEntry(), // Copy the SiaFileSetEntry for the request
	}
	// See if we can combine multiple requests into a chunk.
	crs := pcs.requests[ecid]
	requests := crs.combineRequests()
	if requests == nil {
		return false, nil // not enough requests yet
	}
	// We removed the requests from the set of requests. We need to close them when
	// done. If something goes wrong they will simply be added again by the repair
	// code later.
	defer func() {
		for _, request := range requests {
			_ = request.sf.Close()
		}
	}()
	//	If we can, build combined chunk.
	chunkID, chunkData, pci, err := pcs.buildChunk(requests)
	if err != nil {
		return false, err
	}
	cci := siafile.NewCombinedChunkInfo(chunkID, chunkData, pci)
	// Let the SiaFile know about the combined chunk.
	if err := siafile.SetCombinedChunk(cci, pcs.combinedChunkRoot); err != nil {
		return false, err
	}
	// Return the new combined chunk.
	return true, pcs.fetchLogicalCombinedChunk(chunkID)
}

// loadCombinedChunk loads a CombinedChunk from disk using it's chunkID.
func (pcs *partialChunkSet) fetchLogicalCombinedChunk(chunkID string) error {
	panic("not implemented yet")
}

// buildChunk builds a chunk and returns it together with its ID.
func (pcs *partialChunkSet) buildChunk(requests []*chunkRequest) (string, []byte, []siafile.PartialChunkInfo, error) {
	var chunkInfos []siafile.PartialChunkInfo
	chunkID := hex.EncodeToString(fastrand.Bytes(16))
	chunkData := make([]byte, requests[0].sf.ChunkSize())
	copiedData := 0
	totalData := 0
	for _, request := range requests {
		partialChunk, err := request.sf.LoadPartialChunk()
		if err != nil {
			return "", nil, nil, err
		}
		totalData += len(partialChunk)
		n := copy(chunkData[copiedData:], partialChunk)
		chunkInfos = append(chunkInfos, siafile.NewPartialChunkInfo(uint64(len(partialChunk)), uint64(copiedData), request.sf.SiaFile))
		copiedData += n
	}
	if totalData != copiedData {
		return "", nil, nil, fmt.Errorf("only %v out of %v bytes were copied", copiedData, totalData)
	}
	return chunkID, chunkData, chunkInfos, nil
}

// combineRequests tries to combine multiple requests with the same erasure code
// id into a full chunk.
// TODO: This is a very trivial algorithm and can probably be improved a lot.
// Right now it's greedy and might not perform well for large chunks. It also
// won't consider the total size of all requests before starting. So even if all
// the requests are below ChunkSize it will try to form a chunk.
func (crs chunkRequestSet) combineRequests() []*chunkRequest {
	// No requests yet.
	if len(crs) == 0 {
		return nil
	}
	// Get all the requests in a slice
	requests := make([]*chunkRequest, 0, len(crs))
	for _, cr := range crs {
		requests = append(requests, cr)
	}
	// Sort the requests in decending order.
	sort.Slice(requests, func(i, j int) bool {
		sfi := requests[i].sf
		sfj := requests[j].sf
		sfiSize := sfi.Size() % sfi.ChunkSize()
		sfjSize := sfj.Size() % sfj.ChunkSize()
		return sfjSize < sfiSize
	})
	// Choose requests to fill up the combined chunk.
	var chosenRequests []*chunkRequest
	for len(requests) > 0 {
		chosenRequests = []*chunkRequest{} // reset
		chunkSize := requests[0].sf.ChunkSize()
		totalSize := uint64(0)
		for _, request := range requests {
			size := request.sf.Size() % chunkSize
			if totalSize+size <= chunkSize {
				chosenRequests = append(chosenRequests, request)
				totalSize += size
			}
		}
		// Check if the totalSize is within the acceptable threshold of 10%.
		if chunkSize-totalSize > uint64(0.1*float64(chunkSize)) {
			requests = requests[1:] // ignore the largest request on the next try
			return nil              // not good enough
		}
		// Remove the chose requests from the set.
		for _, request := range chosenRequests {
			delete(crs, request.sf.UID())
		}
		return chosenRequests
	}
	return nil
}
