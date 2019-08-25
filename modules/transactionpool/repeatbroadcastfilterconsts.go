package transactionpool

import (
	"gitlab.com/NebulousLabs/Sia/build"
)

var (
	// repeatBroadcastFilterEvictionFrequency indicates how often transactions
	// are evicted from the repeat broadcast filter. Each time a unique
	// transaction is sent to a peer, this counts as one.
	repeatBroadcastFilterEvictionFrequency = build.Select(build.Var{
		Standard: uint64(50e3),
		Dev:      uint64(25),
		Testing:  uint64(10),
	}).(uint64)
)
