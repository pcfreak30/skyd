package renter

import (
	"math"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/siatest/dependencies"
)

// testProcessUploadChunkBasic tests processing a valid, needed chunk.
func testProcessUploadChunkBasic(t *testing.T, chunk func(wt *workerTester) *unfinishedUploadChunk) {
	t.Parallel()

	// create worker.
	wt, err := newWorkerTesterCustomDependency(t.Name(), &dependencies.DependencyDisableWorker{}, modules.ProdDependencies)
	if err != nil {
		t.Fatal(err)
	}
	defer wt.Close()

	uuc := chunk(wt)
	wt.mu.Lock()
	wt.unprocessedChunks = []*unfinishedUploadChunk{uuc, uuc, uuc}
	wt.mu.Unlock()
	uuc.mu.Lock()
	uuc.pieceUsage[0] = true // mark first piece as used
	uuc.mu.Unlock()
	_ = wt.renter.memoryManager.Request(modules.SectorSize*uint64(uuc.piecesNeeded-1), true)
	nc, pieceIndex := wt.managedProcessUploadChunk(uuc)
	if nc == nil {
		t.Error("next chunk shouldn't be nil")
	}
	uuc.mu.Lock()
	if pieceIndex != 1 {
		t.Error("expected pieceIndex to be 1 since piece 0 is marked as used", pieceIndex)
	}
	if uuc.piecesRegistered != 1 {
		t.Errorf("piecesRegistered %v != %v", uuc.piecesRegistered, 1)
	}
	if uuc.workersRemaining != 0 {
		t.Errorf("workersRemaining %v != %v", uuc.workersRemaining, 0)
	}
	if len(uuc.unusedHosts) != 0 {
		t.Errorf("unusedHosts %v != %v", len(uuc.unusedHosts), 0)
	}
	if !uuc.pieceUsage[1] {
		t.Errorf("expected pieceUsage[1] to be true")
	}
	if len(uuc.workersStandby) != 0 {
		t.Errorf("expected %v standby workers got %v", 0, len(uuc.workersStandby))
	}
	uuc.mu.Unlock()
	wt.mu.Lock()
	if len(wt.unprocessedChunks) != 3 {
		t.Errorf("unprocessedChunks %v != %v", len(wt.unprocessedChunks), 3)
	}
	wt.mu.Unlock()
}

// testProcessUploadChunkNoHelpNeeded tests processing a chunk that the worker
// could help with but no help is needed at the moment.
func testProcessUploadChunkNoHelpNeeded(t *testing.T, chunk func(wt *workerTester) *unfinishedUploadChunk) {
	t.Parallel()

	// create worker.
	wt, err := newWorkerTesterCustomDependency(t.Name(), &dependencies.DependencyDisableWorker{}, modules.ProdDependencies)
	if err != nil {
		t.Fatal(err)
	}
	defer wt.Close()

	uuc := chunk(wt)
	pieces := uuc.piecesNeeded
	wt.mu.Lock()
	wt.unprocessedChunks = nil
	wt.mu.Unlock()
	uuc.mu.Lock()
	uuc.piecesRegistered = uuc.piecesNeeded
	uuc.mu.Unlock()
	println("request", modules.SectorSize*uint64(pieces))
	_ = wt.renter.memoryManager.Request(modules.SectorSize*uint64(pieces), true)
	nc, pieceIndex := wt.managedProcessUploadChunk(uuc)
	if nc != nil {
		t.Error("next chunk should be nil")
	}
	uuc.mu.Lock()
	if pieceIndex != 0 {
		t.Error("expected pieceIndex to be 0", pieceIndex)
	}
	if uuc.piecesRegistered != uuc.piecesNeeded {
		t.Errorf("piecesRegistered %v != %v", uuc.piecesRegistered, 1)
	}
	if uuc.workersRemaining != 1 {
		t.Errorf("workersRemaining %v != %v", uuc.workersRemaining, 1)
	}
	if len(uuc.unusedHosts) != 1 {
		t.Errorf("unusedHosts %v != %v", len(uuc.unusedHosts), 1)
	}
	for i, pu := range uuc.pieceUsage {
		// Only index 0 is false.
		if b := i != 0; b != pu {
			t.Errorf("%v: expected %v but was %v", i, b, pu)
		}
	}
	// Standby workers are woken.
	if len(uuc.workersStandby) != 0 {
		t.Errorf("expected %v standby workers got %v", 0, len(uuc.workersStandby))
	}
	uuc.mu.Unlock()
	wt.mu.Lock()
	if len(wt.unprocessedChunks) != 1 {
		t.Errorf("unprocessedChunks %v != %v", len(wt.unprocessedChunks), 0)
	}
	wt.mu.Unlock()
}

// testProcessUploadChunkNotACandiate tests processing a chunk that the worker
// is not a valid candidate for.
func testProcessUploadChunkNotACandidate(t *testing.T, chunk func(wt *workerTester) *unfinishedUploadChunk) {
	t.Parallel()

	// create worker.
	wt, err := newWorkerTesterCustomDependency(t.Name(), &dependencies.DependencyDisableWorker{}, modules.ProdDependencies)
	if err != nil {
		t.Fatal(err)
	}
	defer wt.Close()

	uuc := chunk(wt)
	pieces := uuc.piecesNeeded
	wt.mu.Lock()
	wt.unprocessedChunks = []*unfinishedUploadChunk{uuc, uuc, uuc}
	wt.mu.Unlock()
	uuc.mu.Lock()
	uuc.unusedHosts = make(map[string]struct{})
	uuc.mu.Unlock()
	_ = wt.renter.memoryManager.Request(modules.SectorSize*uint64(pieces), true)
	nc, pieceIndex := wt.managedProcessUploadChunk(uuc)
	if nc != nil {
		t.Error("next chunk should be nil")
	}
	uuc.mu.Lock()
	if pieceIndex != 0 {
		t.Error("expected pieceIndex to be 0", pieceIndex)
	}
	if uuc.piecesRegistered != 0 {
		t.Errorf("piecesRegistered %v != %v", uuc.piecesRegistered, 0)
	}
	if uuc.workersRemaining != 0 {
		t.Errorf("workersRemaining %v != %v", uuc.workersRemaining, 0)
	}
	if len(uuc.unusedHosts) != 0 {
		t.Errorf("unusedHosts %v != %v", len(uuc.unusedHosts), 0)
	}
	for _, pu := range uuc.pieceUsage {
		// managedCleanUpUploadChunk sets all elements to true
		if !pu {
			t.Errorf("expected pu to be true")
		}
	}
	// Standby workers are woken.
	if len(uuc.workersStandby) != 0 {
		t.Errorf("expected %v standby workers got %v", 0, len(uuc.workersStandby))
	}
	uuc.mu.Unlock()
	wt.mu.Lock()
	if len(wt.unprocessedChunks) != 3 {
		t.Errorf("unprocessedChunks %v != %v", len(wt.unprocessedChunks), 3)
	}
	wt.mu.Unlock()
}

// testProcessUploadChunkNotACandiate tests processing a chunk that was already
// completed.
func testProcessUploadChunkCompleted(t *testing.T, chunk func(wt *workerTester) *unfinishedUploadChunk) {
	t.Parallel()

	// create worker.
	wt, err := newWorkerTesterCustomDependency(t.Name(), &dependencies.DependencyDisableWorker{}, modules.ProdDependencies)
	if err != nil {
		t.Fatal(err)
	}
	defer wt.Close()

	uuc := chunk(wt)
	pieces := uuc.piecesNeeded
	wt.mu.Lock()
	wt.unprocessedChunks = []*unfinishedUploadChunk{uuc, uuc, uuc}
	wt.mu.Unlock()
	uuc.mu.Lock()
	uuc.piecesCompleted = uuc.piecesNeeded
	uuc.mu.Unlock()
	_ = wt.renter.memoryManager.Request(modules.SectorSize*uint64(pieces), true)
	nc, pieceIndex := wt.managedProcessUploadChunk(uuc)
	if nc != nil {
		t.Error("next chunk should be nil")
	}
	uuc.mu.Lock()
	if pieceIndex != 0 {
		t.Error("expected pieceIndex to be 0", pieceIndex)
	}
	if uuc.piecesRegistered != 0 {
		t.Errorf("piecesRegistered %v != %v", uuc.piecesRegistered, 0)
	}
	if uuc.workersRemaining != 0 {
		t.Errorf("workersRemaining %v != %v", uuc.workersRemaining, 0)
	}
	if len(uuc.unusedHosts) != 1 {
		t.Errorf("unusedHosts %v != %v", len(uuc.unusedHosts), 1)
	}
	for _, pu := range uuc.pieceUsage {
		// managedCleanUpUploadChunk sets all elements to true
		if !pu {
			t.Errorf("expected pu to be true")
		}
	}
	// Standby workers are woken.
	if len(uuc.workersStandby) != 0 {
		t.Errorf("expected %v standby workers got %v", 0, len(uuc.workersStandby))
	}
	uuc.mu.Unlock()
	wt.mu.Lock()
	if len(wt.unprocessedChunks) != 3 {
		t.Errorf("unprocessedChunks %v != %v", len(wt.unprocessedChunks), 3)
	}
	wt.mu.Unlock()
}

// testProcessUploadChunkNotACandiateOnCooldown tests processing a chunk that
// the worker is not a candidate for and also the worker is currently on a
// cooldown.
func testProcessUploadChunk_NotACandidateCooldown(t *testing.T, chunk func(wt *workerTester) *unfinishedUploadChunk) {
	t.Parallel()

	// create worker.
	wt, err := newWorkerTesterCustomDependency(t.Name(), &dependencies.DependencyDisableWorker{}, modules.ProdDependencies)
	if err != nil {
		t.Fatal(err)
	}
	defer wt.Close()

	uuc := chunk(wt)
	pieces := uuc.piecesNeeded
	wt.mu.Lock()
	wt.uploadRecentFailure = time.Now()
	wt.uploadConsecutiveFailures = math.MaxInt32
	wt.unprocessedChunks = []*unfinishedUploadChunk{uuc, uuc, uuc}
	wt.mu.Unlock()
	uuc.mu.Lock()
	uuc.unusedHosts = make(map[string]struct{})
	uuc.mu.Unlock()
	_ = wt.renter.memoryManager.Request(modules.SectorSize*uint64(pieces), true)
	nc, pieceIndex := wt.managedProcessUploadChunk(uuc)
	if nc != nil {
		t.Error("next chunk should be nil")
	}
	uuc.mu.Lock()
	if pieceIndex != 0 {
		t.Error("expected pieceIndex to be 0", pieceIndex)
	}
	if uuc.piecesRegistered != 0 {
		t.Errorf("piecesRegistered %v != %v", uuc.piecesRegistered, 0)
	}
	if uuc.workersRemaining != -3 {
		t.Errorf("workersRemaining %v != %v", uuc.workersRemaining, 0)
	}
	if len(uuc.unusedHosts) != 0 {
		t.Errorf("unusedHosts %v != %v", len(uuc.unusedHosts), 0)
	}
	for _, pu := range uuc.pieceUsage {
		// managedCleanUpUploadChunk sets all elements to true
		if !pu {
			t.Errorf("expected pu to be true")
		}
	}
	// Standby workers are woken.
	if len(uuc.workersStandby) != 0 {
		t.Errorf("expected %v standby workers got %v", 0, len(uuc.workersStandby))
	}
	uuc.mu.Unlock()
	wt.mu.Lock()
	if len(wt.unprocessedChunks) != 0 {
		t.Errorf("unprocessedChunks %v != %v", len(wt.unprocessedChunks), 0)
	}
	wt.mu.Unlock()
}

// testProcessUploadChunkCompletedCooldown tests processing a chunk that was already completed
// worker is not a candidate for and also the worker is currently on a cooldown.
func testProcessUploadChunkCompletedCooldown(t *testing.T, chunk func(wt *workerTester) *unfinishedUploadChunk) {
	t.Parallel()

	// create worker.
	wt, err := newWorkerTesterCustomDependency(t.Name(), &dependencies.DependencyDisableWorker{}, modules.ProdDependencies)
	if err != nil {
		t.Fatal(err)
	}
	defer wt.Close()

	uuc := chunk(wt)
	pieces := uuc.piecesNeeded
	wt.mu.Lock()
	wt.uploadRecentFailure = time.Now()
	wt.uploadConsecutiveFailures = math.MaxInt32
	wt.unprocessedChunks = []*unfinishedUploadChunk{uuc, uuc, uuc}
	wt.mu.Unlock()
	wt.mu.Lock()
	wt.unprocessedChunks = []*unfinishedUploadChunk{uuc, uuc, uuc}
	wt.mu.Unlock()
	uuc.mu.Lock()
	uuc.piecesCompleted = uuc.piecesNeeded
	uuc.mu.Unlock()
	_ = wt.renter.memoryManager.Request(modules.SectorSize*uint64(pieces), true)
	nc, pieceIndex := wt.managedProcessUploadChunk(uuc)
	if nc != nil {
		t.Error("next chunk should be nil")
	}
	uuc.mu.Lock()
	if pieceIndex != 0 {
		t.Error("expected pieceIndex to be 0", pieceIndex)
	}
	if uuc.piecesRegistered != 0 {
		t.Errorf("piecesRegistered %v != %v", uuc.piecesRegistered, 0)
	}
	if uuc.workersRemaining != -3 {
		t.Errorf("workersRemaining %v != %v", uuc.workersRemaining, 0)
	}
	if len(uuc.unusedHosts) != 1 {
		t.Errorf("unusedHosts %v != %v", len(uuc.unusedHosts), 1)
	}
	for _, pu := range uuc.pieceUsage {
		// managedCleanUpUploadChunk sets all elements to true
		if !pu {
			t.Errorf("expected pu to be true")
		}
	}
	// Standby workers are woken.
	if len(uuc.workersStandby) != 0 {
		t.Errorf("expected %v standby workers got %v", 0, len(uuc.workersStandby))
	}
	uuc.mu.Unlock()
	wt.mu.Lock()
	if len(wt.unprocessedChunks) != 0 {
		t.Errorf("unprocessedChunks %v != %v", len(wt.unprocessedChunks), 0)
	}
	wt.mu.Unlock()
}

// testProcessUploadChunkNotGoodForUpload tests processing a chunk with a worker
// that's not good for uploading.
func testProcessUploadChunkNotGoodForUpload(t *testing.T, chunk func(wt *workerTester) *unfinishedUploadChunk) {
	t.Parallel()

	// create worker.
	wt, err := newWorkerTesterCustomDependency(t.Name(), &dependencies.DependencyDisableWorker{}, modules.ProdDependencies)
	if err != nil {
		t.Fatal(err)
	}
	defer wt.Close()

	uuc := chunk(wt)
	pieces := uuc.piecesNeeded
	wt.mu.Lock()
	wt.unprocessedChunks = []*unfinishedUploadChunk{uuc, uuc, uuc}
	wt.mu.Unlock()

	// mark contract as bad
	err = wt.renter.CancelContract(wt.staticCache().staticContractID)
	if err != nil {
		t.Fatal(err)
	}
	wt.managedUpdateCache()
	_ = wt.renter.memoryManager.Request(modules.SectorSize*uint64(pieces), true)
	nc, pieceIndex := wt.managedProcessUploadChunk(uuc)
	if nc != nil {
		t.Error("next chunk should be nil")
	}
	uuc.mu.Lock()
	if pieceIndex != 0 {
		t.Error("expected pieceIndex to be 0", pieceIndex)
	}
	if uuc.piecesRegistered != 0 {
		t.Errorf("piecesRegistered %v != %v", uuc.piecesRegistered, 0)
	}
	if uuc.workersRemaining != -3 {
		t.Errorf("workersRemaining %v != %v", uuc.workersRemaining, 0)
	}
	if len(uuc.unusedHosts) != 1 {
		t.Errorf("unusedHosts %v != %v", len(uuc.unusedHosts), 1)
	}
	for _, pu := range uuc.pieceUsage {
		// managedCleanUpUploadChunk sets all elements to true
		if !pu {
			t.Errorf("expected pu to be true")
		}
	}
	// Standby workers are woken.
	if len(uuc.workersStandby) != 0 {
		t.Errorf("expected %v standby workers got %v", 0, len(uuc.workersStandby))
	}
	uuc.mu.Unlock()
	wt.mu.Lock()
	if len(wt.unprocessedChunks) != 0 {
		t.Errorf("unprocessedChunks %v != %v", len(wt.unprocessedChunks), 0)
	}
	wt.mu.Unlock()
}

// TestProcessUploadChunk is a unit test for managedProcessUploadChunk.
func TestProcessUploadChunk(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}

	// some vars for the test.
	pieces := 10

	// helper method to create a valid upload chunk.
	chunk := func(wt *workerTester) *unfinishedUploadChunk {
		return &unfinishedUploadChunk{
			unusedHosts: map[string]struct{}{
				wt.staticHostPubKey.String(): {},
			},
			piecesNeeded:      pieces,
			piecesCompleted:   0,
			piecesRegistered:  0,
			pieceUsage:        make([]bool, pieces),
			released:          true,
			workersRemaining:  1,
			physicalChunkData: make([][]byte, pieces),
			logicalChunkData:  make([][]byte, pieces),
			availableChan:     make(chan struct{}),
			memoryNeeded:      uint64(pieces) * modules.SectorSize,
		}
	}

	t.Run("Basic", func(t *testing.T) {
		testProcessUploadChunkBasic(t, chunk)
	})
	t.Run("Completed", func(t *testing.T) {
		testProcessUploadChunkCompleted(t, chunk)
	})
	t.Run("CompletedOnCooldown", func(t *testing.T) {
		testProcessUploadChunkCompletedCooldown(t, chunk)
	})
	t.Run("NoHelpNeeded", func(t *testing.T) {
		testProcessUploadChunkNoHelpNeeded(t, chunk)
	})
	t.Run("NotACandidate", func(t *testing.T) {
		testProcessUploadChunkNotACandidate(t, chunk)
	})
	t.Run("NotACandidateOnCooldown", func(t *testing.T) {
		testProcessUploadChunk_NotACandidateCooldown(t, chunk)
	})
	t.Run("NotGoodForUpload", func(t *testing.T) {
		testProcessUploadChunkNotGoodForUpload(t, chunk)
	})
}
