package transactionpool

const (
	// repeatBroadcastFilterEvictionFrequency indicates how often transactions
	// are evicted from the repeat broadcast filter. Each time a unique
	// transaction is sent to a peer, this counts as one.
	repeatBroadcastFilterEvictionFrequency = 50e3
)
