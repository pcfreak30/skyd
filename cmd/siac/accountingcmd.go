package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
)

var (
	// csvHeaders is the headers for the csv file generated for the accounting
	// information
	csvHeaders = []string{"TimeStamp", "Siacoin Balance", "Siafund Balance", "Unspent Unallocated", "Withheld Funds"}
)

var (
	accountingCmd = &cobra.Command{
		Use:   "accounting",
		Short: "Generate a csv file of the accounting information for the node.",
		Long: `Generate a csv file of the accounting information for the node.
Information will be written to accounting.csv`,
		Run: wrap(accountingcmd),
	}
)

// accountingcmd is the handler for the command `siac accounting`.
// It generates a csv file of the accounting information for the node.
func accountingcmd() {
	// Create csv file
	f, err := os.Create("accounting.csv")
	if err != nil {
		die(err)
	}
	defer func() {
		err = f.Close()
		if err != nil {
			die(err)
		}
	}()

	// Grab the accounting information
	ais, err := httpClient.AccountingGet(0, time.Now().Unix())
	if err != nil {
		die("Unable to get accounting information: ", err)
	}

	// Write the information to the csv file.
	err = writeAccountingCSV(ais, f)
	if err != nil {
		die("Unable to write accounting information to csv file: ", err)
	}

	fmt.Println("CSV Successfully generated!")
}

// writeAccountingCSV is a helper to write the accounting information to a csv
// file.
func writeAccountingCSV(ais []modules.AccountingInfo, w io.Writer) error {
	// Create csv writer
	csvW := csv.NewWriter(w)

	// Convert to csv format
	//
	// Write Headers for Reference
	err := csvW.Write(csvHeaders)
	if err != nil {
		return errors.AddContext(err, "unable to write header")
	}

	// Write Wallet info first, then Renter info.
	var records [][]string
	for _, ai := range ais {
		timeStr := strconv.FormatInt(ai.Timestamp, 10)
		scStr := ai.Wallet.ConfirmedSiacoinBalance.String()
		sfStr := ai.Wallet.ConfirmedSiafundBalance.String()
		usStr := ai.Renter.UnspentUnallocated.String()
		whStr := ai.Renter.WithheldFunds.String()
		record := []string{timeStr, scStr, sfStr, usStr, whStr}
		records = append(records, record)
	}

	// Write output to file
	err = csvW.WriteAll(records)
	if err != nil {
		return errors.AddContext(err, "unable to write records")
	}

	// Flush the writer and check for an error
	csvW.Flush()
	if err := csvW.Error(); err != nil {
		die("Error when flushing csv writer: ", err)
	}
	return nil
}
