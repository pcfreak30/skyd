package dependencies

import "gitlab.com/skynetlabs/skyd/skymodules"

// AccountingDisablePersistLoop is a dependency that disables the background
// loop from updating and persisting the accounting information.
type AccountingDisablePersistLoop struct {
	skymodules.ProductionDependencies
}

// Disrupt will prevent the Accounting module from launching the background
// persist loop.
func (d *AccountingDisablePersistLoop) Disrupt(s string) bool {
	return s == "DisablePersistLoop"
}
