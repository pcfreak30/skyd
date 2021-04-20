package renter

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
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

// TestThreadedAddResponseSetRetry tests that threadedAddResponseSet will try to
// fetch the retrieved revision from other workers to prevent slow hosts that
// are updated from skewing the stats.
func TestThreadedAddResponseSetRetry(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a renter.
	rt, err := newRenterTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Add 4 hosts.
	var hosts []modules.Host
	for i := 0; i < 4; i++ {
		h, err := rt.addHost(fmt.Sprintf("host%v", i))
		if err != nil {
			t.Fatal(err)
		}
		hosts = append(hosts, h)
	}
	// Close 3 of them at the end of the test.
	for i := 0; i < len(hosts)-1; i++ {
		defer func(i int) {
			if err := hosts[i].Close(); err != nil {
				t.Fatal(err)
			}
		}(i)
	}

	// Set an allowance.
	err = rt.renter.staticHostContractor.SetAllowance(skymodules.DefaultAllowance)
	if err != nil {
		t.Fatal(err)
	}

	// Wait until we got 4 workers in the pool.
	numRetries := 0
	var workers []*worker
	err = build.Retry(1000, 100*time.Millisecond, func() error {
		if numRetries%10 == 0 {
			_, err = rt.miner.AddBlock()
			if err != nil {
				t.Fatal(err)
			}
		}
		numRetries++
		workers = rt.renter.staticWorkerPool.callWorkers()
		if len(workers) != len(hosts) {
			return fmt.Errorf("%v != %v", len(workers), len(hosts))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create a random registry entry and a higher revision.
	srvLower, spk, sk := randomRegistryValue()
	srvHigher := srvLower
	srvHigher.Revision++
	srvHigher = srvHigher.Sign(sk)

	w1 := workers[0]
	w2 := workers[1]
	w3 := workers[2]
	w4 := workers[3]

	// Update first two hosts with the higher revision. The rest doesn't know.
	for i := 0; i < 2; i++ {
		err = workers[i].UpdateRegistry(context.Background(), spk, srvHigher)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Take host 4 offline.
	if err := hosts[3].Close(); err != nil {
		t.Fatal(err)
	}

	// Create a fake response set where w1 returns the lower entry and w2, w3
	// and w4 the higher one.
	startTime := time.Now()
	c := make(chan *jobReadRegistryResponse)
	close(c)
	rrs := &readResponseSet{
		c:    c,
		left: 0,
		readResps: []*jobReadRegistryResponse{
			// Super fast response but no response value.
			{
				staticSPK:                 &spk,
				staticTweak:               &srvLower.Tweak,
				staticCompleteTime:        startTime.Add(time.Millisecond),
				staticSignedRegistryValue: nil, // no response
				staticWorker:              nil, // will be ignored
			},
			// Super fast response but error.
			{
				staticSPK:          &spk,
				staticTweak:        &srvLower.Tweak,
				staticCompleteTime: startTime.Add(time.Millisecond),
				staticErr:          errors.New("failed"),
				staticWorker:       nil, // will be ignored
			},
			// Fast response with lower revision number.
			{
				staticSPK:                 &spk,
				staticTweak:               &srvLower.Tweak,
				staticCompleteTime:        startTime.Add(20 * time.Millisecond),
				staticSignedRegistryValue: &srvLower,
				staticWorker:              w1,
			},
			// Slow response with higher revision number.
			{
				staticSPK:                 &spk,
				staticTweak:               &srvHigher.Tweak,
				staticCompleteTime:        startTime.Add(ReadRegistryBackgroundTimeout),
				staticSignedRegistryValue: &srvHigher,
				staticWorker:              w2,
			},
			// Super fast response but won't know the entry later.
			{
				staticSPK:                 &spk,
				staticTweak:               &srvLower.Tweak,
				staticCompleteTime:        startTime.Add(time.Millisecond),
				staticSignedRegistryValue: &srvHigher,
				staticWorker:              w3,
			},
			// Super fast response but will be offline later.
			{
				staticSPK:                 &spk,
				staticTweak:               &srvLower.Tweak,
				staticCompleteTime:        startTime.Add(time.Millisecond),
				staticSignedRegistryValue: &srvHigher,
				staticWorker:              w4,
			},
		},
	}

	// Create a logger.
	buf := bytes.NewBuffer(nil)
	log, err := persist.NewLogger(buf)
	if err != nil {
		t.Fatal(err)
	}

	// Reset the rrs by setting all buckets and the total to 0.
	for i := range rt.renter.staticRRS.staticBuckets {
		rt.renter.staticRRS.staticBuckets[i] = 0
	}
	rt.renter.staticRRS.total = 0

	// Run the method.
	rt.renter.staticRRS.threadedAddResponseSet(context.Background(), startTime, rrs, log)

	// Check p99. The estimate should match the bucket that the timing got
	// inserted to. That means the estimate should be smaller than the
	// completion time of the slow response with the high revision number.
	d := rt.renter.staticRRS.Estimate()[0]
	if d >= ReadRegistryBackgroundTimeout {
		t.Fatal("d is too high", d)
	}

	// The buffer should contain the two messages printed when a worker either
	// failed to respond or retrieved a nil value.
	logs := buf.String()
	if !strings.Contains(logs, "threadedAddResponseSet: worker that successfully retrieved a registry value failed to retrieve it again") {
		t.Log("logs", logs)
		t.Fatal("didn't log first line")
	}
	if !strings.Contains(logs, "threadedAddResponseSet: worker that successfully retrieved a non-nil registry value returned nil") {
		t.Log("logs", logs)
		t.Fatal("didn't log second line")
	}
}
