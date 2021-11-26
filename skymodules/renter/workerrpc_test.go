package renter

import (
	"context"
	"strings"
	"testing"

	"gitlab.com/NebulousLabs/encoding"
	"gitlab.com/NebulousLabs/fastrand"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// TestMDMBandwidthCost is a unit test for mdmBandwidthCost.
func TestMDMBandwidthCost(t *testing.T) {
	pt := newDefaultPriceTable()
	pt.UploadBandwidthCost = types.SiacoinPrecision
	pt.DownloadBandwidthCost = types.SiacoinPrecision

	// compute bandwidth cost for 100 bytes of upload and 200 of download.
	ul := uint64(100)
	dl := uint64(200)
	bandwidthCost, refund := mdmBandwidthCost(pt, ul, dl)

	// compute the expected value.
	expectedCost := modules.MDMBandwidthCost(pt, ul, dl)
	if !bandwidthCost.Equals(expectedCost) {
		t.Fatal("mismatch", bandwidthCost, expectedCost)
	}

	// check for a positiv refund - less bandwidth was used than expected.
	ref := refund(ul-10, dl-20)
	expectedRef := bandwidthCost.Sub(modules.MDMBandwidthCost(pt, ul-10, dl-20))
	if !ref.Equals(expectedRef) {
		t.Log(bandwidthCost)
		t.Fatal("mismatch", ref, expectedRef)
	}

	// check for a negative refund - more bandwidth was used than expected.
	ref = refund(ul+10, dl+20)
	if !ref.IsZero() {
		t.Fatal("should be zero", ref)
	}

	// exact match
	ref = refund(ul, dl)
	if !ref.IsZero() {
		t.Fatal("should be zero", ref)
	}
}

// TestUseHostBlockHeight verifies we use the host's blockheight.
func TestUseHostBlockHeight(t *testing.T) {
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
	w := wt.worker

	// manually corrupt the price table's host blockheight
	wpt := w.staticPriceTable()
	hbh := wpt.staticPriceTable.HostBlockHeight // save host blockheight
	var pt modules.RPCPriceTable
	err = encoding.Unmarshal(encoding.Marshal(wpt.staticPriceTable), &pt)
	if err != nil {
		t.Fatal(err)
	}
	pt.HostBlockHeight += 1e3

	wptc := new(workerPriceTable)
	wptc.staticExpiryTime = wpt.staticExpiryTime
	wptc.staticUpdateTime = wpt.staticUpdateTime
	wptc.staticPriceTable = pt
	w.staticSetPriceTable(wptc)

	// create a dummy program
	pb := modules.NewProgramBuilder(&pt, 0)
	pb.AddHasSectorInstruction(crypto.Hash{})
	p, data := pb.Program()
	cost, _, _ := pb.Cost(true)
	jhs := new(jobHasSector)
	jhs.staticSectors = []crypto.Hash{{1, 2, 3}}
	ulBandwidth, dlBandwidth := jhs.callExpectedBandwidth()
	bandwidthCost, bandwidthRefund := mdmBandwidthCost(pt, ulBandwidth, dlBandwidth)
	cost = cost.Add(bandwidthCost)

	// execute the program
	_, _, err = w.managedExecuteProgram(p, data, types.FileContractID{}, categoryDownload, cost, bandwidthRefund)
	if err == nil || !strings.Contains(err.Error(), "ephemeral account withdrawal message expires too far into the future") {
		t.Fatal("Unexpected error", err)
	}

	// revert the corruption to assert success
	wpt = w.staticPriceTable()
	err = encoding.Unmarshal(encoding.Marshal(wpt.staticPriceTable), &pt)
	if err != nil {
		t.Fatal(err)
	}
	pt.HostBlockHeight = hbh

	wptc = new(workerPriceTable)
	wptc.staticExpiryTime = wpt.staticExpiryTime
	wptc.staticUpdateTime = wpt.staticUpdateTime
	wptc.staticPriceTable = pt
	w.staticSetPriceTable(wptc)

	// execute the program
	_, _, err = w.managedExecuteProgram(p, data, types.FileContractID{}, categoryDownload, cost, bandwidthRefund)
	if err != nil {
		t.Fatal("Unexpected error", err)
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

	jhs := new(jobHasSector)
	jhs.staticSectors = []crypto.Hash{{1, 2, 3}}
	ulBandwidth, dlBandwidth := jhs.callExpectedBandwidth()

	bandwidthCost, bandwidthRefund := mdmBandwidthCost(pt, ulBandwidth, dlBandwidth)
	cost = cost.Add(bandwidthCost)

	// execute it
	_, limit, err := w.managedExecuteProgram(p, data, types.FileContractID{}, categoryDownload, cost, bandwidthRefund)
	if err != nil {
		t.Fatal(err)
	}

	// ensure bandwidth is as we expected
	expectedDownload := uint64(1460)
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

	// create job metadata
	jobMetadata := jobReadMetadata{
		staticWorker:           w,
		staticSectorRoot:       sectorRoot,
		staticSpendingCategory: categoryDownload,
	}

	// create read sector job
	readSectorRespChan := make(chan *jobReadResponse)
	jrs := w.newJobReadSector(context.Background(), w.staticJobReadQueue, readSectorRespChan, jobMetadata, sectorRoot, 0, modules.SectorSize)

	ulBandwidth, dlBandwidth := jrs.callExpectedBandwidth()
	bandwidthCost, bandwidthRefund := mdmBandwidthCost(pt, ulBandwidth, dlBandwidth)
	cost = cost.Add(bandwidthCost)

	// execute it
	_, limit, err := w.managedExecuteProgram(p, data, types.FileContractID{}, categoryDownload, cost, bandwidthRefund)
	if err != nil {
		t.Fatal(err)
	}

	// ensure bandwidth is as we expected
	expectedDownload := uint64(4380)
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
