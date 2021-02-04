package accounting

import (
	"os"

	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/Sia/siatest"
)

// accountingTestDir creates a temporary testing directory for accounting tests.
// This should only every be called once per test. Otherwise it will delete the
// directory again.
func accountingTestDir(testName string) string {
	path := siatest.TestDir("accounting", testName)
	if err := os.MkdirAll(path, persist.DefaultDiskPermissionsTest); err != nil {
		panic(err)
	}
	return path
}
