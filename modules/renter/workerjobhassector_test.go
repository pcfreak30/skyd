package renter

import (
	"testing"

	"gitlab.com/NebulousLabs/errors"
)

// TestHasSectorJobString verifies the String implemention of
// jobHasSectorResponse
func TestJobHasSectorResponseString(t *testing.T) {
	t.Parallel()

	// basic case
	hsr := jobHasSectorResponse{
		staticAvailables: []bool{true},
		staticErr:        nil,
		staticWorker:     &worker{staticHostPubKeyStr: "hostpubkey"},
	}
	if hsr.String() != "host: [hostpubkey] availables: [1] err: '<nil>'" {
		t.Fatal("unexpected string representation", hsr.String())
	}

	// multiple sector lookups
	hsr = jobHasSectorResponse{
		staticAvailables: []bool{true, false, false},
		staticErr:        nil,
		staticWorker:     &worker{staticHostPubKeyStr: "hostpubkey"},
	}
	if hsr.String() != "host: [hostpubkey] availables: [100] err: '<nil>'" {
		t.Fatal("unexpected string representation", hsr.String())
	}

	// error and nil availables
	hsr = jobHasSectorResponse{
		staticAvailables: nil,
		staticErr:        errors.New("errorstring"),
		staticWorker:     &worker{staticHostPubKeyStr: "hostpubkey"},
	}
	if hsr.String() != "host: [hostpubkey] availables: [] err: 'errorstring'" {
		t.Fatal("unexpected string representation", hsr.String())
	}

	// nil worker (even though this is always set in code)
	hsr = jobHasSectorResponse{
		staticAvailables: []bool{false},
		staticErr:        nil,
		staticWorker:     nil,
	}
	if hsr.String() != "host: [] availables: [0] err: '<nil>'" {
		t.Fatal("unexpected string representation", hsr.String())
	}
}
