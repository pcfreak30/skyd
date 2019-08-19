package transactionpool

import (
	"testing"

	"gitlab.com/NebulousLabs/fastrand"
)

// TestPeerHeap checks that the implementation of the peer heap is correctly
// sorting peers by age, preferring the oldest peers, and also notifying the
// unblock thread upon a push.
func TestPeerHeap(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}

	// Create a tpool tester to use for this test.
	tpt, err := createTpoolTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer tpt.Close()
	tp := tpt.tpool

	// Create a few peers to push.
	bp1 := &blockingPeer{
		timeDiscovered: 10,
		sizeChan:       make(chan int),
		unblockChan:    make(chan struct{}),
	}
	bp2 := &blockingPeer{
		timeDiscovered: 20,
		sizeChan:       make(chan int),
		unblockChan:    make(chan struct{}),
	}
	bp3 := &blockingPeer{
		timeDiscovered: 30,
		sizeChan:       make(chan int),
		unblockChan:    make(chan struct{}),
	}
	bp4 := &blockingPeer{
		timeDiscovered: 40,
		sizeChan:       make(chan int),
		unblockChan:    make(chan struct{}),
	}
	bps := []*blockingPeer{bp1, bp2, bp3, bp4}

	// Create a peerShareRateLimiter that will be used to interact with the
	// heap.
	psrl := tp.newPeerShareRateLimiter()

	// Push each of the blocking peers into the heap, in a random order.
	perm := fastrand.Perm(len(bps))
	for _, i := range perm {
		psrl.managedPush(bps[i])
	}

	// Pop the peers one at a time, and ensure that we get the right peers.
	p1 := psrl.managedPop()
	if p1.timeDiscovered != bp1.timeDiscovered {
		t.Error("Heap seems to be popping in the wrong order")
	}
	p2 := psrl.managedPop()
	if p2.timeDiscovered != bp2.timeDiscovered {
		t.Error("Heap seems to be popping in the wrong order")
	}
	p3 := psrl.managedPop()
	if p3.timeDiscovered != bp3.timeDiscovered {
		t.Error("Heap seems to be popping in the wrong order")
	}
	p4 := psrl.managedPop()
	if p4.timeDiscovered != bp4.timeDiscovered {
		t.Error("Heap seems to be popping in the wrong order")
	}
}
