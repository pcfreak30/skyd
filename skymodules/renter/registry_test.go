package renter

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
	"unsafe"

	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// TestReadResponseSet is a unit test for the readResponseSet.
func TestReadResponseSet(t *testing.T) {
	t.Parallel()

	// Get a set and fill it up completely.
	n := 10
	c := make(chan *jobReadRegistryResponse)
	set := newReadResponseSet(c, n)
	go func() {
		for i := 0; i < n; i++ {
			c <- &jobReadRegistryResponse{staticErr: fmt.Errorf("%v", i)}
		}
	}()
	if set.responsesLeft() != n {
		t.Fatal("wrong number of responses left", set.responsesLeft(), n)
	}

	// Calling Next should work until it's empty.
	i := 0
	for set.responsesLeft() > 0 {
		resp := set.next(context.Background())
		if resp == nil {
			t.Fatal("resp shouldn't be nil")
		}
		if resp.staticErr.Error() != fmt.Sprint(i) {
			t.Fatal("wrong error", resp.staticErr, fmt.Sprint(i))
		}
		i++
	}

	// Call Next one more time and close the context while doing so.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp := set.next(ctx)
	if resp != nil {
		t.Fatal("resp should be nil")
	}

	// Collect all values.
	resps := set.collect(context.Background())
	for i, resp := range resps {
		if resp.staticErr.Error() != fmt.Sprint(i) {
			t.Fatal("wrong error", resp.staticErr, fmt.Sprint(i))
		}
	}

	// Create another set that is collected right away.
	c = make(chan *jobReadRegistryResponse)
	set = newReadResponseSet(c, n)
	go func() {
		for i := 0; i < n; i++ {
			c <- &jobReadRegistryResponse{staticErr: fmt.Errorf("%v", i)}
		}
	}()
	resps = set.collect(context.Background())
	for i, resp := range resps {
		if resp.staticErr.Error() != fmt.Sprint(i) {
			t.Fatal("wrong error", resp.staticErr, fmt.Sprint(i))
		}
	}

	// Create another set that is collected halfway and then cancelled.
	c = make(chan *jobReadRegistryResponse)
	set = newReadResponseSet(c, n/2)
	ctx, cancel = context.WithCancel(context.Background())
	go func(cancel context.CancelFunc) {
		for i := 0; i < n/2; i++ {
			c <- &jobReadRegistryResponse{staticErr: fmt.Errorf("%v", i)}
		}
		cancel()
	}(cancel)
	resps = set.collect(ctx)
	if len(resps) != n/2 {
		t.Fatal("wrong number of resps", len(resps), n/2)
	}
	for i, resp := range resps {
		if resp.staticErr.Error() != fmt.Sprint(i) {
			t.Fatal("wrong error", resp.staticErr, fmt.Sprint(i))
		}
	}

	// Collect a set without responses with a closed context.
	set = newReadResponseSet(c, n)
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	resps = set.collect(ctx)
	if len(resps) != 0 {
		t.Fatal("resps should be empty", resps)
	}
}

// TestIsBetterReadRegistryResponse is a unit test for isBetterReadRegistryResponse.
func TestIsBetterReadRegistryResponse(t *testing.T) {
	t.Parallel()

	registryEntry := func(revision uint64, tweak crypto.Hash) *skymodules.RegistryEntry {
		v := modules.SignedRegistryValue{
			RegistryValue: modules.NewRegistryValue(tweak, nil, revision, modules.RegistryTypeWithoutPubkey),
		}
		srv := skymodules.NewRegistryEntry(types.SiaPublicKey{}, v)
		return &srv
	}

	tests := []struct {
		existing *jobReadRegistryResponse
		new      *jobReadRegistryResponse
		result   bool
		equal    bool
	}{
		{
			existing: nil,
			new:      &jobReadRegistryResponse{},
			result:   true,
			equal:    false,
		},
		{
			existing: &jobReadRegistryResponse{},
			new:      nil,
			result:   false,
			equal:    false,
		},
		{
			existing: nil,
			new:      nil,
			result:   false,
			equal:    true,
		},
		{
			existing: &jobReadRegistryResponse{
				staticSignedRegistryValue: nil,
			},
			new: &jobReadRegistryResponse{
				staticSignedRegistryValue: &skymodules.RegistryEntry{},
			},
			result: true,
			equal:  false,
		},
		{
			existing: &jobReadRegistryResponse{
				staticSignedRegistryValue: &skymodules.RegistryEntry{},
			},
			new: &jobReadRegistryResponse{
				staticSignedRegistryValue: nil,
			},
			result: false,
			equal:  false,
		},
		{
			existing: &jobReadRegistryResponse{
				staticSignedRegistryValue: nil,
			},
			new: &jobReadRegistryResponse{
				staticSignedRegistryValue: nil,
			},
			result: false,
			equal:  true,
		},
		{
			existing: &jobReadRegistryResponse{
				staticSignedRegistryValue: registryEntry(0, crypto.Hash{}),
			},
			new: &jobReadRegistryResponse{
				staticSignedRegistryValue: registryEntry(1, crypto.Hash{}),
			},
			result: true,
			equal:  false,
		},
		{
			existing: &jobReadRegistryResponse{
				staticSignedRegistryValue: registryEntry(1, crypto.Hash{}),
			},
			new: &jobReadRegistryResponse{
				staticSignedRegistryValue: registryEntry(0, crypto.Hash{}),
			},
			result: false,
			equal:  false,
		},
		{
			existing: &jobReadRegistryResponse{
				staticSignedRegistryValue: registryEntry(0, crypto.Hash{1, 2, 3}),
			},
			new: &jobReadRegistryResponse{
				staticSignedRegistryValue: registryEntry(0, crypto.Hash{3, 2, 1}),
			},
			result: true,
			equal:  false,
		},
		{
			existing: &jobReadRegistryResponse{
				staticSignedRegistryValue: registryEntry(0, crypto.Hash{3, 2, 1}),
			},
			new: &jobReadRegistryResponse{
				staticSignedRegistryValue: registryEntry(0, crypto.Hash{1, 2, 3}),
			},
			result: false,
			equal:  false,
		},
		{
			existing: &jobReadRegistryResponse{
				staticSignedRegistryValue: registryEntry(1, crypto.Hash{}),
			},
			new: &jobReadRegistryResponse{
				staticSignedRegistryValue: registryEntry(1, crypto.Hash{}),
			},
			result: false,
			equal:  true,
		},
	}

	for i, test := range tests {
		if test.new != nil {
			test.new.staticWorker = &worker{}
		}
		result, equal := isBetterReadRegistryResponse(test.existing, test.new)
		if result != test.result {
			t.Errorf("%v: wrong result expected %v but was %v", i, test.result, result)
		}
		if equal != test.equal {
			t.Errorf("%v: wrong result expected %v but was %v", i, test.result, result)
		}
	}
}

// TestRegReadCutoffWorkers is a unit test for regReadCutoffWorkers.
func TestRegReadCutoffWorkers(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create 3 workers. A faster one, a slower one and a malicious one.
	wOneSecond := &worker{
		atomicCache: unsafe.Pointer(&workerCache{
			staticMaliciousHost: false,
		}),
		staticHostPubKeyStr:     "onesec",
		staticJobReadRegistryDT: skymodules.NewDistributionTrackerStandard(),
	}
	wOneSecond.staticJobReadRegistryDT.AddDataPoint(time.Second)

	wTwoSeconds := &worker{
		atomicCache: unsafe.Pointer(&workerCache{
			staticMaliciousHost: false,
		}),
		staticHostPubKeyStr:     "twosecs",
		staticJobReadRegistryDT: skymodules.NewDistributionTrackerStandard(),
	}
	wTwoSeconds.staticJobReadRegistryDT.AddDataPoint(2 * time.Second)

	wMalicious := func() *worker {
		pks := hex.EncodeToString(fastrand.Bytes(8))
		w := &worker{
			atomicCache: unsafe.Pointer(&workerCache{
				staticMaliciousHost: true,
			}),
			staticHostPubKeyStr:     "malicious" + pks,
			staticJobReadRegistryDT: skymodules.NewDistributionTrackerStandard(),
		}
		w.staticJobReadRegistryDT.AddDataPoint(time.Millisecond)
		return w
	}

	// Test result. There should only be 1 worker in the result. The
	// malicious worker was trimmed, then the slow one was dropped so only
	// the fast one remains.
	for i := 0; i < 10; i++ {
		workerSet := []*worker{wMalicious(), wTwoSeconds, wMalicious(), wMalicious(), wOneSecond, wMalicious()}
		fastrand.Shuffle(len(workerSet), func(i, j int) {
			workerSet[i], workerSet[j] = workerSet[j], workerSet[i]
		})
		// Try multiple values for minWorkers.
		for minWorkers := 0; minWorkers < len(workerSet)*2; minWorkers++ {
			// Deep copy input.
			workers := append([]*worker{}, workerSet...)
			result := regReadCutoffWorkers(workers, minWorkers)
			// For minWorkers == 0 or 1, we expect 1 worker in the
			// result. For larger values we expect 2.
			expectedResult := 1
			if minWorkers > 1 {
				expectedResult = 2
			}
			if len(result) != expectedResult {
				t.Fatal("wrong length", len(result), expectedResult, minWorkers)
			}
			if _, ok := result[wOneSecond.staticHostPubKeyStr]; !ok {
				t.Fatal("wrong worker remaining", result)
			}
		}
	}
}
