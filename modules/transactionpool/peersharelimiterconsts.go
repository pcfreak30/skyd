package transactionpool

import (
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
)

var (
	// peerShareRateLimit defines the number of nanoseconds per byte that the
	// limiter will wait between unblocking calls to share a transaction set
	// with a new peer.
	//
	// TODO: Make this a configurable value in the API.
	peerShareRateLimit = build.Select(build.Var{
		Standard: 250 * time.Nanosecond,  // 4 mbps
		Dev:      2000 * time.Nanosecond, // 500 kbps
		Testing:  20e3 * time.Nanosecond, // 50 kbps
	}).(time.Duration)

	// onlineSyncedLoopSleepTime is the amount of time that the tpool will sleep
	// between checks to se if it is online and synced.
	//
	// TODO: This constant can be eliminated once the gatway supports a
	// subsscription model that allows the gateway to announce changes in the
	// peer list instead of forcing would-be subscribers to poll.
	onlineSyncedLoopSleepTime = build.Select(build.Var{
		Standard: 30 * time.Second,
		Dev:      10 * time.Second,
		Testing:  3 * time.Second,
	}).(time.Duration)
)
