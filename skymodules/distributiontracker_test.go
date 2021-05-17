package skymodules

import (
	"testing"
	"time"
)

// TestFullDistributionTracker attempts to use a distribution tracker in full,
// including using actual sleeps instead of artificial clock manipulation.
func TestFullDistributionTracker(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Get the standard distributions but then fix their half lives.
	dt := NewDistributionTrackerStandard()
	dt.distributions[0].staticHalfLife = 5 * time.Second
	dt.distributions[1].staticHalfLife = 20 * time.Second
	dt.distributions[2].staticHalfLife = time.Hour

	// 1000 data points to the first bucket.
	for i := 0; i < 1e3; i++ {
		dt.AddDataPoint(time.Millisecond * 3)
	}
	// Add 100 data points to the third bucket.
	for i := 0; i < 100; i++ {
		dt.AddDataPoint(time.Millisecond * 10)
	}
	// Add 10 data points to the 10th bucket.
	for i := 0; i < 10; i++ {
		dt.AddDataPoint(time.Millisecond * 39)
	}
	// Add 1 data point to the 65th bucket.
	dt.AddDataPoint(time.Millisecond * 266)

	// Check how the distributions seem.
	nines := dt.Percentiles()
	// Each nine should be less than the next, and equal across all
	// distribuitons.
	if nines[0][0] >= nines[0][1] || nines[0][1] >= nines[0][2] || nines[0][2] >= nines[0][3] {
		t.Log(nines)
		t.Error("bad")
	}
	if nines[0][0] != nines[1][0] || nines[1][0] != nines[2][0] {
		t.Log(nines)
		t.Error("bad")
	}
	if nines[0][1] != nines[1][1] || nines[1][1] != nines[2][1] {
		t.Log(nines)
		t.Error("bad")
	}
	if nines[0][2] != nines[1][2] || nines[1][2] != nines[2][2] {
		t.Log(nines)
		t.Error("bad")
	}
	if nines[0][3] != nines[1][3] || nines[1][3] != nines[2][3] {
		t.Log(nines)
		t.Error("bad")
	}

	// Have 20 seconds elpase, and add 2e3 more data points to the first bucket.
	// This should skew the distribution for the first bucket but not the other
	// two.
	time.Sleep(time.Second * 20)
	for i := 0; i < 2e3; i++ {
		dt.AddDataPoint(time.Millisecond * 3)
	}
	nines = dt.Percentiles()
	if nines[0][0] != nines[0][1] {
		t.Log(nines)
		t.Error("bad")
	}
	if nines[0][1] >= nines[1][1] {
		t.Log(nines)
		t.Error("bad")
	}
	if nines[1][1] != nines[2][1] {
		t.Log(nines)
		t.Error("bad")
	}

	// Add 5,000 more entries, this should shift the 20 second bucket but not
	// the 1 hour bucket.
	for i := 0; i < 5e3; i++ {
		dt.AddDataPoint(time.Millisecond * 3)
	}
	nines = dt.Percentiles()
	if nines[0][1] != nines[0][2] {
		t.Log(nines)
		t.Error("bad")
	}
	if nines[1][0] != nines[1][1] {
		t.Log(nines)
		t.Error("bad")
	}
	if nines[1][1] != nines[2][0] {
		t.Log(nines)
		t.Error("bad")
	}
	if nines[2][0] >= nines[2][1] {
		t.Log(nines)
		t.Error("bad")
	}
}

// TestDistributionDecay will test that the distribution is being decayed
// correctly when enough time has passed.
func TestDistributionDecay(t *testing.T) {
	t.Parallel()

	// Create a distribution with a half life of 100 minutes, which means a
	// decay operation should trigger every minute.
	d := NewDistribution(time.Minute * 100)
	totalPoints := func() float64 {
		var total float64
		for i := 0; i < len(d.timings); i++ {
			total += d.timings[i]
		}
		return total
	}

	// Add 500 data points.
	for i := 0; i < 500; i++ {
		// Use different buckets.
		if i%6 == 0 {
			d.AddDataPoint(time.Millisecond)
		} else {
			d.AddDataPoint(time.Millisecond * 100)
		}
	}
	// We accept a range of values to compensate for the limited precision of
	// floating points.
	if totalPoints() < 499 || totalPoints() > 501 {
		t.Error("bad", totalPoints())
	}

	// Simulate exactly the half life of time passing.
	d.lastDecay = time.Now().Add(-100 * time.Minute)
	d.AddDataPoint(time.Millisecond)
	// We accept a range of values to compensate for the limited precision of
	// floating points.
	if totalPoints() < 250 || totalPoints() > 252 {
		t.Error("bad", totalPoints())
	}

	// Simulate exactly one quarter of the half life passing twice.
	d.lastDecay = time.Now().Add(-50 * time.Minute)
	d.AddDataPoint(time.Millisecond)
	d.lastDecay = time.Now().Add(-50 * time.Minute)
	d.AddDataPoint(time.Millisecond)
	// We accept a range of values to compensate for the limited precision of
	// floating points.
	if totalPoints() < 126 || totalPoints() > 128 {
		t.Error("bad", totalPoints())
	}
}

// TestDistributionDecayedLifetime checks that the total counted decayed
// lifetime of the distribution is being tracked correctly.
func TestDistributionDecayedLifetime(t *testing.T) {
	t.Parallel()

	// Create a distribution with a half life of 300 minutes, which means a
	// decay operation should trigger every three minutes.
	d := NewDistribution(time.Minute * 300)
	totalPoints := func() float64 {
		var total float64
		for i := 0; i < len(d.timings); i++ {
			total += d.timings[i]
		}
		return total
	}

	// Do 10k steps, each step advancing one minute. Every third step should
	// trigger a decay. Add 1 data point each step.
	for i := 0; i < 10e3; i++ {
		d.lastDecay = d.lastDecay.Add(-1 * time.Minute)
		d.AddDataPoint(time.Millisecond)
	}
	pointsPerHour := totalPoints() / (float64(d.decayedLifetime) / float64(time.Hour))
	// We accept a range of values to compensate for the limited precision of
	// floating points.
	if pointsPerHour < 55 || pointsPerHour > 65 {
		t.Error("bad", pointsPerHour)
	}
}

// TestDistributionBucketing will check that the distribution is placing timings
// into the right buckets and then reporting the right timings in the pstats.
func TestDistributionBucketing(t *testing.T) {
	t.Parallel()

	// Adding a half life prevents it from decaying every time we add a data
	// point.
	d := NewDistribution(time.Minute * 100)

	// Get a distribution with no data collected.
	if d.PStat(0.55) != distributionDuration(64+48*distributionTrackerNumIncrements) {
		t.Error("expecting a distribution with no data to return the max possible value")
	}

	// Try adding a single datapoint to each bucket, by adding it at the right
	// millisecond offset.
	var i int
	total := time.Millisecond
	for i < 64 {
		d.AddDataPoint(total)
		if d.timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 4 * time.Millisecond
		i++

		pstat := d.PStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.00000001)
		if pstat != time.Millisecond*4 {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.5)
		if pstat != distributionDuration((i+1)/2) {
			t.Error("bad", i, pstat, total)
		}
	}
	for i < 64+48 {
		d.AddDataPoint(total)
		if d.timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 16 * time.Millisecond
		i++

		pstat := d.PStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.00000001)
		if pstat != time.Millisecond*4 {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.5)
		if pstat != distributionDuration((i+1)/2) {
			t.Error("bad", i, pstat, total)
		}
	}
	for i < 64+48*2 {
		d.AddDataPoint(total)
		if d.timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 64 * time.Millisecond
		i++

		pstat := d.PStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.00000001)
		if pstat != time.Millisecond*4 {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.5)
		if pstat != distributionDuration((i+1)/2) {
			t.Error("bad", i, pstat, total)
		}
	}
	for i < 64+48*3 {
		d.AddDataPoint(total)
		if d.timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 256 * time.Millisecond
		i++

		pstat := d.PStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.00000001)
		if pstat != time.Millisecond*4 {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.5)
		if pstat != distributionDuration((i+1)/2) {
			t.Error("bad", i, pstat, total)
		}
	}
	for i < 64+48*4 {
		d.AddDataPoint(total)
		if d.timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 1024 * time.Millisecond
		i++

		pstat := d.PStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.00000001)
		if pstat != time.Millisecond*4 {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.5)
		if pstat != distributionDuration((i+1)/2) {
			t.Error("bad", i, pstat, total)
		}
	}
	for i < 64+48*5 {
		d.AddDataPoint(total)
		if d.timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 4096 * time.Millisecond
		i++

		pstat := d.PStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.00000001)
		if pstat != time.Millisecond*4 {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.5)
		if pstat != distributionDuration((i+1)/2) {
			t.Error("bad", i, pstat, total)
		}
	}
	for i < 64+48*6 {
		d.AddDataPoint(total)
		if d.timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 16384 * time.Millisecond
		i++

		pstat := d.PStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.00000001)
		if pstat != time.Millisecond*4 {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.5)
		if pstat != distributionDuration((i+1)/2) {
			t.Error("bad", i, pstat, total)
		}
	}
	for i < 64+48*7 {
		d.AddDataPoint(total)
		if d.timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 65536 * time.Millisecond
		i++

		pstat := d.PStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.00000001)
		if pstat != time.Millisecond*4 {
			t.Error("bad", i, pstat, total)
		}
		pstat = d.PStat(0.5)
		if pstat != distributionDuration((i+1)/2) {
			t.Error("bad", i, pstat, total)
		}
	}

	// Test off the end of the bucket.
	expectedPStat := total - time.Millisecond
	total += 1e9 * time.Millisecond
	d.AddDataPoint(total)
	pstat := d.PStat(0.99999999999)
	if pstat != expectedPStat {
		t.Error("bad")
	}
	pstat = d.PStat(0.00000001)
	if pstat != distributionTrackerInitialStepSize {
		t.Error("bad", i, pstat, total)
	}
	pstat = d.PStat(0.5)
	if pstat != distributionDuration(((64+48*distributionTrackerNumIncrements)/2)+1) {
		t.Error("bad", pstat, distributionDuration(201))
	}
}
