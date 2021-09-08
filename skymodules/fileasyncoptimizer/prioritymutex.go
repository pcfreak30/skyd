// prioritymutex.go implements a priority mutex that allows calls to
// PriorityLock() which will take absolute preference over any calls to a normal
// Lock(). If there is a constant stream of calls to PriorityLock(), the
// non-priority threads will starve.
//
// TODO: Can probably move this code to a separate package.
package fileasyncoptimizer

// TODO: Switch from this dumb append construction to a ringbuffer. Grow the
// ringbuffer if it fills up, and log when the ringbuffer grows.

// priorityMutex defines a mutex where threads can get priority access by
// calling PriorityLock(). Anyone waiting to acquire the lock via calling Lock()
// will starve until there are no priority threads requesting the lock. Then
// Unlock() will be called in order the Lock() was called.
type priorityMutex struct {
	// Every caller gets their own mutex and gets put into a queue. The caller's
	// mutex will be unlocked (allowing free execution)
	threadActive bool
	priorityQueue []*sync.Mutex
	queue []*sync.Mutex

	queueMutex sync.Mutex
}

// Lock will block until all calls to PriorityLock have been fulfilled.
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
	pm.queue = append(pm.queue, &mu)

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
	pm.priorityQueue = append(pm.priorityQueue, &mu)

	// Block until our queued mutex is unblocked, then we are free to go.
	mu.Lock()
}

// Unlock will make the mutex available to the next caller.
func (pm *priorityMutex) Unlock() {
	// Upon unlock, figure out which stalling thread to release.
	pm.queueMutex.Lock()
	defer pm.queueMutex.Unlock()

	// Look for any blocking priority threads.
	if len(pm.priorityQueue) > 0 {
		pm.priorityQueue[0].mu.Unlock()
		pm.priorityQueue = pm.priorityQueue[1:]
		return
	}

	// Look for any blocking threads.
	if len(pm.queue) > 0 {
		pm.queue[0].mu.Unlock()
		pm.queue = pm.queue[1:]
		return
	}

	// Nobody is in the queue, set threadActive to false.
	pm.threadActive = false
}
