package transactions

// TODO: Need to update the connections so that the nodes are connected to
// eachother in the graph given. This can be done using gateway blacklisting,
// which wasn't supported as of this file being implemented.
//
// TODO: We can't actually check if the transaction propagation worked because
// we can't actually see the transactions in the mempool of each node.

import (
	"sync"
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

	// Check that the miner has received the transaction in its unconfirmed set
	// of transactions.
	wg, err := miner.WalletGet()
	if err != nil {
		t.Fatal(err)
	}
	err = build.Retry(50, 100*time.Millisecond, func() error {
		// Check that outgoing siacoins is zero.
		if !wg.UnconfirmedOutgoingSiacoins.IsZero() {
			t.Fatal("outgoing siacoins expected to be zero for miner")
		}
		if wg.UnconfirmedIncomingSiacoins.Cmp(sendAmount) != 0 {
			return errors.New("The miner has not recognized the incoming transaction")
		}
		return nil
	})

	// TODO: Check if the transaction is present in every tpool, because every
	// node is a part of the graph a healthy propagation process should mean
	// that the transaction appears in every transaction pool.

	// Send money from each of the hosts to the miner's address.
	hosts := tg.Hosts()
	for _, host := range hosts {
		wsp, err = host.WalletSiacoinsPost(sendAmount, minerAddr)
		if err != nil {
			t.Fatal(err)
		}
		knownTxids = append(knownTxids, wsp.TransactionIDs...)
	}

	// TODO: Check that every transaction is present in every tpool.

	// TODO: Send money again from the renter to the miner, this time making one
	// hundred consecutive transactions that should chain together, or at least
	// some value that will put a bit more strain on the tpool.

	// Check that the miner has received the transactions in its unconfirmed set
	// of transactions.
	wg, err = miner.WalletGet()
	if err != nil {
		t.Fatal(err)
	}
	err = build.Retry(50, 100*time.Millisecond, func() error {
		// Check that outgoing siacoins is zero.
		if !wg.UnconfirmedOutgoingSiacoins.IsZero() {
			t.Fatal("outgoing siacoins expected to be zero for miner")
		}
		if wg.UnconfirmedIncomingSiacoins.Cmp(sendAmount.Mul64(uint64(len(hosts)+1))) != 0 {
			return errors.New("The miner has not recognized the incoming transaction")
		}
		return nil
	})

	// Disconnect the miner from the renter to force some relay propagation.
	//
	// Start by fetching the number of nodes the miner and renter are connected
	// to, and verifying that the number decreases for each after the disconnect
	// call.
	gg, err := miner.GatewayGet()
	if err != nil {
		t.Fatal(err)
	}
	minerBeforePeers := len(gg.Peers)
	gg, err = renter.GatewayGet()
	if err != nil {
		t.Fatal(err)
	}
	renterBeforePeers := len(gg.Peers)
	err = miner.GatewayDisconnectPost(renter.GatewayAddress())
	if err != nil {
		t.Fatal(err)
	}
	err = build.Retry(50, 100*time.Millisecond, func() error {
		gg, err = miner.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != minerBeforePeers-1 {
			return errors.New("miner did not drop peer")
		}
		gg, err = renter.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != renterBeforePeers-1 {
			return errors.New("miner did not drop peer")
		}
		return nil
	})

	// Send a transaction from the miner to the renter.
	wag, err = renter.WalletAddressGet()
	if err != nil {
		t.Fatal(err)
	}
	renterAddr := wag.Address
	wsp, err = miner.WalletSiacoinsPost(sendAmount, renterAddr)
	if err != nil {
		t.Fatal(err)
	}
	knownTxids = append(knownTxids, wsp.TransactionIDs...)

	// TODO: Check that all of the tpools are in sync at this point.

	// Check that the renter has received the transaction in its unconfirmed set
	// of transactions.
	wg, err = renter.WalletGet()
	if err != nil {
		t.Fatal(err)
	}
	// At this point, the renter has both sent and received 200 siacoins, so the
	// incoming and outgoing siacoins should be equal.
	err = build.Retry(50, 100*time.Millisecond, func() error {
		if wg.UnconfirmedIncomingSiacoins.Cmp(wg.UnconfirmedOutgoingSiacoins) != 0 {
			return errors.New("The renter has not recognized the incoming transaction " + wg.UnconfirmedIncomingSiacoins.String() + " " + wg.UnconfirmedOutgoingSiacoins.String())
		}
		return nil
	})

	// Send several more transactions from the renter to the miner. When this
	// code was written, the renter would start with 25 outputs in its database,
	// so we send 60 transactions to ensure that we're well into the territory
	// of spending unconfirmed change. 60 also has the benefit of being above
	// the defrag limit.
	sends := 60
	for i := 0; i < sends; i++ {
		println("outputs in renter wallet db")
		wsp, err := renter.WalletSiacoinsPost(sendAmount, minerAddr)
		if err != nil {
			t.Fatal(err)
		}
		knownTxids = append(knownTxids, wsp.TransactionIDs...)
	}

	// Check that the miner has received the transaction in its unconfirmed set
	// of transactions.
	wg, err = miner.WalletGet()
	if err != nil {
		t.Fatal(err)
	}
	err = build.Retry(50, 100*time.Millisecond, func() error {
		// Check that outgoing siacoins is zero.
		if wg.UnconfirmedOutgoingSiacoins.Cmp(wg.UnconfirmedIncomingSiacoins) > 0 {
			t.Fatal("the miner should have more incoming siacoins than outgoing siacoins")
		}
		minerBal := wg.UnconfirmedIncomingSiacoins.Sub(wg.UnconfirmedOutgoingSiacoins)
		expectedMinerBal := sendAmount.Mul64(uint64(len(hosts) + sends))
		if minerBal.Cmp(expectedMinerBal) != 0 {
			return errors.New("The miner has not recognized the incoming transaction")
		}
		return nil
	})

	// Verify that the miner and renter are still disconnected from eachother.
	err = build.Retry(50, 100*time.Millisecond, func() error {
		gg, err = miner.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != minerBeforePeers-1 {
			return errors.New("miner did not drop peer")
		}
		gg, err = renter.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != renterBeforePeers-1 {
			return errors.New("miner did not drop peer")
		}
		return nil
	})

	// TODO: Check that all of the tpools are still synced.

	// Restart all of the hosts. This will dump out all of the transactions in
	// their mempools, and test how the nodes behave when the mempools are not
	// equal.
	//
	// TODO: This won't always dump out the mempool, not after persistence has
	// been added.
	var waitGroup sync.WaitGroup
	waitGroup.Add(len(hosts))
	for i := 0; i < len(hosts); i++ {
		go func(i int) {
			defer waitGroup.Done()
			err := hosts[i].StopNode()
			if err != nil {
				t.Error(err)
			}
			err = hosts[i].StartNode()
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	waitGroup.Wait()

	// During the restarting of the hosts, it's highly likely that the miner and
	// renter connected to eachother again. Once a proper blacklist for peers is
	// created, this step won't be necessary but in the meantime we need to
	// disconnect the miner from the renter again.
	//
	// TODO: Eliminate this check via replacing the disconnect with a blacklist
	// ban. The error is currently unchecked because it can return 'not
	// connected to this node'.
	miner.GatewayDisconnectPost(renter.GatewayAddress())

	// Send yet another round of transactions from the renter to the miner. This
	// will have to go through the hosts, who have disconnected from the network
	// and had their mempools cleared.
	//
	// TODO: This won't always dump out the mempool, not after persistence has
	// been added.
	for i := 0; i < sends; i++ {
		println("outputs in renter wallet db")
		wsp, err := renter.WalletSiacoinsPost(sendAmount, minerAddr)
		if err != nil {
			t.Fatal(err)
		}
		knownTxids = append(knownTxids, wsp.TransactionIDs...)
	}

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
