package host

import (
	"reflect"
	"testing"

	"gitlab.com/NebulousLabs/merkletree"
)

// TestModifyLeafRanges tests all the edge cases of modifyLeafRanges.
func TestModifyLeafRanges(t *testing.T) {
	tests := []struct {
		in  []merkletree.LeafRange
		out []merkletree.LeafRange
	}{
		{
			in:  []merkletree.LeafRange{},
			out: []merkletree.LeafRange{},
		},
		{
			in: []merkletree.LeafRange{
				{Start: 0, End: 1},
			},
			out: []merkletree.LeafRange{
				{Start: 0, End: 1},
			},
		},
		{
			in: []merkletree.LeafRange{
				{Start: 1, End: 2},
				{Start: 0, End: 1},
			},
			out: []merkletree.LeafRange{
				{Start: 0, End: 1},
				{Start: 1, End: 2},
			},
		},
		{
			in: []merkletree.LeafRange{
				{Start: 2, End: 3},
				{Start: 1, End: 2},
				{Start: 2, End: 3},
				{Start: 1, End: 2},
			},
			out: []merkletree.LeafRange{
				{Start: 1, End: 2},
				{Start: 2, End: 3},
			},
		},
		{
			in: []merkletree.LeafRange{
				{Start: 0, End: 2},
				{Start: 1, End: 3},
			},
			out: []merkletree.LeafRange{
				{Start: 0, End: 1},
				{Start: 1, End: 2},
				{Start: 2, End: 3},
			},
		},
		{
			in: []merkletree.LeafRange{
				{Start: 2, End: 3},
				{Start: 0, End: 2},
				{Start: 1, End: 3},
			},
			out: []merkletree.LeafRange{
				{Start: 0, End: 1},
				{Start: 1, End: 2},
				{Start: 2, End: 3},
			},
		},
		{
			in: []merkletree.LeafRange{
				{Start: 0, End: 10},
				{Start: 2, End: 3},
				{Start: 5, End: 7},
			},
			out: []merkletree.LeafRange{
				{Start: 0, End: 2},
				{Start: 2, End: 3},
				{Start: 3, End: 5},
				{Start: 5, End: 7},
				{Start: 7, End: 10},
			},
		},
		{
			in: []merkletree.LeafRange{
				{Start: 0, End: 5},
				{Start: 1, End: 6},
				{Start: 2, End: 7},
				{Start: 3, End: 8},
				{Start: 4, End: 9},
				{Start: 5, End: 10},
			},
			out: []merkletree.LeafRange{
				{Start: 0, End: 1},
				{Start: 1, End: 2},
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
			in: []merkletree.LeafRange{
				{Start: 0, End: 5},
				{Start: 2, End: 2},
			},
			out: []merkletree.LeafRange{
				{Start: 0, End: 2},
				{Start: 2, End: 2},
				{Start: 2, End: 5},
			},
		},
	}

	for _, test := range tests {
		if out := modifyLeafRanges(test.in); !reflect.DeepEqual(out, test.out) {
			t.Fatalf("expected %v but got %v", test.out, out)
		}
	}
}
