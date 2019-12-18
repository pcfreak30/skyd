package host

// NOTE: this file contains a placeholder interface for the account manager.
// This file should be removed when EA is merged.

import (
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
)

type accountManager interface {
	callDeposit(id string, amount types.Currency) error
	callCommitDeposit(amount types.Currency)
	callWithdraw(modules.WithdrawalMessage, crypto.Signature, int64) error
}
