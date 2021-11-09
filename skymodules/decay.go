package skymodules

import (
	"math"
	"time"
)

const (
	// decayFrequencyDenom determines what percentage of the half life must
	// expire before a decay is applied. If the decay denominator is '100', it
	// means that the decay will only be applied once 1/100 (1%) of the half
	// life has elapsed.
	//
	// The main reason that the decay is performed in small blocks instead of
	// continuously is the limited precision of floating point numbers. We found
	// that in production, performing a decay after a very tiny amount of time
	// had elapsed resulted in highly inaccurate data, because the floating
	// points rounded the result too heavily. 100 is generally a good value,
	// because it is infrequent enough that the float point precision can still
	// provide strong accuracy, yet frequent enough that a value which has not
	// been decayed recently is still also an accurate value.
	decayFrequencyDenom = 15
)

type (
	// GenericDecay is a generic abstraction of applying decay to an underlying
	// set of data.
	GenericDecay struct {
		// Determines the decay rate of the data. Zero value here means that no
		// decay is applied.
		staticHalfLife time.Duration

		// Tracks the last time decay was applied so we know if we need to apply
		// another round of decay when adding a datapoint.
		lastDecay time.Time

		// Keeps track of the total amount of time that the data has been alive.
		// This time gets decayed alongside the values, which means you can get
		// the total rate of objects being added by dividing the total number of
		// objects by the decayed lifetime.
		decayedLifetime time.Duration
	}

	// ApplyFunc is a helper type that specifies a function that applies decay
	// on an underlying data set.
	ApplyFunc = func(decay float64)
)

// NewDecay returns a new decay
func NewDecay(halfLife time.Duration) GenericDecay {
	return GenericDecay{
		staticHalfLife: halfLife,
		lastDecay:      time.Now(),
	}
}

// Clone returns a clone of the decay
func (d *GenericDecay) Clone() GenericDecay {
	return GenericDecay{
		staticHalfLife:  d.staticHalfLife,
		lastDecay:       d.lastDecay,
		decayedLifetime: d.decayedLifetime,
	}
}

// Decay calculates the amount of decay that needs to be applied, and applies it
// by calling the passed in function which acts on the data.
func (d *GenericDecay) Decay(applyDecayFn ApplyFunc) {
	// Don't add any decay if the half life is zero.
	if d.staticHalfLife == 0 {
		return
	}

	// Determine if a decay should be applied based on the progression through
	// the half life. We don't apply any decay if not much time has elapsed
	// because applying decay over very short periods taxes the precision of the
	// float64s that we use.
	sincelastDecay := time.Since(d.lastDecay)
	decayFrequency := d.staticHalfLife / decayFrequencyDenom
	if sincelastDecay < decayFrequency {
		return
	}

	// Determine how much decay should be applied.
	d.decayedLifetime += time.Since(d.lastDecay)
	strength := float64(sincelastDecay) / float64(d.staticHalfLife)
	decay := math.Pow(0.5, strength) // 0.5 is the power you use to perform half life calculations

	// Apply the decay to all values.
	applyDecayFn(decay)

	// Update the state.
	d.decayedLifetime = time.Duration(float64(d.decayedLifetime) * decay)
	d.lastDecay = time.Now()
}
