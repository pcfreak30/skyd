package renter

// expMovingAvg is a function to track the exponential moving average of a
// series of datapoints.
type expMovingAvg struct {
	decay float64

	// weightedSpan tracks the total decayed amount of data we've received, and
	// the weighted sum tracks the total decayed value of all the data  we've
	// received.
	weightedSpan float64
	weightedSum  float64
}

// newExpMovingAvg returns an expMovingAvg that's ready to receive data and
// compute averages.
func newExpMovingAvg(decay float64) *expMovingAvg {
	return &expMovingAvg{
		decay: decay,
	}
}

// addDataPoint adds a single data point to the average, decaying the existing
// data by the chosen decay of the average.
func (ema *expMovingAvg) addDataPoint(data float64) {
	ema.weightedSpan *= ema.decay
	ema.weightedSpan += 1
	ema.weightedSum *= ema.decay
	ema.weightedSum += data
}

// average returns the average of all the data that's been added to the
// expMovingAvg.
func (ema *expMovingAvg) average() float64 {
	return ema.weightedSum / ema.weightedSpan
}

// expMovingAvgHotStart is a helper to compute the next exponential moving
// average given the last value and a new point of measurement. The function is
// fully stateless, but also has a 'hot start', which means that the first data
// point sets the average at full strength. If the first datapoint is an outlier
// datapoint, it can significantly disrupt the average until many more
// datapoints have come in.
//
// https://en.wikipedia.org/wiki/Moving_average#Exponential_moving_average
func expMovingAvgHotStart(oldEMA, newValue, decay float64) float64 {
	if decay < 0 || decay > 1 {
		panic("decay has to be a value in range 0 <= x <= 1")
	}
	if oldEMA == 0 {
		return newValue
	}
	return newValue*(1-decay) + decay*oldEMA
}
