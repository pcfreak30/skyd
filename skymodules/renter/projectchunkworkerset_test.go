package renter

import (
	"context"
	"fmt"
	"io/ioutil"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentracing/opentracing-go"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/persist"
	"go.sia.tech/siad/types"
)

// TestPCWS verifies the functionality of the PCWS.
func TestPCWS(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}

	// create a worker tester
	wt, err := newWorkerTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := wt.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	t.Run("basic", func(t *testing.T) { testBasic(t, wt) })
	t.Run("multiple", func(t *testing.T) { testMultiple(t, wt) })
	t.Run("newPCWSByRoots", testNewPCWSByRoots)
}

// testBasic verifies the PCWS using a simple setup with a single host, looking
// for a single sector.
func testBasic(t *testing.T, wt *workerTester) {
	// create a ctx with test span
	ctx := opentracing.ContextWithSpan(context.Background(), testSpan())

	// create a random sector
	sectorData := fastrand.Bytes(int(modules.SectorSize))
	sectorRoot := crypto.MerkleRoot(sectorData)

	// create a passthrough EC and a passhtrough cipher key
	ptec := skymodules.NewPassthroughErasureCoder()
	ptck, err := crypto.NewSiaKey(crypto.TypePlain, nil)
	if err != nil {
		t.Fatal(err)
	}

	// define a helper function that waits for an update
	waitForUpdate := func(ws *pcwsWorkerState) {
		ws.mu.Lock()
		wu := ws.registerForWorkerUpdate()
		ws.mu.Unlock()
		select {
		case <-wu:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out")
		}
	}

	// create PCWS
	pcws, err := wt.staticRenter.newPCWSByRoots(ctx, []crypto.Hash{sectorRoot}, ptec, ptck, 0)
	if err != nil {
		t.Fatal(err)
	}

	// get the current state update
	pcws.mu.Lock()
	ws := pcws.workerState
	wslt := pcws.workerStateLaunchTime
	pcws.mu.Unlock()

	// verify launch time was set
	unset := time.Time{}
	if wslt == unset {
		t.Fatal("launch time not set")
	}

	// register for worker update and wait
	waitForUpdate(ws)

	// verify resolved and unresolved workers
	ws.mu.Lock()
	resolved := ws.resolvedWorkers
	numResolved := len(ws.resolvedWorkers)
	numUnresolved := len(ws.unresolvedWorkers)
	ws.mu.Unlock()

	if numResolved != 1 || numUnresolved != 0 {
		t.Fatal("unexpected")
	}
	if len(resolved[0].pieceIndices) != 0 {
		t.Fatal("unexpected")
	}

	// add the sector to the host
	err = wt.host.AddSector(sectorRoot, sectorData)
	if err != nil {
		t.Fatal(err)
	}

	// reset the launch time - allowing us to force a state update
	pcws.mu.Lock()
	pcws.workerStateLaunchTime = unset
	pcws.mu.Unlock()
	err = pcws.managedTryUpdateWorkerState()
	if err != nil {
		t.Fatal(err)
	}

	// get the current worker state (!important)
	ws = pcws.managedWorkerState()

	// register for worker update and wait
	waitForUpdate(ws)

	// verify resolved and unresolved workers
	ws.mu.Lock()
	resolved = ws.resolvedWorkers
	ws.mu.Unlock()

	// expect we found sector at index 0
	if len(resolved) != 1 || len(resolved[0].pieceIndices) != 1 {
		t.Fatal("unexpected", len(resolved), len(resolved[0].pieceIndices))
	}
}

// testMultiple verifies the PCWS for a multiple sector lookup on multiple
// hosts.
func testMultiple(t *testing.T, wt *workerTester) {
	// create a ctx with test span
	ctx := opentracing.ContextWithSpan(context.Background(), testSpan())

	// create a helper function that adds a host
	numHosts := 0
	addHost := func() modules.Host {
		testdir := filepath.Join(wt.rt.dir, fmt.Sprintf("host%d", numHosts))
		host, err := wt.rt.addCustomHost(testdir, modules.ProdDependencies)
		if err != nil {
			t.Fatal(err)
		}
		numHosts++
		return host
	}

	// create a helper function that adds a random sector on a given host
	addSector := func(h modules.Host) crypto.Hash {
		// create a random sector
		sectorData := fastrand.Bytes(int(modules.SectorSize))
		sectorRoot := crypto.MerkleRoot(sectorData)

		// add the sector to the host
		err := h.AddSector(sectorRoot, sectorData)
		if err != nil {
			t.Fatal(err)
		}
		return sectorRoot
	}

	// create a helper function that waits for an update
	waitForUpdate := func(ws *pcwsWorkerState) {
		ws.mu.Lock()
		wu := ws.registerForWorkerUpdate()
		ws.mu.Unlock()
		select {
		case <-wu:
		case <-time.After(time.Minute):
			t.Fatal("timed out")
		}
	}

	// create a helper function that compares uint64 slices for equality
	isEqualTo := func(a, b []uint64) bool {
		if len(a) != len(b) {
			return false
		}
		for i, v := range a {
			if v != b[i] {
				return false
			}
		}
		return true
	}

	h1 := addHost()
	h2 := addHost()
	h3 := addHost()

	h1PK := h1.PublicKey().String()
	h2PK := h2.PublicKey().String()
	h3PK := h3.PublicKey().String()

	r1 := addSector(h1)
	r2 := addSector(h1)
	r3 := addSector(h2)
	r4 := addSector(h3)
	r5 := crypto.MerkleRoot(fastrand.Bytes(int(modules.SectorSize)))
	roots := []crypto.Hash{r1, r2, r3, r4, r5}

	// create an EC and a passhtrough cipher key
	ec, err := skymodules.NewRSCode(1, 4)
	if err != nil {
		t.Fatal(err)
	}
	ptck, err := crypto.NewSiaKey(crypto.TypePlain, nil)
	if err != nil {
		t.Fatal(err)
	}

	// wait until the renter has a worker for all hosts
	err = build.Retry(600, 100*time.Millisecond, func() error {
		ws, err := wt.staticRenter.WorkerPoolStatus()
		if err != nil {
			t.Fatal(err)
		}
		if ws.NumWorkers < 3 {
			_, err = wt.rt.miner.AddBlock()
			if err != nil {
				t.Fatal(err)
			}

			return errors.New("workers not ready yet")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// wait until we're certain all workers are fit for duty
	err = build.Retry(100, 100*time.Millisecond, func() error {
		ws, err := wt.staticRenter.WorkerPoolStatus()
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range ws.Workers {
			if w.AccountStatus.AvailableBalance.IsZero() ||
				!w.PriceTableStatus.Active ||
				w.MaintenanceOnCooldown {
				return errors.New("worker is not ready yet")
			}
		}
		return nil
	})

	// create PCWS
	pcws, err := wt.staticRenter.newPCWSByRoots(ctx, roots, ec, ptck, 0)
	if err != nil {
		t.Fatal(err)
	}
	ws := pcws.managedWorkerState()

	// wait until all workers have resolved
	numWorkers := len(ws.staticRenter.staticWorkerPool.callWorkers())
	for {
		waitForUpdate(ws)
		ws.mu.Lock()
		numResolved := len(ws.resolvedWorkers)
		ws.mu.Unlock()
		if numResolved == numWorkers {
			break
		}
	}

	// fetch piece indices per host
	ws.mu.Lock()
	resolved := ws.resolvedWorkers
	ws.mu.Unlock()
	for _, rw := range resolved {
		var expected []uint64
		var hostname string
		switch rw.worker.staticHostPubKeyStr {
		case h1PK:
			expected = []uint64{0, 1}
			hostname = "host1"
		case h2PK:
			expected = []uint64{2}
			hostname = "host2"
		case h3PK:
			expected = []uint64{3}
			hostname = "host3"
		default:
			hostname = "other"
			continue
		}

		if !isEqualTo(rw.pieceIndices, expected) {
			t.Error("unexpected pieces", hostname, rw.worker.staticHostPubKeyStr[64:], rw.pieceIndices, rw.err)
		}
	}
}

// testNewPCWSByRoots verifies the 'newPCWSByRoots' constructor function and its
// edge cases
func testNewPCWSByRoots(t *testing.T) {
	r := new(Renter)
	r.staticWorkerPool = new(workerPool)

	// create a ctx with test span
	ctx := opentracing.ContextWithSpan(context.Background(), testSpan())

	// create random roots
	var root1 crypto.Hash
	var root2 crypto.Hash
	fastrand.Read(root1[:])
	fastrand.Read(root2[:])
	roots := []crypto.Hash{root1, root2}

	// create a passthrough EC and a passhtrough cipher key
	ptec := skymodules.NewPassthroughErasureCoder()
	ptck, err := crypto.NewSiaKey(crypto.TypePlain, nil)
	if err != nil {
		t.Fatal(err)
	}

	// verify basic case
	_, err = r.newPCWSByRoots(ctx, roots[:1], ptec, ptck, 0)
	if err != nil {
		t.Fatal("unexpected")
	}

	// verify the case where we the amount of roots does not equal num pieces
	// defined in the erasure coder
	_, err = r.newPCWSByRoots(ctx, roots, ptec, ptck, 0)
	if err == nil || !strings.Contains(err.Error(), "but erasure coder specifies 1 pieces") {
		t.Fatal(err)
	}

	// verify the legacy case where 1-of-N only needs 1 root
	ec, err := skymodules.NewRSCode(1, 10)
	if err != nil {
		t.Fatal("unexpected")
	}

	// verify the amount of roots provided **does not** equal num pieces,
	// usually causing an error
	if len(roots[:1]) == ec.NumPieces() {
		t.Fatal("unexpected")
	}
	_, err = r.newPCWSByRoots(ctx, roots[:1], ec, ptck, 0)
	if err != nil {
		t.Fatal("unexpected")
	}

	// verify passing nil for the master key returns an error
	_, err = r.newPCWSByRoots(ctx, roots[:1], ptec, nil, 0)
	if err == nil {
		t.Fatal("unexpected")
	}
}

// TestProjectChunkWorsetSet_managedLaunchWorker probes the
// 'managedLaunchWorker' function on the PCWS.
func TestProjectChunkWorsetSet_managedLaunchWorker(t *testing.T) {
	t.Parallel()

	// create EC + key
	ec := skymodules.NewPassthroughErasureCoder()
	ck, err := crypto.NewSiaKey(crypto.TypePlain, nil)
	if err != nil {
		t.Fatal(err)
	}

	// create renter
	renter := new(Renter)
	renter.staticWorkerPool = new(workerPool)

	// create PCWS
	pcws := &projectChunkWorkerSet{
		staticChunkIndex:   0,
		staticErasureCoder: ec,
		staticMasterKey:    ck,
		staticPieceRoots:   []crypto.Hash{},

		staticCtx:    context.Background(),
		staticRenter: renter,
	}

	// create PCWS worker state
	ws := &pcwsWorkerState{
		unresolvedWorkers: make(map[string]*pcwsUnresolvedWorker),
		staticRenter:      pcws.staticRenter,
	}

	// mock the worker
	w := new(worker)
	w.newCache()
	w.newPriceTable()
	w.newMaintenanceState()
	w.initJobHasSectorQueue()

	// give it a name and set an initial estimate on the HS queue
	seed := 123 * time.Second
	w.staticJobHasSectorQueue.staticDT.AddDataPoint(seed)
	w.staticJobHasSectorQueue.weightedJobTime = float64(seed)
	w.staticHostPubKeyStr = "myworker"

	// ensure PT is valid
	w.staticPriceTable().staticExpiryTime = time.Now().Add(time.Hour)

	// launch the worker - shouldn't work
	responseChan := make(chan *jobHasSectorResponse, 0)
	err = pcws.managedLaunchWorker(w, responseChan, ws)
	if !errors.Contains(err, errEstimateAboveMax) {
		t.Fatal(err)
	}

	// verify the worker didn't launch.
	uw, exists := ws.unresolvedWorkers["myworker"]
	if exists {
		t.Log(ws.unresolvedWorkers)
		t.Fatal("unexpected")
	}

	// launch the worker
	w.staticJobHasSectorQueue.staticDT = skymodules.NewDistributionTrackerStandard()
	w.staticJobHasSectorQueue.staticDT.AddDataPoint(pcwsHasSectorTimeout)
	w.staticJobHasSectorQueue.weightedJobTime = float64(pcwsHasSectorTimeout)
	responseChan = make(chan *jobHasSectorResponse, 0)
	err = pcws.managedLaunchWorker(w, responseChan, ws)
	if err != nil {
		t.Fatal(err)
	}

	// verify the worker launched successfully
	uw, exists = ws.unresolvedWorkers["myworker"]
	if !exists {
		t.Log(ws.unresolvedWorkers)
		t.Fatal("unexpected")
	}

	// verify the expected dur matches the initial queue estimate
	expectedDur := time.Until(uw.staticExpectedResolvedTime)
	expectedDurInS := math.Round(expectedDur.Seconds())
	if expectedDurInS != pcwsHasSectorTimeout.Seconds() {
		t.Log(expectedDurInS)
		t.Fatal("unexpected")
	}

	// tweak the maintenancestate, putting it on a cooldown
	minuteFromNow := time.Now().Add(time.Minute)
	w.staticMaintenanceState.cooldownUntil = minuteFromNow
	err = pcws.managedLaunchWorker(w, responseChan, ws)
	if err != nil {
		t.Fatal(err)
	}

	// verify the cooldown is being reflected in the estimate
	uw = ws.unresolvedWorkers["myworker"]
	expectedDur = time.Until(uw.staticExpectedResolvedTime)
	expectedDurInS = math.Round(expectedDur.Seconds())
	if expectedDurInS != pcwsHasSectorTimeout.Seconds()+60 {
		t.Log(expectedDurInS)
		t.Fatal("unexpected")
	}
}

// TestWaitForResult is a unit test for the worker state's WaitForResults
// method.
func TestWaitForResult(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a plain worker state.
	ws := &pcwsWorkerState{
		unresolvedWorkers: make(map[string]*pcwsUnresolvedWorker),
	}

	// Add unresolved worker.
	_, pk1 := crypto.GenerateKeyPair()
	hpk1 := types.Ed25519PublicKey(pk1)
	ws.unresolvedWorkers[hpk1.String()] = &pcwsUnresolvedWorker{}

	// Wait for its result in a separate goroutine.
	done := make(chan struct{})
	var result []*pcwsWorkerResponse
	go func() {
		result = ws.WaitForResults(context.Background())
		close(done)
	}()

	// Wait some time. Should still not be done.
	select {
	case <-done:
		t.Fatal("wait finished")
	case <-time.After(100 * time.Millisecond):
	}

	// Move the unresolved workers to resolved.
	ws.mu.Lock()
	delete(ws.unresolvedWorkers, hpk1.String())
	resolvedWorker := &pcwsWorkerResponse{err: errors.New("test")}
	ws.resolvedWorkers = append(ws.resolvedWorkers, resolvedWorker)

	// Close the update chan.
	ws.closeUpdateChans()
	ws.mu.Unlock()

	// Should be done now.
	select {
	case <-time.After(100 * time.Millisecond):
		t.Fatal("still not done")
	case <-done:
	}

	// Result should have length 1.
	if len(result) != 1 {
		t.Fatal("unexpected", len(result))
	}

	// Add another unresolved worker.
	_, pk2 := crypto.GenerateKeyPair()
	hpk2 := types.Ed25519PublicKey(pk2)
	ws.unresolvedWorkers[hpk2.String()] = &pcwsUnresolvedWorker{}

	// Use a timeout this time.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Measure the time the call takes.
	start := time.Now()
	result = ws.WaitForResults(ctx)

	// Should have taken at least 100ms to return.
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("returned too early")
	}

	// Result should have length 1 again. Because we got the same resolved
	// worker.
	if len(result) != 1 {
		t.Fatal("unexpected", len(result))
	}
}

// newTestProjectChunkWorkerSet returns a PCWS used for testing
func newTestProjectChunkWorkerSet() *projectChunkWorkerSet {
	return newCustomTestProjectChunkWorkerSet(skymodules.NewRSSubCodeDefault())
}

// newCustomTestProjectChunkWorkerSet returns a PCWS used for testing and allows
// to pass a custom erasure coder
func newCustomTestProjectChunkWorkerSet(ec skymodules.ErasureCoder) *projectChunkWorkerSet {
	// create a passhtrough cipher key
	ck, err := crypto.NewSiaKey(crypto.TypePlain, nil)
	if err != nil {
		return nil
	}

	// create renter
	renter := new(Renter)
	renter.staticBaseSectorDownloadStats = skymodules.NewSectorDownloadStats()
	renter.staticFanoutSectorDownloadStats = skymodules.NewSectorDownloadStats()

	// create discard logger
	logger, err := persist.NewLogger(ioutil.Discard)
	if err != nil {
		return nil
	}
	renter.staticLog = logger

	// create PCWS manually
	return &projectChunkWorkerSet{
		workerState: &pcwsWorkerState{
			unresolvedWorkers: make(map[string]*pcwsUnresolvedWorker),
			staticRenter:      renter,
		},

		staticChunkIndex:   0,
		staticErasureCoder: ec,
		staticMasterKey:    ck,
		staticPieceRoots:   make([]crypto.Hash, ec.NumPieces()),

		staticCtx:    context.Background(),
		staticRenter: renter,
	}
}
