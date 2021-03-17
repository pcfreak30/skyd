package renter

import (
	"encoding/json"
	"io/ioutil"
	"reflect"
	"testing"

	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/skynetlabs/skyd/build"
)

// TestSpendingHistory tests the spending history persistence.
func TestSpendingHistory(t *testing.T) {
	testDir := build.TempDir("renter", t.Name())
	fileName := "test"

	spending1 := types.NewCurrency64(1)
	txn1 := []types.Transaction{{ArbitraryData: [][]byte{fastrand.Bytes(100)}}}
	bh1 := types.BlockHeight(1)

	// Create a new history.
	sh, err := NewSpendingHistory(testDir, fileName)
	if err != nil {
		t.Fatal(err)
	}
	// Spending should be zero.
	lastSpending, lastBH := sh.LastSpending()
	if !lastSpending.IsZero() {
		t.Fatal("spending should be zero.")
	}
	if lastBH != 0 {
		t.Fatal("bh should be zero")
	}
	// Add some spending
	err = sh.AddSpending(spending1, txn1, bh1)
	if err != nil {
		t.Fatal(err)
	}
	// Spending should match now.
	lastSpending, lastBH = sh.LastSpending()
	if !lastSpending.Equals(spending1) {
		t.Fatal("wrong spending", lastSpending, spending1)
	}
	if lastBH != bh1 {
		t.Fatal("wrong bh", lastBH, bh1)
	}
	// Close history.
	if err := sh.Close(); err != nil {
		t.Fatal(err)
	}

	// Open the file directly and make sure the content matches the
	// expectations.
	aop, r, err := persist.NewAppendOnlyPersist(testDir, fileName, metadataHeader, persist.MetadataVersionv156)
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
	if entry.Height != bh1 {
		t.Fatal("wrong height", entry.Height)
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
	lastSpending, lastBH = sh.LastSpending()
	if !lastSpending.Equals(spending1) {
		t.Fatal("wrong spending", lastSpending, spending1)
	}
	if lastBH != bh1 {
		t.Fatal("wrong bh", lastBH, bh1)
	}
	// Close history.
	if err := sh.Close(); err != nil {
		t.Fatal(err)
	}
}
