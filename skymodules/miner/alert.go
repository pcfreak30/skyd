package miner

import "gitlab.com/skynetlabs/skyd/skymodules"

// Alerts implements the skymodules.Alerter interface for the miner.
func (m *Miner) Alerts() (crit, err, warn []skymodules.Alert) {
	return []skymodules.Alert{}, []skymodules.Alert{}, []skymodules.Alert{}
}
