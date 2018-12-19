package consensus

import (
	"testing"
	"time"
	"unsafe"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/consensus/database"
	"gitlab.com/NebulousLabs/Sia/types"
)

// genesisBlockTx fakes the Block method of database.Tx for a single block:
// the genesis block, with a custom ChildTarget. Other methods will panic.
type genesisBlockTx struct {
	target types.Target
	database.Tx
}

func (tx genesisBlockTx) Block(id types.BlockID) (*database.Block, bool) {
	if id != types.GenesisID {
		return nil, false
	}
	return &database.Block{
		Block:       types.GenesisBlock,
		ChildTarget: tx.target,
	}, true
}

func mockGenesisBlockTx(initialTarget types.Target) genesisBlockTx {
	return genesisBlockTx{
		target: initialTarget,
	}
}

// TestUnitValidateHeaderAndBlock runs a series of unit tests for validateHeaderAndBlock.
func TestUnitValidateHeaderAndBlock(t *testing.T) {
	validBlock := types.Block{
		ParentID:     types.GenesisID,
		Timestamp:    types.GenesisTimestamp + 100,
		MinerPayouts: []types.SiacoinOutput{{Value: types.CalculateCoinbase(1)}},
	}
	orphanBlock := types.Block{
		ParentID: types.BlockID{42},
	}
	invalidBlock := types.Block{
		ParentID:  types.GenesisID,
		Timestamp: types.GenesisTimestamp + 100,
		// missing miner payouts
	}

	var tests = []struct {
		block       types.Block
		dosBlocks   map[types.BlockID]struct{}
		currentTime types.Timestamp
		errWant     error
		msg         string
	}{
		{
			block:       validBlock,
			dosBlocks:   make(map[types.BlockID]struct{}),
			currentTime: validBlock.Timestamp,
			errWant:     nil,
			msg:         "validateHeaderAndBlock should accept a valid block",
		},
		{
			block:       validBlock,
			dosBlocks:   map[types.BlockID]struct{}{validBlock.ID(): {}},
			currentTime: validBlock.Timestamp,
			errWant:     errDoSBlock,
			msg:         "validateHeaderAndBlock should reject known bad blocks",
		},
		{
			block:       orphanBlock,
			dosBlocks:   make(map[types.BlockID]struct{}),
			currentTime: orphanBlock.Timestamp,
			errWant:     errOrphan,
			msg:         "validateHeaderAndBlock should reject a block if its parent block does not appear in the block database",
		},
		{
			block:       invalidBlock,
			dosBlocks:   make(map[types.BlockID]struct{}),
			currentTime: invalidBlock.Timestamp,
			errWant:     errBadMinerPayouts,
			msg:         "validateHeaderAndBlock should reject a block if ValidateBlock returns an error for the block",
		},
	}
	for _, tt := range tests {
		cs := ConsensusSet{
			dosBlocks: tt.dosBlocks,
		}
		tx := mockGenesisBlockTx(types.RootDepth)
		_, err := cs.validateHeaderAndBlock(tx, tt.block, tt.block.ID(), tt.currentTime)
		if err != tt.errWant {
			t.Errorf("%s: expected to fail with `%v', got: `%v'", tt.msg, tt.errWant, err)
		}
	}
}

// TestCheckHeaderTarget probes the checkHeaderTarget function and checks that
// the result matches the result of checkTarget.
func TestCheckHeaderTarget(t *testing.T) {
	var b types.Block
	var h types.BlockHeader

	tests := []struct {
		target   types.Target
		expected bool
		msg      string
	}{
		{types.RootDepth, true, "checkHeaderTarget failed for a low target"},
		{types.Target{}, false, "checkHeaderTarget passed for a high target"},
		{types.Target(h.ID()), true, "checkHeaderTarget failed for a same target"},
	}
	for _, tt := range tests {
		if checkHeaderTarget(h, tt.target) != tt.expected {
			t.Error(tt.msg)
		}
		if checkHeaderTarget(h, tt.target) != checkTarget(b, b.ID(), tt.target) {
			t.Errorf("checkHeaderTarget and checkTarget do not match for target %v", tt.target)
		}
	}
}

// TestUnitValidateHeader runs a series of unit tests for validateHeader.
func TestUnitValidateHeader(t *testing.T) {
	validBlock := types.BlockHeader{
		ParentID:  types.GenesisID,
		Timestamp: types.GenesisTimestamp + 100,
	}
	orphanBlock := types.BlockHeader{
		ParentID: types.BlockID{42},
	}
	earlyBlock := types.BlockHeader{
		ParentID:  types.GenesisID,
		Timestamp: 0,
	}
	futureBlock := types.BlockHeader{
		ParentID:  types.GenesisID,
		Timestamp: types.CurrentTimestamp() + types.ExtremeFutureThreshold + 2,
	}
	nearBlock := types.BlockHeader{
		ParentID:  types.GenesisID,
		Timestamp: types.CurrentTimestamp() + types.FutureThreshold + 2,
	}

	var tests = []struct {
		header    types.BlockHeader
		dosBlocks map[types.BlockID]struct{}
		target    types.Target
		errWant   error
		msg       string
	}{
		// Test that valid blocks are accepted.
		{
			header:    validBlock,
			dosBlocks: make(map[types.BlockID]struct{}),
			target:    types.RootDepth,
			errWant:   nil,
			msg:       "validateHeader should accept a valid block",
		},
		// Test that known dos blocks are rejected.
		{
			header:    validBlock,
			dosBlocks: map[types.BlockID]struct{}{validBlock.ID(): {}},
			target:    types.RootDepth,
			errWant:   errDoSBlock,
			msg:       "validateHeader should reject known bad blocks",
		},
		// Test that known blocks are rejected.
		{
			header:    types.GenesisBlock.Header(),
			dosBlocks: make(map[types.BlockID]struct{}),
			target:    types.RootDepth,
			errWant:   modules.ErrBlockKnown,
			msg:       "validateHeader should fail when the block has been seen before",
		},
		// Test that blocks with unknown parents (orphans) are rejected.
		{
			header:    orphanBlock,
			dosBlocks: make(map[types.BlockID]struct{}),
			target:    types.RootDepth,
			errWant:   errOrphan,
			msg:       "validateHeader should reject a block if its parent block does not appear in the block database",
		},
		// Test that blocks with too early of a timestamp are rejected.
		{
			header:    earlyBlock,
			dosBlocks: make(map[types.BlockID]struct{}),
			target:    types.RootDepth,
			errWant:   errEarlyTimestamp,
			msg:       "validateHeader should fail when the header's timestamp is too early",
		},
		// Test that headers in the extreme future are rejected.
		{
			header:    futureBlock,
			dosBlocks: make(map[types.BlockID]struct{}),
			target:    types.RootDepth,
			errWant:   errExtremeFutureTimestamp,
			msg:       "validateHeader should fail when the header's timestamp is in the extreme future",
		},
		// Test that headers in the near future are not rejected.
		{
			header:    nearBlock,
			dosBlocks: make(map[types.BlockID]struct{}),
			target:    types.RootDepth,
			errWant:   nil,
			msg:       "validateHeader should not reject headers whose timestamps are in the near future",
		},
		// Test that blocks with too large of a target are rejected.
		{
			header:    validBlock,
			dosBlocks: make(map[types.BlockID]struct{}),
			target:    types.Target{},
			errWant:   modules.ErrBlockUnsolved,
			msg:       "validateHeader should reject blocks with an insufficiently low target",
		},
	}
	for _, tt := range tests {
		cs := ConsensusSet{
			dosBlocks: tt.dosBlocks,
		}
		tx := mockGenesisBlockTx(tt.target)
		err := cs.validateHeader(tx, tt.header)
		if err != tt.errWant {
			t.Errorf("%s: expected to fail with `%v', got: `%v'", tt.msg, tt.errWant, err)
		}
	}
}

// TestIntegrationDoSBlockHandling checks that saved bad blocks are correctly
// ignored.
func TestIntegrationDoSBlockHandling(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	cst, err := createConsensusSetTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer cst.Close()

	// Mine a block that is valid except for containing a buried invalid
	// transaction. The transaction has more siacoin inputs than outputs.
	txnBuilder, err := cst.wallet.StartTransaction()
	if err != nil {
		t.Fatal(err)
	}
	err = txnBuilder.FundSiacoins(types.NewCurrency64(50))
	if err != nil {
		t.Fatal(err)
	}
	txnSet, err := txnBuilder.Sign(true) // true sets the 'wholeTransaction' flag
	if err != nil {
		t.Fatal(err)
	}

	// Mine and submit the invalid block to the consensus set. The first time
	// around, the complaint should be about the rule-breaking transaction.
	block, target, err := cst.miner.BlockForWork()
	if err != nil {
		t.Fatal(err)
	}
	block.Transactions = append(block.Transactions, txnSet...)
	dosBlock, _ := cst.miner.SolveBlock(block, target)
	err = cst.cs.AcceptBlock(dosBlock)
	if err != errSiacoinInputOutputMismatch {
		t.Fatalf("expected %v, got %v", errSiacoinInputOutputMismatch, err)
	}

	// Submit the same block a second time. The complaint should be that the
	// block is already known to be invalid.
	err = cst.cs.AcceptBlock(dosBlock)
	if err != errDoSBlock {
		t.Fatalf("expected %v, got %v", errDoSBlock, err)
	}
}

// TestBlockKnownHandling submits known blocks to the consensus set.
func TestBlockKnownHandling(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	cst, err := createConsensusSetTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer cst.Close()

	// Get a block destined to be stale.
	block, target, err := cst.miner.BlockForWork()
	if err != nil {
		t.Fatal(err)
	}
	staleBlock, _ := cst.miner.SolveBlock(block, target)

	// Add two new blocks to the consensus set to block the stale block.
	block1, err := cst.miner.AddBlock()
	if err != nil {
		t.Fatal(err)
	}
	block2, err := cst.miner.AddBlock()
	if err != nil {
		t.Fatal(err)
	}

	// Submit the stale block.
	err = cst.cs.AcceptBlock(staleBlock)
	if err != nil && err != modules.ErrNonExtendingBlock {
		t.Fatal(err)
	}

	// Submit all the blocks again, looking for a 'stale block' error.
	err = cst.cs.AcceptBlock(block1)
	if err == nil {
		t.Fatal("expected an error upon submitting the block")
	}
	err = cst.cs.AcceptBlock(block2)
	if err == nil {
		t.Fatal("expected an error upon submitting the block")
	}
	err = cst.cs.AcceptBlock(staleBlock)
	if err == nil {
		t.Fatal("expected an error upon submitting the block")
	}

	// Try submitting the genesis block.
	id, err := cst.cs.dbGetPath(0)
	if err != nil {
		t.Fatal(err)
	}
	genesisBlock, err := cst.cs.dbGetBlockMap(id)
	if err != nil {
		t.Fatal(err)
	}
	err = cst.cs.AcceptBlock(genesisBlock.Block)
	if err == nil {
		t.Fatal("expected an error upon submitting the block")
	}
}

// TestOrphanHandling passes an orphan block to the consensus set.
func TestOrphanHandling(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	cst, err := createConsensusSetTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer cst.Close()

	// Try submitting an orphan block to the consensus set. The empty block can
	// be used, because looking for a parent is one of the first checks the
	// consensus set performs.
	orphan := types.Block{}
	err = cst.cs.AcceptBlock(orphan)
	if err != errOrphan {
		t.Fatalf("expected %v, got %v", errOrphan, err)
	}
	err = cst.cs.AcceptBlock(orphan)
	if err != errOrphan {
		t.Fatalf("expected %v, got %v", errOrphan, err)
	}
}

// TestMissedTarget submits a block that does not meet the required target.
func TestMissedTarget(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	cst, err := createConsensusSetTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer cst.Close()

	// Mine a block that doesn't meet the target.
	block, target, err := cst.miner.BlockForWork()
	if err != nil {
		t.Fatal(err)
	}
	for checkTarget(block, block.ID(), target) {
		*(*uint64)(unsafe.Pointer(&block.Nonce)) += types.ASICHardforkFactor
	}
	if checkTarget(block, block.ID(), target) {
		t.Fatal("unable to find a failing target")
	}
	err = cst.cs.AcceptBlock(block)
	if err != modules.ErrBlockUnsolved {
		t.Fatalf("expected %v, got %v", modules.ErrBlockUnsolved, err)
	}
}

// TestMinerPayoutHandling checks that blocks with incorrect payouts are
// rejected.
func TestMinerPayoutHandling(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	cst, err := createConsensusSetTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer cst.Close()

	// Create a block with the wrong miner payout structure - testing can be
	// light here because there is heavier testing in the 'types' package,
	// where the logic is defined.
	block, target, err := cst.miner.BlockForWork()
	if err != nil {
		t.Fatal(err)
	}
	block.MinerPayouts = append(block.MinerPayouts, types.SiacoinOutput{Value: types.NewCurrency64(1)})
	solvedBlock, _ := cst.miner.SolveBlock(block, target)
	err = cst.cs.AcceptBlock(solvedBlock)
	if err != errBadMinerPayouts {
		t.Fatalf("expected %v, got %v", errBadMinerPayouts, err)
	}
}

// TestEarlyTimestampHandling checks that blocks too far in the past are
// rejected.
func TestEarlyTimestampHandling(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	cst, err := createConsensusSetTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer cst.Close()
	minTimestamp := types.CurrentTimestamp()

	// Submit a block with a timestamp in the past, before minTimestamp.
	block, target, err := cst.miner.BlockForWork()
	if err != nil {
		t.Fatal(err)
	}
	block.Timestamp = minTimestamp - 1
	solvedBlock, _ := cst.miner.SolveBlock(block, target)
	err = cst.cs.AcceptBlock(solvedBlock)
	if err != errEarlyTimestamp {
		t.Fatalf("expected %v, got %v", errEarlyTimestamp, err)
	}
}

// testFutureTimestampHandling checks that blocks in the future (but not
// extreme future) are handled correctly.
func TestFutureTimestampHandling(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	cst, err := createConsensusSetTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer cst.Close()

	// Submit a block with a timestamp in the future, but not the extreme
	// future.
	block, target, err := cst.miner.BlockForWork()
	if err != nil {
		t.Fatal(err)
	}
	block.Timestamp = types.CurrentTimestamp() + 2 + types.FutureThreshold
	solvedBlock, _ := cst.miner.SolveBlock(block, target)
	err = cst.cs.AcceptBlock(solvedBlock)
	if err != errFutureTimestamp {
		t.Fatalf("expected %v, got %v", errFutureTimestamp, err)
	}

	// Poll the consensus set until the future block appears.
	for i := 0; i < 30; i++ {
		time.Sleep(time.Second * 3)
		_, err = cst.cs.dbGetBlockMap(solvedBlock.ID())
		if err == nil {
			break
		}
	}
	_, err = cst.cs.dbGetBlockMap(solvedBlock.ID())
	if err != nil {
		t.Errorf("Future block not added to consensus set.\nCurrent Timestamp %v\nFutureThreshold: %v\nBlock Timestamp %v\n", types.CurrentTimestamp(), types.FutureThreshold, block.Timestamp)
	}
}

// TestExtremeFutureTimestampHandling checks that blocks in the extreme future
// are rejected.
func TestExtremeFutureTimestampHandling(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	cst, err := createConsensusSetTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer cst.Close()

	// Submit a block with a timestamp in the extreme future.
	block, target, err := cst.miner.BlockForWork()
	if err != nil {
		t.Fatal(err)
	}
	block.Timestamp = types.CurrentTimestamp() + 2 + types.ExtremeFutureThreshold
	solvedBlock, _ := cst.miner.SolveBlock(block, target)
	err = cst.cs.AcceptBlock(solvedBlock)
	if err != errExtremeFutureTimestamp {
		t.Fatalf("expected %v, got %v", errFutureTimestamp, err)
	}
}

// TestBuriedBadTransaction tries submitting a block with a bad transaction
// that is buried under good transactions.
func TestBuriedBadTransaction(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	cst, err := createConsensusSetTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer cst.Close()
	b := cst.cs.dbCurrentProcessedBlock()

	// Create a good transaction using the wallet.
	txnValue := types.NewCurrency64(1200)
	txnBuilder, err := cst.wallet.StartTransaction()
	if err != nil {
		t.Fatal(err)
	}
	err = txnBuilder.FundSiacoins(txnValue)
	if err != nil {
		t.Fatal(err)
	}
	txnBuilder.AddSiacoinOutput(types.SiacoinOutput{Value: txnValue})
	txnSet, err := txnBuilder.Sign(true)
	if err != nil {
		t.Fatal(err)
	}
	err = cst.tpool.AcceptTransactionSet(txnSet)
	if err != nil {
		t.Fatal(err)
	}

	// Create a bad transaction
	badTxn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{}},
	}
	txns := append(cst.tpool.TransactionList(), badTxn)

	// Create a block with a buried bad transaction.
	block := types.Block{
		ParentID:     b.ID(),
		Timestamp:    types.CurrentTimestamp(),
		MinerPayouts: []types.SiacoinOutput{{Value: types.CalculateCoinbase(b.Height + 1)}},
		Transactions: txns,
	}
	block, _ = cst.miner.SolveBlock(block, b.ChildTarget)
	err = cst.cs.AcceptBlock(block)
	if err == nil {
		t.Error("buried transaction didn't cause an error")
	}
}

// TestInconsistencyCheck puts the consensus set in to an inconsistent state
// and makes sure that the santiy checks are triggering panics.
func TestInconsistentCheck(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	cst, err := createConsensusSetTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt the consensus set by adding a new siafund output.
	sfo := types.SiafundOutput{
		Value: types.NewCurrency64(1),
	}
	cst.cs.dbAddSiafundOutput(types.SiafundOutputID{}, sfo)

	// Catch a panic that should be caused by the inconsistency check after a
	// block is mined.
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("inconsistency panic not triggered by corrupted database")
		}
	}()
	_, err = cst.miner.AddBlock()
	if err != nil {
		t.Fatal(err)
	}
}

// COMPATv0.4.0
//
// This test checks that the hardfork scheduled for block 21,000 rolls through
// smoothly.
func TestTaxHardfork(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	cst, err := createConsensusSetTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer cst.Close()

	// Create a file contract with a payout that is put into the blockchain
	// before the hardfork block but expires after the hardfork block.
	payout := types.NewCurrency64(400e6)
	outputSize := types.PostTax(cst.cs.dbBlockHeight(), payout)
	fc := types.FileContract{
		WindowStart:        cst.cs.dbBlockHeight() + 12,
		WindowEnd:          cst.cs.dbBlockHeight() + 14,
		Payout:             payout,
		ValidProofOutputs:  []types.SiacoinOutput{{Value: outputSize}},
		MissedProofOutputs: []types.SiacoinOutput{{Value: outputSize}},
		UnlockHash:         types.UnlockConditions{}.UnlockHash(), // The empty UC is anyone-can-spend
	}

	// Create and fund a transaction with a file contract.
	txnBuilder, err := cst.wallet.StartTransaction()
	if err != nil {
		t.Fatal(err)
	}
	err = txnBuilder.FundSiacoins(payout)
	if err != nil {
		t.Fatal(err)
	}
	fcIndex := txnBuilder.AddFileContract(fc)
	txnSet, err := txnBuilder.Sign(true)
	if err != nil {
		t.Fatal(err)
	}
	err = cst.tpool.AcceptTransactionSet(txnSet)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cst.miner.AddBlock()
	if err != nil {
		t.Fatal(err)
	}

	// Check that the siafund pool was increased by the faulty float amount.
	siafundPool := cst.cs.dbGetSiafundPool()
	if !siafundPool.Equals64(15590e3) {
		t.Fatal("siafund pool was not increased correctly")
	}

	// Mine blocks until the hardfork is reached.
	for i := 0; i < 10; i++ {
		_, err = cst.miner.AddBlock()
		if err != nil {
			t.Fatal(err)
		}
	}

	// Submit a file contract revision and check that the payouts are able to
	// be the same.
	fcid := txnSet[len(txnSet)-1].FileContractID(fcIndex)
	fcr := types.FileContractRevision{
		ParentID:          fcid,
		UnlockConditions:  types.UnlockConditions{},
		NewRevisionNumber: 1,

		NewFileSize:           1,
		NewWindowStart:        cst.cs.dbBlockHeight() + 2,
		NewWindowEnd:          cst.cs.dbBlockHeight() + 4,
		NewValidProofOutputs:  fc.ValidProofOutputs,
		NewMissedProofOutputs: fc.MissedProofOutputs,
	}
	txnBuilder, err = cst.wallet.StartTransaction()
	if err != nil {
		t.Fatal(err)
	}
	txnBuilder.AddFileContractRevision(fcr)
	txnSet, err = txnBuilder.Sign(true)
	if err != nil {
		t.Fatal(err)
	}
	err = cst.tpool.AcceptTransactionSet(txnSet)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cst.miner.AddBlock()
	if err != nil {
		t.Fatal(err)
	}

	// Mine blocks until the revision goes through, such that the sanity checks
	// can be run.
	for i := 0; i < 6; i++ {
		_, err = cst.miner.AddBlock()
		if err != nil {
			t.Fatal(err)
		}
	}

	// Check that the siafund pool did not change after the submitted revision.
	siafundPool = cst.cs.dbGetSiafundPool()
	if !siafundPool.Equals64(15590e3) {
		t.Fatal("siafund pool was not increased correctly")
	}
}

// mockGatewayDoesBroadcast implements modules.Gateway to mock the Broadcast
// method.
type mockGatewayDoesBroadcast struct {
	modules.Gateway
	broadcastCalled chan struct{}
}

// Broadcast is a mock implementation of modules.Gateway.Broadcast that
// sends a sentinel value down a channel to signal it's been called.
func (g *mockGatewayDoesBroadcast) Broadcast(name string, obj interface{}, peers []modules.Peer) {
	g.Gateway.Broadcast(name, obj, peers)
	g.broadcastCalled <- struct{}{}
}

// TestAcceptBlockBroadcasts tests that AcceptBlock broadcasts valid blocks and
// that managedAcceptBlock does not.
func TestAcceptBlockBroadcasts(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	cst, err := blankConsensusSetTester(t.Name(), modules.ProdDependencies)
	if err != nil {
		t.Fatal(err)
	}
	defer cst.Close()
	mg := &mockGatewayDoesBroadcast{
		Gateway:         cst.cs.gateway,
		broadcastCalled: make(chan struct{}),
	}
	cst.cs.gateway = mg

	// Test that Broadcast is called for valid blocks.
	b, _ := cst.miner.FindBlock()
	err = cst.cs.AcceptBlock(b)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-mg.broadcastCalled:
	case <-time.After(10 * time.Millisecond):
		t.Error("expected AcceptBlock to broadcast a valid block")
	}

	// Test that Broadcast is not called for invalid blocks.
	err = cst.cs.AcceptBlock(types.Block{})
	if err == nil {
		t.Fatal("expected AcceptBlock to error on an invalid block")
	}
	select {
	case <-mg.broadcastCalled:
		t.Error("AcceptBlock broadcasted an invalid block")
	case <-time.After(10 * time.Millisecond):
	}

	// Test that Broadcast is not called in managedAcceptBlock.
	b, _ = cst.miner.FindBlock()
	_, err = cst.cs.managedAcceptBlocks([]types.Block{b})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-mg.broadcastCalled:
		t.Errorf("managedAcceptBlock should not broadcast blocks")
	case <-time.After(10 * time.Millisecond):
	}
}

// blockCountingSubscriber counts the number of blocks that get submitted to the
// subscriber, as well as the number of times that the subscriber has been given
// changes at all.
type blockCountingSubscriber struct {
	changes []modules.ConsensusChangeID

	appliedBlocks  int
	revertedBlocks int
}

// ProcessConsensusChange fills the subscription interface for the
// blockCountingSubscriber.
func (bcs *blockCountingSubscriber) ProcessConsensusChange(cc modules.ConsensusChange) {
	bcs.changes = append(bcs.changes, cc.ID)
	bcs.revertedBlocks += len(cc.RevertedBlocks)
	bcs.appliedBlocks += len(cc.AppliedBlocks)
}

// TestChainedAcceptBlock creates series of blocks, some of which are valid,
// some invalid, and submits them to the consensus set, verifying that the
// consensus set updates correctly each time.
func TestChainedAcceptBlock(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	// Create a tester to send blocks in a batch to the other tester.
	cst, err := createConsensusSetTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer cst.Close()
	cst2, err := blankConsensusSetTester(t.Name()+"2", modules.ProdDependencies)
	if err != nil {
		t.Fatal(err)
	}
	defer cst2.Close()
	// Subscribe a blockCountingSubscriber to cst2.
	var bcs blockCountingSubscriber
	cst2.cs.ConsensusSetSubscribe(&bcs, modules.ConsensusChangeBeginning, cst2.cs.tg.StopChan())
	if len(bcs.changes) != 1 || bcs.appliedBlocks != 1 || bcs.revertedBlocks != 0 {
		t.Error("consensus changes do not seem to be getting passed to subscribers correctly")
	}

	// Grab all of the blocks in cst, with the intention of giving them to cst2.
	var blocks []types.Block
	height := cst.cs.Height()
	for i := types.BlockHeight(0); i <= height; i++ {
		id, err := cst.cs.dbGetPath(i)
		if err != nil {
			t.Fatal(err)
		}
		b, err := cst.cs.dbGetBlockMap(id)
		if err != nil {
			t.Fatal(err)
		}
		blocks = append(blocks, b.Block)
	}

	// Create a jumbling of the blocks, so that the set is not in order.
	jumble := make([]types.Block, len(blocks))
	jumble[0] = blocks[0]
	jumble[1] = blocks[2]
	jumble[2] = blocks[1]
	for i := 3; i < len(jumble); i++ {
		jumble[i] = blocks[i]
	}
	// Try to submit the blocks out-of-order, which would violate one of the
	// assumptions in managedAcceptBlocks.
	_, err = cst2.cs.managedAcceptBlocks(jumble)
	if err != errNonLinearChain {
		t.Fatal(err)
	}
	if cst2.cs.Height() != 0 {
		t.Fatal("blocks added even though the inputs were jumbled")
	}
	if len(bcs.changes) != 1 || bcs.appliedBlocks != 1 || bcs.revertedBlocks != 0 {
		t.Error("consensus changes do not seem to be getting passed to subscribers correctly")
	}

	// Tag an invalid block onto the end of blocks.
	block, err := cst.miner.AddBlock()
	if err != nil {
		t.Fatal(err)
	}
	// Adding an invalid transaction to make the block invalid.
	badBlock := block
	badBlock.Transactions = append(badBlock.Transactions, types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			ParentID: types.SiacoinOutputID{1},
		}},
	})
	// Append the invalid transaction to the block.
	badBlocks := append(blocks, badBlock)
	// Submit the whole invalid set. Result should be that nothing is added.
	_, err = cst2.cs.managedAcceptBlocks(badBlocks)
	if err == nil {
		t.Fatal(err)
	}
	if cst2.cs.Height() != 0 {
		t.Log(cst2.cs.Height())
		t.Log(cst.cs.Height())
		t.Fatal("height is not correct, seems that blocks were added")
	}
	if bcs.appliedBlocks != 1 || bcs.revertedBlocks != 0 {
		t.Error("consensus changes do not seem to be getting passed to subscribers correctly")
	}

	// Try submitting the good blocks.
	_, err = cst2.cs.managedAcceptBlocks(blocks)
	if err != nil {
		t.Fatal(err)
	}
	if bcs.appliedBlocks != int(cst2.cs.Height()+1) || bcs.revertedBlocks != 0 {
		t.Error("consensus changes do not seem to be getting passed to subscribers correctly")
	}

	// Check that every change recorded in 'bcs' is also available in the
	// consensus set.
	for _, change := range bcs.changes {
		err := cst2.cs.db.Update(func(tx database.Tx) error {
			_, exists := tx.ChangeEntry(change)
			if !exists {
				t.Error("an entry was provided that doesn't exist")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
