package modules

import (
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
)

// SpecifierLen is the length of both the RPCSpecifier and
// RPCPriceTableSpecifier types.
const SpecifierLen = 8

type (
	// RPCSpecifier is the specifier sent at the beginning of every RPC.
	RPCSpecifier [SpecifierLen]byte

	// RPCPriceTableSpecifier uniquely identifies a price table.
	RPCPriceTableSpecifier [SpecifierLen]byte

	// RPCPriceTable contains a list of RPC costs to remain vaild up until the
	// specified expiry timestamp.
	RPCPriceTable struct {
		UUID   RPCPriceTableSpecifier
		Costs  map[types.Specifier]types.Currency
		Expiry int64
	}
)

// DontLookAtMeHarryImHideous converts a RPCSpecifier into a types.Specifier.
// TODO: fix this.
func (rpcs RPCSpecifier) DontLookAtMeHarryImHideous() (specifier types.Specifier) {
	copy(specifier[:], rpcs[:])
	return
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
func NewRPCPriceTable(expiry int64) RPCPriceTable {
	pt := RPCPriceTable{
		Expiry: expiry,
		Costs:  make(map[types.Specifier]types.Currency),
	}
	fastrand.Read(pt.UUID[:])
	return pt
}
