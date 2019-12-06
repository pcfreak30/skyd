package modules

import (
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
)

// RPCPaymentProvider is the interface implemented when payment has to be made
// for an RPC call to a host.
type RPCPaymentProvider interface {
	ProvidePaymentForRPC(rpcID types.Specifier, cost types.Currency, stream Stream, currentBlockHeight types.BlockHeight) (types.Currency, error)
}

// RPCPaymentExtractor is the interface implemented when receiving payment for
// an RPC.
type RPCPaymentExtractor interface {
	ExtractPaymentForRPC(stream Stream) (types.Currency, bool, error)
}

// RPCPaymentProviderFunc is an adapter for the interface. This allows wrapping
// an anonymous function as if it were an object implementing the interface.
type RPCPaymentProviderFunc func(rpcID types.Specifier, cost types.Currency, stream Stream, currentBlockHeight types.BlockHeight) (types.Currency, error)

// ProvidePaymentForRPC implements the interface
func (f RPCPaymentProviderFunc) ProvidePaymentForRPC(rpcID types.Specifier, cost types.Currency, stream Stream, currentBlockHeight types.BlockHeight) (types.Currency, error) {
	return f(rpcID, cost, stream, currentBlockHeight)
}

// Extract payment identifiers
var (
	PayByContract         = newSpecifier("PayByContract")
	PayByEphemeralAccount = newSpecifier("PayByEphemAcc")
)

// Extract payment request-response objects
type (
	PaymentRequest struct {
		Type types.Specifier
	}

	PayByEphemeralAccountRequest struct {
		Message   WithdrawalMessage
		Signature crypto.Signature
	}

	PayByEphemeralAccountResponse struct {
		Amount                 types.Currency
		AcceptRejectMessage    string
		AccountManagerResponse error
	}

	PayByContractRequest struct {
		Revision  types.FileContractRevision
		Signature crypto.Signature
	}

	PayByContractResponse struct {
		Amount              types.Currency
		AcceptRejectMessage string
		Signature           crypto.Signature
	}

	// WithdrawalMessage contains all details to spend from an ephemeral account
	WithdrawalMessage struct {
		Id     string
		Expiry types.BlockHeight
		Amount types.Currency
		Nonce  []byte
	}
)

// RPC identifiers
var (
	RPCFundEphemeralAccount = newSpecifier("FundEphemeralAcc")
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

	// RPCEphemeralAccountBalanceRequest
	RPCEphemeralAccountBalanceRequest struct {
		AccountID string
	}

	// RPCEphemeralAccountBalanceResponse
	RPCEphemeralAccountBalanceResponse struct {
		Balance types.Currency
	}
)

// TODO: replace this with new specifier.go (yet to be merged)
// newSpecifier takes in a name and returns a fixed-length byte-array specifier
func newSpecifier(name string) types.Specifier {
	if len(name) > 16 {
		panic("ERROR: specifier max length exceeded")
	}
	var s types.Specifier
	copy(s[:], name)
	return s
}

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
