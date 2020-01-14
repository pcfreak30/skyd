package host

// TODO: this is a placeholder that holds an interface for the account manager.
// This file should be removed when EA gets merged.

import (
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
)

type accountManager interface {
	callDeposit(string, types.Currency, chan struct{}) error
	callWithdraw(modules.WithdrawalMessage, crypto.Signature, int64) error
}
