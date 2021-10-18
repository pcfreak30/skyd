package renter

import (
	"math"
	"reflect"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/modules"
)

// TestChimeraWorker is a unit test that verifies the functionality of a chimera
// worker.
func TestChimeraWorker(t *testing.T) {
	t.Parallel()

	randLength := fastrand.Uint64n(1 << 24)

	// consideredEqual is a helper function that compares floats using an
	// equality threshold of 1e-9
	consideredEqual := func(a, b float64) bool {
		return math.Abs(a-b) <= 1e-9
	}

	// create a chimera and assert its initial state
	cw := NewChimeraWorker()
	if !cw.cost(randLength).IsZero() {
		t.Fatal("bad")
	}
	if cw.worker() != nil {
		t.Fatal("bad")
	}
	if cw.pieces() != nil {
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
		pieceIndices:           []uint64{1, 2},
		resolveChance:          .2,
		staticReadDistribution: d1,
		staticWorker:           mockWorker(0),
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
		pieceIndices:           []uint64{1, 2},
		resolveChance:          .2,
		staticReadDistribution: d2,
		staticWorker:           mockWorker(0),
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
		pieceIndices:           []uint64{1, 2},
		resolveChance:          .85,
		staticReadDistribution: d3,
		staticWorker:           mockWorker(0),
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

	if cw.cost(fastrand.Uint64n(100)).IsZero() {
		t.Fatal("bad")
	}

	// check the distribution's chance of resolving after 100ms, due to the
	// large weight of the 3rd worker that has a distribution with datapoints
	// over a second, this should be over than 50%
	distribution := cw.distribution()
	if distribution.ChanceAfter(100*time.Millisecond) > .5 {
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
	t.Run("distribution", testIndividualWorker_distribution)
	t.Run("pieces", testIndividualWorker_pieces)
	t.Run("isLaunched", testIndividualWorker_isLaunched)
	t.Run("worker", testIndividualWorker_worker)
	t.Run("split", testIndividualWorker_split)
}

// testIndividualWorker_cost is a unit test for the cost method
func testIndividualWorker_cost(t *testing.T) {
	t.Parallel()

	iw := &individualWorker{staticWorker: mockWorker(0)}
	randLength := fastrand.Uint64n(1 << 24)

	// individual workers that have not been launched yet, should have a cost
	// greather than zero
	cost := iw.cost(randLength)
	if cost.IsZero() {
		t.Fatal("bad")
	}

	iw.launchedAt = time.Now()
	cost = iw.cost(randLength)
	if !cost.IsZero() {
		t.Fatal("bad")
	}
}

// testIndividualWorker_distribution is a unit test for the distribution method
func testIndividualWorker_distribution(t *testing.T) {
	t.Parallel()

	// create a random distribution with 1000 datapoints
	d := skymodules.NewDistribution(time.Minute * 100)
	for i := 0; i < 1000; i++ {
		d.AddDataPoint(time.Duration(fastrand.Uint64n(1000)) * time.Millisecond)
	}

	iw := &individualWorker{staticReadDistribution: d}
	if !reflect.DeepEqual(iw.distribution(), d) {
		t.Fatal("bad")
	}

	// get the chance after 100ms and mock the worker has launched, requesting
	// the distribution from the worker should return a distribution that got
	// shifted by the time since it launched
	chanceAfter100MS := d.ChanceAfter(100 * time.Millisecond)
	iw.launchedAt = time.Now().Add(time.Duration(-500) * time.Millisecond)
	if iw.distribution().ChanceAfter(100*time.Millisecond) >= chanceAfter100MS {
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
	t.Run("NumOverdriveWorkers", testWorkerSetNumOverdriveWorkers)
}

// testWorkerSetAdjustedDuration is a unit test that verifies the functionality
// of the AdjustedDuration method on the worker set.
func testWorkerSetAdjustedDuration(t *testing.T) {
	t.Parallel()

	ws := &workerSet{
		staticExpectedDuration: 100 * time.Millisecond,
		staticLength:           modules.SectorSize,
		staticMinPieces:        1,
	}

	// default ppms
	ppms := skymodules.DefaultSkynetPricePerMS

	// expect no penalty if the ppms exceeds the job cost
	if ws.adjustedDuration(ppms) != ws.staticExpectedDuration {
		t.Fatal("bad")
	}

	// create a worker and ensure its job cost is not zero
	iw1 := &individualWorker{
		staticWorker: mockWorker(10 * time.Millisecond),
	}
	if iw1.cost(ws.staticLength).IsZero() {
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

	// updateReadCostWithFactor is a helper function that enables to alter a
	// worker's cost by multiplying the read length cost of the worker's price
	// table with the given factor
	updateReadCostWithFactor := func(w *individualWorker, factor uint64) {
		pt := w.worker().staticPriceTable().staticPriceTable
		pt.ReadLengthCost = pt.ReadLengthCost.Mul64(factor)
		w.worker().staticPriceTable().staticPriceTable = pt
	}

	// workerAt is a small helper function that returns the worker's identifier
	// at the given index
	workerAt := func(ws *workerSet, index int) string {
		return ws.workers[index].worker().staticHostPubKeyStr
	}

	// create some workers
	iw1 := newTestIndivualWorker("w1", 1, 10*time.Millisecond)
	iw2 := newTestIndivualWorker("w2", 1, 10*time.Millisecond)

	// make both workers more expensive than the default
	updateReadCostWithFactor(iw1, 2)
	updateReadCostWithFactor(iw2, 3)

	// assert the workers are increasingly more expensive
	length := modules.SectorSize
	if !(iw1.cost(length).Cmp(iw2.cost(length)) < 0) {
		t.Fatal("bad")
	}

	// build a worker set
	ws := &workerSet{
		workers:                []downloadWorker{iw1, iw2},
		staticExpectedDuration: 100 * time.Millisecond,
		staticLength:           length,
		staticMinPieces:        1,
	}

	// create a candidate worker and try and build a cheaper set, should return
	// nil because the workers can't resolve a single piece
	cw := newTestIndivualWorker("w3", 1, 10*time.Millisecond)
	if ws.cheaperSetFromCandidate(cw) != nil {
		t.Fatal("bad")
	}

	// make the candidate worker able to resolve a piece, should now return a
	// cheaper set because we will have thrown out the most expensive worker
	// (w2) in favor of w3
	cw.pieceIndices = append(cw.pieceIndices, 3)
	cheaperSet := ws.cheaperSetFromCandidate(cw)
	if cheaperSet == nil {
		t.Fatal("bad")
	}
	if len(cheaperSet.workers) != 2 {
		t.Fatal("bad")
	}
	if workerAt(cheaperSet, 0) != "w1" && workerAt(cheaperSet, 1) != "w3" {
		t.Fatal("bad")
	}

	// continue with the cheaper set as working set
	ws = cheaperSet

	// make the 1st worker resolve piece '1'
	iw1.pieceIndices = append(iw1.pieceIndices, 1)

	// make a new candidate worker that is capable of resolving both piece '1'
	// and '3', to ensure we replace the most expensive one of the two
	cw = newTestIndivualWorker("w4", 1, 10*time.Millisecond)
	cw.pieceIndices = append(cw.pieceIndices, 1, 3)
	cheaperSet = ws.cheaperSetFromCandidate(cw)
	if cheaperSet == nil {
		t.Fatal("bad")
	}
	if len(cheaperSet.workers) != 2 {
		t.Fatal("bad")
	}
	if workerAt(cheaperSet, 0) != "w4" && workerAt(cheaperSet, 1) != "w3" {
		t.Fatal("bad")
	}

	// continue with the cheaper set as working set
	ws = cheaperSet

	// present a worker that is capable of resolving a piece but is more
	// expensive than the workers we have currently
	cw = newTestIndivualWorker("w5", .1, 10*time.Millisecond)
	cw.pieceIndices = append(cw.pieceIndices, 1, 3)
	updateReadCostWithFactor(cw, 3)
	cheaperSet = ws.cheaperSetFromCandidate(cw)
	if cheaperSet != nil {
		t.Fatal("bad")
	}

	// now make it cheaper and try and build a cheaper set from it, assert we
	// now have replaced w4 for w5
	updateReadCostWithFactor(cw, 0)
	cheaperSet = ws.cheaperSetFromCandidate(cw)
	if cheaperSet == nil {
		t.Fatal("bad")
	}
	if len(cheaperSet.workers) != 2 {
		t.Fatal("bad")
	}
	if workerAt(cheaperSet, 0) != "w5" && workerAt(cheaperSet, 1) != "w3" {
		t.Fatal("bad")
	}

	// now do the same thing but set a launch time for w4 to ensure its cost is
	// treated at 0, this should make it so the cheaper worker replaces w3
	ws.workers[0].(*individualWorker).launchedAt = time.Now()
	cheaperSet = ws.cheaperSetFromCandidate(cw)
	if cheaperSet == nil {
		t.Fatal("bad")
	}
	if len(cheaperSet.workers) != 2 {
		t.Fatal("bad")
	}
	if workerAt(cheaperSet, 0) != "w4" && workerAt(cheaperSet, 1) != "w5" {
		t.Fatal("bad")
	}

	// now do the same thing again but launch w3, this should have as a result
	// we can't build a cheaper set
	ws.workers[1].(*individualWorker).launchedAt = time.Now()
	cheaperSet = ws.cheaperSetFromCandidate(cw)
	if cheaperSet != nil {
		t.Fatal("bad")
	}
}

// testWorkerSetClone is a unit test that verifies the
// functionality of the Clone method on the worker set.
func testWorkerSetClone(t *testing.T) {
	t.Parallel()

	// create some workers
	iw1 := newTestIndivualWorker("w1", .1, 100*time.Millisecond)
	iw2 := newTestIndivualWorker("w2", .1, 100*time.Millisecond)
	iw3 := newTestIndivualWorker("w3", .1, 100*time.Millisecond)

	// build a worker set
	ws := &workerSet{
		workers:                []downloadWorker{iw1, iw2, iw3},
		staticExpectedDuration: 100 * time.Millisecond,
		staticLength:           modules.SectorSize,
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
	iw1 := newTestIndivualWorker("w1", .1, 100*time.Millisecond)
	iw2 := newTestIndivualWorker("w2", .1, 100*time.Millisecond)
	iw3 := newTestIndivualWorker("w3", .1, 100*time.Millisecond)
	iw4 := newTestIndivualWorker("w4", .1, 100*time.Millisecond)
	iw5 := newTestIndivualWorker("w5", .1, 100*time.Millisecond)

	// populate their distributions in a way that the output of the distribution
	// becomes predictable by adding a single datapoint to every bucket
	for _, w := range []downloadWorker{iw1, iw2, iw3, iw4, iw5} {
		distribution := w.distribution()
		for i := 0; i < distributionTotalBuckets; i++ {
			point := DistributionDurationForBucketIndex(i)
			distribution.AddDataPoint(point)
		}
	}

	// durationForPct is a helper function that returns a duration at which the
	// chance is exactly the requested percentage, this helper function will
	// also ensure that it is the case and fail otherwise
	durationForPct := func(pct float64) time.Duration {
		index := distributionTotalBuckets * int(pct*100) / 100
		duration := DistributionDurationForBucketIndex(index)

		// assert it against the distribution of the first worker, they are all
		// identical
		if iw1.distribution().ChanceAfter(duration) != pct {
			t.Fatal("bad duration pct")
		}
		return duration
	}

	// build a worker set with 0 overdrive workers
	ws := &workerSet{
		workers:                []downloadWorker{iw1},
		staticExpectedDuration: 100 * time.Millisecond,
		staticLength:           modules.SectorSize,
		staticMinPieces:        2,
	}

	// assert chance is not greater than .5 (it's exactly half)
	if ws.chanceGreaterThanHalf(durationForPct(.5)) {
		t.Fatal("bad")
	}

	// push it right over the 50% mark
	if !ws.chanceGreaterThanHalf(durationForPct(.51)) {
		t.Fatal("bad")
	}

	// add another worker, seeing as the chances are multiplied and both workers
	// have a chance ~= 50% the total chance should not be greater than half
	ws.workers = append(ws.workers, iw2)
	if ws.chanceGreaterThanHalf(durationForPct(.51)) {
		t.Fatal("bad")
	}

	// at 75% chance per worker the total chance should be ~56% which is greater
	// than half
	if !ws.chanceGreaterThanHalf(durationForPct(.75)) {
		t.Fatal("bad")
	}

	// add another worker, this makes it so the worker set has one overdrive
	// worker, which influences the 'chanceGreaterThanHalf' because now we have
	// one worker to spare, increasing our total chance
	ws.workers = append(ws.workers, iw3)
	if ws.numOverdriveWorkers() != 1 {
		t.Fatal("bad")
	}

	// when all workers have exatly a 50% chance, the total chance does NOT
	// exceed 0.5 because it is exactly equal to 0.5
	//
	// 0.5*0.5*0.5 = 0.125 is the chance they're all able to complete after dur
	// 0.125/0.5*0.5 = 0.125 is the chance one is tails and the others are heads
	// 0.125+(0.125*3) = 0.5 because every worker can be the one that's tails
	if ws.chanceGreaterThanHalf(durationForPct(.5)) {
		t.Fatal("bad")
	}

	// now redo the calculation with a duration that's just over half of the
	// distributand assert the chance is now greater than half
	if !ws.chanceGreaterThanHalf(durationForPct(.51)) {
		t.Fatal("bad")
	}

	// add another worker, this makes it so the worker set has two overdrive
	// workers
	ws.workers = append(ws.workers, iw4)
	if ws.numOverdriveWorkers() != 2 {
		t.Fatal("bad")
	}

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
	if ws.chanceGreaterThanHalf(durationForPct(.33)) {
		t.Fatal("bad")
	}

	// doing the same calculations with 0.4 results in:
	// 0,4^4 ~= 0,025
	// (0,6*0,4^3)*4 ~= 0,15
	// (0,6^2*0,4^2)*6 ~= 0,35
	//
	// the toal chance is thus ~= 0.525 which is not greater than half
	if !ws.chanceGreaterThanHalf(durationForPct(.4)) {
		t.Fatal("bad")
	}

	// add another worker, we limit n choose m to 2 because of computational
	// complexity, in the case we have more than 2 overdrive workers we use an
	// approximation where we return true if the sum of all chances is greater
	// than minpieces
	//
	// we have two minpieces and 5 workers so we have to exceed 40% per worker
	ws.workers = append(ws.workers, iw5)
	if ws.chanceGreaterThanHalf(durationForPct(.4)) {
		t.Fatal("bad")
	}
	if !ws.chanceGreaterThanHalf(durationForPct(.41)) {
		t.Fatal("bad")
	}
}

// testWorkerSetNumOverdriveWorkers is a unit test that verifies the
// functionality of the NumOverdriveWorkers method on the worker set.
func testWorkerSetNumOverdriveWorkers(t *testing.T) {
	t.Parallel()

	// create some workers
	iw1 := newTestIndivualWorker("w1", .1, 100*time.Millisecond)
	iw2 := newTestIndivualWorker("w2", .1, 100*time.Millisecond)
	iw3 := newTestIndivualWorker("w3", .1, 100*time.Millisecond)

	// create an empty worker set and assert the basic case
	ws := &workerSet{staticMinPieces: 1}
	if ws.numOverdriveWorkers() != 0 {
		t.Fatal("bad")
	}

	// add one worker and asser the number of overdrive workers is still zero
	ws.workers = append(ws.workers, iw1)
	if ws.numOverdriveWorkers() != 0 {
		t.Fatal("bad")
	}

	// add another worker and assert it now counts as an overdrive worker
	ws.workers = append(ws.workers, iw2)
	if ws.numOverdriveWorkers() != 1 {
		t.Fatal("bad")
	}
	ws.workers = append(ws.workers, iw3)
	if ws.numOverdriveWorkers() != 2 {
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

// newTestIndivualWorker is a helper function that returns an individualWorker
// for testing purposes.
func newTestIndivualWorker(hostPubKeyStr string, resolveChance float64, readDuration time.Duration) *individualWorker {
	w := mockWorker(readDuration)
	w.staticHostPubKeyStr = hostPubKeyStr
	return &individualWorker{
		resolveChance: resolveChance,

		staticReadDistribution: skymodules.NewDistribution(15 * time.Minute),
		staticWorker:           w,
	}
}
