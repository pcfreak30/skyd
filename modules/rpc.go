package modules

import (
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
)

const (
	// SpecifierLen is the length in bytes of the RPCPriceTableSpecifier
	SpecifierLen = 8
)

type (
	// RPCPriceTableSpecifier uniquely identifies a price table.
	RPCPriceTableSpecifier [SpecifierLen]byte

	// RPCPriceTable contains a list of costs associated to RPCs. It is uniquely
	// identified by its uuid, and is given out by the host which guarantees the
	// listed costs up until the expiry timestamp.
	RPCPriceTable struct {
		UUID   RPCPriceTableSpecifier
		Costs  map[types.Specifier]types.Currency
		Expiry int64
	}
)

var (
	// RPCUpdatePriceTable specifier
	RPCUpdatePriceTable = types.NewSpecifier("UpdatePriceTable")

	// RPCFundEphemeralAccount specifier
	RPCFundEphemeralAccount = types.NewSpecifier("FundEphemeralAcc")

	// RPCExecuteMDMProgram specifier
	RPCExecuteMDMProgram = types.NewSpecifier("ExecMDMProgram")
)

type (
	// RPCUpdatePriceTableResponse contains a JSON encoded price table.
	RPCUpdatePriceTableResponse struct {
		PriceTableJSON []byte
	}

	// RPCFundEphemeralAccountRequest specifies the ephemeral account id.
	RPCFundEphemeralAccountRequest struct {
		AccountID string
	}

	// RPCFundEphemeralAccountResponse contains the signature. This signature
	// is a signed receipt, and can be used as proof of payment.
	RPCFundEphemeralAccountResponse struct {
		Receipt   Receipt
		Signature []byte
	}

	// RPCExecuteProgramRequest contains the filecontract ID on which to execute
	// the program.
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

// Clone returns a deep copy of the rpc price table with an updated expiry, the
// host will call this function on its price table every time it hands out a
// price table to the renter.
func (pt *RPCPriceTable) Clone(expiry int64) *RPCPriceTable {
	cloned := NewRPCPriceTable(expiry)
	for k, v := range pt.Costs {
		cloned.Costs[k] = v
	}
	fastrand.Read(cloned.UUID[:])
	return &cloned
}
