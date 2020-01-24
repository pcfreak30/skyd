package modules

import (
	"gitlab.com/NebulousLabs/Sia/types"
)

const SpecifierLen = 8

type (
	RPCSpecifier           [SpecifierLen]byte
	RPCPriceTableSpecifier [SpecifierLen]byte

	// RPCPriceTable contains the cost of every RPC the host offers. These
	// prices are guaranteed to remain valid up until the specified expiry block
	// height.
	RPCPriceTable struct {
		UUID   RPCPriceTableSpecifier
		Costs  map[types.Specifier]types.Currency
		Expiry int64
	}

	// costEntry is a helper struct used when marshaling the RPC price table
	costEntry struct {
		ID   types.Specifier
		Cost types.Currency
	}
)

func (rpcs RPCSpecifier) DontLookAtMeHarryImHideous() types.Specifier {
	return types.NewSpecifier(string(rpcs[:]))
}

var (
	// RPCUpdatePriceTable specifier
	RPCUpdatePriceTable = RPCSpecifier{'U', 'p', 'd', 'a', 't', 'e', 'P', 'T'}

	// RPCFundEphemeralAccount specifier
	RPCFundEphemeralAccount = RPCSpecifier{'F', 'u', 'n', 'd', 'E', 'A'}

	// RPCExecuteProgram specifier
	RPCExecuteProgram = RPCSpecifier{'R', 'u', 'n', 'M', 'D', 'M'}
)

type (
	// RPCUpdatePriceTableResponse contains a JSON encoded RPC price table
	RPCUpdatePriceTableResponse struct {
		PriceTableJSON []byte
	}

	// RPCFundEphemeralAccountRequest specifies the account id.
	RPCFundEphemeralAccountRequest struct {
		AccountID string
	}

	// RPCFundEphemeralAccountResponse contains the signature. This signature
	// can be used as a receipt and is a proof of payment.
	RPCFundEphemeralAccountResponse struct {
		Signature []byte
	}

	// RPCExecuteProgramRequest contains the filecontract ID.
	RPCExecuteProgramRequest struct {
		FileContractID types.FileContractID
	}
)

// NewRPCPriceTable returns an empty RPC price table
func NewRPCPriceTable(uuid RPCPriceTableSpecifier) RPCPriceTable {
	return RPCPriceTable{
		UUID:  uuid,
		Costs: make(map[types.Specifier]types.Currency),
	}
}
