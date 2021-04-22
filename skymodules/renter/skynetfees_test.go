package renter

import (
	"encoding/json"
	"io/ioutil"
	"reflect"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"go.sia.tech/siad/types"
)

// TestSpendingHistory tests the spending history persistence.
func TestSpendingHistory(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	testDir := build.TempDir("renter", t.Name())
	fileName := "test"

	spending1 := types.NewCurrency64(1)
	txn1 := []types.Transaction{{ArbitraryData: [][]byte{fastrand.Bytes(100)}}}
	time1 := time.Time{}.Add(time.Nanosecond) // a long time ago

	// Create a new history.
	sh, err := NewSpendingHistory(testDir, fileName)
	if err != nil {
		t.Fatal(err)
	}
	// Spending should be zero.
	lastSpending, lastTime := sh.LastSpending()
	if !lastSpending.IsZero() {
		t.Fatal("spending should be zero.")
	}
	if !lastTime.IsZero() {
		t.Fatal("bh should be zero")
	}
	// Add some spending
	err = sh.AddSpending(spending1, txn1, time1)
	if err != nil {
		t.Fatal(err)
	}
	// Spending should match now.
	lastSpending, lastTime = sh.LastSpending()
	if !lastSpending.Equals(spending1) {
		t.Fatal("wrong spending", lastSpending, spending1)
	}
	if lastTime != time1 {
		t.Fatal("wrong time", lastTime, time1)
	}
	// Close history.
	if err := sh.Close(); err != nil {
		t.Fatal(err)
	}

	// Open the file directly and make sure the content matches the
	// expectations.
	aop, r, err := persist.NewAppendOnlyPersist(testDir, fileName, spendingHistoryMDHeader, persist.MetadataVersionv156)
	if err != nil {
		t.Fatal(err)
	}
	content, err := ioutil.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := aop.Close(); err != nil {
		t.Fatal(err)
	}
	var entry spendingEntry
	err = json.Unmarshal(content, &entry)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Time != time1 {
		t.Fatal("wrong time", entry.Time)
	}
	if !reflect.DeepEqual(entry.Txn, txn1) {
		t.Fatal("wrong txn", entry.Txn)
	}
	if !entry.Value.Equals(spending1) {
		t.Fatal("wrong spending", entry.Value)
	}

	// Reopen it.
	sh, err = NewSpendingHistory(testDir, fileName)
	if err != nil {
		t.Fatal(err)
	}
	// Spending should still match.
	lastSpending, lastTime = sh.LastSpending()
	if !lastSpending.Equals(spending1) {
		t.Fatal("wrong spending", lastSpending, spending1)
	}
	if lastTime != time1 {
		t.Fatal("wrong time", lastTime, time1)
	}
	// Close history.
	if err := sh.Close(); err != nil {
		t.Fatal(err)
	}
}
