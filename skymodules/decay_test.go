package skymodules

import (
	"testing"
	"time"

	"gitlab.com/NebulousLabs/fastrand"
)

// testDataSet is a test struct that defines a data set with a decay
type testDataSet struct {
	GenericDecay
	data [64]float64
}

// addDataPoint adds a data point and applies decay to the underlying data
func (ds *testDataSet) addDataPoint(point int) {
	ds.Decay(func(decay float64) {
		for i := 0; i < len(ds.data); i++ {
			ds.data[i] *= decay
		}
	})
	ds.data[point%64]++
}

// totalPoints returns the total data points
func (ds *testDataSet) totalPoints() float64 {
	var total float64
	for _, val := range ds.data {
		total += val
	}
	return total
}

// newTestDataSet returns a test data set with given half time
func newTestDataSet(halfTime time.Duration) *testDataSet {
	return &testDataSet{
		GenericDecay: NewDecay(halfTime),
	}
}

// TestDecay is a unit test to verify the functionality of the GenericDecay.
func TestDecay(t *testing.T) {
	t.Parallel()

	t.Run("Decay", testDecay)
	t.Run("DecayedLifetime", testDecayedLifetime)
}

// testAddDecay is a unit test that verify decay is correctly applied to an
// underlying data set.
func testDecay(t *testing.T) {
	t.Parallel()

	// create a test dataset with a decay with a half life of 100 minutes, which
	// means a decay operation should trigger every minute.
	ds := newTestDataSet(time.Minute * 100)

	// add 500 data points
	for i := 0; i < 500; i++ {
		ds.addDataPoint(i)
	}

	// We accept a range of values to compensate for the limited precision of
	// floating points.
	if ds.totalPoints() < 499 || ds.totalPoints() > 501 {
		t.Error("bad", ds.totalPoints())
	}

	// Simulate exactly the half life of time passing.
	ds.lastDecay = time.Now().Add(-100 * time.Minute)
	ds.addDataPoint(fastrand.Intn(64))

	// We accept a range of values to compensate for the limited precision of
	// floating points.
	if ds.totalPoints() < 250 || ds.totalPoints() > 252 {
		t.Error("bad", ds.totalPoints())
	}

	// Simulate exactly one quarter of the half life passing twice.
	ds.lastDecay = time.Now().Add(-50 * time.Minute)
	ds.addDataPoint(fastrand.Intn(64))
	ds.lastDecay = time.Now().Add(-50 * time.Minute)
	ds.addDataPoint(fastrand.Intn(64))

	// We accept a range of values to compensate for the limited precision of
	// floating points.
	if ds.totalPoints() < 126 || ds.totalPoints() > 128 {
		t.Error("bad", ds.totalPoints())
	}
}

// testDecayedLifetime checks that the total counted decayed lifetime of the
// distribution is being tracked correctly.
func testDecayedLifetime(t *testing.T) {
	t.Parallel()

	// Create a test dataset with decay with a half life of 300 minutes, which
	// means a decay operation should trigger every three minutes.
	ds := newTestDataSet(time.Minute * 300)

	// Do 10k steps, each step advancing one minute. Every third step should
	// trigger a decay. Add 1 data point each step.
	for i := 0; i < 10e3; i++ {
		ds.lastDecay = ds.lastDecay.Add(-1 * time.Minute)
		ds.addDataPoint(fastrand.Intn(64))
	}
	pointsPerHour := ds.totalPoints() / (float64(ds.decayedLifetime) / float64(time.Hour))
	// We accept a range of values to compensate for the limited precision of
	// floating points.
	if pointsPerHour < 55 || pointsPerHour > 65 {
		t.Error("bad", pointsPerHour)
	}
}
