package transactionpool

// newpeershare.go is a file that ensures all of the transactions that we have
// are getting shared with all of our peers. If a new peer connects to the
// transaction pool, we should send them all of our transactions to ensure that
// they have a similar view of the network that we do. The expectation is that
// the peer will do the same, meaning that the transaction pools should quickly
// converge on having the same transactions. This helps everyone to pick
// reasonable fee rates for outgoing transactions, and also helps overall
// transaction propagation.
//
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
	for oid := range tp.knownObjects {
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
		sizeFeedback, success := tp.managedBlockForRateLimit(timeDiscovered)
		if !success {
			// The transaction pool was shut down before the ratelimit reached
			// this thread.
			return
		}

		// TODO: After unblocking, there should probably be a check that the
		// peer is still a peer in the gateway.

		// Pick an object to send next.
		var nextObject ObjectID
		for obj := range remainingObjects {
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
			sizeFeedback <- 0
			continue
		}

		// If a corresponding transaction set does exist, remove all related
		// objects. Do this before pruning the transaction set.
		oids := relatedObjectIDs(tset)
		for _, oid := range oids {
			delete(remainingObjects, oid)
		}

		// Use the repeat broadcast filter to reduce
		//
		// TODO: Implement.
		// tset = tp.rbf.Filter(tset)

		// Get the size of the remaining transactions.
		tsetSize := transactionSetSize(tset)
		sizeFeedback <- tsetSize

		// Send the transaction.
		//
		// TODO: Implement.
		// tp.Broadcast("relay", peer, tset)
	}
}

//////////
// Below this line is the old implementation. Will be discarded.
//////////

// managedBlockUntilOnlineAndSynced will block until siad is both online and
// synced. The function will return false if shutdown occurs before both
// conditions are met.
func (tp *TransactionPool) managedBlockUntilOnlineAndSynced() bool {
	// Infinite loop to keep checking online and synced status.
	for {
		// Three conditions need to be satisfied. The consensus package needs to
		// be synced, which can be checked by calling out to the consensus
		// package. We also need to check that the tpool package is synced,
		// meaning that the tpool has the most recent consensus change. And
		// finally, we need the gateway to assert that siad is online.
		consensusSynced := tp.consensusSet.Synced()
		tp.mu.Lock()
		tpoolSynced := tp.synced
		tp.mu.Unlock()
		online := tp.gateway.Online()
		if consensusSynced && tpoolSynced && online {
			return true
		}

		// Sleep for a bit before trying again, exit early if the transaction
		// pool is shutting down.
		select {
		case <-tp.tg.StopChan():
			return false
		case <-time.After(onlineSyncedLoopSleepTime):
			continue
		}
	}
}

// managedBroadcastTpoolToNewPeers takes a map of peers that have already
// received the full transaction pool and will use that to determine which peers
// have not yet received the full transaction pool. From there, the full
// transactoin pool will be broadcast to the new peers, and a new map will be
// returned which contains all of the new peers, and all of the old peers that
// had already been broadcasted to that are still online, but will not contain
// the old peers that were broadcasted to that we are no longer connected to.
func (tp *TransactionPool) managedBroadcastTpoolToNewPeers(finishedPeers map[modules.NetAddress]struct{}) map[modules.NetAddress]struct{} {
	// Get the current list of peers from the gateway. Scan through the
	// peers and determine whether it's a new unfinished peer or if it's a
	// finished peer.
	//
	// We use the map 'newFinishedPeers' so that we clear out all of the
	// peers from 'finishedPeers' that are actually no longer our peers.
	// After buildling the newFinishedPeers map, the finishedPeers map is
	// rotated out.
	currentPeers := tp.gateway.Peers()
	newFinishedPeers := make(map[modules.NetAddress]struct{})
	var unfinishedPeers []modules.Peer
	for _, peer := range currentPeers {
		_, exists := finishedPeers[peer.NetAddress]
		if exists {
			newFinishedPeers[peer.NetAddress] = struct{}{}
		} else {
			unfinishedPeers = append(unfinishedPeers, peer)
		}
	}
	finishedPeers = newFinishedPeers

	// Get the set of transactions that will be sent to the new peers.
	tp.mu.Lock()
	transactionSetIDs := make([]TransactionSetID, 0, len(tp.transactionSets))
	transactionSets := make([][]types.Transaction, 0, len(tp.transactionSets))
	for id, set := range tp.transactionSets {
		transactionSetIDs = append(transactionSetIDs, id)
		transactionSets = append(transactionSets, set)
	}
	tp.mu.Unlock()

	// Broadcast each transaction set to each peer, self-ratelimiting to avoid
	// slamming our peers with transactions.
	//
	// TODO: Change the broadcast to send the sets from highest fee rate to
	// lowest fee rate.
	for i, set := range transactionSets {
		tp.mu.Lock()
		_, exists := tp.transactionSets[transactionSetIDs[i]]
		tp.mu.Unlock()
		if !exists {
			continue
		}

		// Determine the amount of time that needs to pass between each
		// broadcast according to the ratelimit.
		setSize := transactionSetSize(set)
		sleepBetweenBroadcasts := time.Duration(setSize) * newPeerBroadcastRateLimit
		nextSetChan := time.After(sleepBetweenBroadcasts)

		// Perform the broadcast and then sleep before continuing.
		tp.gateway.Broadcast("RelayTransactionSet", set, unfinishedPeers)
		select {
		case <-tp.tg.StopChan():
			return finishedPeers
		case <-nextSetChan:
		}
	}

	// Add all of the peers to the finished set of peers and return the updated
	// set.
	for _, peer := range unfinishedPeers {
		finishedPeers[peer.NetAddress] = struct{}{}
	}
	return finishedPeers
}

// threadedShareTransactions will perpetually loop, polling the gateway for new
// peers. If a new peer is found, that peer will be sent the full list of
// transactions in the tpool.
func (tp *TransactionPool) threadedShareTransactions() {
	err := tp.tg.Add()
	if err != nil {
		return
	}
	defer tp.tg.Done()

	// A map of peers that we are currently connected to and have also finished
	// syncing.
	finishedPeers := make(map[modules.NetAddress]struct{})

	// Infinite loop to keep checking for new peers.
	for {
		// Check for a stop signal.
		select {
		case <-tp.tg.StopChan():
			return
		default:
		}
		checkAgainChan := time.After(newPeerPollingFrequency)

		// Block until we are online and synced.
		if !tp.managedBlockUntilOnlineAndSynced() {
			return
		}

		// Broadcast the transaction pool contents to all new peers.
		finishedPeers = tp.managedBroadcastTpoolToNewPeers(finishedPeers)

		// Block until the timer says it is time to check again to avoid hogging
		// the CPU.
		select {
		case <-checkAgainChan:
			continue
		case <-tp.tg.StopChan():
			return
		}
	}
}
