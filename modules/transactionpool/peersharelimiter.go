package transactionpool

import (
	"container/heap"
	"sync"
	"time"
)

// blockingPeer represents a thread that is being blocked by the peer share
// limiter. Closing unblockChan will unblock the thread. The timeConnected field
// indicates what time we connected with this peer. Peers that were connected
// earlier are given priority.
type blockingPeer struct {
	timeConnected int64
	sizeChan      chan int
	unblockChan   chan struct{}
}

// peerHeap is a heap of blocking peers that is sorted by timeConnected, with
// the earliest timeConnected value being the first to be popped off of the
// heap.
type peerHeap []*blockingPeer

// Implmentation of heap.Interface for peerHeap.
func (ph peerHeap) Len() int           { return len(ph) }
func (ph peerHeap) Less(i, j int) bool { return ph[i].timeConnected < ph[j].timeConnected }
func (ph peerHeap) Swap(i, j int)      { ph[i], ph[j] = ph[j], ph[i] }
func (ph *peerHeap) Push(x interface{}) {
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

// peerShareLimiter determines when a pre-existing transaction set can be shared
// with a new peer. The peer share limiter watches for conditions such as the
// transaction pool being online and synced, and also enforces a long term
// ratelimit that prevents the new peer share subsystem from consuming too much
// bandwidth.
type peerShareLimiter struct {
	peerHeap   peerHeap
	pushNotify chan struct{}
	mu         sync.Mutex

	*transactionPoolUtils
}

// callBlockForShareTSet is called by the new peer share subsystem when a thread
// wishes to send a pre-existing transaction set to a peer that has not yet seen
// that transaction set. This call will block until the limiter is ready to
// allow the thread to send a transaction set.
//
// The thread is expected to pick which transaction set after being unblocked,
// because the set of transactions in the tpool can change substantially while
// the thread is blocked. Because of this, the size of the transaction set that
// will be sent cannot be known until after the thread is unblocked. A channel
// is returned by this call to allow the thread to communicate back to the peer
// share limiter how much bandwidth will be used to relay the chosen transaction
// set. This channel MUST be used, or the peer share limiter will deadlock.
//
// Once the thread has been unblocked, the transaction set can be shared at full
// speed, regardless of how large it is. The ratelimit enforced by the peer
// share limiter is intended to manage the long term average bandwidth
// consumption of the new peer share subsystem, and is not concerned with bursts
// of bandwidth.
//
// receiving the transaction set connected to the transaction pool. This time
// will be used to give priority to peers that connected earlier when the peer
// share limiter is choosing which threads to unblock.
func (psl *peerShareLimiter) callBlockForShareTSet(timeConnected int64) (chan int, bool) {
	// Create a blocked peer object to represent this blocked thread and push it
	// to the peer heap.
	bp := &blockingPeer{
		timeConnected: timeConnected,
		sizeChan:       make(chan int),
		unblockChan:    make(chan struct{}),
	}
	psl.managedPush(bp)

	// Block until the ratelimiter releases the object.
	select {
	case <-psl.tg.StopChan():
		return nil, false
	case <-bp.unblockChan:
		return bp.sizeChan, true
	}
}

// managedBlockUntilOnlineAndSynced will block until siad is both online and
// synced. The function will return false if shutdown occurs before both
// conditions are met.
func (psl *peerShareLimiter) managedBlockUntilOnlineAndSynced() bool {
	// Infinite loop to keep checking online and synced status.
	for {
		tpoolSynced := psl.staticCore.callTpoolSynced()
		online := psl.gateway.Online()
		if tpoolSynced && online {
			return true
		}

		// Sleep for a bit before trying again, exit early if the transaction
		// pool is shutting down.
		select {
		case <-psl.tg.StopChan():
			return false
		case <-time.After(onlineSyncedLoopSleepTime):
			continue
		}
	}
}

// managedPush will push a blockingPeer onto the heap of the
// peerShareLimiter.
func (psl *peerShareLimiter) managedPush(bp *blockingPeer) {
	psl.mu.Lock()
	heap.Push(&psl.peerHeap, bp)
	psl.mu.Unlock()

	// Notify the unblocker thread that there is a new element in the heap.
	select {
	case psl.pushNotify <- struct{}{}:
	default:
	}
}

// managedPop will pop a blockingPeer off of the peer heap.
func (psl *peerShareLimiter) managedPop() *blockingPeer {
	psl.mu.Lock()
	bp := heap.Pop(&psl.peerHeap).(*blockingPeer)
	psl.mu.Unlock()
	return bp
}

// threadedUnblockSharingThreads wlll unblock sharing threads that are waiting
// to be released by the peer heap as the conditions for unblocking them are
// met.
func (psl *peerShareLimiter) threadedUnblockSharingThreads() {
	for {
		// The transaction pool should not be sharing transaction sets with
		// peers unless the transaction pool is synced to the currenct consensus
		// of the network. The transaction pool should also be online.
		//
		// If false is returned, the transaction pool was shutdown before it
		// could satisfy the conditions of being online and synced.
		if !psl.managedBlockUntilOnlineAndSynced() {
			return
		}

		// Check the length of the heap. If there is nothing in the heap, block
		// until there is something in the heap.
		psl.mu.Lock()
		heapLen := len(psl.peerHeap)
		psl.mu.Unlock()
		if heapLen == 0 {
			select{
			case <-psl.tg.StopChan():
				return
			case <-psl.pushNotify:
			}
		}

		// Pop an element off of the heap and release the object. After the
		// object is released, its sizeChan should be used to send a transaction
		// size to the psl. This size will tell the psl how long to wait
		// before popping off another peer.
		bp := psl.managedPop()
		close(bp.unblockChan)
		size := <-bp.sizeChan

		// Sleep based on the size and the ratelimit.
		sleepDuration := time.Duration(size) * peerShareRateLimit
		select {
		case <-psl.tg.StopChan():
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

		transactionPoolUtils: tp.transactionPoolUtils,
	}
}
