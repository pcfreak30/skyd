package host

import (
	"context"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/siamux"
)

// managedRPCExecuteProgram will read a program from the stream and execute it
// on the MDM.
func (h *Host) managedRPCExecuteProgram(stream siamux.Stream, pt *modules.RPCPriceTable) error {
	// read request
	var epr modules.RPCExecuteProgramRequest
	err := modules.RPCRead(stream, &epr)
	if err != nil {
		return errors.AddContext(err, "Failed to read RPCExecuteProgramRequest")
	}

	// process payment
	pp := h.NewPaymentProcessor()
	budget, err := pp.ProcessPaymentForRPC(stream)
	if err != nil {
		return errors.AddContext(err, "Failed to process payment")
	}

	// read instructions
	var instructions []modules.Instruction
	err = modules.RPCRead(stream, &instructions)
	if err != nil {
		return errors.AddContext(err, "Failed to read instructions")
	}

	// read length of program data
	var dataLen uint64
	err = modules.RPCRead(stream, &dataLen)
	if err != nil {
		return errors.AddContext(err, "Failed to read dataLen")
	}

	// // calculate the cost of the program.
	// cost, err := modules.CalculateProgramCost(instructions, dataLen)
	// if err != nil {
	// 	return errors.AddContext(err, "Failed to calculate program cost")
	// }
	// price := modules.ConvertCostToPrice(cost, pt)

	// // verify if payment was sufficient
	// if amountPaid.Cmp(price) < 0 {
	// 	return fmt.Errorf("The renter did not supply sufficient payment to cover the cost of the ExecuteProgramRPC. Expected: %v Actual: %v", price.HumanString(), amountPaid.HumanString())
	// }

	// get storage obligation
	so, err := h.managedGetStorageObligation(epr.FileContractID)
	if err != nil {
		return errors.AddContext(err, "Failed to get storage obligation")
	}

	// TODO: figure out how to get initial contract size without locking.
	mso := newMDMStorageObligation(so, h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, outputs, err := h.staticMDM.ExecuteProgram(ctx, *pt, instructions, budget, mso, dataLen, stream)
	if err != nil {
		return errors.AddContext(err, "failed to execute program")
	}

	// TODO handle multiple
	output := <-outputs
	err = modules.RPCWrite(stream, modules.MDMInstructionResponse{
		Output:        output.Output,
		NewMerkleRoot: output.NewMerkleRoot,
		NewSize:       output.NewSize,
		Proof:         output.Proof,
		Error:         output.Error.Error(),
	})
	if err != nil {
		return errors.AddContext(err, "Failed to send output")
	}

	return err
}
