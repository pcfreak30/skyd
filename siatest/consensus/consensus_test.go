package consensus

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/node"
	"gitlab.com/NebulousLabs/Sia/siatest"
	"gitlab.com/NebulousLabs/Sia/types"
)

// TestApiHeight checks if the consensus api endpoint works
func TestApiHeight(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	testDir := consensusTestDir(t.Name())

	// Create a new server
	testNode, err := siatest.NewNode(node.AllModules(testDir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := testNode.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Send GET request
	cg, err := testNode.ConsensusGet()
	if err != nil {
		t.Fatal(err)
	}
	height := cg.Height

	// Mine a block
	if err := testNode.MineBlock(); err != nil {
		t.Fatal(err)
	}

	// Request height again and check if it increased
	cg, err = testNode.ConsensusGet()
	if err != nil {
		t.Fatal(err)
	}
	if cg.Height != height+1 {
		t.Fatal("Height should have increased by 1 block")
	}
}

// TestConsensusBlocksIDGet tests the /consensus/blocks endpoint
func TestConsensusBlocksIDGet(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	// Create a testgroup
	groupParams := siatest.GroupParams{
		Hosts:   1,
		Renters: 1,
		Miners:  1,
	}
	tg, err := siatest.NewGroupFromTemplate(consensusTestDir(t.Name()), groupParams)
	if err != nil {
		t.Fatal("Failed to create group: ", err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	testNode := tg.Miners()[0]

	// Send /consensus request
	endBlock, err := testNode.ConsensusGet()
	if err != nil {
		t.Fatal("Failed to call ConsensusGet():", err)
	}

	// Loop over blocks and compare
	var i types.BlockHeight
	var zeroID types.BlockID
	for i = 0; i <= endBlock.Height; i++ {
		cbhg, err := testNode.ConsensusBlocksHeightGet(i)
		if err != nil {
			t.Fatal("Failed to retrieve block by height:", err)
		}
		cbig, err := testNode.ConsensusBlocksIDGet(cbhg.ID)
		if err != nil {
			t.Fatal("Failed to retrieve block by ID:", err)
		}
		// Confirm blocks received by both endpoints are the same
		if !reflect.DeepEqual(cbhg, cbig) {
			t.Fatal("Blocks not equal")
		}
		// Confirm Fields were set properly
		// Ignore ParentID and MinerPayouts for genisis block
		if cbig.ParentID == zeroID && i != 0 {
			t.Fatal("ParentID wasn't set correctly")
		}
		if len(cbig.MinerPayouts) == 0 && i != 0 {
			t.Fatal("Block has no miner payouts")
		}
		if cbig.Timestamp == types.Timestamp(0) {
			t.Fatal("Timestamp wasn't set correctly")
		}
		if len(cbig.Transactions) == 0 {
			t.Fatal("Block doesn't have any transactions even though it should")
		}

		// Verify IDs
		for _, tx := range cbhg.Transactions {
			// Building transaction of type Transaction to use as
			// comparison for ID creation
			txn := types.Transaction{
				SiacoinInputs:         tx.SiacoinInputs,
				FileContractRevisions: tx.FileContractRevisions,
				StorageProofs:         tx.StorageProofs,
				SiafundInputs:         tx.SiafundInputs,
				MinerFees:             tx.MinerFees,
				ArbitraryData:         tx.ArbitraryData,
				TransactionSignatures: tx.TransactionSignatures,
			}
			for _, sco := range tx.SiacoinOutputs {
				txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{
					Value:      sco.Value,
					UnlockHash: sco.UnlockHash,
				})
			}
			for i, fc := range tx.FileContracts {
				txn.FileContracts = append(txn.FileContracts, types.FileContract{
					FileSize:       fc.FileSize,
					FileMerkleRoot: fc.FileMerkleRoot,
					WindowStart:    fc.WindowStart,
					WindowEnd:      fc.WindowEnd,
					Payout:         fc.Payout,
					UnlockHash:     fc.UnlockHash,
					RevisionNumber: fc.RevisionNumber,
				})
				for _, vp := range fc.ValidProofOutputs {
					txn.FileContracts[i].ValidProofOutputs = append(txn.FileContracts[i].ValidProofOutputs, types.SiacoinOutput{
						Value:      vp.Value,
						UnlockHash: vp.UnlockHash,
					})
				}
				for _, mp := range fc.MissedProofOutputs {
					txn.FileContracts[i].MissedProofOutputs = append(txn.FileContracts[i].MissedProofOutputs, types.SiacoinOutput{
						Value:      mp.Value,
						UnlockHash: mp.UnlockHash,
					})
				}
			}
			for _, sfo := range tx.SiafundOutputs {
				txn.SiafundOutputs = append(txn.SiafundOutputs, types.SiafundOutput{
					Value:      sfo.Value,
					UnlockHash: sfo.UnlockHash,
					ClaimStart: types.ZeroCurrency,
				})
			}

			// Verify SiacoinOutput IDs
			for i, sco := range tx.SiacoinOutputs {
				if sco.ID != txn.SiacoinOutputID(uint64(i)) {
					t.Fatalf("SiacoinOutputID not as expected, got %v expected %v", sco.ID, txn.SiacoinOutputID(uint64(i)))
				}
			}

			// FileContracts
			for i, fc := range tx.FileContracts {
				// Verify FileContract ID
				fcid := txn.FileContractID(uint64(i))
				if fc.ID != fcid {
					t.Fatalf("FileContract ID not as expected, got %v expected %v", fc.ID, fcid)
				}
				// Verify ValidProof IDs
				for j, vp := range fc.ValidProofOutputs {
					if vp.ID != fcid.StorageProofOutputID(types.ProofValid, uint64(j)) {
						t.Fatalf("File Contract ValidProofOutputID not as expected, got %v expected %v", vp.ID, fcid.StorageProofOutputID(types.ProofValid, uint64(j)))
					}
				}
				// Verify MissedProof IDs
				for j, mp := range fc.MissedProofOutputs {
					if mp.ID != fcid.StorageProofOutputID(types.ProofMissed, uint64(j)) {
						t.Fatalf("File Contract MissedProofOutputID not as expected, got %v expected %v", mp.ID, fcid.StorageProofOutputID(types.ProofMissed, uint64(j)))
					}
				}
			}

			// Verify SiafundOutput IDs
			for i, sfo := range tx.SiafundOutputs {
				// Failing, switch back to !=
				if sfo.ID != txn.SiafundOutputID(uint64(i)) {
					t.Fatalf("SiafundOutputID not as expected, got %v expected %v", sfo.ID, txn.SiafundOutputID(uint64(i)))
				}
			}
		}
	}
}

type testSubscriber struct {
	height types.BlockHeight
	ccid   modules.ConsensusChangeID
	mu     sync.Mutex
}

func (ts *testSubscriber) ProcessConsensusChange(cc modules.ConsensusChange) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.height += types.BlockHeight(len(cc.AppliedBlocks))
	ts.height -= types.BlockHeight(len(cc.RevertedBlocks))
	ts.ccid = cc.ID
}

// TestConsensusSubscribe tests the /consensus/subscribe endpoint
func TestConsensusSubscribe(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	// Create a testgroup
	groupParams := siatest.GroupParams{
		Miners: 1,
	}
	tg, err := siatest.NewGroupFromTemplate(consensusTestDir(t.Name()), groupParams)
	if err != nil {
		t.Fatal("Failed to create group: ", err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	testNode := tg.Miners()[0]

	// subscribe via api, but cancel immediately
	s := &testSubscriber{height: ^types.BlockHeight(0)}
	cancel := make(chan struct{})
	close(cancel)
	errCh, _ := testNode.ConsensusSetSubscribe(s, modules.ConsensusChangeBeginning, cancel)
	if err := <-errCh; errors.Is(err, context.Canceled) {
		t.Fatal("expected context.Canceled, got", err)
	}
	// subscribe again without cancelling
	errCh, unsubscribe := testNode.ConsensusSetSubscribe(s, modules.ConsensusChangeBeginning, nil)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}

	// subscriber should be synced with miner
	cg, err := testNode.ConsensusGet()
	if err != nil {
		t.Fatal(err)
	}
	if s.height != cg.Height {
		t.Fatal("subscriber not synced", s.height, cg.Height)
	}

	// unsubscribe and mine more blocks; subscriber should not see them
	unsubscribe()
	for i := 0; i < 5; i++ {
		err = testNode.MineBlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	cg, err = testNode.ConsensusGet()
	if err != nil {
		t.Fatal(err)
	}
	if s.height == cg.Height {
		t.Fatal("subscriber was not unsubscribed", s.height, cg.Height)
	}

	// resubscribe from most recent ccid; should resync
	errCh, unsubscribe = testNode.ConsensusSetSubscribe(s, s.ccid, nil)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if s.height != cg.Height {
		t.Fatal("subscriber not synced", s.height, cg.Height)
	}
}

// TestFoundationHardfork tests the foundation hardfork, ensuring that upgraded
// nodes have the ability to follow the hardfork, and ensuring that the
// mechanisms for spending the foundation coins are functional.
func TestFoundationHardfork(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	// Create a testgroup. Include hosts and renters to check that basic renting
	// functions continue to work after the fork.
	groupParams := siatest.GroupParams{
		Hosts:   2,
		Miners:  1,
		Renters: 1,
	}
	tg, err := siatest.NewGroupFromTemplate(consensusTestDir(t.Name()), groupParams)
	if err != nil {
		t.Fatal("Failed to create group: ", err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	ws, err := tg.AddNodes(node.WalletTemplate)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 {
		t.Fatal("bad")
	}
	w := ws[0]

	// Have the renter upload some files to Sia prior to the foundation fork
	// activating. We will check at the end of the test whether these files are
	// still retrievable, indicating that upgraded renters and hosts had no
	// trouble following along in the fork.
	r := tg.Renters()[0]
	localFile, remoteFile, err := r.UploadNewFileBlocking(100+siatest.Fuzz(), 1, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	localFileData, err := localFile.Data()
	if err != nil {
		t.Fatal(err)
	}

	// TODO: Check that the height is still pre-hardfork at this point. If it's
	// not, we'll need to adjust the constant that sets when the hardfork
	// activates.
	height, err := w.BlockHeight()
	if err != nil {
		t.Fatal(height)
	}
	if height >= types.FoundationHardforkHeight {
		t.Log(height)
		t.Log(types.FoundationHardforkHeight)
		t.Fatal("test has already passed the foundation hardfork height, test is invalid")
	}

	// TODO: Create a transaction that updates the foudation addresses, to be
	// submitted to the blockchain prior to the fork. Because of how the
	// foundation code scans for updates, this will require sending money to the
	// foundation addresses prior to the hardfork activating.

	// TODO: Mine until we are after the hardfork. Check that the foundation
	// addresses were not changed by the transaction submitted before the
	// hardfork.

	// TODO: Create a transaction that spends the initial foundation subsidy to
	// a wallet. Verify that the coins make it to the wallet, and that the
	// wallet can send those coins like any other coins.

	// TODO: Mine until the first monthy output is created. Then create a
	// transaction that spends that monthly output and verify that the monthly
	// outputs are usable like any other outputs.

	// TODO: Mine until the second monthly output is created. Then create a
	// transaction that changes the foundation addresses using the primary
	// address.
	//
	// Then try to spend the second monthly output using both the original
	// address and the updated address. If the fork is updated to retro-actively
	// re-assign the address of the output, use the original address first and
	// see that it fails. If the fork is not updated to do that, use the new
	// address first and see that it fails. After trying the address that is
	// supposed to fail, try that address that is supposed to work and ensure it
	// still works.

	// TODO: Mine until the third monthly output is created. Try to spend the
	// thrid monthly output using the old foundation address. Ensure it fails.
	// Try to spend the third monthly output using the updated foundation
	// address. Ensure that it succeeds.

	// TODO: Mine until the fourth monthly output is created. Then create a
	// transaction that changes the foundation addresses using the failsafe
	// address.
	//
	// Then try to spend the fourth monthly output using both the original
	// address and the updated address. If the fork is updated to retro-actively
	// re-assign the address of the output, use the original address first and
	// see that it fails. If the fork is not updated to do that, use the new
	// address first and see that it fails. After trying the address that is
	// supposed to fail, try that address that is supposed to work and ensure it
	// still works.

	// TODO: Update the failsafe foundation address to have a timelock that
	// expires after the fifth monthly output is created. Mine until the fifth
	// monthly output is created.
	///
	// Then create a transaction that simultaneously spends the fifth monthly
	// output and also updates the failsafe address to extend the timeout to
	// being after the sixth monthly payout.

	// TODO: Mine until after the sixth monthly payout is available, but not so
	// far that the failsafe is supposed to be able to spend the sixth payout.
	// Then try change the foundation outputs using the failsafe, which should
	// not work. This ensures that the timelock extension was effective.

	/////// I think that's it ////////

	// Check that the files uploaded before the hardfork activiation height are
	// still doing well, even after all of the paces that we have put the group
	// through with managing the foundation subsidy.
	_, remoteFileData, err := r.DownloadByStream(remoteFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(localFileData, remoteFileData) {
		t.Fatal(err)
	}
}
