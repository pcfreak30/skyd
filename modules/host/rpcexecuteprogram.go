package host

import (
	"context"
	"fmt"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/siamux"
)

// managedRPCExecuteProgram will read a program from the stream and execute it
// on the MDM.
func (h *Host) managedRPCExecuteProgram(stream siamux.Stream, pt *modules.RPCPriceTable, fcid types.FileContractID) error {
	// process payment
	pp := h.NewPaymentProcessor()
	amountPaid, err := pp.ProcessPaymentForRPC(stream)
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

	// calculate the cost of the program.
	cost, err := modules.CalculateProgramCost(instructions, dataLen)
	if err != nil {
		return errors.AddContext(err, "Failed to calculate program cost")
	}
	price := modules.ConvertCostToPrice(cost, pt)

	// verify if payment was sufficient
	if amountPaid.Cmp(price) < 0 {
		return fmt.Errorf("The renter did not supply sufficient payment to cover the cost of the ExecuteProgramRPC. Expected: %v Actual: %v", price.HumanString(), amountPaid.HumanString())
	}

	// get storage obligation
	so, err := h.managedGetStorageObligation(fcid)
	if err != nil {
		return errors.AddContext(err, "Failed to get storage obligation")
	}

	// TODO: figure out how to get initial contract size without locking.
	mso := newMDMStorageObligation(so, h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, outputs, err := h.staticMDM.ExecuteProgram(ctx, instructions, cost, mso, dataLen, stream)
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
		Error:         "TODO",
	})
	if err != nil {
		return errors.AddContext(err, "Failed to send output")
	}

	return nil
}

// TODO: move this:
// MDMStorageObligation wraps a host and storage obligation to satisfy the
// mdm.StorageObligation interface.
type MDMStorageObligation struct {
	so storageObligation
	h  *Host
}

func newMDMStorageObligation(so storageObligation, h *Host) *MDMStorageObligation {
	return &MDMStorageObligation{so: so, h: h}
}

// ContractSize satisfies the mdm.StorageObligation interface.
func (mso *MDMStorageObligation) ContractSize() uint64 {
	if !mso.h.managedIsLockedStorageObligation(mso.so.id()) {
		panic("TODO")
	}
	return mso.so.recentRevision().NewFileSize
}

// Locked satisfies the mdm.StorageObligation interface.
func (mso *MDMStorageObligation) Locked() bool {
	return mso.h.managedIsLockedStorageObligation(mso.so.id())
}

// MerkleRoot satisfies the mdm.StorageObligation interface.
func (mso *MDMStorageObligation) MerkleRoot() crypto.Hash {
	if mso.h.managedIsLockedStorageObligation(mso.so.id()) {
		return mso.so.recentRevision().NewFileMerkleRoot
	}
	return crypto.Hash{}
}

// Update satisfies the mdm.StorageObligation interface.
func (mso *MDMStorageObligation) Update(sectorsRemoved, sectorsGained []crypto.Hash, gainedSectorData [][]byte) error {
	return mso.h.modifyStorageObligation(mso.so, sectorsRemoved, sectorsGained, gainedSectorData)
}
