package skynet

import (
	"os"

	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/SkynetLabs/skyd/siatest"
)

// skynetTestDir creates a temporary testing directory for a renter test. This
// should only every be called once per test. Otherwise it will delete the
// directory again.
func skynetTestDir(testName string) string {
	path := siatest.TestDir("renter", testName)
	if err := os.MkdirAll(path, persist.DefaultDiskPermissionsTest); err != nil {
		panic(err)
	}
	return path
}
