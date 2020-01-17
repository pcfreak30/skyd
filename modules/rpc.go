package modules

import (
	"gitlab.com/NebulousLabs/Sia/types"
)

type (
	// RPCPriceTable contains the cost of every RPC the host offers. These
	// prices are guaranteed to remain valid up until the specified expiry block
	// height.
	RPCPriceTable struct {
		Costs  map[types.Specifier]types.Currency
		Expiry types.BlockHeight
	}
)

// RPC identifiers
var (
	RPCFundEphemeralAccount = types.NewSpecifier("FundEphemeralAcc")
)

type (
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
