package skymodules

// skynetperf2.go creates a generic performance tracking object which can absorb
// datapoints and return a distribution for how they occur. This struct was
// created with both performance in mind, and also the limited precision of
// floating points in mind.
//
// NOTE: There are a lot of values here that feel like they should be constants,
// but I've elected not to make them constants because they are all highly
// dependent on each other, and I don't want to give the impression that you can
// tweak them.

import (
	"math"
	"sync"
	"time"

	"gitlab.com/SkynetLabs/skyd/build"
)

type (
	// Distribution tracks the distribution of request timings for a particular
	// half life.
	//
	// A distribution is useful for tracking requests over a range of times from
	// 4ms to about one hour.
	//
	// NOTE: This struct is not thread safe, thread safety is derived from the
	// parent object.
	Distribution struct {
		// Determines the decay rate of data in the distribution. Zero value
		// here means that no decay is applied.
		StaticHalfLife time.Duration

		// Tracks the last time decay was applied so we know if we need to apply
		// another round of decay when adding a datapoint.
		LastDecay time.Time

		// Keeps track of the total amount of time that this distribution has
		// been alive. This time gets decayed alongside the values, which means
		// you can get the total rate of objects being added by dividing the
		// total number of objects by the decayed lifetime.
		DecayedLifetime time.Duration
		PreviousUpdate  time.Time

		// 400 buckets that represent the distribution. The first 64 buckets
		// start at 4ms and are 4ms spaced apart. The next 48 buckets are spaced
		// 16ms apart, then the next 48 are spaced 64ms apart, the spacings
		// multiplying by 4 every 48 buckets. The final bucket is just over an
		// hour, anything over will be put into that bucket as well.
		Timings [400]float64
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
		Nines           [][]time.Duration
		TotalDataPoints []float64
	}
)

// AddDataPoint will add a sampled time to the distribution, performing a decay
// operation if needed.
func (d *Distribution) AddDataPoint(dur time.Duration) {
	// Check for negative inputs.
	if dur < 0 {
		build.Critical("cannot call AddDataPoint with negatime timestamp")
		return
	}

	sinceLastDecay := time.Since(d.LastDecay)
	d.DecayedLifetime += time.Since(d.PreviousUpdate)
	d.PreviousUpdate = time.Now()
	decayFrequency := d.StaticHalfLife / 100
	if sinceLastDecay > decayFrequency {
		// Perform a decay operation.
		strength := float64(sinceLastDecay) / float64(d.StaticHalfLife)
		mult := math.Pow(0.5, strength)
		for i := 0; i < len(d.Timings); i++ {
			d.Timings[i] *= mult
		}
		d.DecayedLifetime = time.Duration(float64(d.DecayedLifetime) * mult)
		d.LastDecay = time.Now()
	}

	// Determine which bucket to add this datapoint to.
	//
	// The first case is a special case because it covers 64 buckets instead of
	// 48.
	stepSize := time.Millisecond * 4
	max := time.Millisecond * 256
	if dur < max {
		slot := dur / stepSize
		d.Timings[slot]++
		return
	}
	for i := 64; i < 400; i += 48 {
		stepSize *= 4
		max *= 4
		if dur < max {
			slot := int(dur/stepSize) + i - 16
			d.Timings[slot]++
			return
		}
	}
	d.Timings[399]++
}

// GetPStat will return the timing at which the percentage of requests is lower
// than the provided p. P must be greater than 0 and less than 1.
//
// A bad input will return 0.
func (d *Distribution) GetPStat(p float64) time.Duration {
	if p <= 0 || p >= 1 {
		return 0
	}

	// Get the total.
	var total float64
	for i := 0; i < len(d.Timings); i++ {
		total += d.Timings[i]
	}

	// Count up until we reach p.
	var run float64
	var index int
	for run/total < p && index < 400 {
		run += d.Timings[index]
		index++
	}

	// Convert i into a duration.
	stepSize := time.Millisecond * 4
	if index <= 64 {
		return stepSize * time.Duration(index)
	}
	prevMax := time.Millisecond * 256
	for i := 64; i <= 400; i += 48 {
		stepSize *= 4
		if index < i+48 {
			return stepSize*time.Duration(index-i) + prevMax
		}
		prevMax *= 4
	}

	// The final bucket value.
	return prevMax
}

// TotalDataPoints returns the total number of data points contained within the
// distribution.
func (d *Distribution) TotalDataPoints() float64 {
	var total float64
	for i := 0; i < len(d.Timings); i++ {
		total += d.Timings[i]
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
		timings[i][0] = dt.distributions[i].GetPStat(.9)
		timings[i][1] = dt.distributions[i].GetPStat(.99)
		timings[i][2] = dt.distributions[i].GetPStat(.999)
		timings[i][3] = dt.distributions[i].GetPStat(.9999)
	}
	return timings
}

// TotalDataPoints returns the total number of items represented in each distribution.
func (dt *DistributionTracker) TotalDataPoints() []float64 {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	var totals []float64
	for _, d := range dt.distributions {
		totals = append(totals, d.TotalDataPoints())
	}
	return totals
}

// Stats returns a full suite of statistics about the distributions in the
// tracker.
func (dt *DistributionTracker) Stats() *DistributionTrackerStats {
	return &DistributionTrackerStats{
		Nines:           dt.AllNines(),
		TotalDataPoints: dt.TotalDataPoints(),
	}
}

// NewDistributionTrackerStandard returns a standard distribution tracker, which
// tracks data points over distributions with half lives of 15 minutes, 24
// hours, and 30 days.
func NewDistributionTrackerStandard() *DistributionTracker {
	d1 := &Distribution{
		StaticHalfLife: 15 * time.Minute,
		LastDecay:      time.Now(),
		PreviousUpdate: time.Now(),
	}
	d2 := &Distribution{
		StaticHalfLife: 24 * time.Hour,
		LastDecay:      time.Now(),
		PreviousUpdate: time.Now(),
	}
	d3 := &Distribution{
		StaticHalfLife: 30 * 24 * time.Hour,
		LastDecay:      time.Now(),
		PreviousUpdate: time.Now(),
	}
	return &DistributionTracker{
		distributions: []*Distribution{
			d1,
			d2,
			d3,
		},
	}
}
