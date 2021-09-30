package renter

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// newSubscriber creates a new subscriber with a no-op notification function for
// testing.
func (sm *registrySubscriptionManager) newSubscriber() *renterSubscriber {
	return sm.NewSubscriber(func(skymodules.RegistryEntry) error { return nil })
}

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
		testSubscriptionManagerSubscribeUnsubscribe(t, rt.renter)
	})
	t.Run("Notify", func(t *testing.T) {
		testSubscriptionManagerNotify(t, rt.renter)
	})
	t.Run("Parallel", func(t *testing.T) {
		testSubscriptionManagerSubscribeUnsubscribeParallel(t, rt.renter)
	})
}

// testSubscriptionManagerSubscribeUnsubscribe is a unit test for subscribing to
// and unsubscribing from the manager.
func testSubscriptionManagerSubscribeUnsubscribe(t *testing.T, r *Renter) {
	sm := newSubscriptionManager(r)

	// Create random pubkey tweak pair.
	value, spk, _ := randomRegistryValue()
	srv := skymodules.NewRegistryEntry(spk, value)
	tweak := srv.Tweak
	eid := modules.DeriveRegistryEntryID(spk, tweak)

	// Declare the expected renterSubscription.
	expectedRS := &renterSubscription{
		latestValue: nil,
		staticSPK:   spk,
		staticTweak: tweak,
		subscribers: make(map[subscriberID]struct{}),
	}
	var expectedSRV *skymodules.RegistryEntry

	// Get a subscriber.
	subscriber1 := sm.newSubscriber()
	sid1 := subscriber1.staticSubscriberID

	// Subscribe to it. This should return nil.
	rv := subscriber1.Subscribe(spk, tweak)
	if rv != nil {
		t.Fatal("rv should be nil")
	}
	expectedRS.subscribers[sid1] = struct{}{}

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
	if _, exists := rs.subscribers[sid1]; !exists {
		t.Fatal("wrong subscriber")
	}
	subscriber, exists := sm.subscribers[sid1]
	if !exists || len(subscriber.subscriptions) != 1 {
		t.Fatal("missing subscriber")
	}
	sub, exists := subscriber.subscriptions[eid]
	if !exists || sub != nil || expectedSRV != nil {
		t.Fatal("wrong us")
	}

	// Update the latest value.
	rs.latestValue = &srv
	expectedRS.latestValue = rs.latestValue

	// Subscribe again as the same subscriber. This is a no-op but returns the
	// new latest value.
	rv = subscriber1.Subscribe(spk, tweak)
	if !reflect.DeepEqual(rv, &srv) {
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
	if _, exists := rs.subscribers[sid1]; !exists {
		t.Fatal("wrong subscriber")
	}
	subscriber, exists = sm.subscribers[sid1]
	if !exists || len(subscriber.subscriptions) != 1 {
		t.Fatal("missing subscriber")
	}
	sub, exists = subscriber.subscriptions[eid]
	if !exists || sub != nil || expectedSRV != nil {
		t.Fatal("wrong rs")
	}

	// Subscribe again as a different subscriber.
	subscriber2 := sm.newSubscriber()
	sid2 := subscriber2.staticSubscriberID
	rv = subscriber2.Subscribe(spk, tweak)
	if !reflect.DeepEqual(rv, &srv) {
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
	if _, exists := rs.subscribers[sid1]; !exists {
		t.Fatal("wrong subscriber")
	}
	if _, exists := rs.subscribers[sid2]; !exists {
		t.Fatal("wrong subscriber")
	}
	// Check first subscriber.
	subscriber, exists = sm.subscribers[sid1]
	if !exists || len(subscriber.subscriptions) != 1 {
		t.Fatal("missing subscriber")
	}
	sub, exists = subscriber.subscriptions[eid]
	if !exists || !reflect.DeepEqual(sub, expectedSRV) {
		t.Fatal("wrong rs")
	}
	// Check second subscriber. This one should have the latest value set in the
	// user subscription already.
	subscriber, exists = sm.subscribers[sid2]
	if !exists || len(subscriber.subscriptions) != 1 {
		t.Fatal("missing subscriber")
	}
	expectedSRV = rs.latestValue
	sub, exists = subscriber.subscriptions[eid]
	if !exists || !reflect.DeepEqual(sub, expectedSRV) {
		t.Fatal("wrong rs")
	}

	// Unsubscribe the first subscriber.
	if err := subscriber1.Close(); err != nil {
		t.Fatal(err)
	}
	delete(expectedRS.subscribers, sid1)

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
	sub, exists = subscriber.subscriptions[eid]
	if !exists || !reflect.DeepEqual(sub, expectedSRV) {
		t.Fatal("wrong rs")
	}

	// Unsubscribe the second subscriber.
	if err := subscriber2.Close(); err != nil {
		t.Fatal(err)
	}
	if len(sm.subscriptions) != 0 || len(sm.subscribers) != 0 {
		t.Fatal("wrong number of subscriptions and/or subscribers")
	}
}

// testSubscriptionManagerNotify is a unit test for notifying the subscribers of
// new values.
func testSubscriptionManagerNotify(t *testing.T, r *Renter) {
	sm := newSubscriptionManager(r)

	// Create random pubkey tweak pair.
	value1, spk, sk := randomRegistryValue()
	srv1 := skymodules.NewRegistryEntry(spk, value1)
	tweak := srv1.Tweak
	eid := modules.DeriveRegistryEntryID(spk, tweak)

	// Prepare more revisions of the srv.
	srv2 := srv1
	srv2.Revision++
	srv2.Sign(sk)
	srv3 := srv2
	srv3.Revision++
	srv3.Sign(sk)

	// Notify the manager of this pair before subscribing.
	sm.Notify(modules.RPCRegistrySubscriptionNotificationEntryUpdate{
		Entry:  srv1.SignedRegistryValue,
		PubKey: spk,
	})

	// Create a subscriber for that pair which counts the number of updates and
	// allows for returning a custom error.
	var updates []skymodules.RegistryEntry
	var updateMu sync.Mutex
	var notifyErr error
	subscriber := sm.NewSubscriber(func(srv skymodules.RegistryEntry) error {
		updateMu.Lock()
		defer updateMu.Unlock()
		updates = append(updates, srv)
		return notifyErr
	})

	latestValue := subscriber.Subscribe(spk, tweak)
	if latestValue != nil {
		t.Fatal("value should be nil")
	}

	// Notify the manager again after subscribing.
	sm.Notify(modules.RPCRegistrySubscriptionNotificationEntryUpdate{
		Entry:  srv1.SignedRegistryValue,
		PubKey: spk,
	})
	// Latest values should be updated.
	err := build.Retry(100, 100*time.Millisecond, func() error {
		sm.mu.Lock()
		defer sm.mu.Unlock()
		if !reflect.DeepEqual(*sm.subscriptions[eid].latestValue, srv1) {
			return errors.New("wrong latest value")
		}
		subscriber.mu.Lock()
		defer subscriber.mu.Unlock()
		if subscriber.subscriptions[eid] == nil || !reflect.DeepEqual(*subscriber.subscriptions[eid], srv1) {
			return errors.New("wrong latest value")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Notify the manager of the same entry again.
	sm.Notify(modules.RPCRegistrySubscriptionNotificationEntryUpdate{
		Entry:  srv1.SignedRegistryValue,
		PubKey: spk,
	})
	// Latest values should be the same.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		sm.mu.Lock()
		defer sm.mu.Unlock()
		if !reflect.DeepEqual(*sm.subscriptions[eid].latestValue, srv1) {
			return errors.New("wrong latest value")
		}
		subscriber.mu.Lock()
		defer subscriber.mu.Unlock()
		if !reflect.DeepEqual(*subscriber.subscriptions[eid], srv1) {
			return errors.New("wrong latest value")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Notify the manager of a higher revision entry.
	sm.Notify(modules.RPCRegistrySubscriptionNotificationEntryUpdate{
		Entry:  srv2.SignedRegistryValue,
		PubKey: spk,
	})
	// Latest values should be updated.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		sm.mu.Lock()
		defer sm.mu.Unlock()
		if !reflect.DeepEqual(*sm.subscriptions[eid].latestValue, srv2) {
			return errors.New("wrong latest value")
		}
		subscriber.mu.Lock()
		defer subscriber.mu.Unlock()
		if !reflect.DeepEqual(*subscriber.subscriptions[eid], srv2) {
			return errors.New("wrong latest value")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Notify the manager of a higher revision entry but make the notification
	// function fail.
	notifyErr = errors.New("failure")
	sm.Notify(modules.RPCRegistrySubscriptionNotificationEntryUpdate{
		Entry:  srv3.SignedRegistryValue,
		PubKey: spk,
	})
	// The manager's value should be updated but not the subscriber's.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		sm.mu.Lock()
		defer sm.mu.Unlock()
		if !reflect.DeepEqual(*sm.subscriptions[eid].latestValue, srv3) {
			return errors.New("wrong latest value")
		}
		subscriber.mu.Lock()
		defer subscriber.mu.Unlock()
		if !reflect.DeepEqual(*subscriber.subscriptions[eid], srv2) {
			return errors.New("wrong latest value")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Check if the right updates were sent. They should have been sent in the
	// right order and only once per revision.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		updateMu.Lock()
		defer updateMu.Unlock()
		expectedUpdates := []skymodules.RegistryEntry{srv1, srv2, srv3}
		if !reflect.DeepEqual(updates, expectedUpdates) {
			return errors.New("updates don't match")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// testSubscriptionManagerSubscribeUnsubscribeParallel subscribes, unsubscribes
// and notifies in parallel to have the go race checker verify that the locking
// in the subscription manager is sound and we don't have any race conditions.
func testSubscriptionManagerSubscribeUnsubscribeParallel(t *testing.T, r *Renter) {
	sm := newSubscriptionManager(r)

	// Declare a helper type.
	type request struct {
		staticSPK types.SiaPublicKey
		staticSK  crypto.SecretKey

		srv modules.SignedRegistryValue
		mu  sync.RWMutex
	}

	// Create random pubkey tweak pairs.
	var requests []*request
	n := 100
	for i := 0; i < n; i++ {
		srv, spk, sk := randomRegistryValue()
		requests = append(requests, &request{
			staticSPK: spk,
			srv:       srv,
			staticSK:  sk,
		})
	}

	// Create a goroutine for each entry that updates the entry.
	ticker := time.NewTicker(10 * time.Millisecond)
	stopTicker := make(chan struct{})
	var wgTicker sync.WaitGroup
	for i := range requests {
		wgTicker.Add(1)
		go func(i int) {
			defer wgTicker.Done()
			for {
				select {
				case <-ticker.C:
				case <-stopTicker:
					return
				}
				requests[i].mu.Lock()
				requests[i].srv.Revision++
				requests[i].srv = requests[i].srv.Sign(requests[i].staticSK)
				requests[i].mu.Unlock()

				sm.Notify(modules.RPCRegistrySubscriptionNotificationEntryUpdate{
					PubKey: requests[i].staticSPK,
					Entry:  requests[i].srv,
				})
			}
		}(i)
	}

	var wg sync.WaitGroup
	var wgUnsubscribe sync.WaitGroup
	start := make(chan struct{})
	unsubscribe := make(chan struct{})
	for _, req := range requests {
		wg.Add(1)
		wgUnsubscribe.Add(1)
		go func(req *request) {
			defer wg.Done()

			// Wait for the start signal.
			<-start

			// Create the subscriber and subscribe
			subscriber := sm.newSubscriber()
			req.mu.RLock()
			tweak := req.srv.Tweak
			req.mu.RUnlock()
			_ = subscriber.Subscribe(req.staticSPK, tweak)

			// Wait for the unsubscribe signal.
			wgUnsubscribe.Done()
			<-unsubscribe

			// Wait a second and then unsubscribe.
			err := subscriber.Close()
			if err != nil {
				t.Error(err)
			}
		}(req)
	}

	// Start the threads.
	close(start)

	// Wait for them to reach the unsubscribe chan.
	wgUnsubscribe.Wait()

	// Check subscribers and subscriptions.
	sm.mu.Lock()
	if len(sm.subscribers) != n {
		t.Errorf("subscribers %v != %v", len(sm.subscribers), n)
	}
	if len(sm.subscriptions) != n {
		t.Errorf("subscriptions %v != %v", len(sm.subscriptions), n)
	}
	sm.mu.Unlock()

	// Unblock them.
	close(unsubscribe)

	// Wait for them to finish.
	wg.Wait()

	// Stop the ticker.
	close(stopTicker)

	// Wait for it to be done.
	wgTicker.Wait()

	// Check subscribers and subscriptions.
	sm.mu.Lock()
	if len(sm.subscribers) != 0 {
		t.Errorf("subscribers %v != %v", len(sm.subscribers), 0)
	}
	if len(sm.subscriptions) != 0 {
		t.Errorf("subscriptions %v != %v", len(sm.subscriptions), 0)
	}
	sm.mu.Unlock()
}
