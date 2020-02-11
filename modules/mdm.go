package modules

import (
	"encoding/binary"
	"fmt"

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
	// RPCIReadSectorLen is the expected length of the 'Args' of an Instruction.
	RPCIReadSectorLen = 25
)

var (
	// SpecifierReadSector is the specifier for the ReadSector RPC.
	SpecifierReadSector = InstructionSpecifier{'R', 'e', 'a', 'd', 'S', 'e', 'c', 't', 'o', 'r'}
)

// RPCIReadSector is a convenience method to create an Instruction of type 'ReadSector'.
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

// Cost describes the cost of executing an instruction on the MDM split up into
// its individual components.
type Cost struct {
	Compute      uint64 // NOTE: 1 compute cost corresponds to an estimated 2^17 hashes performed on data.
	DiskAccesses uint64 // # of writes and reads
	DiskRead     uint64 // bytes read from disk
	DiskWrite    uint64 // bytes written to disk
	Memory       uint64 // estimated ram used in bytes
}

// CalculateProgramCost computes the mdm.Cost of running Instructions.
func CalculateProgramCost(instructions []Instruction, dataLen uint64) (cost Cost, err error) {
	cost = cost.Add(InitCost(dataLen))
	for _, instruction := range instructions {
		switch instruction.Specifier {
		case SpecifierReadSector:
			cost = cost.Add(ReadSectorCost())
		default:
			return Cost{}, fmt.Errorf("calculateProgramCost: unknown instruction %v", instruction.Specifier)
		}
	}
	return
}

// ConvertCostToPrice uses a price table to convert a mdm.Cost to a
// types.Currency.
func ConvertCostToPrice(cost Cost, pt *RPCPriceTable) (price types.Currency) {
	// price = price.Add(types.NewCurrency64(cost.Compute).Mul(pt.Costs[MDMComponentCompute]))
	// price = price.Add(types.NewCurrency64(cost.DiskAccesses).Mul(pt.Costs[MDMOperationDiskAccess]))
	// price = price.Add(types.NewCurrency64(cost.DiskRead).Mul(pt.Costs[MDMOperationDiskRead]))
	// price = price.Add(types.NewCurrency64(cost.DiskWrite).Mul(pt.Costs[MDMOperationDiskWrite]))
	// price = price.Add(types.NewCurrency64(cost.Memory).Mul(pt.Costs[MDMComponentMemory]))
	return
}

// Add adds a Cost to another Cost and returns the result.
func (c Cost) Add(c2 Cost) Cost {
	return Cost{
		Compute:      c.Compute + c2.Compute,
		DiskAccesses: c.DiskAccesses + c2.DiskAccesses,
		DiskRead:     c.DiskRead + c2.DiskRead,
		DiskWrite:    c.DiskWrite + c2.DiskWrite,
		Memory:       c.Memory + c2.Memory,
	}
}

// Sub subtracts a Cost from another Cost.
func (c Cost) Sub(c2 Cost) (cost Cost, err error) {
	// Helper method that subtracts one number from another and returns 'false'
	// in case of an underflow.
	sub := func(a, b uint64) (uint64, bool) {
		return a - b, b <= a
	}
	var ok bool
	cost.Compute, ok = sub(c.Compute, c2.Compute)
	if !ok {
		err = errors.Extend(err, ErrInsufficientComputeBudget)
	}
	cost.DiskAccesses, ok = sub(c.DiskAccesses, c2.DiskAccesses)
	if !ok {
		err = errors.Extend(err, ErrInsufficientDiskAccessesBudget)
	}
	cost.DiskRead, ok = sub(c.DiskRead, c2.DiskRead)
	if !ok {
		err = errors.Extend(err, ErrInsufficientDiskReadBudget)
	}
	cost.DiskWrite, ok = sub(c.DiskWrite, c2.DiskWrite)
	if !ok {
		err = errors.Extend(err, ErrInsufficientDiskWriteBudget)
	}
	cost.Memory, ok = sub(c.Memory, c2.Memory)
	if !ok {
		err = errors.Extend(err, ErrInsufficientMemoryBudget)
	}
	if err != nil {
		return Cost{}, errors.Extend(ErrInsufficientBudget, err)
	}
	return cost, nil
}

// InitCost is the cost of instantiating the MDM
func InitCost(programLen uint64) Cost {
	return Cost{
		Compute:      1,
		DiskAccesses: 1,
		DiskRead:     0,
		DiskWrite:    0,
		Memory:       1<<22 + programLen, // 4 MiB + program data
	}
}

// ReadCost is the cost of executing a 'Read' instruction.
func ReadCost(contractSize uint64) Cost {
	return Cost{
		Compute:      1 + (contractSize / 1 << 40),
		DiskAccesses: 1,
		DiskRead:     1 << 22, // 4 MiB
		DiskWrite:    0,
		Memory:       1 << 22, // 4 MiB
	}
}

// ReadSectorCost is the cost of executing a 'ReadSector' instruction.
func ReadSectorCost() Cost {
	return Cost{
		Compute:      1,
		DiskAccesses: 1,
		DiskRead:     1 << 22, // 4 MiB
		DiskWrite:    0,
		Memory:       1 << 22, // 4 MiB
	}
}

// WriteSectorCost is the cost of executing a 'WriteSector' instruction.
func WriteSectorCost(contractSize uint64) Cost {
	return Cost{
		Compute:      1 + (contractSize / 1 << 40),
		DiskAccesses: 1,
		DiskRead:     1 << 22, // 4 MiB
		DiskWrite:    1 << 22, // 4 MiB
		Memory:       1 << 22, // 4 MiB
	}
}

// CopyCost is the cost of executing a 'Copy' instruction.
func CopyCost(contractSize uint64) Cost {
	return Cost{
		Compute:      2 + (contractSize / 1 << 40),
		DiskAccesses: 2,
		DiskRead:     1 << 23, // 8 MiB
		DiskWrite:    1 << 22, // 4 MiB
		Memory:       1 << 23, // 8 MiB
	}
}

// SwapCost is the cost of executing a 'Swap' instruction.
func SwapCost(contractSize uint64) Cost {
	return Cost{
		Compute:      2 + (contractSize / 1 << 40),
		DiskAccesses: 2,
		DiskRead:     1 << 23, // 8 MiB
		DiskWrite:    1 << 23, // 8 MiB
		Memory:       1 << 23, // 8 MiB
	}
}

// TruncateCost is the cost of executing a 'Truncate' instruction.
func TruncateCost(contractSize uint64) Cost {
	return Cost{
		Compute:      1 + (contractSize / 1 << 40),
		DiskAccesses: 1,
		DiskRead:     1 << 22, // 4 MiB
		DiskWrite:    1 << 22, // 4 MiB
		Memory:       1 << 22, // 4 MiB
	}
}
