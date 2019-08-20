package transactionpool

import (
	"gitlab.com/NebulousLabs/Sia/types"
)

// transactionSetSize will return the Sia encoding size of a transaction set.
func transactionSetSize(set []types.Transaction) (size int) {
	for _, txn := range set {
		size += txn.MarshalSiaSize()
	}
	return size
}
