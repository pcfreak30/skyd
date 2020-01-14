package modules

import (
	"errors"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
)

// ErrInsufficientPaymentForRPC is returned when the provided payment was lower
// than the cost of the RPC.
var ErrInsufficientPaymentForRPC = errors.New("Insufficient payment, the provided payment did not cover the cost of the RPC.")

// PaymentProvider is the interface implemented when payment has to be made
// for an RPC call to a host.
type PaymentProvider interface {
	ProvidePaymentForRPC(rpcID types.Specifier, payment types.Currency, stream Stream, blockHeight types.BlockHeight) (types.Currency, error)
}

// PaymentProcessor is the interface implemented when receiving payment for an
// RPC.
type PaymentProcessor interface {
	ProcessFundEphemeralAccountRPC(stream Stream, priceTable RPCPriceTable) (types.Currency, error)
	ProcessPaymentForRPC(stream Stream, priceTable RPCPriceTable) (types.Currency, error)
}

// PaymentProviderFunc is an adapter for the interface. This allows wrapping
// an anonymous function as if it were an object implementing the interface.
type PaymentProviderFunc func(rpcID types.Specifier, payment types.Currency, stream Stream, blockHeight types.BlockHeight) (types.Currency, error)

// ProvidePaymentForRPC implements the interface
func (f PaymentProviderFunc) ProvidePaymentForRPC(rpcID types.Specifier, payment types.Currency, stream Stream, blockHeight types.BlockHeight) (types.Currency, error) {
	return f(rpcID, payment, stream, blockHeight)
}

// Payment identifiers
var (
	PayByContract         = types.NewSpecifier("PayByContract")
	PayByEphemeralAccount = types.NewSpecifier("PayByEphemAcc")
)

// Payment request-response objects
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
