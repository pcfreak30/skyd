package transactions

// TODO: Integrate the ability to check which transactions are in the
// transaction pool with this test to have better testing coverage of
// propagation.
//
// TODO: Switch from using disconnect commands to do the blacklisting to instead
// using explicit blacklisting endpoints in the API.

import (
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/node"
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

	// Create a renter and a miner that are not connected to eachother. Have the
	// renter send money to the miner's address. After the send is performed,
	// connect the miner to the renter and see whether the transaction
	// propagates and subsequently gets added to a block.
	gp := siatest.GroupParams{
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
	renter := tg.Renters()[0]
	miner := tg.Miners()[0]

	// Curent Topology
	//
	//  miner
	//    |
	//  renter

	// Disconnect the miner from the renter and vice-versa, which will cause the
	// two nodes to blacklist eachother and prevent them from connecting to
	// eachother again.
	gg, err := miner.GatewayGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(gg.Peers) != 1 {
		t.Fatal("miner should only have one peer", len(gg.Peers))
	}
	gg, err = renter.GatewayGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(gg.Peers) != 1 {
		t.Fatal("renter should only have one peer", len(gg.Peers))
	}
	err = miner.GatewayDisconnectPost(renter.GatewayAddress())
	if err != nil {
		t.Fatal(err)
	}
	_ = renter.GatewayDisconnectPost(miner.GatewayAddress())
	// TODO: The error is ignored here because we know there's going to be an
	// error for not being connceted to this peer in the first place.
	err = build.Retry(50, 100*time.Millisecond, func() error {
		gg, err := miner.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != 0 {
			return errors.New("miner did not drop peer")
		}
		gg, err = renter.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != 0 {
			return errors.New("miner did not drop peer")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Curent Topology
	//
	//  miner
	//
	//  -----
	//
	//  renter

	// Get an address from the miner, the renter will send money to this
	// address.
	wag, err := miner.WalletAddressGet()
	if err != nil {
		t.Fatal(err)
	}
	minerAddr := wag.Address

	// Send money from the renter to the miner's address.
	sendAmount := types.SiacoinPrecision.Mul64(200)
	wsp, err := renter.WalletSiacoinsPost(sendAmount, minerAddr)
	if err != nil {
		t.Fatal(err)
	}
	knownTxids := wsp.TransactionIDs

	// Bring up a new node, gateway only, that sits between the renter and the
	// miner.
	relay1Params := node.RelayTemplate
	siatest.RandomNodeDir(tg.Dir(), &relay1Params)
	relay1, err := siatest.NewCleanNode(relay1Params)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := relay1.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()
	err = relay1.GatewayConnectPost(miner.GatewayAddress())
	if err != nil {
		t.Fatal(err)
	}
	err = relay1.GatewayConnectPost(renter.GatewayAddress())
	if err != nil {
		t.Fatal(err)
	}
	err = build.Retry(50, 100*time.Millisecond, func() error {
		gg, err := miner.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != 1 {
			return errors.New("miner should have only one peer (relay1)")
		}
		gg, err = renter.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != 1 {
			return errors.New("renter should have only one peer (relay1")
		}
		gg, err = relay1.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != 2 {
			return errors.New("relay1 should have 2 peers (miner and renter)")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Curent Topology
	//
	//  miner
	//    |
	//  relay1
	//    |
	//  renter

	// Check that the miner has received the transaction in its unconfirmed set
	// of transactions. This will mean that the transaction, sent before the
	// relay1 came online, propagated from the renter to the relay1 to the
	// miner.
	err = build.Retry(200, 250*time.Millisecond, func() error {
		wg, err := miner.WalletGet()
		if err != nil {
			t.Fatal(err)
		}
		// Miner has not made any transactions, outgoing should be zero.
		if !wg.UnconfirmedOutgoingSiacoins.IsZero() {
			t.Fatal("outgoing siacoins expected to be zero for miner")
		}
		// Incoming should be equal to the amount that was sent.
		if wg.UnconfirmedIncomingSiacoins.Cmp(sendAmount) != 0 {
			return errors.New("The miner has not recognized the incoming transaction")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Re-verify the topology.
	err = build.Retry(50, 100*time.Millisecond, func() error {
		gg, err := miner.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != 1 {
			return errors.New("miner should have only one peer (relay1)")
		}
		gg, err = renter.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != 1 {
			return errors.New("renter should have only one peer (relay1")
		}
		gg, err = relay1.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != 2 {
			return errors.New("relay1 should have 2 peers (miner and renter)")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Send several more transactions from the renter to the miner, enough to
	// ensure that some transactions are dependent on other unconfirmed
	// transactions.
	sends := uint64(siatest.OutputsPerNode + 10)
	for i := uint64(0); i < sends; i++ {
		wsp, err := renter.WalletSiacoinsPost(sendAmount, minerAddr)
		if err != nil {
			t.Fatal(err)
		}
		knownTxids = append(knownTxids, wsp.TransactionIDs...)
	}

	// Check that the miner has received the transactions in its unconfirmed set
	// of transactions.
	err = build.Retry(50, 100*time.Millisecond, func() error {
		wg, err := miner.WalletGet()
		if err != nil {
			t.Fatal(err)
		}
		// Check that outgoing siacoins is smaller than incoming siacoins.
		if wg.UnconfirmedOutgoingSiacoins.Cmp(wg.UnconfirmedIncomingSiacoins) > 0 {
			t.Fatal("the miner should have more incoming siacoins than outgoing siacoins")
		}
		minerBal := wg.UnconfirmedIncomingSiacoins.Sub(wg.UnconfirmedOutgoingSiacoins)
		expectedMinerBal := sendAmount.Mul64(sends + 1)
		// Check that the miner has no more than the expected bal.
		if minerBal.Cmp(expectedMinerBal) > 0 {
			return errors.New("the miner has more siacoins than expected")
		}
		// Check that the miner has no less than the expected balance. Add some
		// wiggle room for fees.
		tenSC := types.SiacoinPrecision.Mul64(10)
		if minerBal.Add(tenSC).Cmp(expectedMinerBal) < 0 {
			return errors.New("the miner has fewer siacoins than expected")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Black list the renter from relay1 and vice versa.
	err = renter.GatewayDisconnectPost(relay1.GatewayAddress())
	if err != nil {
		t.Fatal(err)
	}
	_ = relay1.GatewayDisconnectPost(renter.GatewayAddress())
	// TODO: The error is ignored here because we know there's going to be an
	// error for not being connceted to this peer in the first place.

	// Verify that the renter is down to 0 peers.
	gg, err = renter.GatewayGet()
	if len(gg.Peers) != 0 {
		t.Fatal("renter should not have any peers at this time")
	}

	// Bring up another node to sit between the renter and relay1.
	relay2Params := node.RelayTemplate
	siatest.RandomNodeDir(tg.Dir(), &relay2Params)
	relay2, err := siatest.NewCleanNode(relay2Params)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := relay2.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()
	err = relay1.GatewayConnectPost(relay2.GatewayAddress())
	if err != nil {
		t.Fatal(err)
	}
	err = relay2.GatewayConnectPost(renter.GatewayAddress())
	if err != nil {
		t.Fatal(err)
	}
	err = build.Retry(50, 100*time.Millisecond, func() error {
		gg, err := miner.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != 1 {
			return errors.New("miner should have only one peer (relay1)")
		}
		gg, err = renter.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != 1 {
			return errors.New("renter should have only one peer (relay2")
		}
		gg, err = relay1.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != 2 {
			return errors.New("relay1 should have 2 peers (miner and relay2)")
		}
		gg, err = relay2.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(gg.Peers) != 2 {
			return errors.New("relay1 should have 2 peers (relay1 and renter)")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Curent Topology
	//
	//  miner
	//    |
	//  relay1
	//    |
	//  relay2
	//    |
	//  renter

	// TODO: This sleep is in place because the renter is not able to wait until
	// the new peer is synced to start sending the peer transactions. This sleep
	// should be safe to remove once the mechanism to wait for a peer to be
	// synced is in place.
	time.Sleep(time.Second * 10)

	// Sent a new transaction from the renter to the miner. This transaction
	// should reach the miner.
	wsp, err = renter.WalletSiacoinsPost(sendAmount, minerAddr)
	if err != nil {
		t.Fatal(err)
	}
	knownTxids = append(knownTxids, wsp.TransactionIDs...)

	// Check that the miner has received the transactions in its unconfirmed set
	// of transactions.
	err = build.Retry(250, 100*time.Millisecond, func() error {
		wg, err := miner.WalletGet()
		if err != nil {
			t.Fatal(err)
		}
		// Check that outgoing siacoins is smaller than incoming siacoins.
		if wg.UnconfirmedOutgoingSiacoins.Cmp(wg.UnconfirmedIncomingSiacoins) > 0 {
			t.Fatal("the miner should have more incoming siacoins than outgoing siacoins")
		}
		minerBal := wg.UnconfirmedIncomingSiacoins.Sub(wg.UnconfirmedOutgoingSiacoins)
		expectedMinerBal := sendAmount.Mul64(sends + 2)
		// Check that the miner has no more than the expected bal.
		if minerBal.Cmp(expectedMinerBal) > 0 {
			return errors.New("the miner has more siacoins than expected")
		}
		// Check that the miner has no less than the expected balance. Add some
		// wiggle room for fees.
		tenSC := types.SiacoinPrecision.Mul64(10)
		if minerBal.Add(tenSC).Cmp(expectedMinerBal) < 0 {
			return errors.New("the miner has fewer siacoins than expected")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// TODO: Restart both relay1 and relay2. Then send a transaction from the
	// renter to the miner. The transaction should reach the miner.

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
	if err != nil {
		t.Fatal(err)
	}

	// TODO: Send more transactions, and then disconnect the miner so that when
	// it mines a block, its tpool is different from everyone else. Have another
	// node mine an empty block, and then see how propagation works betwen the
	// nodes. Check that if there are small time reorgs, the transaction pools
	// are still able to stay syned and transactions can still get around the
	// network.
	//
	// Try to experiment with all of the common scenarios on the network where
	// various peers are either going to be ahead or behind eachother, and make
	// sure that all common situations do not impede txn propagation.

	// TODO: Eventually play with more advanced topologies such as the one
	// below.
	//
	//  Miner 0
	// |       \
	// |        \
	//  Host 0 - Host 1
	// |        /
	// |       /
	// |      /
	//  Host 2
	//    |
	//    |
	//  Host 3
	// |      \
	// |       \
	// |        \
	//  Host 4 - Host 5
	// |        /
	//  Renter 0

	// TODO: Play with situations where transactions from multiple groups are
	// all propagating simultaneously, sometimes with conflicts.

	// TODO: Start playing around with double spends.
}
