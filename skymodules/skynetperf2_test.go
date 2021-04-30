package skymodules

import (
	"testing"
	"time"
)

// TestDistributionDecay will test that the distribution is being decayed
// correctly when enough time has passed.
func TestDistributionDecay(t *testing.T) {
	// Create a distribution with a half life of 100 minutes, which means a
	// decay operation should trigger every minute.
	d := &Distribution{
		StaticHalfLife: time.Minute * 100,
	}
	totalPoints := func() float64 {
		var total float64
		for i := 0; i < len(d.Timings); i++ {
			total += d.Timings[i]
		}
		return total
	}

	// Add 500 data points.
	for i := 0; i < 500; i++ {
		// Use different buckets.
		if i % 6 == 0 {
			d.AddDataPoint(time.Millisecond)
		} else {
			d.AddDataPoint(time.Millisecond*100)
		}
	}
	if totalPoints() < 499 || totalPoints() > 501 {
		t.Error("bad", totalPoints())
	}

	// Simulate exactly the half life of time passing.
	d.LastDecay = time.Now().Add(-100 * time.Minute)
	d.PreviousUpdate = time.Now().Add(-100 * time.Minute)
	d.AddDataPoint(time.Millisecond)
	if totalPoints() < 250 || totalPoints() > 252 {
		t.Error("bad", totalPoints())
	}

	// Simulate exactly one quarter of the half life passing twice.
	d.LastDecay = time.Now().Add(-50 * time.Minute)
	d.PreviousUpdate = time.Now().Add(-50 * time.Minute)
	d.AddDataPoint(time.Millisecond)
	d.LastDecay = time.Now().Add(-50 * time.Minute)
	d.PreviousUpdate = time.Now().Add(-50 * time.Minute)
	d.AddDataPoint(time.Millisecond)
	if totalPoints() < 126 || totalPoints() > 128 {
		t.Error("bad", totalPoints())
	}
}

// TestDistributionDecayedLifetime checks that the total counted decayed
// lifetime of the distribution is being tracked correctly.
func TestDistributionDecayedLifetime(t *testing.T) {
	// Create a distribution with a half life of 100 minutes, which means a
	// decay operation should trigger every minute.
	d := &Distribution{
		StaticHalfLife: time.Minute * 300,
	}
	totalPoints := func() float64 {
		var total float64
		for i := 0; i < len(d.Timings); i++ {
			total += d.Timings[i]
		}
		return total
	}

	// Do 10k steps, each step advancing one minute. Every third step should
	// trigger a decay. Add 1 data point each step.
	for i := 0; i < 5e3; i++ {
		d.LastDecay = d.LastDecay.Add(-1 * time.Minute)
		d.PreviousUpdate = time.Now().Add(-1 * time.Minute)
		d.AddDataPoint(time.Millisecond)
	}
	pointsPerHour := totalPoints()/float64(d.DecayedLifetime/time.Hour)
	if pointsPerHour < 55 || pointsPerHour > 65 {
		t.Error("bad")
	}
}

// TestDistributionBucketing will check that the distribution is placing timings
// into the right buckets and then reporting the right timings in the pstats.
func TestDistributionBucketing(t *testing.T) {
	// Adding a half life prevents it from decaying every time we add a data
	// point.
	d := &Distribution{
		StaticHalfLife: time.Minute * 100,
	}

	// Try adding a single datapoint to each bucket, by adding it at the right
	// millisecond offset.
	var i int
	total := time.Millisecond
	for i < 64 {
		d.AddDataPoint(total)
		if d.Timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 4 * time.Millisecond
		i++

		pstat := d.GetPStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
	}
	for i < 64+48 {
		d.AddDataPoint(total)
		if d.Timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 16 * time.Millisecond
		i++

		pstat := d.GetPStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
	}
	for i < 64+48*2 {
		d.AddDataPoint(total)
		if d.Timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 64 * time.Millisecond
		i++

		pstat := d.GetPStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
	}
	for i < 64+48*3 {
		d.AddDataPoint(total)
		if d.Timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 256 * time.Millisecond
		i++

		pstat := d.GetPStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
	}
	for i < 64+48*4 {
		d.AddDataPoint(total)
		if d.Timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 1024 * time.Millisecond
		i++

		pstat := d.GetPStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
	}
	for i < 64+48*5 {
		d.AddDataPoint(total)
		if d.Timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 4096 * time.Millisecond
		i++

		pstat := d.GetPStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
	}
	for i < 64+48*6 {
		d.AddDataPoint(total)
		if d.Timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 16384 * time.Millisecond
		i++

		pstat := d.GetPStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
	}
	for i < 64+48*7 {
		d.AddDataPoint(total)
		if d.Timings[i] != 1 {
			t.Error("bad:", i)
		}

		total += 65536 * time.Millisecond
		i++

		pstat := d.GetPStat(0.99999999999)
		if pstat != total-time.Millisecond {
			t.Error("bad", i, pstat, total)
		}
	}

	// Test off the end of the bucket.
	expectedPStat := total - time.Millisecond
	total += 1e9 * time.Millisecond
	d.AddDataPoint(total)
	pstat := d.GetPStat(0.99999999999)
	if pstat != expectedPStat {
		t.Error("bad")
	}
}
