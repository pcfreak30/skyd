package transactionpool

// TODO: Tell Marcin to test his watchdog by doing a transaction chain, mining
// it into a block, then reorging the block with a longer chain of empty blocks,
// and then ensuring that the transaction can still be broadcast to different
// types of miners (brand new miner, miner with existing tpool).
//
// TODO: Need to figure out how the broadcast happens. This subsystem shouldn't
// be reponsible for network communication, should call out to some external
// subsystem. In the short term, that's probably the core.
//
// TODO: Need to hook into the block rewinder and add transactions to the repeat
// broadcast filter based on transactions that get added to the tpool because
// they were in a block that got reverted.
//
// TODO: Handle removing transactions from the rbf upon eviction from the tpool
// (both because blocks are found and becuase of transaction age issues, and any
// other eviction issues).
//
// TODO: Add a sanity check that the transactions in the transaction pool match
// the transactions in the rbf.

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
	transactionHistory map[types.TransactionID]map[modules.NetAddress]struct{}
	mu                 sync.Mutex

	*transactionPoolUtils
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
		}
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
