# SIP1 - Merklized Data Machine

SIP1 is a description of a virtual machine on a host that executes instructions
on a merklized data set. Each instruction can be accompanied by a cryptographic
proof that the instruction was applied correctly. Every instruction has a
computable cost, and instructions can be batched into atomic sets that are
either entirely applied or not applied at all.

## Machine Specification

The Merklized Data Machine is a virtual machine on a host that performs
operations on a merklized file. The file itself is 'N' bytes, broken up
into an array of 64 byte segments, where each segment forms a leaf of a Merkle
tree. The final leaf may have fewer than 64 bytes if the data is not perfectly
aligned to 64 bytes.

The file is divided into sectors that are 4 MiB large. The sectors can be
accessed randomly knowing nothing more than the Merkle root of the sector. Any
updates must also update the lookup table for these sectors. All of the data
within a sector is expected to be continuous on disk (for cost analysis and
performance expectations), however multiple consecutive sectors within a file
are not expected to be continuous on disk.

Sector roots are put into a global lookup table on the host, not a per-file
lookup table. Read operations can be performed on any sector on the host, even
sectors that are not associated with the current file for the MDM. Write
operations can only be performed on the current file of the MDM.

The virtual machine has 'instructions' that can be executed, each instruction
performing a distinct operation on the data. Every instruction starts with an
initial file and produces an updated file. This updated file has a new
Merkle root associated with it. Instructions can produce output data, and
optionally also a Merkle proof that the update was performed honestly.

Each instruction has an associated cost which attempts to mirror the actual cost
of performing the instruction to the host. The cost of an instruction can be
dependent on the input to the instruction, but not on the payload of the
instruction. The instruction input is limited to 4 kib, but the instruction
payload can be larger. The cost will always focus on the consumption of 4 major
resources:

	+ Disk Accesses
	+ Disk Reads
	+ Disk Writes
	+ Compute Power

Instructions can be batched together. A single batch of instructions will be
applied atomically - either all instructions will be applied or none of the
instructions will be applied. Even though the instructions are being executed as
a batch, the output of each instruction will be provided separately, meaning
that in terms of output and midstate, each instruction behaves as though it were
the only instruction being executed.

Because the cost of each instruction can be computed based on the input alone,
the cost of a batch of instructions can also be computed by adding up the cost
of each individual instruction in the batch.

A batch of instructions being executed can be interrupted. If an interrupt
signal is received, the host will attempt to abort the batch, reverting all
changes and accepting the interrupt. It may be the case that an interrupt is
received after a batch has been fully comitted, and can no longer be reversed.
If this happens, the host will reject the interrupt. Rejecting the interrupt is
not an error, both acceptance and rejection of an interrupt are considered
successful calls. The full cost of the batch operation will be applied even if
the batch is interrupted early.

Instantiating the MDM has the following costs:

Disk Access:   0 accesses
Disk Read:     0 bytes
Disk Write:    0 bytes
Compute Power: 1 unit

## Instruction Format

The MDM is always called using one batch of instructions at a time. The batch
has a set of instructions followed by a corresponding set of inputs to each
instruction. Each instruction will have its own semantics for interpreting
input.

```go
type MerkleRoot crypto.Hash

type MerkleProof []crypto.Hash

// InstructionSerialization contains a generic instruction serialized for the
// MDM. The name indicates which instruction should be used. The inputs are all
// of the information required to compute the cost of executing the instruction.
// The inputs are also sufficient to calculate the resulting file size given the
// initial file size.
//
// The inputs are required to be under 4 kib total, any larger inputs need to be
// placed in the payload. The data in the payload must not influence the cost of
// executing the instruction.
type InstructionSerialization struct {
	Name          types.Specifier
	Inputs        []byte
	ProofRequired bool
}

// Instructions are provided to the MDM as a batch.
type MDMBatchInput struct {
	InitialMerkleRoot MerkleRoot
	InitialSize       uint64

	// These slices are the same size. The set of instructions must be provided
	// before the payloads. This ordering enables the MDM to perform
	// optimizations around later instructions before having to read the entire
	// payload of earlier instructions. Some instructions will have a 'nil'
	// payload.
	Instructions        []InstructionSerialization
	InstructionPayloads [][]byte
}

// The MDM returns a set of results from executing a batch of instructions.
type MDMBatchOutput struct {
	FinalMerkleRoot MerkleRoot
	FinalSize       uint64

	// These slices are the same size as the input slices, one element per
	// instruction. Some of the elements of each slice may be 'nil' if there is
	// no ouptut or proof for that instruction.
	//
	// Each proof should enable a verifier to chain a previous intermediate
	// merkle root to an updated intermediate Merkle root. The very first
	// intermediate root is the InitialMerkleRoot of the batch input, and the
	// final intermediate root should match the FinalMerkleRoot of the batch
	// output. The same goes for intermediate sizes, each instruction begins
	// with an intermediate size and... TODO.
	InstructionOutputs [][]byte
	InstructionProofs  []MerkleProof
}

// The MDMCost is the cost of executing an MDM instruction or batch of
// instructions.
type MDMCost struct {
	DiskAccesses uint64
	DiskReads    uint64
	DiskWrites   uint64
	ComputePower uint64
}

// Instruction defines the set of methods required to build, cost, and execute
// an instruction within the MDM. The instruction itself should be stateless,
// all required inputs are provided in the interface.
type Instruction interface {
	// The initialSize and the inputs should be the only information required to
	// compute the cost of executing the instruction and the resulting size of
	// the file.
	CostAndSize(initialSize uint64, inputs []byte) (*MDMCost, uint64)

	// The instruction is executed on file 'f', mutating the state of 'f' in a
	// way that can be reverted if a future instruction in the batch fails. If
	// the instruction has no output, 'output' will be nil even for a successful
	// execution. If no proof is requested, 'proof' will be 'nil' even for a
	// successful execution.
	Execute(f *file, inputs []byte, proofRequired bool, payload []byte) (output []byte, proof []byte, err error)
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

// CostAndSize reports the cost of executing a batch input and the resulting
// size of the file from executing the batch.
func (bi *MDMBatchInput) CostAndSize() MDMCost {
	totalCost := new(MDMCost)
	size := initialSize
	for _, is := range bi.Instructions {
		i := getInstruction(is.Name) // Fetches the instruction object associated with the instruction name.
		newCost, newSize := i.CostAndSize(size, i.Inputs, i.ProofRequired)
		size = newSize
		totalCost.Add(totalCost, newCost)
	}
	c.ComputePower++ // Cost of initializing a batch of instructions.
	return c, size
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

Disk Accesses: `1 + (length / sectorSize)`
Disk Read: `length`
Disk Write: `0`
Computation Cost: `2 * Log2(contractSize/64)`

### ReadRoot(root, offset, len)

### Write(data, offset, len)

### WriteRoot(root, data, offset, len)

### Copy(size, offset1, offset2)

Copy will copy the data from offset2 to offset1, overwriting the data that
currently exists in offset1.

### Swap(size, offset1, offset2)

### Truncate(newSize)

Truncate will change the size of the file to be equal to the newSize, throwing
away data on the tail of the file if needed, and appending zeroes to the end of
the file if needed. If newSize is equal to the current size of the file, this
instruction is a no-op.
