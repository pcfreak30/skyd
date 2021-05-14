package skymodules

// distributiontracker.go creates a generic tool for tracking the performance of
// some function over time. It keeps a distribution of data points that has a
// time based exponential decay. At any point, the pXX can be requested. For
// example, requesting the p99 will tell you the smallest duration that is
// greater than 99% of all data points. Any float value can be requested between
// 0 and 1.
//
// The distribution tracker has been designed to be high performance, low memory
// overhead, and able to cover a range of values starting at 4ms and going up to
// over an hour. The distribution tracker is bucketed rather than continuous,
// which means the return values won't always be accurate, however they will
// always be accurate to within 4% as long as the result is under 1 hour total
// in duration.
//
// NOTE: There are a handful of hardcoded numbers in this file. This is because
// they have some tricky mathematical relationships to make the bucket ordering
// really nice, and I couldn't find a good way to make them generic. These
// relationships may also enable some performance optimizations in the future
// that wouldn't be possible with generic values.

import (
	"math"
	"sync"
	"time"

	"gitlab.com/SkynetLabs/skyd/build"
)

const (
	// distributionTrackerDecayFrequencyDenom determines what percentage of the
	// half life must expire before a decay is applied. If the decay denominator
	// is '100', it means that the decay will only be applied once 1/100 (1%) of
	// the half life has elapsed.
	//
	// The main reason that the decay is performed in small blocks instead of
	// continuously is the limited precision of floating point numbers. We found
	// that in production, performing a decay after a very tiny amount of time
	// had elapsed resulted in highly inaccurate data, because the floating
	// points rounded the result too heavily. 100 is generally a good value,
	// because it is infrequent enough that the float point precision can still
	// provide strong accuracy, yet frequent enough that a value which has not
	// been decayed recently is still also an accurate value.
	distributionTrackerDecayFrequencyDenom = 100

	// distributionTrackerInitialStepSize defines the step size that we use for the
	// first 64 buckets of a distribution. This means that this is the smallest
	// timing that is supported by the distribution. The highest value supported
	// by the distribution is about 1 million times larger.
	//
	// Decreasing this will give better granularity on things that take very
	// little time, but will also proportionally reduce the maximum amount of
	// time that can be measured. For every time you decrease this value by a
	// factor of 4, you should increase the distributionTrackerNumIncrements by
	// '1' to maintain the same upper bound on the time.
	distributionTrackerInitialStepSize = 4 * time.Millisecond

	// distributionTrackerNumIncrements defines the number of times the stats
	// get incremented.
	distributionTrackerNumIncrements = 7
)

type (
	// Distribution tracks the distribution of durations for a particular half
	// life.
	//
	// NOTE: This struct is not thread safe, thread safety is derived from the
	// parent object.
	Distribution struct {
		// Determines the decay rate of data in the distribution. Zero value
		// here means that no decay is applied.
		staticHalfLife time.Duration

		// Tracks the last time decay was applied so we know if we need to apply
		// another round of decay when adding a datapoint.
		lastDecay time.Time

		// Keeps track of the total amount of time that this distribution has
		// been alive. This time gets decayed alongside the values, which means
		// you can get the total rate of objects being added by dividing the
		// total number of objects by the decayed lifetime.
		decayedLifetime time.Duration

		// Buckets that represent the distribution. The first 64 buckets start
		// at 4ms and are 4ms spaced apart. The next 48 buckets are spaced 16ms
		// apart, then the next 48 are spaced 64ms apart, the spacings
		// multiplying by 4 every 48 buckets. The final bucket is just over an
		// hour, anything over will be put into that bucket as well.
		timings [64 + 48*distributionTrackerNumIncrements]float64
	}

	// DistributionTracker will track the performance distribution of a series
	// of operations over a set of time ranges. Each time range corresponds to a
	// different half life. A common choice is to track the half lives for {15
	// minutes, 24 hours, Lifetime}.
	DistributionTracker struct {
		distributions []*Distribution

		mu sync.Mutex
	}

	// DistributionTrackerStats houses a set of fields that get returned by the
	// DistributionTracker which display the values of the underlying
	// distributions.
	DistributionTrackerStats struct {
		Nines      [][]time.Duration
		DataPoints []float64
	}
)

// timingIndexToDuration converts the index of a timing bucket into a timing.
func distributionDuration(index int) time.Duration {
	if index < 0 || index > 64+48*distributionTrackerNumIncrements {
		build.Critical("distribution duration index out of bounds:", index)
	}

	stepSize := distributionTrackerInitialStepSize
	if index <= 64 {
		return stepSize * time.Duration(index)
	}
	prevMax := stepSize * 64
	for i := 64; i <= 64+48*distributionTrackerNumIncrements; i += 48 {
		stepSize *= 4
		if index < i+48 {
			return stepSize*time.Duration(index-i) + prevMax
		}
		prevMax *= 4
	}

	// The final bucket value.
	return prevMax
}

// addDecay will decay the distribution.
func (d *Distribution) addDecay() {
	// Don't add any decay if the half life is zero.
	if d.staticHalfLife == 0 {
		return
	}

	// Determine if a decay should be applied based on the progression through
	// the half life. We don't apply any decay if not much time has elapsed
	// because applying decay over very short periods taxes the precision of the
	// float64s that we use.
	sincelastDecay := time.Since(d.lastDecay)
	decayFrequency := d.staticHalfLife / distributionTrackerDecayFrequencyDenom
	if sincelastDecay < decayFrequency {
		return
	}

	// Determine how much decay should be applied.
	d.decayedLifetime += time.Since(d.lastDecay)
	strength := float64(sincelastDecay) / float64(d.staticHalfLife)
	mult := math.Pow(0.5, strength)

	// Apply the decay to all values.
	for i := 0; i < len(d.timings); i++ {
		d.timings[i] *= mult
	}
	d.decayedLifetime = time.Duration(float64(d.decayedLifetime) * mult)
	d.lastDecay = time.Now()
}

// AddDataPoint will add a sampled time to the distribution, performing a decay
// operation if needed.
func (d *Distribution) AddDataPoint(dur time.Duration) {
	// Check for negative inputs.
	if dur < 0 {
		build.Critical("cannot call AddDataPoint with negatime timestamp")
		return
	}
	d.addDecay()

	// Determine which bucket to add this datapoint to.
	//
	// The first case is a special case because it covers 64 buckets instead of
	// 48.
	stepSize := distributionTrackerInitialStepSize
	max := stepSize * 64
	if dur < max {
		slot := dur / stepSize
		d.timings[slot]++
		return
	}
	for i := 64; i < 64+48*distributionTrackerNumIncrements; i += 48 {
		stepSize *= 4
		max *= 4
		if dur < max {
			slot := int(dur/stepSize) + i - 16
			d.timings[slot]++
			return
		}
	}
	d.timings[63+48*distributionTrackerNumIncrements]++
}

// PStat will return the timing at which the percentage of requests is lower
// than the provided p. P must be greater than 0 and less than 1.
//
// A bad input will return 0.
func (d *Distribution) PStat(p float64) time.Duration {
	// Check for an error value.
	if p <= 0 || p >= 1 {
		build.Critical("PStat needs to be called with a value inside of the range 0 to 1, used:", p)
		return 0
	}

	// Get the total.
	var total float64
	for i := 0; i < len(d.timings); i++ {
		total += d.timings[i]
	}
	if total == 0 {
		// No data collected, just return the worst case.
		return distributionDuration(len(d.timings))
	}

	// Count up until we reach p.
	var run float64
	var index int
	for run/total < p && index < 64+48*distributionTrackerNumIncrements {
		run += d.timings[index]
		index++
	}

	// Convert i into a duration.
	return distributionDuration(index)
}

// DataPoints returns the total number of data points contained within the
// distribution.
func (d *Distribution) DataPoints() float64 {
	// Decay is not applied automatically. If it has been a while since the last
	// datapoint was added, decay should be applied so that the rates are
	// correct.
	d.addDecay()

	var total float64
	for i := 0; i < len(d.timings); i++ {
		total += d.timings[i]
	}
	return total
}

// AddDataPoint will add a data point to each of the distributions in the
// tracker.
func (dt *DistributionTracker) AddDataPoint(dur time.Duration) {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	for _, tr := range dt.distributions {
		tr.AddDataPoint(dur)
	}
}

// AllNines returns 4 timings for each distribution in the tracker:
//	 + the p90
//	 + the p99
//	 + the p999
//	 + the p9999
func (dt *DistributionTracker) AllNines() [][]time.Duration {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	timings := make([][]time.Duration, len(dt.distributions))
	for i := 0; i < len(timings); i++ {
		timings[i] = make([]time.Duration, 4)
		timings[i][0] = dt.distributions[i].PStat(.9)
		timings[i][1] = dt.distributions[i].PStat(.99)
		timings[i][2] = dt.distributions[i].PStat(.999)
		timings[i][3] = dt.distributions[i].PStat(.9999)
	}
	return timings
}

// DataPoints returns the total number of items represented in each distribution.
func (dt *DistributionTracker) DataPoints() []float64 {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	var totals []float64
	for _, d := range dt.distributions {
		totals = append(totals, d.DataPoints())
	}
	return totals
}

// Stats returns a full suite of statistics about the distributions in the
// tracker.
func (dt *DistributionTracker) Stats() *DistributionTrackerStats {
	return &DistributionTrackerStats{
		Nines:      dt.AllNines(),
		DataPoints: dt.DataPoints(),
	}
}

// NewDistribution will create a distribution with the provided half life.
func NewDistribution(halfLife time.Duration) *Distribution {
	return &Distribution{
		staticHalfLife: halfLife,
		lastDecay:      time.Now(),
	}
}

// NewDistributionTrackerStandard returns a standard distribution tracker, which
// tracks data points over distributions with half lives of 15 minutes, 24
// hours, and 30 days.
func NewDistributionTrackerStandard() *DistributionTracker {
	return &DistributionTracker{
		distributions: []*Distribution{
			NewDistribution(15 * time.Minute),
			NewDistribution(24 * time.Hour),
			NewDistribution(30 * 24 * time.Hour),
		},
	}
}
