package dependencies

import (
	"sync"

	"go.sia.tech/siad/modules"
)

// DependencySkipUnpinRequest skips submitting the unpin request.
type DependencySkipUnpinRequest struct {
	modules.ProductionDependencies

	disabled bool
	mu       sync.Mutex
}

// Disable disables the dependency
func (d *DependencySkipUnpinRequest) Disable() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.disabled = true
}

// Disrupt skips the submission of the unpin request.
func (d *DependencySkipUnpinRequest) Disrupt(s string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return s == "SkipUnpinRequest" && !d.disabled
}

// Enable enables the dependency
func (d *DependencySkipUnpinRequest) Enable() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.disabled = false
}
