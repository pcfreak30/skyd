package renter

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/opentracing/opentracing-go"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/siatest/dependencies"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// TestReadRegistryJob tests running a ReadRegistry job on a host.
func TestReadRegistryJob(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	wt, err := newWorkerTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := wt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create a registry value.
	rv, spk, _ := randomRegistryValue()

	// Run the UpdateRegistry job.
	err = wt.UpdateRegistry(context.Background(), spk, rv)
	if err != nil {
		t.Fatal(err)
	}

	// Create a ReadRegistry job to read the entry.
	lookedUpRV, err := wt.ReadRegistry(context.Background(), testSpan(), spk, rv.Tweak)
	if err != nil {
		t.Fatal(err)
	}

	// The entries should match.
	if !reflect.DeepEqual(lookedUpRV.SignedRegistryValue, rv) {
		t.Log(lookedUpRV)
		t.Log(rv)
		t.Fatal("entries don't match")
	}

	// Do it again without the pubkey or tweak. Should also work.
	lookedUpRV, err = wt.ReadRegistryEID(context.Background(), testSpan(), modules.DeriveRegistryEntryID(spk, rv.Tweak))
	if err != nil {
		t.Fatal(err)
	}

	// The entries should match.
	if !reflect.DeepEqual(lookedUpRV.SignedRegistryValue, rv) {
		t.Log(lookedUpRV)
		t.Log(rv)
		t.Fatal("entries don't match")
	}
}

// TestReadRegistryJob tests running a ReadRegistry job on a host by directly
// creating the job and passing it to the queue.
func TestReadRegistryJobManual(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	wt, err := newWorkerTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := wt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create a registry value.
	rv, spk, _ := randomRegistryValue()
	eid := modules.DeriveRegistryEntryID(spk, rv.Tweak)

	// Prepare a span.
	span := opentracing.GlobalTracer().StartSpan(t.Name())

	// Run the UpdateRegistry job.
	err = wt.UpdateRegistry(context.Background(), spk, rv)
	if err != nil {
		t.Fatal(err)
	}

	// Create a ReadRegistry job to read the entry.
	respChan := make(chan *jobReadRegistryResponse)
	rrj := wt.newJobReadRegistry(context.Background(), span, respChan, spk, rv.Tweak)
	if !wt.staticJobReadRegistryQueue.callAdd(rrj) {
		t.Fatal("failed")
	}
	resp := <-respChan
	if resp.staticErr != nil {
		t.Fatal(resp.staticErr)
	}
	lookedUpRV := resp.staticSignedRegistryValue

	// The entries should match.
	if !reflect.DeepEqual(lookedUpRV.SignedRegistryValue, rv) {
		t.Log(lookedUpRV)
		t.Log(rv)
		t.Fatal("entries don't match")
	}

	// The worker, eid, spk and tweak should be set.
	if resp.staticSignedRegistryValue.Tweak != rv.Tweak {
		t.Fatal("wrong tweak")
	}
	if !resp.staticSignedRegistryValue.PubKey.Equals(spk) {
		t.Fatal("wrong spk")
	}
	if resp.staticEID != eid {
		t.Fatal("wrong eid")
	}
	if resp.staticWorker != wt.worker {
		t.Fatal("wrong worker")
	}

	// Do it again without the pubkey or tweak. Should also work.
	rrj = wt.newJobReadRegistryEID(context.Background(), span, respChan, eid, nil, nil)
	if !wt.staticJobReadRegistryQueue.callAdd(rrj) {
		t.Fatal("failed")
	}
	resp = <-respChan
	if resp.staticErr != nil {
		t.Fatal(resp.staticErr)
	}
	lookedUpRV = resp.staticSignedRegistryValue

	// The entries should match.
	if !reflect.DeepEqual(lookedUpRV.SignedRegistryValue, rv) {
		t.Log(lookedUpRV)
		t.Log(rv)
		t.Fatal("entries don't match")
	}

	// The worker and eid should be set.
	emptyHash := crypto.Hash{}
	if resp.staticSignedRegistryValue.Tweak == emptyHash {
		t.Fatal("wrong tweak")
	}
	emptyKey := types.SiaPublicKey{}
	if reflect.DeepEqual(resp.staticSignedRegistryValue.PubKey, emptyKey) {
		t.Fatal("wrong spk")
	}
	if resp.staticEID != eid {
		t.Fatal("wrong eid")
	}
	if resp.staticWorker != wt.worker {
		t.Fatal("wrong worker")
	}
}

// TestReadRegistryInvalidCached checks that a host can't provide an older
// revision for an entry if we have seen a more recent one from it in the past
// already.
func TestReadRegistryInvalidCached(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	deps := dependencies.NewDependencyRegistryUpdateNoOp()
	deps.Disable()
	wt, err := newWorkerTesterCustomDependency(t.Name(), skymodules.SkydProdDependencies, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := wt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create a registry value.
	rv, spk, sk := randomRegistryValue()

	// Run the UpdateRegistry job.
	err = wt.UpdateRegistry(context.Background(), spk, rv)
	if err != nil {
		t.Fatal(err)
	}

	// Run the UpdateRegistry job again. This time it's a no-op. The renter
	// won't know and increment the revision in the cache.
	rv.Revision++
	rv = rv.Sign(sk)
	deps.Enable()
	err = wt.UpdateRegistry(context.Background(), spk, rv)
	deps.Disable()
	if err != nil {
		t.Fatal(err)
	}

	// Read the value. This should result in an error due to the host providing a
	// lower revision number than expected.
	_, err = wt.ReadRegistry(context.Background(), testSpan(), spk, rv.Tweak)
	if !errors.Contains(err, errHostCheating) {
		t.Fatal(err)
	}

	// Make sure there is a recent error and cooldown.
	wt.staticJobReadRegistryQueue.mu.Lock()
	if !errors.Contains(wt.staticJobReadRegistryQueue.recentErr, errHostCheating) {
		t.Fatal("wrong recent error", wt.staticJobReadRegistryQueue.recentErr)
	}
	if wt.staticJobReadRegistryQueue.cooldownUntil == (time.Time{}) {
		t.Fatal("cooldownUntil is not set")
	}
	wt.staticJobReadRegistryQueue.mu.Unlock()
}

// TestReadRegistryCacheUpdated tests the registry cache functionality as used
// by ReadRegistry jobs.
func TestReadRegistryCachedUpdated(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	deps := dependencies.NewDependencyRegistryUpdateNoOp()
	deps.Disable()
	wt, err := newWorkerTesterCustomDependency(t.Name(), skymodules.SkydProdDependencies, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := wt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create a registry value.
	rv, spk, sk := randomRegistryValue()
	sid := modules.DeriveRegistryEntryID(spk, rv.Tweak)

	// Run the UpdateRegistry job.
	err = wt.UpdateRegistry(context.Background(), spk, rv)
	if err != nil {
		t.Fatal(err)
	}

	// Make sure the value is in the cache.
	rev, cached := wt.staticRegistryCache.Get(sid)
	if !cached || !reflect.DeepEqual(rev, rv.RegistryValue) {
		t.Fatal("invalid cached value")
	}

	// Delete the value from the cache.
	wt.staticRegistryCache.Delete(modules.DeriveRegistryEntryID(spk, rv.Tweak))
	_, cached = wt.staticRegistryCache.Get(sid)
	if cached {
		t.Fatal("value wasn't removed")
	}

	// Read the registry value.
	readRV, err := wt.ReadRegistry(context.Background(), testSpan(), spk, rv.Tweak)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rv, readRV.SignedRegistryValue) {
		t.Fatal("read value doesn't match set value")
	}

	// Revision should be cached again.
	rev, cached = wt.staticRegistryCache.Get(sid)
	if !cached || !reflect.DeepEqual(rev, rv.RegistryValue) {
		t.Fatal("invalid cached value")
	}

	// Update the revision again.
	rv2 := rv
	rv2.Revision++
	rv2 = rv2.Sign(sk)
	err = wt.UpdateRegistry(context.Background(), spk, rv2)
	if err != nil {
		t.Fatal(err)
	}

	// Make sure the value is in the cache.
	rev, cached = wt.staticRegistryCache.Get(sid)
	if !cached || !reflect.DeepEqual(rev, rv2.RegistryValue) {
		t.Fatal("invalid cached value")
	}

	// Set the cache to the earlier revision of rv.
	wt.staticRegistryCache.Set(sid, rv, true)
	rev, cached = wt.staticRegistryCache.Get(sid)
	if !cached || !reflect.DeepEqual(rev, rv.RegistryValue) {
		t.Fatal("invalid cached value")
	}

	// Read the registry value. Should be rv2.
	readRV, err = wt.ReadRegistry(context.Background(), testSpan(), spk, rv2.Tweak)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rv2, readRV.SignedRegistryValue) {
		t.Fatal("read value doesn't match set value")
	}

	// Revision from rv2 should be cached again.
	rev, cached = wt.staticRegistryCache.Get(sid)
	if !cached || !reflect.DeepEqual(rev, rv2.RegistryValue) {
		t.Fatal("invalid cached value")
	}
}
