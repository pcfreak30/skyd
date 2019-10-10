# SIP1 - Merklized Data Machine

SIP1 is a description of a virtual machine - the Merklized Data Machine (MDM) -
that executes instructions on the data in a Sia file contract. The file contract
tracks the size and Merkle root of the underlying data, and each instruction of
the MDM will access or modify the data in some way. Each instruction can
optionally produce a cryptographic proof that the instruction was executed
honestly. Every instruction has a computable cost, and instructions can be
batched into atomic sets that are either entirely applied or not applied at all.
A batch is called a program.

## Machine Specification

The Merklized Data Machine (MDM) is a virtual machine on a host that performs
operations on the data in a Sia file contract. The data in the contract is
tracked by a size and a Merkle root. The Sia consensus protocol breaks the
contract data into 64 byte 'segments', where each segment is a leaf of the
Merkle tree that builds into the Merkle root. The MDM operates on this data.

### Sectors

The MDM breaks the contract data into 4 MiB 'sectors'. A sector's Merkle root
can be computed by treating each of the 16,384 segments as a leaf in a Merkle
tree and combining them into the root. Because the sector root is computed using
the same leaves as the contract Merkle root, the sector roots can themselves be
used to compute the contract Merkle root. The MDM requires that the contract
size be a multiple of the sector size.

The host is explicitly expected to store all data within a sector continuously
on disk. Other than this requirement, contract data is not expected to be stored
continuously.

The host maintains a global lookup table that maps from each sector root to the
location on disk where the data is stored. This allows users to query data by
its Merkle root, even if the user does not know which contract is responsible
for maintaining the data. The global lookup table also maps from a sector root
to the contract that stores the sector and to the index within the contract
where the sector currently resides, allowing the owner of a sector to modify the
sector without needing to know it's location within the contract. This global
mapping gets updated any time that the data in a contract is altered.

### Programs

The MDM has 'instructions' that can be executed, each instruction performing a
distinct operation on the data. Every instruction starts with an initial Merkle
root and size and produces an updated Merkle root and size. Instructions can
produce output data to be sent to the caller, and optionally also a Merkle proof
that the update was performed correctly.

Instructions are batched together into 'programs'. The first instruction takes
the contract Merkle root and size as its state input, and then passes the
updated Merkle root and size to the next instruction. The final instruction
passes the final Merkle root and size to the user.

The program has a single data payload. Each instruction can have operands which
total no more than 4 KiB per instruction. These operands can reference the
program data payload. Multiple instructions can reference the same areas of the
payload if desired.

### Tracking Resource Consumption

Each instruction has an associated cost which roughly attempts to mirror the
actual cost of performing the instruction to the host. The cost is a measurement
of the 4 following resources:

* Disk Accesses
* Disk Reads
* Disk Writes
* Compute Power

The cost of an instruction is required to be computable using nothing but the
starting contract size and the operands of the instruction. The cost must not be
dependent on the program payload nor any other external information. The same is
true for the size of the contract - the final size of the contract data must be
computable using nothing more than starting size and the instruction operands.

Because the cost of each instruction can be computed based on the operands
alone, the cost of a program can also be computed by adding up the cost of each
individual instruction run in the program. And because the contract size can be
updated using nothing more than each instruction's operands, the final size of
the contract can be computed by pre-computing the output size of each
instruction run in the program, and then taking the final output size from the
final instruction.

Any branching instructions are only allowed to skip instructions, branching code
is not allowed to jump to a part of the program that has already run. When
costing a program, the assumption is that the branch will not skip any
instructions, meaning that the cost will always provide a worst case estimate
for executing the program.

Instantiating the MDM has the following costs:

* Disk Access:   0 accesses
* Disk Read:     0 bytes
* Disk Write:    0 bytes
* Compute Power: 1 unit

### Interruption

A program being executed can be interrupted. If an interrupt signal is received,
the host will attempt to abort the program, reverting all changes and accepting
the interrupt. It may be the case that an interrupt is received after a program
has been fully comitted, and can no longer be reverted. If this happens, the
host will reject the interrupt. Rejecting the interrupt is not an error, both
acceptance and rejection of an interrupt are considered successful calls. The
full cost of the program will be applied even if the program is interrupted
early.

## Program Format

The MDM is always called using program at a time.

```go
// TODO: Work in branching

type MerkleRoot crypto.Hash

type MerkleProof []crypto.Hash

// InstructionSerialization contains a generic instruction serialized for the
// MDM. The OpCode indicates which instruction should be used. The operands are
// all of the information required to compute the cost of executing the
// instruction and the resulting size of the contract data.
//
// The operands are required to be under 4 kib total, any larger inputs need to
// be placed in the program payload.
type InstructionData struct {
	OpCode        types.Specifier
	Operands      []byte
	ProofRequired bool
}

// Instruction defines the set of methods required to build, cost, and execute
// an instruction within the MDM. The instruction itself should be stateless,
// all required inputs are provided in the interface.
type Instruction interface {
	// The initialSize and the inputs should be the only information required to
	// compute the cost of executing the instruction and the resulting size of
	// the file.
	//
	// Size is necessary to compute in advance for two reasons. The first is
	// that in some cases, for example with proof generation, the size can have
	// an impact on the final cost. The second is that the host needs to be able
	// to tell if there is enough disk space available to execute all of the
	// instructions.
	CostAndSize(initialSize uint64, proofRequired bool, operands []byte) (*MDMCost, uint64)

	// The instruction is executed on file 'f', mutating the state of 'f' in a
	// way that can be reverted if a future instruction in the batch fails. If
	// the instruction has no output, 'output' will be nil even for a successful
	// execution. If no proof is requested, 'proof' will be 'nil' even for a
	// successful execution.
	Execute(env *environment, operands []byte, proofRequired bool, programPayload []byte) (output []byte, proof []byte, err error)
}

// Authorization of the program is assumed to have already happened by the time
// the MDM is initialized.
type Program struct {
	Code    []InstructionData
	Payload []byte
}

// When a program has terminated, this data is sent to the user.
type ProgramOutput struct {
	FinalMerkleRoot MerkleRoot
	FinalSize       uint64

	// These slices are the same size as the input slices, one element per
	// instruction. Some of the elements of each slice may be 'nil' if there is
	// no ouptut or proof for that instruction.
	//
	// Each proof should enable a verifier to chain a previous intermediate
	// merkle root to an updated intermediate Merkle root. The very first
	// intermediate root is the starting root of the file contract, and the
	// final intermediate root should match the FinalMerkleRoot of the program 
	// output.
	InstructionOutputs [][]byte
	InstructionProofs  []MerkleProof
}

// The MDMCost is the cost of executing an MDM instruction or program.
type MDMCost struct {
	DiskAccesses uint64
	DiskReads    uint64
	DiskWrites   uint64
	ComputePower uint64
}

// Add will add the costs of x and y together, placing the result in z and
// returning z.
func (z *MDMCost) Add(x, y *MDMCost) *MDMCost {
	z.DiskAccesses = x.DiskAccesses + y.DiskAccesses
	z.DiskReads = x.DiskReads + y.DiskReads
	z.DiskWrites = x.DiskWrites + y.DiskWrites
	z.ComputePower = x.ComputerPower + y.ComputePower
	return z
}

// CostAndSize reports the cost of executing a program and the resulting size of
// the file from executing the program.
//
// The size following the execution of a program is needed so that the host can
// verify before execution that there is enough space available.
func (p *Program) CostAndSize() MDMCost {
	totalCost := new(MDMCost)
	size := initialSize
	for _, id := range bi.Instructions {
		i := getInstruction(id.OpCode) // Fetches the instruction object associated with the instruction name.
		newCost, newSize := i.CostAndSize(size, i.Inputs, i.ProofRequired)
		size = newSize
		totalCost.Add(totalCost, newCost)
	}
	c.ComputePower++ // Cost of initializing a batch of instructions.
	return c, size
}

func (p *Program) Execute(env *environment) (ProgramOutput, error) {
	var po ProgramOutput
	for _, id := rangebi.Instructions {
		i := getInstruction(id.OpCode)
		iOutput, iProof, err := i.Execute(env, id.Operands, id.ProofRequired, p.Payload)
		if err != nil {
			return ProgramOutput{}, err
		}
		po.InstructionOutputs = append(po.InstructionOutputs, iOutput)
		po.InstructionProofs = append(po.InstructionProofs, iProof)
	}
	env.commit()
	return ProgramOutput, nil
}
```

The MDM is always instantiated with a list of instructions followed by a list
inputs for each instruction.

## Supported Instructions

Notation: the instructions are written as functions that take multiple variables
as input. Within the InstructionSerialization struct, these inputs will be
serialized using the encoding package. If there is a payload, the payload
variables will be prefixed by 'payload'.

### Read(offset, length uint64) (output []byte, 

Read will return data from the file starting from the provided offset, reading
'length' data. The request must not extend outside the boundaries of the file.
There is no payload for Read.

The output of Read is the data that appears in the file starting from the
provided offset and going for 'length' bytes.

* Disk Accesses: `1 + (length / sectorSize)`
* Disk Read: `length`
* Disk Write: `0`
* Computation Cost: `2 * Log2(contractSize/64)`

### ReadSector(root, offset, len)

ReadRoot will read data from a sector with a Merkle root 'root'. If the sector
does not exist, an error will be returned. If the read requests goes outside the
boundaries of the root, an error is returned.

The output of ReadRoot is the data that appears in the sector starting from the
provided offset and going for 'length' bytes.

### Write(data, offset, len)

Write can extend beyond the boundary of the contract data, causing the size of
the contract to be increased. The data within the contract must remain
continuous however, the destination offset must at most be at the end of the
existing contract data.

### WriteSector(root, data, offset, len)

WriteSector cannot go outside the bounds of the sector.

### Copy(size, offset1, offset2)

Copy will copy the data from offset2 to offset1, overwriting the data that
currently exists in offset1. Copy can copy beyond the boundary of the file,
however must remain continuous just like Write.

### Swap(size, offset1, offset2)

### Truncate(newSize)

Truncate will change the size of the file to be equal to the newSize, throwing
away data on the tail of the file if needed, and appending zeroes to the end of
the file if needed. If newSize is equal to the current size of the file, this
instruction is a no-op.

### QUESTION: Should we support CopySector, which would take multiple roots and
allow the renter to copy data from one sector to another?
