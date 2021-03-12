package hostdb

import "gitlab.com/skynetlabs/skyd/skymodules"

// Alerts implements the skymodules.Alerter interface for the hostdb. It returns
// all alerts of the hostdb.
func (hdb *HostDB) Alerts() (crit, err, warn []skymodules.Alert) {
	return hdb.staticAlerter.Alerts()
}
