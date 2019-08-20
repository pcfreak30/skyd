package transactionpool

// TODO: Need to write a sanity check within the transaction pool that checks
// that all of the tsets are complete and independent, because this is an
// assumption that we depend on during broadcast.

import (
	"time"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
)

// newPeerShare tracks the peers of the transaction pool and then sends new
// peers all pre-existing transactions in the transction pool.
type newPeerShare struct{
	*transactionPoolUtils
}

// threadedPollForPeers will repeatedly check the gateway for new peers.
func (nps *newPeerShare) threadedPollForPeers() {
	err := nps.tg.Add()
	if err != nil {
		return
	}
	defer nps.tg.Done()

	// Infinite loop scanning for new peers.
	knownPeers := make(map[modules.NetAddress]struct{})
	for {
		// Fetch the list of peers from the gateway.
		currentPeers := nps.gateway.Peers()
		newKnownPeers := make(map[modules.NetAddress]struct{})
		for _, peer := range currentPeers {
			newKnownPeers[peer.NetAddress] = struct{}{}
			_, exists := knownPeers[peer.NetAddress]
			if !exists {
				go nps.threadedSyncPeer(peer)
			}
		}
		knownPeers = newKnownPeers

		// Block until the next polling cycle.
		select {
		case <-nps.tg.StopChan():
			return
		case <-time.After(newPeerPollingFrequency):
			continue
		}
	}
}

// threadedSyncPeer will take a snapshot of the transaction pool and use that
// snapshot to determine which transactions need to be sent to the new peer.
func (nps *newPeerShare) threadedSyncPeer(peer modules.Peer) {
	err := nps.tg.Add()
	if err != nil {
		return
	}
	defer nps.tg.Done()

	// The connection time of the peer is used by the ratelimiter to determine
	// which peers should be allowed to receive transactions. The ratelimiter
	// ensures that a finite number of resources total are expended on syncing
	// peers, and earlier peers are given priority over more recent peers.
	timeConnected := time.Now().Unix()

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
	// TODO: Convert state complexity into an outbound complexity.
	remainingObjects := make(map[ObjectID]struct{})
	tp.mu.Lock()
	for oid, _ := range tp.knownObjects {
		remainingObjects[oid] = struct{}{}
	}
	tp.mu.Unlock()

	// Grab one object at a time and send the corresponding transaction set
	// until all objects have been sent.
	for len(remainingObjects) > 0 {
		// Block until the ratelimiter says this thread can send a transaction
		// set. The ratelimiter will return a channel which will be used to tell
		// the ratelimiter how big the send was.
		bytesToRelayFeedback, success := nps.staticPeerShareLimiter.callBlockForShareTSet(timeDiscovered)
		if !success {
			// The transaction pool was shut down before the ratelimit reached
			// this thread.
			return
		}

		// Check that the peer is still available in the gateway. If the peer is
		// no longer listed in the gateway, this thread can exit.
		//
		// TODO: When the gateway is able to do notifications, this can be
		// replaced by checking if the gateway has released a notification that
		// the peer is gone.
		currentPeers := nps.gateway.Peers()
		found := false
		for _, currentPeer := range currentPeers {
			if currentPeer.NetAddress == peer.NetAddress {
				found = true
				break
			}
		}
		if !found {
			return
		}

		// Loop through the set of remaining objects until a suitable
		// transaction set is found.
		//
		// TODO: Break this out into a helper function.
		var tset []types.Transaction
		for {
			// If there are no remaining objects, the peer syncing task has been
			// fully successful, and there is no more work to do.
			if len(remainingObjects) == 0 {
				bytesToRelayFeedback <- 0
				return
			}

			// Pick the next object to send.
			var nextObject ObjectID
			for obj, _ := range remainingObjects {
				nextObject = obj
				break
			}

			// Determine what transaction set to send based on the selected object.
			//
			// TODO: Replace this state complexity with outbound complexity.
			tp.mu.Lock()
			tsetID, exists := tp.knownObjects[nextObject]
			if exists {
				tset = tp.transactionSets[tsetID]
			}
			tp.mu.Unlock()
			// If the tset does not exist, this object is not needed anymore. Tell
			// the ratelimiter that no action was taken.
			if exists {
				break
			} else {
				delete(remainingObjects, nextObject)
				continue
			}
		}

		// If a corresponding transaction set does exist, remove all related
		// objects because they will all be sent along with the transaction set.
		oids := relatedObjectIDs(tset)
		for _, oid := range oids {
			delete(remainingObjects, oid)
		}

		// Relay the transaction using the repeat broadcast filter. The action
		// is a non-blocking call that will return the number of bytes that will
		// actually be relayed.
		//
		// TODO: Implement.
		// bytesToRelay, err := nps.staticRepeatBroadcastFilter.callRelayTransactionSet
		// bytesToRelayFeedback <- bytesToRelay
	}
}

// newNewPeerShare will return a newPeerShare object that is ready for use by
// the tpool.
func (tp *TransactionPool) newNewPeerShare() *newPeerShare {
	return &newPeerShare{
		transactionPoolUtils: tp.transactionPoolUtils,
	}
}
