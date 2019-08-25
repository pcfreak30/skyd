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

	// TODO: Blacklist the renter from relay1. Bring up another middle node to
	// sit between relay1 and the renter. Before bringing up the middle node,
	// confirm that the renter has 0 peers, and that relay1 and the miner each
	// have only 1 peer (eachother).

	// Curent Topology
	//
	//  miner
	//    |
	//  relay1
	//    |
	//  relay2
	//    |
	//  renter

	// TODO: Verify topology.

	// TODO: Immediately after connecting the renter to relay2, send a
	// transaction from the renter to the miner. This transaction should reach
	// the miner and be reflected in the miner's balance.

	// TODO: Send enough transactions from the renter to the miner that all of
	// the renter's confirmed outputs have been consumed.

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

	/*

		// TODO: Check if the transaction is present in every tpool.

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

		// Check that the miner has received the transactions in its unconfirmed set
		// of transactions.
		err = build.Retry(100, 100*time.Millisecond, func() error {
			wg, err := miner.WalletGet()
			if err != nil {
				t.Fatal(err)
			}
			// Check that outgoing siacoins is zero.
			if !wg.UnconfirmedOutgoingSiacoins.IsZero() {
				t.Fatal("outgoing siacoins expected to be zero for miner")
			}
			expectedBal := sendAmount.Mul64(uint64(len(hosts) + 1))
			if wg.UnconfirmedIncomingSiacoins.Cmp(expectedBal) != 0 {
				return errors.New("The miner has not recognized the incoming transaction\n" + wg.UnconfirmedIncomingSiacoins.Div(types.SiacoinPrecision).String() + "\n" + expectedBal.Div(types.SiacoinPrecision).String())
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

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

		// TODO: Check that all of the tpools have all the transactions.

		// At this point, the renter has both sent and received 200 siacoins, so the
		// incoming and outgoing siacoins should be equal.
		err = build.Retry(100, 100*time.Millisecond, func() error {
			// Check that the renter has received the transaction in its unconfirmed set
			// of transactions.
			wg, err := renter.WalletGet()
			if err != nil {
				t.Fatal(err)
			}
			// The balance will not be perfect becuase of miner fees that the renter
			// has paid, but it should be within 10 siacoins, and the renter should
			// be net negative.
			uis := wg.UnconfirmedIncomingSiacoins
			uos := wg.UnconfirmedOutgoingSiacoins
			if uis.Cmp(uos) > 0 {
				return errors.New("the renter is net positive, but should be down miner fees")
			}
			outgoingTotal := uos.Sub(uis)
			tenSC := types.SiacoinPrecision.Mul64(10)
			if outgoingTotal.Cmp(tenSC) > 0 {
				return errors.New("the renter has more than 10 siacoins outgoing on net " + outgoingTotal.Div(types.SiacoinPrecision).String())
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		// Send several more transactions from the renter to the miner, enough to
		// ensure that some transactions are dependent on other unconfirmed
		// transactions.
		sends := siatest.TransactionsPerNode + 10
		for i := 0; i < sends; i++ {
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
			expectedMinerBal := sendAmount.Mul64(uint64(len(hosts) + sends))
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

		// Verify that the miner and renter are still disconnected from eachother.
		//
		// TODO: This check won't be necessary once there is a blacklist in place.
		err = build.Retry(50, 100*time.Millisecond, func() error {
			gg, err := miner.GatewayGet()
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
		if err != nil {
			t.Fatal(err)
		}

		// TODO: Check that all of the tpools have all of the transactions.

		// Restart all of the hosts and test how the nodes behave when the mempools
		// are not equal.
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
		for i := 0; i < sends; i++ {
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
				return errors.New("the miner should have more incoming siacoins than outgoing siacoins")
			}
			minerBal := wg.UnconfirmedIncomingSiacoins.Sub(wg.UnconfirmedOutgoingSiacoins)
			expectedMinerBal := sendAmount.Mul64(uint64(len(hosts) + sends*2))
			// Check that the miner has no more than the expected bal.
			if minerBal.Cmp(expectedMinerBal) > 0 {
				return errors.New("the miner has more siacoins than expected")
			}
			// Check that the miner has no less than the expected balance. Add some
			// wiggle room for fees.
			tenSC := types.SiacoinPrecision.Mul64(20)
			if minerBal.Add(tenSC).Cmp(expectedMinerBal) < 0 {
				return errors.New("the miner has fewer siacoins than expected")
			}
			return nil
		})
		if err != nil {
			t.Error(err)
		}

		// Shut down all of the nodes except the miner, so that only the miner sees
		// the block that is mined.
		err = renter.StopNode()
		if err != nil {
			t.Fatal(err)
		}
		for _, host := range hosts {
			err = host.StopNode()
			if err != nil {
				t.Fatal(err)
			}
		}

		// TODO: Send a large number of transactions from the miner to the renter.
		// Save the transactions so we can broadcast them later. These will be
		// created before the block, so that they get confirmed in the block that
		// the other nodes can't see.

		// TODO: Send a large number of transactions from the miner to the renter.
		// Save the transactions so we can broadcast them later. These are
		// transactions which are uncomfirmed even after the block was created.

		// Shut down the miner. This will allow us to bring back up the renter and
		// hosts without them seeing the block.
		err = miner.StopNode()
		if err != nil {
			t.Fatal(err)
		}

		// Bring back up the renter and hosts and wait for them to reconnect.
		err = renter.StartNode()
		if err != nil {
			t.Fatal(err)
		}
		for _, host := range hosts {
			err = host.StartNode()
			if err != nil {
				t.Fatal(err)
			}
		}
		err = build.Retry(50, 100*time.Millisecond, func() error {
			gg, err := renter.GatewayGet()
			if err != nil {
				t.Fatal(err)
			}
			if len(gg.Peers) < 4 {
				return errors.New("renter has not reconnected")
			}
			for _, host := range hosts {
				gg, err = host.GatewayGet()
				if err != nil {
					t.Fatal(err)
				}
				if len(gg.Peers) < 4 {
					return errors.New("renter has not reconnected")
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		// TODO: Send more transactions from the renter to the miner. These will
		// happen outside of the block because the miner is offline. So the renter
		// and hosts will get transactions in their tpools that will be part of sets
		// that will get partially confirmed when the miner comes back online.

		// TODO: Broadcast the transactions from the miner to the renter and the
		// hosts. These transactions will be building on top of a block that is
		// already confirmed, which can cause issues.

		// TODO: Change the connection graph so it looks like the below shape.
		// Currently this isn't possible because the gateway will just reconnect as
		// it tries to maintain its required outbound peers (set to 4 in testing).
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
	*/
}
