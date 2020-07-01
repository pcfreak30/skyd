package host

import (
	"testing"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
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

	// Test the basic flow.
	t.Run("Basic", func(t *testing.T) {
		testProgramRefundBasic(t, rhp)
	})
}

// testProgramRefundBasic tests the basic happy-flow functionality of the
// ProgramRefund RPC.
func testProgramRefundBasic(t *testing.T, rhp *renterHostPair) {
	// Fund the account.
	his := rhp.staticHT.host.managedInternalSettings()
	_, err := rhp.managedFundEphemeralAccount(his.MaxEphemeralAccountBalance, true)
	if err != nil {
		t.Fatal(err)
	}

	// Create a dummy has sector program.
	pt := rhp.managedPriceTable()
	pb := modules.NewProgramBuilder(pt, 0)
	pb.AddHasSectorInstruction(crypto.Hash{})
	program, data := pb.Program()
	programCost, _, _ := pb.Cost(true)

	// Calculate bandwidth cost
	downloadCost := pt.DownloadBandwidthCost.Mul64(2920)
	uploadCost := pt.UploadBandwidthCost.Mul64(1460)
	bandwidthCost := downloadCost.Add(uploadCost)

	// Prepare the request.
	epr := modules.RPCExecuteProgramRequest{
		FileContractID:    types.FileContractID{},
		Program:           program,
		ProgramDataLength: uint64(len(data)),
	}

	// Define a random amount of money that we expect to be refunded
	expected := types.NewCurrency64(fastrand.Uint64n(1000))

	// Execute program.
	cost := programCost.Add(bandwidthCost)
	cost = cost.Add(expected)
	_, _, token, err := rhp.managedExecuteProgram(epr, data, cost, true, true)
	if err != nil {
		t.Fatal(err)
	}

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
