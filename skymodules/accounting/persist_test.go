package accounting

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"gitlab.com/SkynetHQ/skyd/siatest/dependencies"
	"gitlab.com/SkynetHQ/skyd/skymodules"
)

// TestPersist tests the persistence of the accounting package
func TestPersist(t *testing.T) {
	t.Parallel()

	// Short tests
	t.Run("Marshal", testMarshal)

	// Long tests
	if testing.Short() {
		t.SkipNow()
	}

	// Basic functionality test
	t.Run("Basic", testBasic)

	// Specific method tests
	t.Run("callThreadedPersistAccounting", testCallThreadedPersistAccounting)
	t.Run("managedUpdateAndPersistAccounting", testManagedUpdateAndPersistAccounting)
}

// testBasic tests the basic functionality of the Accounting module
func testBasic(t *testing.T) {
	// Create new accounting
	testDir := accountingTestDir(t.Name())
	fm, h, m, r, w, _ := testingParams()
	a, err := NewCustomAccounting(fm, h, m, r, w, testDir, &dependencies.AccountingDisablePersistLoop{})
	if err != nil {
		t.Fatal(err)
	}

	// Check initial persistence
	a.mu.Lock()
	lenHistory := len(a.history)
	a.mu.Unlock()
	if lenHistory != 0 {
		t.Error("history should be empty")
	}

	// Update, close, open, and verify several times
	var expectedHistoryLen int
	for i := 0; i < 5; i++ {
		// Update the persistence
		a.managedUpdateAndPersistAccounting()

		// Grab the current persistence
		a.mu.Lock()
		initHistory := a.history
		a.mu.Unlock()
		expectedHistoryLen++

		// Close accounting
		err = a.Close()
		if err != nil {
			t.Fatal(err)
		}

		// Load Accounting
		a, err = NewCustomAccounting(fm, h, m, r, w, testDir, &dependencies.AccountingDisablePersistLoop{})
		if err != nil {
			t.Fatal(err)
		}

		// Check persistence
		a.mu.Lock()
		history := a.history
		a.mu.Unlock()
		if !reflect.DeepEqual(initHistory, history) {
			t.Log("Expected:", initHistory)
			t.Log("Actual:", history)
			t.Error("history mismatch")
		}
		if len(history) != expectedHistoryLen {
			t.Error("history length unexpected", len(history), expectedHistoryLen)
		}
	}
	// Close accounting
	err = a.Close()
	if err != nil {
		t.Fatal(err)
	}
}

// testCallThreadedPersistAccounting probes the callThreadedPersistAccounting method
func testCallThreadedPersistAccounting(t *testing.T) {
	// Initialize Accounting
	a, err := newTestAccounting(accountingTestDir(t.Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err = a.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	// Check that the background thread timer is working and the persistence is
	// updating
	for i := 0; i < 2; i++ {
		// Grab the current persistence
		a.mu.Lock()
		initLen := len(a.history)
		a.mu.Unlock()

		// Sleep
		time.Sleep(persistInterval * 2)

		// Validate the persistence was updated
		a.mu.Lock()
		lenHistory := len(a.history)
		a.mu.Unlock()
		if initLen >= lenHistory {
			t.Error("History not updated")
		}
	}
}

// testManagedUpdateAndPersistAccounting probes the
// managedUpdateAndPersistAccounting method
func testManagedUpdateAndPersistAccounting(t *testing.T) {
	// Initialize Accounting
	testDir := accountingTestDir(t.Name())
	fm, h, m, r, w, _ := testingParams()
	a, err := NewCustomAccounting(fm, h, m, r, w, testDir, &dependencies.AccountingDisablePersistLoop{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err = a.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	// Grab the persistence beforehand
	a.mu.Lock()
	initLen := len(a.history)
	a.mu.Unlock()

	// Call managedUpdateAndPersistAccounting
	a.managedUpdateAndPersistAccounting()

	// Validate expectations
	a.mu.Lock()
	lenHistory := len(a.history)
	a.mu.Unlock()
	if initLen == lenHistory {
		t.Error("History should have been updated")
	}
}

// testMarshal probes the marshalling and unmarshalling of the persistence
func testMarshal(t *testing.T) {
	// Create AccountingInfo
	ai := skymodules.AccountingInfo{
		Renter: skymodules.RenterAccounting{
			WithheldFunds:      randomCurrency(),
			UnspentUnallocated: randomCurrency(),
		},
		Wallet: skymodules.WalletAccounting{
			ConfirmedSiacoinBalance: randomCurrency(),
			ConfirmedSiafundBalance: randomCurrency(),
		},
		Timestamp: time.Now().Unix(),
	}

	// Marshal persistence
	data, err := marshalPersistence(ai)
	if err != nil {
		t.Fatal(err)
	}

	// Unmarshal persistence
	unmarshalledInfo, err := unmarshalPersistence(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(unmarshalledInfo) != 1 {
		t.Fatal("unexpected")
	}

	// Compare
	if !reflect.DeepEqual(ai, unmarshalledInfo[0]) {
		t.Log("ai", ai)
		t.Log("unmarshalledInfo", unmarshalledInfo[0])
		t.Fatal("info mismatch")
	}

	// Append several persist elements together
	var datas []byte
	for i := 0; i < 5; i++ {
		datas = append(datas, data...)
	}

	// Unmarshal
	unmarshalledInfo, err = unmarshalPersistence(bytes.NewReader(datas))
	if err != nil {
		t.Fatal(err)
	}
	if len(unmarshalledInfo) != 5 {
		t.Fatal("unexpected")
	}
	// Compare each element to the original persistence
	for _, uai := range unmarshalledInfo {
		if !reflect.DeepEqual(ai, uai) {
			t.Log("ai", ai)
			t.Log("uai", uai)
			t.Fatal("accounting info mismatch")
		}
	}
}
