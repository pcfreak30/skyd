package transactionpool

import (
	"sync"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
)

// repeatBroadcastFilter keeps a map of all the transactions that have been sent
// to peers previously, ensuring that transactions are not sent to the peers
// multiple times.
type repeatBroadcastFilter struct {
	// transactionHistory contains a list of every peer that has received every
	// transaction, which can be looked up on a per-transaction basis.
	transactionHistory        map[types.TransactionID]map[modules.NetAddress]struct{}
	transactionsSinceEviction uint64
	mu                        sync.Mutex

	*transactionPoolUtils
}

// managedPrune will delete transactions from the repeat broadcast filter that
// are no longer in the transaction pool.
func (rbf *repeatBroadcastFilter) managedPrune() {
	// Get the list of transactions from the transaction pool.
	txns := rbf.staticCore.TransactionList()

	// Reset the counter for transactions since eviction, and then perform the
	// eviction. To perform the eviction, a copy of the existing transaction
	// history map is made, and then the rbf's history map is replaced with a
	// new one. For each transaction in the transaction pool, the history for
	// that transaction is copied into the new map.
	//
	// This method ensures that only transactions which still exist in the
	// transaction pool will survive the eviction.
	rbf.mu.Lock()
	rbf.transactionsSinceEviction = 0
	oldHistory := rbf.transactionHistory
	rbf.transactionHistory = make(map[types.TransactionID]map[modules.NetAddress]struct{})
	for _, txn := range txns {
		id := txn.ID()
		history, exists := oldHistory[id]
		if exists {
			rbf.transactionHistory[id] = history
		}
	}
	rbf.mu.Unlock()
}

// callRelayTransactionSet will run the input tset through the broadcast filter,
// queue up a broadcast of the transaction set, and return the total number of
// bytes (across all provided peers) that will be sent over the network.
func (rbf *repeatBroadcastFilter) callRelayTransactionSet(tset []types.Transaction, peers []modules.Peer) int {
	rbf.mu.Lock()
	defer rbf.mu.Unlock()

	// Cache the txids to minimize the number of times that they need to be
	// computed.
	txids := make([]types.TransactionID, len(tset), len(tset))
	for i, txn := range tset {
		txids[i] = txn.ID()
	}

	// Ensure a peer map exists for every transaction.
	for _, txid := range txids {
		_, exists := rbf.transactionHistory[txid]
		if !exists {
			rbf.transactionHistory[txid] = make(map[modules.NetAddress]struct{})
		}
	}

	// Filter the transaction set for each peer and then broadcast what remains.
	var totalSize int
	for _, peer := range peers {
		// Check whether this peer has received this transaction before. If no,
		// update the transaction's peer map to reflect that it has been sent to
		// the peer and add the transaction to the list of transactions that
		// need to be sent to the peer.
		var peerTxnList []types.Transaction
		for i, txid := range txids {
			// We know the txnMap exists because we just created it above.
			txnMap := rbf.transactionHistory[txid]
			_, exists2 := txnMap[peer.NetAddress]
			if !exists2 {
				// Action is only needed if the peer hasn't seen the transaction
				// before. We need to queue this transaction to be sent to the
				// peer, and then update the txnMap to reflect that the peer has
				// been sent the transaction previously.
				peerTxnList = append(peerTxnList, tset[i])
				txnMap[peer.NetAddress] = struct{}{}
				rbf.transactionHistory[txid] = txnMap
				rbf.transactionsSinceEviction++
			}
		}

		// Quit if there are no transactions to send to the peer.
		if len(peerTxnList) == 0 {
			continue
		}

		// Send the filtered set of transactions to the peer. Tally up the total
		// number of bytes that will be relayed.
		//
		// NOTE: techincally this isn't the exact number of bytes sent over the
		// wire because we aren't accounting for things like protocol overhead,
		// but this is close enough for the system to behave how the user would
		// expect.
		totalSize += types.TransactionSetSize(peerTxnList)
		go rbf.gateway.Broadcast("RelayTransactionSet", peerTxnList, []modules.Peer{peer})
	}

	// Create a thread to perform an eviction if necessary.
	if rbf.transactionsSinceEviction > repeatBroadcastFilterEvictionFrequency {
		go rbf.managedPrune()
	}

	return totalSize
}

// callUnconditionalBroadcast will unconditionally broadcast a transaction set
// to all peers. The filter that is typically applied will be ignored, hoewver
// the transactions will be added to the filter for all peers.
func (rbf *repeatBroadcastFilter) callUnconditionalBroadcast(tset []types.Transaction) {
	// Have the broadcast run right away.
	peers := rbf.gateway.Peers()
	go rbf.gateway.Broadcast("RelayTransactionSet", tset, peers)

	// Fill out the history to indicate that all peers have received all of
	// these transactions.
	rbf.mu.Lock()
	defer rbf.mu.Unlock()
	for _, txn := range tset {
		txid := txn.ID()
		_, exists := rbf.transactionHistory[txid]
		if !exists {
			rbf.transactionHistory[txid] = make(map[modules.NetAddress]struct{})
		}
		for _, peer := range peers {
			rbf.transactionHistory[txid][peer.NetAddress] = struct{}{}
			rbf.transactionsSinceEviction++
		}
	}

	// Create a thread to perform an eviction if necessary.
	if rbf.transactionsSinceEviction > repeatBroadcastFilterEvictionFrequency {
		go rbf.managedPrune()
	}
}

// newRepeatBroadcastFilter will return a new repeat broadcast filter that is
// ready for use by the tpool.
func (tp *TransactionPool) newRepeatBroadcastFilter() *repeatBroadcastFilter {
	// The repeat broadcast filter doesn't have any subsystem dependencies,
	// nothing to check.

	// Create the repeat broadcast filter.
	rbf := &repeatBroadcastFilter{
		transactionHistory: make(map[types.TransactionID]map[modules.NetAddress]struct{}),

		transactionPoolUtils: tp.transactionPoolUtils,
	}
	return rbf
}
