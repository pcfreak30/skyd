package host

import (
	"container/heap"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
)

var (
	// pruneRefundsListFrequency is the frequency at which the host prunes the
	// refunds list which are kept in memory
	pruneRefundsListFrequency = build.Select(build.Var{
		Standard: 15 * time.Minute,
		Dev:      10 * time.Minute,
		Testing:  30 * time.Second,
	}).(time.Duration)

	// refundExpiry is the amount of time the hosts keeps track of the refund
	// for a certain program token.
	refundExpiry = build.Select(build.Var{
		Standard: 10 * time.Minute,
		Dev:      time.Minute,
		Testing:  10 * time.Second,
	}).(time.Duration)
)

type (
	// refundsList keeps track of refunds, allowing renters to query what was
	// refunded after using their MDMProgramToken.
	refundsList struct {
		refunds map[modules.MDMProgramToken]types.Currency
		tokens  tokenHeap
		mu      sync.Mutex
	}

	// tokenHeap is a min heap of tokens
	tokenHeap []*tokenEntry

	// tokenEntry is a helper struct that keeps track of when the token, and
	// this record of the refund, can be removed from the heap
	tokenEntry struct {
		token  modules.MDMProgramToken
		expiry time.Time
	}
)

// managedRefund returns the refund for the given program token and whether it
// was found in the refunds list or not.
func (rh *refundsList) managedRefund(t modules.MDMProgramToken) (types.Currency, bool) {
	rh.mu.Lock()
	defer rh.mu.Unlock()
	refund, exists := rh.refunds[t]
	return refund, exists
}

// managedRegisterRefund registers the given refund for the program token. It
// will also push the token alongside an expiry time on the heap so we are able
// to prune the refunds list periodically.
func (rh *refundsList) managedRegisterRefund(t modules.MDMProgramToken, r types.Currency) {
	rh.mu.Lock()
	defer rh.mu.Unlock()
	_, exists := rh.refunds[t]
	if exists {
		build.Critical("Refund already registered for given token")
	}
	rh.refunds[t] = r
	heap.Push(&rh.tokens, &tokenEntry{t, time.Now().Add(refundExpiry)})
}

// managedPruneRefundsList prunes the refund list by removing all entries that
// that have an expiry in the past. The refunds are only kept in memory for a
// certain amount of time as otherwise we would be leaking memory.
func (rh *refundsList) managedPruneRefundsList() {
	rh.mu.Lock()
	defer rh.mu.Unlock()

	now := time.Now()
	for rh.tokens.Len() > 0 {
		te := heap.Pop(&rh.tokens).(*tokenEntry)
		if now.Before(te.expiry) {
			heap.Push(&rh.tokens, te)
			break
		}
		delete(rh.refunds, te.token)
	}
}

// Implementation of heap.Interface for tokenHeap.
func (th tokenHeap) Len() int { return len(th) }
func (th tokenHeap) Less(i, j int) bool {
	return th[i].expiry.Before(th[j].expiry)
}
func (th tokenHeap) Swap(i, j int) { th[i], th[j] = th[j], th[i] }
func (th *tokenHeap) Push(x interface{}) {
	t := x.(*tokenEntry)
	*th = append(*th, t)
}
func (th *tokenHeap) Pop() interface{} {
	old := *th
	n := len(old)
	pt := old[n-1]
	*th = old[0 : n-1]
	return pt
}

// staticNewMDMProgramToken is a helper function that returns a new random
// program token
func (h *Host) staticNewMDMProgramToken() modules.MDMProgramToken {
	var token modules.MDMProgramToken
	fastrand.Read(token[:])
	return token
}

// threadedPruneRefundsList will prune the refunds the host keeps in memory.
// These refunds can be consulted by the renter to see if the host is being
// honest with his ephemeral account balance.
//
// Note: threadgroup counter must be inside for loop. If not, calling 'Flush' on
// the threadgroup would deadlock.
func (h *Host) threadedPruneRefundsList() {
	for {
		func() {
			if err := h.tg.Add(); err != nil {
				return
			}
			defer h.tg.Done()
			h.staticRefundsList.managedPruneRefundsList()
		}()

		// Block until next cycle.
		select {
		case <-h.tg.StopChan():
			return
		case <-time.After(pruneRefundsListFrequency):
			continue
		}
	}
}
