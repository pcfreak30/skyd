package transactionpool

// TODO: Need to figure out how the broadcast happens. This subsystem shouldn't
// be reponsible for network communication, should call out to some external
// subsystem. In the short term, that's probably the core.

import (
	"gitlab.com/NebulousLabs/Sia/types"
)

// repeatBroadcastFilter keeps a map of all the transactions that have been sent
// to peers previously, ensuring that transactions are not sent to the peers
// multiple times.
type repeatBroadcastFilter struct {
	*transactionPoolUtils
}

// transactionSetSize will return the Sia encoding size of a transaction set.
//
// TODO: This might be better moved to the types package.
func transactionSetSize(set []types.Transaction) (size int) {
	for _, txn := range set {
		size += txn.MarshalSiaSize()
	}
	return size
}

// callRelayTransactionSet will run the input tset through the broadcast filter,
// queue up a broadcast of the transaction set, and return the total number of
// bytes (across all provided peers) that will be sent over the network.
func (rbf *repeatBroadcastFilter) callRelayTransactionSet(tset []types.Transaction, []modules.Peer) int {
	// TODO
}

// newRepeatBroadcastFilter will return a new repeat broadcast filter that is
// ready for use by the tpool.
func (tp *TransactionPool) newRepeatBroadcastFilter() *repeatBroadcastFilter {
	// Check for dependencies.
	//
	// TODO: Will know as we implement.

	rbf := &repeatBroadcastFilter{
		transactionPoolUtils: tp.transactionPoolUtils,
	}
	return rbf
}
