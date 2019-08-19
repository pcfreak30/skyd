package transactionpool

import (
	"time"
)

const (
	// peerShareRateLimit defines the number of nanoseconds per byte that the
	// limiter will wait between unblocking calls to share a transaction set
	// with a new peer.
	//
	// TODO: Make this a configurable value in the API.
	peerShareRateLimit = 250 * time.Nanosecond // 250 Nanoseconds per byte is 4mbps per peer.

	// onlineSyncedLoopSleepTime is the amount of time that the tpool will sleep
	// between checks to se if it is online and synced.
	//
	// TODO: This constant can be eliminated once the gatway supports a
	// subsscription model that allows the gateway to announce changes in the
	// peer list instead of forcing would-be subscribers to poll.
	onlineSyncedLoopSleepTime = 30 * time.Second
)
