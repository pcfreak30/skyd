// fileasyncoptimizer.go is a utility that wraps a bunch of os.File objects and
// serializes their syscalls, building a queue and optimizing away multiple
// writes to the same area of the file. The primary goal of this object is to
// eliminate any blocking that happens when writing to a file, and the secondary
// goals are to reduce the total number of syscals that get made and reduce
// overall pressure on the filesystem.
//
// We built this code after realizing that when under substantial pressure,
// file.Write calls would often block for 5 or more seconds. This code allows us
// to call file.Write in critical paths without worrying that there will be an
// extended amount of blocking. This file provides the same consistency
// guarantees as a normal os.File - data that is written will be read back
// correctly, and data cannot be guaranteed to have been persisted to disk until
// file.Sync has been called. If file.Sync is called, a guarantee is provided
// that the data is persisted.
//
// There are some mild CPU tradeoffs that get made. There are fewer syscalls,
// but more pressure on the CPU as algorithms run to merge together various
// Write calls. This is generally a good trade.
package fileasyncoptimizer

import (
	"math"
	"os"
	"sync/atomic"
)

const (
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
	// NOTE: If changing this value, FullQueueSleepExponential also needs to be
	// changed.
	MaxPendingWrites = 10e3

	// SleepPerWriteCycle defines the minimum amount of time that the background
	// thread is going to sleep before performing work. This ensures that there is
	// time for multiple writes to the same file to be batched together.
	SleepPerWriteCylce = time.Millisecond * 50
)

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
// NOTE: For best results, the entire executable should use the same
// fileasyncoptimizer, and not one per package. If there are multiple optimizers
// running at once, they will each dedicate a full thread to making syscalls,
// which can significantly increase overheads and substantially damage peak
// latency.
//
// NOTE: Closing this object can take a while, and will not fast-close through a
// thread-group because every pending write needs time to be written to disk. If
// there are a large number of pending writes, this can take some time and there
// is no shortcutting it.
type FileOptimizer struct {
	// The writeCounter exists to apply backpressure on a large number of
	// incoming writeThreads. Generally speaking this value is never important
	// and it should never really get to the point where there are sleeps. But
	// if it does get to that point, we want to make sure that only one write
	// can happen at a time, and that the write that is happening is actively
	// blocking other writes for increasing periods of time. The mutex exists to
	// create lock contention among threads that are trying to increment the
	// counter.
	//
	// The counter itself however is atomic because we want threads to be able
	// to decrement the counter at any time as the write gets cleared. The
	// decrement threads do not need to respect the mutex.
	//
	// The waitingForWorkMu is a second mutex that interacts with the
	// atomicWriteCounter. If the atomicWriteCounter goes to 0, the processing
	// thread will stop all work and block until the waitingForWork mutex is
	// unlocked. When it does get unlocked, it will not do any work until it is
	// locked again. This allows the managedScheduleWrite function to call
	// Unlock safely any time that it increments the number of writes from 0 to
	// 1.
	//
	// atomicClosed gets set to 1 when the program is closed. It should only be
	// written to when the writeCounterMu is locked, so that we can detect
	// misuse where files are still being written do during or after Close is
	// called on the optimizer.
	//
	// NOTE: atomics all need to be clustered at the top of the struct to be
	// safe on 32 bit systems. Since this is a weird hybrid of an atomic and a
	// non-atomic, it needs to be directly below the cluster of atomics that we
	// normally place at the top.
	atomicClosed uint64
	atomicWriteCounter uint64
	waitingForWorkMu sync.Mutex
	writeCounterMu sync.Mutex

	// Linked list of files in the optimizer.
	filesHead *File
	filesTail *File

	// closedMu is used to coordinate shutdown between a call to Close() and the
	// background thread. Close() will lock the mutex when closing starts, and
	// then it'll lock it a second time to wait for the background thread to
	// finish cleaning up all the pending writes. When the background thread is
	// finished, it'll unlock the mutex.
	closedMu sync.Mutex
}

// completeWrite will let the file optimizer know that a pendingWrite has been
// written to disk, and that we can decrement the number of writes remaining.
func (fo *FileOptimizer) completeWrite() uint64 {
	// Decrement the write counter.
	writesRemaining := atomic.AddUint64(&fo.atomicWriteCounter, ^uint64(0))

	// Check for underflow.
	if writesRemaining > MaxPendingWrites {
		panic("the file optimizer write counter is being misused", writesRemaining)
	}
	return writesRemaining
}

// managedScheduleWrite is a thread that keeps backpressure on incoming file
// writes if there are more writes being added than the background thread is
// processing. This serializes write calls through a mutex that will sleep for
// increasingly long periods of time if backpressure builds too high.
//
// The sleeps don't even start until MexPendingWrites/2 writes have built up,
// which is quite a large number, so this really should only come into effect if
// something is going seriously wrong.
func (fo *FileOptimizer) managedScheduleWrite() {
	// Before incrementing the writeCounter, sleep to apply backpressure. Most
	// of the time, there should be no sleeping at all because the writeCounter
	// should be staying small.
	fo.writeCounterMu.Lock()
	if atomic.LoadUint64(&fo.atomicClosed) == 1 {
		// Developer error, should not be calling write if the file optimizer is
		// not running anymore. Panic here because this will result in data not
		// being written to disk that might be expected to get written to disk.
		panic("write called on FileOptimizer after the FileOptimizer was closed")
	}
	writes := atomic.AddUint64(&fo.atomicWriteCounter, 1)
	if writes == 1 {
		// We incremented the number of writes from 0 to 1, so we need to
		// unblock the processing thread.
		fo.waitingForWorkMu.Unlock()
	}
	if writes > MaxPendingWrites {
		panic("write backpressure management has failed")
	}
	half := MaxPendingWrites/2
	if writes > half {
		sleepTime := time.Duration(math.Pow(FullQueueSleepExponential, writes-half))
		if sleepTime > 10*time.Millisecond {
			time.Sleep(time.Duration(sleepTime))
		}
	}
	fo.writeCounterMu.Unlock()
}

// threadedWriteToFiles is a permanent background thread that loops over the
// files that are open in the file optimizer and commits the pending writes to
// disk, aggregating where possible.
//
// TODO: Exit on close. Cannot exit until number of writes remaining is zero.
func (fo *FileOptimizer) threadedWriteToFiles() {
	// Infinite loop.
	for {
		// INVARIANT: We can only reach the top of this loop if the write
		// counter has been brought to zero. This means that the
		// waitingForWorkMu was locked by the processing thread and is waiting
		// for a call to managedScheduleWrite to unblock the processing thread.
		//
		// The waitingForWork mechanism operates by having the write scheduler
		// unlock the waitingForWorkMu if it brings the value from 0 to 1. We
		// guarantee that the mutex is locked in this case by ensuring we do not
		// decrement the counter until after we have locked the mutex. If the
		// value ever goes to zero again, we have to stop all work until we've
		// locked the mutex once more.
		fo.waitingForWorkMu.Lock()

		// Check for closed condition. If we are closed and there are no pending
		// writes, we can exit immediately. Otherwise we need to unlock the
		// mutex and get the pending writes to 0. We need to unlock the mutex
		// because (as a result of the fo being closed) no writing thread will
		// unlock it for us. When we get pending writes to zero, we'll iterate
		// again and exit this time.
		if atomic.LoadUint64(&fo.atomicClosed) == 1 {
			if atomic.LoadUint64(&fo.atomicWriteCounter) == 0 {
				fo.closedMu.Unlock()
				return
			}
			fo.waitingForWorkMu.Unlock()
		}

		// To give the write calls time to clump up and buffer, sleep for some
		// time.
		//
		// We don't soft-sleep because the total amount of time is small and we
		// want to minimze the amount of system overhead introduced by this
		// object.
		time.Sleep(SleepPerWriteIteration)

		// Start work with the first file.
		//
		// We don't need to check if the filesHead is nil because we will only
		// be here if there is a pending write, which implies that there is a
		// file.
		//
		// NOTE: The only exit condition here is the write counter reaching
		// zero. We have to be strict about that exit condition because all of
		// our control flow for waiting for new work and waiting for shutdown
		// depends on it.
		current := fo.filesHead
LOOP A:
		for {
			// Process writes on just this file. Start by grabbing the
			// pendingWrites and moving them to the processingWrites.
			//
			// NOTE: If we ever decrement the write counter to 0, we need to
			// exit immediately and go wait for the waitingForWorkMu to be
			// unlocked again. This is important to the safety of the control
			// flow.
			//
			// NOTE: The nested locking here is safe because no other code in
			// this object performs nested locking. We need to do this because
			// we have to move the pendingWrites to the processingWrites in an
			// atomic action, otherwise the read calls might miss some writes.
			current.pendingWritesMu.Lock()
			current.processingWritesMu.Lock()
			current.processingWritesHead = current.pendingWritesHead
			current.pendingWritesHead = nil
			current.processingWritesMu.Unlock()
			current.pendingWritesMu.Unlock()

			// Process the writes one at a time. There will be concurrent
			// threads reading from these values, but no concurrent threads
			// writing to these values, so we only need to grab the lock when we
			// are doing writing (to protect the other reads).
			nextWrite := currentProcessingWritesHead
			for nextWrite != nil {
				// TODO: We could reduce syscall overhead by checking if a bunch
				// of writes in a row can be all written in a single call. This
				// does require allocating new memory (or using an object pool),
				// and given that the goal here is to substantially reduce
				// syscall pressure, we probably should see if we can combine
				// consecutive data.
				_, err := current.staticFile.WriteAt(current.data, current.offset)
				if err != nil {
					panic("error while calling WriteAt on optimizer file: ", err)
				}

				// Lock the procesingWritesMu and remove this node from the
				// list. Then move on to the next node.
				//
				// TODO: Return the object to the object pool once those are
				// implemented.
				current.processingWritesMu.Lock()
				current.processingWritesHead = nextWrite.next
				current.processingWritesMu.Unlock()
				nextWrite = nextWrite.next

				// Decrement the number of pending writes. If the counter was
				// decremented to 0, break out of loop A so that we go back to
				// blocking on the waitingForWorkMu.
				newWrites := fo.completeWrite()
				if newWrites == 0 {
					break A
				}
			}

			// We've finished processing writes on the current file, move to the
			// next file.
			if current.next == nil {
				current = fo.filesHead
			} else {
				current = current.next
			}
		}
	}
}

// Close will shut down the background thread of the file optimizer.
func (fo *FileOptimizer) Close() error {
	// Lock the closed mutex, this will enable the background thread to inform
	// us when it has finished processing all remaining writes.
	fo.closedMu.Lock()

	// We set the fo to closed while holding the writeCounterMu lock, which
	// means it will not be competing with any scheduled writes. Close() should
	// not be called while there are writes in progress anyway, but we do this
	// as an extra layer of safety + detecting misuse.
	fo.writeCounterMu.Lock()
	swapped := atmoic.CompareAndSwap(&fo.atomicClosed, 0, 1)
	fo.writeCounterMu.Unlock()
	if !swapped {
		fo.closedMu.Unlock()
		return errors.AddContext(err, "file optimizer is already closed")
	}

	// Unlock the waitingForWorkMu to signal a wake-up. The waitingForWorkMu
	// will either be locked because there are pending writes, or it will be
	// locked because no pending writes have woken it up from sleeping. Once we
	// have closed, there should be no more pending writes at all, and we have
	// panic checks that enforce this.
	fo.waitingForWorkMu.Unlock()

	// Block until the background thread unlocks the closedMu.
	fo.closedMu.Lock()
	// Unlock the closedMu again, which will prevent a deadlock in the event
	// that close is called multiple times.
	fo.closedMu.Unlock()
}

// TODO: Implement File.Open.
