package wallet

import (
	"gitlab.com/skynetlabs/skyd/skymodules"
)

// Alerts implements the Alerter interface for the wallet.
func (w *Wallet) Alerts() (crit, err, warn []skymodules.Alert) {
	return []skymodules.Alert{}, []skymodules.Alert{}, []skymodules.Alert{}
}
