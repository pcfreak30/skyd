package modules

import (
	"encoding/binary"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

type (
	// Instruction specifies a generic instruction used as an input to
	// `mdm.ExecuteProgram`.
	Instruction struct {
		Specifier InstructionSpecifier
		Args      []byte
	}
	// InstructionSpecifier specifies the type of the instruction.
	InstructionSpecifier types.Specifier

	// MDMInstructionResponse is the response object containing the result of an
	// executed MDM instruction.
	MDMInstructionResponse struct {
		Error         string
		NewSize       uint64
		NewMerkleRoot crypto.Hash
		Output        []byte
		Proof         []crypto.Hash
	}
)

// MDM instruction cost component specifiers
var (
	MDMComponentCompute    = types.NewSpecifier("Compute")
	MDMComponentMemory     = types.NewSpecifier("Memory")
	MDMOperationDiskAccess = types.NewSpecifier("DiskAccess")
	MDMOperationDiskRead   = types.NewSpecifier("DiskRead")
	MDMOperationDiskWrite  = types.NewSpecifier("DiskWrite")
)

const (
	// MDMProgramInitTime is the time it takes to execute a program. This is a
	// hardcoded value which is meant to be replaced in the future. TODO: The
	// time is hardcoded to 10 for now until we add time management in the
	// future.
	MDMProgramInitTime = 10

	// MDMTimeAppend is the time for executing an 'Append' instruction.
	MDMTimeAppend = 10000

	// MDMTimeCommit is the time used for executing managedFinalize.
	MDMTimeCommit = 50e3

	// MDMTimeHasSector is the time for executing a 'HasSector' instruction.
	MDMTimeHasSector = 1

	// MDMTimeReadSector is the time for executing a 'ReadSector' instruction.
	MDMTimeReadSector = 1000

	// MDMTimeWriteSector is the time for executing a 'WriteSector' instruction.
	MDMTimeWriteSector = 10000

	// RPCIHasSectorLen is the expected length of the 'Args' of a HasSector
	// instruction.
	RPCIHasSectorLen = 8

	// RPCIReadSectorLen is the expected length of the 'Args' of a ReadSector
	// instruction.
	RPCIReadSectorLen = 25

	// RPCIAppendLen is the expected length of the 'Args' of an Append
	// instructon.
	RPCIAppendLen = 9
)

var (
	// SpecifierAppend is the specifier for the Append RPC.
	SpecifierAppend = InstructionSpecifier{'A', 'p', 'p', 'e', 'n', 'd'}

	// SpecifierHasSector is the specifier for the HasSector RPC.
	SpecifierHasSector = InstructionSpecifier{'H', 'a', 's', 'S', 'e', 'c', 't', 'o', 'r'}

	// SpecifierReadSector is the specifier for the ReadSector RPC.
	SpecifierReadSector = InstructionSpecifier{'R', 'e', 'a', 'd', 'S', 'e', 'c', 't', 'o', 'r'}
)

// RPCIReadSector is a convenience method to create an Instruction of type
// 'ReadSector'.
func RPCIReadSector(rootOff, offsetOff, lengthOff uint64, merkleProof bool) Instruction {
	args := make([]byte, RPCIReadSectorLen)
	binary.LittleEndian.PutUint64(args[:8], rootOff)
	binary.LittleEndian.PutUint64(args[8:16], offsetOff)
	binary.LittleEndian.PutUint64(args[16:24], lengthOff)
	if merkleProof {
		args[24] = 1
	}
	return Instruction{
		Args:      args,
		Specifier: SpecifierReadSector,
	}
}

// ErrMDMInsufficientBudget is the error returned if the remaining budget of a
// program is not sufficient to execute the next instruction.
var ErrMDMInsufficientBudget = errors.New("remaining program budget is insufficient")

// CalculateProgramCost returns the total cost of all separate instructions
// combined.
// TODO (pj) I commented this out during merge
// func CalculateProgramCost(pt RPCPriceTable, instructions []Instruction, programLen uint64) types.Currency {
// 	total := types.ZeroCurrency
// 	for _, i := range instructions {
// 		switch i.Specifier {
// 		case SpecifierReadSector:
// 			total.Add(ReadCost(pt, programLen))
// 		default:
// 		}
// 	}
// 	return total
// }

// MDMAppendCost is the cost of executing an 'Append' instruction.
func MDMAppendCost(pt RPCPriceTable) (types.Currency, types.Currency) {
	writeCost := pt.WriteLengthCost.Mul64(SectorSize).Add(pt.WriteBaseCost)
	storeCost := pt.WriteStoreCost.Mul64(SectorSize) // potential refund
	return writeCost.Add(storeCost), storeCost
}

// MDMInitCost is the cost of instantiatine the MDM. It is defined as:
// 'InitBaseCost' + 'MemoryTimeCost' * 'programLen' * Time
func MDMInitCost(pt RPCPriceTable, programLen uint64) types.Currency {
	return pt.MemoryTimeCost.Mul64(programLen).Mul64(MDMProgramInitTime).Add(pt.InitBaseCost)
}

// MDMHasSectorCost is the cost of executing a 'HasSector' instruction.
func MDMHasSectorCost(pt RPCPriceTable) (types.Currency, types.Currency) {
	cost := pt.MemoryTimeCost.Mul64(1 << 20).Mul64(MDMTimeHasSector)
	refund := types.ZeroCurrency // no refund
	return cost, refund
}

// MDMReadCost is the cost of executing a 'Read' instruction. It is defined as:
// 'readBaseCost' + 'readLengthCost' * `readLength`
func MDMReadCost(pt RPCPriceTable, readLength uint64) (types.Currency, types.Currency) {
	cost := pt.ReadLengthCost.Mul64(readLength).Add(pt.ReadBaseCost)
	refund := types.ZeroCurrency // no refund
	return cost, refund
}

// MDMWriteCost is the cost of executing a 'Write' instruction of a certain
// length.
func MDMWriteCost(pt RPCPriceTable, writeLength uint64) (types.Currency, types.Currency) {
	writeCost := pt.WriteLengthCost.Mul64(writeLength).Add(pt.WriteBaseCost)
	storeCost := types.ZeroCurrency // no refund since we overwrite existing storage
	return writeCost, storeCost
}

// MDMCopyCost is the cost of executing a 'Copy' instruction.
func MDMCopyCost(pt RPCPriceTable, contractSize uint64) types.Currency {
	return types.SiacoinPrecision // TODO: figure out good cost
}

// MDMSwapCost is the cost of executing a 'Swap' instruction.
func MDMSwapCost(pt RPCPriceTable, contractSize uint64) types.Currency {
	return types.SiacoinPrecision // TODO: figure out good cost
}

// MDMTruncateCost is the cost of executing a 'Truncate' instruction.
func MDMTruncateCost(pt RPCPriceTable, contractSize uint64) types.Currency {
	return types.SiacoinPrecision // TODO: figure out good cost
}
