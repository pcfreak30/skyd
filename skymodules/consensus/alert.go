package consensus

import (
	"gitlab.com/skynetlabs/skyd/skymodules"
)

// Alerts implements the Alerter interface for the consensusset.
func (c *ConsensusSet) Alerts() (crit, err, warn []skymodules.Alert) {
	return []skymodules.Alert{}, []skymodules.Alert{}, []skymodules.Alert{}
}
