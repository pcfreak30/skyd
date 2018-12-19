package consensus

import (
	"testing"

	"gitlab.com/NebulousLabs/Sia/types"
)

// TestUnitValidateBlock runs a series of unit tests for ValidateBlock.
func TestUnitValidateBlock(t *testing.T) {
	tests := []struct {
		block        types.Block
		now          types.Timestamp
		minTimestamp types.Timestamp
		errWant      error
		msg          string
	}{
		{
			block: types.Block{
				Timestamp: types.Timestamp(4),
			},
			minTimestamp: types.Timestamp(5),
			errWant:      errEarlyTimestamp,
			msg:          "ValidateBlock should reject blocks with timestamps that are too early",
		},
		{
			block: types.Block{
				Transactions: []types.Transaction{{
					ArbitraryData: [][]byte{make([]byte, types.BlockSizeLimit)},
				}},
			},
			errWant: errLargeBlock,
			msg:     "ValidateBlock should reject excessively large blocks",
		},
		{
			block: types.Block{
				Timestamp: types.Timestamp(50) + types.ExtremeFutureThreshold + 1,
			},
			now:     types.Timestamp(50),
			errWant: errExtremeFutureTimestamp,
			msg:     "ValidateBlock should reject blocks timestamped in the extreme future",
		},
	}
	for _, tt := range tests {
		err := validateBlock(tt.block, tt.block.ID(), tt.minTimestamp, types.RootDepth, 0, tt.now)
		if err != tt.errWant {
			t.Errorf("%s: got %v, want %v", tt.msg, err, tt.errWant)
		}
	}
}

// TestCheckMinerPayouts probes the checkMinerPayouts function.
func TestCheckMinerPayouts(t *testing.T) {
	// All tests are done at height = 0.
	coinbase := types.CalculateCoinbase(0)

	// Create a block with a single valid payout.
	b := types.Block{
		MinerPayouts: []types.SiacoinOutput{
			{Value: coinbase},
		},
	}
	if !checkMinerPayouts(b, 0) {
		t.Error("payouts evaluated incorrectly when there is only one payout.")
	}

	// Try a block with an incorrect payout.
	b = types.Block{
		MinerPayouts: []types.SiacoinOutput{
			{Value: coinbase.Sub(types.NewCurrency64(1))},
		},
	}
	if checkMinerPayouts(b, 0) {
		t.Error("payouts evaluated incorrectly when there is a too-small payout")
	}

	// Try a block with 2 payouts.
	b = types.Block{
		MinerPayouts: []types.SiacoinOutput{
			{Value: coinbase.Sub(types.NewCurrency64(1))},
			{Value: types.NewCurrency64(1)},
		},
	}
	if !checkMinerPayouts(b, 0) {
		t.Error("payouts evaluated incorrectly when there are 2 payouts")
	}

	// Try a block with 2 payouts that are too large.
	b = types.Block{
		MinerPayouts: []types.SiacoinOutput{
			{Value: coinbase},
			{Value: coinbase},
		},
	}
	if checkMinerPayouts(b, 0) {
		t.Error("payouts evaluated incorrectly when there are two large payouts")
	}

	// Create a block with an empty payout.
	b = types.Block{
		MinerPayouts: []types.SiacoinOutput{
			{Value: coinbase},
			{},
		},
	}
	if checkMinerPayouts(b, 0) {
		t.Error("payouts evaluated incorrectly when there is only one payout.")
	}
}

// TestCheckTarget probes the checkTarget function.
func TestCheckTarget(t *testing.T) {
	var b types.Block
	lowTarget := types.RootDepth
	highTarget := types.Target{}
	sameTarget := types.Target(b.ID())

	if !checkTarget(b, b.ID(), lowTarget) {
		t.Error("CheckTarget failed for a low target")
	}
	if checkTarget(b, b.ID(), highTarget) {
		t.Error("CheckTarget passed for a high target")
	}
	if !checkTarget(b, b.ID(), sameTarget) {
		t.Error("CheckTarget failed for a same target")
	}
}
