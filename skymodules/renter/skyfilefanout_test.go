package renter

import (
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

	t.Run("Writer", func(t *testing.T) { testSkyfileEncodeFanout_Writer(t, rt) })
}

// testSkyfileEncodeFanout_Writer probes generating the fanout with a
// fanoutWriter.
func testSkyfileEncodeFanout_Writer(t *testing.T, rt *renterTester) {
	// Create a file with N-of-M erasure coding and a non PlainText cipher type
	siaPath, rsc := testingFileParamsCustom(2, 3)
	file, err := rt.renter.createRenterTestFileWithParams(siaPath, rsc, crypto.TypeDefaultRenter)
	if err != nil {
		t.Fatal(err)
	}

	// Even though the file is not uploaded, we should be able to create the
	// fanout from the file on disk.
	//
	// Since we are using test data we don't care about the final result of the
	// fanout, we just are testing that the panics aren't triggered.
	fw := newFanoutWriter(file, false)
	_, err = fw.Write([]byte("this is fine"))
	if err != nil {
		t.Fatal(err)
	}

	// Create a file with 1-of-N erasure coding and a non PlainText cipher type
	siaPath, rsc = testingFileParamsCustom(1, 3)
	file, err = rt.renter.createRenterTestFileWithParams(siaPath, rsc, crypto.TypeDefaultRenter)
	if err != nil {
		t.Fatal(err)
	}

	// Even though the file is not uploaded, we should be able to create the
	// fanout from the file on disk.
	//
	// Since we are using test data we don't care about the final result of the
	// fanout, we just are testing that the panics aren't triggered.
	fw = newFanoutWriter(file, false)
	_, err = fw.Write([]byte("still fine"))
	if err != nil {
		t.Fatal(err)
	}
}
