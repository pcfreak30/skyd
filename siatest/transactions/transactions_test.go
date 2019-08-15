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
	"gitlab.com/NebulousLabs/errors"
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

	// Get an address from the miner, the renter will send money to this
	// address.
	miner := tg.Miners()[0]
	wag, err := miner.WalletAddressGet()
	if err != nil {
		t.Fatal(err)
	}
	minerAddr := wag.Address

	// Send money from the renter to the miner's address.
	sendAmount := types.SiacoinPrecision.Mul64(200)
	renter := tg.Renters()[0]
	wsp, err := renter.WalletSiacoinsPost(sendAmount, minerAddr)
	if err != nil {
		t.Fatal(err)
	}
	knownTxids := wsp.TransactionIDs

	// TODO: Check if the transaction is present in every tpool, because every
	// node is a part of the graph a healthy propagation process should mean
	// that the transaction appears in every transaction pool.

	// TODO: Send money from each of the hosts to the miner's address.

	// TODO: Check that every transaction is present in every tpool.

	// TODO: Send money again from the renter to the miner, this time making one
	// hundred consecutive transactions that should chain together, or at least
	// some value that will put a bit more strain on the tpool.

	// Check that the miner has received the transaction in its unconfirmed set
	// of transactions.
	wg, err := miner.WalletGet()
	if err != nil {
		t.Fatal(err)
	}
	// NOTE: UnconfirmedIncomingSiacoins will also include incoming siacoins
	// from change addresses from the miner. This shouldn't be an issue here,
	// because the miner hasn't made any transactions yet, but if the test is
	// adjusted this check may also need to be adjusted.
	err = build.Retry(50, 100*time.Millisecond, func() error {
		if wg.UnconfirmedIncomingSiacoins.Cmp(sendAmount) != 0 {
			return errors.New("The miner has not recognized the incoming transaction")
		}
		return nil
	})

	// Mine a block and see that all of the txids we've collected from the sends
	// have made it into the blockchain.
	err = miner.MineBlock()
	if err != nil {
		t.Fatal(err)
	}
	err = build.Retry(50, 200*time.Millisecond, func() error {
		for _, txid := range knownTxids {
			confirmed, err := miner.TransactionPoolConfirmedGet(txid)
			if err != nil {
				return err
			}
			if !confirmed.Confirmed {
				return errors.New("txid does not seem to be confirmed on the blockchain")
			}
		}
		return nil
	})

	// TODO: Change the connection graph so it looks like the below shape.
	// Currently this isn't possible because the gateway will just reconnect as
	// it tries to maintain its required outbound peers (set to 4 in testing).
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

	// Check the transaction pools of all of the nodes and see whether the nodes
	// have the transaction.
	nodes := tg.Nodes()
	// for _, node := range nodes {
	for range nodes {
		err = build.Retry(50, 100*time.Millisecond, func() error {
			// TODO: Check if the transaction is present in the tpool. This will
			// require adding support to query for the transactions that exist
			// in the tpool.
			return nil
		})
	}
}
