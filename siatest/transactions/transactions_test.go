package transactions

// TODO: Need to update the connections so that the nodes are connected to
// eachother in the graph given. This can be done using gateway blacklisting,
// which wasn't supported as of this file being implemented.
//
// TODO: We can't actually check if the transaction propagation worked because
// we can't actually see the transactions in the mempool of each node.

import (
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/siatest"
	"gitlab.com/NebulousLabs/Sia/types"
)

// TestTransactionPropagation will create a set of nodes and check that the
// nodes can broadcast tranasctions to eachother.
func TestTransactionPropagation(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}

	// Create a group for the test. Hosts are used so that we can try making
	// file contracts as well. We have 7 total nodes because the nodes will
	// connect to eachother until they have 4 outbound peers each.
	gp := siatest.GroupParams{
		Hosts:   6,
		Miners:  1,
		Renters: 1,
	}
	testDir := transactionsTestDir(t.Name())
	tg, err := siatest.NewGroupFromTemplate(testDir, gp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := tg.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	// TODO:
	//
	// Change the connection graph so it looks like:
	//
	// Miner 0
	// |      \
	// |       \
	// Host 0 - Host 1
	// |       /
	// |      /
	// |     /
	// Host 2
	// |
	// |
	// Host 3
	// |     \
	// |      \
	// |       \
	// Host 4 - Host 5
	// |       /
	// Renter 0
	//
	// This graph can be created by using the di

	// Get an address from the miner, the renter will send money to this
	// address.
	miner := tg.Miners()[0]
	wag, err := miner.WalletAddressGet()
	if err != nil {
		t.Fatal(err)
	}
	minerAddr := wag.Address

	// Send money from the renter to the miner's address.
	renter := tg.Renters()[0]
	wsp, err := renter.WalletSiacoinsPost(types.SiacoinPrecision.Mul64(200), minerAddr)
	if err != nil {
		t.Fatal(err)
	}
	// txids := wsp.TransactionIDs
	_ = wsp.TransactionIDs

	// Check the transaction pools of all of the nodes and see whether the nodes
	// have the transaction.
	nodes := tg.Nodes()
	// for _, node := range nodes {
	for _, _ = range nodes {
		err = build.Retry(50, 100*time.Millisecond, func() error {
			// TODO: Check if the transaction is present in the tpool.
			return nil
		})
	}
}
