package proto

import (
	"reflect"
	"testing"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
)

func TestCalculateProofRanges(t *testing.T) {
	leavesPerSector := modules.SectorSize / crypto.SegmentSize
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
			desc:       "Swap",
			numSectors: 3,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionSwap, A: 1, B: 2},
			},
			exp: []crypto.ProofRange{
				{Start: 1 * leavesPerSector, End: 2 * leavesPerSector},
				{Start: 2 * leavesPerSector, End: 3 * leavesPerSector},
			},
		},
		{
			desc:       "Trim",
			numSectors: 3,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionTrim, A: 1},
			},
			exp: []crypto.ProofRange{
				{Start: 2 * leavesPerSector, End: 3 * leavesPerSector},
			},
		},
		{
			desc:       "Update",
			numSectors: 4,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionUpdate, A: 0, B: 0, Data: make([]byte, crypto.SegmentSize)},
				{Type: modules.WriteActionUpdate, A: 1, B: crypto.SegmentSize, Data: make([]byte, crypto.SegmentSize)},

				{Type: modules.WriteActionUpdate, A: 0, B: 1, Data: make([]byte, crypto.SegmentSize)},
				{Type: modules.WriteActionUpdate, A: 1, B: 1 + crypto.SegmentSize, Data: make([]byte, crypto.SegmentSize)},

				{Type: modules.WriteActionUpdate, A: 2, B: 0, Data: make([]byte, crypto.SegmentSize+1)},
				{Type: modules.WriteActionUpdate, A: 3, B: crypto.SegmentSize, Data: make([]byte, crypto.SegmentSize+1)},
			},
			exp: []crypto.ProofRange{
				{Start: 0, End: 1},
				{Start: 1, End: 2},
				{Start: 1*leavesPerSector + 1, End: 1*leavesPerSector + 2},
				{Start: 1*leavesPerSector + 2, End: 1*leavesPerSector + 3},
				{Start: 2 * leavesPerSector, End: 2*leavesPerSector + 1},
				{Start: 2*leavesPerSector + 1, End: 2*leavesPerSector + 2},
				{Start: 3*leavesPerSector + 1, End: 3*leavesPerSector + 2},
				{Start: 3*leavesPerSector + 2, End: 3*leavesPerSector + 3},
			},
		},
		{
			desc:       "AppendSwapTrimUpdate",
			numSectors: 12,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionAppend, Data: []byte{1, 2, 3}},
				{Type: modules.WriteActionSwap, A: 5, B: 12},
				{Type: modules.WriteActionTrim, A: 1},
				{Type: modules.WriteActionUpdate, A: 1, B: 1, Data: make([]byte, 2*crypto.SegmentSize)},
			},
			exp: []crypto.ProofRange{
				{Start: 1 * leavesPerSector, End: 1*leavesPerSector + 1},
				{Start: 1*leavesPerSector + 1, End: 1*leavesPerSector + 2},
				{Start: 1*leavesPerSector + 2, End: 1*leavesPerSector + 3},
				{Start: 5 * leavesPerSector, End: 6 * leavesPerSector},
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
				{Type: modules.WriteActionUpdate, A: 11, B: 0, Data: []byte{3, 2, 1}},
				{Type: modules.WriteActionAppend, Data: []byte{4, 5, 6}},
			},
			exp: []crypto.ProofRange{
				{Start: 6 * leavesPerSector, End: 7 * leavesPerSector},
				{Start: 7 * leavesPerSector, End: 8 * leavesPerSector},
				{Start: 10 * leavesPerSector, End: 11 * leavesPerSector},
				{Start: 11 * leavesPerSector, End: 12 * leavesPerSector},
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
	leavesPerSector := modules.SectorSize / crypto.SegmentSize
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
				{Start: 2 * leavesPerSector, End: 3 * leavesPerSector},
			},
		},
		{
			desc:       "Swap",
			numSectors: 3,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionSwap, A: 1, B: 2},
			},
			proofRanges: []crypto.ProofRange{
				{Start: 1 * leavesPerSector, End: 2 * leavesPerSector},
				{Start: 2 * leavesPerSector, End: 3 * leavesPerSector},
			},
			exp: []crypto.ProofRange{
				{Start: 1 * leavesPerSector, End: 2 * leavesPerSector},
				{Start: 2 * leavesPerSector, End: 3 * leavesPerSector},
			},
		},
		{
			desc:       "Trim",
			numSectors: 3,
			actions: []modules.LoopWriteAction{
				{Type: modules.WriteActionTrim, A: 1},
			},
			proofRanges: []crypto.ProofRange{
				{Start: 2 * leavesPerSector, End: 3 * leavesPerSector},
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
				{Start: 5 * leavesPerSector, End: 6 * leavesPerSector},
			},
			exp: []crypto.ProofRange{
				{Start: 5 * leavesPerSector, End: 6 * leavesPerSector},
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
				{Start: 6 * leavesPerSector, End: 7 * leavesPerSector},
				{Start: 7 * leavesPerSector, End: 8 * leavesPerSector},
				{Start: 10 * leavesPerSector, End: 11 * leavesPerSector},
				{Start: 11 * leavesPerSector, End: 12 * leavesPerSector},
			},
			exp: []crypto.ProofRange{
				{Start: 6 * leavesPerSector, End: 7 * leavesPerSector},
				{Start: 7 * leavesPerSector, End: 8 * leavesPerSector},
				{Start: 10 * leavesPerSector, End: 11 * leavesPerSector},
				{Start: 11 * leavesPerSector, End: 12 * leavesPerSector},
				{Start: 12 * leavesPerSector, End: 13 * leavesPerSector},
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
