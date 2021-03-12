package transactionpool

import "gitlab.com/skynetlabs/skyd/skymodules"

// Alerts implements the skymodules.Alerter interface for the transactionpool.
func (tpool *TransactionPool) Alerts() (crit, err, warn []skymodules.Alert) {
	return []skymodules.Alert{}, []skymodules.Alert{}, []skymodules.Alert{}
}
