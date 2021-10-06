package renter

import (
	"math"
	"reflect"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
)

// TestChimeraWorker is a unit test that verifies the functionality of a chimera
// worker.
func TestChimeraWorker(t *testing.T) {
	t.Parallel()

	pieceLength := uint64(1 << 16) // 64kb

	// consideredEqual is a helper function that compares floats using an
	// equality threshold of 1e-9
	consideredEqual := func(a, b float64) bool {
		return math.Abs(a-b) <= 1e-9
	}

	// create a chimera and assert its initial state
	cw := NewChimeraWorker(1, pieceLength)
	if cw.worker() != nil {
		t.Fatal("bad")
	}
	if len(cw.pieces()) != 1 {
		t.Fatal("bad")
	}
	if cw.remaining != 1 {
		t.Fatal("bad")
	}

	// add a first individual worker w/distribution that has data points <100ms
	d1 := skymodules.NewDistribution(time.Minute * 100)
	for i := 0; i < 1000; i++ {
		d1.AddDataPoint(time.Duration(fastrand.Uint64n(100)) * time.Millisecond)
	}
	iw1 := &individualWorker{
		pieceIndices:             []uint64{1, 2},
		resolveChance:            .2,
		staticReadDistribution:   d1,
		staticLookupDistribution: skymodules.NewDistribution(time.Minute * 100),
		staticWorker:             mockWorker(0),
	}

	// add the worker and check there's no remainder (the chimera is not
	// complete yet)
	remainder := cw.addWorker(iw1)
	if remainder != nil {
		t.Fatal("bad")
	}
	if !consideredEqual(cw.remaining, 0.8) {
		t.Fatal("bad")
	}

	// add a second individual worker w/distribution that has data points <100ms
	d2 := skymodules.NewDistribution(time.Minute * 100)
	for i := 0; i < 1000; i++ {
		d2.AddDataPoint(time.Duration(fastrand.Uint64n(100)) * time.Millisecond)
	}
	iw2 := &individualWorker{
		pieceIndices:             []uint64{1, 2},
		resolveChance:            .2,
		staticReadDistribution:   d2,
		staticLookupDistribution: skymodules.NewDistribution(time.Minute * 100),
		staticWorker:             mockWorker(0),
	}

	// add the worker and check there's no remainder (the chimera is not
	// complete yet)
	remainder = cw.addWorker(iw2)
	if remainder != nil {
		t.Fatal("bad")
	}
	if !consideredEqual(cw.remaining, 0.6) {
		t.Fatal("bad", cw.remaining)
	}

	// add a third individual worker w/distribution that has data points >1s
	d3 := skymodules.NewDistribution(time.Minute * 100)
	for i := 0; i < 1000; i++ {
		d3.AddDataPoint(time.Duration(fastrand.Uint64n(100)+1000) * time.Millisecond)
	}
	iw3 := &individualWorker{
		pieceIndices:             []uint64{1, 2},
		resolveChance:            .85,
		staticReadDistribution:   d3,
		staticLookupDistribution: skymodules.NewDistribution(time.Minute * 100),
		staticWorker:             mockWorker(0),
	}

	// add the worker and check there's now a remainder, the chimera is complete
	// and we should have a remainder with a chance of .25
	remainder = cw.addWorker(iw3)
	if remainder == nil {
		t.Fatal("bad")
	}
	if !consideredEqual(remainder.resolveChance, 0.25) {
		t.Fatal("bad")
	}
	if cw.remaining != 0 {
		t.Fatal("bad")
	}
	if cw.cost().IsZero() {
		t.Fatal("bad")
	}

	// check the distribution's chance of resolving after 100ms, due to the
	// large weight of the 3rd worker that has a distribution with datapoints
	// over a second, this should be over than 50%
	if cw.staticReadDistribution.ChanceAfter(100*time.Millisecond) > .5 {
		t.Fatal("bad")
	}

	// check that the chimera worker implements the download worker interface
	var _ downloadWorker = (*individualWorker)(nil)
}

// TestIndividualWorker runs a series of unit tests that probe the methods on
// the individual worker object.
func TestIndividualWorker(t *testing.T) {
	t.Parallel()
	t.Run("cost", testIndividualWorker_cost)
	t.Run("pieces", testIndividualWorker_pieces)
	t.Run("isLaunched", testIndividualWorker_isLaunched)
	t.Run("worker", testIndividualWorker_worker)
	t.Run("split", testIndividualWorker_split)
}

// testIndividualWorker_cost is a unit test for the cost method
func testIndividualWorker_cost(t *testing.T) {
	t.Parallel()

	iw := &individualWorker{
		staticWorker:       mockWorker(0),
		staticExpectedCost: types.NewCurrency64(fastrand.Uint64n(10) + 1),
	}

	// cost should equal expected cost for workers that are not launched
	cost := iw.cost()
	if !cost.Equals(iw.staticExpectedCost) {
		t.Fatal("bad")
	}

	// cost should equal zero for workers that are launched
	iw.launchedAt = time.Now()
	cost = iw.cost()
	if !cost.IsZero() {
		t.Fatal("bad")
	}
}

// testIndividualWorker_pieces is a unit test for the pieces method
func testIndividualWorker_pieces(t *testing.T) {
	t.Parallel()

	iw := &individualWorker{}
	if iw.pieces() != nil {
		t.Fatal("bad")
	}

	pieceIndices := []uint64{0, 1, 2}
	iw.pieceIndices = pieceIndices
	if len(iw.pieces()) != len(pieceIndices) {
		t.Fatal("bad")
	}
	for i, piece := range pieceIndices {
		if iw.pieces()[i] != piece {
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

	iw.launchedAt = time.Now()
	if !iw.isLaunched() {
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

// testIndividualWorker_split is a unit test for the split method
func testIndividualWorker_split(t *testing.T) {
	t.Parallel()

	iw := &individualWorker{
		launchedAt:             time.Now(),
		pieceIndices:           []uint64{0},
		resolveChance:          0.055,
		staticReadDistribution: skymodules.NewDistribution(time.Minute * 100),
		staticWorker:           mockWorker(time.Duration(fastrand.Uint64n(99))),
	}

	// verify the resolve chance was split between main and remainder
	chance := 0.0234
	main, remainder := iw.split(chance)
	if main.resolveChance != chance {
		t.Fatal("bad")
	}
	if remainder.resolveChance != iw.resolveChance-chance {
		t.Fatal("bad")
	}

	// verify that all other properties of the individual worker are identical
	if main.launchedAt != remainder.launchedAt {
		t.Fatal("bad")
	}
	if main.staticWorker != remainder.staticWorker {
		t.Fatal("bad")
	}
	if len(main.pieceIndices) != len(remainder.pieceIndices) {
		t.Fatal("bad")
	}
	if !reflect.DeepEqual(main.staticReadDistribution, remainder.staticReadDistribution) {
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
	t.Run("GreaterThanHalf", testWorkerSetGreaterThanHalf)
}

// testWorkerSetAdjustedDuration is a unit test that verifies the functionality
// of the AdjustedDuration method on the worker set.
func testWorkerSetAdjustedDuration(t *testing.T) {
	t.Parallel()

	ws := &workerSet{
		staticExpectedDuration: 100 * time.Millisecond,
		staticMinPieces:        1,
	}

	// default ppms
	ppms := types.SiacoinPrecision.MulFloat(1e-7)

	// expect no penalty if the ppms exceeds the job cost
	if ws.adjustedDuration(ppms) != ws.staticExpectedDuration {
		t.Fatal("bad")
	}

	// create a worker and ensure its job cost is not zero
	iw1 := &individualWorker{
		staticWorker:       mockWorker(10 * time.Millisecond),
		staticExpectedCost: types.SiacoinPrecision,
	}
	if iw1.cost().IsZero() {
		t.Fatal("bad")
	}

	// add the worker and calculate the adjusted duration, ensure a cost penalty
	// has been applied. We don't have to cover the actual cost penalty as that
	// is unit tested by TestAddCostPenalty.
	ws.workers = append(ws.workers, iw1)
	if ws.adjustedDuration(ppms) <= ws.staticExpectedDuration {
		t.Fatal("bad")
	}
}

// testWorkerSetCheaperSetFromCandidate is a unit test that verifies the
// functionality of the CheaperSetFromCandidate method on the worker set.
func testWorkerSetCheaperSetFromCandidate(t *testing.T) {
	t.Parallel()

	sc := types.SiacoinPrecision

	// updateReadCostWithFactor is a helper function that enables altering a
	// worker's cost by multiplying initial cost with the given factor
	updateReadCostWithFactor := func(w *individualWorker, factor uint64) {
		w.staticExpectedCost = sc.Mul64(factor)
	}

	// workerAt is a small helper function that returns the worker's identifier
	// at the given index
	workerAt := func(ws *workerSet, index int) string {
		return ws.workers[index].worker().staticHostPubKeyStr
	}

	// create some workers
	iw1 := newTestIndivualWorker("w1", 1, 10*time.Millisecond, []uint64{1})
	iw2 := newTestIndivualWorker("w2", 1, 10*time.Millisecond, []uint64{2})

	// have w1 and w2 download p1 and p2
	iw1.markPieceForDownload(1)
	iw2.markPieceForDownload(2)

	// update the read cost of both workers
	updateReadCostWithFactor(iw1, 10) // 10SC
	updateReadCostWithFactor(iw2, 20) // 20SC

	// assert the workers are increasingly more expensive
	if !(iw1.cost().Cmp(iw2.cost()) < 0) {
		t.Fatal("bad")
	}

	// build a worker set
	pcws := newTestProjectChunkWorkerSet()
	pdc := newTestProjectDownloadChunk(pcws, nil)
	ws := &workerSet{
		workers:                []downloadWorker{iw1, iw2},
		staticExpectedDuration: 100 * time.Millisecond,
		staticMinPieces:        1,
		staticPDC:              pdc,
	}

	// create w3 without pieces
	iw3 := newTestIndivualWorker("w3", 1, 10*time.Millisecond, []uint64{})

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
	iw4 := newTestIndivualWorker("w4", 1, 10*time.Millisecond, []uint64{1, 3})

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
	iw5 := newTestIndivualWorker("w5", .1, 10*time.Millisecond, []uint64{1})
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
	ws.workers[0].(*individualWorker).launchedAt = time.Now()

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
	iw1 := newTestIndivualWorker("w1", 0, 0, nil)
	iw2 := newTestIndivualWorker("w2", 0, 0, nil)
	iw3 := newTestIndivualWorker("w3", 0, 0, nil)

	// build a worker set
	ws := &workerSet{
		workers:                []downloadWorker{iw1, iw2, iw3},
		staticExpectedDuration: 100 * time.Millisecond,
		staticMinPieces:        1,
	}

	// use reflection to assert "clone" returns an identical object
	clone := ws.clone()
	if !reflect.DeepEqual(clone, ws) {
		t.Fatal("bad")
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
	iw1 := newTestIndivualWorker("w1", 0, 0, nil)
	iw2 := newTestIndivualWorker("w2", 0, 0, nil)
	iw3 := newTestIndivualWorker("w3", 0, 0, nil)
	iw4 := newTestIndivualWorker("w4", 0, 0, nil)
	iw5 := newTestIndivualWorker("w5", 0, 0, nil)

	// populate their distributions in a way that the output of the distribution
	// becomes predictable by adding a single datapoint to every bucket
	for _, w := range []downloadWorker{iw1, iw2, iw3, iw4, iw5} {
		distribution := w.(*individualWorker).staticReadDistribution
		for i := 0; i < distributionTotalBuckets; i++ {
			point := DistributionDurationForBucketIndex(i)
			distribution.AddDataPoint(point)
		}
		w.rebuildChanceAfterCache(time.Now())
	}

	// indexForPct is a helper function that returns the index at which the
	// chance is exactly the requested percentage, this helper function will
	// also ensure that it is the case and fail otherwise
	indexForPct := func(pct float64) int {
		index := distributionTotalBuckets * int(pct*100) / 100
		return index - 1
	}

	// build a worker set with 0 overdrive workers
	ws := &workerSet{
		workers:                []downloadWorker{iw1},
		staticExpectedDuration: 100 * time.Millisecond,
		staticMinPieces:        2,
	}

	// assert chance is not greater than .5 (it's exactly half)
	if ws.chanceGreaterThanHalf(indexForPct(.5)) {
		t.Fatal("bad")
	}

	// push it right over the 50% mark
	if !ws.chanceGreaterThanHalf(indexForPct(.51)) {
		t.Fatal("bad")
	}

	// add another worker, seeing as the chances are multiplied and both workers
	// have a chance ~= 50% the total chance should not be greater than half
	ws.workers = append(ws.workers, iw2)
	if ws.chanceGreaterThanHalf(indexForPct(.51)) {
		t.Fatal("bad")
	}

	// at 75% chance per worker the total chance should be ~56% which is greater
	// than half
	if !ws.chanceGreaterThanHalf(indexForPct(.75)) {
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
	if ws.chanceGreaterThanHalf(indexForPct(.5)) {
		t.Fatal("bad")
	}

	// now redo the calculation with a duration that's just over half of the
	// distributand assert the chance is now greater than half
	if !ws.chanceGreaterThanHalf(indexForPct(.51)) {
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
	if ws.chanceGreaterThanHalf(indexForPct(.33)) {
		t.Fatal("bad")
	}

	// doing the same calculations with 0.4 results in:
	// 0,4^4 ~= 0,025
	// (0,6*0,4^3)*4 ~= 0,15
	// (0,6^2*0,4^2)*6 ~= 0,35
	//
	// the toal chance is thus ~= 0.525 which is not greater than half
	if !ws.chanceGreaterThanHalf(indexForPct(.4)) {
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
	if ws.chanceGreaterThanHalf(indexForPct(.4)) {
		t.Fatal("bad")
	}
	if !ws.chanceGreaterThanHalf(indexForPct(.41)) {
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
	newIndividualWorker := func(resolveChance float64) *individualWorker {
		iw := newTestIndivualWorker("w", resolveChance, 0, []uint64{0})
		return iw
	}

	// create pdc
	pcws := newTestProjectChunkWorkerSet()
	pdc := newTestProjectDownloadChunk(pcws, nil)

	// empty case
	var workers []*individualWorker
	chimeras := pdc.buildChimeraWorkers(workers)
	if len(chimeras) != 0 {
		t.Fatal("bad")
	}

	// add couple of workers with resolve chance not adding up to 1
	workers = append(
		workers,
		newIndividualWorker(0.3),
		newIndividualWorker(0.1),
		newIndividualWorker(0.5),
	)

	// check we still don't have a full chimera
	chimeras = pdc.buildChimeraWorkers(workers)
	if len(chimeras) != 0 {
		t.Fatal("bad")
	}

	// add more workers we should end up with 2 chimeras
	workers = append(
		workers,
		newIndividualWorker(0.4),
		// chimera 1 complete
		newIndividualWorker(0.2),
		newIndividualWorker(0.3),
		newIndividualWorker(0.3),
		// chimera 2 complete, .1 remainder
	)

	// assert we have two chimeras
	chimeras = pdc.buildChimeraWorkers(workers)
	if len(chimeras) != 2 {
		t.Fatal("bad")
	}

	// assert the properties of every chimera worker
	for _, dw := range chimeras {
		cw, ok := dw.(*chimeraWorker)
		if !ok {
			t.Fatal("bad")
		}
		if len(cw.workers) != 4 {
			t.Fatal("bad")
		}
		numPieces := pdc.workerSet.staticErasureCoder.NumPieces()
		if len(cw.staticPieceIndices) != numPieces {
			t.Fatal("bad")
		}
	}

	// add workers with resolve chances of 1
	workers = []*individualWorker{
		newIndividualWorker(1),
		newIndividualWorker(1),
	}

	// check we build 2 chimeras out of them
	chimeras = pdc.buildChimeraWorkers(workers)
	if len(chimeras) != 2 {
		t.Fatal("bad")
	}
}

// TestBuildDownloadWorkers is a unit test that covers the
// 'buildDownloadWorkers' helper function.
func TestBuildDownloadWorkers(t *testing.T) {
	t.Parallel()

	// newIndividualWorker wraps newTestIndivualWorker
	newIndividualWorker := func(resolveChance float64) *individualWorker {
		iw := newTestIndivualWorker("w", resolveChance, 0, []uint64{0})
		iw.hasResolved = resolveChance == 1
		return iw
	}

	// create pdc
	pcws := newTestProjectChunkWorkerSet()
	pdc := newTestProjectDownloadChunk(pcws, nil)

	// empty case
	var workers []*individualWorker
	downloadWorkers := pdc.buildDownloadWorkers(workers)
	if len(downloadWorkers) != 0 {
		t.Fatal("bad")
	}

	// add couple of workers with resolve chance not adding up to 1
	workers = append(
		workers,
		newIndividualWorker(0.3),
		newIndividualWorker(0.1),
		newIndividualWorker(0.5),
	)

	// assert we still don't have a chimera
	downloadWorkers = pdc.buildDownloadWorkers(workers)
	if len(downloadWorkers) != 0 {
		t.Fatal("bad")
	}

	// add more workers we should end up with 2 chimeras
	workers = append(
		workers,
		newIndividualWorker(0.4),
		// chimera 1 complete
		newIndividualWorker(0.2),
		newIndividualWorker(0.3),
		newIndividualWorker(0.3),
		// chimera 2 complete, .1 remainder
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
		newIndividualWorker(0.4),
		newIndividualWorker(1),
		newIndividualWorker(0.6),
		// chimera 3 complete
		newIndividualWorker(1),
	)

	downloadWorkers = pdc.buildDownloadWorkers(workers)
	if len(downloadWorkers) != 5 {
		t.Fatal("bad")
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
	bucketIndex := 0
	workersNeeded := 2
	overdriveWorkers := 0

	// create pdc
	pcws := newTestProjectChunkWorkerSet()
	pdc := newTestProjectDownloadChunk(pcws, nil)

	// mock the worker
	_, pk := crypto.GenerateKeyPair()
	spk := types.SiaPublicKey{
		Algorithm: types.SignatureEd25519,
		Key:       pk[:],
	}
	worker := new(worker)
	worker.staticHostPubKey = spk

	// we will have 3 workers, one resolved one and two chimeras
	iw1 := &individualWorker{
		resolveChance: 1,
		hasResolved:   true,
		pieceIndices:  []uint64{0, 1},
		staticWorker:  worker,
	}
	cw1 := NewChimeraWorker(2, 1<<16)
	cw2 := NewChimeraWorker(2, 1<<16)

	// mock the chance afters, make sure the chimeras have a higher chance,
	// meaning they are more likely to end up in the most likely set
	iw1.cachedChancesAfter[0] = .1
	cw1.cachedChancesAfter[0] = .2
	cw2.cachedChancesAfter[0] = .3

	// helper variables
	iw1Key := iw1.identifier()
	cw1Key := cw1.identifier()
	cw2Key := cw2.identifier()

	// split the workers in most likely and less likely
	workers := []downloadWorker{iw1, cw1, cw2}
	mostLikely, lessLikely := pdc.splitMostlikelyLessLikely(workers, bucketIndex, workersNeeded, overdriveWorkers)

	// expect the most likely to consist of the 2 chimeras
	if len(mostLikely) != workersNeeded {
		t.Fatal("bad")
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
	_, lessLikely = pdc.splitMostlikelyLessLikely(workers, bucketIndex, workersNeeded, 1)
	if len(lessLikely) != 1 {
		t.Fatal("bad")
	}
	lessLikelyKey1 := lessLikely[0].identifier()
	if lessLikelyKey1 != iw1Key {
		t.Fatal("bad")
	}

	// rebuild cw1 and cw2 with only one piece
	cw1 = NewChimeraWorker(1, 1<<16)
	cw2 = NewChimeraWorker(1, 1<<16)
	cw1.cachedChancesAfter[0] = .2
	cw2.cachedChancesAfter[0] = .3
	workers = []downloadWorker{cw1, cw2}

	// assert that we allow overdriving on the same piece if we don't have
	// enough workers to meet the workersNeeded quota
	mostLikely, lessLikely = pdc.splitMostlikelyLessLikely(workers, bucketIndex, workersNeeded, 0)
	if len(mostLikely) != 1 {
		t.Fatal("bad")
	}
	if len(lessLikely) != 0 {
		t.Fatal("bad")
	}

	mostLikely, lessLikely = pdc.splitMostlikelyLessLikely(workers, bucketIndex, workersNeeded, 1)
	if len(mostLikely) != 2 {
		t.Fatal("bad")
	}
	if len(lessLikely) != 0 {
		t.Fatal("bad")
	}
}

// newTestIndivualWorker is a helper function that returns an individualWorker
// for testing purposes.
func newTestIndivualWorker(hostPubKeyStr string, resolveChance float64, readDuration time.Duration, pieceIndices []uint64) *individualWorker {
	w := mockWorker(readDuration)
	w.staticHostPubKeyStr = hostPubKeyStr

	iw := &individualWorker{
		resolveChance: resolveChance,
		pieceIndices:  pieceIndices,

		staticExpectedCost:       types.SiacoinPrecision,
		staticLookupDistribution: skymodules.NewDistribution(15 * time.Minute),
		staticReadDistribution:   skymodules.NewDistribution(15 * time.Minute),
		staticWorker:             w,
		staticIdentifier:         hostPubKeyStr,
	}
	return iw
}
