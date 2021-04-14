package renter

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/skynetlabs/skyd/build"
)

// readRegistryStatsDecayInterval is the interval after which the registry stats
// are decayed.
var readRegistryStatsDecayInterval = build.Select(build.Var{
	Dev:      time.Second,
	Standard: time.Second * 5,
	Testing:  time.Second,
}).(time.Duration)

// readRegistryStats collects stats about read registry jobs. each bucket has a
// number of items, this amount decays over time so we focus on recent event
// timings. We decay all buckets every time a new datum is added to any of them
// and if the time since the last decay is larger than the decay interval.
type readRegistryStats struct {
	staticBuckets     []float64
	currentPositions  []int
	interval          time.Duration
	lastDecay         time.Time
	staticDecay       float64
	staticPercentiles []float64
	total             float64

	mu sync.Mutex
}

// AddDatum adds a new datapoint to the stats.
func (rrs *readRegistryStats) AddDatum(duration time.Duration) error {
	rrs.mu.Lock()
	defer rrs.mu.Unlock()

	// A negative duration is invalid.
	if duration < 0 {
		err := errors.New("AddDatum: can't add negative duration")
		build.Critical(err)
		return err
	}

	// Figure out if we need to decay this time by checking the time since the
	// last decay against the interval.
	decay := time.Since(rrs.lastDecay) > readRegistryStatsDecayInterval
	if decay {
		rrs.lastDecay = time.Now()
	}

	// Check if the buckets need to be extended.
	bi := int(duration / rrs.interval)
	if bi >= len(rrs.staticBuckets) {
		return fmt.Errorf("bucket index out-of-bounds %v >= %v", bi, len(rrs.staticBuckets))
	}

	// Add the new data to the total and decay it if necessary before doing so.
	if decay {
		rrs.total *= rrs.staticDecay
	}
	rrs.total++

	// Loop over all buckets and find the new current position. It's the first
	// index where smaller / total >= percentile.
	smaller := 0.0
	larger := rrs.total
	for i := range rrs.currentPositions {
		rrs.currentPositions[i] = -1
	}
	for i := range rrs.staticBuckets {
		// Decay the bucket if necessary.
		if decay {
			rrs.staticBuckets[i] *= rrs.staticDecay
		}
		// Add to the bucket if necessary.
		if i == bi {
			rrs.staticBuckets[i]++
		}
		// Increment smaller and decrement larger as we continue.
		larger -= rrs.staticBuckets[i]
		smaller += rrs.staticBuckets[i]
		// If the condition is met for the position, set it.
		for j := range rrs.staticPercentiles {
			if rrs.currentPositions[j] == -1 && smaller/rrs.total >= rrs.staticPercentiles[j] {
				rrs.currentPositions[j] = i
			}
		}
	}
	// If position wasn't set, set it to the last index.
	for i := range rrs.currentPositions {
		if rrs.currentPositions[i] == -1 {
			rrs.currentPositions[i] = len(rrs.staticBuckets) - 1
		}
	}
	return nil
}

// Estimate returns the current estimate.
func (rrs *readRegistryStats) Estimate() []time.Duration {
	rrs.mu.Lock()
	defer rrs.mu.Unlock()
	durations := make([]time.Duration, len(rrs.currentPositions))
	for i := range durations {
		durations[i] = time.Duration(rrs.currentPositions[i]+1) * rrs.interval
	}
	return durations
}

// newReadRegistryStats creates new stats from a given decay and percentile.
func newReadRegistryStats(maxTime, interval time.Duration, decay float64, percentiles []float64) *readRegistryStats {
	if !sort.Float64sAreSorted(percentiles) {
		build.Critical("percentiles need to be sorted in ascending order")
		return nil
	}
	return &readRegistryStats{
		currentPositions:  make([]int, len(percentiles)),
		interval:          interval,
		staticBuckets:     make([]float64, (maxTime/interval)+1),
		staticDecay:       decay,
		staticPercentiles: percentiles,
	}
}
