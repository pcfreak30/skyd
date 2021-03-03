package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
)

var (
	// csvHeaders is the headers for the csv file generated for the accounting
	// information
	csvHeaders = []string{"Siacoin Balance", "Siafund Balance", "Unspent Unallocated", "Withheld Funds"}
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

	// Create csv writer
	w := csv.NewWriter(f)
	defer w.Flush()

	// Grab the accounting information
	ais, err := httpClient.AccountingGet(0, time.Now().Unix())
	if err != nil {
		die("Unable to get accounting information: ", err)
	}

	// Write the information to the csv file.
	err = writeAccountingCSV(ais[0], w)
	if err != nil {
		die("Unable to write accounting information to csv file: ", err)
	}

	fmt.Println("CSV Successfully generated!")
}

// writeAccountingCSV is a helper to write the accounting information to a csv
// file.
func writeAccountingCSV(ai modules.AccountingInfo, w *csv.Writer) error {
	// Convert to csv format
	//
	// Write Headers for Reference
	err := w.Write(csvHeaders)
	if err != nil {
		return errors.AddContext(err, "unable to write header")
	}

	// Write Wallet info first, then Renter info.
	scStr := ai.Wallet.ConfirmedSiacoinBalance.String()
	sfStr := ai.Wallet.ConfirmedSiafundBalance.String()
	usStr := ai.Renter.UnspentUnallocated.String()
	whStr := ai.Renter.WithheldFunds.String()
	csvOutput := []string{scStr, sfStr, usStr, whStr}

	// Write output to file
	err = w.Write(csvOutput)
	if err != nil {
		return errors.AddContext(err, "unable to write data")
	}
	return nil
}
