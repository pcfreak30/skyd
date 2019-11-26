package modules

import (
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
)

// Extract payment identifiers
var (
	PayByContract         = newSpecifier("PayByContract")
	PayByEphemeralAccount = newSpecifier("PayByEphemeralAcc")
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
		Id     types.SiaPublicKey
		Expiry types.BlockHeight
		Amount types.Currency
		Nonce  int
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
)

// newSpecifier takes in a name and returns a fixed-length byte-array specifier
func newSpecifier(name string) types.Specifier {
	if len(name) > 16 {
		panic("ERROR: specifier max length exceeded")
	}
	var s types.Specifier
	copy(s[:], name)
	return s
}
