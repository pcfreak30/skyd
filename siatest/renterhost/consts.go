package renterhost

import (
	"os"

	"gitlab.com/SkynetLabs/skyd/siatest"
	"go.sia.tech/siad/persist"
)

// renterHostTestDir creates a temporary testing directory for a renterhost
// test. This should only every be called once per test. Otherwise it will
// delete the directory again.
func renterHostTestDir(testName string) string {
	path := siatest.TestDir("renterhost", testName)
	if err := os.MkdirAll(path, persist.DefaultDiskPermissionsTest); err != nil {
		panic(err)
	}
	return path
}
