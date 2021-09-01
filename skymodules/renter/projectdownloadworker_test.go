package renter

import (
	"math"
	"reflect"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/skymodules"
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
		staticPieceIndices:     []uint64{1, 2},
		staticResolveChance:    .2,
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
		staticPieceIndices:     []uint64{1, 2},
		staticResolveChance:    .2,
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
		staticPieceIndices:     []uint64{1, 2},
		staticResolveChance:    .85,
		staticReadDistribution: d3,
		staticWorker:           mockWorker(0),
	}

	// add the worker and check there's now a remainder, the chimera is complete
	// and we should have a remainder with a chance of .25
	remainder = cw.addWorker(iw3)
	if remainder == nil {
		t.Fatal("bad")
	}
	if !consideredEqual(remainder.staticResolveChance, 0.25) {
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

	iw.staticLaunchedAt = time.Now()
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
	iw.staticLaunchedAt = time.Now().Add(time.Duration(-500) * time.Millisecond)
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
	iw.staticPieceIndices = pieceIndices
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

	iw.staticLaunchedAt = time.Now()
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
		staticLaunchedAt:       time.Now(),
		staticPieceIndices:     []uint64{0},
		staticResolveChance:    0.055,
		staticReadDistribution: skymodules.NewDistribution(time.Minute * 100),
		staticWorker:           mockWorker(time.Duration(fastrand.Uint64n(99))),
	}

	// verify the resolve chance was split between main and remainder
	chance := 0.0234
	main, remainder := iw.split(chance)
	if main.staticResolveChance != chance {
		t.Fatal("bad")
	}
	if remainder.staticResolveChance != iw.staticResolveChance-chance {
		t.Fatal("bad")
	}

	// verify that all other properties of the individual worker are identical
	if main.staticLaunchedAt != remainder.staticLaunchedAt {
		t.Fatal("bad")
	}
	if main.staticWorker != remainder.staticWorker {
		t.Fatal("bad")
	}
	if len(main.staticPieceIndices) != len(remainder.staticPieceIndices) {
		t.Fatal("bad")
	}
	if !reflect.DeepEqual(main.staticReadDistribution, remainder.staticReadDistribution) {
		t.Fatal("bad")
	}
}
