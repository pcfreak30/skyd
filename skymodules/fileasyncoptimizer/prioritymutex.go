// prioritymutex.go implements a priority mutex that allows calls to
// PriorityLock() which will take absolute preference over any calls to a normal
// Lock(). If there is a constant stream of calls to PriorityLock(), the
// non-priority threads will starve.
//
// TODO: Can probably move this code to a separate package. When we do make a
// separate package for the priority mutex, the ringQueue can be moved to a
// separate file.
package fileasyncoptimizer

const initialRingSize = 10e3

// ringQueue defines a queue where you can add elements to the end of the queue
// and pop elements from the front of the queue. In an effort to minimize the
// total number of memory allocations, ringQueue uses an array as a ring with a
// constantly moving first and last index, and only expands the size of the
// array when it fills all the way up.
type ringQueue struct {
	ring       []*sync.Mutex
	firstIndex int
	nextIndex  int
}

// push will add a new element to the ringQueue. If the ring queue is
// uninitizialed or full, push will resize the ring.
func (rq *ringQueue) push(e *sync.Mutex) {
	// Check if the ring is uninitialized.
	if len(rq.ring) == 0 {
		rq.ring = make([]*sync.Mutex, initialRingSize)
		rq.firstIndex = 0
		rq.nextIndex = 0
	}

	// Add the new element at the nextIndex. An invariant of the ring is that
	// the nextIndex will always be empty, because we resize the ring after
	// checking.
	rq.ring[nextIndex] = e
	rq.nextIndex++
	if rq.nextIndex == len(rq.ring) {
		rq.nextIndex = 0
	}

	// Check if the ring is now full, and resize if needed. We can use first ==
	// next as the comparison because we know an element was just added, we know
	// that the ring is not empty.
	if rq.firstIndex == rq.nextIndex {
		newRing := make([]*sync.Mutex, len(rq.ring)*2)

		// Copy the existing ring into the new ring. We're going to re-order
		// the new ring so that the firstIndex is back to 0.
		n := copy(newRing, rq.ring[rq.firstIndex:])
		if rq.nextIndex < rq.firstIndex {
			n += copy(newRing[len(rq.ring)-rq.firstIndex:], rq.ring[0:rq.nextIndex])
		}

		// Reset the first and last index, and update the final position of
		// the ring.
		rq.firstIndex = 0
		rq.nextIndex = n
		rq.ring = newRing
	}
}

// pop will pull the first element out of the queue. If there are no elements in
// the queue, pop will panic.
func (rq *ringQueue) pop() *sync.Mutex {
	// Check that this is a safe operation.
	if rq.firstIndex == rq.nextIndex {
		panic("cannot call pop on an empty ringQueue")
	}

	// Grab the element and then increment the firstIndex.
	r := rq.firstIndex
	rq.firstIndex++
	// Determine if the first index is now wrapping around to the front.
	if rq.firstIndex == len(rq.ring) {
		rq.firstIndex = 0
	}
	return r
}

// empty returns true if there are no elements in the queue.
func (rq *ringQueue) empty() bool {
	return rq.firstIndex == rq.nextIndex
}

// PriorityMutex defines a mutex where threads can get priority access by
// calling PriorityLock(). Anyone waiting to acquire the lock via calling Lock()
// will starve until there are no priority threads requesting the lock. Then
// Unlock() will be called in order the Lock() was called.
type PriorityMutex struct {
	// Every caller gets their own mutex and gets put into a queue. The caller's
	// mutex will be unlocked when it is their turn.
	threadActive  bool
	priorityQueue ringQueue
	queue         ringQueue

	queueMutex sync.Mutex
}

// Lock will block until all calls to PriorityLock have been fulfilled. New
// calls to PriorityLock after Lock is called will get preference, this call can
// starve/block indefinitely if there is a continuous stream of calls to
// PriorityLock by other threads.
func (pm *priorityMutex) Lock() {
	pm.queueMutex.Lock()
	defer pm.queueMutex.Unlcok()

	// If there is no thread active, we can go without blocking.
	if !threadActive {
		threadActive = true
		return
	}

	// If there is a thread active, we need to add ourselves to the queue and
	// wait for the active thread to unblock us.
	var mu sync.Mutex
	mu.Lock()
	pm.queue.push(&mu)
	// Block until our queued mutex is unblocked, then we are free to go.
	mu.Lock()
}

// PriorityLock grabs a lock in front of any pending calls to Lock.
func (pm *priorityMutex) PriorityLock() {
	pm.queueMutex.Lock()
	defer pm.queueMutex.Unlcok()

	// If there is no thread active, we can go without blocking.
	if !threadActive {
		threadActive = true
		return
	}

	// If there is a thread active, we need to add ourselves to the queue and
	// wait for the active thread to unblock us.
	var mu sync.Mutex
	mu.Lock()
	pm.priorityQueue.push(&mu)
	// Block until our queued mutex is unblocked.
	mu.Lock()
}

// Unlock will make the mutex available to the next caller.
func (pm *priorityMutex) Unlock() {
	// Upon unlock, figure out which stalling thread to release.
	pm.queueMutex.Lock()
	defer pm.queueMutex.Unlock()

	// Look for any blocking priority threads.
	if !pm.priorityQueue.empty() {
		pm.priorityQueue.pop().Unlock()
		return
	}

	// Look for any blocking non-priority threads.
	if !pm.queue.empty() {
		pm.queue.pop().Unlock()
		return
	}

	// Check for misuse.
	if !pm.threadActive {
		panic("calling unlock when nothing is locked")
	}

	// Nobody is in the queue, set threadActive to false so the next called does
	// not block.
	pm.threadActive = false
}
