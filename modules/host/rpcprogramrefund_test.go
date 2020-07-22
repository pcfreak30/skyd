package host

import (
	"testing"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
)

// TestProgramRefund verifies the ProgramRefund RPC.
func TestProgramRefund(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// create a blank host tester
	rhp, err := newRenterHostPair(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := rhp.Close()
		if err != nil {
			t.Error(err)
		}
	}()

	// Fund the account.
	his := rhp.staticHT.host.managedInternalSettings()
	_, err = rhp.managedFundEphemeralAccount(his.MaxEphemeralAccountBalance, true)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("Basic", func(t *testing.T) { testBasic(t, rhp) })
	t.Run("ExpiredToken", func(t *testing.T) { testExpiredToken(t, rhp) })
	t.Run("UnknownToken", func(t *testing.T) { testUnknownToken(t, rhp) })
}

// testBasic tests the basic happy-flow functionality of the ProgramRefund RPC.
func testBasic(t *testing.T, rhp *renterHostPair) {
	// Prepare the request.
	program, data, cost := dummyHasSectorProgram(rhp.pt)
	epr := modules.RPCExecuteProgramRequest{
		FileContractID:    types.FileContractID{},
		Program:           program,
		ProgramDataLength: uint64(len(data)),
	}

	// Define a random amount of money that we expect to be refunded
	expected := types.NewCurrency64(fastrand.Uint64n(1000))

	// Execute program.
	cost = cost.Add(expected)
	_, _, token, err := rhp.managedExecuteProgram(epr, data, cost, true, true)
	if err != nil {
		t.Fatal(err)
	}

	// Execute the refund RPC
	refund, found, err := rhp.managedProgramRefund(token)
	if err != nil {
		t.Fatal(err)
	}

	if !found {
		t.Fatal("Expected token to be found")
	}
	if !refund.Equals(expected) {
		t.Log(refund)
		t.Log(expected)
		t.Fatal("Expected refund to equal the amount we overpaid")
	}
}

// testExpiredToken tests the program info eventually expires on the host.
func testExpiredToken(t *testing.T, rhp *renterHostPair) {
	// Prepare the request.
	program, data, cost := dummyHasSectorProgram(rhp.pt)
	epr := modules.RPCExecuteProgramRequest{
		FileContractID:    types.FileContractID{},
		Program:           program,
		ProgramDataLength: uint64(len(data)),
	}

	// Execute program.
	_, _, token, err := rhp.managedExecuteProgram(epr, data, cost, true, true)
	if err != nil {
		t.Fatal(err)
	}

	// Execute the refund RPC
	_, found, err := rhp.managedProgramRefund(token)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("Expected token to be found")
	}

	// Retry the RPC and expect the program info to expire after a while
	err = build.Retry(10, pruneProgramsListFrequency, func() error {
		_, found, err := rhp.managedProgramRefund(token)
		if err != nil {
			return err
		}
		if found {
			return errors.New("Expected program info to be expired")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// testUnknownToken tests the behaviour when a program refund is queried for an
// unknown program token
func testUnknownToken(t *testing.T, rhp *renterHostPair) {
	// Execute the refund RPC
	refund, found, err := rhp.managedProgramRefund(modules.NewMDMProgramToken())
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("Expected token to not be found")
	}
	if !refund.IsZero() {
		t.Fatal("Expected refund to equal the zero currency")
	}
}

// dummyHasSectorProgram is a helper function that returns a dummy program
func dummyHasSectorProgram(pt *modules.RPCPriceTable) (modules.Program, modules.ProgramData, types.Currency) {
	// Create a dummy has sector program.
	pb := modules.NewProgramBuilder(pt, 0)
	pb.AddHasSectorInstruction(crypto.Hash{})
	program, data := pb.Program()
	programCost, _, _ := pb.Cost(true)

	// Calculate bandwidth cost
	downloadCost := pt.DownloadBandwidthCost.Mul64(1460)
	uploadCost := pt.UploadBandwidthCost.Mul64(1460)
	bandwidthCost := downloadCost.Add(uploadCost)

	return program, data, programCost.Add(bandwidthCost)
}
