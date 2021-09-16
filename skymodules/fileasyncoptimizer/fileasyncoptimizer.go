// fileasyncoptimizer.go wraps the os.File object with another object that
// attempts to optimize the latency with which async operations return. It will
// also try to minimize the total number of syscalls that get made. These
// optimizations achieve higher total throughput and lower overall system load
// at the cost of potentially substantially slowing down synchronous operations
// like File.Sync() and File.Close().
//
// This is optimized code, which means that extra steps are taken at various
// points in the code to minimize overheads, potentially at the cost of
// readability. We take these steps more aggressivley than we do in most of the
// rest of the codebase.
//
// NOTE: A single optimizer can be used for many files across many packages, and
// will perform better at its job of reducing the total number of filesystem
// calls if more pieces of the codebase are all using the optimizer.
package fileasyncoptimizer

// TODO: Add a safety check that probabilistically verifies that the data model
// of the optimizer is being respected. When you call WriteAt on the optimizer,
// you are not allowed to subsequently modify the data that you passed in. We
// can verify this by making a copy of the original input and comparing the
// original input to the current state of the slice at write-time. This is only
// performed probabilistically because it otherwise would have runtime
// overheads, and ultimately defeat the point of the optimization.

import (
	"math"
	"os"
	"sync/atomic"
)

const (
	// MaxPendingWrites defines the maximum number of pending writes that the
	// FileOptimizer will accept. When the FileOptimzer starts to get close to the
	// limit, it will put artifical sleeps into the write calls to apply pressure on
	// the callers and reduce the number of incoming writes. It will start to log
	// when this happens so that sysadmins can see that there are a lot of calls
	// taking place.
	//
	// NOTE: There is only a single background thread processing syscalls. We use
	// only one thread to minimize the total syscall throughput and keep the overall
	// system load low. This thread is allowed to run at full speed however.
	//
	// NOTE: In the worst case, this array adds linear overhead to each call. When
	// the list of pending writes is processed, each write potentially has to
	// iterate over the full size of the array twice before the write can be
	// committed.
	//
	// NOTE: If changing this value, FullQueueSleepExponential also needs to be
	// changed.
	MaxPendingWrites = 10e3

	// SleepPerWriteIteration defines the minimum amount of time that the background
	// thread is going to sleep before performing work. This ensures that there is
	// time for multiple writes to the same file to be batched together.
	SleepPerWriteIteration = time.Millisecond * 50

	// FullQueueSleepExponential defines the slope of the exponential curve that
	// establishes how long a thread must wait before adding a new pendingWrite
	// to the FileOptimizer.
	//
	// When MaxPendingWrites is 10e3 and FullQueueSleepExponential is 1.006,
	// there will be no sleeping at all until about 7700 writes have been put in
	// the queue. By the time 8000 writes are in the queue, the sleeps will be
	// 80 milliseconds each. By the time 9000 writes are in the queue, the
	// sleeps will be 24 seconds each. And at 9900 writes, the sleeps will be
	// over an hour each, indicating serious issues.
	//
	// NOTE: This constant directly interacts with MaxPendingWrites, make sure
	// the change them in tandem.
	FullQueueSleepExponential = 1.006
)

// pendingWrite contains a byte slice and an offset. The length is implied by
// the length of the byte slice. Since the byte slice is just a pointer, this
// entire object is constant size.
//
// The zero-value of this struct sets writePending to 'false', indicating that
// no work is needed. When it gets updated with a write, the value needs to be
// flipped to 'true'.
//
// The pendingWrite fields are protected by the fo mutex.
type pendingWrite struct {
	data   []byte // data being written, length implied by len(data)
	offset int64  // offset for where to start the write

	fileIndex    int  // index of the file in the optimizer being written to
	writePending bool // set to true when the write finishes
}

// File is a wrapper for os.File that interacts with the FileOptimizer.
type File struct {
	// atomicClosed is used to indicate whether the file has been closed.
	// Atomics are used here to eliminate lock contention between multiple
	// threads performing operations on the file at the same time, while still
	// allowing all threads to verify that no operation has been performed after
	// the file has been closed.
	atomicClosed uint64

	staticFileIndex int
	staticFile      *os.File
}

// A FileOptimizer is a single struct that can manage multiple files at the same
// time. All of them will syscall over a single thread, in an attempt to reduce
// the total number of syscalls that the program is making.
//
// The FileOptimizer will start to add sleeps between additional writes if they
// pile up beyond a certain point, so that it doesn't fall too far behind.
//
// The list of pending syscalls is a pre-allocated array containing pointers,
// offsets, and lengths. Using a pre-allocated array means that beyond
// allocations inside of the syscall itself, the FileOptimizer is not doing any
// allocations.
//
// NOTE: There is no threadgroup in the FileOptimizer, you close it by calling
// Close() in a tg.AfterStop call. FileOptimizer.Close() will error if it not
// all of the underyling files have been closed yet.
type FileOptimizer struct {
	// files is a list of files that the FileOptimizer is tracking. It'll expand
	// the list as needed, meaning (barely) reduced performance initially, with
	// performance improving
	files []File

	// pendingWrites is a pointer to a slice of MaxPendingWrites entries that
	// tracks writes that are pending to files in the file optimizer. This is a
	// pointer instead of a true slice because the background thread has another
	// slice of the same length, and the background thread is continually
	// swapping out the pendingWrites for the list it finished to keep things
	// efficient.
	pendingSyncs           []*sync.Mutex // TODO: Figure out how to buffer this.
	pendingWrites          *[MaxPendingWrites]pendingWrite
	pendingWritesNextIndex int

	// A channel-free system for orchestrating whether the background thread has
	// work to do. The core is the hasWork mutex, which will remain locked until
	// there is work to do, causing the background thread to block when it tries
	// to lock the mutex. Other threads that are providing work to the
	// background thread will unlock the mutex after they add work to the pile.
	// They will know if the hasWork mutex needs to be unlocked because
	// 'pendingWritesNextIndex' equals zero when they obtain the FileOptimizer
	// mutex.
	hasWork sync.Mutex // there is work if it is unlocked

	// Utilities.
	closed bool
	mu     priorityMutex

	// externFinalIterationComplete is a bool that only gets used by the
	// background thread in 'threadedWriteToFiles'. This bool gets set to true
	// after the FileOptimizer is closed when the background thread begins
	// working on the final set of writes. After all of those writes are
	// complete, the background thread can exit successfully, knowing that every
	// write was called.
	externFinalIterationComplete bool
}

// addWrite is used by File.Write and File.WriteAt to add writes to the
// optimizer's queue.
func (fo *FileOptimizer) addWrite(file File, data []byte, offset int64) {
	// The FileOptimzer needs to be locked for the whole addWrite call.
	fo.mu.Lock()
	defer fo.mu.Unlock()

	// If the queue is full, panic.
	//
	// An alternative option would be to unlock the 'fo' and go to sleep for a
	// long time before waking up to try again, but really we should never be
	// this backlogged, and it IS worth a panic.
	if fo.pendingWritesNextIndex > MaxPendingWrites {
		panic("ratelimiting mechanism on FileOptimizer has failed")
	}

	// Sleep for a while if the queue is filling up. This does unfortunately
	// require sleeping while the lock is held, because we have to ensure other
	// threads are not concurrently adding the the queue as well.  The one
	// downside is that this will also delay the background thread from getting
	// started on the queue if it wants to begin processing, but this is seen as
	// an acceptable sacrifice. If there is a material delay here, it means
	// there are thousands of entries that will be processed at once, and at
	// most one of them delayed the processing by sleeping.
	//
	// The sleep here is not protected with a softSleep because if the sleep
	// takes a long time and yet things are still filling up quickly
	if fo.pendingWritesNextIndex > MaxPendingWrites/2 {
		pwni := float64(pendingWritesNextIndex - MaxPendingWrites/2)
		sleepTime := time.Duration(math.Pow(pwni, FullQueueSleepExponential))

		// Only bother calling Sleep if it's a meaningful amount of time.
		//
		// TODO: Make some noisy logs if the sleep time gets over 100
		// milliseconds, it means the background thread has been processing the
		// same queue for at least 5 seconds.
		if sleepTime > 10*time.Millisecond {
			time.Sleep(time.Duration(sleepTime))
		}
	}

	// Check whether the hasWork mutex needs to be unlocked.
	if fo.pendingWritesNextIndex == 0 {
		fo.hasWork.Unlock()
	}

	// Add a pendingWrite to the next pendingWrite slot.
	fo.pendingWrites[fo.pendingWritesNextIndex].data = data
	fo.pendingWrites[fo.pendingWritesNextIndex].offset = offset
	fo.pendingWrites[fo.pendingWritesNextIndex].fileIndex = file.staticFileIndex
	fo.pendingWrites[fo.pendingWritesNextIndex].writePending = true
	fo.pendingWritesNextIndex++
}

// threadedWriteToFiles is a permanent background thread
func (fo *FileOptimizer) threadedWriteToFiles() {
	// Create a set of pendingWrites that is specific to this thread. As writes
	// get processed, this array of pendingWrites will be swapped with the array
	// in the FileOptimizer, so that the FileOptimizer is always collecting new
	// writes into a fresh batch.
	//
	// NOTE: activeWritesFinalIndex is actually 1 larger than the final index,
	// meaning the comparison is '<' and not '<=' when iterating over the
	// activeWrites.
	activeWrites := new([MaxPendingWrites]pendingWrite)
	var activeWritesFinalIndex int

	// Write space is a moderately sized array that gets used when calling
	// WriteAt, it gets used to minimze the total number of allocations required
	// when writing to the file.
	writeSpace := make([]byte, 1<<24)

	// Infinite loop.
	for {
		// Block until there is work. If the hasWork mutex is locked, the
		// background thread will block until an external thread signals that
		// there is work by unlocking it.
		//
		// Once unblocking, we will wait to signal through the hasWorkLocked
		// bool that it can be unblocked again until we grab the pendingWrites,
		// so that we ensure that all files queued in the interim are waiting
		// for the correct moment to sync.
		fo.hasWork.Lock()

		// To give the write calls time to clump up and buffer, sleep for some
		// time.
		//
		// We don't soft-sleep because the total amount of time is small and we
		// want to minimze the amount of system overhead introduced by this
		// object.
		time.Sleep(SleepPerWriteIteration)

		// The background thread gets priority access to the fo mutex. If a
		// bajillion threads are trying to call addWrite at the same time and
		// all blocking on it because the thread with the lock is sleeping for
		// an hour, the background thread needs to get priority access so it can
		// immediately reset the queue and begin working on the next batch.
		fo.mu.PriorityLock()
		if fo.closed {
			// Exit if the fileOptimizer has been closed.
			fo.mu.Unlock()
			return
		}
		// Swap out the active writes (now empty) for the pending writes.
		// Resetting the pendingWritesNextIndex to zero will also signal that
		// the hasWork mutex is now locked.
		fo.pendingWrites, activeWrites = activeWrites, fo.pendingWrites
		fo.pendingWritesNextIndex, activeWritesFinalIndex = 0, fo.pendingWritesNextIndex
		pendingSyncs := fo.pendingSyncs
		fo.pendingSyncs = make([]*sync.Mutex, 0)
		fo.mu.Unlock()

		// Loop over the new activeWrites array. The activeWrites array has been
		// written in the order that the write calls were made. Each write call
		// is acting on a different file.
		//
		// Originally I tried to sort this array to reduce the n^2 computational
		// overhead, but it got messy because you need to preserve the
		// write-ordering, which means that while you can sort by file index,
		// you can't easily sort within a file index, since each write covers a
		// range of data and you can't sort an element past another element that
		// overlaps with your range. Instead I opted to eat the full n^2 cost
		// and just assume that MaxPendingWrites could reasonably be kept small
		// enough to prevent it from mattering.
		//
		// Each iteration of the loop, we find the first write that hasn't been
		// committed yet. Then we loop over the remaining array to find all
		// overlapping writes to the same file and we learn the full range of
		// writes that need to be made. Then we create a []byte that covers the
		// full range of data. Finally, we copy all of the overlapping writes
		// into that array in the order that the data was originally written and
		// write the result to the os.File.
		nextIndex := 0
		for nextIndex < activeWritesFinalIndex {
			// Iterate over the array to find the full range of overlapping
			// writes with the nextIndex. Any time that the range is expanded,
			// we have to go back to the start and look for more overlaps. Even
			// though this makes the algorithm seem like n^3, it's still
			// actually n^2 because each time we go back we reduce the total
			// number of times we need to iterate over the base array while
			// writing.
			//
			// NOTE: There is a bit of trickiness with the offsets here. The
			// 'low' is set to the index of the offset, and the 'high' is set to
			// the index plus the range. So if you have 1 byte at index 0, your
			// low will be '0' and your high will be '1', even though it's only
			// one byte. This means that two bytes right next to each other will
			// overlap in the comparisons. A single byte at index 0 will have a
			// low of 0 and a high of 1, and a single byte at index 1 will have
			// a low of 1 and a high of 2. When the comparison checks if the low
			// of the '1' byte overlaps with the high of the '0' byte, it will
			// find that they _do_ overlap, which is the correct finding for
			// this algorithm.
			//
			// If in doubt, check the unit tests, which cover this edge case and
			// others.
			targetFile := activeWrites[nextIndex].staticFileIndex
			low := activeWrites[nextIndex].offset
			high := activeWrites[nextIndex].offset + len(activeWrites[nextIndex].data)
			expandedRange := true
			for expandedRange {
				expandedRange = false
				for i := nextIndex + 1; i < activeWritesFinalIndex; i++ {
					// Make sure this write is targeting the same file.
					if activeWrites[i].staticFileIndex != targetFile {
						continue
					}
					// Make sure the write is not already complete.
					if !activeWrites[i].writePending {
						continue
					}
					// Make sure this write overlaps the current write range.
					if activeWrites[i].offset > high {
						continue
					}
					if activeWrites[i].offset+len(activeWrites[i].data) < low {
						continue
					}
					// Make sure this write actually expands the range as
					// opposed to sitting inside of the range. We need this
					// check because we need to know whether we should set the
					// 'expandedRange' variable to true.
					if activeWrites[i].offset >= low && activeWrites[i].offset+len(activeWrites[i].data) <= high {
						continue
					}

					// This element geniunely expands the range, reset the low
					// and high.
					expandedRange = true
					if activeWrites[i].offset < low {
						low = activeWrites[i].offset
					}
					if activeWrites[i].offset+len(activeWrites[i].data) > high {
						high = activeWrites[i].offset + len(activeWrites[i].data)
					}
					// I am fairly confident that in the worst case this break
					// is more optimal than continuing. I am unsure about the
					// average case.
					break
				}
			}

			// We have established a low and high, create the corresponding
			// []byte and then copy elements in. If the final []byte fits in our
			// writeSpace, we'll use the writeSpace slice, otherwise we'll make
			// a new slice. This helps minimize allocations and GC pressure.
			var finalWrite []byte
			if high-low > len(writeSpace) {
				finalWrite = make([]byte, high-low)
			} else {
				finalWrite = writeSpace[:high-low]
			}

			// Iterate over the list one more time, copying in every overlapping
			// byte slice in order.
			for i := nextIndex; i < activeWritesFinalIndex; i++ {
				// Make sure this write is targeting the same file.
				if activeWrites[i].staticFileIndex != targetFile {
					continue
				}
				// Make sure the write is not already complete.
				if !activeWrites[i].writePending {
					continue
				}
				// Make sure this write is part of the write range.
				if activeWrites[i].offset > high {
					continue
				}
				if activeWrites[i].offset+len(activeWrites[i].data) < low {
					continue
				}

				// Mark this write as complete.
				activeWrites[i].writePending = false
				// Copy the write into the finalWrite.
				start := activeWrites[i].offset - low
				copy(finalWrite[start:], activeWrites[i].data)
			}

			// Call WriteAt on the underlying file. If the write fails, crash
			// the program. Critical files use the optimizer, there is no
			// recovering if a write can't complete.
			_, err := x.WriteAt(low, finalWrite)
			if err != nil {
				panic("write failed on optimized file:", err)
			}

			// Advance nextIndex to the next pending write.
			for nextIndex < activeWritesFinalIndex && !activeWrites[nextIndex].writePending {
				nextIndex++
			}
		}

		// Release all of the pending Syncs.
		for _, sync := range pendingSyncs {
			sync.Unlock()
		}
	}
}

// TODO: Implement File.Open. It'll look for the lowest fileIndex that is
// available, and allocate a larger slice if needed. It does this by iterating
// over the array. If we know there are no gaps before a certain point, we can
// remember that and reduce the amount of future scanning required.

// Close will ensure the fileOptimizer gets shut down.
//
// Note: Close should not be called on the FileOptimizer until Close has been
// called on every underlying file.
func (fo *FileOptimizer) Close() error {
	fo.mu.Lock()
	defer fo.mu.Unlock()
	if fo.closed {
		return errors.New("cannot call close multiple times")
	}

	// Check that all files were closed before the optimizer was closed.
	for i := 0; i < len(fo.files); i++ {
		if fo[i] != nil {
			return errors.New("cannot call close while there are unclosed files")
		}
	}
	// Set 'closed' to true to kill the optimizer loop and catch frivolous calls
	// to Close.
	fo.closed = true
	return nil
}

// Close will block until all pending writes have been called, and then it will
// subsequently call Sync() on the underlying os.File. Close should only be
// called one time, Close should not be called concurrently with any other
// function.
func (f *File) Close() error {
	firstClose := atomic.CompareAndSwapUint64(&f.atomicClosed, 0, 1)
	if !firstClose {
		return errors.New("cannot close a closed file")
	}

	// Sync the file to ensure that all pending writes have been cleared from
	// the file optimizer, and then properly close the file.
	syncErr := f.Sync()
	closeErr := f.file.Close()

	// Pull the file out of the file optimizer.
	f.staticFileOptimizer.mu.Lock()
	f.staticFileOptimizer.files[f.fileIndex] = nil
	f.staticFileOptimizer.mu.Unlock()

	// Return a composition of the sync and close errors.
	return errors.Compose(syncErr, closeErr)
}

// Sync will block until all pending writes have completed, and then it will
// call Sync() on the underlying file.
//
// TODO: This can be optimized by ensuring that it is connected directly to at
// least one Write call. I ran out of time today and didn't figure out how to do
// this without an in-memory map, which we would like to avoid.
func (f *File) Sync() error {
	// Create a mutex and lock it.
	var syncMu sync.Mutex
	syncMu.Lock()

	// Add the mutex to the FileOptimizer, it will be unlocked when all pending
	// writes are complete.
	f.staticFileOptimizer.mu.Lock()
	f.staticFileOptimizer.pendingSyncs = append(f.staticFileOptimizer.pendingSyncs, &syncMu)
	f.staticFileOptimizer.mu.Unlock()

	// Block until the FileOptimizer unlocks the syncMu.
	syncMu.Lock()

	// Sync the underlying file and return.
	return f.file.Sync()
}

// NewFileOptimizer initializes and returns a FileOptimizer.
//
// TODO: incomplete, need to allocate the array for example
func NewFileOptimizer() (*FileOptimizer, error) {
	fo := &FileOptimizer{}

	// lock the hasWork mutex so that the background thread is immediately
	// blocking at startup.
	fo.hasWork.Lock()
	return fo, nil
}
