package host

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
)

// TestRPCConcurrentCalls makes a whole set of concurrent RPC calls to the host
// from multiple renters and verifies all of them succeed without error
func TestRPCConcurrentCalls(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// determine a reasonable timeout
	var timeout time.Duration
	if build.VLONG {
		timeout = 5 * time.Minute
	} else {
		timeout = 30 * time.Second
	}

	// setup the host
	ht, err := newHostTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := ht.Close()
		if err != nil {
			t.Error(err)
		}
	}()
	his := ht.host.InternalSettings()

	// create 10 renter host pairs
	pairs := make([]*renterHostPair, 10)
	for i := range pairs {
		pair, err := newRenterHostPairCustomHostTester(ht)
		if err != nil {
			t.Fatal(err)
		}

		// note we can not simply call the `Close` function on the
		// renterHostPair because all renter host pairs share the same host
		// tester
		defer func() {
			err := pair.staticRenterMux.Close()
			if err != nil {
				t.Error(err)
			}
		}()

		// prefund the EAs
		funding := his.MaxEphemeralAccountBalance.Div64(1e5)
		_, err = pair.FundEphemeralAccount(funding, true)
		if err != nil {
			t.Fatal(err)
		}
		pairs[i] = pair
	}

	// create a sector on every pair and index them for later use, we will use
	// these to generate random MDM programs on the fly
	sectorRoots := make(map[types.FileContractID]crypto.Hash)
	for _, pair := range pairs {
		root, _, err := pair.addRandomSector()
		if err != nil {
			t.Fatal(err)
		}
		sectorRoots[pair.staticFCID] = root
	}

	// start the timer
	finishedChan := make(chan struct{})
	time.AfterFunc(timeout, func() {
		close(finishedChan)
	})

	// collect rpc stats
	stats := &rpcStats{}

	numThreads := 10
	var wg sync.WaitGroup
	for _, p := range pairs {
		// spin up a goroutine for every pair that just tries to recover from a
		// set of errors which are expected to happen, if we can recover from
		// them, the test should not be considered as failed
		recoverChan := make(chan error)
		go recoverFromError(t, p, stats, recoverChan, finishedChan)

		wg.Add(numThreads)
		for i := 0; i < numThreads; i++ {
			go func(pair *renterHostPair, recoverChan chan error) {
				defer wg.Done()
			LOOP:
				for {
					select {
					case <-finishedChan:
						break LOOP
					default:
					}

					// get a random program to execute
					root := sectorRoots[pair.staticFCID]
					p, cost, trackRPC := randomMDMProgram(pair, root)
					epr := modules.RPCExecuteProgramRequest{
						FileContractID:    pair.staticFCID,
						Program:           p.program,
						ProgramDataLength: uint64(len(p.data)),
					}

					// execute it and handle the error
					_, _, err := pair.ExecuteProgram(epr, p.data, cost, false)
					if err != nil {
						recoverChan <- err
					} else {
						trackRPC(stats)
					}
				}
			}(p, recoverChan)
		}
	}
	<-finishedChan
	wg.Wait()

	t.Logf("In %.f seconds, on %d cores across %d threads, the following RPCs completed: %s\n", timeout.Seconds(), runtime.NumCPU(), numThreads, stats.String())
}

// randomMDMProgram is a helper function that randomly creates an MDM program.
// It returns either a full sector read, partial sector read or has sector
// program. Alongside the program and cost it returns a function that updates
// the appropriate RPC tracker in the stats object.
func randomMDMProgram(pair *renterHostPair, sectorRoot crypto.Hash) (program mdmProgram, cost types.Currency, updateStats func(stats *rpcStats)) {
	pt := pair.PriceTable()
	var expectedDLBandwidth uint64
	var expectedULBandwidth uint64

	switch fastrand.Intn(3) {
	case 0:
		program = newRandomReadSectorProgram(pt, sectorRoot, true)
		expectedDLBandwidth = 10220
		expectedULBandwidth = 18980
		updateStats = func(stats *rpcStats) { stats.trackReadSector(true) }
	case 1:
		program = newRandomReadSectorProgram(pt, sectorRoot, false)
		expectedDLBandwidth = 10220
		expectedULBandwidth = 18980
		updateStats = func(stats *rpcStats) { stats.trackReadSector(false) }
	case 2:
		program = newRandomHasSectorProgram(pt, sectorRoot)
		expectedDLBandwidth = 7300
		expectedULBandwidth = 18980
		updateStats = func(stats *rpcStats) { stats.trackHasSector() }
	}

	dlcost := pt.DownloadBandwidthCost.Mul64(expectedDLBandwidth)
	ulcost := pt.UploadBandwidthCost.Mul64(expectedULBandwidth)
	cost = program.cost.Add(dlcost).Add(ulcost)
	return
}

// recoverFromError is a helper function that takes a channel over which errors
// are sent. These errors occurred by executing RPC calls on the pair, and they
// might be expected errors from which we want to recover. Examples of such
// errors are expired price tables, or out of balance errors.
func recoverFromError(t *testing.T, pair *renterHostPair, stats *rpcStats, errChan chan error, finishedChan chan struct{}) {
	his := pair.ht.host.InternalSettings()
	funding := his.MaxEphemeralAccountBalance.Div64(1e5)

	for err := range errChan {
		select {
		case <-finishedChan:
			continue
		default:
		}

		// try to recover from insufficient balance
		var recovered bool
		if !recovered && strings.Contains(err.Error(), ErrBalanceInsufficient.Error()) {
			_, err = pair.FundEphemeralAccount(funding, false)
			stats.trackFundEA()
			recovered = err == nil
		}

		// try to recover from expired PT
		if !recovered && (strings.Contains(err.Error(), ErrPriceTableExpired.Error()) || strings.Contains(err.Error(), ErrPriceTableNotFound.Error())) {
			var payByFC bool
			// try using an EA, but fall back to contract payment, this
			// ensures the price table gets updated, and attempts to do
			// it in the fastest way possible
			err = pair.UpdatePriceTable(false)
			if err != nil {
				pair.UpdatePriceTable(true)
				payByFC = true
			}
			stats.trackUpdatePT(payByFC)
			recovered = err == nil
		}

		// ignore max balance exceeded & cancelled deposits
		if !recovered && (strings.Contains(err.Error(), ErrBalanceMaxExceeded.Error()) || strings.Contains(err.Error(), ErrDepositCancelled.Error())) {
			err = nil
		}

		if err != nil {
			t.Error(err)
		}
	}
}

// mdmProgram is a helper struct that contains all necessary details to execute
// an MDM program to read a full (or partial) sector from the host
type mdmProgram struct {
	program modules.Program
	data    []byte
	cost    types.Currency
}

// newRandomReadSectorProgram is a helper function that creates a program to
// read data from the host. If full is set to true, the program will perform a
// full sector read, if it is false we return a program that reads a random
// couple of segments at random offset.
func newRandomReadSectorProgram(pt *modules.RPCPriceTable, root crypto.Hash, full bool) mdmProgram {
	var offset uint64
	var length uint64
	if full {
		offset = 0
		length = modules.SectorSize
	} else {
		offset = uint64(fastrand.Uint64n((modules.SectorSize/crypto.SegmentSize)-1) * crypto.SegmentSize)
		length = uint64(crypto.SegmentSize) * (fastrand.Uint64n(5) + 1)
	}
	p, data, cost, _, _, _ := newReadSectorProgram(length, offset, root, pt)
	return mdmProgram{
		program: p,
		data:    data,
		cost:    cost,
	}
}

// newRandomHasSectorProgram is a helper function that creates a random sector
// on the host and returns a program that returns whether or not the host has
// this sector.
func newRandomHasSectorProgram(pt *modules.RPCPriceTable, root crypto.Hash) mdmProgram {
	p, data, cost, _, _, _ := newHasSectorProgram(root, pt)
	return mdmProgram{
		program: p,
		data:    data,
		cost:    cost,
	}
}

// rpcStats is a helper struct to collect the amount of times an RPC has been
// performed.
type rpcStats struct {
	atomicUpdatePTCallsFC                uint64
	atomicUpdatePTCallsEA                uint64
	atomicFundAccountCalls               uint64
	atomicExecuteProgramFullReadCalls    uint64
	atomicExecuteProgramPartialReadCalls uint64
	atomicExecuteHasSectorCalls          uint64
}

// trackUpdatePT tracks an update price table call
func (rs *rpcStats) trackUpdatePT(payByFC bool) {
	if payByFC {
		atomic.AddUint64(&rs.atomicUpdatePTCallsFC, 1)
	} else {
		atomic.AddUint64(&rs.atomicUpdatePTCallsEA, 1)
	}
}

// trackFundEA tracks a fund ephemeral account call
func (rs *rpcStats) trackFundEA() {
	atomic.AddUint64(&rs.atomicFundAccountCalls, 1)
}

// trackReadSector tracks an execute MDM program call with a read sector program
// it tracks a full read or partial read depending on the given 'full' parameter
func (rs *rpcStats) trackReadSector(full bool) {
	if full {
		atomic.AddUint64(&rs.atomicExecuteProgramFullReadCalls, 1)
	} else {
		atomic.AddUint64(&rs.atomicExecuteProgramPartialReadCalls, 1)
	}
}

// trackHasSector tracks an execute MDM program call with a has sector program
func (rs *rpcStats) trackHasSector() {
	atomic.AddUint64(&rs.atomicExecuteHasSectorCalls, 1)
}

// String prints a string representation of the RPC stats object
func (rs *rpcStats) String() string {
	numPTFC := atomic.LoadUint64(&rs.atomicUpdatePTCallsFC)
	numPTEA := atomic.LoadUint64(&rs.atomicUpdatePTCallsEA)
	numPT := numPTFC + numPTEA

	numEA := atomic.LoadUint64(&rs.atomicFundAccountCalls)
	numFS := atomic.LoadUint64(&rs.atomicExecuteProgramFullReadCalls)
	numPS := atomic.LoadUint64(&rs.atomicExecuteProgramPartialReadCalls)
	numHS := atomic.LoadUint64(&rs.atomicExecuteHasSectorCalls)
	total := numPT + numEA + numFS + numPS + numHS

	return fmt.Sprintf(`
	Total RPC Calls: %d
 
	UpdatePriceTableRPC: %d (%d by FC)
	FundEphemeralAccountRPC: %d
	ExecuteMDMProgramRPC (Full Sector Read): %d
	ExecuteMDMProgramRPC (Partial Sector Read): %d
	ExecuteMDMProgramRPC (Has Sector): %d
`, total, numPT, numPTFC, numEA, numFS, numPS, numHS)
}
