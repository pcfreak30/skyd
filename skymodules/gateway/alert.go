package gateway

import "gitlab.com/skynetlabs/skyd/skymodules"

// Alerts implements the skymodules.Alerter interface for the gateway.
func (g *Gateway) Alerts() (crit, err, warn []skymodules.Alert) {
	return g.staticAlerter.Alerts()
}
