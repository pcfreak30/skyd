package proto

import (
	"encoding/binary"
	"reflect"
	"testing"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
)

// swapRangeLength is a convenience method which encodes a uint64 length into a
// byte slice for testing.
func swapRangeLength(length uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, length)
	return b
}

func TestCalculateProofRanges(t *testing.T) {
	tests := []struct {
		desc       string
		numSectors uint64
		actions    []modules.LoopWriteAction
		exp        []crypto.ProofRange
	}{
		{
			desc:       "Append",
			numSectors: 2,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionAppend, Data: []byte{1, 2, 3}},
			},
			exp: []crypto.ProofRange{},
		},
		{
			desc:       "RSwap",
			numSectors: 3,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionSwap, A: 1, B: 2},
			},
			exp: []crypto.ProofRange{
				{Start: 1, End: 2},
				{Start: 2, End: 3},
			},
		},
		{
			desc:       "SwapRange",
			numSectors: 4,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionSwapRange, A: 0, B: 2, Data: swapRangeLength(2)},
			},
			exp: []crypto.ProofRange{
				{Start: 0, End: 1},
				{Start: 1, End: 2},
				{Start: 2, End: 3},
				{Start: 3, End: 4},
			},
		},
		{
			desc:       "Trim",
			numSectors: 3,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionTrim, A: 1},
			},
			exp: []crypto.ProofRange{
				{Start: 2, End: 3},
			},
		},
		{
			desc:       "AppendSwapTrimRSwap",
			numSectors: 12,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionAppend, Data: []byte{1, 2, 3}},
				{Type: modules.WriteActionSwap, A: 5, B: 12},
				{Type: modules.WriteActionTrim, A: 1},
				{Type: modules.WriteActionSwapRange, A: 2, B: 6, Data: swapRangeLength(4)},
			},
			exp: []crypto.ProofRange{
				{Start: 2, End: 3},
				{Start: 3, End: 4},
				{Start: 4, End: 5},
				{Start: 5, End: 6},
				{Start: 6, End: 7},
				{Start: 7, End: 8},
				{Start: 8, End: 9},
				{Start: 9, End: 10},
			},
		},
		{
			desc:       "SwapTrimSwapAppendAppend",
			numSectors: 12,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionSwap, A: 6, B: 11},
				{Type: modules.WriteActionTrim, A: 1},
				{Type: modules.WriteActionSwap, A: 7, B: 10},
				{Type: modules.WriteActionAppend, Data: []byte{1, 2, 3}},
				{Type: modules.WriteActionAppend, Data: []byte{4, 5, 6}},
			},
			exp: []crypto.ProofRange{
				{Start: 6, End: 7},
				{Start: 7, End: 8},
				{Start: 10, End: 11},
				{Start: 11, End: 12},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			res := calculateProofRanges(test.actions, test.numSectors)
			if !reflect.DeepEqual(res, test.exp) {
				t.Errorf("incorrect ranges: expected %v, got %v", test.exp, res)
			}
		})
	}
}

func TestModifyProofRanges(t *testing.T) {
	tests := []struct {
		desc        string
		numSectors  uint64
		actions     []modules.LoopWriteAction
		proofRanges []crypto.ProofRange
		exp         []crypto.ProofRange
	}{
		{
			desc:       "Append",
			numSectors: 2,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionAppend, Data: []byte{1, 2, 3}},
			},
			proofRanges: nil,
			exp: []crypto.ProofRange{
				{Start: 2, End: 3},
			},
		},
		{
			desc:       "Swap",
			numSectors: 3,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionSwap, A: 1, B: 2},
			},
			proofRanges: []crypto.ProofRange{
				{Start: 1, End: 2},
				{Start: 2, End: 3},
			},
			exp: []crypto.ProofRange{
				{Start: 1, End: 2},
				{Start: 2, End: 3},
			},
		},
		{
			desc:       "Trim",
			numSectors: 3,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionTrim, A: 1},
			},
			proofRanges: []crypto.ProofRange{
				{Start: 2, End: 3},
			},
			exp: []crypto.ProofRange{},
		},
		{
			desc:       "AppendSwapTrim",
			numSectors: 12,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionAppend, Data: []byte{1, 2, 3}},
				{Type: modules.WriteActionSwap, A: 5, B: 12},
				{Type: modules.WriteActionTrim, A: 1},
			},
			proofRanges: []crypto.ProofRange{
				{Start: 5, End: 6},
			},
			exp: []crypto.ProofRange{
				{Start: 5, End: 6},
			},
		},
		{
			desc:       "SwapTrimSwapAppendAppend",
			numSectors: 12,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionSwap, A: 6, B: 11},
				{Type: modules.WriteActionTrim, A: 1},
				{Type: modules.WriteActionSwap, A: 7, B: 10},
				{Type: modules.WriteActionAppend, Data: []byte{1, 2, 3}},
				{Type: modules.WriteActionAppend, Data: []byte{4, 5, 6}},
			},
			proofRanges: []crypto.ProofRange{
				{Start: 6, End: 7},
				{Start: 7, End: 8},
				{Start: 10, End: 11},
				{Start: 11, End: 12},
			},
			exp: []crypto.ProofRange{
				{Start: 6, End: 7},
				{Start: 7, End: 8},
				{Start: 10, End: 11},
				{Start: 11, End: 12},
				{Start: 12, End: 13},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			res := modifyProofRanges(test.proofRanges, test.actions, test.numSectors)
			if !reflect.DeepEqual(res, test.exp) {
				t.Errorf("incorrect modification: expected %v, got %v", test.exp, res)
			}
		})
	}
}

func TestModifyLeafHashes(t *testing.T) {
	tests := []struct {
		desc       string
		numSectors uint64
		actions    []modules.LoopWriteAction
		leaves     []crypto.Hash
		exp        []crypto.Hash
	}{
		{
			desc:       "Append",
			numSectors: 2,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionAppend, Data: []byte{1, 2, 3}},
			},
			leaves: nil,
			exp:    []crypto.Hash{crypto.MerkleRoot([]byte{1, 2, 3})},
		},
		{
			desc:       "Swap",
			numSectors: 3,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionSwap, A: 1, B: 2},
			},
			leaves: []crypto.Hash{{1}, {2}},
			exp:    []crypto.Hash{{2}, {1}},
		},
		{
			desc:       "RSwap",
			numSectors: 5,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionSwapRange, A: 1, B: 3, Data: swapRangeLength(2)},
			},
			leaves: []crypto.Hash{
				{1}, {2}, {3}, {4},
			},
			exp: []crypto.Hash{
				{3}, {4}, {1}, {2},
			},
		},
		{
			desc:       "Trim",
			numSectors: 3,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionTrim, A: 1},
			},
			leaves: []crypto.Hash{{1}},
			exp:    []crypto.Hash{},
		},
		{
			desc:       "AppendSwapTrim",
			numSectors: 12,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionAppend, Data: []byte{1, 2, 3}},
				{Type: modules.WriteActionSwap, A: 5, B: 12},
				{Type: modules.WriteActionTrim, A: 1},
			},
			leaves: []crypto.Hash{{1}},
			exp:    []crypto.Hash{crypto.MerkleRoot([]byte{1, 2, 3})},
		},
		{
			desc:       "AppendAppendRSwapTrim",
			numSectors: 12,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionAppend, Data: []byte{1, 2, 3}},
				{Type: modules.WriteActionAppend, Data: []byte{4, 5, 6}},
				{Type: modules.WriteActionSwapRange, A: 4, B: 11, Data: swapRangeLength(2)},
				{Type: modules.WriteActionTrim, A: 1},
			},
			leaves: []crypto.Hash{{1}, {2}},
			exp:    []crypto.Hash{crypto.MerkleRoot([]byte{1, 2, 3}), crypto.MerkleRoot(([]byte{4, 5, 6})), {1}},
		},
		{
			desc:       "SwapTrimSwapAppendAppend",
			numSectors: 12,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionSwap, A: 6, B: 11},
				{Type: modules.WriteActionTrim, A: 1},
				{Type: modules.WriteActionSwap, A: 7, B: 10},
				{Type: modules.WriteActionAppend, Data: []byte{1, 2, 3}},
				{Type: modules.WriteActionAppend, Data: []byte{4, 5, 6}},
			},
			leaves: []crypto.Hash{{1}, {2}, {3}, {4}},
			exp:    []crypto.Hash{{4}, {3}, {2}, crypto.MerkleRoot([]byte{1, 2, 3}), crypto.MerkleRoot([]byte{4, 5, 6})},
		},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			res := modifyLeaves(test.leaves, test.actions, test.numSectors)
			if !reflect.DeepEqual(res, test.exp) {
				t.Errorf("incorrect modification: expected %v, got %v", test.exp, res)
			}
		})
	}
}
