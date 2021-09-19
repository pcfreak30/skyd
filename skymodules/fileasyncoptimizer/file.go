package fileasyncoptimizer

import (
	"encoding/binary"
	"sync/atomic"

	"gitlab.com/NebulousLabs/errors"
)

// TODO: Add a safety check that probabilistically verifies that the data model
// of the optimizer is being respected. When you call WriteAt on the optimizer,
// you are not allowed to subsequently modify the data that you passed in. We
// can verify this by making a copy of the original input and comparing the
// original input to the current state of the slice at write-time. This is only
// performed probabilistically because it otherwise would have runtime
// overheads, and ultimately defeat the point of the optimization.

// pendingSyncsNode is a node in a linked list that contains a locked mutex. The
// mutex should be unlocked by the background processor thread when a write has
// been persisted to disk.
type pendingSyncsNode struct {
	mu   sync.Mutex
	next *pendingSyncsNode
}

// pendingWritesNode is a node in a linked list that contains a pending write.
// The data is the data that is meant to be written, and the offset is the
// location to write the data.
//
// The next element in the list must not overlap with this element. If
// overlapping writes are added to the list, the overlap should be handled by
// having the later write overwrite the overlap, and then reslice the new array
// so that it only contains the non-overlapping data.
//
// We reslice the data to avoid needing to make more memory allocations. This
// does result in some extra overhead in the form of potentially unused slice
// data that hangs around, but skynet portals general have lots of RAM, so we'd
// rather have a little extra memory in use than a little extra GC pressure.
//
// Thread safety is handled on this object by its partent File.
type pendingWritesNode struct {
	data   []byte // data being written, length implied by len(data)
	offset int64  // offset for where to start the write

	// We don't need a prev because we always iterate through this list from the
	// beginning. We can figure out what prev is using context.
	next *pendingWritesNode
}

// gapsNode is a node in a linked list of gaps in a read request. The request is
// filled out one piece at a time by looking at pending writes and then finally
// doing reads from the underlying file. To keep things clean, gaps are indexed
// to the file, not to the read request. This creates congruence between the
// linked list of gaps and the linked linked list of pending writes.
type gapsNode struct {
	offset int64
	length int

	// We don't need a prev because we always iterate through this list from the
	// beginning. We can figure out what prev is using context.
	next *gapsNode
}

// File is a wrapper for os.File that interacts with the FileOptimizer. Writes
// to the file will be serialized and batched, with overlapping writes being
// optimized. A single background thread makes syscalls (mostly writes) on all
// of the files in the FileOptimizer, which means that calls to os.Write are
// significantly faster, and calls to Sync and Close are significantly slower.
//
// There is a small amount of performance impact for Reads, but we have tried to
// optimize that away as much as possible.
//
// The File follows the same consistency model as the os.File, which means that
// it can be safely used concurrently, but writes are not guaranteed to be
// persisted to disk until Sync() is called. If read and write are called
// concurrently on the same data within the file, corruption can occur - avoid
// calling File.Write on the same location of the file concurrently from
// multiple threads, and avoid callind File.Write on a location of the file that
// has a file.Write call which hasn't returned.
type File struct {
	// TODO: Need some sort of object pool for the pendingWritesNodes.

	// atomicClosed is an atomic variable that tracks the closed status of the
	// file. The file should not be closed while there are unreturned read or
	// write calls, and read/write cannot be called after close has been called.
	atomicClosed bool

	// The pending writes are broken up into two lists. Each list has its own
	// mutex so it can be accessed separately. Write calls will only interact
	// with the pendingWritesHead, and Read calls will first interact with the
	// pendingWritesHead, then the processingWritesHead.
	//
	// NOTE: The Read calls need to be certain that when switching from the
	// pendingWrites to the processingWrites, the counter is one lower. If when
	// switching to the processing writes, the counter is either the same or
	// larger, it means while the Read was blocking on the mutex, one or more
	// write cycles happened and the read call does not need to look at the
	// underlying data (and doing so could cause corruption).
	pendingSyncsHead     *pendingSyncsNode
	pendingSyncsTail     *pendingSyncsNode
	pendingWritesHead    *pendingWritesNode
	pendingWritesMu      sync.Mutex
	processingSyncsHead  *pendingSyncsNode
	processingSyncsTail  *pendingSyncsNode
	processingWritesHead *pendingWritesNode
	processingWritesMu   sync.Mutex

	// We don't need a prev because the thread that manages things like creating
	// new nodes and deleting them always accesses the list from the beginning.
	// It can use context to figure out what prev is.
	//
	// next is managed by a mutex that is active on the whole linked list of
	// files.
	next            *File
	staticOptimizer *FileOptimizer
	staticFile      *os.File
}

// ReadAt will read data from the file, taking into account any pending writes.
func (f *File) ReadAt(b []byte, readOffset int64) (_ int, err error) {
	// Input bounds checking.
	if readOffset < 0 {
		panic("misuse of ReadAt, offset must be greater than 0")
	}
	if len(b) == 0 {
		return 0, nil
	}
	// Check if the file is closed.
	if atomic.ReadUint64(&f.atomicClosed) != 0 {
		return 0, errors.New("cannot call ReadAt on closed file")
	}
	// Defer a function to catch a misuse of ReadAt, it is not safe to
	// concurrently call ReadAt and Close on a file.
	defer func() {
		if atomic.ReadUint64(&f.atomicClosed) != 0 {
			return 0, errors.Compose(err, "cannot call ReadAt on closed file")
		}
	}()

	// Create the first gap, which is the whole request.
	//
	// TODO: Switch to using object pool
	firstGap := &gapsNode{
		offset: readOffset,
		length: len(b),
	}

	// fillGaps is an anonymous function which will fill the gaps in 'b' based
	// on an input 'currentWrite' and 'currentGap'. We use an anonymous function
	// here because the logic is the exact same whether we are using the
	// pendingWrites or processingWrites list.
	fillGaps := func(currentWrite *pendingWritesNode) {
		currentGap := firstGap
		var prevGap *gapsNode

		// Each iteration of the loop, either we go to the next pendingWrite or we
		// go to the next gap. If there is overlap we'll fill the gap, which counts
		// as going to the next gap.
		for currentWrite != nil && currentGap != nil {
			// Check if 'currentWrite' is before the gap entirely. If so,
			// advance to the next pending write.
			if currentWrite.offset+int64(len(currentWrite.data)) < currentGap.offset {
				currentWrite = currentWrite.next
				continue
			}
			// Check if 'currentGap' is before the current pending write. If so,
			// advance to the next gap.
			if currentGap.offset+int64(currentGap.length) < currentWrite.offset {
				prevGap = currentGap
				currentGap = currentGap.next
				continue
			}
			// Check if the overlap crosses the front and back.
			crossesFront := currentWrite.offset <= currentGap.offset
			crossesBack := currentWrite.offset+int64(len(currentPendingSrite.data)) >= currentGap.offset+int64(currentGap.length)
			// If the overlap does not cross the front but does cross the back, we
			// need to change the current gap to point to the now-smaller gap at the
			// front.
			if crossesFront && !crossesBack {
				// Copy the data from the pending write into 'b'. We know that we
				// are copying the full length of the write minus the front bits, so
				// we only have to determine where in 'b' we start writing and where
				// in the pending write we start reading from.
				bStart := currentGap.offset - readOffset
				pendStart := currentGap.offset - currentWrite.offset
				n := copy(b[bStart:], currentWrite.data[pendStart:])

				// Shrink the current gap to represent that the start has moved
				// forward.
				currentGap.offset += n
				currentGap.length -= n
				continue
			}
			// If the overlap does not cross the back but does cross the front, we
			// need to change the current gap to point to the now-smaller gap at the
			// back.
			if !crossesFront && crossesBack {
				// Copy the data from the pending write into 'b'. We need to start
				// the write to 'b' based on where the currentWrite overlaps,
				// and we need to stop the write to 'b' at the end of the gap. We
				// can copy the entire pendingWrite because the bounds on 'b' will
				// keep it controlled.
				bStart := currentWrite.offset - readOffset
				bEnd := currentGap.offset + currentGap.length - readOffset
				n := copy(b[bStart:bEnd], currentWrite.data)

				// Shrink the current gap to represent that the end has moved
				// backwards.
				currentGap.length -= n
				continue
			}
			// If the overlap does not cross either the front or the back, we need
			// to create a new gap for the back and shrink the current gap at the
			// front.
			if !crossesFront && !crossesBack {
				// Copy the data from the pending write into 'b'. We know that we
				// are copying the full write and that the write is the bounding
				// point, so we just need to figure out where in b to begin.
				bStart := currentWrite.offset - readOffset
				n := copy(b[bStart:], currentWrite.data)

				// Create the new gap for the back.
				//
				// TODO: Swtich to using object pool.
				newGapLen := currentWrite.offset - currentGap.offset
				newGap := &gapsNode{
					offset: bStart + n,
					length: currentGap.length - n - newGapLen,
					next:   currentGap.next,
				}

				// Shrink the current gap.
				currentGap.next = newGap
				currentGap.length = newGapLen
				continue
			}
			// If the overlap crosses both the front and back, eliminate the current
			// gap.
			if crossesFront && crossesBack {
				// Copy the data from the pending write into 'b'. We're going to
				// have to get the bounds on the full gap for 'b', and we have ot
				// know where to start with the write copy. We don't need to know
				// the end because the b bounds will constrain the copy.
				bStart := currentGap.offset - readOffset
				bEnd := bStart + currentGap.length
				pendStart := currentGap.offset - currentWrite.offset
				copy(b[bStart:bEnd], currentWrite.data[pendStart:])

				// If there is a previous gap, have the previous gap point to the
				// next gap.
				if prevGap != nil {
					prevGap.next = currentGap.next
				}
				// If there is no previous gap, point the first gap to the next
				// gap.
				if prevGap == nil {
					firstGap = currengGap.next
				}
				continue
			}
		}
	}

	// Scan the pending writes for places where the writes can fill gaps.
	pendingWritesMu.Lock()
	fillGaps(pendingWritesHead)
	pendingWritesMu.Unlock()

	// Scan the processing writes for places where the writes can fill gaps.
	processingWritesMu.Lock()
	fillGaps(processingWritesHead)
	processingWritesMu.Unlock()

	// If the firstGap is nil, it means there are no gaps and all the data is
	// covered.
	if firstGap == nil {
		return b, nil
	}

	// Fill the remaining gaps by reading from the file. We will read into one
	// large []byte from our bufferzone, and then copy over as necessary.
	readStart := firstGap.offset
	current := firstGap
	for current.next != nil {
		current = current.next
	}
	readEnd := current.offset + int64(current.length)

	// Read the smallest segment of data possible from the file while only using
	// a single syscall.
	//
	// TODO: grab []byte from object pool
	r := make([]byte, readEnd-readStart)
	_, err := f.staticFile.ReadAt(r, firstGap.offset)
	if err != nil {
		return 0, errors.AddContext(err, "fileasyncoptmizer failed to read from the underlying file")
	}
	// Turn 'r' into a pendingWrite node so that we can use the fillGaps
	// algorithm to fill the gaps.
	//
	// TODO: use object pool
	readNode := &pendingWritesNode{
		data:   r,
		offset: firstGap.offset,
	}
	fillGaps(readNode)
	return b, nil
}

// WriteAt will add the write to a set of pending writes on the file, returning
// before they are written to disk. The consistency model here is the same as
// the consistency model for a normal WriteAt call, the only difference is
// higher latencies on Sync, and lower latencies to complete the WriteAt call.
func (f *File) WriteAt(b []byte, offset int64) (int, error) {
	// Input bounds checking.
	if offset < 0 {
		panic("misuse of ReadAt, offset must be greater than 0")
	}
	if len(b) == 0 {
		return 0, nil
	}
	// Check if the file is closed.
	if atomic.ReadUint64(&f.atomicClosed) != 0 {
		return 0, errors.New("cannot call ReadAt on closed file")
	}
	// Defer a function to catch a misuse of ReadAt, it is not safe to
	// concurrently call ReadAt and Close on a file.
	defer func() {
		if atomic.ReadUint64(&f.atomicClosed) != 0 {
			return 0, errors.Compose(err, "cannot call ReadAt on closed file")
		}
	}()
	// Save the original length for later, we will be modifying the length as we
	// fill out the set of pending writes.
	originalLen := len(b)

	// Schedule the write so that the file optimizer can apply backpressure if
	// too many writes are coming in at once.
	f.staticOptimizer.managedScheduleWrite()

	// Lock the list of pending writes to get this write added to the list.
	f.pendingWritesMu.Lock()
	defer f.pendingWritesMu.Unlock()

	// Add new pending writes to the linked list until all data is written.
	current := f.pendingWritesHead
	var prev *pendingWritesNode
	for len(b) > 0 {
		// If current is nil, we are at the end of the list. Append a new
		// element with all remaining data.
		if current == nil {
			// TODO: Switch to object pool
			newNode := &pendingWritesNode{
				data:   b,
				offset: offset,
			}
			if prev == nil {
				f.pendingWritesHead = newNode
			} else {
				prev.next = newNode
			}
			break
		}

		// If the current pending write is not caught up to the offset, advance
		// to the next pending write.
		if current != nil && current.offset+len(current.data) < offset {
			prev = current
			current = current.next
			continue
		}

		// If the current pending write is even with 'b', overwrite the data in
		// the current pending write with the data in 'b'.
		if current.offet == offset {
			n := copy(current.data, b)
			offset += n
			b = b[n:]
			current = current.next
			continue
		}

		// If the current pending write is after 'b', create a new node that has
		// data from 'b' up to the offset of current. This is the only option
		// remaining.
		writeSize := current.offset - offset
		if writeSize > len(b) {
			writeSize = len(b)
		}
		// TODO: Switch to object pool
		newNode := &pendingWritesNode{
			data:   b[:writeSize],
			offset: offset,
			next:   current,
		}
		b = b[writeSize:]
		offset += writeSize
		if prev == nil {
			f.pendingWritesHead = newNode
		} else {
			prev.next = newNode
		}
	}

	return originalLen, nil
}

// Sync will block until all of the pending writes have been written to disk and
// then the underlying file has synced.
func (f *File) Sync() error {
	f.pendingWritesMu.Lock()
	// TODO: Maybe object pool. Not really as needed here because Sync isn't
	// one of the latency sensitive calls.
	sn := new(pendingSyncsNode)
	sn.mu.Lock()
	f.pendingSyncsTail.next = sn
	f.pendingWritesMu.Unlock()

	// Block until the persist is complete.
	sn.mu.Lock()
	sn.mu.Unlock()
	// TODO: return the object to the pool.

	// Sync the underlying file and return.
	return f.staticFile.Sync()
}

// Close will close the file.
func (f *File) Close() error {
	// Set the file to closed.
	atomic.StoreUint64(&f.atomicClosed, 1)

	// Sync the file.
	err := f.Sync()
	if err != nil {
		return errors.AddContext(f.Sync(), "error from Sync while closing")
	}

	// TODO: Check that there are no more pending writes, and then return all
	// objects to the object pools so there aren't any memory leaks. The
	// background thread will take care of pulling the file out of the linked
	// list.

	return nil
}

// TODO: background loop

// TODO: object pools

// TODO: byte pool
