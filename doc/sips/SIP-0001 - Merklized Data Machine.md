# SIP-0001 - Merklized Data Machine

Status: Proposal

SIP-0001 is a description of a virtual machine - the Merklized Data Machine
(MDM) - that executes instructions on the data in a Sia file contract. The file
contract tracks the size and Merkle root of the underlying data, which the MDM
will update when running instructions that modify the file contract data. Each
instruction can optionally produce a cryptographic proof that the instruction
was executed honestly. Every instruction has an execution cost, and instructions
are batched into atomic sets called 'programs' that are either entirely applied
or are not applied at all.

## Motivation and Rationale

The renter is required to perform increasingly sophisticated operations on the
host, especially as the renter is introducting new features such as garbage
collection, partial uploads, and file modifications. Further looking features
are even more complex than these.

Currently, each time the Sia team has chosen to add a new feature, the
renter-host protocol has needed an extension to support the feature. This not
only slows down Sia development, it presents a barrier to third parties looking
to develop novel applications on top of Sia.

The purpose of the MDM is to substantially increase the flexibility of the
renter-host protocol, enabling the renter to perform significantly more complex
behaviors and introduce novel ideas without needing any protocol extensions.
These more complex behaviors are enabled by a rich set of composable base
operations provided by the MDM.

## Machine Specification

The Merklized Data Machine (MDM) is a virtual machine on a host that performs
operations on the data in a Sia file contract. The data in the contract is
tracked by a size and a Merkle root. The Sia consensus protocol breaks the
contract data into 64 byte 'segments', where each segment is a leaf of the
Merkle tree that builds into the Merkle root. As the MDM operates on the data,
the size and Merkle root of the file contract will be updated, and optionally
proofs can be produced proving that the operations were executed faithfully.

### Sectors

The MDM breaks the contract data into 4 MiB 'sectors'. A sector's Merkle root
can be computed by treating each of the 65,536 segments as a leaf in a Merkle
tree and combining them into the 'sector root'. Because the sector root is
computed using the same leaves as the contract Merkle root, the sector roots can
themselves be used to compute the contract Merkle root. The MDM requires that
the contract size be a multiple of the sector size.

The host is explicitly expected to store all data within a sector continuously
on disk. Other than this requirement, contract data is not expected to be stored
continuously. This requirement allows the MDM to have predicatable performance
and a preditable execution cost, while allowing the host to optimize the
placement of multiple file contracts across multiple disks.

The host maintains a lookup table that maps from each sector root to the
location on disk where the data is stored. This lookup table is global to the
entire host, allowing callers to query data by sector root even if the caller
does not have access to the contract that manages the sector. The host also
allows callers to look up sector roots by providing a contract id and a sector
offset within the contract, allowing callers to see data that is held at a
specific location within a contract even if they do not know the sector root,
and even if they are not the owners of that contract.

Renters that wish to maintain privacy are expected to encrypt their data. The
host is not expected to enforce any access controls over a contract, especially
because there is no way to enforce that the host obeys the access controls.

The host itself does not have a mapping from sector root to the contract that
contains the sector. This means that data cannot be modified merely by knowing
the sector root, the caller also needs to know the corresponding contract and
the location of the sector within the contract, and additionally needs to have
ownership of the contract to sign any update.

### Programs

The MDM has 'instructions' that can be executed, each instruction performing a
distinct operation on the contract data. Every instruction starts with an
initial Merkle root and size and produces an updated Merkle root and size.

Instructions are batched together into a 'program'. Within a program, each
instruction will pass its updated Merkle root and file contract size to the next
instruction. The final instruction will pass the final Merkle root and contract
size to the MDM to be placed into an updated file contract revision that needs
to be signed by the host and by the contract owner. If the final contract size
and Merkle root are identical to the original, no signature is needed from the
contract owner.

#### Input

The program has a single data field which instructions use to specify their
input. Having a single data field allows multiple instructions to re-use the
same input, which can be helpful for compression. Having a single program data
field also allows possible future extensions to the MDM that would modify the
program data, substantially boosting the power of the MDM. No such instructions
exist within the SIP-0001 specification.

A program itself takes the following form:

```go
// An Operand is a pointer to input for the instruction. The input itself lies
// within the program data.
type Operand struct {
	Offset uint64
	Len    uint64
}

type InstructionInfo struct {
	// The op code tells the MDM how to interpret the instruciton.
	OpCode types.Specifier

	// Pointers to the inputs of the instructions within the program data. The
	// total number of operands is specific to each instruction.
	Operands []Operand

	// If set to 'true', the InstructionOuput will contain a proof that allows
	// the caller to verify the execution of the instruction without needing the
	// file contract data.
	ProofRequired bool
}

type Program struct {
	// The contract specifies which contract is being modified by the MDM. The
	// MDM also supports a special 'read only' mode, which can be triggered by
	// setting 'Contract' to the value 'ReadOnly'.
	Contract types.FileContractID

	Instructions []InstructionInfo
	Data         []byte
}
```

Each instruction points to areas of the program data. Program data is uploaded
sequentially, and instructions will be executed as soon as all of the program
data necessary for that instruction is available. This allows the program to
begin exection even before all of the input is uploaded, reducing latency for
programs with a substantial amount of input.

#### Output

As the program executes, each instruction will produce output to be sent to the
caller. The output will be sent to the caller as the program executes, which
will reduce the latency on the caller receiving any data, which is particularly
useful for programs which contain multiple large Read operations.

Instruction outputs take the following form:

```go
type InstructionOutput struct {
	// The error will be set to nil unless the instruction experienced an error
	// during execution. If the instruction did experience an error during
	// execution, the program will halt at this instruction and no changes will
	// be committed.
	//
	// The error will be prefixed by 'invalid' if the error resulted from an
	// invalid program, and the error will be prefixed by 'hosterr' if the error
	// resulted from a host error such as a disk failure. If the error resulted
	// and interrupt signal, the error will be prefixed by 'interrupted'.
	Error error

	// The proof will be set to nil if there was an error, and also if no proof
	// was requested by the caller. Using only the proof, the caller will be
	// able to compute the next Merkle root and size of the contract.
	Proof []crypto.Hash

	// The output will be set to nil unless the instruction produces output for
	// the caller. One example of such an instruction would be 'Read()'. If
	// there was an error during execution, the output will be nil.
	Output []byte
}
```

Though the outputs of the program are sent as the program executes,
modifications to the contract data are not committed until the caller has had
the chance to sign the final state of the contract following the termination of
the program. If the caller approves the final state of the contract data, all
changes will be committed atomically.

If the caller has all of the required metadata already and does not need proofs
to know the final state of the contract, the caller can send a signed version of
the updated contract even before the program is done executing, as the caller
should be able to derive the final state in advance. This allows the host to
commit the updated state immediately upon completing the program instead of
needing to wait for an extra network round trip, improving update latency.

#### Execution Failures

If a program instruction has an execution failure, the program will stop
executing. Any changes will not be committed, and any updates to the file
contract (pre-signed or not) will be dropped. An error will be returned by the
MDM which indicates what type of execution failure occurred. There are three
types of execution failures.

The first type of execution failure is an invalid program failure. This can
occur if a program attempts an illegal instruction or would reuqire consuming
more resources than were budgeted for the program. An invalid program failure is
also returned in the event of an interrupt.

The second type of execution failure is a host error failure. This can happen if
a host experiences a disk error when performing a read or write, or has some
other unexpected issue that did not result from an invalid instruction.

The final type of execution failure is an interruption. The renter has the
ability to send an interrupt signal which tells the host to stop executing a
program.

The MDM specifically is indifferent to whether the failure is due to an invalid
program, a host error, or an interruption, however the higher level processes
that instantiate the MDM and charge money for its execution depend on knowing
whether the failure was a host error or an invalid program. Knowledge of who is
at fault is also highly relevant to the renter.

#### Interruption

A program being executed may be interrupted. If an interrupt signal is received,
the host will stop executing instructions, and will stop sending any output
which has already been created. The first instruction to not execute will
present an interrupted program error in its output.

Because of latency between the caller and the host, the interrupt signal may
arrive late. If a contract update was pre-signed, sending an interrupt may fail
to prevent the update from being applied. The host will acknowledge the
interrupt even if the signal arrives too late to prevent an update from being
applied.

Interrupts are particularly useful when reading data from the host. In the
example of video streaming, the renter will be attempting to cache data to
improve the user experience, grabbing parts of the video that the user has not
needed yet but is about to need soon. If the user suddenly seeks to a new part
of the stream, these anticipatory fetches will no longer be relevant. Being able
to cancel them will free up bandwidth to fetch data that the user needs
immediately.

### Execution Modes

#### Read Only Mode

If a program has the 'Contract' field set to the value 'ReadOnly', the MDM will
execute in read only mode. No lock is needed, no signatures are needed, and many
read only MDMs can execute simultaneously. Read only MDM instances can even
execute in parallel to read-write MDMs operating on the same data.

An immediate advantage to supporting a read only mode that is non-exclusive with
read-write mode is that a renter can upload and download from the same contract
at the same time, allowing a single host to simultaneously saturate both the
upload bandwidth and the download bandwidth of a renter on a single connection
using a single contract. This also greatly improves the responsiveness of the
renter for streaming downloads when a user is currently uploading large amounts
of data.

Another advantage of read only mode is that data publishing is better supported.
Many clients can read the same data at once, and a publisher can update the data
concurrently without disrupting reads that are in progress or causing hiccups
for their users.

Race conditions between read only programs and read-write programs are handled
at the instruction level. Each instruction will gain exclusive access to the
data on disk before performing a read or write, and then will release that
exclusivity before finishing execution. This means that a single read only
program which reads the exact same location multiple times may get different
results each time. This caveat is exclusive to read only programs, as read-write
programs have a lock on the contract which excludes all other read-write
programs.

#### Read-Write Mode

An MDM can be set to run in read-write mode by setting the 'Contract' value of
the program equal to an existing file contract ID. When locking the contract,
the caller must provide a signature that proves they have knowledge of the
private key of the contract. An MDM running in read-write mode has full access
to all of the instructions in the MDM, including the ones that modify the
contract data. Before the MDM starts running, an exclusive lock must be obtained
on the contract that is being modified. The exclusive lock will not block read
only MDMs, however it will block other RPCs which need access to the contract,
such as any RPC attempting to renew the contract.

The contract lock will prevent all other processes from updating the contract
because other processes that can update the contract may change the filesize or
Merkle root, which would invalidate any updates and proofs made by the
read-write MDM.

The lock around the contract is held until the program is completed. If the
caller has sent a valid presigned a file contract covering the update, the lock
will be released as soon as the changes have committed. If the caller did not
send a pre-signed file contract, the lock will be held after the updated
contract is sent to the caller for a signature, until the caller either accepts
and returns a countersigned contract, rejects, or times out.

In the event of an error, a time-out, or a rejection, the changes are aborted
and the lock is dropped.

### Resource Consumption and Resource Limits

The MDM consumes resources on the host. To achieve fairness between programs and
protect the host against Denial of Service attacks, several resources are
tracked as the MDM executes, and each program is run with a limit for total
consumption of each resource.

Each instruction needs to be able to pre-compute the total resource requirements
for execution immediately prior to execution. If executing the instruction would
put the MDM over any of its resource limits, the program will halt with an
invalid program error.

Five total resources are tracked. The host is able to price each resource, and
the resources are chosen to allow hosts to roughly put a fair price on the
execution of different types of programs even when each host is operating on
different types of hardware. The five resources are:

* Memory Consumption
* Compute Power
* Disk Accesses
* Disk Reads
* Disk Writes

The bandwidth resources are tracked at the networking layer, and are therefore
ignored by the MDM.

All resources are tracked cumulatively, meaning that the totals count up as each
instruction executes by the cost of that instruction. It is assumed that memory
will not be able to be reclaimed until after the program has terminated. In
particular, the output of an instruction may stick around long after the
instruction executes because the output will be put in a queue to be sent to the
caller, and this queue may take a while to process.

Once the program has completed, the costs will be returned to the networking
layer, which also handles pricing and will be responsible for charging the
renter for the resources used.

Instantiating the MDM has the following costs:

```go
MDMCost{
	Compute:      1,
	DiskAccesses: 1 access,
	DiskRead:     0 bytes,
	DiskWrite:    0 bytes,
	Memory:       4 MiB + len(encodedProgram),
}
```

Note that 1 compute cost corresponds to an estimated 2^17 hashes performed on
data. Getting the sector root of a sector requires performing roughly 2^17
hashes. Getting the Merkle root of a contract requires performing roughly
(contractSize / 2^22) hashes, because every sector root needs to be combined
together in a Merkle tree again. Assuming that 2^17 hashes incurs a cost of 1,
the cost of computing the Merkle root of a contract is (contractSize / 2^39).
Getting the Merkle root of a contract only incurs a cost greater than 1 once the
size of the contract exceeds 512 GiB, and then goes up by 1 per 512 GiB.

With more sophisticated cacheing of sector roots, the cost of getting the Merkle
root of a contract can be eliminated, however as of SIP-0001, more sophisticated
caching is not assumed.

## Supported Instructions

This is a list of instructions that are supported by the MDM as of SIP-0001.
Each instruction has a call signature, a set of inputs, a set of outputs, and
set of equations for determining the cost of executing the instruction.

When encoded into InstructionInfo, the name of the instruction will be used as
the 'types.Specifier', with blank values for all remaining characters. Each
input in the call signature will be a single operand. The program producer has
the flexibility of choosing how to order the inputs within the actual program
data, however the naive solution of putting them in order is reasonably
effective and in many cases optimal or nearly optimal. Each input should be
encoded into the program data using Sia encoding.

The return values of the program should be interpreted as a single struct, also
encoded using Sia encoding.

### Read Only Instructions

These are the instructions that are supported in read only mode. Note that these
instructions may also be called in read-write mode.

#### Read

```go
Read(contract types.FileContractID, offset, length uint64) []byte
```

Read will read 'length' bytes from 'contract', starting from the provided
offset. The request must exist fully within the bounds of the contract, and the
request must also exist fully within the bounds of a single sector.

The output of Read is the 'length' bytes that appear in the contract starting
from the provided offset.

Race conditions between this instruction and potential write instructions
operating on the same data are handled by grabbing a lock on the list of sector
roots for the input file contract and making a copy of those roots. While the
copy is being made, the host will read the relevant sector into memory. The lock
is released once the copy is completed and the relevant sector is in memory.
Note that this lock is different from a contract lock, the contract lock refers
to the ability to make changes to the file contract, this is merely a lock of
the list of sector roots in that contract.

The Read instruction can fail if the contract does not exist, or if the contract
is no longer large enough to cover the read. The Read instruction should not
fail if another MDM is modifying the same piece of data, and the Read
instruction should not return corrupted data - either it will return the data
that existed before the other MDM made a change, or it will return the data that
existed after the other MDM made a change.

Consistency is only guaranteed within this instruction, there are no consistency
guarantees between read instructions, even if they are on the same contract. The
one exception to this is that consistency will be guaranteed if the Read is
performed on a contract from a read-write MDM that has a lock on the same
contract being read.

```go
MDMCost{
	Compute:      1 + (contractSize / 2^39),
	DiskAccesses: 1,
	DiskRead:     4 MiB,
	DiskWrite:    0,
	Memory:       4 MiB,
}
```

#### ReadSector

```go
ReadSector(sectorRoot crypto.Hash, offset, length uint64) []byte
```

ReadSector will read data from the sector with the provided sector root.
'length' bytes will be read from the sector starting from the provided offset.
The request must exist fully within the bounds of the sector.

The output of ReadSector is the 'length' bytes that appear in the sector
starting from the provided offset.

ReadSector can fail if the sector with the provided root no longer exists on the
host. ReadSector uses the host's global sector lookkup table, and therefore is
not associated with any particular file contract.

```go
MDMCost{
	Compute:      1,
	DiskAccesses: 1,
	DiskRead:     4 MiB,
	DiskWrite:    0,
	Memory:       4 MiB,
}
```

### Read-Write Instructions

These instructions are only supported in read-write mode. These instructions may
only be called if the MDM has opened editing access on a file contract, and the
ID of that file contract is an implicit parameter of every instruction. The
contract being modified will be referred to as the 'parent contract'.

#### Write

```go
Write(offset uint64, data []byte)
```

Write will write the provided data at the provided offset within the parent
contract, overwriting any data that already exists at that location. Write must
start within an existing sector, and must no go beyond the bounds of that
sector. This means that Write cannot be used to append to a contract.

```go
MDMCost{
	Compute:      1 + (contractSize / 2^39),
	DiskAccesses: 1,
	DiskRead:     4 MiB,
	DiskWrite:    4 MiB,
	Memory:       4 MiB,
}
```

#### Copy

```go
Copy(destOffset, sourceOffset, size uint64)
```

Copy will copy the data from sourceOffset to destOffset, overwriting existing
data. The source of the copy must be contained entirely within a sector, and the
destination of a copy must be contained entirely within a sector.

```go
MDMCost{
	Compute:      2 + (contractSize / 2^39),
	DiskAccesses: 2,
	DiskRead:     8 MiB,
	DiskWrite:    4 MiB,
	Memory:       8 MiB,
}
```

#### Swap

```go
Swap(offset1, offset2, size uint64)
```

Swap will read data from offset1, then read data from offset2, then write the
data from offset1 to offset2, then write the data from offset2 to offset1. Each
offset+size must exist entirely within the bounds of a sector, however offset1
and offset2 can be from different sectors.

```go
MDMCost{
	Compute:      2 + (contractSize / 2^39),
	DiskAccesses: 2,
	DiskRead:     8 MiB,
	DiskWrite:    8 MiB,
	Memory:       8 MiB,
}
```

#### Truncate(newSize uint64)

```go
Truncate(newSize uint64)
```

Truncate will change the size of the contract data to be equal to the newSize,
throwing away data on the tail of the file if the new size is smaller, and
appending zeroes to the end of the contract data if the new size is larger. If
newSize is equal to the current size of the contract data, this instruction is a
no-op.

'newSize' must be a multiple of the sectorSize, and no more than one sector can
be added or deleted in a single instruction.

```go
MDMCost{
	Compute:      1 + (contractSize / 2^39),
	DiskAccesses: 1,
	DiskRead:     4 MiB,
	DiskWrite:    4 MiB,
	Memory:       4 MiB,
}
```

## Example Programs

The following are a set of example programs that are representative of the types
of programs we wish to add to Sia in the coming months.

### Append New Sector

The following program appends a new sector to a contract that is already 3
sectors large.

```go
Program (
	Truncate(4 * 2^22)
	Write(3 * 2^22, newData)
)
```

### Delete a Sector

The following program deletes a sector from a contract that is 5 sectors large.
The second sector is deleted by swapping it with the final sector, and then
after the swap dropping the final sector.

```go
Program (
	Swap(2^22, 4 * 2^22, 2^22)
	Truncate(4 * 2^22)
)
```
