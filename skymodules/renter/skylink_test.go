package renter

import (
	"fmt"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

const (
	testSkylink1 = "AABEKWZ_wc2R9qlhYkzbG8mImFVi08kBu1nsvvwPLBtpEg"
	testSkylink2 = "AADxpqE6bH2yFBuCFakOeouCj99CIIKSfgv4B9XsImkxLQ"
)

var (
	skylink1 skymodules.Skylink
	skylink2 skymodules.Skylink
)

// TestSkylink probes the skylink manager subsystem.
func TestSkylink(t *testing.T) {
	t.Parallel()

	// Load Skylinks for tests
	err := skylink1.LoadString(testSkylink1)
	if err != nil {
		t.Fatal(err)
	}
	err = skylink2.LoadString(testSkylink2)
	if err != nil {
		t.Fatal(err)
	}

	// Run Tests
	t.Run("Basic", testSkylinkBasic)
	t.Run("IsUnpinned", testIsUnpinned)
}

// testIsUnpinned probes the handling of checking if a filenode is considered
// unpinned.
func testIsUnpinned(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}

	// Create renter
	rt, err := newRenterTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err = rt.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	// create siafile
	sf, err := rt.renter.newRenterTestFile()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err = sf.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	// add link to siafile
	err = sf.AddSkylink(skylink1)
	if err != nil {
		t.Fatal(err)
	}

	// check isunpinned
	if rt.renter.staticSkylinkManager.callIsUnpinned(sf) {
		t.Error("filenode should not be considered unpinned")
	}

	// add different link to skylink manager
	rt.renter.staticSkylinkManager.managedAddUnpinRequest(skylink2)

	// check inunpinned
	if rt.renter.staticSkylinkManager.callIsUnpinned(sf) {
		t.Error("filenode should not be considered unpinned")
	}

	// add link to skylink manager
	rt.renter.staticSkylinkManager.managedAddUnpinRequest(skylink1)

	// check isunpinned
	if !rt.renter.staticSkylinkManager.callIsUnpinned(sf) {
		t.Error("filenode should be considered unpinned")
	}
}

// testSkylinkBasic probes the basic functionality of the skylinkManager
func testSkylinkBasic(t *testing.T) {
	// Initialize new skylinkManager
	sm := newSkylinkManager()
	start := time.Now()

	// Calling prune on a newly initialized empty skylinkManager should be fine
	sm.callPruneUnpinRequests()

	// Add skylink
	sm.managedAddUnpinRequest(skylink1)

	// Define a helper to verify state. This basic test will be adding 1 skylink
	// at a time and we want to make sure that the time is set to be far enough in
	// the future.
	verifyState := func(skylink skymodules.Skylink) error {
		sm.mu.Lock()
		defer sm.mu.Unlock()
		if len(sm.unpinRequests) != 1 {
			return fmt.Errorf("Prune result unexpected; have %v expected %v", len(sm.unpinRequests), 1)
		}
		urt, ok := sm.unpinRequests[skylink.String()]
		if !ok {
			return errors.New("skylink not in unpinRequests")
		}
		if urt.Before(start.Add(2 * TargetHealthCheckFrequency)) {
			return errors.New("time not far enough in the future")
		}
		return nil
	}

	// Verify state
	err := verifyState(skylink1)
	if err != nil {
		t.Fatal(err)
	}

	// Grab the unpinRequest time
	sm.mu.Lock()
	urt := sm.unpinRequests[skylink1.String()]
	sm.mu.Unlock()

	// Call prune, nothing should happen since the pruneTimeThreshold is still 0
	// so no time is before it.
	sm.callPruneUnpinRequests()
	err = verifyState(skylink1)
	if err != nil {
		t.Fatal(err)
	}

	// Update the pruneTimeThreshold to now plus 2 * HealthCheckInterval. This
	// will cause the first skylink to be pruned.
	sm.callUpdatePruneTimeThreshold(time.Now().Add(2 * TargetHealthCheckFrequency))

	// Add skylink again should be a no-op
	sm.managedAddUnpinRequest(skylink1)
	err = verifyState(skylink1)
	if err != nil {
		t.Fatal(err)
	}
	sm.mu.Lock()
	urt2 := sm.unpinRequests[skylink1.String()]
	sm.mu.Unlock()
	if !urt.Equal(urt2) {
		t.Error("times shouldn't have been changed")
	}

	// Add a new skylink
	sm.managedAddUnpinRequest(skylink2)

	// Call prune, this should prune the original skylink and leave the new
	// skylink.
	sm.callPruneUnpinRequests()

	// Only the last skylink should be in the unpinRequests
	err = verifyState(skylink2)
	if err != nil {
		t.Fatal(err)
	}
}
