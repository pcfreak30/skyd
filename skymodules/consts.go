package skymodules

import "go.sia.tech/siad/types"

const (
	// StackSize is the size of the buffer used to store the stack trace.
	StackSize = 64e6 // 64MB
)

// skynetPayoutAddress is an address owned by Skynet Labs
var skynetPayoutAddress = [32]byte{14, 56, 201, 152, 87, 64, 139, 125, 38, 4, 161, 206, 32, 198, 119, 108, 158, 66, 177, 5, 178, 222, 155, 12, 209, 231, 91, 170, 213, 236, 57, 197}

// SkynetPayoutAddress is the address used for skynet related payouts
func SkynetPayoutAddress() types.UnlockHash {
	return skynetPayoutAddress
}
