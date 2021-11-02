package renter

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/opentracing/opentracing-go"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

var (
	// MaxRegistryReadTimeout is the default timeout used when reading from
	// the registry.
	MaxRegistryReadTimeout = build.Select(build.Var{
		Dev:      30 * time.Second,
		Standard: 5 * time.Minute,
		Testing:  30 * time.Second,
	}).(time.Duration)

	// DefaultRegistryHealthTimeout is the default timeout used when
	// requesting a registry entry's health.
	DefaultRegistryHealthTimeout = build.Select(build.Var{
		Dev:      30 * time.Second,
		Standard: 30 * time.Second,
		Testing:  10 * time.Second,
	}).(time.Duration)

	// DefaultRegistryUpdateTimeout is the default timeout used when updating
	// the registry.
	DefaultRegistryUpdateTimeout = build.Select(build.Var{
		Dev:      30 * time.Second,
		Standard: 5 * time.Minute,
		Testing:  3 * time.Second,
	}).(time.Duration)

	// ErrRegistryEntryNotFound is returned if all workers were unable to fetch
	// the entry.
	ErrRegistryEntryNotFound = errors.New("registry entry not found")

	// ErrRegistryLookupTimeout is similar to ErrRegistryEntryNotFound but it is
	// returned instead if the lookup timed out before all workers returned.
	ErrRegistryLookupTimeout = errors.New("registry entry not found within given time")

	// ErrRegistryUpdateInsufficientRedundancy is returned if updating the
	// registry failed due to running out of workers before reaching
	// MinUpdateRegistrySuccess successful updates.
	ErrRegistryUpdateInsufficientRedundancy = errors.New("registry update failed due reach sufficient redundancy")

	// ErrRegistryUpdateNoSuccessfulUpdates is returned if not a single update
	// was successful.
	ErrRegistryUpdateNoSuccessfulUpdates = errors.New("all registry updates failed")

	// ErrRegistryUpdateTimeout is returned when updating the registry was
	// aborted before reaching MinUpdateRegistrySucesses.
	ErrRegistryUpdateTimeout = errors.New("registry update timed out before reaching the minimum amount of updated hosts")

	// MinUpdateRegistrySuccesses is the minimum amount of success responses we
	// require from UpdateRegistry to be valid.
	MinUpdateRegistrySuccesses = build.Select(build.Var{
		Dev:      3,
		Standard: 3,
		Testing:  3,
	}).(int)

	// RegistryEntryRepairThreshold is the minimum amount of success
	// responses we require from a registry repair.
	RegistryEntryRepairThreshold = build.Select(build.Var{
		Dev:      10,
		Standard: 20,
		Testing:  4,
	}).(int)

	// ReadRegistryBackgroundTimeout is the amount of time a read registry job
	// can stay active in the background before being cancelled.
	ReadRegistryBackgroundTimeout = build.Select(build.Var{
		Dev:      time.Minute,
		Standard: 2 * time.Minute,
		Testing:  5 * time.Second,
	}).(time.Duration)

	// updateRegistryMemory is the amount of registry that UpdateRegistry will
	// request from the memory manager.
	updateRegistryMemory = uint64(20 * (1 << 10)) // 20kib

	// readRegistryMemory is the amount of registry that ReadRegistry will
	// request from the memory manager.
	readRegistryMemory = uint64(20 * (1 << 10)) // 20kib

	// updateRegistryBackgroundTimeout is the time an update registry job on a
	// worker stays active in the background after managedUpdateRegistry returns
	// successfully.
	updateRegistryBackgroundTimeout = time.Minute

	// readRegistrySeed is the first duration added to the registry stats after
	// creating it.
	// NOTE: This needs to be <= readRegistryBackgroundTimeout
	readRegistryStatsSeed = build.Select(build.Var{
		Dev:      30 * time.Second,
		Standard: 2 * time.Second,
		Testing:  5 * time.Second,
	}).(time.Duration)

	// minAwaitedCutoffWorkerPercentage is the percentage of cutoff workers
	// we wait for before cutting off a registry entry lookup.
	minAwaitedCutoffWorkersPercentage = 0.8 // 80%

	// minCutoffWorkers is the lower limit of workers we wait for when
	// looking up a registry entry.
	minCutoffWorkers = 10
)

// readResponseSet is a helper type which allows for returning a set of ongoing
// ReadRegistry responses.
type readResponseSet struct {
	c    <-chan *jobReadRegistryResponse
	left int

	readResps []*jobReadRegistryResponse
}

// newReadResponseSet creates a new set from a response chan and number of
// workers which are expected to write to that chan.
func newReadResponseSet(responseChan <-chan *jobReadRegistryResponse, numWorkers int) *readResponseSet {
	return &readResponseSet{
		c:         responseChan,
		left:      numWorkers,
		readResps: make([]*jobReadRegistryResponse, 0, numWorkers),
	}
}

// collect will collect all responses. It will block until it has received all
// of them or until the provided context is closed.
func (rrs *readResponseSet) collect(ctx context.Context) []*jobReadRegistryResponse {
	for rrs.responsesLeft() > 0 {
		resp := rrs.next(ctx)
		if resp == nil {
			break
		}
	}
	return rrs.readResps
}

// next returns the next available response. It will block until the response is
// received or the provided context is closed.
func (rrs *readResponseSet) next(ctx context.Context) *jobReadRegistryResponse {
	select {
	case <-ctx.Done():
		return nil
	case resp := <-rrs.c:
		rrs.readResps = append(rrs.readResps, resp)
		rrs.left--
		return resp
	}
}

// responsesLeft returns the number of responses that can still be fetched with
// Next.
func (rrs *readResponseSet) responsesLeft() int {
	return rrs.left
}

// RegistryEntryHealth returns the health of a registry entry specified by the
// spk and tweak.
func (r *Renter) RegistryEntryHealth(ctx context.Context, spk types.SiaPublicKey, tweak crypto.Hash) (skymodules.RegistryEntryHealth, error) {
	if err := r.tg.Add(); err != nil {
		return skymodules.RegistryEntryHealth{}, err
	}
	defer r.tg.Done()
	return r.managedRegistryEntryHealth(ctx, modules.DeriveRegistryEntryID(spk, tweak), &spk, &tweak)
}

// RegistryEntryHealthRID returns the health of a registry entry specified by
// the RID.
func (r *Renter) RegistryEntryHealthRID(ctx context.Context, rid modules.RegistryEntryID) (skymodules.RegistryEntryHealth, error) {
	if err := r.tg.Add(); err != nil {
		return skymodules.RegistryEntryHealth{}, err
	}
	defer r.tg.Done()
	return r.managedRegistryEntryHealth(ctx, rid, nil, nil)
}

// ReadRegistry starts a registry lookup on all available workers. The jobs have
// until ctx is closed to return a response. Otherwise the response with the
// highest revision number will be used.
func (r *Renter) ReadRegistry(ctx context.Context, spk types.SiaPublicKey, tweak crypto.Hash) (skymodules.RegistryEntry, error) {
	start := time.Now()
	srv, err := r.managedReadRegistry(ctx, modules.DeriveRegistryEntryID(spk, tweak), &spk, &tweak)
	if errors.Contains(err, ErrRegistryLookupTimeout) {
		err = errors.AddContext(err, fmt.Sprintf("timed out after %vs", time.Since(start).Seconds()))
	}
	return srv, err
}

// ReadRegistryRID starts a registry lookup on all available workers. The jobs
// have until ctx is closed to return a response. Otherwise the response with
// the highest revision number will be used.
func (r *Renter) ReadRegistryRID(ctx context.Context, rid modules.RegistryEntryID) (skymodules.RegistryEntry, error) {
	start := time.Now()
	srv, err := r.managedReadRegistry(ctx, rid, nil, nil)
	if errors.Contains(err, ErrRegistryLookupTimeout) {
		err = errors.AddContext(err, fmt.Sprintf("timed out after %vs", time.Since(start).Seconds()))
	}
	return srv, err
}

// UpdateRegistry updates the registries on all workers with the given
// registry value.
func (r *Renter) UpdateRegistry(ctx context.Context, spk types.SiaPublicKey, srv modules.SignedRegistryValue) error {
	// Block until there is memory available, and then ensure the memory gets
	// returned.
	// Since registry entries are very small we use a fairly generous multiple.
	if !r.staticRegistryMemoryManager.Request(ctx, updateRegistryMemory, memoryPriorityHigh) {
		return errors.New("timeout while waiting in job queue - server is busy")
	}
	defer r.staticRegistryMemoryManager.Return(updateRegistryMemory)

	// Start the UpdateRegistry jobs.
	return r.managedUpdateRegistry(ctx, spk, srv)
}

// UpdateRegistryMulti updates the registries on the given workers with the
// corresponding registry values.
func (r *Renter) UpdateRegistryMulti(ctx context.Context, srvs map[string]skymodules.RegistryEntry) error {
	// Block until there is memory available, and then ensure the memory gets
	// returned.
	// Since registry entries are very small we use a fairly generous multiple.
	if !r.staticRegistryMemoryManager.Request(ctx, updateRegistryMemory, memoryPriorityHigh) {
		return errors.New("timeout while waiting in job queue - server is busy")
	}
	defer r.staticRegistryMemoryManager.Return(updateRegistryMemory)

	// Start the UpdateRegistry jobs.
	workers := r.staticWorkerPool.callWorkers()
	return r.managedUpdateRegistryMulti(ctx, workers, srvs, MinUpdateRegistrySuccesses)
}

// managedRegistryEntryHealth reads an entry from all hosts on the network until
// ctx is closed. It will then find out the best entry and count how many times
// that entry was found on the network.
func (r *Renter) managedRegistryEntryHealth(ctx context.Context, rid modules.RegistryEntryID, spk *types.SiaPublicKey, tweak *crypto.Hash) (skymodules.RegistryEntryHealth, error) {
	// Start tracing.
	tracer := opentracing.GlobalTracer()
	span := tracer.StartSpan("managedRegistryEntryHealth")
	defer span.Finish()

	// Log some info about this trace.
	span.LogKV("RID", hex.EncodeToString(rid[:]))
	if spk != nil && tweak != nil {
		span.LogKV("SPK", spk.String())
		span.LogKV("Tweak", tweak.String())
	}

	// Block until there is memory available, and then ensure the memory gets
	// returned.
	// Since registry entries are very small we use a fairly generous multiple.
	if !r.staticRegistryMemoryManager.Request(ctx, readRegistryMemory, memoryPriorityHigh) {
		return skymodules.RegistryEntryHealth{}, errors.New("timeout while waiting in job queue - server is busy")
	}
	defer r.staticRegistryMemoryManager.Return(readRegistryMemory)

	// Specify a context for the background jobs. It will be closed as soon as
	// threadedHandleRegistryRepairs is done.
	backgroundCtx, backgroundCancel := context.WithCancel(r.tg.StopCtx())
	defer backgroundCancel()
	responseSet, launchedWorkers := r.managedLaunchReadRegistryWorkers(backgroundCtx, span, rid, spk, tweak)

	// If there are no workers remaining, fail early.
	if responseSet.left == 0 {
		return skymodules.RegistryEntryHealth{}, errors.AddContext(skymodules.ErrNotEnoughWorkersInWorkerPool, "cannot perform ReadRegistry")
	}

	// Collect as many responses as possible before the ctx is closed.
	var best *jobReadRegistryResponse
	resps := responseSet.collect(ctx)
	for _, resp := range resps {
		if resp.staticErr != nil {
			continue
		}
		if isBetter, _ := isBetterReadRegistryResponse(best, resp); isBetter {
			best = resp
		}
	}

	// If no entry was found return all 0s.
	if best == nil || best.staticSignedRegistryValue == nil {
		return skymodules.RegistryEntryHealth{}, nil
	}
	bestSRV := best.staticSignedRegistryValue

	// Get the cutoff workers and wait for 80% of them to finish.
	workersToWaitFor := regReadCutoffWorkers(launchedWorkers, minCutoffWorkers)
	awaitedWorkers := 0
	cutoff := int(float64(len(workersToWaitFor)) * minAwaitedCutoffWorkersPercentage)
	if cutoff == 0 {
		cutoff = len(workersToWaitFor)
	}
	if r.staticDeps.Disrupt("DelayRegistryHealthResponses") {
		cutoff = 0 // all workers will be conidered to come after the cutoff
	}

	// Count the number of responses that match the best one. We do so by
	// asking for the reason why the individual entries can't update the
	// best one. If ErrSameRevNum is returned, the entries are equal.
	var nTotal, nBestTotal, nBestTotalBeforeCutoff, nPrimary uint64
	for _, resp := range resps {
		// Check if response arrived before cutoff.
		beforeCutoff := awaitedWorkers < cutoff
		// Check if the response comes from one of the workers we wait
		// for.
		_, exists := workersToWaitFor[resp.staticWorker.staticHostPubKeyStr]
		if exists {
			awaitedWorkers++
		}
		if resp.staticSignedRegistryValue == nil {
			// Ignore responses without value.
			continue
		}
		nTotal++
		// We call ShouldUpdateWith without pubkey here because we don't
		// want to prefer primary entries here. We will explicitly check
		// for them afterwards.
		update, reason := bestSRV.ShouldUpdateWith(&resp.staticSignedRegistryValue.RegistryValue, types.SiaPublicKey{})
		if update {
			nPrimary++
		}
		if update || errors.Contains(reason, modules.ErrSameRevNum) {
			nBestTotal++
			// Check if it is a primary entry.
			if resp.staticSignedRegistryValue.IsPrimaryEntry(resp.staticWorker.staticHostPubKey) {
				nPrimary++
			}
			// Check if we have waited for enough workers.
			if beforeCutoff {
				nBestTotalBeforeCutoff++
			}
		}
	}
	return skymodules.RegistryEntryHealth{
		RevisionNumber:             bestSRV.Revision,
		NumEntries:                 nTotal,
		NumBestEntries:             nBestTotal,
		NumBestEntriesBeforeCutoff: nBestTotalBeforeCutoff,
		NumBestPrimaryEntries:      nPrimary,
	}, nil
}

// managedReadRegistry starts a registry lookup on all available workers. The
// jobs have 'timeout' amount of time to finish their jobs and return a
// response. Otherwise the response with the highest revision number will be
// used.
func (r *Renter) managedReadRegistry(ctx context.Context, rid modules.RegistryEntryID, spk *types.SiaPublicKey, tweak *crypto.Hash) (skymodules.RegistryEntry, error) {
	// Start tracing.
	tracer := opentracing.GlobalTracer()
	span := tracer.StartSpan("managedReadRegistry")
	defer span.Finish()

	// Check if we are subscribed to the entry first.
	subscribedRV, ok := r.staticSubscriptionManager.Get(rid)
	span.SetTag("cached", ok)
	if ok && subscribedRV != nil {
		// We are, no need to look it up.
		return *subscribedRV, nil
	}

	// Measure the time it takes to fetch the entry.
	startTime := time.Now()
	defer func() {
		r.staticRegistryReadStats.AddDataPoint(time.Since(startTime))
	}()

	// Log some info about this trace.
	span.LogKV("RID", hex.EncodeToString(rid[:]))
	if spk != nil && tweak != nil {
		span.LogKV("SPK", spk.String())
		span.LogKV("Tweak", tweak.String())
	}

	// Block until there is memory available, and then ensure the memory gets
	// returned.
	// Since registry entries are very small we use a fairly generous multiple.
	if !r.staticRegistryMemoryManager.Request(ctx, readRegistryMemory, memoryPriorityHigh) {
		return skymodules.RegistryEntry{}, errors.New("timeout while waiting in job queue - server is busy")
	}
	defer r.staticRegistryMemoryManager.Return(readRegistryMemory)

	// Specify a context for the background jobs. It will be closed as soon as
	// threadedHandleRegistryRepairs is done.
	backgroundCtx, backgroundCancel := context.WithCancel(r.tg.StopCtx())

	responseSet, launchedWorkers := r.managedLaunchReadRegistryWorkers(backgroundCtx, span, rid, spk, tweak)
	numWorkers := len(launchedWorkers)

	// If there are no workers remaining, fail early.
	if numWorkers == 0 {
		backgroundCancel()
		return skymodules.RegistryEntry{}, errors.AddContext(skymodules.ErrNotEnoughWorkersInWorkerPool, "cannot perform ReadRegistry")
	}

	defer func() {
		_ = r.tg.Launch(func() {
			defer backgroundCancel()

			// Handle registry repairs.
			r.threadedHandleRegistryRepairs(r.tg.StopCtx(), span, responseSet)
		})
	}()

	// Get the cutoff workers and wait for 80% of them to finish.
	workersToWaitFor := regReadCutoffWorkers(launchedWorkers, minCutoffWorkers)
	awaitedWorkers := 0
	cutoff := int(float64(len(workersToWaitFor)) * minAwaitedCutoffWorkersPercentage)
	if cutoff == 0 {
		cutoff = len(workersToWaitFor)
	}

	// Prevent reaching the cutoff point when ReadRegistryBlocking is
	// injected as a dependency.
	if r.staticDeps.Disrupt("ReadRegistryBlocking") {
		awaitedWorkers = -1
	}

	var best *jobReadRegistryResponse
	responses := 0
	// Wait for responses until either there are no responses left or until
	// we have waited for enough of our workersToWaitFor.
	for responseSet.responsesLeft() > 0 {
		// Check cancel condition and block for more responses.
		resp := responseSet.next(ctx)
		if resp == nil {
			break // context triggered
		}

		// Check if we have waited for enough workers.
		if awaitedWorkers >= cutoff {
			break // done
		}

		// Check if the response comes from one of the workers we wait
		// for.
		_, exists := workersToWaitFor[resp.staticWorker.staticHostPubKeyStr]
		if exists {
			awaitedWorkers++
		}

		// Increment responses.
		responses++

		// Ignore error responses and responses that returned no entry.
		if resp.staticErr != nil || resp.staticSignedRegistryValue == nil {
			continue
		}

		// Remember the best response.
		if isBetter, _ := isBetterReadRegistryResponse(best, resp); isBetter {
			best = resp
		}
	}

	// If we don't have a successful response and also not a response for every
	// worker, we timed out.
	noResponse := best == nil || best.staticSignedRegistryValue == nil
	if noResponse && responses < numWorkers {
		return skymodules.RegistryEntry{}, ErrRegistryLookupTimeout
	}

	// If we don't have a successful response but received a response from every
	// worker, we were unable to look up the entry.
	if noResponse {
		return skymodules.RegistryEntry{}, ErrRegistryEntryNotFound
	}
	return *best.staticSignedRegistryValue, nil
}

// managedLaunchReadRegistryWorkers launches read registry jobs on all available
// workers and returns a read response set which can be used to wait for the
// workers' responses.
func (r *Renter) managedLaunchReadRegistryWorkers(ctx context.Context, span opentracing.Span, rid modules.RegistryEntryID, spk *types.SiaPublicKey, tweak *crypto.Hash) (*readResponseSet, []*worker) {
	// Get the full list of workers and create a channel to receive all of the
	// results from the workers. The channel is buffered with one slot per
	// worker, so that the workers do not have to block when returning the
	// result of the job, even if this thread is not listening.
	workers := r.staticWorkerPool.callWorkers()
	staticResponseChan := make(chan *jobReadRegistryResponse, len(workers))

	// Filter out hosts that don't support the registry.
	numRegistryWorkers := 0
	for _, worker := range workers {
		cache := worker.staticCache()
		if build.VersionCmp(cache.staticHostVersion, minRegistryVersion) < 0 {
			continue
		}

		// check for price gouging
		//
		// TODO: use 'checkProjectDownloadGouging' gouging for some basic
		// protection. Should be replaced as part of the gouging overhaul.
		pt := worker.staticPriceTable().staticPriceTable
		err := checkProjectDownloadGouging(pt, cache.staticRenterAllowance)
		if err != nil {
			r.staticLog.Debugf("price gouging detected in worker %v, err: %v\n", worker.staticHostPubKeyStr, err)
			continue
		}

		jrr := worker.newJobReadRegistryEID(ctx, span, staticResponseChan, rid, spk, tweak)
		if !worker.staticJobReadRegistryQueue.callAdd(jrr) {
			// This will filter out any workers that are on cooldown or
			// otherwise can't participate in the project.
			continue
		}
		workers[numRegistryWorkers] = worker
		numRegistryWorkers++
	}
	workers = workers[:numRegistryWorkers]

	// If specified, increment numWorkers. This will cause the loop to never
	// exit without any of the context being closed since the response set won't
	// be able to read the last response.
	if r.staticDeps.Disrupt("ReadRegistryBlocking") {
		numRegistryWorkers++
	}

	return newReadResponseSet(staticResponseChan, numRegistryWorkers), workers
}

// managedUpdateRegistry updates the registries on all workers with the given
// registry value.
// NOTE: the input ctx only unblocks the call if it fails to hit the threshold
// before the timeout. It doesn't stop the update jobs. That's because we want
// to always make sure we update as many hosts as possble.
func (r *Renter) managedUpdateRegistry(ctx context.Context, spk types.SiaPublicKey, srv modules.SignedRegistryValue) (err error) {
	workers := r.staticWorkerPool.callWorkers()
	srvs := make(map[string]skymodules.RegistryEntry, len(workers))
	for _, w := range workers {
		srvs[w.staticHostPubKeyStr] = skymodules.NewRegistryEntry(spk, srv)
	}
	return r.managedUpdateRegistryMulti(ctx, workers, srvs, MinUpdateRegistrySuccesses)
}

// managedUpdateRegistry updates the registries on all workers with the given
// registry value.
// NOTE: the input ctx only unblocks the call if it fails to hit the threshold
// before the timeout. It doesn't stop the update jobs. That's because we want
// to always make sure we update as many hosts as possble.
func (r *Renter) managedUpdateRegistryMulti(ctx context.Context, workers []*worker, srvs map[string]skymodules.RegistryEntry, minUpdates int) (err error) {
	// Start tracing.
	start := time.Now()
	tracer := opentracing.GlobalTracer()
	span := tracer.StartSpan("managedUpdateRegistryMulti")
	defer span.Finish()

	// Check how many updates we expect at the very least.
	if minUpdates > len(srvs) {
		minUpdates = len(srvs)
	}

	// Verify the signatures before updating the hosts.
	for _, srv := range srvs {
		if err := srv.Verify(); err != nil {
			return errors.AddContext(err, "managedUpdateRegistry: failed to verify signature of entry")
		}
	}
	// Create a channel to receive all of the
	// results from the workers. The channel is buffered with one slot per
	// worker, so that the workers do not have to block when returning the
	// result of the job, even if this thread is not listening.
	staticResponseChan := make(chan *jobUpdateRegistryResponse, len(workers))
	span.LogKV("workers", len(workers))

	// Create a context to continue updating registry values in the background.
	updateTimeoutCtx, updateTimeoutCancel := context.WithTimeout(r.tg.StopCtx(), updateRegistryBackgroundTimeout)
	defer func() {
		if err != nil {
			// If managedUpdateRegistry fails the caller is going to assume that
			// updating the value failed. Don't let any jobs linger in that
			// case.
			updateTimeoutCancel()
		}
	}()

	// Filter out hosts that don't support the registry.
	numRegistryWorkers := 0
	for _, worker := range workers {
		// Filter out workers that we don't have an srv for.
		srv, exists := srvs[worker.staticHostPubKeyStr]
		if !exists {
			continue
		}
		// Check if worker is good for updating the registry.
		if !isWorkerGoodForRegistryUpdate(worker) {
			continue
		}

		// Create the job.
		jrr := worker.newJobUpdateRegistry(updateTimeoutCtx, span, staticResponseChan, srv.PubKey, srv.SignedRegistryValue)
		if !worker.staticJobUpdateRegistryQueue.callAdd(jrr) {
			// This will filter out any workers that are on cooldown or
			// otherwise can't participate in the project.
			continue
		}
		workers[numRegistryWorkers] = worker
		numRegistryWorkers++
	}
	workers = workers[:numRegistryWorkers]
	// If there are no workers remaining, fail early.
	if len(workers) < minUpdates {
		return errors.AddContext(skymodules.ErrNotEnoughWorkersInWorkerPool, "cannot perform UpdateRegistry")
	}

	workersLeft := len(workers)
	responses := 0
	successfulResponses := 0

	var respErrs error
	for successfulResponses < minUpdates && workersLeft+successfulResponses >= minUpdates {
		// Check deadline.
		var resp *jobUpdateRegistryResponse
		select {
		case <-ctx.Done():
			// Timeout reached.
			return ErrRegistryUpdateTimeout
		case resp = <-staticResponseChan:
		}

		// Decrement the number of workers.
		workersLeft--

		// Increment number of responses.
		responses++

		// Ignore error responses except for invalid revision errors.
		if resp.staticErr != nil {
			// If we receive an error indicating that a better entry exists on
			// the network we immediately return an error. That's because our
			// update won't be able to change the consensus of the network on
			// the latest entry.
			if modules.IsRegistryEntryExistErr(resp.staticErr) {
				return resp.staticErr
			}
			respErrs = errors.Compose(respErrs, resp.staticErr)
			continue
		}

		// Increment successful responses.
		successfulResponses++
	}

	// Check if we ran out of workers.
	if successfulResponses == 0 {
		r.staticLog.Print("RegistryUpdate failed with 0 successful responses: ", respErrs)
		return errors.Compose(err, ErrRegistryUpdateNoSuccessfulUpdates)
	}
	if successfulResponses < minUpdates {
		r.staticLog.Printf("RegistryUpdate failed with %v < %v successful responses: %v", successfulResponses, minUpdates, respErrs)
		return errors.Compose(err, ErrRegistryUpdateInsufficientRedundancy)
	}
	r.staticRegWriteStats.AddDataPoint(time.Since(start))
	return nil
}

// isBetterReadRegistryResponse returns true if resp2 is a better response than
// resp1 and false otherwise. Better means that the response either has a higher
// revision number, more work or was faster.
func isBetterReadRegistryResponse(resp1, resp2 *jobReadRegistryResponse) (bool, bool) {
	// Check for nil response.
	if resp2 == nil {
		// A nil entry never replaces an existing entry.
		return false, resp1 == resp2
	} else if resp1 == nil {
		// A non-nil entry always replaces a nil entry.
		return true, resp1 == resp2
	}
	// Same but with the entries.
	srv1 := resp1.staticSignedRegistryValue
	srv2 := resp2.staticSignedRegistryValue
	if srv2 == nil {
		return false, srv1 == srv2
	} else if srv1 == nil {
		return true, srv1 == srv2
	}
	// Compare entries. We pass the empty key here since we don't care about
	// whether the entry is a primary or secondary one.
	shouldUpdate, updateErr := srv1.ShouldUpdateWith(&srv2.RegistryValue, types.SiaPublicKey{})

	// If the entry is not capable of updating the existing one and both entries
	// have the same revision number, use the time.
	if !shouldUpdate && errors.Contains(updateErr, modules.ErrSameRevNum) {
		return resp2.staticCompleteTime.Before(resp1.staticCompleteTime), true
	}

	// Otherwise we return the result
	return shouldUpdate, false
}

// threadedHandleRegistryRepairs waits for all provided read registry programs
// to finish and updates all workers from responses which either didn't provide
// the highest revision number, or didn't have the entry at all.
func (r *Renter) threadedHandleRegistryRepairs(ctx context.Context, parentSpan opentracing.Span, responseSet *readResponseSet) {
	if err := r.tg.Add(); err != nil {
		return
	}
	defer r.tg.Done()

	span := opentracing.StartSpan("threadedHandleRegistryRepairs", opentracing.ChildOf(parentSpan.Context()))
	defer span.Finish()

	// Collect all responses.
	ctx, cancel := context.WithTimeout(ctx, ReadRegistryBackgroundTimeout)
	defer cancel()
	resps := responseSet.collect(ctx)
	if resps == nil {
		return // nothing to do
	}

	// Find the best response.
	var best *jobReadRegistryResponse
	for _, resp := range resps {
		if better, _ := isBetterReadRegistryResponse(best, resp); better {
			best = resp
		}
	}

	// If no entry was found we can't do anything.
	if best == nil || best.staticSignedRegistryValue == nil {
		return
	}
	bestSRV := best.staticSignedRegistryValue

	// Register the update to make sure we don't try again if a value is rapidly
	// polled before this update is done.
	rid := modules.DeriveRegistryEntryID(bestSRV.PubKey, bestSRV.Tweak)
	r.ongoingRegistryRepairsMu.Lock()
	_, exists := r.ongoingRegistryRepairs[rid]
	if !exists {
		r.ongoingRegistryRepairs[rid] = struct{}{}
	}
	r.ongoingRegistryRepairsMu.Unlock()
	if exists {
		return // ongoing update found
	}

	// Unregister the update once done.
	defer func() {
		r.ongoingRegistryRepairsMu.Lock()
		delete(r.ongoingRegistryRepairs, rid)
		r.ongoingRegistryRepairsMu.Unlock()
	}()

	// Figure out how many entries with the highest revision are out there.
	upToDateHosts := make(map[string]struct{})
	for _, resp := range resps {
		if resp == nil || resp.staticSignedRegistryValue == nil || resp.staticErr != nil {
			continue
		}
		if resp.staticSignedRegistryValue.Revision != best.staticSignedRegistryValue.Revision {
			continue
		}
		upToDateHosts[resp.staticWorker.staticHostPubKeyStr] = struct{}{}
	}

	// Check if the entry requires repairing.
	if len(upToDateHosts) >= RegistryEntryRepairThreshold {
		return
	}

	// Prepare the updates.
	workers := r.staticWorkerPool.callWorkers()
	srvs := make(map[string]skymodules.RegistryEntry, len(workers))
	for _, w := range workers {
		if _, upToDate := upToDateHosts[w.staticHostPubKeyStr]; upToDate {
			continue
		}
		srvs[w.staticHostPubKeyStr] = *best.staticSignedRegistryValue
	}

	// Update the registry.
	err := r.managedUpdateRegistryMulti(ctx, workers, srvs, RegistryEntryRepairThreshold-len(upToDateHosts))
	if err != nil {
		r.staticLog.Debugln("threadedHandleRegistryRepairs: failed to update registry", err)
	}
}

// isWorkerGoodForRegistryUpdate is a helper function which returns 'true' if a
// worker can be used for updating the registry.
func isWorkerGoodForRegistryUpdate(worker *worker) bool {
	cache := worker.staticCache()
	if build.VersionCmp(cache.staticHostVersion, minRegistryVersion) < 0 {
		return false
	}
	// Skip !goodForUpload workers.
	if !cache.staticContractUtility.GoodForUpload {
		return false
	}

	// check for price gouging
	pt := worker.staticPriceTable().staticPriceTable
	err := checkUploadGougingPT(pt, cache.staticRenterAllowance)
	if err != nil {
		return false
	}
	return true
}

// regReadCutoffWorkers returns the workers to wait for before considering the
// result good enough amongst the provided launched workers.
func regReadCutoffWorkers(workers []*worker, minWorkers int) map[string]*worker {
	// Filter malicious hosts.
	i := 0
	for _, w := range workers {
		if w.staticCache().staticMaliciousHost {
			continue
		}
		workers[i] = w
		i++
	}
	workers = workers[:i]
	// Sort workers by their estimate.
	sort.Slice(workers, func(i, j int) bool {
		return workers[i].ReadRegCutoffEstimate() < workers[j].ReadRegCutoffEstimate()
	})
	// Drop slowest 50% but don't go below the min.
	newLen := len(workers) / 2
	if newLen < minWorkers && minWorkers <= len(workers) {
		newLen = minWorkers
	} else if newLen < minWorkers && minWorkers > len(workers) {
		newLen = len(workers)
	}
	workers = workers[:newLen]

	// Put remaining ones in map.
	remaining := make(map[string]*worker, len(workers))
	for _, w := range workers {
		remaining[w.staticHostPubKeyStr] = w
	}
	return remaining
}
