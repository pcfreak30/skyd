package explorer

import "gitlab.com/skynetlabs/skyd/skymodules"

// Alerts implements the skymodules.Alerter interface for the explorer.
func (e *Explorer) Alerts() (crit, err, warn []skymodules.Alert) {
	return []skymodules.Alert{}, []skymodules.Alert{}, []skymodules.Alert{}
}
