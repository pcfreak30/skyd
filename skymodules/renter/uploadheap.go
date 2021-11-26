package renter

// TODO: replace managedRefreshHostsAndWorkers with structural updates to the
// worker pool. The worker pool should maintain the map of hosts that
// managedRefreshHostsAndWorkers builds every call, and the contractor should
// work with the worker pool to instantly notify the worker pool of any changes
// to the set of contracts.

import (
	"container/heap"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/opentracing/opentracing-go"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/NebulousLabs/threadgroup"

	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem/siafile"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
)

// repairTarget is a helper type for telling the repair heap what type of
// files/chunks to target for repair
type repairTarget int

// targetStuckChunks tells the repair loop to target stuck chunks for repair and
// targetUnstuckChunks tells the repair loop to target unstuck chunks for repair
const (
	targetStuckChunks repairTarget = iota + 1
	targetUnstuckChunks
	targetBackupChunks
)

type chunkType bool

var (
	// chunkTypeStreamChunk indicates that a chunk is being uploaded or repaired
	// by a stream.
	chunkTypeStreamChunk chunkType = true

	// chunkTypeLocalChunk indicates that a chunk is being uploaded or repaired
	// from data on disk. That data can either be local to the renter or data on
	// the hosts.
	chunkTypeLocalChunk chunkType = false
)

var (
	// DefaultPauseDuration is the default duration that the repairs and uploads
	// will be paused
	DefaultPauseDuration = build.Select(build.Var{
		Standard: 10 * time.Minute,
		Dev:      1 * time.Minute,
		Testing:  100 * time.Millisecond,
	}).(time.Duration)
)

// uploadChunkHeap is a bunch of priority-sorted chunks that need to be either
// uploaded or repaired.
type uploadChunkHeap []*unfinishedUploadChunk

// Implementation of heap.Interface for uploadChunkHeap.
func (uch uploadChunkHeap) Len() int { return len(uch) }
func (uch uploadChunkHeap) Less(i, j int) bool {
	// The chunks in the uploadHeap are prioritized in the following order:
	//  1) Priority Chunks
	//    - These are chunks added by a subsystem that are deemed more important
	//      than all other chunks. An example would be if the upload of a single
	//      chunk is a blocking task.
	//
	//  2) File Recently Successful Chunks
	//    - These are stuck chunks that are from a file that recently had a
	//      successful repair
	//
	//  3) Stuck Chunks
	//    - These are chunks added by the stuck loop
	//
	//  4) Remote Chunks
	//    - These are chunks of a siafile that do not have a local file to repair
	//    from
	//
	//  5) Worst Health Chunk
	//    - The base priority of chunks in the heap is by the worst health

	// Check for Priority chunks
	//
	// If only chunk i is high priority, return true to prioritize it.
	if uch[i].staticPriority && !uch[j].staticPriority {
		return true
	}
	// If only chunk j is high priority, return false to prioritize it.
	if !uch[i].staticPriority && uch[j].staticPriority {
		return false
	}

	// Check for File Recently Successful Chunks
	//
	// If only chunk i's file was recently successful, return true to prioritize
	// it.
	if uch[i].fileRecentlySuccessful && !uch[j].fileRecentlySuccessful {
		return true
	}
	// If only chunk j's file was recently successful, return true to prioritize
	// it.
	if !uch[i].fileRecentlySuccessful && uch[j].fileRecentlySuccessful {
		return false
	}

	// Check for Stuck Chunks
	//
	// If chunk i is stuck, return true to prioritize it.
	if uch[i].stuck && !uch[j].stuck {
		return true
	}
	// If chunk j is stuck, return true to prioritize it.
	if !uch[i].stuck && uch[j].stuck {
		return false
	}

	// Check for Remote Chunks
	if !uch[i].onDisk && uch[j].onDisk {
		return true
	}
	if uch[i].onDisk && !uch[j].onDisk {
		return false
	}

	// Base case, Check for worst health
	return uch[i].health > uch[j].health
}
func (uch uploadChunkHeap) Swap(i, j int)       { uch[i], uch[j] = uch[j], uch[i] }
func (uch *uploadChunkHeap) Push(x interface{}) { *uch = append(*uch, x.(*unfinishedUploadChunk)) }
func (uch *uploadChunkHeap) Pop() interface{} {
	old := *uch
	n := len(old)
	x := old[n-1]
	*uch = old[:n-1]
	return x
}

// removeByID removes the chunk with the corresponding uploadChunkID from the heap.
//
// NOTE: This is intentionally not using the Remove interface of the heap
// because the uploadChunkHeap does not utilize an index for the chunks in the
// heap. The index of the chunks in the heap refers to the siafile index.
func (uch *uploadChunkHeap) removeByID(uuc *unfinishedUploadChunk) {
	// Find the chunk index in the heap
	index := -1
	for i, c := range *uch {
		if c.id != uuc.id {
			continue
		}
		index = i
		break
	}

	//Remove the chunk from the heap
	if index == -1 || (*uch)[index].id != uuc.id {
		// Chunk not found
		return
	}
	old := *uch
	copy(old[index:], old[index+1:])
	*uch = old[:len(old)-1]
}

// reset clears the uploadChunkHeap and makes sure all the files belonging to
// the chunks are closed
func (uch *uploadChunkHeap) reset() (err error) {
	for _, c := range *uch {
		err = errors.Compose(err, c.Close())
	}
	*uch = uploadChunkHeap{}
	return err
}

// uploadHeap contains a priority-sorted heap of all the chunks being uploaded
// to the renter, along with some metadata.
type uploadHeap struct {
	heap uploadChunkHeap

	// heapChunks is a map containing all the chunks that are currently in the
	// heap. Chunks are added and removed from the map when chunks are pushed
	// and popped off the heap
	//
	// repairingChunks is a map containing all the chunks are that currently
	// assigned to workers and are being repaired/worked on.
	stuckHeapChunks   map[uploadChunkID]*unfinishedUploadChunk
	unstuckHeapChunks map[uploadChunkID]*unfinishedUploadChunk

	// Internal control channels
	newUploads        chan struct{}
	repairNeeded      chan struct{}
	stuckChunkFound   chan struct{}
	stuckChunkSuccess chan struct{}

	// External control channels
	pauseChan     chan struct{}
	pauseDuration time.Duration
	pauseStart    time.Time
	pauseTimer    *time.Timer

	mu sync.Mutex
}

// managedExists checks if a chunk currently exists in the upload heap. A chunk
// exists in the upload heap if it exists in any of the heap's tracking maps
func (uh *uploadHeap) managedExists(id uploadChunkID) bool {
	uh.mu.Lock()
	defer uh.mu.Unlock()
	_, existsUnstuckHeap := uh.unstuckHeapChunks[id]
	_, existsStuckHeap := uh.stuckHeapChunks[id]
	return existsUnstuckHeap || existsStuckHeap
}

// managedIsPaused returns the boolean indicating whether or not the user
// has paused the repairs and uploads
func (uh *uploadHeap) managedIsPaused() bool {
	uh.mu.Lock()
	defer uh.mu.Unlock()
	select {
	case <-uh.pauseChan:
		return false
	default:
		return true
	}
}

// managedLen will return the length of the heap
func (uh *uploadHeap) managedLen() int {
	uh.mu.Lock()
	uhLen := uh.heap.Len()
	uh.mu.Unlock()
	return uhLen
}

// managedPauseStatus will return whether or not the uploadheap is paused and
// the duration of the pause
func (uh *uploadHeap) managedPauseStatus() (bool, time.Time) {
	uh.mu.Lock()
	defer uh.mu.Unlock()
	endTime := uh.pauseStart.Add(uh.pauseDuration)
	select {
	case <-uh.pauseChan:
		return false, endTime
	default:
		return true, endTime
	}
}

// managedNumStuckChunks returns total number of stuck chunks in the heap and
// the number of stuck chunks that were added at random as opposed to being
// added due to a recently successful file repair
func (uh *uploadHeap) managedNumStuckChunks() (total int, random int) {
	uh.mu.Lock()
	defer uh.mu.Unlock()
	for _, chunk := range uh.stuckHeapChunks {
		if !chunk.fileRecentlySuccessful {
			random++
		}
		total++
	}
	return total, random
}

// managedPause creates the pauseChan and initiates the pauseTimer for the
// duration requested
func (uh *uploadHeap) managedPause(duration time.Duration) {
	uh.mu.Lock()
	defer uh.mu.Unlock()
	uh.pauseDuration = duration
	uh.pauseStart = time.Now()
	select {
	case <-uh.pauseChan:
		// Repairs and Uploads are not currently paused so pause them
		uh.pauseChan = make(chan struct{})
		uh.pauseTimer = time.AfterFunc(duration, func() {
			uh.mu.Lock()
			close(uh.pauseChan)
			uh.pauseDuration = 0
			uh.pauseStart = time.Time{}
			uh.mu.Unlock()
		})
	default:
		// Repairs and Uploads are paused so reset the timer duration
		uh.pauseTimer.Reset(duration)
	}
}

// managedPush will try and add a chunk to the upload heap. If the chunk is
// added it will return true otherwise it will return false. If false is
// returned due to an existing uploadchunk being in the heap already, that chunk
// will be returned to allow the caller to wait for that chunk to be done
// uploading.
//
// If the chunkType is set to streamChunk then managedPush will add the chunk
// directly to the repair map so that it is tracked by the heap but won't be
// popped off the heap by the regular repair loop. In this case the caller is
// then responsible for ensuring the chunk is sent to the workers for repair.
func (uh *uploadHeap) managedPush(uuc *unfinishedUploadChunk, ct chunkType) (*unfinishedUploadChunk, bool) {
	// Grab chunk stuck status and update the chunkCreationTime
	uuc.mu.Lock()
	chunkStuck := uuc.stuck
	if uuc.chunkCreationTime.IsZero() {
		uuc.chunkCreationTime = time.Now()
	}
	uuc.mu.Unlock()

	// Check if chunk is in any of the heap maps
	uh.mu.Lock()
	defer uh.mu.Unlock()
	uucUnstuck, existsUnstuckHeap := uh.unstuckHeapChunks[uuc.id]
	uucStuck, existsStuckHeap := uh.stuckHeapChunks[uuc.id]
	exists := existsUnstuckHeap || existsStuckHeap

	// Check if the chunk can be added to the heap
	canAddStuckChunk := chunkStuck && !exists && len(uh.stuckHeapChunks) < maxStuckChunksInHeap && ct == chunkTypeLocalChunk
	canAddUnstuckChunk := !chunkStuck && !exists && ct == chunkTypeLocalChunk

	// Add the chunk to the heap
	if canAddStuckChunk {
		uh.stuckHeapChunks[uuc.id] = uuc
		heap.Push(&uh.heap, uuc)
		return nil, true
	} else if canAddUnstuckChunk {
		uh.unstuckHeapChunks[uuc.id] = uuc
		heap.Push(&uh.heap, uuc)
		return nil, true
	} else if ct == chunkTypeStreamChunk {
		// Make sure the chunk is removed from unstuck and stuck maps
		delete(uh.unstuckHeapChunks, uuc.id)
		delete(uh.stuckHeapChunks, uuc.id)

		// Remove the chunk from the heap slice if it currently exists
		if exists {
			uh.heap.removeByID(uuc)
		}
		return uuc, true
	}
	// Return a potentially existing upload chunk that prevented our chunk
	// from being added to the heap.
	if existsUnstuckHeap {
		return uucUnstuck, false
	} else if existsStuckHeap {
		return uucStuck, false
	}
	return nil, false
}

// managedPop will pull a chunk off of the upload heap and return it.
func (uh *uploadHeap) managedPop() (uc *unfinishedUploadChunk) {
	uh.mu.Lock()
	if len(uh.heap) > 0 {
		uc = heap.Pop(&uh.heap).(*unfinishedUploadChunk)
		delete(uh.unstuckHeapChunks, uc.id)
		delete(uh.stuckHeapChunks, uc.id)
	}
	uh.mu.Unlock()
	return uc
}

// managedReset will reset the slice and maps within the heap to free up memory.
func (uh *uploadHeap) managedReset() error {
	uh.mu.Lock()
	defer uh.mu.Unlock()
	uh.unstuckHeapChunks = make(map[uploadChunkID]*unfinishedUploadChunk)
	uh.stuckHeapChunks = make(map[uploadChunkID]*unfinishedUploadChunk)
	return uh.heap.reset()
}

// managedResume will close the pauseChan and stop the pauseTimer
func (uh *uploadHeap) managedResume() {
	uh.mu.Lock()
	defer uh.mu.Unlock()
	select {
	case <-uh.pauseChan:
		// uploadHeap isn't paused, nothing to do
		return
	default:
	}

	// Stop the timer and reset the duration
	stopped := uh.pauseTimer.Stop()
	uh.pauseDuration = 0
	uh.pauseStart = time.Time{}

	// We only want to close the channel if we were able to stop the timer,
	// otherwise the channel is already closed because the timer reached its
	// duration
	if stopped {
		close(uh.pauseChan)
	}
}

// managedTryUpdate will try and update the chunk in the uploadHeap associated
// with a chunk id. If a chunk exists in the uploadHeap and needs to be updated
// to the supplied chunk, the chunk that is currently in the heap will be
// canceled.
func (uh *uploadHeap) managedTryUpdate(uuc *unfinishedUploadChunk, ct chunkType) error {
	// Validate use of chunkType
	if (ct == chunkTypeStreamChunk) != (uuc.sourceReader != nil) {
		err := fmt.Errorf("Invalid chunkType use: streamChunk  %v, chunk has sourceReader reader %v",
			ct == chunkTypeStreamChunk, uuc.sourceReader != nil)
		build.Critical(err)
	}

	// If the new chunk doesn't have a sourceReader there is nothing to do
	if uuc.sourceReader == nil {
		return nil
	}

	// Check to see if the chunk is currently in the heap
	uh.mu.Lock()
	unstuckUUC, existsunstuckheap := uh.unstuckHeapChunks[uuc.id]
	stuckUUC, existsstuckheap := uh.stuckHeapChunks[uuc.id]
	exists := existsunstuckheap || existsstuckheap

	// If the chunk doesn't already exist there is nothing to update
	if !exists {
		uh.mu.Unlock()
		return nil
	}

	// get the existing chunk.
	var existingUUC *unfinishedUploadChunk
	if existsstuckheap {
		existingUUC = stuckUUC
	} else if existsunstuckheap {
		existingUUC = unstuckUUC
	}

	// If the existing chunk already has a sourceReader there is nothing to do
	if existingUUC.sourceReader != nil {
		uh.mu.Unlock()
		return nil
	}
	uh.mu.Unlock()

	// At this point we now know that there is an existing chunk in the uploadHeap
	// that has already been popped of the heap for repair that does not have
	// a sourceReader. Since we now have a chunk that does have a sourceReader we
	// want to cancel the repair of the existing chunk in order to prioritize
	// using the sourcereader to repair the chunk.
	existingUUC.Cancel()

	// Wait for all workers to finish ongoing work on the existing chunk.
	existingUUC.cancelWG.Wait()

	// Mark the repair as done.
	return nil
}

// PauseRepairsAndUploads pauses the renter's repairs and uploads for a time
// duration
func (r *Renter) PauseRepairsAndUploads(duration time.Duration) error {
	if err := r.tg.Add(); err != nil {
		return err
	}
	defer r.tg.Done()
	r.staticUploadHeap.managedPause(duration)
	return nil
}

// ResumeRepairsAndUploads resumes the renter's repairs and uploads
func (r *Renter) ResumeRepairsAndUploads() error {
	if err := r.tg.Add(); err != nil {
		return err
	}
	defer r.tg.Done()
	r.staticUploadHeap.managedResume()
	return nil
}

// managedBuildUnfinishedChunk will pull out a single unfinished chunk of a file.
func (r *Renter) managedBuildUnfinishedChunk(ctx context.Context, entry *filesystem.FileNode, chunkIndex uint64, hosts map[string]struct{}, priority bool, offline, goodForRenew map[string]bool, mm *memoryManager) (_ *unfinishedUploadChunk, _ bool, err error) {
	cid := uploadChunkID{entry.UID(), chunkIndex}

	// Check if the chunk is already being repaired before building it.
	r.repairingChunksMu.Lock()
	defer r.repairingChunksMu.Unlock()
	if uc, repairing := r.repairingChunks[cid]; repairing {
		return uc, true, nil // already being repaired
	}

	// Copy entry
	entryCopy := entry.Copy()
	stuck, err := entry.StuckChunkByIndex(chunkIndex)
	if err != nil {
		r.staticLog.Println("WARN: unable to get 'stuck' status:", err)
		return nil, false, errors.AddContext(err, "unable to get 'stuck' status")
	}
	_, err = os.Stat(entryCopy.LocalPath())
	onDisk := err == nil

	// Create a child trace for this unfinishedUploadChunk.
	var span opentracing.Span
	if parent := opentracing.SpanFromContext(ctx); parent != nil {
		spanRef := opentracing.ChildOf(parent.Context())
		span = opentracing.StartSpan("unfinishedUploadChunk", spanRef)
	}

	uuc := &unfinishedUploadChunk{
		ctx:          ctx,
		fileEntry:    entryCopy,
		staticRenter: r,

		id: uploadChunkID{
			fileUID: entry.UID(),
			index:   chunkIndex,
		},

		length:         entry.ChunkSize(),
		offset:         int64(chunkIndex * entry.ChunkSize()),
		onDisk:         onDisk,
		staticPriority: priority,

		staticIndex:   chunkIndex,
		staticSiaPath: entryCopy.SiaFilePath(),

		staticMemoryManager: mm,

		// staticMemoryNeeded has to also include the logical data, and also
		// include the overhead for encryption.
		//
		// TODO: Currently we request memory for all of the pieces as well
		// as the minimum pieces, but we perhaps don't need to request all
		// of that.
		staticMemoryNeeded:  entry.PieceSize()*uint64(entry.ErasureCode().NumPieces()+entry.ErasureCode().MinPieces()) + uint64(entry.ErasureCode().NumPieces())*entry.MasterKey().Type().Overhead(),
		staticMinimumPieces: entry.ErasureCode().MinPieces(),
		staticPiecesNeeded:  entry.ErasureCode().NumPieces(),
		stuck:               stuck,

		physicalChunkData:        make([][]byte, entry.ErasureCode().NumPieces()),
		staticExpectedPieceRoots: make([]crypto.Hash, entry.ErasureCode().NumPieces()),

		staticAvailableChan:       make(chan struct{}),
		staticUploadCompletedChan: make(chan struct{}),

		pieceUsage:  make([]bool, entry.ErasureCode().NumPieces()),
		unusedHosts: make(map[string]struct{}, len(hosts)),

		staticSpan: span,
	}

	// Every chunk can have a different set of unused hosts.
	for host := range hosts {
		uuc.unusedHosts[host] = struct{}{}
	}

	// Iterate through the pieces of all chunks of the file and mark which
	// hosts are already in use for a particular chunk. As you delete hosts
	// from the 'unusedHosts' map, also increment the 'piecesCompleted' value.
	pieces, err := entry.Pieces(chunkIndex)
	if err != nil {
		r.staticLog.Println("failed to get pieces for building incomplete chunks", err)
		if entry.Finished() {
			if err := entry.SetStuck(chunkIndex, true); err != nil {
				r.staticLog.Printf("failed to set chunk %v stuck: %v", chunkIndex, err)
			}
		}
		return nil, false, errors.AddContext(err, "error trying to get the pieces for the chunk")
	}
	for pieceIndex, pieceSet := range pieces {
		for _, piece := range pieceSet {
			// Determine whether this piece counts towards the redundancy.
			// Several criteria must be met:
			//
			// + The host much be online and marked as GoodForRenew
			// + A different piece with the same index must not have been
			//   counted already.
			// + The host must not be holding any other piece which was already
			//   counted (this shouldn't happen under the current code, but
			//   previous and possibly future bugs have allowed hosts to
			//   sometimes wind up holding multiple piece of the same chunk)
			hpk := piece.HostPubKey.String()
			goodForRenew, exists := goodForRenew[hpk]
			offline, exists2 := offline[hpk]
			redundantPiece := uuc.pieceUsage[pieceIndex]
			_, exists3 := uuc.unusedHosts[hpk]
			if exists && goodForRenew && exists2 && !offline && exists3 && !redundantPiece {
				uuc.pieceUsage[pieceIndex] = true
				uuc.piecesCompleted++
			} else if redundantPiece && build.Release == "testing" {
				// This shouldn't happen in testing unless
				// explicitly tested for.
				build.Critical("same piece was uploaded to multiple hosts")
			}
			// In all cases, if this host already has a piece, the host cannot
			// appear in the set of unused hosts.
			delete(uuc.unusedHosts, hpk)
		}
		// If there are already pieces uploaded for that set, we remember the
		// roots of the uploaded pieces in order to be able to later perform an
		// integrity check while repairing if the repair pulls information from
		// a local (and therefore potentially altered or corrupt) file.
		if len(pieceSet) > 0 {
			uuc.staticExpectedPieceRoots[pieceIndex] = pieceSet[0].MerkleRoot
		}
	}
	// Now that we have calculated the completed pieces for the chunk we can
	// calculate the health of the chunk to avoid a call to ChunkHealth
	uuc.health = siafile.CalculateHealth(uuc.piecesCompleted, uuc.staticMinimumPieces, uuc.staticPiecesNeeded)
	// Add the chunk to the repairing chunks.
	r.repairingChunks[cid] = uuc
	return uuc, false, nil
}

// managedBuildUnfinishedChunks will pull all of the unfinished chunks out of a
// file.
//
// NOTE: each unfinishedUploadChunk needs its own SiaFileSetEntry. This is due
// to the SiaFiles being removed from memory. Since the renter does not keep the
// SiaFiles in memory the unfinishedUploadChunks need to close the SiaFile when
// they are done and so cannot share a SiaFileSetEntry as the first chunk to
// finish would then close the Entry and consequentially impact the remaining
// chunks.
//
// NOTE: The caller is responsible for ensuring that the file should be repair,
// therefore we do not check if a file is Unfinished in this method or lower
// down. This is to avoid blocking or failing uploads.
func (r *Renter) managedBuildUnfinishedChunks(entry *filesystem.FileNode, hosts map[string]struct{}, target repairTarget, offline, goodForRenew map[string]bool, mm *memoryManager) []*unfinishedUploadChunk {
	// If we don't have enough workers for the file, don't repair it right now.
	minPieces := entry.ErasureCode().MinPieces()
	workerPoolLen := r.staticWorkerPool.callNumWorkers()
	if workerPoolLen < minPieces {
		// There are not enough workers for the chunk to reach minimum
		// redundancy. Check if the allowance has enough hosts for the chunk to
		// reach minimum redundancy
		r.staticLog.Debugf("Not building any chunks from file: %v: num workers %v, min pieces %v", skymodules.ErrNotEnoughWorkersInWorkerPool, workerPoolLen, minPieces)
		allowance := r.staticHostContractor.Allowance()
		// Only perform this check when we are looking for unstuck chunks. This
		// will prevent log spam from repeatedly logging to the user the issue
		// with the file after marking the chunks as stuck
		if allowance.Hosts < uint64(minPieces) && target == targetUnstuckChunks && entry.Finished() {
			// There are not enough hosts in the allowance for the file to reach
			// minimum redundancy. Mark all chunks as stuck.
			r.staticLog.Printf("WARN: allownace had insufficient hosts for chunk to reach minimum redundancy, have %v need %v for file %v", allowance.Hosts, minPieces, entry.SiaFilePath())
			if err := entry.SetAllStuck(true); err != nil {
				r.staticLog.Println("WARN: unable to mark all chunks as stuck:", err)
			}
		}
		return nil
	}

	// Assemble chunk indexes, stuck Loop should only be adding stuck chunks and
	// the repair loop should only be adding unstuck chunks
	var chunkIndexes []uint64
	for i := uint64(0); i < entry.NumChunks(); i++ {
		stuck, err := entry.StuckChunkByIndex(i)
		if err != nil {
			r.staticLog.Debugln("failed to get 'stuck' status of entry:", err)
			continue
		}
		if (target == targetStuckChunks) == stuck {
			chunkIndexes = append(chunkIndexes, i)
		}
	}

	// Sanity check that we have chunk indices to go through
	if len(chunkIndexes) == 0 {
		r.staticLog.Println("WARN: no chunk indices gathered, can't add chunks to heap")
		return nil
	}

	// Build a map of host public keys. We assume that all entrys are the same.
	pks := make(map[string]types.SiaPublicKey)
	for _, pk := range entry.HostPublicKeys() {
		pks[string(pk.Key)] = pk
	}

	// Assemble the set of chunks.
	newUnfinishedChunks := make([]*unfinishedUploadChunk, 0, len(chunkIndexes))
	for _, index := range chunkIndexes {
		// Sanity check: fileUID should not be the empty value.
		if entry.UID() == "" {
			build.Critical("empty string for file UID")
		}

		// Create unfinishedUploadChunk
		chunk, exists, err := r.managedBuildUnfinishedChunk(r.tg.StopCtx(), entry, uint64(index), hosts, memoryPriorityLow, offline, goodForRenew, mm)
		if err != nil {
			r.staticLog.Debugln("Error when building an unfinished chunk:", err)
			continue
		}
		// Chunk might already being repaired.
		if exists {
			continue
		}
		newUnfinishedChunks = append(newUnfinishedChunks, chunk)
	}

	// Prune the incomplete chunks
	return r.managedPruneIncompleteChunks(newUnfinishedChunks)
}

// managedPruneIncompleteChunks will remove any completed chunks from the list
// of unfinishedUploadChunks
func (r *Renter) managedPruneIncompleteChunks(newUnfinishedChunks []*unfinishedUploadChunk) []*unfinishedUploadChunk {
	// Iterate through the set of newUnfinishedChunks and remove any that are
	// completed.
	incompleteChunks := newUnfinishedChunks[:0]
	for _, chunk := range newUnfinishedChunks {
		// Check if the chunk needs repair
		needsRepair := skymodules.NeedsRepair(chunk.health)

		// Add chunk to list of incompleteChunks if it is in need of repair.
		if needsRepair {
			incompleteChunks = append(incompleteChunks, chunk)
			continue
		}

		// Close entry of completed chunk. Since the chunk doesn't need
		// repair we should make sure that it is not marked as stuck.
		chunk.stuck = false
		err := chunk.managedSetStuckAndClose(true)
		if err != nil {
			r.staticLog.Debugln("WARN: unable to set chunk stuck status and close:", err)
		}
	}
	return incompleteChunks
}

// managedBlockUntilSynced will block until the contractor is synced with the
// peer-to-peer network.
func (r *Renter) managedBlockUntilSynced() bool {
	for {
		synced := r.staticConsensusSet.Synced()
		if synced {
			return true
		}

		select {
		case <-r.tg.StopChan():
			return false
		case <-time.After(syncCheckInterval):
			continue
		case <-r.staticHostContractor.Synced():
			return true
		}
	}
}

// managedAddChunksToHeap will add chunks to the upload heap one directory at a
// time until the directory heap is empty or the uploadheap is full. It does
// this by popping directories off the directory heap and adding the chunks from
// that directory to the upload heap. If the worst health directory found is
// sufficiently healthy then we return.
func (r *Renter) managedAddChunksToHeap(hosts map[string]struct{}) error {
	prevHeapLen := r.staticUploadHeap.managedLen()
	// Loop until the upload heap has maxUploadHeapChunks in it or the directory
	// heap is empty
	offline, goodForRenew, _, _ := r.callRenterContractsAndUtilities()
	consecutiveDirHeapFailures := 0
	for r.staticUploadHeap.managedLen() < maxUploadHeapChunks && r.staticDirectoryHeap.managedLen() > 0 {
		select {
		case <-r.tg.StopChan():
			return errors.New("renter shutdown before we could finish adding chunks to heap")
		default:
		}

		// Pop an explored directory off of the directory heap
		dir, err := r.managedNextExploredDirectory()
		if errors.Contains(err, threadgroup.ErrStopped) {
			// Check to see if the error is due to a shutdown. If so then avoid the
			// log Severe.
			return errors.New("renter shutdown before we could finish adding chunks to heap")
		} else if err != nil {
			r.staticRepairLog.Severe("error fetching directory for repair:", err)
			// Log the error and then decide whether or not to continue of to return
			consecutiveDirHeapFailures++
			if consecutiveDirHeapFailures > maxConsecutiveDirHeapFailures {
				r.staticDirectoryHeap.managedReset()
				return errors.AddContext(err, "too many consecutive dir heap failures")
			}
			continue
		}
		consecutiveDirHeapFailures = 0

		// Sanity Check if directory was returned
		if dir == nil {
			r.staticRepairLog.Debugln("no more chunks added to the upload heap because there are no more directories")
			return nil
		}

		// If the directory that was just popped does not need to be repaired then
		// return
		heapHealth, _ := dir.managedHeapHealth()
		if !skymodules.NeedsRepair(heapHealth) {
			r.staticRepairLog.Debugln("no more chunks added to the upload heap because directory popped is healthy")
			return nil
		}

		// The dir needs repairing. So we queue an update. Even if we
		// don't end up adding chunks, we want to update the health
		// because there is a reason we ended up with that dir.
		r.staticDirUpdateBatcher.callQueueDirUpdate(dir.staticSiaPath)

		// Add chunks from the directory to the uploadHeap.
		r.managedBuildChunkHeap(dir.staticSiaPath, hosts, targetUnstuckChunks, offline, goodForRenew)

		// Check to see if we are still adding chunks
		heapLen := r.staticUploadHeap.managedLen()
		if heapLen == prevHeapLen {
			// If no chunks were added from this directory then just continue as
			// this could be due to a slight delay in the metadata being updated
			continue
		}
		chunksAdded := heapLen - prevHeapLen
		prevHeapLen = heapLen

		// Since we added chunks from this directory, track the siaPath
		r.staticRepairLog.Printf("Added %v chunks from %s to the repair heap", chunksAdded, dir.staticSiaPath) // TODO: Tag log
	}

	return nil
}

// managedBuildAndPushRandomChunk randomly selects a stuck chunk from a file and
// adds it to the upload heap
func (r *Renter) managedBuildAndPushRandomChunk(siaPath skymodules.SiaPath, hosts map[string]struct{}, target repairTarget, mm *memoryManager) error {
	// Open file
	file, err := r.staticFileSystem.OpenSiaFile(siaPath)
	if err != nil {
		return err
	}

	// Build offline and goodForRenew maps
	offline, goodForRenew, _, _ := r.callRenterContractsAndUtilities()

	// Build the unfinished stuck chunks from the file
	unfinishedUploadChunks := r.managedBuildUnfinishedChunks(file, hosts, target, offline, goodForRenew, mm)

	// Sanity check that there are stuck chunks
	if len(unfinishedUploadChunks) == 0 {
		return fmt.Errorf("No stuck chunks built from %v", siaPath)
	}

	// Grab a random stuck chunk and set its stuckRepair field to true
	randChunkIndex := fastrand.Intn(len(unfinishedUploadChunks))
	randChunk := unfinishedUploadChunks[randChunkIndex]
	randChunk.stuckRepair = true
	unfinishedUploadChunks = append(unfinishedUploadChunks[:randChunkIndex], unfinishedUploadChunks[randChunkIndex+1:]...)
	var allErrs error
	defer func() {
		// Close the unused unfinishedUploadChunks
		for _, chunk := range unfinishedUploadChunks {
			allErrs = errors.Compose(allErrs, chunk.Close())
		}
	}()

	// Push chunk onto the uploadHeap
	_, pushed, err := r.managedPushChunkForRepair(randChunk, chunkTypeLocalChunk)
	if err != nil {
		return errors.Compose(allErrs, err, randChunk.Close())
	}
	if !pushed {
		// Chunk wasn't added to the heap. Close the file
		r.staticLog.Debugln("WARN: stuck chunk", randChunk.id, "wasn't added to heap")
		allErrs = errors.Compose(allErrs, randChunk.Close())
	}
	return allErrs
}

// callBuildAndPushChunks builds the unfinished upload chunks and adds them to
// the upload heap
//
// NOTE: the files submitted to this function should all be from the same
// directory
func (r *Renter) callBuildAndPushChunks(files []*filesystem.FileNode, hosts map[string]struct{}, target repairTarget, offline, goodForRenew map[string]bool) {
	// Sanity check that at least one file was provided
	if len(files) == 0 {
		build.Critical("callBuildAndPushChunks called without providing any files")
		return
	}

	// Loop through the set of files, building a temporary heap of chunks that
	// need repairs. A temporary heap is used because we do not know in advance
	// how bad the health of the worst chunks are, and we don't want to add any
	// chunks to the full chunk heap unless we are sure they are the worst
	// chunks.
	//
	// The temporary heap uses a staging technique where it stores up to twice
	// the total number of chunks that it needs, and then it prunes itself by
	// popping out the worst chunks, deleting the less bad chunks, and then
	// re-adding the worst chunks again.
	//
	// When determining whether or not to skip a chunk in this directory, we
	// consider the worst health of any chunk in the next directory, as well as
	// the worst health of any chunk we have skipped so far.
	//
	// To prevent an infinite loop, we need to track the worst health of
	// currently skipped chunks and the worst health of the next directory
	// separately, so that if we skip only chunks that have better health than
	// the next directory, when we re-add this directory to the directory heap,
	// it gets added behind the next directory, ensuring progress is made.
	var tempChunkHeap uploadChunkHeap
	nextDirHealth, nextDirRemote := r.staticDirectoryHeap.managedPeekHealth()
	wh := worstIgnoredHealth{
		nextDirHealth: nextDirHealth,
		nextDirRemote: nextDirRemote,

		target: target,
	}
	// Loop through all the files and build the temporary heap.
	for _, file := range files {
		// Skip any unfinished files
		if !file.Finished() {
			continue
		}

		// If this file has better health than other files that we have ignored,
		// this file can be skipped. This only counts for unstuck chunks, if we
		// are adding stuck files, we ignore health as a consideration.
		fileMetadata := file.Metadata()
		fileHealth := fileMetadata.CachedHealth
		_, err := os.Stat(fileMetadata.LocalPath)
		remoteFile := fileMetadata.LocalPath == "" || err != nil
		if wh.canSkip(fileHealth, remoteFile) {
			wh.updateWorstIgnoredHealth(fileHealth, remoteFile)
			continue
		}

		// Build unfinished chunks from file and add them to the temp heap.
		unfinishedUploadChunks := r.managedBuildUnfinishedChunks(file, hosts, target, offline, goodForRenew, r.staticRepairMemoryManager)
		for i := 0; i < len(unfinishedUploadChunks); i++ {
			chunk := unfinishedUploadChunks[i]
			// Skip adding this chunk if it is already in the upload heap.
			if r.staticUploadHeap.managedExists(chunk.id) {
				// Close the file entry before skipping the chunk.
				err := chunk.Close()
				if err != nil {
					r.staticLog.Println("Error closing file entry:", err)
				}
				// The chunk is already in the heap, so it does not count as
				// being ignored even though technically we are skipping it. Do
				// not update the worst health vars based on this chunk.
				continue
			}
			if wh.canSkip(chunk.health, chunk.onDisk) {
				// Close the file entry before skipping the chunk.
				err := chunk.Close()
				if err != nil {
					r.staticLog.Println("Error closing file entry:", err)
				}

				wh.updateWorstIgnoredHealth(chunk.health, chunk.onDisk)
				continue
			}
			// Add chunk to temp heap
			heap.Push(&tempChunkHeap, chunk)

			// Check if temp heap is growing too large. We want to restrict it
			// to twice the size of the max upload heap size. This restriction
			// should be applied to all repairs to prevent excessive memory
			// usage.
			//
			// By restricting to twice the size of the normal upload heap, we
			// can guarantee that if this directory is 100% full of chunks that
			// have worse health than the current directory heap, we will still
			// keep all of them.
			if len(tempChunkHeap) < maxUploadHeapChunks*2 {
				continue
			}

			// Pruning has begun. Pruning happens by popping the worst chunks
			// off of the heap (enough to fully fill the upload heap), and then
			// resetting the heap, and then pushing all of the worst chunks back
			// onto the heap. Effectively this clears the heap from having
			// chunks that will never be put into the full heap because the
			// health is too poor.
			var chunksToKeep []*unfinishedUploadChunk
			for len(tempChunkHeap) > maxUploadHeapChunks {
				chunksToKeep = append(chunksToKeep, heap.Pop(&tempChunkHeap).(*unfinishedUploadChunk))
			}

			// Grab the health of the worst chunk that we are going to ignore.
			// Then update the worstIgnoredHealth to reflect this ignored chunk.
			chunk = heap.Pop(&tempChunkHeap).(*unfinishedUploadChunk)
			// Close the file entry, since this chunk is popped, the reset of
			// the heap won't catch this chunk.
			err := chunk.Close()
			if err != nil {
				r.staticLog.Println("Error closing file entry:", err)
			}
			wh.updateWorstIgnoredHealth(chunk.health, chunk.onDisk)

			// Reset the temp heap to throw out all of the chunks that we don't
			// care about.
			err = tempChunkHeap.reset()
			if err != nil {
				r.staticLog.Println("WARN: error resetting the temporary upload heap:", err)
			}
			// Add all of the bad chunks we saved from earlier back into the
			// temp heap.
			for _, chunk := range chunksToKeep {
				heap.Push(&tempChunkHeap, chunk)
			}
			// Clean up the chunksToKeep memory, this improves garbage
			// collection.
			chunksToKeep = []*unfinishedUploadChunk{}
		}
	}

	// We now have a temporary heap of the worst chunks in the directory. Move
	// the chunks from the temporary heap to the upload heap until either there
	// are no more temporary chunks or until the upload heap is full.
	for len(tempChunkHeap) > 0 && (r.staticUploadHeap.managedLen() < maxUploadHeapChunks || target == targetBackupChunks) {
		// Add this chunk to the upload heap.
		chunk := heap.Pop(&tempChunkHeap).(*unfinishedUploadChunk)
		_, pushed, err := r.managedPushChunkForRepair(chunk, chunkTypeLocalChunk)
		if err != nil {
			r.staticRepairLog.Println("WARN: Error pushing chunk for repair", err)
			err = chunk.Close()
			if err != nil {
				r.staticRepairLog.Println("Error closing file entry:", err)
			}
			return
		}
		if !pushed {
			// We don't track the health of this chunk since the only reason it
			// wouldn't be added to the heap is if it is already in the heap or
			// is currently being repaired. Close the file.
			err := chunk.Close()
			if err != nil {
				r.staticRepairLog.Println("Error closing file entry:", err)
			}
		}
	}

	// If there are any chunks left in the temporary heap, these chunks are
	// going to be ignored. Set the worst ignored values based on the worst of
	// the chunks being ignored here.
	if len(tempChunkHeap) > 0 {
		chunk := heap.Pop(&tempChunkHeap).(*unfinishedUploadChunk)
		// Close the file entry since it's no longer in the temp heap and
		// therefore will not be caught by the call to reset().
		err := chunk.Close()
		if err != nil {
			r.staticLog.Println("Error closing file entry:", err)
		}
		wh.updateWorstIgnoredHealth(chunk.health, chunk.onDisk)
	}
	// We are done with the temporary heap, reset it so the resources are closed
	// and the memory is released.
	err := tempChunkHeap.reset()
	if err != nil {
		r.staticLog.Println("WARN: error resetting the temporary upload heap:", err)
	}

	// Check if we were adding backup chunks, if so return here as backups are
	// not added to the directory heap
	if target == targetBackupChunks {
		return
	}
	// If the worst ignored health is below the repair threshold, ie does not need
	// to be repaired, there is no need to re-add the directory to the directory
	// heap.
	if !skymodules.NeedsRepair(wh.health) {
		return
	}

	// There are chunks in this directory which need to be repaired, but got
	// excluded from the upload heap. This directory should be added back into
	// the directory heap with a health that matches the worst health of any
	// chunk that got ignored.
	//
	// This means that the directory may be added with a health that doesn't
	// match its actual health, this is okay because the goal is to make sure
	// that the upload heap is making progress.

	// All files submitted are from the same directory so use the first one to
	// get the directory siapath
	dirSiaPath, err := r.staticFileSystem.FileSiaPath(files[0]).Dir()
	if err != nil {
		r.staticLog.Println("WARN: unable to get directory SiaPath and add directory back to directory heap:", err)
		return
	}

	// The directory will be added back as 'explored', under the assumption that
	// only explored directories are having their chunks added to the upload
	// heap.
	//
	// The health of the directory is set equal to the worst health of any chunk
	// that got ignored. This is because that is what the health of the
	// directory will be after all the repairs we just queued are completed.
	//
	// The aggregate health needs to be set as well, because the directory may
	// already exist on the directory heap in an unexplored state, as it may
	// have been added by another thread. When the directory is added under that
	// race condition, the worst healths of all the directories will be used. We
	// want to ensure that we don't shadow worse healths in subdirs.
	d := &directory{
		aggregateHealth: wh.health,
		explored:        true,
		health:          wh.health,
		staticSiaPath:   dirSiaPath,
	}
	// The remote health values should only be set if the worst health of any
	// ignored chunk was a remote health chunk.
	if wh.remote {
		d.aggregateRemoteHealth = wh.health
		d.remoteHealth = wh.health
	}
	// Push the directory back onto the directory heap so that when the current
	// upload heap is drained, the ignored chunks in this dir will be
	// reconsidered.
	r.staticDirectoryHeap.managedPush(d)
}

// managedBuildChunkHeap will iterate through all of the files in the renter and
// construct a chunk heap.
//
// TODO: accept an input to indicate how much room is in the heap
//
// TODO: Explore whether there is a way to perform the task below without
// opening a full file entry for each file in the directory.
func (r *Renter) managedBuildChunkHeap(dirSiaPath skymodules.SiaPath, hosts map[string]struct{}, target repairTarget, offline, goodForRenew map[string]bool) {
	// Get Directory files
	fileinfos, err := r.staticFileSystem.ReadDir(dirSiaPath)
	if err != nil {
		r.staticLog.Println("WARN: could not read directory:", err)
		return
	}
	// Build files from fileinfos
	var files []*filesystem.FileNode
	for _, fi := range fileinfos {
		// skip sub directories and non siafiles
		ext := filepath.Ext(fi.Name())
		if fi.IsDir() || ext != skymodules.SiaFileExtension {
			continue
		}

		// Open SiaFile
		siaPath, err := dirSiaPath.Join(strings.TrimSuffix(fi.Name(), ext))
		if err != nil {
			r.staticLog.Println("WARN: could not create siaPath:", err)
			continue
		}
		file, err := r.staticFileSystem.OpenSiaFile(siaPath)
		if err != nil {
			r.staticLog.Println("WARN: could not open siafile:", err)
			continue
		}

		// For stuck chunk repairs, check to see if file has stuck chunks
		if target == targetStuckChunks && file.NumStuckChunks() == 0 {
			// Close unneeded files
			err = file.Close()
			if err != nil {
				r.staticLog.Println("WARN: Could not close file:", file.SiaFilePath(), err)
			}
			continue
		}
		// For normal repairs, ignore files that don't have any unstuck chunks,
		// are healthy and not in need of repair.
		//
		// We can used the cached value of health because it is updated during
		// bubble. Since the repair loop operates off of the metadata
		// information updated by bubble this cached health is accurate enough
		// to use in order to determine if a file has any chunks that need
		// repair
		ignore := file.NumChunks() == file.NumStuckChunks() || !skymodules.NeedsRepair(file.Metadata().CachedHealth)
		if target == targetUnstuckChunks && ignore {
			err = file.Close()
			if err != nil {
				r.staticLog.Println("WARN: Could not close file:", file.SiaFilePath(), err)
			}
			continue
		}

		files = append(files, file)
	}

	// Check if any files were selected from directory
	if len(files) == 0 {
		r.staticLog.Debugln("No files pulled from `", dirSiaPath, "` to build the repair heap")
		return
	}

	// If there are more files than there is room in the heap, sort the files by
	// health and only use the required number of files to build the heap. In
	// the absolute worst case, each file will be only contributing one chunk to
	// the heap, so this shortcut will not be missing any important chunks. This
	// shortcut will also not be used for directories that have fewer than
	// 'maxUploadHeapChunks' files in them, minimzing the impact of this code in
	// the typical case.
	//
	// This check only applies to normal repairs. Stuck repairs have their own
	// way of managing the number of chunks added to the heap and backup chunks
	// should always be added.
	//
	// v1.4.1 Benchmark: on a computer with an SSD, the time to sort 6,000 files
	// is less than 50 milliseconds, while the time to process 250 files with 40
	// chunks each using 'callBuildAndPushChunks' is several seconds. Even in
	// the worst case, where we are sorting 251 files with 1 chunk each, there
	// is not much slowdown compared to skipping the sort, because the sort is
	// so fast.
	if len(files) > maxUploadHeapChunks && target == targetUnstuckChunks {
		// Sort so that the highest health chunks will be first in the array.
		// Higher health values equal worse health for the file, and we want to
		// focus on the worst files.
		sort.Slice(files, func(i, j int) bool {
			return files[i].Metadata().CachedHealth > files[j].Metadata().CachedHealth
		})
		for i := maxUploadHeapChunks; i < len(files); i++ {
			err = files[i].Close()
			if err != nil {
				r.staticLog.Println("WARN: Could not close file:", files[i].SiaFilePath(), err)
			}
		}
		files = files[:maxUploadHeapChunks]
	}

	// Build the unfinished upload chunks and add them to the upload heap
	switch target {
	case targetBackupChunks:
		r.staticLog.Debugln("Attempting to add backup chunks to heap")
		r.callBuildAndPushChunks(files, hosts, target, offline, goodForRenew)
	case targetStuckChunks:
		r.staticLog.Println("stuck repair target used incorrectly")
	case targetUnstuckChunks:
		r.staticLog.Debugln("Attempting to add chunks to heap")
		r.callBuildAndPushChunks(files, hosts, target, offline, goodForRenew)
	default:
		r.staticLog.Println("WARN: repair target not recognized", target)
	}

	// Close all files
	for _, file := range files {
		err = file.Close()
		if err != nil {
			r.staticLog.Println("WARN: Could not close file:", file.SiaFilePath(), err)
		}
	}
}

// managedPushChunkForRepair pushes a chunk to the appropriate resource for
// repair based on the chunkType. For localChunks this means pushing the chunk
// onto the uploadHeap. For streamChunks this means adding the chunk directly to
// the uploadHeap's repair map and then sending the chunk directly to the
// workers.
//
// The boolean returned indicates whether or not the chunk was successfully
// pushed onto the uploadHeap.
func (r *Renter) managedPushChunkForRepair(uuc *unfinishedUploadChunk, ct chunkType) (*unfinishedUploadChunk, bool, error) {
	// Validate use of chunkType
	if (ct == chunkTypeStreamChunk) != (uuc.sourceReader != nil) {
		err := fmt.Errorf("Invalid chunkType use: streamChunk  %v, chunk has sourceReader reader %v",
			ct == chunkTypeStreamChunk, uuc.sourceReader != nil)
		build.Critical(err)
		return nil, false, err
	}

	// Try and update any existing chunk in the heap
	err := r.staticUploadHeap.managedTryUpdate(uuc, ct)
	if err != nil {
		return nil, false, errors.AddContext(err, "unable to update chunk in heap")
	}
	// Push the chunk onto the upload heap
	existingUUC, pushed := r.staticUploadHeap.managedPush(uuc, ct)
	// If we were not able to push the chunk, or if the chunkType is localChunk we
	// return
	if !pushed || ct == chunkTypeLocalChunk {
		return existingUUC, pushed, nil
	}

	// For streamChunks update the heap popped time
	uuc.mu.Lock()
	uuc.chunkPoppedFromHeapTime = time.Now()
	uuc.mu.Unlock()

	// Disrupt check
	if r.staticDeps.Disrupt("skipPrepareNextChunk") {
		return nil, pushed, nil
	}

	// Prepare and send the chunk to the workers.
	err = r.managedPrepareNextChunk(uuc)
	if err != nil {
		return uuc, pushed, errors.AddContext(err, "unable to prepare chunk for workers")
	}
	return nil, pushed, nil
}

// managedPrepareNextChunk takes the next chunk from the chunk heap and prepares
// it for upload. Preparation includes blocking until enough memory is
// available, fetching the logical data for the chunk (either from the disk or
// from the network), erasure coding the logical data into the physical data,
// and then finally passing the work onto the workers.
func (r *Renter) managedPrepareNextChunk(uuc *unfinishedUploadChunk) error {
	// Grab the next chunk, loop until we have enough memory, update the amount
	// of memory available, and then spin up a thread to asynchronously handle
	// the rest of the chunk tasks.
	if !uuc.staticMemoryManager.Request(uuc.ctx, uuc.staticMemoryNeeded, uuc.staticPriority) {
		return errors.New("couldn't request memory")
	}
	go r.threadedFetchAndRepairChunk(uuc)
	return nil
}

// managedRefreshHostsAndWorkers will reset the set of hosts and the set of
// workers for the renter.
//
// TODO: This function can be removed entirely if the worker pool is made to
// keep a list of hosts. Then instead of passing around the hosts as a parameter
// the cached value in the worker pool can be used instead. Using the cached
// value in the worker pool is more accurate anyway because the hosts field will
// match the set of workers that we have. Doing it the current way means there
// can be drift between the set of workers and the set of hosts we are using to
// build out the chunk heap.
func (r *Renter) managedRefreshHostsAndWorkers() map[string]struct{} {
	// Grab the current set of contracts and use them to build a list of hosts
	// that are currently active. The hosts are assembled into a map where the
	// key is the String() representation of the host's SiaPublicKey.
	//
	// TODO / NOTE: This code can be removed once files store the HostPubKey
	// of the hosts they are using, instead of just the FileContractID.
	currentContracts := r.staticHostContractor.Contracts()
	hosts := make(map[string]struct{})
	for _, contract := range currentContracts {
		hosts[contract.HostPublicKey.String()] = struct{}{}
	}
	// Refresh the worker pool as well.
	r.staticWorkerPool.callUpdate()
	return hosts
}

// managedRepairLoop works through the uploadheap repairing chunks. The repair
// loop will continue until the renter stops, there are no more chunks, or the
// number of chunks in the uploadheap has dropped below the minUploadHeapSize
func (r *Renter) managedRepairLoop() error {
	// smallRepair indicates whether or not the repair loop should process all
	// of the chunks in the heap instead of just processing down to the minimum
	// heap size. We want to process all of the chunks if the rest of the
	// directory heap is in good health, ie does not need to be repaired, and
	// there are no more chunks that could be added to the heap.
	dirHeapHealth, _ := r.staticDirectoryHeap.managedPeekHealth()
	smallRepair := !skymodules.NeedsRepair(dirHeapHealth)

	// Limit the amount of time spent in each iteration of the repair loop so
	// that changes to the directory heap take effect sooner rather than later.
	repairBreakTime := time.Now().Add(maxRepairLoopTime)

	// Work through the heap repairing chunks until heap is empty for
	// smallRepairs or heap drops below minUploadHeapSize for larger repairs, or
	// until the total amount of time spent in one repair iteration has elapsed.
	for r.staticUploadHeap.managedLen() >= minUploadHeapSize || smallRepair || time.Now().After(repairBreakTime) {
		select {
		case <-r.tg.StopChan():
			// Return if the renter has shut down.
			return errors.New("Repair loop interrupted because renter is shutting down")
		default:
		}

		// Return if the renter is not online.
		if !r.staticGateway.Online() {
			return errors.New("repair loop returned early due to the renter been offline")
		}

		// Check if the repair has been paused
		if r.staticUploadHeap.managedIsPaused() {
			// If paused we reset the upload heap and return so that when the
			// repair is resumes the upload heap can be built fresh.
			errPaused := errors.New("could not finish repairing upload heap because repair was paused")
			err := r.staticUploadHeap.managedReset()
			return errors.Compose(err, errPaused)
		}

		// Check if there is work by trying to pop off the next chunk from the
		// heap.
		nextChunk := r.staticUploadHeap.managedPop()
		if nextChunk == nil {
			// The heap is empty so reset it to free memory and return.
			r.staticUploadHeap.managedReset()
			return nil
		}
		chunkPath := nextChunk.staticSiaPath
		r.staticRepairLog.Printf("Repairing chunk %v of %s, currently have %v out of %v pieces", nextChunk.staticIndex, chunkPath, nextChunk.piecesCompleted, nextChunk.staticPiecesNeeded)

		// Make sure we have enough workers for this chunk to reach minimum
		// redundancy.
		availableWorkers := r.staticWorkerPool.callNumWorkers()
		if availableWorkers < nextChunk.staticMinimumPieces {
			r.staticRepairLog.Printf("WARN: Not enough workers to repair %s, have %v but need %v", chunkPath, availableWorkers, nextChunk.staticMinimumPieces)
			// If the chunk is not stuck, check whether there are enough hosts
			// in the allowance to support the chunk.
			if !nextChunk.stuck {
				// There are not enough available workers for the chunk to reach
				// minimum redundancy. Check if the allowance has enough hosts
				// for the chunk to reach minimum redundancy
				allowance := r.staticHostContractor.Allowance()
				if allowance.Hosts < uint64(nextChunk.staticMinimumPieces) {
					// There are not enough hosts in the allowance for this
					// chunk to reach minimum redundancy. Log an error, set the
					// chunk as stuck, and close the file
					r.staticRepairLog.Printf("Allowance has insufficient hosts for %s, have %v, need %v", chunkPath, allowance.Hosts, nextChunk.staticMinimumPieces)
					err := nextChunk.fileEntry.SetStuck(nextChunk.staticIndex, true)
					if err != nil {
						r.staticRepairLog.Printf("WARN: unable to mark chunk %v of %s as stuck: %v", nextChunk.staticIndex, chunkPath, err)
					}
				}
			}

			// There are enough hosts set in the allowance so this is a
			// temporary issue with available workers, just ignore the chunk
			// for now and close the file
			nextChunk.Close()
			continue
		}

		// Perform the work. managedPrepareNextChunk will block until
		// enough memory is available to perform the work, slowing this
		// thread down to using only the resources that are available.
		nextChunk.mu.Lock()
		nextChunk.chunkPoppedFromHeapTime = time.Now()
		nextChunk.repair = true
		nextChunk.mu.Unlock()
		err := r.managedPrepareNextChunk(nextChunk)
		if err != nil {
			// An error was return which means the renter was unable to allocate
			// memory for the repair. Since that is not an issue with the file
			// we will just close the chunk file entry instead of marking it as
			// stuck
			r.staticRepairLog.Printf("WARN: error while preparing chunk %v from %s: %v", nextChunk.staticIndex, chunkPath, err)
			nextChunk.Close()
			continue
		}
	}
	return nil
}

// threadedUploadAndRepair is a background thread that maintains a queue of
// chunks to repair. This thread attempts to prioritize repairing files and
// chunks with the lowest health, and attempts to keep heavy throughput
// sustained for data upload as long as there is at least one chunk in need of
// upload or repair.
func (r *Renter) threadedUploadAndRepair() {
	err := r.tg.Add()
	if err != nil {
		return
	}
	defer r.tg.Done()

	// Perpetual loop to scan for more files and add chunks to the uploadheap.
	// The loop assumes that the heap has already been initialized (either at
	// startup, or after sleeping) and does checks to see whether there is any
	// work required. If there is not any work required, the loop will sleep
	// until woken up. If there is work required, the loop will begin to process
	// the chunks and directories in the repair heaps.
	//
	// After 'repairLoopResetFrequency', the repair loop will be reset. This
	// adds a layer of robustness in case the repair loop gets stuck or can't
	// work through the full heap quickly because the user keeps uploading new
	// files and keeping a minimum number of chunks in the repair heap.
	resetTime := time.Now().Add(repairLoopResetFrequency)
	for {
		// Return if the renter has shut down.
		select {
		case <-r.tg.StopChan():
			return
		default:
		}

		// Wait until the contractor is synced.
		if !r.managedBlockUntilSynced() {
			// The renter shut down before the contract was synced.
			return
		}

		// Wait until the renter is online to proceed. This function will return
		// 'false' if the renter has shut down before being online.
		if !r.managedBlockUntilOnline() {
			return
		}

		// Check if repair process has been paused
		if r.staticUploadHeap.managedIsPaused() {
			r.staticRepairLog.Println("Repairs and Uploads have been paused")
			// Block until the repair process is restarted
			select {
			case <-r.tg.StopChan():
				return
			case <-r.staticUploadHeap.pauseChan:
				r.staticRepairLog.Println("Repairs and Uploads have been resumed")
			}
			// Reset the upload heap and the directory heap now that has been
			// resumed
			err := r.staticUploadHeap.managedReset()
			if err != nil {
				r.staticRepairLog.Println("WARN: there was an error resetting the upload heap:", err)
			}
			r.staticDirectoryHeap.managedReset()
			err = r.managedPushUnexploredDirectory(skymodules.RootSiaPath())
			if err != nil {
				r.staticRepairLog.Println("WARN: there was an error pushing an unexplored root directory onto the directory heap:", err)
			}
			if err != nil {
				select {
				case <-time.After(uploadAndRepairErrorSleepDuration):
				case <-r.tg.StopChan():
					return
				}
			}
			continue
		}

		// Refresh the worker set.
		hosts := r.managedRefreshHostsAndWorkers()

		// If enough time has elapsed to trigger a directory reset, reset the
		// directory.
		if time.Now().After(resetTime) {
			resetTime = time.Now().Add(repairLoopResetFrequency)
			r.staticDirectoryHeap.managedReset()
			err = r.managedPushUnexploredDirectory(skymodules.RootSiaPath())
			if err != nil {
				r.staticRepairLog.Println("WARN: error re-initializing the directory heap:", err)
			}
		}

		// Add any chunks from the backup heap that need to be repaired. This
		// needs to be handled separately because currently the filesystem for
		// storing system files and chunks such as those related to snapshot
		// backups is different from the siafileset that stores non-system files
		// and chunks.
		heapLen := r.staticUploadHeap.managedLen()
		offline, goodForRenew, _, _ := r.callRenterContractsAndUtilities()
		r.managedBuildChunkHeap(skymodules.BackupFolder, hosts, targetBackupChunks, offline, goodForRenew)
		numBackupChunks := r.staticUploadHeap.managedLen() - heapLen
		if numBackupChunks > 0 {
			r.staticRepairLog.Printf("Added %v backup chunks to the upload heap", numBackupChunks)
		}

		// Check if there is work to do. If the filesystem is healthy and the
		// heap is empty, there is no work to do and the thread should block
		// until there is work to do.
		dirHeapHealth, _ := r.staticDirectoryHeap.managedPeekHealth()
		if r.staticUploadHeap.managedLen() == 0 && !skymodules.NeedsRepair(dirHeapHealth) {
			// TODO: This has a tiny window where it might be dumping out chunks
			// that need health, if the upload call is appending to the
			// directory heap because there is a new upload.
			//
			// I believe that a good fix for this would be to change the upload
			// heap so that it performs a rapid bubble before trying to insert
			// the chunks into the heap. Then, even if a reset is triggered,
			// because a rapid bubble has already completed updating the health
			// of the root dir, it will be considered fairly.
			r.staticDirectoryHeap.managedReset()

			// If the file system is healthy then block until there is a new
			// upload or there is a repair that is needed.
			select {
			case <-r.staticUploadHeap.newUploads:
				r.staticRepairLog.Debugln("repair loop triggered by new upload channel")
			case <-r.staticUploadHeap.repairNeeded:
				r.staticRepairLog.Debugln("repair loop triggered by repair needed channel")
			case <-r.tg.StopChan():
				return
			}

			err = r.managedPushUnexploredDirectory(skymodules.RootSiaPath())
			if err != nil {
				// If there is an error initializing the directory heap log
				// the error. We don't want to sleep here as we were trigger
				// to repair chunks so we don't want to delay the repair if
				// there are chunks in the upload heap already.
				r.staticRepairLog.Println("WARN: error re-initializing the directory heap:", err)
			}

			// Continue here to force the code to re-check for backups, to
			// re-block until it's online, and to refresh the worker pool.
			continue
		}

		// Add chunks to heap.
		err := r.managedAddChunksToHeap(hosts)
		if err != nil {
			// Log the error but don't sleep as there are potentially chunks in
			// the heap from new uploads. If the heap is empty the next check
			// will catch that and handle it as an error
			r.staticRepairLog.Println("WARN: error adding chunks to the heap:", err)
		}

		// There are benign edge cases where the heap will be empty after chunks
		// have been added. For example, if a chunk has gotten more healthy
		// since the last health check due to one of its hosts coming back
		// online. In these cases, the best course of action is to proceed with
		// the repair and move on to the next directories in the directory heap.
		// The repair loop will return immediately if it is given little or no
		// work but it can see that there is more work that it could be given.

		uploadHeapLen := r.staticUploadHeap.managedLen()
		if uploadHeapLen > 0 {
			r.staticRepairLog.Printf("Executing an upload and repair cycle, uploadHeap has %v chunks in it", uploadHeapLen)
		}
		err = r.managedRepairLoop()
		if err != nil {
			// If there was an error with the repair loop sleep for a little bit
			// and then try again. Here we do not skip to the next iteration as
			// we want to call bubble on the impacted directories
			r.staticRepairLog.Println("WARN: there was an error in the repair loop:", err)
			select {
			case <-time.After(uploadAndRepairErrorSleepDuration):
			case <-r.tg.StopChan():
				return
			}
		}

		// Make sure all of the repairs we made are now represented in the
		// aggregate statistics of the filesystem.
		//
		// TODO: Get some stats/traces on this call, so we can understand how it
		// is limiting the repair heap.
		r.staticDirUpdateBatcher.callFlush()
	}
}
