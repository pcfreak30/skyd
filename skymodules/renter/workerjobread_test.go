package renter

import (
	"context"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// TestJobExpectedJobTime is a small unit test that verifies the result of
// 'callExpectedJobTime' on the jobReadQueue
func TestJobExpectedJobTime(t *testing.T) {
	t.Parallel()

	dur80MS := 80 * time.Millisecond
	dur120MS := 120 * time.Millisecond

	// randTimeMS returns a random duration between 40 and 80ms
	randTimeMS := func() time.Duration {
		return time.Duration(fastrand.Intn(40)+80) * time.Millisecond
	}

	w := new(worker)
	w.initJobReadQueue(NewJobReadStats())
	jrq := w.staticJobReadQueue
	for _, readLength := range []uint64{1 << 16, 1 << 20, 1 << 24} {
		// update metrics couple of times, due to the decay the estimate might
		// dip below the 80ms threshold after one or two jobs.
		for i := 0; i < 10; i++ {
			jrq.staticStats.callUpdateJobTimeMetrics(readLength, randTimeMS())
		}
		// update the jobqueue a bunch of times with random read times between
		// 80 and 120ms and assert the expected job time keeps returning a value
		// between those boundaries
		for i := 0; i < 1000; i++ {
			randJobTime := time.Duration(fastrand.Intn(40)+80) * time.Millisecond
			jrq.staticStats.callUpdateJobTimeMetrics(readLength, randJobTime)
			ejt := jrq.staticStats.callExpectedJobTime(readLength)
			if ejt < dur80MS || ejt > dur120MS {
				t.Error("unexpected", ejt, i)
			}
		}
	}
}

// TestJobReadMetadata verifies the job metadata is set on the job read response
func TestJobReadMetadata(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	wt, err := newWorkerTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := wt.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	w := wt.worker

	// allow the worker some time to fetch a PT and fund its EA
	err = build.Retry(600, 100*time.Millisecond, func() error {
		if w.staticAccount.managedMinExpectedBalance().IsZero() {
			return errors.New("account not funded yet")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// add sector data to the host
	sectorData := fastrand.Bytes(int(modules.SectorSize))
	sectorRoot := crypto.MerkleRoot(sectorData)
	err = wt.host.AddSector(sectorRoot, sectorData)
	if err != nil {
		t.Fatal(err)
	}

	// add job to the worker
	ctx := context.Background()
	responseChan := make(chan *jobReadResponse)

	jrs := &jobReadSector{
		jobRead: jobRead{
			staticResponseChan: responseChan,
			staticLength:       modules.SectorSize,

			jobGeneric: jobGeneric{
				staticCtx:   ctx,
				staticQueue: w.staticJobReadQueue,
				staticMetadata: jobReadMetadata{
					// set metadata, set it to something different than the
					// sector root to ensure the response contains the sector
					// given in the metadata
					staticSectorRoot:       crypto.Hash{1, 2, 3},
					staticSpendingCategory: categoryDownload,
					staticWorker:           w,
				},
			},
		},
		staticSector: sectorRoot,
		staticOffset: 0,
	}
	if !w.staticJobReadQueue.callAdd(jrs) {
		t.Fatal("Could not add job to queue")
	}

	// verify the job properly returns the metadata
	metadata := jrs.staticJobReadMetadata()
	if metadata == (jobReadMetadata{}) {
		t.Fatal("unexpected")
	}
	if metadata.staticSectorRoot != (crypto.Hash{1, 2, 3}) {
		t.Fatal("unexpected")
	}

	// receive response and verify if metadata is set
	jrr := <-responseChan
	if jrr.staticMetadata.staticSectorRoot != (crypto.Hash{1, 2, 3}) {
		t.Fatal("unexpected", jrr.staticMetadata.staticSectorRoot, sectorRoot)
	}
	if jrr.staticMetadata.staticWorker == nil || jrr.staticMetadata.staticWorker.staticHostPubKeyStr != wt.host.PublicKey().String() {
		t.Fatal("unexpected")
	}
}

// TestJobReadExpectedJobCost tests the read queue's callExpectedJobCost method.
func TestJobReadExpectedJobCost(t *testing.T) {
	pt := newDefaultPriceTable()
	w := &worker{}
	w.staticSetPriceTable(&workerPriceTable{
		staticPriceTable: pt,
	})
	jq := &jobReadQueue{
		jobGenericQueue: newJobGenericQueue(w),
	}

	// Compute the cost using the programbuilder for comparison. Unfortunately
	// using the programbuilder does quite a few memory allocations so it's not
	// very cpu friendly in production.
	cost := func(l uint64) types.Currency {
		pb := modules.NewProgramBuilder(&pt, 0)
		pb.AddReadSectorInstruction(l, 0, crypto.Hash{}, true)
		cost, _, _ := pb.Cost(true)

		// take into account bandwidth costs
		ulBandwidth, dlBandwidth := new(jobReadSector).callExpectedBandwidth()
		bandwidthCost := modules.MDMBandwidthCost(pt, ulBandwidth, dlBandwidth)
		return cost.Add(bandwidthCost)
	}

	// Run tests for every length between 0 and 2 sectors.
	for i := uint64(0); i < 2*modules.SectorSize; i++ {
		c := jq.callExpectedJobCost(i)
		expectedCost := cost(i)
		if !c.Equals(expectedCost) {
			t.Fatalf("%v: mismatch %v != %v", i, c, expectedCost)
		}
	}
}
