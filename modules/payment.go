package modules

import (
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
)

// PaymentProvider is the interface implemented when payment has to be made
// for an RPC call to a host.
type PaymentProvider interface {
	ProvidePaymentForRPC(rpcID types.Specifier, payment types.Currency, stream Stream, currentBlockHeight types.BlockHeight) (types.Currency, error)
}

// PaymentExtractor is the interface implemented when receiving payment for
// an RPC.
type PaymentExtractor interface {
	ExtractPaymentForRPC(stream Stream, currentBlockHeight types.BlockHeight) (types.Currency, bool, error)
}

// PaymentProviderFunc is an adapter for the interface. This allows wrapping
// an anonymous function as if it were an object implementing the interface.
type PaymentProviderFunc func(rpcID types.Specifier, payment types.Currency, stream Stream, currentBlockHeight types.BlockHeight) (types.Currency, error)

// ProvidePaymentForRPC implements the interface
func (f PaymentProviderFunc) ProvidePaymentForRPC(rpcID types.Specifier, payment types.Currency, stream Stream, currentBlockHeight types.BlockHeight) (types.Currency, error) {
	return f(rpcID, payment, stream, currentBlockHeight)
}

// Extract payment identifiers
var (
	PayByContract         = types.NewSpecifier("PayByContract")
	PayByEphemeralAccount = types.NewSpecifier("PayByEphemAcc")
)

// Extract payment request-response objects
type (
	PaymentRequest struct {
		Type types.Specifier
	}

	PayByEphemeralAccountRequest struct {
		Message   WithdrawalMessage
		Signature crypto.Signature
		Priority  int64
	}

	PayByEphemeralAccountResponse struct {
		Amount                 types.Currency
		AcceptRejectMessage    string
		AccountManagerResponse error
	}

	PayByContractRequest struct {
		ContractID           types.FileContractID
		NewRevisionNumber    uint64
		NewValidProofValues  []types.Currency
		NewMissedProofValues []types.Currency
		Signature            []byte
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
