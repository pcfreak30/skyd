package dependencies

import (
	"gitlab.com/skynetlabs/skyd/skymodules"
)

// DependencyDoNotAcceptTxnSet will not accept a transaction set.
type DependencyDoNotAcceptTxnSet struct {
	skymodules.ProductionDependencies
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyDoNotAcceptTxnSet) Disrupt(s string) bool {
	return s == "DoNotAcceptTxnSet"
}
