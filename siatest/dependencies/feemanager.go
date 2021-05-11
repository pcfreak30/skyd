package dependencies

import (
	"sync"

	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/types"
)

// DependencyCustomSkynetAddress will use a custom address for the Skynet
// address when processing a fee.
type DependencyCustomSkynetAddress struct {
	skymodules.SkynetDependencies

	address types.UnlockHash
	mu      sync.Mutex
}

// SkynetAddress returns the custom address of the dependency.
func (d *DependencyCustomSkynetAddress) SkynetAddress() types.UnlockHash {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.address
}

// SetAddress sets the address field of the dependency.
func (d *DependencyCustomSkynetAddress) SetAddress(addr types.UnlockHash) {
	d.mu.Lock()
	d.address = addr
	d.mu.Unlock()
}
