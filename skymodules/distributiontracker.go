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

	// distributionTrackerInitialStepSize defines the step size that we use for
	// the first bucketsPerStepChange buckets of a distribution. This means that
	// this is the smallest timing that is supported by the distribution. The
	// highest value supported by the distribution is about 1 million times
	// larger.
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

// NOTE: These consts are interconnected, do not change them.
// distriubtionTrackerInitialBuckets needs to be a power of 2,
// distriubtionTrackerStepChangeMultiple needs to be a power of 2, and
// distriubtionTrackerBucketsPerStepChange needs to be:
// 		distributionTrackerInitialBuckets - (distributionTrackerInitialBuckets/distributionTrackerStepChangeMultiple)
const (
	// bucketsPerStepChange defines the number of buckets that are used in the
	// first step. It has a mathematical relationship to
	// distributoinTrackerBucketsPerStepChange, because we want every step range
	// to cover the same amount of new ground, but the first step range has no
	// history. By adding an extra few buckets at the beginning, we can give it
	// that history and bootstrap the data structure.
	distributionTrackerInitialBuckets = 64

	// distributionTrackerBucketsPerStepChange defines the number of buckets per
	// step change. Increasing this number will give better granularity at each
	// step range at the cost of more memory and more computation.
	distributionTrackerBucketsPerStepChange = 48

	// distributionTrackerStepChangeMultiple defines the multiple that is used
	// when a step change is applied. A larger multiple means the data structure
	// can cover a greater range of data in fewer buckets at the cost of
	// granularity. This saves computation and memory.
	distributionTrackerStepChangeMultiple = 4

	// distributionTrackerTotalBuckets is a shortcut defining one of the
	// commonly used relationships between the other consts.
	distributionTrackerTotalBuckets = distributionTrackerInitialBuckets + distributionTrackerBucketsPerStepChange*distributionTrackerNumIncrements
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

		// Buckets that represent the distribution. The first
		// bucketsPerStepChange buckets start at 4ms and are 4ms spaced apart.
		// The next 48 buckets are spaced 16ms apart, then the next 48 are
		// spaced 64ms apart, the spacings multiplying by 4 every 48 buckets.
		// The final bucket is just over an hour, anything over will be put into
		// that bucket as well.
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
func durationForIndex(index int) time.Duration {
	if index < 0 || index > 64+48*distributionTrackerNumIncrements {
		build.Critical("distribution duration index out of bounds:", index)
	}

	stepSize := distributionTrackerInitialStepSize
	if index <= distributionTrackerInitialBuckets {
		return stepSize * time.Duration(index)
	}
	prevMax := stepSize * distributionTrackerInitialBuckets
	for i := distributionTrackerInitialBuckets; i <= distributionTrackerTotalBuckets; i += distributionTrackerBucketsPerStepChange {
		stepSize *= distributionTrackerStepChangeMultiple
		if index < i+distributionTrackerBucketsPerStepChange {
			return stepSize*time.Duration(index-i) + prevMax
		}
		prevMax *= distributionTrackerStepChangeMultiple
	}

	// The final bucket value.
	return prevMax
}

// TODO PJ: docstring
func indexForDuration(duration time.Duration) (int, float64) {
	if duration < 0 {
		build.Critical("negative duration")
		return -1, 0
	}

	// check if it falls in the initial buckets
	stepSize := distributionTrackerInitialStepSize
	max := stepSize * distributionTrackerInitialBuckets
	if duration < max {
		index := duration / stepSize
		fraction := float64(duration%stepSize) / float64(stepSize)
		return int(index), fraction
	}

	// range over all buckets and see whether the given duration falls into it
	for i := distributionTrackerInitialBuckets; i < distributionTrackerTotalBuckets; i += distributionTrackerBucketsPerStepChange {
		stepSize *= distributionTrackerStepChangeMultiple
		max *= distributionTrackerStepChangeMultiple
		if duration < max {
			index := int(duration/stepSize) + i - distributionTrackerInitialBuckets/distributionTrackerStepChangeMultiple
			fraction := float64(duration%stepSize) / float64(stepSize)
			return int(index), fraction
		}
	}

	// if we haven't found the index, return the last one
	return distributionTrackerTotalBuckets - 1, 1
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
	mult := math.Pow(0.5, strength) // 0.5 is the power you use to perform half life calculations

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
	index, _ := indexForDuration(dur)

	// Add the datapoint
	d.timings[index]++
}

// TODO PJ: docstring
func (d *Distribution) ChanceAfter(dur time.Duration) float64 {
	// Check for negative inputs.
	if dur < 0 {
		build.Critical("cannot call ChanceAfter with negatime duration")
		return 0
	}

	// Get the total data points. Return worst case if no data was collected.
	total := d.DataPoints()
	if total == 0 {
		return 1 // TODO PJ: hmmm
	}

	// Get the data point count up until the given index.
	count := float64(0)
	index, fraction := indexForDuration(dur)
	for i := 0; i <= index; i++ {
		if i == index {
			count += fraction * d.timings[index]
		} else {
			count += d.timings[i]
		}
	}

	chance := count / total
	return chance
}

func (d *Distribution) Clone() *Distribution {
	c := &Distribution{
		staticHalfLife:  d.staticHalfLife,
		lastDecay:       d.lastDecay,
		decayedLifetime: d.decayedLifetime,
	}
	for i, b := range d.timings {
		c.timings[i] = b
	}
	return c
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

// TODO PJ: docstring
func (d *Distribution) DurationForIndex(index int) time.Duration {
	return durationForIndex(index)
}

// ExpectedDuration returns the estimated duration based upon the current
// distribution.
func (d *Distribution) ExpectedDuration() time.Duration {
	// Get the total data points.
	var total float64
	for i := 0; i < len(d.timings); i++ {
		total += d.timings[i]
	}
	if total == 0 {
		// No data collected, just return the worst case.
		return durationForIndex(len(d.timings))
	}

	// Across all buckets, multiply the pct chance times the bucket's duration.
	// The sum is the expected duration.
	var expected float64
	for i := 0; i < len(d.timings); i++ {
		pct := d.timings[i] / total
		expected += pct * float64(durationForIndex(i).Nanoseconds())
	}
	return time.Duration(expected)
}

// MergeWith merges the given distribution according to a certain weight.
func (d *Distribution) MergeWith(other *Distribution, weight float64) {
	if weight <= 0 || weight > 1 {
		build.Critical("unexpected weight")
		return
	}

	// TODO validate distribution have equal half life (?)

	// loop over every bucket in other's distribution and calculate the pct
	// chance a datapoint appears in this bucket, append this chance
	// multiplied by the given weight and add it to the corresponding bucket
	// in dt.
	total := other.DataPoints()
	for bi, b := range other.timings {
		chance := b / total
		d.timings[bi] += chance * weight
	}
}

// NumBuckets returns the total number of buckets in the distribution
func (d *Distribution) NumBuckets() int {
	return len(d.timings)
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
		return durationForIndex(len(d.timings))
	}

	// Count up until we reach p.
	var run float64
	var index int
	for run/total < p && index < 64+48*distributionTrackerNumIncrements {
		run += d.timings[index]
		index++
	}

	// Convert i into a duration.
	return durationForIndex(index)
}

func (d *Distribution) Shift(dur time.Duration) {
	// Check for negative inputs.
	if dur < 0 {
		build.Critical("cannot call Shift with negatime duration")
		return
	}

	// Determine the index of the bucket for given duration, we want to nullify
	// all buckets up until that index.
	index, fraction := indexForDuration(dur)
	for i := 0; i < index; i++ {
		d.timings[i] = 0
	}

	// Get the value at index
	value := d.timings[index]

	keep := (1 - fraction) * value
	remainder := fraction * value
	smear := remainder / float64(index)

	d.timings[index] = keep
	for i := 0; i < index; i++ {
		d.timings[i] = smear
	}
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

// Percentiles returns the percentiles for 4 timings for each distribution in
// the tracker:
//	 + the p90
//	 + the p99
//	 + the p999
//	 + the p9999
func (dt *DistributionTracker) Percentiles() [][]time.Duration {
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

// DataPoints returns the total number of items represented in each
// distribution.
func (dt *DistributionTracker) DataPoints() []float64 {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	var totals []float64
	for _, d := range dt.distributions {
		totals = append(totals, d.DataPoints())
	}
	return totals
}

// Distribution returns the distribution at the requested index. If the given
// index is not within bounds it returns nil.
func (dt *DistributionTracker) Distribution(index int) *Distribution {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	if index < 0 || index >= len(dt.distributions) {
		build.Critical("unexpected distribution index")
		return nil
	}
	return dt.distributions[index].Clone()
}

// Stats returns a full suite of statistics about the distributions in the
// tracker.
func (dt *DistributionTracker) Stats() *DistributionTrackerStats {
	return &DistributionTrackerStats{
		Nines:      dt.Percentiles(),
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
