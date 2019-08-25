package transactionpool

import (
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
)

var (
	// newPeerPollingFrequency is the amount of time that the gateway will sleep
	// between checking for new peers.
	newPeerPollingFrequency = build.Select(build.Var{
		Standard: 30 * time.Second,
		Dev:      10 * time.Second,
		Testing:  3 * time.Second,
	}).(time.Duration)
)
