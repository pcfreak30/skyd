package transactionpool

// TODO: Need to write a sanity check within the transaction pool that checks
// that all of the tsets are complete and independent, because this is an
// assumption that we depend on during broadcast.

import (
	"time"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
)

// transactionSetSize will return the Sia encoding size of a transaction set.
func transactionSetSize(set []types.Transaction) (size int) {
	for _, txn := range set {
		size += txn.MarshalSiaSize()
	}
	return size
}

// threadedPollForPeers will repeatedly check the gateway for new peers.
func (tp *TransactionPool) threadedPollForPeers() {
	err := tp.tg.Add()
	if err != nil {
		return
	}
	defer tp.tg.Done()

	// Infinite loop scanning for new peers.
	knownPeers := make(map[modules.NetAddress]struct{})
	for {
		// Fetch the list of peers from the gateway.
		currentPeers := tp.gateway.Peers()
		newKnownPeers := make(map[modules.NetAddress]struct{})
		for _, peer := range currentPeers {
			newKnownPeers[peer.NetAddress] = struct{}{}
			_, exists := knownPeers[peer.NetAddress]
			if !exists {
				go tp.threadedSyncPeer(peer)
			}
		}
		knownPeers = newKnownPeers

		// Block until the next polling cycle.
		select {
		case <-tp.tg.StopChan():
			return
		case <-time.After(newPeerPollingFrequency):
			continue
		}
	}
}

// threadedSyncPeer will take a snapshot of the transaction pool and use that
// snapshot to determine which transactions need to be sent to the new peer.
func (tp *TransactionPool) threadedSyncPeer(peer modules.Peer) {
	err := tp.tg.Add()
	if err != nil {
		return
	}
	defer tp.tg.Done()

	// The discovery time of the peer is used by the ratelimiter to determine
	// which peers should be allowed to receive transactions. The ratelimiter
	// ensures that a finite number of resources total are expended on syncing
	// peers, and older peers are given priority over newer peers.
	timeDiscovered := time.Now().Unix()

	// Take a snapshot (remainingObjects) of the current transaction pool's
	// object IDs. Object IDs are used to determine which transaction sets have
	// not been sent to the peer yet. When a transaction set is broadcast to a
	// new peer, all of the objects in that transaction set are removed from the
	// snapshot.
	//
	// Object IDs are used instead of transaction set IDs because the
	// transaction sets are constantly changing and adjusting as blocks are
	// found and as new transactions are propagated. Using transaction IDs would
	// be most efficient, however as of implementing this, there was no mapping
	// in the transaction pool from transaction ID to transaction set, only a
	// mapping from object ID to transaction set. For that reason, object IDs
	// are used instead.
	//
	// STATE COMPLEXITY: Reading state directly from the core subsystem.
	remainingObjects := make(map[ObjectID]struct{})
	tp.mu.Lock()
	for oid, _ := range tp.knownObjects {
		remainingObjects[oid] = struct{}{}
	}
	tp.mu.Unlock()

	// Grab one object at a time and send the corresponding transaction set
	// until all objects have been sent.
	//
	// NOTE: Currently the thread will return a failure / no data sent to the
	// ratelimiter if nothing valid is found on the first attempt. This can be
	// computationally expensive if there are hundreds of objects that no longer
	// exist in the tpool which need to be sorted through. That could be sped up
	// significantly by waiting to return anything to the ratelimiter until
	// either a valid tset has been found, or until all remaining objects have
	// been checked / discarded.
	for len(remainingObjects) > 0 {
		// Block until the ratelimiter says this thread can send a transaction
		// set. The ratelimiter will return a channel which will be used to tell
		// the ratelimiter how big the send was.
		bytesRelayedFeedback, success := tp.staticPeerShareLimiter.callBlockForShareTSet(timeDiscovered)
		if !success {
			// The transaction pool was shut down before the ratelimit reached
			// this thread.
			return
		}

		// TODO: After unblocking, there should probably be a check that the
		// peer is still a peer in the gateway.

		// Pick an object to send next.
		var nextObject ObjectID
		for obj, _ := range remainingObjects {
			nextObject = obj
			break
		}

		// Determine what transaction set to send based on the selected object.
		//
		// STATE COMPLEXITY: Reading state directly from the core subsystem.
		//
		// TODO: Replace this state complexity with a method call?
		tp.mu.Lock()
		var tset []types.Transaction
		tsetID, exists := tp.knownObjects[nextObject]
		if exists {
			tset = tp.transactionSets[tsetID]
		}
		tp.mu.Unlock()
		// If the tset does not exist, this object is not needed anymore. Tell
		// the ratelimiter that no action was taken.
		if !exists {
			delete(remainingObjects, nextObject)
			bytesRelayedFeedback <- 0
			continue
		}

		// If a corresponding transaction set does exist, remove all related
		// objects. Do this before pruning the transaction set.
		oids := relatedObjectIDs(tset)
		for _, oid := range oids {
			delete(remainingObjects, oid)
		}

		// Relay the transaction using the repeat broadcast filter. The action
		// is a non-blocking call that will return the number of bytes that will
		// actually be relayed.
		//
		// TODO: Implement.
		// bytesRelayed, err := tp.callRelayTransactionSet
		// bytesRelayedFeedback <- bytesRelayed
	}
}
