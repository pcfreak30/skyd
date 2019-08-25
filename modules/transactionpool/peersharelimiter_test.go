package transactionpool

import (
	"sync"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/fastrand"
)

// TestPeerHeap checks that the implementation of the peer heap is correctly
// sorting peers by age, preferring the oldest peers, and waiting an appropriate
// amount of time between unblocking peers.
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
	psl := tpt.tpool.staticPeerShareLimiter

	// Simulate multiple peers blocking simultaneously and check that the peers
	// are being unblocked in the correct order with a time delay.
	recentSize := 0
	recentUnblock := time.Now()
	recentlyFinished, firstFinished := -1, -1
	perm := fastrand.Perm(10)
	var wg sync.WaitGroup
	wg.Add(len(perm))
	for _, I := range perm {
		go func(I int) {
			defer wg.Done()

			// Set the peer to block.
			sizeChan, success := psl.callBlockForShareTSet(int64(I))
			if !success {
				t.Error("tpool is reporting that it shut down before unblocking TSet broadcast request")
			}

			// After the peer has unblocked but before the unblocker thread is
			// unblocked (to prevent race conditions), check what order peers
			// are being unblocked in. The first peer may come in any order but
			// the following peers should be in sorted order.
			if recentlyFinished == -1 {
				firstFinished = I
			}
			if recentlyFinished != -1 && firstFinished != recentlyFinished && recentlyFinished >= I {
				t.Error("peers do not seem to be getting unblocked in the correct order", recentlyFinished, I)
			}
			timeSinceUnblock := time.Since(recentUnblock)
			desiredTimeSinceUnblock := peerShareRateLimit * time.Duration(recentSize)
			if recentlyFinished != -1 && timeSinceUnblock*2 < desiredTimeSinceUnblock {
				t.Error("unblocker does not seem to be waiting long enough between unblocks", timeSinceUnblock, desiredTimeSinceUnblock)
			}
			longestAcceptableBlock := peerShareRateLimit * (time.Duration(recentSize)+500)
			if recentlyFinished != -1 && timeSinceUnblock > longestAcceptableBlock {
				// This check is only performed if recentSize is relatively
				// large, otherwise machine jitter and such could cause the
				// unblock to take a while.
				t.Error("unblocker seems to be waiting too long between unblocks", timeSinceUnblock, longestAcceptableBlock)
			}
			recentSize = fastrand.Intn(2e3)
			recentlyFinished = I
			recentUnblock = time.Now()
			sizeChan <- recentSize
		}(I)
	}
	wg.Wait()
}
