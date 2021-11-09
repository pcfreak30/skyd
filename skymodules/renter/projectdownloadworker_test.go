package renter

import (
	"math"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
)

func newTestDownloadState() *bufferedDownloadState {
	return &bufferedDownloadState{
		pieces: make(map[uint64]struct{}),
		added:  make(map[uint32]struct{}),
	}
}

// TestChimeraWorker verifies the NewChimeraWorker constructor.
func TestChimeraWorker(t *testing.T) {
	t.Parallel()

	// newIndividualWorker is a helper function that creates a test worker
	numWorkers := uint32(0)
	newIndividualWorker := func(complete, avail, cost float64) *individualWorker {
		numWorkers++
		return &individualWorker{
			resolved:               false,
			cachedCompleteChance:   complete,
			staticAvailabilityRate: avail,
			staticCost:             cost,
			staticIdentifier:       numWorkers,
		}
	}

	// assert the complete chances and cost are averaged and the identifier is
	// initialized
	workers := []*individualWorker{
		newIndividualWorker(.1, .5, .5),
		newIndividualWorker(.2, .5, .6),
		newIndividualWorker(.3, .5, .7),
		newIndividualWorker(.4, .5, .8),
	}
	numWorkers++
	cw := NewChimeraWorker(workers, numWorkers)
	if cw.staticChanceComplete != .125 {
		t.Fatal("bad", cw.staticChanceComplete)
	}
	if cw.staticCost != .65 {
		t.Fatal("bad", cw.staticCost)
	}
	if cw.staticIdentifier == 0 {
		t.Fatal("bad", cw.staticIdentifier)
	}
}

// TestIndividualWorker runs a series of small unit tests that probe the methods
// on the individual worker object.
func TestIndividualWorker(t *testing.T) {
	t.Parallel()
	t.Run("cost", testIndividualWorker_cost)
	t.Run("pieces", testIndividualWorker_pieces)
	t.Run("isLaunched", testIndividualWorker_isLaunched)
	t.Run("isResolved", testIndividualWorker_isResolved)
	t.Run("worker", testIndividualWorker_worker)
}

// testIndividualWorker_cost is a unit test for the cost method
func testIndividualWorker_cost(t *testing.T) {
	t.Parallel()

	iw := &individualWorker{staticCost: float64(fastrand.Uint64n(10) + 1)}

	// cost should return the static cost if the worker is not launched
	if iw.cost() != iw.staticCost {
		t.Fatal("bad")
	}

	// cost should return zero if the worker is launched
	iw.currentPieceLaunchedAt = time.Now()
	if iw.cost() != 0 {
		t.Fatal("bad")
	}
}

// testIndividualWorker_pieces is a unit test for the pieces method
func testIndividualWorker_pieces(t *testing.T) {
	t.Parallel()

	iw := &individualWorker{}
	if iw.pieces(nil) != nil {
		t.Fatal("bad")
	}

	iw.pieceIndices = []uint64{0, 1, 2}
	if len(iw.pieces(nil)) != 3 {
		t.Fatal("bad")
	}
	for i, piece := range []uint64{0, 1, 2} {
		if iw.pieces(nil)[i] != piece {
			t.Fatal("bad")
		}
	}
}

// testIndividualWorker_isLaunched is a unit test for the isLaunched method
func testIndividualWorker_isLaunched(t *testing.T) {
	t.Parallel()

	iw := &individualWorker{}
	if iw.isLaunched() {
		t.Fatal("bad")
	}

	iw.currentPieceLaunchedAt = time.Now()
	if !iw.isLaunched() {
		t.Fatal("bad")
	}
}

// testIndividualWorker_isResolved is a unit test for the isResolved method
func testIndividualWorker_isResolved(t *testing.T) {
	t.Parallel()

	iw := &individualWorker{}
	if iw.isResolved() {
		t.Fatal("bad")
	}

	iw.resolved = true
	if !iw.isResolved() {
		t.Fatal("bad")
	}
}

// testIndividualWorker_worker is a unit test for the worker method
func testIndividualWorker_worker(t *testing.T) {
	t.Parallel()

	iw := &individualWorker{}
	if iw.worker() != nil {
		t.Fatal("bad")
	}

	w := new(worker)
	iw.staticWorker = w
	if iw.worker() != w {
		t.Fatal("bad")
	}
}

// TestWorkerSet is a set of unit tests that verify the functionality of the
// worker set.
func TestWorkerSet(t *testing.T) {
	t.Parallel()

	t.Run("AdjustedDuration", testWorkerSetAdjustedDuration)
	t.Run("CheaperSetFromCandidate", testWorkerSetCheaperSetFromCandidate)
	t.Run("Clone", testWorkerSetClone)
	t.Run("Create", testWorkerSetCreate)
	t.Run("GreaterThanHalf", testWorkerSetGreaterThanHalf)
}

// testWorkerSetAdjustedDuration is a unit test that verifies the functionality
// of the AdjustedDuration method on the worker set.
func testWorkerSetAdjustedDuration(t *testing.T) {
	t.Parallel()

	ws := &workerSet{
		staticBucketDuration: 100 * time.Millisecond,
		staticMinPieces:      1,
	}

	// default ppms
	ppms := skymodules.DefaultSkynetPricePerMS

	// expect no penalty if the ppms exceeds the job cost
	if ws.adjustedDuration(ppms) != ws.staticBucketDuration {
		t.Fatal("bad")
	}

	// create a worker and ensure its job cost is not zero
	cost, _ := types.SiacoinPrecision.Float64()
	iw1 := &individualWorker{
		staticWorker: mockWorker(10 * time.Millisecond),
		staticCost:   cost,
	}
	if iw1.cost() == 0 {
		t.Fatal("bad")
	}

	// add the worker and calculate the adjusted duration, ensure a cost penalty
	// has been applied. We don't have to cover the actual cost penalty as that
	// is unit tested by TestAddCostPenalty.
	ws.workers = append(ws.workers, iw1)
	if ws.adjustedDuration(ppms) <= ws.staticBucketDuration {
		t.Fatal("bad")
	}
}

// testWorkerSetCheaperSetFromCandidate is a unit test that verifies the
// functionality of the CheaperSetFromCandidate method on the worker set.
func testWorkerSetCheaperSetFromCandidate(t *testing.T) {
	t.Parallel()

	sc, _ := types.SiacoinPrecision.Float64()

	// updateReadCostWithFactor is a helper function that enables altering a
	// worker's cost by multiplying initial cost with the given factor
	updateReadCostWithFactor := func(w *individualWorker, factor uint64) {
		w.staticCost = sc * float64(factor)
	}

	// workerAt is a small helper function that returns the worker's identifier
	// at the given index
	workerAt := func(ws *workerSet, index int) string {
		return ws.workers[index].worker().staticHostPubKeyStr
	}

	// create some workers
	iw1 := newTestIndivualWorker("w1", 1, 1, 10*time.Millisecond, []uint64{1})
	iw2 := newTestIndivualWorker("w2", 2, 1, 10*time.Millisecond, []uint64{2})

	// have w1 and w2 download p1 and p2
	iw1.markPieceForDownload(1)
	iw2.markPieceForDownload(2)

	// update the read cost of both workers
	updateReadCostWithFactor(iw1, 10) // 10SC
	updateReadCostWithFactor(iw2, 20) // 20SC

	// assert the workers are increasingly more expensive
	if iw1.cost() >= iw2.cost() {
		t.Fatal("bad")
	}

	// build a worker set
	pcws := newTestProjectChunkWorkerSet()
	pdc := newTestProjectDownloadChunk(pcws, nil)
	ws := &workerSet{
		workers:              []downloadWorker{iw1, iw2},
		staticBucketDuration: 100 * time.Millisecond,
		staticMinPieces:      1,
		staticPDC:            pdc,
	}

	// create w3 without pieces
	iw3 := newTestIndivualWorker("w3", 3, 1, 10*time.Millisecond, []uint64{})

	// assert the cheaper set is nil (candidate has no pieces)
	if ws.cheaperSetFromCandidate(iw3) != nil {
		t.Fatal("bad")
	}

	// have w3 download p3 but at a high cost, higher than w1 and w2
	iw3.pieceIndices = append(iw3.pieceIndices, 3)
	iw3.markPieceForDownload(3)
	updateReadCostWithFactor(iw3, 30) // 30SC

	// assert the cheaper set is nil (candidate is more expensive)
	cheaperSet := ws.cheaperSetFromCandidate(iw3)
	if cheaperSet != nil {
		t.Fatal("bad")
	}

	// make w3 cheaper than w2
	updateReadCostWithFactor(iw3, 15) // 15SC
	cheaperSet = ws.cheaperSetFromCandidate(iw3)
	if cheaperSet == nil {
		t.Fatal("bad")
	}
	if len(cheaperSet.workers) != 2 {
		t.Fatal("bad")
	}
	if workerAt(cheaperSet, 0) != "w1" || workerAt(cheaperSet, 1) != "w3" {
		t.Fatal("bad")
	}

	// continue with the cheaper set as working set (w1 and w3)
	ws = cheaperSet

	// create w4 with all pieces
	iw4 := newTestIndivualWorker("w4", 4, 1, 10*time.Millisecond, []uint64{1, 3})

	// make w4 more expensive
	updateReadCostWithFactor(iw4, 40) // 40SC
	cheaperSet = ws.cheaperSetFromCandidate(iw4)
	if cheaperSet != nil {
		t.Fatal("bad")
	}

	// make w4 cheaper than w1
	updateReadCostWithFactor(iw4, 4) // 4SC
	cheaperSet = ws.cheaperSetFromCandidate(iw4)
	if cheaperSet == nil {
		t.Fatal("bad")
	}
	if len(cheaperSet.workers) != 2 {
		t.Fatal("bad")
	}

	// assert we did not swap w1 but w3 because it was more expensive
	if workerAt(cheaperSet, 0) != "w1" || workerAt(cheaperSet, 1) != "w4" {
		t.Fatal("bad", workerAt(cheaperSet, 0), workerAt(cheaperSet, 1))
	}

	// continue with the cheaper set as working set
	ws = cheaperSet

	// create w5 capable of resolving p1, but make it more expensive
	iw5 := newTestIndivualWorker("w5", 5, .1, 10*time.Millisecond, []uint64{1})
	updateReadCostWithFactor(iw5, 50) // 50SC

	// assert the cheaper set is nil (candidate is more expensive)
	cheaperSet = ws.cheaperSetFromCandidate(iw5)
	if cheaperSet != nil {
		t.Fatal("bad")
	}

	// make w5 cheaper than w1
	updateReadCostWithFactor(iw5, 5) // 5SC
	cheaperSet = ws.cheaperSetFromCandidate(iw5)
	if cheaperSet == nil {
		t.Fatal("bad")
	}
	if len(cheaperSet.workers) != 2 {
		t.Fatal("bad")
	}

	// assert we swapped out w1 for w5 because it was cheaper
	if workerAt(cheaperSet, 0) != "w5" || workerAt(cheaperSet, 1) != "w4" {
		t.Fatal("bad", workerAt(cheaperSet, 0), workerAt(cheaperSet, 1))
	}

	// set a launch time for w1
	ws.workers[0].(*individualWorker).currentPieceLaunchedAt = time.Now()

	// assert we did not swap out w1 for w5 because the cost is now 0
	cheaperSet = ws.cheaperSetFromCandidate(iw5)
	if cheaperSet != nil {
		t.Fatal("bad")
	}
}

// testWorkerSetClone is a unit test that verifies the
// functionality of the Clone method on the worker set.
func testWorkerSetClone(t *testing.T) {
	t.Parallel()

	// create some workers
	iw1 := newTestIndivualWorker("w1", 1, 0, 0, nil)
	iw2 := newTestIndivualWorker("w2", 2, 0, 0, nil)
	iw3 := newTestIndivualWorker("w3", 3, 0, 0, nil)

	// build a worker set
	ws := &workerSet{
		workers:              []downloadWorker{iw1, iw2, iw3},
		staticBucketDuration: 100 * time.Millisecond,
		staticMinPieces:      1,
	}

	// use reflection to assert "clone" returns an identical object
	clone := ws.clone()
	if !reflect.DeepEqual(clone, ws) {
		t.Fatal("bad")
	}
}

// testWorkerSetCreate is a unit test that verifies the creation of a worker set
func testWorkerSetCreate(t *testing.T) {
	t.Parallel()

	bI := 11
	bDur := skymodules.DistributionDurationForBucketIndex(bI) // 44ms
	minPieces := 1
	numOD := 0
	workersNeeded := minPieces + numOD

	// create pdc
	pcws := newTestProjectChunkWorkerSet()
	pdc := newTestProjectDownloadChunk(pcws, nil)

	// mock a launched worker
	readDT := skymodules.NewDistribution(time.Minute)
	readDT.AddDataPoint(30 * time.Millisecond)
	readDT.AddDataPoint(40 * time.Millisecond)
	readDT.AddDataPoint(60 * time.Millisecond)
	launchedWorkerIdentifier := uint32(1)
	lw := &individualWorker{
		pieceIndices: []uint64{0},
		resolved:     true,

		currentPiece:           0,
		currentPieceLaunchedAt: time.Now().Add(-20 * time.Millisecond),
		staticReadDistribution: *readDT,
		staticIdentifier:       launchedWorkerIdentifier,
		staticCost:             1,
	}

	// mock a resolved worker
	readDT = skymodules.NewDistribution(time.Minute)
	readDT.AddDataPoint(20 * time.Millisecond)
	readDT.AddDataPoint(30 * time.Millisecond)
	readDT.AddDataPoint(40 * time.Millisecond)
	readDT.AddDataPoint(60 * time.Millisecond)
	resolvedWorkerIdentifier := uint32(2)
	rw := &individualWorker{
		pieceIndices: []uint64{0},
		resolved:     true,

		staticReadDistribution: *readDT,
		staticIdentifier:       resolvedWorkerIdentifier,
		staticCost:             1,
	}

	// recalculate the distribution chances
	lw.recalculateDistributionChances()
	rw.recalculateDistributionChances()

	// cache the complete chance at the bucket index
	lw.recalculateCompleteChance(bI)
	rw.recalculateCompleteChance(bI)

	// assert the launched worker's chance is lower than the resolved worker,
	// but both are higher than 50% so they can sustain a worker set on their
	// own
	lwcc := lw.completeChanceCached()
	rwcc := rw.completeChanceCached()
	if lwcc >= rwcc || lwcc <= .5 || rwcc <= .5 {
		t.Fatal("unexpected", lwcc, rwcc)
	}

	// create a worker set
	workers := []*individualWorker{lw, rw}
	ws, _ := pdc.createWorkerSetInner(workers, minPieces, numOD, bI, bDur, newTestDownloadState())
	if ws == nil || len(ws.workers) != 1 {
		t.Fatal("unexpected")
	}

	// assert the launched worker got selected
	selected := ws.workers[0]
	if selected.identifier() != launchedWorkerIdentifier {
		t.Fatal("unexpected", selected.identifier())
	}

	// assert the resolved worker was selected as most likely, proving that the
	// launched worker beat it because it's cheaper
	dlw := pdc.buildDownloadWorkers(workers)
	mostLikely, lessLikely := pdc.splitMostlikelyLessLikely(dlw, workersNeeded, newTestDownloadState())
	if len(mostLikely) != 1 || len(lessLikely) != 1 {
		t.Fatal("unexpected")
	}
	if mostLikely[0].identifier() != resolvedWorkerIdentifier || lessLikely[0].identifier() != launchedWorkerIdentifier {
		t.Fatal("unexpected")
	}

	// now bump the overdrive workers, and assert both workers are in the set
	ws, _ = pdc.createWorkerSetInner(workers, minPieces, numOD+1, bI, bDur, newTestDownloadState())
	if ws == nil || len(ws.workers) != 2 {
		t.Fatal("unexpected", ws)
	}
}

// testWorkerSetGreaterThanHalf is a unit test that verifies the functionality
// of the GreaterThanHalf method on the worker set.
func testWorkerSetGreaterThanHalf(t *testing.T) {
	t.Parallel()

	// convenience Variables
	DistributionDurationForBucketIndex := skymodules.DistributionDurationForBucketIndex
	distributionTotalBuckets := skymodules.DistributionTrackerTotalBuckets

	// create some workers
	iw1 := newTestIndivualWorker("w1", 1, 0, 0, nil)
	iw2 := newTestIndivualWorker("w2", 2, 0, 0, nil)
	iw3 := newTestIndivualWorker("w3", 3, 0, 0, nil)
	iw4 := newTestIndivualWorker("w4", 4, 0, 0, nil)
	iw5 := newTestIndivualWorker("w5", 5, 0, 0, nil)

	// populate their distributions in a way that the output of the distribution
	// becomes predictable by adding a single datapoint to every bucket
	for _, w := range []*individualWorker{iw1, iw2, iw3, iw4, iw5} {
		// mimic resolved workers, that ensures we only take the read DTs into
		// account, which is sufficient for the goal of this unit test
		w.resolved = true
		for i := 0; i < distributionTotalBuckets; i++ {
			point := DistributionDurationForBucketIndex(i)
			w.staticReadDistribution.AddDataPoint(point)
		}
		w.recalculateDistributionChances()
	}

	// recalculateCompleteChance is a helper function that mimics the download
	// algorithm recalculating complete chances at a certain bucket index, we
	// use percentages here because we're reasoning about complete chances
	recalculateCompleteChance := func(pct float64) {
		index := distributionTotalBuckets * int(pct*100) / 100
		for _, w := range []*individualWorker{iw1, iw2, iw3, iw4, iw5} {
			w.recalculateCompleteChance(index)
		}
	}

	// build a worker set with 0 overdrive workers
	ws := &workerSet{
		workers:              []downloadWorker{iw1},
		staticBucketDuration: 100 * time.Millisecond,
		staticMinPieces:      2,
	}

	// assert chance is not greater than .5
	recalculateCompleteChance(.5)
	if ws.chanceGreaterThanHalf() {
		t.Fatal("bad")
	}

	// push it right over the 50% mark
	recalculateCompleteChance(.51)
	if !ws.chanceGreaterThanHalf() {
		t.Fatal("bad")
	}

	// add another worker, seeing as the chances are multiplied and both workers
	// have a chance ~= 50% the total chance should not be greater than half
	recalculateCompleteChance(.5)
	ws.workers = append(ws.workers, iw2)
	if ws.chanceGreaterThanHalf() {
		t.Fatal("bad")
	}

	// at 75% chance per worker the total chance should be ~56% which is greater
	// than half
	recalculateCompleteChance(.75)
	if !ws.chanceGreaterThanHalf() {
		t.Fatal("bad")
	}

	// add another worker, this makes it so the worker set has one overdrive
	// worker, which influences the 'chanceGreaterThanHalf' because now we have
	// one worker to spare, increasing our total chance
	ws.workers = append(ws.workers, iw3)
	ws.staticNumOverdrive = 1

	// when all workers have exatly a 50% chance, the total chance does NOT
	// exceed 0.5 because it is exactly equal to 0.5
	//
	// 0.5*0.5*0.5 = 0.125 is the chance they're all able to complete after dur
	// 0.125/0.5*0.5 = 0.125 is the chance one is tails and the others are heads
	// 0.125+(0.125*3) = 0.5 because every worker can be the one that's tails
	recalculateCompleteChance(.5)
	if ws.chanceGreaterThanHalf() {
		t.Fatal("bad")
	}

	// now redo the calculation with a duration that's just over half of the
	// distributand assert the chance is now greater than half
	recalculateCompleteChance(.51)
	if !ws.chanceGreaterThanHalf() {
		t.Fatal("bad")
	}

	// add another worker, this makes it so the worker set has two overdrive
	// workers
	ws.workers = append(ws.workers, iw4)
	ws.staticNumOverdrive = 2

	// whe we have two overdrive workers, we have a similar situation but now
	// there are essentially two workers to spare, increasing our chances. We
	// again have the situation where exactly one of them is unable to download
	// the piece but all others are (OD worker one) but also the situation where
	// every worker pair is the pair where both workers are able to download
	//
	// assert that at a duration that corresponds with the 33% mark the total
	// chance is not greater than half:
	//
	// chance they all complete the DL is:
	// 0,33^4 ~= 0.012
	//
	// chance one of them fails and others complete is:
	// (0,67*0,33^4)*4 ~= 0,09
	//
	// chance each pair becomes tails and other workers complete is:
	// (0,67^2*0,33^2)*6 ~= 0,3
	//
	// there are 6 unique pairs for 4 workers and 2 OD workers
	// which is n choose k and equal to n!/k(n-k)! or in our case 4!/2*2! = 6
	//
	// the toal chance is thus ~= 0.4 which is not greater than half
	recalculateCompleteChance(.33)
	if ws.chanceGreaterThanHalf() {
		t.Fatal("bad")
	}

	// doing the same calculations with 0.4 results in:
	// 0,4^4 ~= 0,025
	// (0,6*0,4^3)*4 ~= 0,15
	// (0,6^2*0,4^2)*6 ~= 0,35
	//
	// the toal chance is thus ~= 0.525 which is not greater than half
	recalculateCompleteChance(.4)
	if !ws.chanceGreaterThanHalf() {
		t.Fatal("bad")
	}

	// add another worker, we limit n choose m to 2 because of computational
	// complexity, in the case we have more than 2 overdrive workers we use an
	// approximation where we return true if the sum of all chances is greater
	// than minpieces
	//
	// we have two minpieces and 5 workers so we have to exceed 40% per worker
	ws.workers = append(ws.workers, iw5)
	ws.staticNumOverdrive = 3
	if ws.chanceGreaterThanHalf() {
		t.Fatal("bad")
	}
	recalculateCompleteChance(.41)
	if !ws.chanceGreaterThanHalf() {
		t.Fatal("bad")
	}
}

// TestCoinflips wraps a set of unit tests that verify the functionality of the
// coinflips type.
func TestCoinflips(t *testing.T) {
	t.Parallel()
	t.Run("AllHeads", testCoinflipsAllHeads)
	t.Run("HeadsAllowOneTails", testCoinflipsHeadsAllowOneTails)
	t.Run("HeadsAllowTwoTails", testCoinflipsHeadsAllowTwoTails)
	t.Run("Sum", testCoinflipsSum)
}

// testCoinflipsAllHeads is a unit test to verify the functionality of
// 'chanceAllHeads' on the coinflips type.
func testCoinflipsAllHeads(t *testing.T) {
	t.Parallel()

	// almost equal compares floats up until a precision threshold of 1e-9, this
	// necessary due to floating point errors that arise when multiplying floats
	almostEqual := func(a, b float64) bool {
		return math.Abs(a-b) <= 1e-9
	}

	tests := []struct {
		name   string
		flips  coinflips
		chance float64
	}{
		{"no_flips", []float64{}, 0},
		{"one_pure_flip", []float64{.5}, .5},
		{"two_pure_flips", []float64{.5, .5}, math.Pow(.5, 2)},
		{"multiple_diff_flips", []float64{.1, .3, .2}, .1 * .2 * .3},
		{"one_heads", []float64{1}, 1},
		{"multiple_heads", []float64{1}, 1},
	}

	for _, test := range tests {
		actual := test.flips.chanceAllHeads()
		if !almostEqual(actual, test.chance) {
			t.Error("bad", test.name, actual, test.chance)
		}
	}
}

// testCoinflipsHeadsAllowOneTails is a unit test to verify the functionality of
// 'chanceHeadsAllowOneTails' on the coinflips type.
func testCoinflipsHeadsAllowOneTails(t *testing.T) {
	t.Parallel()

	// almost equal compares floats up until a precision threshold of 1e-9, this
	// necessary due to floating point errors that arise when multiplying floats
	almostEqual := func(a, b float64) bool {
		return math.Abs(a-b) <= 1e-9
	}

	tests := []struct {
		name   string
		flips  coinflips
		chance float64
	}{
		{"no_flips", []float64{}, 0},
		{"one_pure_flip", []float64{.5}, 1},
		{"two_pure_flips", []float64{.5, .5}, 0.75},
		{"multiple_diff_flips", []float64{.25, .5, .75}, (.25 * .5 * .75) + (0.75 * .5 * .75) + (.5 * .25 * .75) + (.25 * .25 * .5)},
		{"one_heads", []float64{1}, 1},
		{"multiple_heads", []float64{1, 1}, 1},
	}

	for _, test := range tests {
		actual := test.flips.chanceHeadsAllowOneTails()
		if !almostEqual(actual, test.chance) {
			t.Error("bad", test.name, actual, test.chance)
		}
	}
}

// testCoinflipsHeadsAllowTwoTails is a unit test to verify the functionality of
// 'chanceHeadsAllowTwoTails' on the coinflips type.
func testCoinflipsHeadsAllowTwoTails(t *testing.T) {
	t.Parallel()

	// almost equal compares floats up until a precision threshold of 1e-9, this
	// necessary due to floating point errors that arise when multiplying floats
	almostEqual := func(a, b float64) bool {
		return math.Abs(a-b) <= 1e-9
	}

	tests := []struct {
		name   string
		flips  coinflips
		chance float64
	}{
		{"no_flips", []float64{}, 0},
		{"one_pure_flip", []float64{.5}, 1},
		{"two_pure_flips", []float64{.5, .5}, 1},
		{"multiple_diff_flips", []float64{.25, .5, .75}, (.25 * .5 * .75) + (0.75 * .5 * .75) + (.5 * .25 * .75) + (.25 * .25 * .5) + (.75 * .5 * .75) + (.75 * .5 * .25) + (.25 * .5 * .25)},
		{"one_heads", []float64{1}, 1},
		{"multiple_heads", []float64{1, 1}, 1},
	}

	for _, test := range tests {
		actual := test.flips.chanceHeadsAllowTwoTails()
		if !almostEqual(actual, test.chance) {
			t.Error("bad", test.name, actual, test.chance)
		}
	}
}

// testCoinflipsHeadsAllowTwoTails is a unit test to verify the functionality of
// 'chanceSum' on the coinflips type.
func testCoinflipsSum(t *testing.T) {
	t.Parallel()

	// almost equal compares floats up until a precision threshold of 1e-9, this
	// necessary due to floating point errors that arise when multiplying floats
	almostEqual := func(a, b float64) bool {
		return math.Abs(a-b) <= 1e-9
	}

	tests := []struct {
		name   string
		flips  coinflips
		chance float64
	}{
		{"no_flips", []float64{}, 0},
		{"one_pure_flip", []float64{.5}, .5},
		{"two_pure_flips", []float64{.5, .5}, 1},
		{"multiple_diff_flips", []float64{.25, .5, .75}, 1.5},
		{"one_heads", []float64{1}, 1},
		{"multiple_heads", []float64{1, 1}, 2},
	}

	for _, test := range tests {
		actual := test.flips.chanceSum()
		if !almostEqual(actual, test.chance) {
			t.Error("bad", test.name, actual, test.chance)
		}
	}
}

// TestAddCostPenalty is a unit test that covers the `addCostPenalty` helper
// function.
//
// NOTE: should this file get removed due to introducing a new version of the
// overdrive, this test has to move to projectdownloadworker_test as the
// `addCostPenalty` function is used there as well.
func TestAddCostPenalty(t *testing.T) {
	t.Parallel()

	// verify happy case
	jt := time.Duration(fastrand.Intn(10) + 1)
	jc := types.NewCurrency64(fastrand.Uint64n(100) + 10)
	pricePerMS := types.NewCurrency64(5)

	// calculate the expected outcome
	penalty, err := jc.Div(pricePerMS).Uint64()
	if err != nil {
		t.Fatal(err)
	}
	expected := jt + (time.Duration(penalty) * time.Millisecond)
	adjusted := addCostPenalty(jt, jc, pricePerMS)
	if adjusted != expected {
		t.Error("unexpected", adjusted, expected)
	}

	// verify no penalty if pricePerMS is higher than the cost of the job
	adjusted = addCostPenalty(jt, jc, types.SiacoinPrecision)
	if adjusted != jt {
		t.Error("unexpected")
	}

	// verify penalty equal to MaxInt64 and job time of 0
	jt = time.Duration(1)
	jc = types.NewCurrency64(math.MaxInt64)
	pricePerMS = types.NewCurrency64(1)
	jt = addCostPenalty(jt, jc, pricePerMS)
	if jt != time.Duration(math.MaxInt64) {
		t.Error("unexpected")
	}

	// verify penalty equal to MaxInt64 and job time of 0
	jt = time.Duration(0)
	jc = types.NewCurrency64(math.MaxInt64)
	pricePerMS = types.NewCurrency64(1)
	jt = addCostPenalty(jt, jc, pricePerMS)
	if jt != time.Duration(math.MaxInt64) {
		t.Error("unexpected")
	}

	// verify overflow
	jt = time.Duration(1)
	jc = types.NewCurrency64(math.MaxUint64).Mul64(10)
	pricePerMS = types.NewCurrency64(2)
	jt = addCostPenalty(jt, jc, pricePerMS)
	if jt != time.Duration(math.MaxInt64) {
		t.Error("Expected job time to be adjusted to MaxInt64 on overflow")
	}

	// verify penalty higher than MaxInt64
	jt = time.Duration(1)
	jc = types.NewCurrency64(math.MaxInt64).Add64(1)
	pricePerMS = types.NewCurrency64(1)
	jt = addCostPenalty(jt, jc, pricePerMS)
	if jt != time.Duration(math.MaxInt64) {
		t.Error("Expected job time to be adjusted to MaxInt64 when penalty exceeds MaxInt64")
	}

	// verify high job time overflowing after adding penalty
	jc = types.NewCurrency64(10)
	pricePerMS = types.NewCurrency64(1)   // penalty is 10
	jt = time.Duration(math.MaxInt64 - 5) // job time + penalty exceeds MaxInt64
	jt = addCostPenalty(jt, jc, pricePerMS)
	if jt != time.Duration(math.MaxInt64) {
		t.Error("Expected job time to be adjusted to MaxInt64 when job time + penalty exceeds MaxInt64")
	}
}

// TestBuildChimeraWorkers is a unit test that covers the 'buildChimeraWorkers'
// helper function.
func TestBuildChimeraWorkers(t *testing.T) {
	t.Parallel()

	// newIndividualWorker wraps newTestIndivualWorker
	numWorkers := uint32(0)
	newIndividualWorker := func(availabilityRate float64) *individualWorker {
		numWorkers++
		iw := newTestIndivualWorker("w", numWorkers, availabilityRate, 0, []uint64{0})
		iw.resolved = false
		return iw
	}

	// create pdc
	pcws := newTestProjectChunkWorkerSet()
	pdc := newTestProjectDownloadChunk(pcws, nil)

	// empty case
	numWorkers++
	chimeras := pdc.buildChimeraWorkers([]*individualWorker{}, numWorkers)
	if len(chimeras) != 0 {
		t.Fatal("bad")
	}

	// add some workers that do not add up to the availability rate threshold
	workers := []*individualWorker{
		newIndividualWorker(0.3),
		newIndividualWorker(0.1),
		newIndividualWorker(0.5),
	}

	// check we still don't have a full chimera
	numWorkers++
	chimeras = pdc.buildChimeraWorkers(workers, numWorkers)
	if len(chimeras) != 0 {
		t.Fatal("bad")
	}

	// add more workers we should end up with 2 chimeras
	workers = append(
		workers,
		newIndividualWorker(0.6),
		newIndividualWorker(0.6),
		// chimera 1 complete
		newIndividualWorker(0.8),
		newIndividualWorker(0.3),
		newIndividualWorker(0.6),
		newIndividualWorker(0.3),
		// chimera 2 complete, .1 remainder
	)

	// assert we have two chimeras
	numWorkers++
	chimeras = pdc.buildChimeraWorkers(workers, numWorkers)
	if len(chimeras) != 2 {
		t.Fatal("bad", len(chimeras))
	}
}

// TestBuildDownloadWorkers is a unit test that covers the
// 'buildDownloadWorkers' helper function.
func TestBuildDownloadWorkers(t *testing.T) {
	t.Parallel()

	// newIndividualWorker wraps newTestIndivualWorker
	numWorkers := uint32(0)
	newIndividualWorker := func(availabilityRate float64, resolved bool) *individualWorker {
		numWorkers++
		iw := newTestIndivualWorker("w", numWorkers, availabilityRate, 0, []uint64{0})
		iw.cachedCompleteChance = .1
		iw.resolved = resolved
		return iw
	}

	// create pdc
	pcws := newTestProjectChunkWorkerSet()
	pdc := newTestProjectDownloadChunk(pcws, nil)

	// empty case
	downloadWorkers := pdc.buildDownloadWorkers([]*individualWorker{})
	if len(downloadWorkers) != 0 {
		t.Fatal("bad")
	}

	// add couple of workers with resolve chance not adding up to to the
	workers := []*individualWorker{
		newIndividualWorker(0.3, false),
		newIndividualWorker(0.1, false),
		newIndividualWorker(0.5, false),
		// 0.9 avail rate
	}

	// assert we still don't have a chimera
	downloadWorkers = pdc.buildDownloadWorkers(workers)
	if len(downloadWorkers) != 0 {
		t.Fatal("bad")
	}

	// add more workers we should end up with 2 chimeras
	workers = append(
		workers,
		newIndividualWorker(0.6, false),
		newIndividualWorker(0.6, false),
		// chimera 1 complete (.1 extra)
		newIndividualWorker(0.9, false),
		newIndividualWorker(0.3, false),
		newIndividualWorker(0.6, false),
		newIndividualWorker(0.3, false),
		// chimera 2 complete (.1 extra)
	)

	// assert we have two download workers, both chimera workers
	downloadWorkers = pdc.buildDownloadWorkers(workers)
	if len(downloadWorkers) != 2 {
		t.Fatal("bad", len(downloadWorkers))
	}
	_, w1IsChimera := downloadWorkers[0].(*chimeraWorker)
	_, w2IsChimera := downloadWorkers[1].(*chimeraWorker)
	if !(w1IsChimera && w2IsChimera) {
		t.Fatal("bad")
	}

	// add two resolved workers + enough unresolved to complete another chimera
	workers = append(
		workers,
		newIndividualWorker(0.4, true),
		newIndividualWorker(0.3, true),
		newIndividualWorker(0.9, false),
		newIndividualWorker(0.5, false),
		newIndividualWorker(0.6, false),
	)

	downloadWorkers = pdc.buildDownloadWorkers(workers)
	if len(downloadWorkers) != 5 {
		t.Fatal("bad", len(downloadWorkers))
	}
	_, w1IsIndividual := downloadWorkers[0].(*individualWorker)
	_, w2IsIndividual := downloadWorkers[1].(*individualWorker)
	_, w3IsChimera := downloadWorkers[2].(*chimeraWorker)
	_, w4IsChimera := downloadWorkers[3].(*chimeraWorker)
	_, w5IsChimera := downloadWorkers[4].(*chimeraWorker)
	if !(w1IsIndividual && w2IsIndividual && w3IsChimera && w4IsChimera && w5IsChimera) {
		t.Fatal("bad")
	}
}

// TestSplitMostLikelyLessLikely is a unit test that covers
// 'splitMostlikelyLessLikely' on the projectDownloadChunk
func TestSplitMostLikelyLessLikely(t *testing.T) {
	t.Parallel()

	// define some variables
	workersNeeded := 2

	// create pdc
	pcws := newTestProjectChunkWorkerSet()
	pdc := newTestProjectDownloadChunk(pcws, nil)
	pdc.staticPieceIndices = []uint64{0, 1}

	// mock the worker
	_, pk := crypto.GenerateKeyPair()
	spk := types.SiaPublicKey{
		Algorithm: types.SignatureEd25519,
		Key:       pk[:],
	}
	worker := new(worker)
	worker.staticHostPubKey = spk

	// we will have 3 workers, one resolved one and two chimeras
	iw1 := &individualWorker{staticIdentifier: 1}
	cw1 := NewChimeraWorker(nil, 2)
	cw2 := NewChimeraWorker(nil, 3)

	// mock the chance afters, make sure the chimeras have a higher chance,
	// meaning they are more likely to end up in the most likely set
	iw1.cachedCompleteChance = .1
	cw1.staticChanceComplete = .2
	cw2.staticChanceComplete = .3

	// helper variables
	iw1Key := iw1.identifier()
	cw1Key := cw1.identifier()
	cw2Key := cw2.identifier()

	// split the workers in most likely and less likely
	workers := []downloadWorker{iw1, cw1, cw2}
	mostLikely, lessLikely := pdc.splitMostlikelyLessLikely(workers, workersNeeded, newTestDownloadState())

	// expect the most likely to consist of the 2 chimeras
	if len(mostLikely) != workersNeeded {
		t.Fatal("bad", len(mostLikely))
	}
	mostLikelyKey1 := mostLikely[0].identifier()
	mostLikelyKey2 := mostLikely[1].identifier()
	if !(mostLikelyKey1 == cw2Key && mostLikelyKey2 == cw1Key) {
		t.Fatal("bad")
	}

	// assert the less likely set is empty
	if len(lessLikely) != 0 {
		t.Fatal("bad")
	}

	// add a piece to the individual worker
	iw1.pieceIndices = append(iw1.pieceIndices, 2)

	// assert it's now in the less likely set
	_, lessLikely = pdc.splitMostlikelyLessLikely(workers, workersNeeded, newTestDownloadState())
	if len(lessLikely) != 1 {
		t.Fatal("bad")
	}
	lessLikelyKey1 := lessLikely[0].identifier()
	if lessLikelyKey1 != iw1Key {
		t.Fatal("bad")
	}

	// reconfigure the pdc to only have 1 static piece
	pdc.staticPieceIndices = []uint64{0}

	// assert most likely and less likely
	mostLikely, lessLikely = pdc.splitMostlikelyLessLikely(workers, workersNeeded, newTestDownloadState())
	if len(mostLikely) != 2 {
		t.Fatal("bad")
	}
	if len(lessLikely) != 1 {
		t.Fatal("bad")
	}

	// we want to assert that despites its lower chance, the individual worker
	// now made it in the most likely set as it can resolve a piece the chimeras
	// can't - and both chimeras are present in the returned slices as well
	mostLikelyKey1 = mostLikely[0].identifier()
	mostLikelyKey2 = mostLikely[1].identifier()
	lessLikelyKey1 = lessLikely[0].identifier()
	if mostLikelyKey1 != cw2Key || mostLikelyKey2 != iw1Key || lessLikelyKey1 != cw1Key {
		t.Fatal("bad")
	}
}

// TestBucketIndexRange is a unit test that verifies the functionality of the
// helper function BucketIndexRange
func TestBucketIndexRange(t *testing.T) {
	t.Parallel()

	maxBI := skymodules.DistributionTrackerTotalBuckets - 1

	min, max := bucketIndexRange(0)
	if min != 0 || max != bucketIndexScanStep {
		t.Fatal("bad")
	}

	min, max = bucketIndexRange(maxBI - bucketIndexScanStep)
	if min != maxBI-2*bucketIndexScanStep || max != maxBI {
		t.Fatal("bad")
	}

	min, max = bucketIndexRange(bucketIndexScanStep)
	if min != 0 || max != 2*bucketIndexScanStep {
		t.Fatal("bad")
	}

	// randomly generate a bucket index that's constructed in a way that the
	// range is not below 0 or above the max
	maxBII := uint64(skymodules.DistributionTrackerTotalBuckets - 2*bucketIndexScanStep)
	random := fastrand.Uint64n(maxBII) + bucketIndexScanStep
	min, max = bucketIndexRange(int(random))
	if min != int(random)-bucketIndexScanStep || max != int(random)+bucketIndexScanStep {
		t.Fatal("bad", random)
	}
}

// TestIsGoodForDownload is a unit test that verifies the functionality of the
// helper function IsGoodForDownload
func TestIsGoodForDownload(t *testing.T) {
	t.Parallel()

	w := mockWorker(0)
	sc := types.SiacoinPrecision

	// assert happy case
	gfd := isGoodForDownload(w, []uint64{0})
	if !gfd {
		t.Fatal("bad")
	}

	// assert workers with no pieces are not good for download
	gfd = isGoodForDownload(w, nil)
	if gfd {
		t.Fatal("bad")
	}
	gfd = isGoodForDownload(w, []uint64{})
	if gfd {
		t.Fatal("bad")
	}

	// assert workers on maintenance cooldown are not good for download
	w.staticMaintenanceState.cooldownUntil = time.Now().Add(time.Minute)
	gfd = isGoodForDownload(w, []uint64{0})
	if gfd {
		t.Fatal("bad")
	}
	w.staticMaintenanceState.cooldownUntil = time.Time{} // reset

	// assert workers that are not async ready are not good for download
	w.staticPriceTable().staticExpiryTime = time.Now().Add(-time.Minute)
	gfd = isGoodForDownload(w, []uint64{0})
	if gfd {
		t.Fatal("bad")
	}
	w.staticPriceTable().staticExpiryTime = time.Now().Add(time.Minute) // reset

	// assert workers that are considered gouging are not good for download (we
	// trigger gouging detection by pushing the dl bandwidthcost over the max)
	wc := new(workerCache)
	wc.staticRenterAllowance.MaxDownloadBandwidthPrice = sc
	atomic.StorePointer(&w.atomicCache, unsafe.Pointer(wc))
	w.staticPriceTable().staticPriceTable.DownloadBandwidthCost = sc.Mul64(2)
	gfd = isGoodForDownload(w, []uint64{0})
	if gfd {
		t.Fatal("bad")
	}
}

// newTestIndivualWorker is a helper function that returns an individualWorker
// for testing purposes.
func newTestIndivualWorker(hostPubKeyStr string, identifier uint32, availabilityRate float64, readDuration time.Duration, pieceIndices []uint64) *individualWorker {
	w := mockWorker(readDuration)
	w.staticHostPubKeyStr = hostPubKeyStr

	sc, _ := types.SiacoinPrecision.Float64()
	iw := &individualWorker{
		pieceIndices:             pieceIndices,
		staticAvailabilityRate:   availabilityRate,
		staticCost:               sc,
		staticDownloadLaunchTime: time.Now(),
		staticIdentifier:         identifier,
		staticLookupDistribution: *skymodules.NewDistribution(15 * time.Minute),
		staticReadDistribution:   *skymodules.NewDistribution(15 * time.Minute),
		staticWorker:             w,
	}
	return iw
}
