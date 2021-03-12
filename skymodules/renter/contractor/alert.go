package contractor

import "gitlab.com/skynetlabs/skyd/skymodules"

// Alerts implements the skymodules.Alerter interface for the contractor. It returns
// all alerts of the contractor.
func (c *Contractor) Alerts() (crit, err, warn []skymodules.Alert) {
	return c.staticAlerter.Alerts()
}
