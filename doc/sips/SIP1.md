# SIP1 - Merklized Data Machine

SIP1 is a description of a virtual machine protocol between a renter and a host.
The renter knows nothing beyond the size of the data maintained by the host and
the merkle root of the underlying data. The renter can send instructions to the
host requesting data and data modifications in a loop. The host will then
provide a response which includes any requested data and a cryptographic proof
for any operations.

TODO: Computation cost might be pessimistic. Proof size might also be
pessimistic. Better to be pessimistic and overcharge for operations and optimize
later than to underestimate and be vulnerable to DoS.

## Machine Specification

The initial state is a file contract with a Merkle root and a filesize. The
virtual machine offers a set of operations that can be performed from the
initial state. Each operation is performed independently, having a starting
state, an ending state, and a proof that any change or any data provided is
correct according to the starting state for that operation.

The renter can sent a set of operations, all of which must be performed
atomically by the host. If there is any error, the host must abort all of the
operations. The set of operations occurs as a single round trip. The renter
sends a list of operations, the host performs the operations, and then the host
returns any data and any proofs.

Each operation has a cost depending on the resources used. Operations that are
added to the MDM must have a pre-calculateble cost, and must not introduce any
denial of service vectors. Futher, the cost of an operations must be logarithmic
in the size of the contract, and must have a small constant cost factor.

The resources that are considered for the MDM are upload bandwidth to the host,
disk access cost for the host (any seek counts as one access), disk read cost
for the host, proof computation cost, and download bandwidth from the host. The
host has the ability to set prices on all of these resoures.

At any point in time between the renter sending the request and receiving a
response, the renter can send an "abort" signal to the host. The renter will
send the abort signal along with a signed contract that pays for the resources
that were requested but does not otherwise contain any changes.

Calling the machine at all consumes the following resources:

Upload Bandwidth: 4 kib
Disk Access: 0 accesses
Disk Read: 0 bytes
Computation Cost: 1 Hash
Download Bandwidth: 4 kib

The upload bandwidth cost is primarily driven by mandatory padding. The
computation cost is to acknowledge that the CPU is not fully idle. The download
bandwidth is primarily driven by mandatory padding.

## Supported Operations

### Read(offset, len)

Read will read data within the contract starting at the provided offset (zero
indexed), and the read will cover the provided length. 

Upload Bandwidth Cost: 4 kib
Disk Accesses: 1 access
Disk Read: len bytes
Computation Cost: Log2(contractCost/64) Hashes
Download Bandwidth: 'len' bytes + len(proof) + 4 kib

### ReadRoot(root, offset, len)

Read will read data within the contract by seeking to the provided root within
the contract, and then reading data from the root at the provided offset and
len. The offset and len must be fully contained within the root.

Upload Bandwidth Cost: 4 kib
Disk Accesses: 1 access
Disk Read: len bytes
Computation Cost: Log2(contractCost/64) Hashes
Download Bandwidth: 'len' bytes + len(proof) + 4 kib

### Write(data, offset, len)

### WriteRoot(root, data, offset, len)

### CopyOver(size, offset1, offset2)

### AppendBlank()

### Swap(size, offset1, offset2)

### Trim(size)

## Example:

Renter sends data:
    {
	    RPC Name: MerkleDataMachine,
		Actions: []Action{
			Read(x,y),
			Swap(x, y, z),
			Trim(x),
		},
		NewContract: {FileContract},
	}

Host responds:
	{
		Responses: []ActionRepsonse{
			ReadResponse{
				data,
				proof,
			},
			SwapResponse{
				proof,
			},
			TrimResponse{
				proof,
			},
		},
		NewContract: {FileContract},
	}
