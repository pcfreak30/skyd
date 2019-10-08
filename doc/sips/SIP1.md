# SIP1 - Merklized Data Machine

SIP1 is a description of a virtual machine on a host that executes instructions
on a merklized data set. Each instruction is accompanied by a cryptographic
proof that the instruction was applied correctly. Every instruction has a
computable cost, and instructions can be batched into atomic sets that are
either entirely applied or not applied at all.

## Machine Specification

The Merklized Data Machine is a virtual machine on a host that performs
operations on a merklized data set. The dataset itself is 'N' bytes, broken up
into an array of 64 byte segments, where each segment forms a leaf of a Merkle
tree. The final leaf may have fewer than 64 bytes if the data is not perfectly
aligned to 64 bytes.

The virtual machine has 'instructions' that can be executed, each instruction
performing a distinct operation on the data. Every instruction starts with an
initial dataset and produces an updated dataset. This updated dataset has a new
Merkle root associated with it. Instructions can produce output data, and
optionally also a Merkle proof that the update was performed honestly.

Each instruction has an associated cost which attempts to mirror the actual cost
of performing the instruction to the host. The cost of an instruction can be
dependent on the input to the instruction. The cost will always focus on the
consumption of 4 major resources:

	+ Disk Accesses
	+ Disk Reads
	+ Disk Writes
	+ Compute Power

The cost of an instruction will always be computable using only the input to the
instruction and the size of the file contract. Further, the cost of an
instruction will grow at most logarithmically in the size of the contract.

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
successful calls.

Instantiating the MDM has the following costs:

Disk Access: 0 accesses
Disk Read: 0 bytes
Disk Write: 0 bytes
Computation Cost: 1 Hash // TODO: More or less might be fair, need to benchmark

## Instruction Format

The MDM is always called using one batch of instructions at a time. The batch
has a set of instructions followed by a corresponding set of inputs to each
instruction. Each instruction will have its own semantics for interpreting
input.

// TODO: Cost eventually gets translated into 

```go
type Instruction string

type MerkleRoot crypto.Hash

type MerkleProof []crypto.Hash

type MDMBatchInput struct {
	InitialMerkleRoot MerkleRoot
	InitialSize       uint64

	// These slices are the same size.
	Instructions      []Instruction
	ProofRequired     []bool
	InstructionInputs [][]byte
}

type MDMBatchOutput struct {
	FinalMerkleRoot MerkleRoot
	FinalSize       uint64

	// These slices are the same size as the input slices. Some of the elements
	// of each slice may be 'nil' if there is no ouptut or proof for that
	// instruction.
	InstructionOutputs [][]byte
	InstructionProofs  []MerkleProof
}
```

The MDM is always instantiated with a list of instructions followed by a list
inputs for each instruction.

## Supported Instructions

### Read(offset, len)

Read will read data within the contract starting at the provided offset and the
read will cover the provided length. The proof is a Merkle proof

Disk Accesses: 1 access
Disk Read: len bytes
Disk Write: 0 bytes
Computation Cost: Log2(contractSize/64) Hashes // TODO: Actually I think it's trickier than this

### ReadRoot(root, offset, len)

Read will read data within the contract by seeking to the provided root within
the contract, and then reading data from the root at the provided offset and
len. The offset and len must be fully contained within the root.

Disk Accesses: 1 access
Disk Read: len bytes
Disk Write: 0 bytes
Computation Cost: Log2(contractSize/64) Hashes // TODO: Actually I think it's trickier than this

### Write(data, offset, len)

### WriteRoot(root, data, offset, len)

### Copy(size, offset1, offset2)

Copy will copy the data from offset2 to offset1, overwriting the data that
currently exists in offset1.

### Swap(size, offset1, offset2)

### Truncate(newSize)
