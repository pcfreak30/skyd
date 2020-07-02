package dependencies

import (
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/modules"
)

// HostExpireEphemeralAccounts is a dependency injection for the host that will
// expire ephemeral accounts as soon as they get pruned
type HostExpireEphemeralAccounts struct {
	modules.ProductionDependencies
}

// HostLowerDeposit is a dependency injection for the host that will make the
// deposit amount substantially lower. This allows us to verify the renter has
// synced its account balance with the host's balance after an unclean shutdown.
type HostLowerDeposit struct {
	modules.ProductionDependencies
}

// HostMDMProgramDelayedWrite is a dependency injection for the host that will
// ensure the response of an instruction is written after the set latency.
type HostMDMProgramDelayedWrite struct {
	modules.ProductionDependencies
}

// HostV1412 is a dependency injection for the host that will alter the host's
// version to v1.4.12. This allows us to verify behaviour where the host version
// is compared against v1.4.12.
type HostV1412 struct {
	modules.ProductionDependencies
}

// HostRejectAllSessionLocks is a dependency injection for the host that will
// cause the host to reject all contracts as though they do not exist.
type HostRejectAllSessionLocks struct {
	started bool
	mu      sync.Mutex

	modules.ProductionDependencies
}

// NewDependencyHostDiskTrouble creates a new dependency that disrupts storage
// folder operations due to disk trouble
func NewDependencyHostDiskTrouble() *DependencyInterruptOnceOnKeyword {
	return newDependencyInterruptOnceOnKeyword("diskTrouble")
}

// NewHostMaxEphemeralAccountRiskReached is a dependency injection for the host
// that will ensure the ephemeral account max saved delta is reached by
// persisting with a set latency.
func NewHostMaxEphemeralAccountRiskReached(duration time.Duration) modules.Dependencies {
	return newDependencyAddLatency("errMaxRiskReached", duration)
}

// Disrupt will interpret a signal from the host and tell the host to pretend it
// has no record of the contract.
func (d *HostRejectAllSessionLocks) Disrupt(s string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.started {
		return false
	}
	return s == "loopLockNoRecordOfThatContract"
}

// StartRejectingLocks will activate the dependency.
func (d *HostRejectAllSessionLocks) StartRejectingLocks() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.started = true
}

// Disrupt will interpret a signal from the host and tell the host to force
// expire all ephemeral accounts on the next prune cycle
func (d *HostExpireEphemeralAccounts) Disrupt(s string) bool {
	return s == "expireEphemeralAccounts"
}

// Disrupt returns true if the correct string is provided.
func (d *HostLowerDeposit) Disrupt(s string) bool {
	return s == "lowerDeposit"
}

// Disrupt returns true if the correct string is provided.
func (d *HostMDMProgramDelayedWrite) Disrupt(s string) bool {
	return s == "MDMProgramOutputDelayWrite"
}

// Disrupt returns true if the correct string is provided.
func (d *HostV1412) Disrupt(s string) bool {
	return s == "HostV1412"
}
