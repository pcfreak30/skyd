package renter

import (
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
)

type (
	// subscriptionManager is the interface of the subscriptionManager that is
	// notified whenever any worker receives an update for a subscribed value.
	subscriptionManager interface {
		Notify(types.SiaPublicKey, *modules.SignedRegistryValue)
	}

	// registrySubscriptionManager is the renter's global subscription manager.
	// It manages the subscriptions across workers and notifies subscribers.
	registrySubscriptionManager struct {
	}
)

// newSubscriptionManager creates a new subscription manager.
func newSubscriptionManager() *registrySubscriptionManager {
	return &registrySubscriptionManager{}
}

// Notify implements subscriptionManager. It is called by workers whenever they
// receive a new value from a host. The manager will then forward the value to
// potential subscribers if necessary.
func (sm *registrySubscriptionManager) Notify(_ types.SiaPublicKey, _ *modules.SignedRegistryValue) {
}
