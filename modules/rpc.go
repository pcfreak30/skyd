package modules

import (
	"gitlab.com/NebulousLabs/Sia/types"
)

const RemoteRPCLen = 48

type (
	// The RPCPriceTable lists all the RPCs the host offers and their price. The
	// prices remain valid up until the expiry block height.
	RPCPriceTable struct {
		Costs  map[RemoteRPCID]types.Currency
		Expiry types.BlockHeight
	}

	// RemoteRPCID is a fixed-length 48 byte-array which uniquely identifies an
	// RPC on a host (HostID|RPCID)
	RemoteRPCID = [RemoteRPCLen]byte
)

// NewRemoteRPCID takes a host public key and an rpc identifier and joins them
// into a remote RPC identifier
func NewRemoteRPCID(host types.SiaPublicKey, rpcID types.Specifier) RemoteRPCID {
	var id RemoteRPCID
	copy(id[:], host.Key[:])
	copy(id[:], rpcID[:])
	return id
}

// RPC identifiers
var (
	RPCFundEphemeralAccount = types.NewSpecifier("FundEphemeralAcc")
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
)
