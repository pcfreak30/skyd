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
	// MDMProgramTime is the time it takes to execute a program. This is a
	// hardcoded value which is meant to be replaced in the future. TODO: The
	// time is hardcoded to 10 for now until we add time management in the
	// future.
	MDMProgramTime = 10

	// RPCIReadSectorLen is the expected length of the 'Args' of a ReadSector
	// Instruction.
	RPCIReadSectorLen = 25
	// RPCIAppendLen is the expected length of the 'Args' of an Append
	// instructon.
	RPCIAppendLen = 9
)

var (
	// SpecifierAppend is the specifier for the Append RPC.
	SpecifierAppend = InstructionSpecifier{'A', 'p', 'p', 'e', 'n', 'd'}
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

// The following errors are returned by `cost.Sub` in case of an underflow.
// ErrInsufficientBudget is always returned in combination with the error of the
// corresponding resource.
var (
	ErrInsufficientBudget             = errors.New("program has insufficient budget to execute")
	ErrInsufficientComputeBudget      = errors.New("insufficient 'compute' budget")
	ErrInsufficientDiskAccessesBudget = errors.New("insufficient 'diskaccess' budget")
	ErrInsufficientDiskReadBudget     = errors.New("insufficient 'diskread' budget")
	ErrInsufficientDiskWriteBudget    = errors.New("insufficient 'diskwrite' budget")
	ErrInsufficientMemoryBudget       = errors.New("insufficient 'memory' budget")
)

// CalculateProgramCost returns the total cost of all separate instructions
// combined.
func CalculateProgramCost(pt RPCPriceTable, instructions []Instruction, programLen uint64) types.Currency {
	total := types.ZeroCurrency
	for _, i := range instructions {
		switch i.Specifier {
		case SpecifierReadSector:
			total.Add(ReadCost(pt, programLen))
		default:
		}
	}
	return total
}

// InitCost is the cost of instantiatine the MDM. It is defined as:
// 'InitBaseCost' + 'MemoryTimeCost' * 'programLen' * Time
func InitCost(pt RPCPriceTable, programLen uint64) types.Currency {
	return pt.MemoryTimeCost.Mul64(programLen).Mul64(MDMProgramTime).Add(pt.InitBaseCost)
}

// ReadCost is the cost of executing a 'Read' instruction. It is defined as:
// 'readBaseCost' + 'readLengthCost' * `readLength`
func ReadCost(pt RPCPriceTable, readLength uint64) types.Currency {
	return pt.ReadLengthCost.Mul64(readLength).Add(pt.ReadBaseCost)
}

// WriteCost is the cost of executing a 'Write' instruction of a certain length.
// It's also used to compute the cost of a `WriteSector` and `Append`
// instruction.
func WriteCost(pt RPCPriceTable, writeLength uint64) types.Currency {
	return pt.WriteLengthCost.Mul64(writeLength).Add(pt.WriteBaseCost)
}

// CopyCost is the cost of executing a 'Copy' instruction.
func CopyCost(pt RPCPriceTable, contractSize uint64) types.Currency {
	return types.SiacoinPrecision // TODO: figure out good cost
}

// SwapCost is the cost of executing a 'Swap' instruction.
func SwapCost(pt RPCPriceTable, contractSize uint64) types.Currency {
	return types.SiacoinPrecision // TODO: figure out good cost
}

// TruncateCost is the cost of executing a 'Truncate' instruction.
func TruncateCost(pt RPCPriceTable, contractSize uint64) types.Currency {
	return types.SiacoinPrecision // TODO: figure out good cost
}
