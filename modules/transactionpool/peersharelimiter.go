package transactionpool

import (
	"container/heap"
	"sync"
)

// blockingPeer is a thread that is being blocked by the transaction pool
// ratelimit.
type blockingPeer struct {
	timeDiscovered int64
	sizeChan       chan int
	unblockChan    chan struct{}
}

// peerHeap is a heap of peers that are blocking to sent a transaction set,
// sorted by their timeDiscovered. Peers that were discovered sooner will be
// popped off of the heap first.
type peerHeap []*blockingPeer

// Implmentation of heap.Interface for peerHeap.
func (ph peerHeap) Len() int           { return len(ph) }
func (ph peerHeap) Less(i, j int) bool { return ph[i].timeDiscovered < ph[j].timeDiscovered }
func (ph peerHeap) Swap(i, j int)      { ph[i], ph[j] = ph[j], ph[i] }
func (ph *peerHeap) Push(x interface{}) {
	// Add the element to the heap.
	bp := x.(*blockingPeer)
	*ph = append(*ph, bp)
}
func (ph *peerHeap) Pop() interface{} {
	old := *ph
	n := len(old)
	bp := old[n-1]
	*ph = old[0 : n-1]
	return bp
}

// peerShareLimiter will ratelimit the new peer share action of the
// transaction pool, ensuring that new peers can only consume so much bandwidth.
type peerShareLimiter struct {
	peerHeap   peerHeap
	pushNotify chan struct{}
	mu         sync.Mutex

	*TransactionPoolUtils
}

// callBlockForShareTset will block until the transaction pool is ready to share
// a transaction set with a new peer. This method is intended to be called only
// by the new peer share subsystem.
//
// A channel is returned which MUST be used to report the size of the
// transaction set being broadcast. The size of the requested broadcast is
// reported after-the-fact because the broadcast is not allowed to be sent right
// away, and factors such as the repeat broadcast filter can change how large a
// broadcast may be while the broadcast is being blocked.
//
// Once callBlockForShareTset unblocks, the transaction set can be shared at
// full speed. The ratelimit enforced by the peer share limiter is intended to
// be an average ratelimit over longer periods of time, as opposed to a
// continuous ratelimit inteded to throttle bursts of activity.
//
// The input to this method, 'timeDiscovered', is used to give priority to peers
// that have been around longer. If multiple threads are trying to send
// transaction sets to peers at once, the threads that provide an older/smaller
// 'timeDiscovered' input will be given priority.
func (psrl *peerShareLimiter) callBlockForShareTSet(timeDiscovered int64) (chan int, bool) {
	// Create a blocked peer object to represent this blocked thread and push it
	// to the peer heap.
	bp := &blockingPeer{
		timeDiscovered: timeDiscovered,
		sizeChan:       make(chan int),
		unblockChan:    make(chan struct{}),
	}
	psrl.managedPush(bp)

	// Block until the ratelimiter releases the object.
	select {
	case <-psrl.tg.StopChan():
		return nil, false
	case <-bp.unblockChan:
		return bp.sizeChan, true
	}
}

// managedPush will push a blockingPeer onto the heap of the
// peerShareLimiter
func (psrl *peerShareLimiter) managedPush(bp *blockingPeer) {
	psrl.mu.Lock()
	heap.Push(&psrl.peerHeap, bp)
	psrl.mu.Unlock()

	// Notify the unblocker thread that there is a new element in the heap.
	select {
	case psrl.pushNotify <- struct{}{}:
	default:
	}
}

// managedPop will pop a blockingPeer off of the peer heap.
func (psrl *peerShareLimiter) managedPop() *blockingPeer {
	psrl.mu.Lock()
	bp := heap.Pop(&psrl.peerHeap).(*blockingPeer)
	psrl.mu.Unlock()
	return bp
}

// threadedUnblockSharingThreads wlll unblock sharing threads that are waiting
// to be released by the peer heap.
func (psrl *peerShareLimiter) threadedUnblockSharingThreads() {
	for {
		// TODO: Block until online and synced.

		// Check the length of the heap. If there is nothing in the heap, block
		// until there is something in the heap.
		psrl.mu.Lock()
		heapLen := len(psrl.peerHeap)
		psrl.mu.Unlock()
		if heapLen == 0 {
			select{
			case <-psrl.tg.StopChan():
				return
			case <-psrl.pushNotify():
			}
		}

		// Pop an element off of the heap and release the object. After the
		// object is released, its sizeChan should be used to send a transaction
		// size to the psrl. This size will tell the psrl how long to wait
		// before popping off another peer.
		bp := psrl.managedPop()
		close(bp.unblockChan)
		size := <-bp.sizechan

		// Sleep based on the size and the ratelimit.
		sleepDuration := time.Duration(size) * peerShareRateLimit
		select {
		case <-psrl.tg.StopChan():
			return
		case <-time.After(sleepDuration):
		}
	}
}

// newPeerShareLimiter will return a new peerShareLimiter that is ready for use
// by the tpool.
func (tp *TransactionPool) newPeerShareLimiter() *peerShareLimiter {
	return &peerShareLimiter{
		pushNotify: make(chan struct{}, 1),

		TransactionPoolUtils: tp.TransactionPoolUtils,
	}
}
