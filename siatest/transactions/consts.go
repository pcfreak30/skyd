package transactions

import (
	"os"

	"gitlab.com/NebulousLabs/Sia/siatest"
)

// transactionsTestDir creates a temporary testing directory for a transactions
// test. This function will wipe out any previous directory of that name upon
// being called and create a clean slate.
func transactionsTestDir(testName string) string {
	path := siatest.TestDir("transactions", testName)
	if err := os.MkdirAll(path, 0777); err != nil {
		panic(err)
	}
	return path
}
