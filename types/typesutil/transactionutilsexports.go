package typesutil

import (
	"gitlab.com/NebulousLabs/Sia/types"
)

// TransactionSetSize will return the Sia encoding size of a transaction set.
func TransactionSetSize(set []types.Transaction) (size int) {
	for _, txn := range set {
		size += txn.MarshalSiaSize()
	}
	return size
}
