package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
)

// TestWriteAccountingCSV tests the writeAccountingCSV function
func TestWriteAccountingCSV(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create csv file
	testDir := siacTestDir(t.Name())
	f, err := os.Create(filepath.Join(testDir, "accounting.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err = f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	// Create csv writer
	w := csv.NewWriter(f)

	// Grab the accounting information
	ai := modules.AccountingInfo{
		Wallet: modules.WalletAccounting{
			ConfirmedSiacoinBalance: types.NewCurrency64(fastrand.Uint64n(1000)),
			ConfirmedSiafundBalance: types.NewCurrency64(fastrand.Uint64n(1000)),
		},
		Renter: modules.RenterAccounting{
			UnspentUnallocated: types.NewCurrency64(fastrand.Uint64n(1000)),
			WithheldFunds:      types.NewCurrency64(fastrand.Uint64n(1000)),
		},
	}

	// Write the information to the csv file.
	err = writeAccountingCSV(ai, w)
	if err != nil {
		t.Fatal(err)
	}

	// Flush the writer
	w.Flush()

	// Read the written data from the file.
	_, err = f.Seek(0, io.SeekStart)
	bytes, err := ioutil.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}

	// Generated expected
	headerStr := strings.Join(csvHeaders, ",")
	timeStr := strconv.FormatInt(ai.Timestamp, 10)
	scStr := ai.Wallet.ConfirmedSiacoinBalance.String()
	sfStr := ai.Wallet.ConfirmedSiafundBalance.String()
	usStr := ai.Renter.UnspentUnallocated.String()
	whStr := ai.Renter.WithheldFunds.String()
	data := []string{timeStr, scStr, sfStr, usStr, whStr}
	dataStr := strings.Join(data, ",")
	expected := fmt.Sprintf("%v\n%v\n", headerStr, dataStr)
	if expected != string(bytes) {
		t.Log("actual", string(bytes))
		t.Log("expected", expected)
		t.Fatal("unexpected")
	}
}
