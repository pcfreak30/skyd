package transactionpool

import (
	"time"
)

const (
	// newPeerPollingFrequency is the amount of time that the gateway will sleep
	// between checking for new peers.
	newPeerPollingFrequency = 30 * time.Second
)
