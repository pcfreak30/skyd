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
		testSubscriptionManagerSubscribeUnsubscribe(t, rt.renter.staticWorkerPool)
	})
}

// testSubscriptionManagerSubscribeUnsubscribe is a unit test for the manager's
// Subscribe and Unsubscribe methods.
func testSubscriptionManagerSubscribeUnsubscribe(t *testing.T, wp *workerPool) {
	sm := newSubscriptionManager(wp)

	// Create random pubkey tweak pair.
	srv, spk, _ := randomRegistryValue()
	tweak := srv.Tweak
	eid := modules.DeriveRegistryEntryID(spk, tweak)

	// Declare the expected renterSubscription.
	expectedRS := &renterSubscription{
		latestValue: nil,
		staticSPK:   spk,
		staticTweak: tweak,
		subscribers: make(map[subscriberID]struct{}),
	}
	expectedUS := &userSubscription{}

	// Get a subscriber id.
	var sid subscriberID
	fastrand.Read(sid[:])

	// Subscribe to it. This should return nil.
	rv := sm.Subscribe(spk, tweak, sid)
	if rv != nil {
		t.Fatal("rv should be nil")
	}
	expectedRS.subscribers[sid] = struct{}{}

	// Expect 1 subscription and 1 subscriber.
	if len(sm.subscriptions) != 1 || len(sm.subscribers) != 1 {
		t.Fatal("wrong number of subscriptions and/or subscribers")
	}
	rs, exists := sm.subscriptions[eid]
	if !exists || !reflect.DeepEqual(rs, expectedRS) {
		t.Fatal("wrong rs")
	}
	if len(rs.subscribers) != 1 {
		t.Fatal("expected 1 subscriber")
	}
	if _, exists := rs.subscribers[sid]; !exists {
		t.Fatal("wrong subscriber")
	}
	subscriber, exists := sm.subscribers[sid]
	if !exists || len(subscriber.subscriptions) != 1 {
		t.Fatal("missing subscriber")
	}
	us, exists := subscriber.subscriptions[eid]
	if !exists || !reflect.DeepEqual(us, expectedUS) {
		t.Fatal("wrong us")
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
	if len(rs.subscribers) != 1 {
		t.Fatal("expected 1 subscribers")
	}
	if _, exists := rs.subscribers[sid]; !exists {
		t.Fatal("wrong subscriber")
	}
	subscriber, exists = sm.subscribers[sid]
	if !exists || len(subscriber.subscriptions) != 1 {
		t.Fatal("missing subscriber")
	}
	us, exists = subscriber.subscriptions[eid]
	if !exists || !reflect.DeepEqual(us, expectedUS) {
		t.Fatal("wrong rs")
	}

	// Subscribe again as a different subscriber.
	var sid2 subscriberID
	fastrand.Read(sid2[:])
	rv = sm.Subscribe(spk, tweak, sid2)
	if !reflect.DeepEqual(*rv, srv) {
		t.Fatal("wrong rv")
	}
	expectedRS.subscribers[sid2] = struct{}{}

	// Expect 1 subscription and 2 subscribers. The refcount should also be
	// increased.
	if len(sm.subscriptions) != 1 || len(sm.subscribers) != 2 {
		t.Fatal("wrong number of subscriptions and/or subscribers")
	}
	rs, exists = sm.subscriptions[eid]
	if !exists || !reflect.DeepEqual(rs, expectedRS) {
		t.Fatal("wrong rs")
	}
	if len(rs.subscribers) != 2 {
		t.Fatal("expected 2 subscribers")
	}
	if _, exists := rs.subscribers[sid]; !exists {
		t.Fatal("wrong subscriber")
	}
	if _, exists := rs.subscribers[sid2]; !exists {
		t.Fatal("wrong subscriber")
	}
	// Check first subscriber.
	subscriber, exists = sm.subscribers[sid]
	if !exists || len(subscriber.subscriptions) != 1 {
		t.Fatal("missing subscriber")
	}
	us, exists = subscriber.subscriptions[eid]
	if !exists || !reflect.DeepEqual(us, expectedUS) {
		t.Fatal("wrong rs")
	}
	// Check second subscriber. This one should have the latest value set in the
	// user subscription already.
	subscriber, exists = sm.subscribers[sid2]
	if !exists || len(subscriber.subscriptions) != 1 {
		t.Fatal("missing subscriber")
	}
	expectedUS.latestValue = rs.latestValue
	us, exists = subscriber.subscriptions[eid]
	if !exists || !reflect.DeepEqual(us, expectedUS) {
		t.Fatal("wrong rs")
	}

	// Unsubscribe the first subscriber.
	sm.UnsubscribeAll(sid)
	delete(expectedRS.subscribers, sid)

	// Expect 1 subscription and 1 subscriber. The refcount should be decreased.
	if len(sm.subscriptions) != 1 || len(sm.subscribers) != 1 {
		t.Fatal("wrong number of subscriptions and/or subscribers")
	}
	rs, exists = sm.subscriptions[eid]
	if !exists || !reflect.DeepEqual(rs, expectedRS) {
		t.Fatal("wrong rs")
	}
	if len(rs.subscribers) != 1 {
		t.Fatal("expected 1 subscriber")
	}
	if _, exists := rs.subscribers[sid2]; !exists {
		t.Fatal("wrong subscriber")
	}
	subscriber, exists = sm.subscribers[sid2]
	if !exists || len(subscriber.subscriptions) != 1 {
		t.Fatal("missing subscriber")
	}
	us, exists = subscriber.subscriptions[eid]
	if !exists || !reflect.DeepEqual(us, expectedUS) {
		t.Fatal("wrong rs")
	}

	// Unsubscribe the second subscriber.
	sm.UnsubscribeAll(sid2)
	if len(sm.subscriptions) != 0 || len(sm.subscribers) != 0 {
		t.Fatal("wrong number of subscriptions and/or subscribers")
	}
}
