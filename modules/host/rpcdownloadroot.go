package host

import (
	"context"
	"fmt"
	"net"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/host/mdm"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

func readInstructions(stream net.Conn) (instructions []modules.Instruction, err error) {
	err = encoding.ReadObject(stream, &instructions, 4096)
	return
}

func readDataLen(stream net.Conn) (dataLen uint64, err error) {
	err = encoding.ReadObject(stream, &dataLen, 4096)
	return
}

func calculateProgramCost(instructions []modules.Instruction, dataLen uint64) (cost mdm.Cost, err error) {
	cost = cost.Add(mdm.InitCost(dataLen))
	for _, instruction := range instructions {
		switch instruction.Specifier {
		case modules.SpecifierReadSector:
			cost = cost.Add(mdm.ReadSectorCost())
		default:
			return mdm.Cost{}, fmt.Errorf("calculateProgramCost: unknown instruction %v", instruction.Specifier)
		}
	}
	return
}

func convertCostToPrice(cost mdm.Cost, pt modules.RPCPriceTable) (price types.Currency) {
	price = price.Add(types.NewCurrency64(cost.Compute).Mul(pt.Costs[modules.ComponentCompute]))
	price = price.Add(types.NewCurrency64(cost.DiskAccesses).Mul(pt.Costs[modules.OperationDiskAccess]))
	price = price.Add(types.NewCurrency64(cost.DiskRead).Mul(pt.Costs[modules.OperationDiskRead]))
	price = price.Add(types.NewCurrency64(cost.DiskWrite).Mul(pt.Costs[modules.OperationDiskWrite]))
	price = price.Add(types.NewCurrency64(cost.Memory).Mul(pt.Costs[modules.ComponentMemory]))
	return
}

// managedRPCExecuteProgram will read a program from the stream and execute it
// on the MDM.
func (h *Host) managedRPCExecuteProgram(stream net.Conn, pt modules.RPCPriceTable, fcid types.FileContractID) error {
	// TODO: process payment for this RPC call (introduced in other MR)
	pp := h.NewPaymentProcessor()
	amountPaid, err := pp.ProcessPaymentForRPC(stream)
	if err != nil {
		return errors.AddContext(err, "Failed to process payment")
	}

	instructions, err := readInstructions(stream)
	if err != nil {
		return errors.AddContext(err, "Failed to read instructions")
	}

	dataLen, err := readDataLen(stream)
	if err != nil {
		return errors.AddContext(err, "Failed to read dataLen")
	}

	// Figure out the mdm.Cost of the program.
	cost, err := calculateProgramCost(instructions, dataLen)
	if err != nil {
		return errors.AddContext(err, "Failed to calculate program cost")
	}
	price := convertCostToPrice(cost, pt)

	if amountPaid.Cmp(price) < 0 {
		return fmt.Errorf("The renter did not supply sufficient payment to cover the cost of the ExecuteProgramRPC. Expected: %v Actual: %v", price.HumanString(), amountPaid.HumanString())
	}

	// Get storage obligation
	so, err := h.managedGetStorageObligation(fcid)
	if err != nil {
		return errors.AddContext(err, "Failed to get storage obligation")
	}
	// TODO: figure out how to get initial contract size without locking.
	so.recentRevision().NewFileMerkleRoot

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, outputs, err := h.staticMDM.ExecuteProgram(ctx, instructions, cost, so, crypto.Hash{}, dataLen, stream)
	if err != nil {
		return errors.AddContext(err, "failed to execute program")
	}
	for output := range outputs {
		// TODO: handle output
		if output.Error != nil {
			panic(fmt.Sprint("instruction encountered error", output.Error))
		}
	}
	return nil
}
