package renter

import (
	"testing"

	"gitlab.com/NebulousLabs/errors"
)

// TestJobReadString verifies the String implemention of jobReadResponse
func TestJobReadString(t *testing.T) {
	t.Parallel()

	// basic case
	jrr := jobReadResponse{
		staticErr:  nil,
		staticData: nil,
	}
	if jrr.String() != "read: [0] err: '<nil>'" {
		t.Fatal("unexpected string representation", jrr.String())
	}

	// data set
	jrr = jobReadResponse{
		staticErr:  nil,
		staticData: make([]byte, 100),
	}
	if jrr.String() != "read: [100] err: '<nil>'" {
		t.Fatal("unexpected string representation", jrr.String())
	}

	// error set
	jrr = jobReadResponse{
		staticErr:  errors.New("errorstring"),
		staticData: nil,
	}
	if jrr.String() != "read: [0] err: 'errorstring'" {
		t.Fatal("unexpected string representation", jrr.String())
	}
}
