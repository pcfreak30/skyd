package feemanager

import (
	"path/filepath"
	"time"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/skynetlabs/skyd/build"
	"gitlab.com/skynetlabs/skyd/skymodules"
	"gitlab.com/skynetlabs/skyd/skymodules/consensus"
	"gitlab.com/skynetlabs/skyd/skymodules/gateway"
	"gitlab.com/skynetlabs/skyd/skymodules/transactionpool"
	"gitlab.com/skynetlabs/skyd/skymodules/wallet"
)

// addRandomFee will add a random fee to the FeeManager
func addRandomFee(fm *FeeManager) (skymodules.FeeUID, error) {
	fee := randomFee()
	uid, err := fm.AddFee(fee.Address, fee.Amount, fee.AppUID, fee.Recurring)
	if err != nil {
		return "", err
	}
	return uid, nil
}

// addRandomFees will add a random number of fees to the FeeManager, always at
// least 1.
func addRandomFees(fm *FeeManager) ([]skymodules.FeeUID, error) {
	return addRandomFeesN(fm, fastrand.Intn(5)+1)
}

// addRandomFeesN will add N number of fees to the FeeManager
func addRandomFeesN(fm *FeeManager, n int) ([]skymodules.FeeUID, error) {
	var uids []skymodules.FeeUID
	for i := 0; i < n; i++ {
		uid, err := addRandomFee(fm)
		if err != nil {
			return nil, err
		}
		uids = append(uids, uid)
	}
	return uids, nil
}

// randomFee creates and returns a fee with random values
func randomFee() skymodules.AppFee {
	randBytes := fastrand.Bytes(16)
	var uh types.UnlockHash
	copy(uh[:], randBytes)
	return skymodules.AppFee{
		Address:            uh,
		Amount:             types.NewCurrency64(fastrand.Uint64n(1e9)),
		AppUID:             skymodules.AppUID(uniqueID()),
		PaymentCompleted:   fastrand.Intn(2) == 0,
		PayoutHeight:       types.BlockHeight(fastrand.Uint64n(1e9)),
		Recurring:          fastrand.Intn(2) == 0,
		Timestamp:          time.Now().Unix(),
		TransactionCreated: fastrand.Intn(2) == 0,
		FeeUID:             uniqueID(),
	}
}

// newTestingFeeManager creates a FeeManager for testing
func newTestingFeeManager(testName string) (*FeeManager, error) {
	// Create testdir
	testDir := build.TempDir("feemanager", testName)

	// Create Dependencies
	cs, tp, w, err := testingDependencies(testDir)
	if err != nil {
		return nil, err
	}

	// Return FeeManager
	return NewCustomFeeManager(cs, tp, w, filepath.Join(testDir, skymodules.FeeManagerDir), skymodules.ProdDependencies)
}

// testingDependencies creates the dependencies needed for the FeeManager
func testingDependencies(testdir string) (skymodules.ConsensusSet, skymodules.TransactionPool, skymodules.Wallet, error) {
	// Create a gateway
	g, err := gateway.New("localhost:0", false, filepath.Join(testdir, skymodules.GatewayDir))
	if err != nil {
		return nil, nil, nil, err
	}
	// Create a consensus set
	cs, errChan := consensus.New(g, false, filepath.Join(testdir, skymodules.ConsensusDir))
	if err := <-errChan; err != nil {
		return nil, nil, nil, err
	}
	// Create a tpool
	tp, err := transactionpool.New(cs, g, filepath.Join(testdir, skymodules.TransactionPoolDir))
	if err != nil {
		return nil, nil, nil, err
	}
	// Create a wallet and unlock it
	w, err := wallet.New(cs, tp, filepath.Join(testdir, skymodules.WalletDir))
	if err != nil {
		return nil, nil, nil, err
	}
	key := crypto.GenerateSiaKey(crypto.TypeDefaultWallet)
	_, err = w.Encrypt(key)
	if err != nil {
		return nil, nil, nil, err
	}
	err = w.Unlock(key)
	if err != nil {
		return nil, nil, nil, err
	}

	return cs, tp, w, nil
}
