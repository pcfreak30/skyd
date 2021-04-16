package renter

import (
	"sync"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
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
		staticRenter *Renter

		subscriptions map[modules.RegistryEntryID]*renterSubscription
		subscribers   map[subscriberID]*renterSubscriber
		mu            sync.Mutex
	}

	// renterSubscription contains information related to the renter's
	// subscription.
	renterSubscription struct {
		staticSPK   types.SiaPublicKey
		staticTweak crypto.Hash

		latestValue *modules.SignedRegistryValue
		subscribers map[subscriberID]struct{}
	}

	// renterSubscriber contains information about a subscriber.
	renterSubscriber struct {
		subscriptions map[modules.RegistryEntryID]*modules.SignedRegistryValue

		staticNotifyFunc          func(*modules.SignedRegistryValue) error
		staticSubscriberID        subscriberID
		staticSubscriptionManager *registrySubscriptionManager
		notificationMu            sync.Mutex
	}

	// subscriberID is a helper type to uniquely identify a subscriber.
	subscriberID types.Specifier
)

// newSubscriptionManager creates a new subscription manager.
func newSubscriptionManager(renter *Renter) *registrySubscriptionManager {
	return &registrySubscriptionManager{
		staticRenter:  renter,
		subscriptions: make(map[modules.RegistryEntryID]*renterSubscription),
		subscribers:   make(map[subscriberID]*renterSubscriber),
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
		srv := &notification.Entry

		if moreRecentSRV(sub.latestValue, srv) {
			sub.latestValue = srv
			changedSubs[eid] = sub
			continue
		}
	}

	// Notify subscribers.
	for eid, sub := range changedSubs {
		for sid := range sub.subscribers {
			// Get subscriber.
			subscriber, exists := sm.subscribers[sid]
			if !exists {
				continue
			}
			go subscriber.threadedNotify(eid, sub.latestValue)
		}
	}
}

// Close closes the subscriber and unsubscribes it from all entries.
func (subscriber *renterSubscriber) Close() error {
	sm := subscriber.staticSubscriptionManager
	sm.managedUnsubscribeAll(subscriber.staticSubscriberID)
	return nil
}

// Subscribe subscribes the subscriber to an entry.
func (subscriber *renterSubscriber) Subscribe(spk types.SiaPublicKey, tweak crypto.Hash) *modules.SignedRegistryValue {
	sm := subscriber.staticSubscriptionManager
	return sm.managedSubscribe(spk, tweak, subscriber)
}

// Unsubscribe unsubscribes the subscriber from an entry.
func (subscriber *renterSubscriber) Unsubscribe(eid modules.RegistryEntryID) {
	sm := subscriber.staticSubscriptionManager
	sm.managedUnsubscribe(subscriber.staticSubscriberID, eid)
}

// threadedNotify notifies a subscriber about an updated entry.
func (subscriber *renterSubscriber) threadedNotify(eid modules.RegistryEntryID, srv *modules.SignedRegistryValue) {
	subscriber.notificationMu.Lock()
	defer subscriber.notificationMu.Unlock()

	// Check if subscriber is interested in the change.
	latestSRV, exists := subscriber.subscriptions[eid]
	if !exists {
		return
	}

	// Check if the new srv is better than the latest one.
	if !moreRecentSRV(latestSRV, srv) {
		return // nothing to do
	}

	// Notify subscriber.
	err := subscriber.staticNotifyFunc(srv)
	if err != nil {
		return // notification func will log error
	}

	// If the notification was successful, we update the latest value.
	subscriber.subscriptions[eid] = srv
}

// Get allows for fetching the latest value of a subscribed entry from the
// subscription manager.
func (sm *registrySubscriptionManager) Get(eid modules.RegistryEntryID) (modules.SignedRegistryValue, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sub, exists := sm.subscriptions[eid]
	if !exists || sub.latestValue == nil {
		return modules.SignedRegistryValue{}, false
	}
	if sub.latestValue == nil {
		return modules.SignedRegistryValue{}, false
	}
	return *sub.latestValue, true
}

// SetNotificationFunc sets the notification function for a subscriber. This can
// be called before subscribing to a value
func (sm *registrySubscriptionManager) NewSubscriber(notifyFunc func(*modules.SignedRegistryValue) error) *renterSubscriber {
	var sid subscriberID
	fastrand.Read(sid[:])
	return &renterSubscriber{
		subscriptions:             make(map[modules.RegistryEntryID]*modules.SignedRegistryValue),
		staticNotifyFunc:          notifyFunc,
		staticSubscriberID:        sid,
		staticSubscriptionManager: sm,
	}
}

// managedSubscribe subscribes a subscriber to an entry.
func (sm *registrySubscriptionManager) managedSubscribe(spk types.SiaPublicKey, tweak crypto.Hash, sub *renterSubscriber) *modules.SignedRegistryValue {
	eid := modules.DeriveRegistryEntryID(spk, tweak)

	// Check if the subscription exists already. If not, create it.
	sm.mu.Lock()
	rs, subExists := sm.subscriptions[eid]
	if !subExists {
		rs = &renterSubscription{
			staticSPK:   spk,
			staticTweak: tweak,
			subscribers: make(map[subscriberID]struct{}),
		}
		sm.subscriptions[eid] = rs
	}

	// Check if subscriber exists already. If not, create it.
	sid := sub.staticSubscriberID
	subscriber, exists := sm.subscribers[sid]
	if !exists {
		subscriber = sub
		sm.subscribers[sid] = subscriber
	}

	// Add the subscription to the subscriber if it doesn't exist yet.
	_, exists = subscriber.subscriptions[eid]
	if !exists {
		subscriber.subscriptions[eid] = rs.latestValue
		rs.subscribers[sid] = struct{}{}
	}

	// If the sub wasn't new, return the latest known value.
	if subExists {
		sm.mu.Unlock()
		return rs.latestValue
	}

	// Otherwise, update the workers. They will notify us as soon as a value
	// becomes availeble.
	requests := sm.buildSubscriptionRequests()
	sm.mu.Unlock()
	sm.managedUpdateWorkersWithRequests(requests)
	return nil
}

// Unsubscribe unsubscribes a subscriber from a single entry.
func (sm *registrySubscriptionManager) managedUnsubscribe(sid subscriberID, eid modules.RegistryEntryID) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.unsubscribe(sid, eid)
}

// unsubscribe unsubscribes a subscriber from a single entry.
func (sm *registrySubscriptionManager) unsubscribe(sid subscriberID, eid modules.RegistryEntryID) {
	// Get subscriber.
	rs, exists := sm.subscribers[sid]
	if !exists {
		build.Critical("unsubscribing from unknown sid")
		return
	}
	delete(rs.subscriptions, eid)

	// Unsubscribe. If this was the last the last subscriber, delete the
	// subscription.
	sub, exists := sm.subscriptions[eid]
	if !exists {
		return // nothing to do
	}
	delete(sub.subscribers, sid)
	if len(sub.subscribers) == 0 {
		delete(sm.subscriptions, eid)
	}
}

// UnsubscribeAll completely unsubsribes a subscriber and all related
// subscriptions.
func (sm *registrySubscriptionManager) managedUnsubscribeAll(sid subscriberID) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Get subscriber.
	us, exists := sm.subscribers[sid]
	if !exists {
		build.Critical("unsubscribing unknown subscriber")
		return
	}
	// Unsubscribe from all subscribed entries.
	for eid := range us.subscriptions {
		sm.unsubscribe(sid, eid)
	}
	// Remove subscriber from sm.subscribers.
	delete(sm.subscribers, sid)
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
	sm.managedUpdateWorkersWithRequests(requests)
}

// managedUpdateWorkersWithRequests updates all workers with the given requests.
func (sm *registrySubscriptionManager) managedUpdateWorkersWithRequests(requests []modules.RPCRegistrySubscriptionRequest) {
	// Update workers.
	for _, w := range sm.staticRenter.staticWorkerPool.callWorkers() {
		w.UpdateSubscriptions(requests...)
	}
}

// moreRecentSRV returns true if srv1 should be replaced by the more recent
// srv2.
func moreRecentSRV(srv1, srv2 *modules.SignedRegistryValue) bool {
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
