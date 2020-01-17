package modules

import (
	"net"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

var (
	// ErrUnknownPaymentMethod occurs when the payment method specified in the
	// PaymentRequest object is unknown. The possible options are outlined below
	// under "Payment identifiers".
	ErrUnknownPaymentMethod = errors.New("unknown payment method")

	// ErrInvalidPaymentMethod occurs when the payment method is not accepted
	// for a specific RPC.
	ErrInvalidPaymentMethod = errors.New("invalid payment method")

	// ErrInsufficientPaymentForRPC is returned when the provided payment was
	// lower than the cost of the RPC.
	ErrInsufficientPaymentForRPC = errors.New("Insufficient payment, the provided payment did not cover the cost of the RPC.")
)

// PaymentProvider is the interface implemented when payment has to be made
// for an RPC call to a host.
type PaymentProvider interface {
	ProvidePaymentForRPC(rpcID types.Specifier, payment types.Currency, stream net.Conn, blockHeight types.BlockHeight) (types.Currency, error)
}

// PaymentProcessor is the interface implemented when receiving payment for an
// RPC.
type PaymentProcessor interface {
	ProcessFundEphemeralAccountRPC(stream net.Conn, pt RPCPriceTable) (types.Currency, error)
	ProcessPaymentForRPC(stream net.Conn) (types.Currency, error)
}

// PaymentProviderFunc is an adapter for the interface. This allows wrapping
// an anonymous function as if it were an object implementing the interface.
type PaymentProviderFunc func(rpcID types.Specifier, payment types.Currency, stream net.Conn, blockHeight types.BlockHeight) (types.Currency, error)

// ProvidePaymentForRPC implements the interface
func (f PaymentProviderFunc) ProvidePaymentForRPC(rpcID types.Specifier, payment types.Currency, stream net.Conn, blockHeight types.BlockHeight) (types.Currency, error) {
	return f(rpcID, payment, stream, blockHeight)
}

// Payment identifiers
var (
	PayByContract         = types.NewSpecifier("PayByContract")
	PayByEphemeralAccount = types.NewSpecifier("PayByEphemAcc")
)

type (
	// PaymentRequest identifies the payment method. This can be either
	// PayByContract or PayByEphemeralAccount
	PaymentRequest struct {
		Type types.Specifier
	}

	// PayByEphemeralAccountRequest holds all payment details to pay from an
	// ephemeral account.
	PayByEphemeralAccountRequest struct {
		Message   WithdrawalMessage
		Signature crypto.Signature
		Priority  int64
	}

	// PayByEphemeralAccountResponse is the object sent in response to the
	// PayByEphemeralAccountRequest
	PayByEphemeralAccountResponse struct {
		Amount                 types.Currency
		AcceptRejectMessage    string
		AccountManagerResponse error
	}

	// PayByContractRequest holds all payment details to pay from a file
	// contract.
	PayByContractRequest struct {
		ContractID           types.FileContractID
		NewRevisionNumber    uint64
		NewValidProofValues  []types.Currency
		NewMissedProofValues []types.Currency
		Signature            []byte
	}

	// PayByContractResponse is the object sent in response to the
	// PayByContractRequest
	PayByContractResponse struct {
		Amount              types.Currency
		AcceptRejectMessage string
		Signature           crypto.Signature
	}

	// WithdrawalMessage contains all details to spend from an ephemeral account
	WithdrawalMessage struct {
		Account string
		Expiry  types.BlockHeight
		Amount  types.Currency
		Nonce   []byte
	}
)
