package mdm

import (
	"fmt"

	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/encoding"
	"gitlab.com/skynetlabs/skyd/skymodules"
)

// instructionRevision returns the FileContractRevision returned by this MDM
// program.
type instructionRevision struct {
	commonInstruction
}

// staticDecodeRevisionInstruction creates a new 'Revision' instruction from
// the provided generic instruction.
func (p *program) staticDecodeRevisionInstruction(instruction skymodules.Instruction) (instruction, error) {
	// Check specifier.
	if instruction.Specifier != skymodules.SpecifierRevision {
		return nil, fmt.Errorf("expected specifier %v but got %v",
			skymodules.SpecifierRevision, instruction.Specifier)
	}
	// Check args.
	if len(instruction.Args) != skymodules.RPCIRevisionLen {
		return nil, fmt.Errorf("expected instruction to have len %v but was %v",
			skymodules.RPCIRevisionLen, len(instruction.Args))
	}
	return &instructionRevision{
		commonInstruction: commonInstruction{
			staticData:        p.staticData,
			staticMerkleProof: false,
			staticState:       p.staticProgramState,
		},
	}, nil
}

// Batch declares whether or not this instruction can be batched together with
// the previous instruction.
func (i instructionRevision) Batch() bool {
	return false
}

// Collateral is zero for the Revision instruction.
func (i *instructionRevision) Collateral() types.Currency {
	return skymodules.MDMRevisionCollateral()
}

// Cost returns the cost of executing this instruction.
func (i *instructionRevision) Cost() (executionCost, _ types.Currency, err error) {
	executionCost = skymodules.MDMRevisionCost(i.staticState.priceTable)
	return
}

// Memory returns the memory allocated by this instruction beyond the end of its
// lifetime.
func (i *instructionRevision) Memory() uint64 {
	return skymodules.MDMRevisionMemory()
}

// Execute executes the 'Revision' instruction.
func (i *instructionRevision) Execute(prevOutput output) (output, types.Currency) {
	// Fetch the requested information.
	revTxn := i.staticState.staticRevisionTxn

	return output{
		NewSize:       prevOutput.NewSize,       // size stays the same
		NewMerkleRoot: prevOutput.NewMerkleRoot, // root stays the same
		Output: encoding.Marshal(skymodules.MDMInstructionRevisionResponse{
			RevisionTxn: revTxn,
		}),
	}, types.ZeroCurrency
}

// Time returns the execution time of a 'Revision' instruction.
func (i *instructionRevision) Time() (uint64, error) {
	return skymodules.MDMTimeRevision, nil
}
