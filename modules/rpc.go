package modules

import (
	"gitlab.com/NebulousLabs/Sia/types"
	"time"
)

type (
	// RPCPriceTable contains a list of RPC costs to remain vaild up until the
	// specified expiry timestamp.
	RPCPriceTable struct {
		Costs  map[types.Specifier]types.Currency
		Expiry types.Timestamp
	}
)

var (
	// RPCUpdatePriceTable specifier
	RPCUpdatePriceTable = types.NewSpecifier("UpdatePriceTable")

	// RPCFundEphemeralAccount specifier
	RPCFundEphemeralAccount = types.NewSpecifier("FundEphemeralAcc")
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
)

// NewRPCPriceTable returns an empty RPC price table
func NewRPCPriceTable(expiry time.Time) RPCPriceTable {
	return RPCPriceTable{
		Expiry: types.Timestamp(expiry.Unix()),
		Costs:  make(map[types.Specifier]types.Currency),
	}
}
