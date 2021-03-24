package renter

import (
	"strings"
	"testing"

	"gitlab.com/NebulousLabs/Sia/crypto"
)

// TestSkyfileFanout probes the fanout encoding.
func TestSkyfileFanout(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a renter for the tests
	rt, err := newRenterTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	t.Run("Reader", func(t *testing.T) { testSkyfileEncodeFanout_Reader(t, rt) })
}

// testSkyfileEncodeFanout_Reader probes generating the fanout from a reader
func testSkyfileEncodeFanout_Reader(t *testing.T, rt *renterTester) {
	// Create a file with N-of-M erasure coding and a non PlainText cipher type
	siaPath, rsc := testingFileParamsCustom(2, 3)
	file, err := rt.renter.createRenterTestFileWithParams(siaPath, rsc, crypto.TypeDefaultRenter)
	if err != nil {
		t.Fatal(err)
	}

	// Create a mock reader to the file on disk
	reader := strings.NewReader("this is fine")

	// Even though the file is not uploaded, we should be able to create the
	// fanout from the file on disk.
	//
	// Since we are using test data we don't care about the final result of the
	// fanout, we just are testing that the panics aren't triggered.
	_, err = skyfileEncodeFanoutFromReader(file, reader, false)
	if err != nil {
		t.Fatal(err)
	}

	// Create a file with 1-of-N erasure coding and a non PlainText cipher type
	siaPath, rsc = testingFileParamsCustom(1, 3)
	file, err = rt.renter.createRenterTestFileWithParams(siaPath, rsc, crypto.TypeDefaultRenter)
	if err != nil {
		t.Fatal(err)
	}

	// Create a mock reader to the file on disk
	reader = strings.NewReader("still fine")

	// Even though the file is not uploaded, we should be able to create the
	// fanout from the file on disk.
	//
	// Since we are using test data we don't care about the final result of the
	// fanout, we just are testing that the panics aren't triggered.
	_, err = skyfileEncodeFanoutFromReader(file, reader, true)
	if err != nil {
		t.Fatal(err)
	}
}
