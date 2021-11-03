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
	"fmt"
	"reflect"
	"sync"
	"time"

	"gitlab.com/SkynetLabs/skyd/build"
)

const (
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
	// DistributionTrackerTotalBuckets is a shortcut defining one of the
	// commonly used relationships between the other consts.
	DistributionTrackerTotalBuckets = distributionTrackerInitialBuckets + distributionTrackerBucketsPerStepChange*distributionTrackerNumIncrements

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

	// numBuckets is the number of buckets a distribution consists of.
	numBuckets = 64 + 48*distributionTrackerNumIncrements
)

type (
	// Distribution tracks the distribution of durations for a particular half
	// life.
	//
	// NOTE: This struct is not thread safe, thread safety is derived from the
	// parent object.
	//
	// NOTE: If you extend this struct, take the changes into account in the
	// 'Clone' method.
	Distribution struct {
		// Decay is applied to the distribution.
		*GenericDecay

		// Buckets that represent the distribution. The first
		// bucketsPerStepChange buckets start at 4ms and are 4ms spaced apart.
		// The next 48 buckets are spaced 16ms apart, then the next 48 are
		// spaced 64ms apart, the spacings multiplying by 4 every 48 buckets.
		// The final bucket is just over an hour, anything over will be put into
		// that bucket as well.
		timings [numBuckets]float64
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

	// PersistedDistribution contains the information about a distribution
	// that is persisted to disk.
	PersistedDistribution struct {
		Timings [numBuckets]float64 `json:"timings"`
	}

	// PersistedDistributionTracker contains the information about a
	// distributiontracker that is persisted to disk.
	PersistedDistributionTracker struct {
		Distributions []PersistedDistribution `json:"distributions"`
	}

	// Chances is a helper type that represent a distribition's chance array
	Chances [DistributionTrackerTotalBuckets]float64
)

// Persist returns a PersistedDistributionTracker for the DistributionTracker by
// copying all of its buckets.
func (dt *DistributionTracker) Persist() PersistedDistributionTracker {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	distributions := make([]PersistedDistribution, 0, len(dt.distributions))
	for _, d := range dt.distributions {
		distributions = append(distributions, PersistedDistribution{
			Timings: d.timings,
		})
	}
	return PersistedDistributionTracker{
		Distributions: distributions,
	}
}

// DistributionBucketIndexForDuration converts the given duration to a bucket
// index
func DistributionBucketIndexForDuration(dur time.Duration) int {
	index, _ := indexForDuration(dur)
	return index
}

var staticDistributionDurationsForBucketIndices = func() []time.Duration {
	durations := make([]time.Duration, DistributionTrackerTotalBuckets)
	for index := 0; index < DistributionTrackerTotalBuckets; index++ {
		stepSize := distributionTrackerInitialStepSize
		if index <= distributionTrackerInitialBuckets {
			durations[index] = stepSize * time.Duration(index)
			continue
		}
		prevMax := stepSize * distributionTrackerInitialBuckets
		for i := distributionTrackerInitialBuckets; i <= DistributionTrackerTotalBuckets; i += distributionTrackerBucketsPerStepChange {
			stepSize *= distributionTrackerStepChangeMultiple
			if index < i+distributionTrackerBucketsPerStepChange {
				durations[index] = stepSize*time.Duration(index-i) + prevMax
				continue
			}
			prevMax *= distributionTrackerStepChangeMultiple
		}
	}
	return durations
}()

// DistributionDurationForBucketIndex converts the index of a timing bucket into
// a timing.
func DistributionDurationForBucketIndex(index int) time.Duration {
	if index < 0 || index > DistributionTrackerTotalBuckets-1 {
		build.Critical("distribution duration index out of bounds:", index)
	}
	return staticDistributionDurationsForBucketIndices[index]
}

// indexForDuration converts the given duration to a bucket index. Alongside the
// index it also returns a float that represents the fraction of the bucket that
// is included by the given duration.
//
// e.g. if we are dealing with 4ms buckets and a duration of 1ms is passed, the
// return values would be 0 and 0.25, indicating the duration corresponds with
// bucket at index 0, and the given duration included 25% of that bucket.
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
	for i := distributionTrackerInitialBuckets; i < DistributionTrackerTotalBuckets; i += distributionTrackerBucketsPerStepChange {
		stepSize *= distributionTrackerStepChangeMultiple
		max *= distributionTrackerStepChangeMultiple
		if duration < max {
			index := int(duration/stepSize) + i - distributionTrackerInitialBuckets/distributionTrackerStepChangeMultiple
			fraction := float64(duration%stepSize) / float64(stepSize)
			return int(index), fraction
		}
	}

	// if we haven't found the index, return the last one
	return DistributionTrackerTotalBuckets - 1, 1
}

// addDecay will decay the data in the distribution.
func (d *Distribution) addDecay() {
	d.Decay(func(decay float64) {
		for i := 0; i < len(d.timings); i++ {
			d.timings[i] *= decay
		}
	})
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

// ChanceAfter returns the chance we find a data point after the given duration.
func (d *Distribution) ChanceAfter(dur time.Duration) float64 {
	// Check for negative inputs.
	if dur < 0 {
		build.Critical("cannot call ChanceAfter with negative duration")
		return 0
	}

	// Get the total data points. If no data was collected we return 0.
	total := d.DataPoints()
	if total == 0 {
		return 0
	}

	// Get the amount of data points up until the bucket index.
	count := float64(0)
	index, fraction := indexForDuration(dur)
	for i := 0; i < index; i++ {
		count += d.timings[i]
	}

	// Add the fraction of the data points in the bucket at index.
	count += fraction * d.timings[index]

	// Calculate the chance
	chance := count / total
	return chance
}

// ChancesAfter returns an array of chances, every entry represents the chance
// we find a data point after the duration that corresponds with the bucket at
// the index of the entry.
func (d *Distribution) ChancesAfter() Chances {
	var chances Chances

	// Get the total data points.
	total := d.DataPoints()
	if total == 0 {
		return chances
	}

	// Loop over every bucket once and calculate the chance at that bucket
	count := float64(0)
	for i := 0; i < DistributionTrackerTotalBuckets; i++ {
		chances[i] = count / total
		count += d.timings[i]
	}

	return chances
}

// Clone returns a deep copy of the distribution.
func (d *Distribution) Clone() *Distribution {
	c := &Distribution{GenericDecay: d.GenericDecay.Clone()}
	for i, b := range d.timings {
		c.timings[i] = b
	}

	// sanity check using reflect package, only executed in testing
	if build.Release == "testing" {
		if !reflect.DeepEqual(d, c) {
			build.Critical("cloned distribution not equal")
		}
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

// DurationForIndex converts the index of a bucket into a duration.
func (d *Distribution) DurationForIndex(index int) time.Duration {
	return DistributionDurationForBucketIndex(index)
}

// ExpectedDuration returns the estimated duration based upon the current
// distribution.
func (d *Distribution) ExpectedDuration() time.Duration {
	// Get the total data points.
	total := d.DataPoints()
	if total == 0 {
		// No data collected, just return the worst case.
		return DistributionDurationForBucketIndex(len(d.timings) - 1)
	}

	// Across all buckets, multiply the pct chance times the bucket's duration.
	// The sum is the expected duration.
	var expected float64
	for i := 0; i < len(d.timings); i++ {
		pct := d.timings[i] / total
		expected += pct * float64(DistributionDurationForBucketIndex(i))
	}
	return time.Duration(expected)
}

// MergeWith merges the given distribution according to a certain weight.
func (d *Distribution) MergeWith(other *Distribution, weight float64) {
	// validate the given distribution
	if d.staticHalfLife != other.staticHalfLife {
		build.Critical(fmt.Sprintf("only distributions with equal half lives should be merged, %v != %v", d.staticHalfLife, other.staticHalfLife))
		return
	}

	// validate the weight
	if weight <= 0 || weight > 1 {
		build.Critical(fmt.Sprintf("unexpected weight %v", weight))
		return
	}

	// loop all other timings and append them taking into account the given
	// weight
	for bi, b := range other.timings {
		d.timings[bi] += b * weight
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
		return DistributionDurationForBucketIndex(DistributionTrackerTotalBuckets - 1)
	}

	// Count up until we reach p.
	var run float64
	var index int
	for run/total < p && index < DistributionTrackerTotalBuckets-1 {
		run += d.timings[index]
		index++
	}

	// Convert i into a duration.
	return DistributionDurationForBucketIndex(index)
}

// Shift shifts the distribution by a certain duration. The shift operation will
// essentially ignore all data points up until the duration with which we're
// shifting. If that duration does not perfectly align with the distribution's
// buckets, we smear the fractionalised value over the buckets preceding the
// bucket that corresponds with the given duration.
func (d *Distribution) Shift(dur time.Duration) {
	// Check for negative inputs.
	if dur < 0 {
		build.Critical("cannot call Shift with negative duration")
		return
	}

	// Get the value at index
	index, fraction := indexForDuration(dur)
	value := d.timings[index]

	// Calculate the fraction we want to keep and update the bucket
	keep := (1 - fraction) * value
	d.timings[index] = keep

	// If we're at index 0 we are done because there's no buckets preceding it.
	if index == 0 {
		return
	}

	// Otherwise we calculate the remainder and smear it over all buckets
	// up until we reach index.
	remainder := fraction * value
	smear := remainder / float64(index)
	for i := 0; i < index; i++ {
		d.timings[i] = smear
	}
}

// HalfLife returns this distribution's half file.
func (d *Distribution) HalfLife() time.Duration {
	return d.staticHalfLife
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

// Load loads the buckets of a PersistedDistributionTracker into the tracker
// that this method is called on, overwriting the buckets in the process.
func (dt *DistributionTracker) Load(tracker PersistedDistributionTracker) error {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	if len(dt.distributions) != len(tracker.Distributions) {
		return fmt.Errorf("failed to load distribution tracker -  number of persisted distributions doesn't match the expectations: %v != %v", len(dt.distributions), len(tracker.Distributions))
	}
	for i := range tracker.Distributions {
		for j := range dt.distributions[i].timings {
			dt.distributions[i].timings[j] = tracker.Distributions[i].Timings[j]
		}
	}
	return nil
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
		GenericDecay: NewDecay(halfLife),
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
