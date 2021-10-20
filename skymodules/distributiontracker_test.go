package skymodules

import (
	"math"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/fastrand"
)

// TestDistributionTracker is a collection of unit test that verify the
// functionality of the distribution tracker.
func TestDistributionTracker(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	t.Run("Bucketing", testDistributionBucketing)
	t.Run("ChanceAfter", testDistributionChanceAfter)
	t.Run("ChancesAfter", testDistributionChancesAfter)
	t.Run("ChanceAfterShift", testDistributionChanceAfterShift)
	t.Run("Clone", testDistributionClone)
	t.Run("Decay", testDistributionDecay)
	t.Run("DecayedLifetime", testDistributionDecayedLifetime)
	t.Run("ExpectedDuration", testDistributionExpectedDuration)
	t.Run("FullTestLong", testDistributionTrackerFullTestLong)
	t.Run("Helpers", testDistributionHelpers)
	t.Run("MergeWith", testDistributionMergeWith)
	t.Run("Shift", testDistributionShift)
}

// testDistributionBucketing will check that the distribution is placing timings
// into the right buckets and then reporting the right timings in the pstats.
func testDistributionBucketing(t *testing.T) {
	t.Parallel()

	// Adding a half life prevents it from decaying every time we add a data
	// point.
	d := NewDistribution(time.Minute * 100)

	// Get a distribution with no data collected.
	if d.PStat(0.55) != DistributionDurationForBucketIndex(DistributionTrackerTotalBuckets-1) {
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
		if pstat != DistributionDurationForBucketIndex((i+1)/2) {
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
		if pstat != DistributionDurationForBucketIndex((i+1)/2) {
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
		if pstat != DistributionDurationForBucketIndex((i+1)/2) {
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
		if pstat != DistributionDurationForBucketIndex((i+1)/2) {
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
		if pstat != DistributionDurationForBucketIndex((i+1)/2) {
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
		if pstat != DistributionDurationForBucketIndex((i+1)/2) {
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
		if pstat != DistributionDurationForBucketIndex((i+1)/2) {
			t.Error("bad", i, pstat, total)
		}
	}
	for i < 64+48*7-1 {
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
		if pstat != DistributionDurationForBucketIndex((i+1)/2) {
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
	if pstat != DistributionDurationForBucketIndex(DistributionTrackerTotalBuckets/2) {
		t.Error("bad", pstat, DistributionDurationForBucketIndex(DistributionTrackerTotalBuckets/2))
	}
}

// testDistributionChanceAfter will test the `ChanceAfter` method on the
// distribution tracker.
func testDistributionChanceAfter(t *testing.T) {
	t.Parallel()

	d := NewDistribution(time.Minute * 100)
	ms := time.Millisecond

	// verify the chance is zero if we don't have any datapoints
	chance := d.ChanceAfter(time.Duration(0))
	if chance != 0 {
		t.Fatal("bad")
	}
	chance = d.ChanceAfter(time.Second)
	if chance != 0 {
		t.Fatal("bad")
	}

	// add some datapoints below 100ms
	for i := 0; i < 100; i++ {
		d.AddDataPoint(time.Duration(fastrand.Intn(100)) * ms)
	}

	// verify we have a 100% chance of coming in after 100ms
	chance = d.ChanceAfter(100 * ms)
	if chance != 1 {
		t.Fatal("bad")
	}

	// verify the chance is somewhere between 0 and 1 for random durations
	for i := 0; i < 100; i++ {
		randomDur := time.Duration(fastrand.Intn(100)) * ms
		chance = d.ChanceAfter(randomDur)
		if !(chance >= 0 && chance <= 1) {
			t.Fatal("bad", chance, randomDur)
		}
	}

	// verify the chance increases if the duration increases
	prev := float64(0)
	for i := 0; i < 100; i += 10 {
		chance = d.ChanceAfter(time.Duration(i) * ms)
		if chance < prev {
			t.Fatal("bad", chance, prev)
		}
		prev = chance
	}

	// verify the chance is deterministic
	randomDur := time.Duration(fastrand.Intn(100)) * ms
	if d.ChanceAfter(randomDur) != d.ChanceAfter(randomDur) {
		t.Fatal("bad")
	}

	// reset the distribution and add a datapoint in every bucket
	d = NewDistribution(time.Minute * 100)
	for i := 0; i < DistributionTrackerTotalBuckets; i++ {
		d.AddDataPoint(DistributionDurationForBucketIndex(i))
	}

	// assert the chance at every bucket equals the sum of all data points in
	// buckets up until then divided by the total amount of data points
	for i := 0; i < DistributionTrackerTotalBuckets; i++ {
		if d.ChanceAfter(DistributionDurationForBucketIndex(i)) != float64(i)/d.DataPoints() {
			t.Fatal("bad", i)
		}
	}
}

// testDistributionChancesAfter will test the `ChancesAfter` method on the
// distribution tracker.
func testDistributionChancesAfter(t *testing.T) {
	t.Parallel()

	// add a datapoint to every bucket
	d := NewDistribution(time.Minute * 100)
	for i := 0; i < DistributionTrackerTotalBuckets; i++ {
		d.AddDataPoint(DistributionDurationForBucketIndex(i))
	}

	// verify chances after equals chance after duration and corresponding index
	chances := d.ChancesAfter()
	for i := 0; i < 400; i++ {
		if d.ChanceAfter(DistributionDurationForBucketIndex(i)) != chances[i] {
			t.Fatal("bad")
		}
	}
}

// testDistributionChanceAfterShift will test the `ChanceAfter` method on the
// distribution tracker after a `Shift` has been applied to the distribution.
func testDistributionChanceAfterShift(t *testing.T) {
	t.Parallel()

	d := NewDistribution(time.Minute * 100)
	for i := 0; i < 100; i++ {
		d.AddDataPoint(time.Duration(i) * time.Millisecond)
	}

	if d.ChanceAfter(90*time.Millisecond) != .9 {
		t.Fatal("bad")
	}

	d.Shift(80 * time.Millisecond)
	chanceAfter := d.ChanceAfter(90 * time.Millisecond)
	if chanceAfter != 0.5 {
		t.Fatal("bad", chanceAfter)
	}

	d.Shift(100 * time.Millisecond)
	chanceAfter = d.ChanceAfter(100 * time.Millisecond)
	if chanceAfter != 0 {
		t.Fatal("bad", chanceAfter)
	}
}

// testDistributionClone will test the `Clone` method on the distribution
// tracker.
func testDistributionClone(t *testing.T) {
	t.Parallel()

	d := NewDistribution(time.Minute * 100)

	// add 1000 random data points
	ms := time.Millisecond
	for i := 0; i < 1000; i++ {
		d.AddDataPoint(time.Duration(fastrand.Intn(100)) * ms)
	}

	// clone the distributions
	c := d.Clone()

	// assert the distribution's properties were copied over
	if c.staticHalfLife != d.staticHalfLife {
		t.Fatal("bad")
	}
	if c.decayedLifetime != d.decayedLifetime {
		t.Fatal("bad")
	}
	if c.lastDecay != d.lastDecay {
		t.Fatal("bad")
	}

	// assert the datapoints and percentiles are identical
	if c.DataPoints() != d.DataPoints() {
		t.Fatal("bad")
	}
	if c.PStat(.9) != d.PStat(.9) {
		t.Fatal("bad")
	}

	// add more datapoints to the original distribution
	for i := 0; i < 1000; i++ {
		d.AddDataPoint(time.Duration(fastrand.Intn(100)) * ms)
	}

	// assert the original distribution diverged from the clone
	if c.DataPoints() == d.DataPoints() {
		t.Fatal("bad")
	}
}

// testDistributionDecay will test that the distribution is being decayed
// correctly when enough time has passed.
func testDistributionDecay(t *testing.T) {
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

// testDistributionDecayedLifetime checks that the total counted decayed
// lifetime of the distribution is being tracked correctly.
func testDistributionDecayedLifetime(t *testing.T) {
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

// testDistributionExpectedDuration will test that the distribution correctly
// returns the expected duration based upon all data points in the distribution.
func testDistributionExpectedDuration(t *testing.T) {
	t.Parallel()

	d := NewDistribution(time.Minute * 100)
	ms := time.Millisecond

	// check whether we default to the worst case if we have 0 data points
	expected := d.ExpectedDuration()
	if expected != DistributionDurationForBucketIndex(len(d.timings)-1) {
		t.Error("bad")
	}

	// add a first data point
	duration := 8 * ms
	d.AddDataPoint(duration)
	expected = d.ExpectedDuration()
	if expected != duration {
		t.Error("bad")
	}

	// now add 1000 datapoints, between 1-50ms
	for i := 0; i < 1000; i++ {
		d.AddDataPoint(time.Duration(fastrand.Uint64n(50)+1) * ms)
	}
	expected = d.ExpectedDuration()
	if expected < 22*ms || expected > 28*ms {
		t.Error("bad")
	}

	// add 1000 more datapoints, between 51 and 100ms
	for i := 0; i < 1000; i++ {
		d.AddDataPoint(time.Duration(fastrand.Uint64n(100)+50) * ms)
	}

	// assert the expected duration increased
	expected = d.ExpectedDuration()
	if expected < 50*ms || expected > 75*ms {
		t.Error("bad")
	}

	// reset the distribution
	d = NewDistribution(time.Minute * 100)

	// add one datapoint to every bucket and keep track of the total duration
	var totalDuration int64
	for i := 0; i < DistributionTrackerTotalBuckets; i++ {
		point := DistributionDurationForBucketIndex(i)
		totalDuration += point.Nanoseconds()
		d.AddDataPoint(point)
	}

	// the expected duration is the sum of the duration of all buckets
	// multiplied by the % chance a datapoint is in that bucket, because we
	// added exactly one datapoint to every bucket, the pct chance will be
	// the same across all buckets, namely 1/DistributionTrackerTotalBuckets
	pctChance := float64(1) / float64(DistributionTrackerTotalBuckets)
	expected = time.Duration(pctChance * float64(totalDuration))
	if d.ExpectedDuration() != expected {
		t.Error("bad", d.ExpectedDuration(), expected)
	}

	// add 100 datapoints to the first and last bucket
	firstBucketDur := DistributionDurationForBucketIndex(0)
	lastBucketDur := DistributionDurationForBucketIndex(DistributionTrackerTotalBuckets - 1)
	for i := 0; i < 100; i++ {
		d.AddDataPoint(firstBucketDur)
		d.AddDataPoint(lastBucketDur)
	}

	// now calculate the expected duration from the first and lust bucket
	// separately and sum it with the expected durations of the other buckets,
	// we have to this into account in the total duration
	totalDuration -= firstBucketDur.Nanoseconds()
	totalDuration -= lastBucketDur.Nanoseconds()

	pctChanceFirst := float64(101) / float64(d.DataPoints())
	pctChanceLast := float64(101) / float64(d.DataPoints())
	pctChanceOther := float64(1) / float64(d.DataPoints())

	expectedFirst := pctChanceFirst * float64(firstBucketDur)
	expectedLast := pctChanceLast * float64(lastBucketDur)
	expectedOthers := pctChanceOther * float64(totalDuration)

	expected = time.Duration(expectedFirst + expectedOthers + expectedLast)
	if d.ExpectedDuration() != expected {
		t.Error("bad", d.ExpectedDuration(), expected)
	}
}

// testDistributionTrackerFullTestLong attempts to use a distribution tracker in
// full, including using actual sleeps instead of artificial clock manipulation.
func testDistributionTrackerFullTestLong(t *testing.T) {
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

// testDistributionMergeWith verifies the 'MergeWith' method on the
// distribution.
func testDistributionMergeWith(t *testing.T) {
	t.Parallel()

	// create a new distribution
	d := NewDistribution(time.Minute * 100)
	ms := time.Millisecond

	// add some random data points
	for i := 0; i < 1000; i++ {
		d.AddDataPoint(time.Duration(fastrand.Intn(i+1)) * ms)
	}

	// get the chance at a random duration that's expected to be non zero
	randDur := time.Duration(fastrand.Intn(500)+250) * ms
	chance := d.ChanceAfter(randDur)
	if chance == 0 {
		t.Error("bad")
	}

	// create another distribution and merge it, use 1 as weight, seeing as the
	// other distribution has no datapoints in it, the distribution won't change
	other := NewDistribution(time.Minute * 100)
	d.MergeWith(other, 1)
	if d.ChanceAfter(randDur) != chance {
		t.Fatal("unexpected")
	}

	// get the expected duration and verify it's lowers after merging with a
	// distribution that has more datapoints on the lower end
	expectedDur := d.ExpectedDuration()
	for i := 0; i < 1000; i++ {
		other.AddDataPoint(time.Duration(fastrand.Intn(i/4+1)) * ms)
	}
	d.MergeWith(other, 1)
	if d.ExpectedDuration() >= expectedDur {
		t.Fatal("unexpected")
	}

	// create a clean distribution and merge it with 3 distributions using a
	// weight of 33% every time, the 3 distributions have datapoints below 1s,
	// between 1s and 2s, and between 2s and 3s respectively
	clean := NewDistribution(time.Minute * 100)
	d1s := NewDistribution(time.Minute * 100)
	for i := 0; i < 1000; i++ {
		d1s.AddDataPoint(time.Duration(fastrand.Intn(1000)) * ms)
	}
	clean.MergeWith(d1s, .33)
	d2s := NewDistribution(time.Minute * 100)
	for i := 0; i < 1000; i++ {
		d2s.AddDataPoint(time.Duration(fastrand.Intn(1000)+1000) * ms)
	}
	clean.MergeWith(d2s, .33)
	d3s := NewDistribution(time.Minute * 100)
	for i := 0; i < 1000; i++ {
		d3s.AddDataPoint(time.Duration(fastrand.Intn(1000)+2000) * ms)
	}
	clean.MergeWith(d3s, .33)

	// assert the chance after 1,2 and 3s is more or less equal to 33%, 66%, 99%
	chanceAfter1s := clean.ChanceAfter(time.Second)
	if chanceAfter1s < 0.31 || chanceAfter1s > 0.35 {
		t.Fatal("unexpected", chanceAfter1s)
	}
	chanceAfter2s := clean.ChanceAfter(2 * time.Second)
	if chanceAfter2s < 0.64 || chanceAfter2s > 0.68 {
		t.Fatal("unexpected", chanceAfter2s)
	}
	chanceAfter3s := clean.ChanceAfter(3 * time.Second)
	if chanceAfter3s < 0.97 {
		t.Fatal("unexpected", chanceAfter3s)
	}

	// create two distributions
	d1 := NewDistribution(time.Minute * 100)
	d2 := NewDistribution(time.Minute * 100)

	// add a datapoint in every bucket
	for i := 0; i < DistributionTrackerTotalBuckets; i++ {
		d1.AddDataPoint(DistributionDurationForBucketIndex(i))
		d2.AddDataPoint(DistributionDurationForBucketIndex(i))
	}
	if d1.DataPoints() != DistributionTrackerTotalBuckets {
		t.Fatal("unexpected")
	}
	if d2.DataPoints() != DistributionTrackerTotalBuckets {
		t.Fatal("unexpected")
	}
	if d1.ExpectedDuration() != d2.ExpectedDuration() {
		t.Fatal("unexpected")
	}
	oldExpectedDuration := d1.ExpectedDuration()

	// merge the two distributions using a weight of .5
	d1.MergeWith(d2, .5)
	if d1.DataPoints() != 1.5*DistributionTrackerTotalBuckets {
		t.Fatal("unexpected")
	}
	if d2.DataPoints() != DistributionTrackerTotalBuckets {
		t.Fatal("unexpected")
	}

	// assert the expected duration has not changed, the expected duration
	// relies on the pct chance a datapoint is in some bucket, seeing as the
	// datapoints are evenly distributed, this has not changed
	if d1.ExpectedDuration() != oldExpectedDuration {
		t.Fatal("unexpected")
	}

	// create a third and fourth distribution with datapoints in the lower and
	// up half of the buckets
	d3 := NewDistribution(time.Minute * 100)
	d4 := NewDistribution(time.Minute * 100)
	var totalDurFirstHalf int64
	var totalDurSecondHalf int64
	for i := 0; i < DistributionTrackerTotalBuckets; i++ {
		point := DistributionDurationForBucketIndex(i)
		if i < DistributionTrackerTotalBuckets/2 {
			d3.AddDataPoint(point)
			totalDurFirstHalf += point.Nanoseconds()
		} else {
			d4.AddDataPoint(DistributionDurationForBucketIndex(i))
			totalDurSecondHalf += point.Nanoseconds()
		}
	}

	// merge the third distribution using a weight of 1, manually calculate the
	// expected duration and compare it with the actual value
	d1.MergeWith(d3, 1)
	if d1.ExpectedDuration() >= oldExpectedDuration {
		t.Fatal("unexpected")
	}
	pctChanceFirst := 2.5 / d1.DataPoints()
	pctChanceSecond := 1.5 / d1.DataPoints()
	if d1.ExpectedDuration() != time.Duration(pctChanceFirst*float64(totalDurFirstHalf)+pctChanceSecond*float64(totalDurSecondHalf)) {
		t.Fatal("unexpected")
	}

	// merge the fourth distribution, again using a weight of 1, the expect
	// duration should be back to normal because we're evenly distributed again
	d1.MergeWith(d4, 1)
	if d1.ExpectedDuration() != oldExpectedDuration {
		t.Fatal("unexpected")
	}
	pctChanceFirst = 2.5 / d1.DataPoints()
	pctChanceSecond = 2.5 / d1.DataPoints()
	if d1.ExpectedDuration() != time.Duration(pctChanceFirst*float64(totalDurFirstHalf)+pctChanceSecond*float64(totalDurSecondHalf)) {
		t.Fatal("unexpected")
	}
}

// testDistributionShift verifies the 'Shift' method on the distribution.
func testDistributionShift(t *testing.T) {
	t.Parallel()

	// create a new distribution
	d := NewDistribution(time.Minute * 100)
	ms := time.Millisecond

	// add some datapoints below 896ms (896 perfectly aligns with a bucket)
	for i := 0; i < 1000; i++ {
		d.AddDataPoint(time.Duration(fastrand.Uint64n(896)) * ms)
	}

	// check the chance is 1
	chance := d.ChanceAfter(896 * ms)
	if chance != 1 {
		t.Fatal("bad")
	}

	// calculate the chance after 576ms, expect it to be hovering around 65%
	chance = d.ChanceAfter(576 * ms)
	if !(chance > .55 && chance < .75) {
		t.Fatal("bad")
	}

	// shift the distribution by 0ms - it should have no effect
	d.Shift(time.Duration(0))
	chanceAfterShift := d.ChanceAfter(576 * ms)
	if chanceAfterShift != chance {
		t.Fatal("bad")
	}

	// shift the distribution by 576ms, this is perfectly aligned with a bucket
	// so there'll be no fractionalised value that's being smeared over all
	// buckets preceding it
	d.Shift(time.Duration(576) * ms)
	chanceAfterShift = d.ChanceAfter(576 * ms)
	if chanceAfterShift != 0 {
		t.Fatal("bad")
	}

	// verify the chance after 896ms is still one
	chance = d.ChanceAfter(896 * ms)
	if chance != 1 {
		t.Fatal("bad")
	}

	// get a random chance value between 576 and 896ms, verify shifting the
	// distribution below the 576ms essentially is a no-op
	randDur := time.Duration(fastrand.Intn(896-576) + 576)
	chance = d.ChanceAfter(randDur)
	for i := 0; i < 550; i += 50 {
		d.Shift(time.Duration(i) * ms)
		if d.ChanceAfter(randDur) != chance {
			t.Fatal("bad")
		}
	}

	// verify initial buckets are empty
	if d.ChanceAfter(16*ms) != 0 {
		t.Fatal("bad")
	}

	// shift until we hit a bucket that got fractionalised and smeared across
	// all buckets preceding it, we start at 804ms and go up with steps of 8ms
	// to ensure we shift at a duration that induces a fractionalised shift
	for i := 804; i < 896; i += 8 {
		_, fraction := indexForDuration(time.Duration(i) * ms)
		if fraction == 0 {
			continue
		}

		d.Shift(time.Duration(i) * ms)
		if d.ChanceAfter(16*ms) > 0 {
			break
		}
	}

	// verify the shift fractionalised a bucket and smeared the remainder over
	// all buckets preceding the one at which we shifted.
	if d.ChanceAfter(16*ms) == 0 {
		t.Fatal("bad")
	}

	// reset the new distribution
	d = NewDistribution(time.Minute * 100)

	// add one datapoint in every bucket
	for i := 0; i < DistributionTrackerTotalBuckets; i++ {
		d.AddDataPoint(DistributionDurationForBucketIndex(i))
	}

	// shift it by 100 buckets and assert all buckets are completely empty,
	// there was no smear because the shift aligned perfectly with a bucket
	d.Shift(DistributionDurationForBucketIndex(100))
	for i := 0; i < 100; i++ {
		if d.ChanceAfter(DistributionDurationForBucketIndex(i)) != 0 {
			t.Fatal("bad")
		}
	}

	// shift it again but now make sure we shift at a fraction of a bucket so we
	// should see a remainder value smeared out across all preceding buckets
	shiftAt := DistributionDurationForBucketIndex(200) + (256/2)*ms

	// quickly assert that we're shifting at the exact point we want to shift,
	// namely at bucket index 200 and we want to make sure we're at exactly 50%
	// of that bucket, which is a 256ms bucket.
	index, fraction := indexForDuration(shiftAt)
	if index != 200 || fraction != .5 {
		t.Fatal("bad")
	}

	// perform the shift
	d.Shift(shiftAt)

	// we expected to see a smear of 1/2/200 because we are smearing half of the
	// original value over the buckets before it
	smear := float64(1) / float64(400)

	// we know the value now in all buckets up until bucket with index 200, so
	// the chance after every duration will increase with the same amount, let's
	// calculate the amount for the bucket at index 1 which is the step with
	// which we'll be increasing
	index, fraction = indexForDuration(4 * ms)
	if index != 1 && fraction != 1 {
		t.Fatal("bad")
	}

	// compare the expected chance with the actual chance, allow for some
	// floating point precision errors up until 1e-9
	for i := 1; i < 200; i++ {
		chance = smear * float64(i) / d.DataPoints()
		if math.Abs(chance-d.ChanceAfter(DistributionDurationForBucketIndex(i))) > 1e-9 {
			t.Fatal("bad", i, chance, d.ChanceAfter(DistributionDurationForBucketIndex(i)))
		}
	}
}

// testDistributionHelpers probes the `indexForDuration` helper function.
func testDistributionHelpers(t *testing.T) {
	t.Parallel()

	ms := time.Millisecond

	// verify some duration values up until the "initial buckets"
	// there's 64 initial buckets using a 4ms step size
	index, fraction := indexForDuration(0)
	if index != 0 || fraction != 0 {
		t.Error("bad")
	}
	index, fraction = indexForDuration(16 * ms)
	if index != 4 || fraction != 0 {
		t.Error("bad")
	}
	index, fraction = indexForDuration(65 * ms)
	if index != 16 || fraction != 0.25 {
		t.Error("bad")
	}
	index, fraction = indexForDuration(255 * ms)
	if index != 63 || fraction != 0.75 {
		t.Error("bad")
	}

	// verify some durations where the stepsize is 16ms

	// 64x4ms buckets + 22x16ms buckets = 608ms mark
	// meaning we are 12ms into the next 16ms bucket which is 75%
	index, fraction = indexForDuration(620 * ms)
	if index != 86 || fraction != 0.75 {
		t.Error("bad")
	}

	// 64x4ms buckets + 40x16ms buckets = 896ms mark
	// meaning we are 0ms into the next bucket
	index, fraction = indexForDuration(896 * ms)
	if index != 104 || fraction != 0 {
		t.Error("bad")
	}

	// verify some durations where the stepsize is 64ms

	// 64x4ms buckets + 48x16ms buckets + 15*64buckets = 1984ms mark
	// meaning we are 16ms into the next bucket which is 25%
	index, fraction = indexForDuration(2000 * ms)
	if index != 127 || fraction != 0.25 {
		t.Error("bad")
	}

	// verify upper bound
	index, fraction = indexForDuration(time.Hour + 10*time.Minute)
	if index != 399 || fraction != 1 {
		t.Error("bad", index, fraction)
	}
}
