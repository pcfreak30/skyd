package renter

import (
	"sync"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
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
		mu sync.Mutex

		staticWorkers *workerPool

		subscriptions map[modules.RegistryEntryID]*renterSubscription
		subscribers   map[subscriberID]*renterSubscriber
	}

	renterSubscriber struct {
		subscriptions map[modules.RegistryEntryID]*renterSubscription
	}

	renterSubscription struct {
		latestValue *modules.SignedRegistryValue
		refcount    int64
		staticSPK   types.SiaPublicKey
		staticTweak crypto.Hash
	}

	subscriberID types.Specifier
)

// newSubscriptionManager creates a new subscription manager.
func newSubscriptionManager(workerPool *workerPool) *registrySubscriptionManager {
	return &registrySubscriptionManager{
		staticWorkers: workerPool,
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
	for _, notification := range notifications {
		eid := modules.DeriveRegistryEntryID(notification.PubKey, notification.Entry.Tweak)

		sub, exists := sm.subscriptions[eid]
		if !exists {
			continue
		}
		srv := &notification.Entry

		// If the latest value is nil, update it.
		if sub.latestValue == nil {
			sub.latestValue = srv
			continue
		}
		// If the latest value has a lower revision number, update it.
		if sub.latestValue.Revision < srv.Revision {
			sub.latestValue = srv
			continue
		}
		// If the revision numbers are the same, check the pow.
		if sub.latestValue.Revision == srv.Revision &&
			srv.HasMoreWork(sub.latestValue.RegistryValue) {
			sub.latestValue = srv
			continue
		}
	}
	sm.mu.Unlock()

	// Notify subscribers.
	for _, sub := range changedSubs {
		sub.managedNotifySubscribers()
	}
}

func (sub *renterSubscription) managedNotifySubscribers() {
	panic("implement")
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

func (sm *registrySubscriptionManager) Subscribe(spk types.SiaPublicKey, tweak crypto.Hash, sid subscriberID) *modules.SignedRegistryValue {
	eid := modules.DeriveRegistryEntryID(spk, tweak)

	// Check if the subscription exists already. If not, create it.
	sm.mu.Lock()
	rs, subExists := sm.subscriptions[eid]
	if !subExists {
		rs = &renterSubscription{
			staticSPK:   spk,
			staticTweak: tweak,
		}
		sm.subscriptions[eid] = rs
	}

	// Check if subscriber exists already. If not, create it.
	subscriber, exists := sm.subscribers[sid]
	if !exists {
		subscriber = &renterSubscriber{
			subscriptions: make(map[modules.RegistryEntryID]*renterSubscription),
		}
		sm.subscribers[sid] = subscriber
	}

	// Add the subscription to the subscriber if it doesn't exist yet.
	// Also increment the refcount in that case.
	_, exists = subscriber.subscriptions[eid]
	if !exists {
		subscriber.subscriptions[eid] = rs
		rs.refcount++
	}

	// If the sub wasn't new, return the latest known value.
	if subExists {
		sm.mu.Unlock()
		return rs.latestValue
	}
	requests := sm.buildSubscriptionRequests()
	sm.mu.Unlock()

	// Otherwise, update the workers. They will notify us as soon as a value
	// becomes availeble.
	sm.staticUpdateWorkers(requests)
	return nil
}

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

func (sm *registrySubscriptionManager) managedUpdateWorkers() {
	sm.mu.Lock()
	requests := sm.buildSubscriptionRequests()
	sm.mu.Unlock()
	sm.staticUpdateWorkers(requests)
}

func (sm *registrySubscriptionManager) staticUpdateWorkers(requests []modules.RPCRegistrySubscriptionRequest) {
	// Update workers.
	for _, w := range sm.staticWorkers.callWorkers() {
		w.UpdateSubscriptions(requests...)
	}
}
