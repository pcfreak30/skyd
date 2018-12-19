package consensus

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sort"

	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/consensus/database"
	"gitlab.com/NebulousLabs/Sia/types"
)

var (
	errBadMinerPayouts        = errors.New("miner payout sum does not equal block subsidy")
	errEarlyTimestamp         = errors.New("block timestamp is too early")
	errExtremeFutureTimestamp = errors.New("block timestamp too far in future, discarded")
	errFutureTimestamp        = errors.New("block timestamp too far in future, but saved for later use")
	errLargeBlock             = errors.New("block is too large to be accepted")
)

// checkMinerPayouts compares a block's miner payouts to the block's subsidy and
// returns true if they are equal.
func checkMinerPayouts(b types.Block, height types.BlockHeight) bool {
	// Add up the payouts and check that all values are legal.
	var payoutSum types.Currency
	for _, payout := range b.MinerPayouts {
		if payout.Value.IsZero() {
			return false
		}
		payoutSum = payoutSum.Add(payout.Value)
	}
	return b.CalculateSubsidy(height).Equals(payoutSum)
}

// checkTarget returns true if the block's ID meets the given target.
func checkTarget(b types.Block, id types.BlockID, target types.Target) bool {
	return bytes.Compare(target[:], id[:]) >= 0
}

// minimumValidChildTimestamp returns the earliest timestamp that a child node
// can have while still being valid. See section 'Block Timestamps' in
// Consensus.md.
func minimumValidChildTimestamp(tx database.Tx, b *database.Block) types.Timestamp {
	// Get the previous MedianTimestampWindow timestamps.
	windowTimes := make(types.TimestampSlice, types.MedianTimestampWindow)
	windowTimes[0] = b.Timestamp
	parent := b.ParentID
	for i := uint64(1); i < types.MedianTimestampWindow; i++ {
		// If the genesis block is 'parent', use the genesis block timestamp
		// for all remaining times.
		if parent == (types.BlockID{}) {
			windowTimes[i] = windowTimes[i-1]
			continue
		}

		// Get the next parent ID and timestamp
		parentBlock, _ := tx.Block(parent)
		parent = parentBlock.ParentID
		windowTimes[i] = parentBlock.Timestamp
	}
	sort.Sort(windowTimes)

	// Return the median of the sorted timestamps.
	return windowTimes[len(windowTimes)/2]
}

// ValidateBlock validates a block against a minimum timestamp, a block target,
// and a block height. Returns nil if the block is valid and an appropriate
// error otherwise.
func validateBlock(b types.Block, id types.BlockID, minTimestamp types.Timestamp, target types.Target, height types.BlockHeight, currentTime types.Timestamp) error {
	// Check that the timestamp is not too far in the past to be acceptable.
	if minTimestamp > b.Timestamp {
		return errEarlyTimestamp
	}

	// Check that the nonce is a legal nonce.
	if height >= types.ASICHardforkHeight && binary.LittleEndian.Uint64(b.Nonce[:])%types.ASICHardforkFactor != 0 {
		return errors.New("block does not meet nonce requirements")
	}
	// Check that the target of the new block is sufficient.
	if !checkTarget(b, id, target) {
		return modules.ErrBlockUnsolved
	}

	// Check that the block is below the size limit.
	blockSize := len(encoding.Marshal(b))
	if uint64(blockSize) > types.BlockSizeLimit {
		return errLargeBlock
	}

	// Check if the block is in the extreme future. We make a distinction between
	// future and extreme future because there is an assumption that by the time
	// the extreme future arrives, this block will no longer be a part of the
	// longest fork because it will have been ignored by all of the miners.
	if b.Timestamp > currentTime+types.ExtremeFutureThreshold {
		return errExtremeFutureTimestamp
	}

	// Verify that the miner payouts are valid.
	if !checkMinerPayouts(b, height) {
		return errBadMinerPayouts
	}

	// Check if the block is in the near future, but too far to be acceptable.
	// This is the last check because it's an expensive check, and not worth
	// performing if the payouts are incorrect.
	if b.Timestamp > currentTime+types.FutureThreshold {
		return errFutureTimestamp
	}
	return nil
}
