package skymodules

import (
	"testing"

	"go.sia.tech/siad/types"
)

// TestSkynetAddress is a unit test to confirm the 32 byte representation of the
// skynetAddress matches the string representation
func TestSkynetAddress(t *testing.T) {
	expected := "537affa97693a48e23918e59ce281243f2d748bb8bfdada49535c5faad3efac2b5fe4f168390"
	actual := types.UnlockHash(skynetAddress).String()
	if expected != actual {
		t.Fatalf("Skynet Address Mismatch: xpected %v, got %v", expected, actual)
	}
}
