package transactionpool

import (
	"encoding/json"

	bolt "github.com/coreos/bbolt"
	"gitlab.com/NebulousLabs/errors"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
)

// database.go contains objects related to the layout of the transaction pool's
// database, as well as getters and setters. Logic for interacting with the
// database can be found in persist.go

// Buckets in the database.
var (
	// bucketBlockHeight holds the most recent block height seen by the
	// transaction pool.
	bucketBlockHeight = []byte("BlockHeight")

	// bucketConfirmedTransactions holds the ids of every transaction that has
	// been confirmed on the blockchain.
	bucketConfirmedTransactions = []byte("ConfirmedTransactions")

	// bucketTransactionSets stores each transaction set currently present in
	// the transaction pool. Its contents are identical to tp.transactionSets,
	// except that they are only updated once per block.
	bucketTransactionSets = []byte("TransactionSets")

	// bucketTransactionHeights stores the "seen height" of each transaction
	// currently present in the transaction pool. Its contents are identical to
	// tp.transactionHeights, except that they are only updated once per block.
	bucketTransactionHeights = []byte("TransactionHeights")

	// bucketFeeMedian stores all of the persist data relating to the fee
	// median.
	bucketFeeMedian = []byte("FeeMedian")

	// bucketRecentConsensusChange holds the most recent consensus change seen
	// by the transaction pool.
	bucketRecentConsensusChange = []byte("RecentConsensusChange")
)

// Explicitly named fields in the database.
var (
	// fieldBlockHeight is the field in bucketBlockHeight that holds the value of
	// the most recent block height.
	fieldBlockHeight = []byte("BlockHeight")

	// fieldFeeMedian is the fee median persist data stored in a fee median
	// field.
	fieldFeeMedian = []byte("FeeMedian")

	// fieldRecentBlockID is used to store the id of the most recent block seen
	// by the transaction pool.
	fieldRecentBlockID = []byte("RecentBlockID")

	// fieldRecentConsensusChange is the field in bucketRecentConsensusChange
	// that holds the value of the most recent consensus change.
	fieldRecentConsensusChange = []byte("RecentConsensusChange")
)

// Errors relating to the database.
var (
	// errNilConsensusChange is returned if there is no consensus change in the
	// database.
	errNilConsensusChange = errors.New("no consensus change found")

	// errNilFeeMedian is the message returned if a database does not find fee
	// median persistence.
	errNilFeeMedian = errors.New("no fee median found")

	// errNilRecentBlock is returned if there is no data stored in
	// fieldRecentBlockID.
	errNilRecentBlock = errors.New("no recent block found in the database")
)

// Complex objects that get stored in database fields.
type (
	// medianPersist is the json object that gets stored in the database so that
	// the transaction pool can persist its block based fee estimations.
	medianPersist struct {
		RecentMedians   []types.Currency
		RecentMedianFee types.Currency
	}
)

// deleteTransaction deletes a transaction from the list of confirmed
// transactions.
func (tp *TransactionPool) deleteTransaction(tx *bolt.Tx, id types.TransactionID) error {
	return tx.Bucket(bucketConfirmedTransactions).Delete(id[:])
}

// getBlockHeight returns the most recent block height from the database.
func (tp *TransactionPool) getBlockHeight(tx *bolt.Tx) (bh types.BlockHeight, err error) {
	err = encoding.Unmarshal(tx.Bucket(bucketBlockHeight).Get(fieldBlockHeight), &bh)
	return
}

// getFeeMedian will get the fee median struct stored in the database.
func (tp *TransactionPool) getFeeMedian(tx *bolt.Tx) (medianPersist, error) {
	medianBytes := tp.dbTx.Bucket(bucketFeeMedian).Get(fieldFeeMedian)
	if medianBytes == nil {
		return medianPersist{}, errNilFeeMedian
	}

	var mp medianPersist
	err := json.Unmarshal(medianBytes, &mp)
	if err != nil {
		return medianPersist{}, build.ExtendErr("unable to unmarshal median data:", err)
	}
	return mp, nil
}

// getRecentBlockID will fetch the most recent block id and most recent parent
// id from the database.
func (tp *TransactionPool) getRecentBlockID(tx *bolt.Tx) (recentID types.BlockID, err error) {
	idBytes := tx.Bucket(bucketRecentConsensusChange).Get(fieldRecentBlockID)
	if idBytes == nil {
		return types.BlockID{}, errNilRecentBlock
	}
	copy(recentID[:], idBytes[:])
	if recentID == (types.BlockID{}) {
		return types.BlockID{}, errNilRecentBlock
	}
	return recentID, nil
}

// getRecentConsensusChange returns the most recent consensus change from the
// database.
func (tp *TransactionPool) getRecentConsensusChange(tx *bolt.Tx) (cc modules.ConsensusChangeID, err error) {
	ccBytes := tx.Bucket(bucketRecentConsensusChange).Get(fieldRecentConsensusChange)
	if ccBytes == nil {
		return modules.ConsensusChangeID{}, errNilConsensusChange
	}
	copy(cc[:], ccBytes)
	return cc, nil
}

// getTransactionSets returns the current set of transaction sets in the database.
func (tp *TransactionPool) getTransactionSets(tx *bolt.Tx) (map[TransactionSetID][]types.Transaction, error) {
	b, err := tx.CreateBucketIfNotExists(bucketTransactionSets)
	if err != nil {
		return nil, err
	}
	sets := make(map[TransactionSetID][]types.Transaction)
	err = b.ForEach(func(k, v []byte) error {
		var id TransactionSetID
		copy(id[:], k)
		var set []types.Transaction
		if err := encoding.Unmarshal(v, &set); err != nil {
			return err
		}
		sets[id] = set
		return nil
	})
	return sets, err
}

// getTransactionHeights returns the current set of transaction heights in the database.
func (tp *TransactionPool) getTransactionHeights(tx *bolt.Tx) (map[types.TransactionID]types.BlockHeight, error) {
	b, err := tx.CreateBucketIfNotExists(bucketTransactionHeights)
	if err != nil {
		return nil, err
	}
	heights := make(map[types.TransactionID]types.BlockHeight)
	err = b.ForEach(func(k, v []byte) error {
		var id types.TransactionID
		copy(id[:], k)
		var height types.BlockHeight
		if err := encoding.Unmarshal(v, &height); err != nil {
			return err
		}
		heights[id] = height
		return nil
	})
	return heights, err
}

// putBlockHeight updates the transaction pool's block height.
func (tp *TransactionPool) putBlockHeight(tx *bolt.Tx, height types.BlockHeight) error {
	tp.blockHeight = height
	return tx.Bucket(bucketBlockHeight).Put(fieldBlockHeight, encoding.Marshal(height))
}

// putFeeMedian puts a median fees object into the database.
func (tp *TransactionPool) putFeeMedian(tx *bolt.Tx, mp medianPersist) error {
	objBytes, err := json.Marshal(mp)
	if err != nil {
		return err
	}
	return tx.Bucket(bucketFeeMedian).Put(fieldFeeMedian, objBytes)
}

// putRecentBlockID will store the most recent block id and the parent id of
// that block in the database.
func (tp *TransactionPool) putRecentBlockID(tx *bolt.Tx, recentID types.BlockID) error {
	return tx.Bucket(bucketRecentConsensusChange).Put(fieldRecentBlockID, recentID[:])
}

// putRecentConsensusChange updates the most recent consensus change seen by
// the transaction pool.
func (tp *TransactionPool) putRecentConsensusChange(tx *bolt.Tx, cc modules.ConsensusChangeID) error {
	return tx.Bucket(bucketRecentConsensusChange).Put(fieldRecentConsensusChange, cc[:])
}

// putTransaction adds a transaction to the list of confirmed transactions.
func (tp *TransactionPool) putTransaction(tx *bolt.Tx, id types.TransactionID) error {
	return tx.Bucket(bucketConfirmedTransactions).Put(id[:], []byte{})
}

// putTransactionSets stores the current set of transaction sets in the database.
func (tp *TransactionPool) putTransactionSets(tx *bolt.Tx, sets map[TransactionSetID][]types.Transaction) error {
	_ = tx.DeleteBucket(bucketTransactionSets)
	b, err := tx.CreateBucket(bucketTransactionSets)
	if err != nil {
		return err
	}
	for id, set := range sets {
		if err := b.Put(id[:], encoding.Marshal(set)); err != nil {
			return err
		}
	}
	return nil
}

// putTransactionHeights stores the current set of transaction heights in the database.
func (tp *TransactionPool) putTransactionHeights(tx *bolt.Tx, heights map[types.TransactionID]types.BlockHeight) error {
	_ = tx.DeleteBucket(bucketTransactionHeights)
	b, err := tx.CreateBucket(bucketTransactionHeights)
	if err != nil {
		return err
	}
	for id, height := range heights {
		if err := b.Put(id[:], encoding.Marshal(height)); err != nil {
			return err
		}
	}
	return nil
}
