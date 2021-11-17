package renter

import (
	"sync"

	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

type (
	// subscriptionManager is the interface of the subscriptionManager that is
	// notified whenever any worker receives an update for a subscribed value.
	subscriptionManager interface {
		Notify(...modules.RPCRegistrySubscriptionNotificationEntryUpdate)
	}

	// registrySubscriptionManager is the renter's global subscription manager.
	// It manages the subscriptions across workers and notifies subscribers.
	registrySubscriptionManager struct {
		staticRenter               *Renter
		staticSubscriptionsChanged chan struct{}

		subscriptions map[modules.RegistryEntryID]*renterSubscription
		subscribers   map[subscriberID]*renterSubscriber
		mu            sync.Mutex
	}

	// renterSubscription contains information related to the renter's
	// subscription.
	renterSubscription struct {
		staticSPK   types.SiaPublicKey
		staticTweak crypto.Hash

		latestValue *skymodules.RegistryEntry
		subscribers map[subscriberID]struct{}
	}

	// renterSubscriber contains information about a subscriber.
	renterSubscriber struct {
		subscriptions map[modules.RegistryEntryID]*skymodules.RegistryEntry

		staticNotifyFunc          func(skymodules.RegistryEntry) error
		staticSubscriberID        subscriberID
		staticSubscriptionManager *registrySubscriptionManager

		mu sync.Mutex
	}

	// subscriberID is a helper type to uniquely identify a subscriber.
	subscriberID types.Specifier
)

// newSubscriptionManager creates a new subscription manager.
func newSubscriptionManager(renter *Renter) *registrySubscriptionManager {
	return &registrySubscriptionManager{
		staticRenter:               renter,
		staticSubscriptionsChanged: make(chan struct{}, 1),
		subscriptions:              make(map[modules.RegistryEntryID]*renterSubscription),
		subscribers:                make(map[subscriberID]*renterSubscriber),
	}
}

// Notify implements subscriptionManager. It is called by workers whenever they
// receive a new value from a host. The manager will then forward the value to
// potential subscribers if necessary.
func (sm *registrySubscriptionManager) Notify(notifications ...modules.RPCRegistrySubscriptionNotificationEntryUpdate) {
	changedSubs := make(map[modules.RegistryEntryID]*renterSubscription)
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for _, notification := range notifications {
		eid := modules.DeriveRegistryEntryID(notification.PubKey, notification.Entry.Tweak)

		sub, exists := sm.subscriptions[eid]
		if !exists {
			continue
		}
		srv := skymodules.RegistryEntry{
			SignedRegistryValue: notification.Entry,
			PubKey:              sub.staticSPK,
		}

		if moreRecentSRV(sub.latestValue, &srv) {
			sub.latestValue = &srv
			changedSubs[eid] = sub
			continue
		}
	}

	// Notify subscribers.
	for _, sub := range changedSubs {
		for sid := range sub.subscribers {
			// Get subscriber.
			subscriber, exists := sm.subscribers[sid]
			if !exists {
				continue
			}
			go subscriber.threadedNotify(sub.staticSPK, sub.latestValue)
		}
	}
}

// Close closes the subscriber and unsubscribes it from all entries.
func (rs *renterSubscriber) Close() error {
	rs.managedUnsubscribeAll()
	return nil
}

// Subscribe subscribes the subscriber to an entry.
func (rs *renterSubscriber) Subscribe(spk types.SiaPublicKey, tweak crypto.Hash) *skymodules.RegistryEntry {
	return rs.managedSubscribe(spk, tweak)
}

// Unsubscribe unsubscribes the subscriber from an entry.
func (rs *renterSubscriber) Unsubscribe(eid modules.RegistryEntryID) {
	rs.managedUnsubscribe(eid)
}

// threadedNotify notifies a subscriber about an updated entry.
func (rs *renterSubscriber) threadedNotify(spk types.SiaPublicKey, srv *skymodules.RegistryEntry) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	// Check if subscriber is interested in the change.
	eid := modules.DeriveRegistryEntryID(spk, srv.Tweak)
	latestSRV, exists := rs.subscriptions[eid]
	if !exists {
		return
	}

	// Check if the new srv is better than the latest one.
	if !moreRecentSRV(latestSRV, srv) {
		return // nothing to do
	}

	// Notify subscriber.
	err := rs.staticNotifyFunc(*srv)
	if err != nil {
		return // notification func will log error
	}

	// If the notification was successful, we update the latest value.
	rs.subscriptions[eid] = srv
}

// Get allows for fetching the latest value of a subscribed entry from the
// subscription manager.
func (sm *registrySubscriptionManager) Get(eid modules.RegistryEntryID) (*skymodules.RegistryEntry, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sub, exists := sm.subscriptions[eid]
	if !exists {
		return &skymodules.RegistryEntry{}, false
	}
	return sub.latestValue, true
}

// NewSubscriber creates a new subscriber that can subscribe to and unsubscribe
// from entries. It needs to be closed to make sure it is cleanly unsubscribed
// from all entries.
func (sm *registrySubscriptionManager) NewSubscriber(notifyFunc func(skymodules.RegistryEntry) error) *renterSubscriber {
	var sid subscriberID
	fastrand.Read(sid[:])
	return &renterSubscriber{
		subscriptions:             make(map[modules.RegistryEntryID]*skymodules.RegistryEntry),
		staticNotifyFunc:          notifyFunc,
		staticSubscriberID:        sid,
		staticSubscriptionManager: sm,
	}
}

// managedSubscribe subscribes a subscriber to an entry.
func (rs *renterSubscriber) managedSubscribe(spk types.SiaPublicKey, tweak crypto.Hash) *skymodules.RegistryEntry {
	sm := rs.staticSubscriptionManager
	eid := modules.DeriveRegistryEntryID(spk, tweak)

	// Check if the subscription exists already. If not, create it.
	sm.mu.Lock()
	sub, subExists := sm.subscriptions[eid]
	if !subExists {
		sub = &renterSubscription{
			staticSPK:   spk,
			staticTweak: tweak,
			subscribers: make(map[subscriberID]struct{}),
		}
		sm.subscriptions[eid] = sub
	}

	// Add the subscriber to the subscription.
	sub.subscribers[rs.staticSubscriberID] = struct{}{}

	// Get the latest value of the sub.
	latestValue := sub.latestValue

	// Add the subscription to the subscriber if it doesn't exist yet. We do
	// this last to avoid holding the sm.mu.
	defer func() {
		rs.mu.Lock()
		_, exists := rs.subscriptions[eid]
		if !exists {
			rs.subscriptions[eid] = latestValue
		}
		rs.mu.Unlock()
	}()

	// Check if subscriber exists already. If not, create it.
	sid := rs.staticSubscriberID
	_, exists := sm.subscribers[sid]
	if !exists {
		sm.subscribers[sid] = rs
	}

	// If the sub wasn't new, return the latest known value.
	if subExists {
		sm.mu.Unlock()
		return latestValue
	}
	// Otherwise, update the workers. They will notify us as soon as a value
	// becomes availeble.
	select {
	case sm.staticSubscriptionsChanged <- struct{}{}:
	default:
	}
	sm.mu.Unlock()
	return nil
}

// managedUnsubscribe unsubscribes a subscriber from a single entry.
func (rs *renterSubscriber) managedUnsubscribe(eid modules.RegistryEntryID) {
	rs.mu.Lock()
	delete(rs.subscriptions, eid)
	rs.mu.Unlock()

	// Unsubscribe. If this was the last subscriber, delete the
	// subscription.
	sm := rs.staticSubscriptionManager
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sub, exists := sm.subscriptions[eid]
	if !exists {
		return // nothing to do
	}
	delete(sub.subscribers, rs.staticSubscriberID)
	if len(sub.subscribers) == 0 {
		delete(sm.subscriptions, eid)
	}
}

// UnsubscribeAll completely unsubsribes a subscriber and all related
// subscriptions.
func (rs *renterSubscriber) managedUnsubscribeAll() {
	// Remove subscriber from sm.subscribers.
	sm := rs.staticSubscriptionManager
	sm.mu.Lock()
	delete(sm.subscribers, rs.staticSubscriberID)
	sm.mu.Unlock()

	// Unsubscribe from all subscribed entries.
	rs.mu.Lock()
	oldSubscriptions := rs.subscriptions
	rs.subscriptions = make(map[modules.RegistryEntryID]*skymodules.RegistryEntry)
	rs.mu.Unlock()
	for eid := range oldSubscriptions {
		rs.managedUnsubscribe(eid)
	}
}

// builSubscriptionRequests creates subscription requests from all currently
// active subscriptions.
func (sm *registrySubscriptionManager) buildSubscriptionRequests() []modules.RPCRegistrySubscriptionRequest {
	requests := make([]modules.RPCRegistrySubscriptionRequest, 0, len(sm.subscriptions))
	for _, sub := range sm.subscriptions {
		requests = append(requests, modules.RPCRegistrySubscriptionRequest{
			PubKey: sub.staticSPK,
			Tweak:  sub.staticTweak,
		})
	}
	return requests
}

// threadedUpdateWorkers updates the subscriptions on the workers whenever the workerpool changes.
func (sm *registrySubscriptionManager) threadedUpdateWorkers() {
	r := sm.staticRenter
	if err := r.tg.Add(); err != nil {
		return
	}
	defer r.tg.Done()

	poolChanged := r.staticWorkerPool.callChangeChan()
	for {
		select {
		case <-r.tg.StopChan():
			return
		case <-poolChanged:
		case <-sm.staticSubscriptionsChanged:
		}
		sm.managedUpdateWorkers()
		poolChanged = r.staticWorkerPool.callChangeChan()
	}
}

// managedUpdateWorkers updates the subscription on all workers from the current
// active subscriptions.
func (sm *registrySubscriptionManager) managedUpdateWorkers() {
	sm.mu.Lock()
	requests := sm.buildSubscriptionRequests()
	sm.mu.Unlock()
	for _, w := range sm.staticRenter.staticWorkerPool.callWorkers() {
		w.UpdateSubscriptions(requests...)
	}
}

// moreRecentSRV returns true if srv1 should be replaced by the more recent
// srv2.
func moreRecentSRV(srv1, srv2 *skymodules.RegistryEntry) bool {
	// If the latest value is nil, update it.
	if srv1 == nil {
		return true
	}
	// If the latest value has a lower revision number, update it.
	if srv1.Revision < srv2.Revision {
		return true
	}
	// If the revision numbers are the same, check the pow.
	if srv1.Revision == srv2.Revision &&
		srv2.HasMoreWork(srv1.RegistryValue) {
		return true
	}
	return false
}
