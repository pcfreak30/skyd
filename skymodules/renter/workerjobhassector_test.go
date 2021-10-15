package renter

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// TestHasSectorJobBatchCallNext makes sure that multiple has sector jobs are
// batched together correctly.
func TestHasSectorJobBatchCallNext(t *testing.T) {
	t.Parallel()

	// Create queue and job.
	queue := jobHasSectorQueue{
		availabilityMetrics: newAvailabilityMetrics(availabilityMetricsDefaultHalfLife),
		jobGenericQueue:     newJobGenericQueue(&worker{}),
	}
	jhs := &jobHasSector{
		jobGeneric: &jobGeneric{
			staticQueue: queue,
			staticCtx:   context.Background(),
		},
		staticSpan: testSpan(),
	}

	// add jobs
	for i := 0; i < int(hasSectorBatchSize)+1; i++ {
		if !queue.callAdd(jhs) {
			t.Fatal("job wasn't added")
		}
	}

	// call callNext 3 times.
	next1 := queue.callNext()
	next2 := queue.callNext()
	next3 := queue.callNext()

	// the first should contain hasSectorBatchSize jobs, the second one 1 job
	// and the third one should be nil.
	if l := len(next1.(*jobHasSectorBatch).staticJobs); l != int(hasSectorBatchSize) {
		t.Fatal("wrong size", l, hasSectorBatchSize)
	}
	if len(next2.(*jobHasSectorBatch).staticJobs) != 1 {
		t.Fatal("wrong size")
	}
	if next3 != nil {
		t.Fatal("should be nil")
	}
}

// TestHasSectorJobQueueAvailabilityRate is a unit that verifies the HS job
// queue correctly returns the availability rate
func TestHasSectorJobQueueAvailabilityRate(t *testing.T) {
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

	// wait until the worker is done with its maintenance tasks - this basically
	// ensures we have a working worker, with valid PT and funded EA
	if err := build.Retry(100, 100*time.Millisecond, func() error {
		if !w.managedMaintenanceSucceeded() {
			return errors.New("worker not ready with maintenance")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// assert the min availability rate on a new queue
	randomNumPieces := fastrand.Intn(64) + 1
	if w.staticJobHasSectorQueue.callAvailabilityRate(randomNumPieces) != jobHasSectorQueueMinAvailabilityRate {
		t.Fatal("unexpected")
	}

	// create a two roots, add one to the host
	randomData := fastrand.Bytes(int(modules.SectorSize))
	randomRoot := crypto.MerkleRoot(randomData)
	sectorData := fastrand.Bytes(int(modules.SectorSize))
	sectorRoot := crypto.MerkleRoot(sectorData)
	err = wt.host.AddSector(sectorRoot, sectorData)
	if err != nil {
		t.Fatal(err)
	}

	// add a job where the host is supposed to find one root out of two
	roots := []crypto.Hash{sectorRoot, randomRoot}
	responseChan := make(chan *jobHasSectorResponse, 1)
	jhs := w.newJobHasSector(context.Background(), responseChan, randomNumPieces, roots...)
	added := w.staticJobHasSectorQueue.callAdd(jhs)
	if !added {
		t.Fatal("unexpected")
	}

	// check whether the availability rate is correct
	if err := build.Retry(10, 10*time.Millisecond, func() error {
		availabilityRate := w.staticJobHasSectorQueue.callAvailabilityRate(randomNumPieces)
		if availabilityRate != .5 {
			return fmt.Errorf("unexpected availability rate %v != .5", availabilityRate)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// add a job where the host won't find any root
	roots = []crypto.Hash{randomRoot, randomRoot, randomRoot}
	responseChan = make(chan *jobHasSectorResponse, 1)
	jhs = w.newJobHasSector(context.Background(), responseChan, randomNumPieces, roots...)
	added = w.staticJobHasSectorQueue.callAdd(jhs)
	if !added {
		t.Fatal("unexpected")
	}

	// check whether the availability rate is correct
	if err := build.Retry(10, 10*time.Millisecond, func() error {
		availabilityRate := w.staticJobHasSectorQueue.callAvailabilityRate(randomNumPieces)
		if availabilityRate != .2 {
			return fmt.Errorf("unexpected availability rate %v != .2", availabilityRate)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestHasSectorJobExpectedBandwidth is a unit test that verifies our HS job
// bandwidth estimates are given in a way we never execute a program and run out
// of budget.
func TestHasSectorJobExpectedBandwidth(t *testing.T) {
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
	pt := wt.staticPriceTable().staticPriceTable

	// numPacketsRequiredForSectors is a helper function that executes a HS
	// program with the given amount of sectors and returns the amount of
	// packets needed to cover both the upload and download bandwidth of the
	// program.
	numPacketsRequiredForSectors := func(numSectors int) (uint64, uint64) {
		// build sectors
		sectors := make([]crypto.Hash, numSectors)
		for i := 0; i < numSectors; i++ {
			sectors[i] = crypto.Hash{1, 2, 3}
		}

		// build program
		pb := modules.NewProgramBuilder(&pt, 0)
		for _, sector := range sectors {
			pb.AddHasSectorInstruction(sector)
		}
		p, data := pb.Program()
		cost, _, _ := pb.Cost(true)

		// build job
		jhs := new(jobHasSector)
		jhs.staticSectors = sectors
		jhs.staticNumPieces = skymodules.RenterDefaultNumPieces

		// build a batch from the job for comparison
		jhsb := *&jobHasSectorBatch{
			staticJobs: []*jobHasSector{
				jhs,
			},
		}

		// calculate cost
		ulBandwidth, dlBandwidth := jhs.callExpectedBandwidth()
		bandwidthCost := modules.MDMBandwidthCost(pt, ulBandwidth, dlBandwidth)
		cost = cost.Add(bandwidthCost)

		// cost of batch should match.
		ulb, dlb := jhsb.callExpectedBandwidth()
		if ulb != ulBandwidth || dlb != dlBandwidth {
			t.Fatal("batch bandwidth doesn't match job bandwidth")
		}

		// execute the program
		_, limit, err := w.managedExecuteProgram(p, data, types.FileContractID{}, categoryDownload, cost)
		if err != nil {
			t.Fatal(err)
		}

		return limit.Downloaded() / 1460, limit.Uploaded() / 1460
	}

	// expect 1 root to only require a single packet on both up and download
	dl, ul := numPacketsRequiredForSectors(1)
	if dl != 1 || ul != 1 {
		t.Fatal("unexpected")
	}

	// expect 12 roots to not exceed the threshold (which is at 13) on download
	dl, ul = numPacketsRequiredForSectors(12)
	if dl != 1 || ul != 1 {
		t.Fatal("unexpected")
	}

	// expect 13 roots to push us over the threshold, and require an extra
	// packet on download
	dl, ul = numPacketsRequiredForSectors(13)
	if dl != 2 || ul != 1 {
		t.Fatal("unexpected")
	}

	// expect 16 roots to not exceed the threshold (which is at 17) on upload
	dl, ul = numPacketsRequiredForSectors(16)
	if dl != 2 || ul != 1 {
		t.Fatal("unexpected")
	}

	// expect 17 roots to push us over the threshold, and require an extra
	// packet on upload
	dl, ul = numPacketsRequiredForSectors(17)
	if dl != 2 || ul != 2 {
		t.Fatal("unexpected")
	}
}

// TestAvailabilityMetrics is a unit test for AvailabilityMetrics
func TestAvailabilityMetrics(t *testing.T) {
	t.Parallel()

	// verify we have the expected amount of buckets
	metrics := newAvailabilityMetrics(100 * time.Second)
	if len(metrics.buckets) != availabilityMetricsNumBuckets {
		t.Fatal("bad")
	}

	// verify the piecesToBuckets slice against this hardcoded slice
	expected := []int{-1, 0, 1, 2, 3, 3, 4, 4, 5, 5, 5, 6, 6, 6, 7, 7, 7, 7, 8, 8, 8, 8, 8, 9, 9, 9, 9, 9, 9, 10, 10, 10, 10, 10, 10, 10, 10, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15}
	if len(metrics.piecesToBuckets) != len(expected) {
		t.Fatal("bad")
	}
	for i := range expected {
		if metrics.piecesToBuckets[i] != expected[i] {
			t.Fatal("bad")
		}
	}

	// manually verify some bucket indices
	bucketIndex := metrics.piecesToBuckets[1]
	if bucketIndex != 0 {
		t.Fatal("bad", bucketIndex)
	}
	bucketIndex = metrics.piecesToBuckets[10]
	if bucketIndex != 5 {
		t.Fatal("bad")
	}
	bucketIndex = metrics.piecesToBuckets[30]
	if bucketIndex != 10 {
		t.Fatal("bad")
	}
	bucketIndex = metrics.piecesToBuckets[96]
	if bucketIndex != 15 {
		t.Fatal("bad")
	}

	// assert we're returning the last bucket if the num pieces is larger than
	// what we support, which is 116 num pieces with the current defaults
	if metrics.bucket(999) != metrics.buckets[bucketIndex] {
		t.Fatal("bad")
	}

	// assert the bucket has no datapoints yet
	bucket := metrics.bucket(10)
	if bucket.totalAvailable != 0 || bucket.totalLookups != 0 {
		t.Fatal("bad")
	}

	// update metrics and assert the correct bucket got updated
	metrics.updateMetrics(10, 3, 2)
	bucket = metrics.bucket(10)
	if bucket.totalAvailable != 2 || bucket.totalLookups != 3 {
		t.Fatal("bad")
	}

	// assert all other buckets have not been updated
	for b := 0; b < availabilityMetricsNumBuckets; b++ {
		bucketIndex = metrics.piecesToBuckets[10]
		if b == bucketIndex {
			continue
		}
		bucket := metrics.buckets[b]
		if bucket.totalAvailable != 0 || bucket.totalLookups != 0 {
			t.Fatal("bad")
		}
	}
}
