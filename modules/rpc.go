package modules

import (
	"gitlab.com/NebulousLabs/Sia/types"
)

type (
	// The PriceTable lists all the RPCs the host offers and their price. The
	// prices remain valid up until the expiry block height.
	PriceTable struct {
		Costs  map[types.Specifier]types.Currency
		Expiry types.BlockHeight
	}
)

// RPC identifiers
var (
	RPCDownload2            = types.NewSpecifier("Download2")
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
