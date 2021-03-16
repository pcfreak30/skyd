package main

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/skynetlabs/skyd/skymodules"
)

// randomAcountingInfo is a helper that generates random accounting information
func randomAcountingInfo(num int) []skymodules.AccountingInfo {
	var ais []skymodules.AccountingInfo
	for i := 0; i < num; i++ {
		ais = append(ais, skymodules.AccountingInfo{
			Wallet: skymodules.WalletAccounting{
				ConfirmedSiacoinBalance: types.NewCurrency64(fastrand.Uint64n(1000)),
				ConfirmedSiafundBalance: types.NewCurrency64(fastrand.Uint64n(1000)),
			},
			Renter: skymodules.RenterAccounting{
				UnspentUnallocated: types.NewCurrency64(fastrand.Uint64n(1000)),
				WithheldFunds:      types.NewCurrency64(fastrand.Uint64n(1000)),
			},
			Timestamp: time.Now().Unix(),
		})
	}
	return ais
}

// TestWriteAccountingCSV tests the writeAccountingCSV function
func TestWriteAccountingCSV(t *testing.T) {
	t.Parallel()

	// Create buffer
	var buf bytes.Buffer

	// Create accounting information
	ais := randomAcountingInfo(5)

	// Write the information to the csv file.
	err := writeAccountingCSV(ais, &buf)
	if err != nil {
		t.Fatal(err)
	}

	// Read the written data from the file.
	bytes := buf.Bytes()

	// Generated expected
	headerStr := strings.Join(csvHeaders, ",")
	expected := fmt.Sprintf("%v\n", headerStr)
	for _, ai := range ais {
		timeStr := strconv.FormatInt(ai.Timestamp, 10)
		scStr := ai.Wallet.ConfirmedSiacoinBalance.String()
		sfStr := ai.Wallet.ConfirmedSiafundBalance.String()
		usStr := ai.Renter.UnspentUnallocated.String()
		whStr := ai.Renter.WithheldFunds.String()
		data := []string{timeStr, scStr, sfStr, usStr, whStr}
		dataStr := strings.Join(data, ",")
		expected = fmt.Sprintf("%v%v\n", expected, dataStr)
	}

	// Check bytes vs expected
	if expected != string(bytes) {
		t.Log("actual", string(bytes))
		t.Log("expected", expected)
		t.Fatal("unexpected")
	}
}
