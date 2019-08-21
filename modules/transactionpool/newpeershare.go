package transactionpool

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

// staticNextTSet will return the next tranasction set that should be sent to a
// peer given the set of objects that the peer has not yet been told about.
func (nps *newPeerShare) staticNextTSet(remainingObjects map[ObjectID]struct{}) (map[ObjectID]struct{}, []types.Transaction, bool) {
	// Loop through the set of remaining objects until a suitable transaction
	// set is found.
	for len(remainingObjects) > 0 {
		// Pick the next object to send.
		var nextObject ObjectID
		for obj, _ := range remainingObjects {
			nextObject = obj
			break
		}

		// Determine what transaction set to send based on the selected object.
		transactionSet, exists := nps.staticCore.callTSetByObjectID(nextObject)
		if !exists {
			delete(remainingObjects, nextObject)
			continue
		}

		// If a corresponding transaction set does exist, remove all related
		// objects because they will all be sent along with the transaction set.
		oids := relatedObjectIDs(tset)
		for _, oid := range oids {
			delete(remainingObjects, oid)
		}
		return remainingObjects, transactionSet, true
	}

	// There are no more objects to try, nothing to return.
	return nil, nil, false
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

	// Grab a list of objects that are currently present in the transaction
	// pool. Object IDs are used to determine which transaction sets have not
	// been sent to the peer yet. When a transaction set is broadcast to a new
	// peer, all of the objects in that transaction set are removed from the
	// snapshot.
	//
	// Object IDs are used instead of transaction set IDs because the
	// transaction sets are constantly changing and adjusting as blocks are
	// found and as new transactions are propagated. Using transaction IDs would
	// be most efficient, however as of implementing this, there was no mapping
	// in the transaction pool from transaction ID to transaction set, only a
	// mapping from object ID to transaction set. For that reason, object IDs
	// are used instead.
	remainingObjects := nps.staticCore.callRemainingObjectsList()

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
		// NOTE: Convention requires that a resource be closed out immediately
		// after acquiring the resource. In this case, the
		// 'bytesToRelayFeedback' channel is something that has a required
		// action, and should techincally be closed immediately in some sort of
		// defer here. However, we want a defer that operates on just the scope
		// of this iteration of the loop, and there was no clean way I could
		// find to accomplish this. Instead, convention is violated in favor of
		// readability.

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
			bytesToRelayFeedback <- 0
			return
		}

		// Grab the next transaction set to send to the peer. If there is no
		// next transaction set, all work is complete and the thread can exit.
		var tset []types.Transaction
		var exists bool
		remainingObjects, tset, exists = nps.staticNextTSet(remainingObjects)
		if !exists {
			bytesToRelayFeedback <- 0
			return
		}

		// Relay the transaction using the repeat broadcast filter. The action
		// is a non-blocking call that will return the number of bytes that will
		// actually be relayed.
		bytesToRelay := nps.staticRepeatBroadcastFilter.callRelayTransactionSet(tset, []modules.Peer{peer})
		bytesToRelayFeedback <- bytesToRelay
	}
}

// newNewPeerShare will return a newPeerShare object that is ready for use by
// the tpool.
func (tp *TransactionPool) newNewPeerShare() *newPeerShare {
	// Check that the dependencies are in place.
	//
	// TODO: Swap out the check for staticCore with a check for the transaction
	// manager once the transaction management is broken out into its own
	// subsystem.
	if tp.staticCore == nil {
		panic("cannot launch new peer share subsystem without the core")
	}
	if tp.staticPeerShareLimiter == nil {
		panic("cannot launch new peer share subsystem without the peer share limiter")
	}
	if tp.staticRepeatBroadcastFilter == nil {
		panic("cannot launch new peer share subsystem without the repeat broadcast filter")
	}

	// Create the new peer share subsystem and launch its background thread.
	nps := &newPeerShare{
		transactionPoolUtils: tp.transactionPoolUtils,
	}
	go nps.threadedPollForNewPeers()
	return nps
}
