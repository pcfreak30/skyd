package renter

import (
	"reflect"
	"testing"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/fastrand"
)

// TestSubscriptionManager runs all subscription manager related unit tests.
func TestSubscriptionManager(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create renter.
	rt, err := newRenterTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("Subscribe", func(t *testing.T) {
		testSubscriptionManagerSubscribe(t, rt.renter.staticWorkerPool)
	})
}

// testSubscriptionManagerSubscribe is a unit test for the manager's Subscribe
// method.
func testSubscriptionManagerSubscribe(t *testing.T, wp *workerPool) {
	sm := newSubscriptionManager(wp)

	// Create random pubkey tweak pair.
	srv, spk, _ := randomRegistryValue()
	tweak := srv.Tweak
	eid := modules.DeriveRegistryEntryID(spk, tweak)

	// Declare the expected renterSubscription.
	expectedRS := &renterSubscription{
		latestValue: nil,
		refcount:    1,
		staticSPK:   spk,
		staticTweak: tweak,
	}

	// Get a subscriber id.
	var sid subscriberID
	fastrand.Read(sid[:])

	// Subscribe to it. This should return nil.
	rv := sm.Subscribe(spk, tweak, sid)
	if rv != nil {
		t.Fatal("rv should be nil")
	}
	// Expect 1 subscription and 1 subscriber.
	if len(sm.subscriptions) != 1 || len(sm.subscribers) != 1 {
		t.Fatal("wrong number of subscriptions and/or subscribers")
	}
	rs, exists := sm.subscriptions[eid]
	if !exists || !reflect.DeepEqual(rs, expectedRS) {
		t.Fatal("wrong rs")
	}
	subscriber, exists := sm.subscribers[sid]
	if !exists || len(subscriber.subscriptions) != 1 {
		t.Fatal("missing subscriber")
	}
	rs, exists = subscriber.subscriptions[eid]
	if !exists || !reflect.DeepEqual(rs, expectedRS) {
		t.Fatal("wrong rs")
	}

	// Update the latest value.
	rs.latestValue = &srv
	expectedRS.latestValue = &srv

	// Subscribe again as the same subscriber. This is a no-op but returns the
	// new latest value.
	rv = sm.Subscribe(spk, tweak, sid)
	if !reflect.DeepEqual(*rv, srv) {
		t.Fatal("wrong rv")
	}
	// Expect 1 subscription and 1 subscriber.
	if len(sm.subscriptions) != 1 || len(sm.subscribers) != 1 {
		t.Fatal("wrong number of subscriptions and/or subscribers")
	}
	rs, exists = sm.subscriptions[eid]
	if !exists || !reflect.DeepEqual(rs, expectedRS) {
		t.Fatal("wrong rs")
	}
	subscriber, exists = sm.subscribers[sid]
	if !exists || len(subscriber.subscriptions) != 1 {
		t.Fatal("missing subscriber")
	}
	rs, exists = subscriber.subscriptions[eid]
	if !exists || !reflect.DeepEqual(rs, expectedRS) {
		t.Fatal("wrong rs")
	}

	// Subscribe again as a different subscriber.
	var sid2 subscriberID
	fastrand.Read(sid2[:])
	rv = sm.Subscribe(spk, tweak, sid2)
	if !reflect.DeepEqual(*rv, srv) {
		t.Fatal("wrong rv")
	}
	// Expect 1 subscription and 2 subscribers. The refcount should also be
	// increased.
	if len(sm.subscriptions) != 1 || len(sm.subscribers) != 2 {
		t.Fatal("wrong number of subscriptions and/or subscribers")
	}
	rs, exists = sm.subscriptions[eid]
	expectedRS.refcount++
	if !exists || !reflect.DeepEqual(rs, expectedRS) {
		t.Fatal("wrong rs")
	}
	subscriber, exists = sm.subscribers[sid]
	if !exists || len(subscriber.subscriptions) != 1 {
		t.Fatal("missing subscriber")
	}
	rs, exists = subscriber.subscriptions[eid]
	if !exists || !reflect.DeepEqual(rs, expectedRS) {
		t.Fatal("wrong rs")
	}
}
