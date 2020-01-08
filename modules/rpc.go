package modules

import (
	"gitlab.com/NebulousLabs/Sia/types"
)

type (
	// The RPCPriceTable lists all the RPCs the host offers and their price. The
	// prices remain valid up until the expiry block height.
	RPCPriceTable struct {
		Costs  map[types.Specifier]types.Currency
		Expiry types.BlockHeight
	}
)

// RPC identifiers
var (
	RPCFundEphemeralAccount = types.NewSpecifier("FundEphemeralAcc")
	RPCUpdatePriceTable     = types.NewSpecifier("UpdatePriceTable")
)

// RPC request-response objects
type (
	// RPCFundEphemeralAccountRequest
	RPCFundEphemeralAccountRequest struct {
		AccountID string
	}

	// RPCFundEphemeralAccountResponse
	RPCFundEphemeralAccountResponse struct {
		Signature []byte // receipt
	}

	RPCUpdatePriceTableResponse struct {
		PriceTableJSON []byte
	}
)
