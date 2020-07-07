package renter

import (
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/siatest/dependencies"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
)

// TestExecuteProgramBandwidthLimit verifies the host db is updated accordingly
// when the host spent an unexpected bandwidth amount.
func TestExecuteProgramBandwidthLimit(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// create a new worker.
	deps := dependencies.NewDependencyInterruptUpdateHostInteractionsAfterExecuteProgram()
	wt, err := newWorkerTesterCustomDependency(t.Name(), deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := wt.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	// get host from hostdb
	host, ok, err := wt.renter.hostDB.Host(wt.host.PublicKey())
	if !ok {
		t.Fatal("host not found in hostdb")
	}
	if err != nil {
		t.Fatal(err)
	}

	// verify recent failed- and successful interactions is zero
	if host.RecentFailedInteractions != 0 || host.RecentSuccessfulInteractions != 0 {
		t.Fatal("expected renter to have no interactions with host")
	}

	// wait until we have a valid pricetable
	err = build.Retry(100, 100*time.Millisecond, func() error {
		if !wt.worker.staticPriceTable().staticValid() {
			return errors.New("price table not updated yet")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// create a dummy program
	pt := wt.staticPriceTable().staticPriceTable
	pb := modules.NewProgramBuilder(&pt, 0)
	pb.AddHasSectorInstruction(crypto.Hash{})
	p, data := pb.Program()
	cost, _, _ := pb.Cost(true)
	ulBandwidth, dlBandwidth := new(jobHasSector).callExpectedBandwidth()

	deps.Disable()
	interactions := 10
	failures := 0

	// run it a couple of times and randomly make it fail
	for i := 0; i < interactions; i++ {
		if fastrand.Intn(2) == 0 {
			deps.Fail()
			failures++
		}
		_, _, err = wt.worker.managedExecuteProgram(p, data, types.FileContractID{}, cost, ulBandwidth, dlBandwidth)
		if err != nil {
			t.Fatal(err)
		}
		deps.Disable()
	}

	// get host from hostdb
	host, ok, err = wt.renter.hostDB.Host(wt.host.PublicKey())
	if !ok {
		t.Fatal("host not found in hostdb")
	}
	if err != nil {
		t.Fatal(err)
	}

	// host interactions should have been incremented according, note we don't
	// do a strict comparison on the successful interactions, however we do on
	// the failures
	if host.RecentSuccessfulInteractions < float64(interactions-failures) {
		t.Errorf("Successful interactions should be (at least) %v but was %v", interactions-failures, host.RecentSuccessfulInteractions)
	}
	if host.RecentFailedInteractions != float64(failures) {
		t.Errorf("Failed interactions should be %v but was %v", failures, host.RecentFailedInteractions)
	}
}

// TestExecuteProgramUsedBandwidth verifies the bandwidth used by executing
// various MDM programs on the host
func TestExecuteProgramUsedBandwidth(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// create a new worker tester
	wt, err := newWorkerTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := wt.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	// wait until we have a valid pricetable
	err = build.Retry(100, 100*time.Millisecond, func() error {
		if !wt.worker.staticPriceTable().staticValid() {
			return errors.New("price table not updated yet")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("HasSector", func(t *testing.T) {
		testExecuteProgramUsedBandwidthHasSector(t, wt)
	})

	t.Run("ReadSector", func(t *testing.T) {
		testExecuteProgramUsedBandwidthReadSector(t, wt)
	})
}

// testExecuteProgramUsedBandwidthHasSector verifies the bandwidth consumed by a
// HasSector program
func testExecuteProgramUsedBandwidthHasSector(t *testing.T, wt *workerTester) {
	w := wt.worker

	// create a dummy program
	pt := wt.staticPriceTable().staticPriceTable
	pb := modules.NewProgramBuilder(&pt, 0)
	pb.AddHasSectorInstruction(crypto.Hash{})
	p, data := pb.Program()
	cost, _, _ := pb.Cost(true)
	ulBandwidth, dlBandwidth := new(jobHasSector).callExpectedBandwidth()

	// execute it
	_, limit, err := w.managedExecuteProgram(p, data, types.FileContractID{}, cost, ulBandwidth, dlBandwidth)
	if err != nil {
		t.Fatal(err)
	}

	// ensure bandwidth is as we expected
	expectedDownload := uint64(2920)
	if limit.Downloaded() != expectedDownload {
		t.Errorf("Expected HasSector program to consume %v download bandwidth, instead it consumed %v", expectedDownload, limit.Downloaded())
	}

	expectedUpload := uint64(1460)
	if limit.Uploaded() != expectedUpload {
		t.Errorf("Expected HasSector program to consume %v upload bandwidth, instead it consumed %v", expectedUpload, limit.Uploaded())
	}

	// log the bandwidth used
	t.Logf("Used bandwidth (has sector program): %v down, %v up", limit.Downloaded(), limit.Uploaded())
}

// testExecuteProgramUsedBandwidthReadSector verifies the bandwidth consumed by
// a ReadSector program
func testExecuteProgramUsedBandwidthReadSector(t *testing.T, wt *workerTester) {
	w := wt.worker

	sectorData := fastrand.Bytes(int(modules.SectorSize))
	sectorRoot := crypto.MerkleRoot(sectorData)
	err := wt.host.AddSector(sectorRoot, sectorData)
	if err != nil {
		t.Fatal("could not add sector to host")
	}

	// create a dummy program
	pt := wt.staticPriceTable().staticPriceTable
	pb := modules.NewProgramBuilder(&pt, 0)
	pb.AddReadSectorInstruction(modules.SectorSize, 0, sectorRoot, true)
	p, data := pb.Program()
	cost, _, _ := pb.Cost(true)

	// execute it
	ulBandwidth, dlBandwidth := new(jobReadSector).callExpectedBandwidth()
	_, limit, err := w.managedExecuteProgram(p, data, types.FileContractID{}, cost, ulBandwidth, dlBandwidth)
	if err != nil {
		t.Fatal(err)
	}

	// ensure bandwidth is as we expected
	expectedDownload := uint64(5840)
	if limit.Downloaded() != expectedDownload {
		t.Errorf("Expected ReadSector program to consume %v download bandwidth, instead it consumed %v", expectedDownload, limit.Downloaded())
	}

	expectedUpload := uint64(1460)
	if limit.Uploaded() != expectedUpload {
		t.Errorf("Expected ReadSector program to consume %v upload bandwidth, instead it consumed %v", expectedUpload, limit.Uploaded())
	}

	// log the bandwidth used
	t.Logf("Used bandwidth (read sector program): %v down, %v up", limit.Downloaded(), limit.Uploaded())
}
