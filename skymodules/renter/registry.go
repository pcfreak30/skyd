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
	"go.sia.tech/siad/persist"
	"go.sia.tech/siad/types"
)

var (
	// MaxRegistryReadTimeout is the default timeout used when reading from
	// the registry.
	MaxRegistryReadTimeout = build.Select(build.Var{
		Dev:      30 * time.Second,
		Standard: 5 * time.Minute,
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

	// useHighestRevDefaultTimeout is the amount of time before ReadRegistry
	// will stop waiting for additional responses from hosts and accept the
	// response with the highest rev number. The timer starts when we get the
	// first response and doesn't reset afterwards.
	useHighestRevDefaultTimeout = 100 * time.Millisecond

	// updateRegistryBackgroundTimeout is the time an update registry job on a
	// worker stays active in the background after managedUpdateRegistry returns
	// successfully.
	updateRegistryBackgroundTimeout = time.Minute

	// readRegistryStatsDebugThreshold is a threshold for the best lookup's
	// timing. If the timing is above the threshold, we log some additional
	// information to figure out why it took that long.
	readRegistryStatsDebugThreshold = 10 * time.Second

	// readRegistrySeed is the first duration added to the registry stats after
	// creating it.
	// NOTE: This needs to be <= readRegistryBackgroundTimeout
	readRegistryStatsSeed = build.Select(build.Var{
		Dev:      30 * time.Second,
		Standard: 2 * time.Second,
		Testing:  5 * time.Second,
	}).(time.Duration)

	// minRegistryReadTimeout is the minimum timeout we give a read registry
	// request to finish.
	minRegistryReadTimeout = build.Select(build.Var{
		Dev:      200 * time.Millisecond,
		Standard: 300 * time.Millisecond,
		Testing:  20 * time.Millisecond,
	}).(time.Duration)
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

// threadedAddResponseSet adds a response set to the stats. This includes
// waiting for all responses to arrive and then identifying the best one by
// choosing the fastest one with the highest revision. After that it works its
// way from the fastest to the slowest worker and asks them for a revision
// again until a revision is found that is >= the best one. This is referred to
// as secondBest. If we find a valid secondBest we use that timing, otherwise we
// stick to the best. That way we get the fastest response for the best entry
// even if an update caused a slow worker to be considered best at first.
func (r *Renter) threadedAddResponseSet(ctx context.Context, parentSpan opentracing.Span, startTime time.Time, rrs *readResponseSet, l *persist.Logger) {
	responseCtx, responseCancel := context.WithTimeout(ctx, ReadRegistryBackgroundTimeout)
	defer responseCancel()

	span := opentracing.StartSpan("threadedAddResponseSet", opentracing.ChildOf(parentSpan.Context()))
	defer span.Finish()

	// Get all responses.
	resps := rrs.collect(responseCtx)
	if resps == nil {
		return // nothing to do
	}

	// Find the fastest timing with the highest revision number.
	var best *jobReadRegistryResponse
	var goodResps []*jobReadRegistryResponse
	for _, resp := range resps {
		if resp.staticErr != nil {
			continue
		}
		// Remember all responses that returned a valid entry.
		if resp.staticSignedRegistryValue != nil {
			goodResps = append(goodResps, resp)
		}
		// If the new response is better, remember it.
		if isBetterReadRegistryResponse(best, resp) {
			best = resp
			continue
		}
	}

	// No successful responses. We can't update the stats.
	if best == nil || best.staticSignedRegistryValue == nil {
		return
	}
	span.LogKV("revision", best.staticSignedRegistryValue.Revision)

	// Drop any responses from goodResps that were slower or equal to best.
	for i := 0; i < len(goodResps); i++ {
		if goodResps[i].staticCompleteTime.Before(best.staticCompleteTime) {
			continue // nothing to do for faster response
		}
		// Replace element to remove with the last one and drop it.
		goodResps[i] = goodResps[len(goodResps)-1]
		goodResps = goodResps[:len(goodResps)-1]
		i--
	}

	// Sort the good responses by completion time.
	sort.Slice(goodResps, func(i, j int) bool {
		return goodResps[i].staticCompleteTime.Before(goodResps[j].staticCompleteTime)
	})

	// Limit the time we spend on finding the second best response.
	secondBestCtx, secondBestCancel := context.WithTimeout(ctx, ReadRegistryBackgroundTimeout)
	defer secondBestCancel()

	// Determine the secondBest response by asking all workers with valid responses
	// again, one-by-one. The secondBest is the first that returns a revision >= the
	// best one.
	var secondBest *skymodules.RegistryEntry
	var d2 time.Duration
	for _, resp := range goodResps {
		// Otherwise look up the same entry.
		var srv *skymodules.RegistryEntry
		var err error
		if resp.staticSPK == nil || resp.staticTweak == nil {
			srv, err = resp.staticWorker.ReadRegistryEID(secondBestCtx, span, resp.staticEID)
		} else {
			srv, err = resp.staticWorker.ReadRegistry(secondBestCtx, span, *resp.staticSPK, *resp.staticTweak)
		}
		// Ignore responses with errors and without revision.
		if err != nil {
			l.Printf("threadedAddResponseSet: worker that successfully retrieved a registry value failed to retrieve it again: %v", err)
			continue
		}
		if srv == nil {
			l.Printf("threadedAddResponseSet: worker that successfully retrieved a non-nil registry value returned nil")
			continue
		}
		// If the revision is >= the best one, we are done.
		if srv.Revision >= best.staticSignedRegistryValue.Revision {
			d2 = resp.staticCompleteTime.Sub(startTime)
			secondBest = srv
			break
		}
	}

	// Get the duration of the best lookup.
	d := best.staticCompleteTime.Sub(startTime)

	// If the duration of the best was very long, print some additional info.
	if d > readRegistryStatsDebugThreshold {
		// base msg
		logStr := fmt.Sprintf("threadedAddResponseSet: WARN: best lookup on host %v took longer than %v seconds", best.staticWorker.staticHostPubKeyStr, readRegistryStatsDebugThreshold)
		srv := best.staticSignedRegistryValue
		// Add revision
		if srv != nil {
			logStr += fmt.Sprintf(" - revision: %v", srv.Revision)
		}
		// Add eid
		logStr += fmt.Sprintf(" - eid: %v", best.staticEID)
		// Add spk and tweak
		if best.staticSPK != nil && best.staticTweak != nil {
			logStr += fmt.Sprintf(" - spk: %v - tweak: %v", best.staticSPK.String(), best.staticTweak.String())
		}
		// Add number of good/total responses.
		logStr += fmt.Sprintf(" - goodResps: %v/%v", len(goodResps), len(resps))
		// Log string.
		l.Print(logStr)
	}

	// If we found a secondBest, use that instead.
	span.LogKV("best", d.Milliseconds())
	if secondBest != nil {
		span.SetTag("secondbest", true)
		span.LogKV("secondbest", d2.Milliseconds())
		l.Printf("threadedAddResponseSet: replaced best with secondBest duration %v -> %v (revs: %v -> %v)", d, d2, best.staticSignedRegistryValue.Revision, secondBest.Revision)
		d = d2
	} else {
		span.SetTag("secondbest", false)
		l.Printf("threadedAddResponseSet: using best duration %v (secondBest: %v, nil: %v)", d, d2, secondBest == nil)
	}

	// Sanity check duration is not zero.
	if d == 0 {
		err := errors.New("zero duration was passed to AddDatum")
		build.Critical(err)
		return
	}

	// The error is ignored since it only returns an error if the measurement is
	// outside of the 5 minute bounds the stats were created with.
	span.LogKV("datapoint", d.Milliseconds())
	if d.Milliseconds() > 2000 {
		span.SetTag("speed", "vslow")
	} else if d.Milliseconds() > 200 {
		span.SetTag("speed", "slow")
	}
	r.staticRegReadStats.AddDataPoint(d)
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
func (r *Renter) UpdateRegistry(spk types.SiaPublicKey, srv modules.SignedRegistryValue, timeout time.Duration) error {
	// Create a context. If the timeout is greater than zero, have the context
	// expire when the timeout triggers.
	ctx := r.tg.StopCtx()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(r.tg.StopCtx(), timeout)
		defer cancel()
	}

	// Block until there is memory available, and then ensure the memory gets
	// returned.
	// Since registry entries are very small we use a fairly generous multiple.
	if !r.staticRegistryMemoryManager.Request(ctx, updateRegistryMemory, memoryPriorityHigh) {
		return errors.New("timeout while waiting in job queue - server is busy")
	}
	defer r.staticRegistryMemoryManager.Return(updateRegistryMemory)

	// Start the UpdateRegistry jobs.
	err := r.managedUpdateRegistry(ctx, spk, srv)
	if errors.Contains(err, ErrRegistryUpdateTimeout) {
		err = errors.AddContext(err, fmt.Sprintf("timed out after %vs", timeout.Seconds()))
	}
	return err
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

	// Get the start time before launching the workers to avoid negative
	// datapoints later.
	startTime := time.Now()

	// Specify a context for the background jobs. It will be closed as soon as
	// threadedAddResponseSet is done.
	backgroundCtx, backgroundCancel := context.WithCancel(r.tg.StopCtx())
	responseSet := r.managedLaunchReadRegistryWorkers(backgroundCtx, span, rid, spk, tweak)

	// If there are no workers remaining, fail early.
	if responseSet.left == 0 {
		backgroundCancel()
		return skymodules.RegistryEntryHealth{}, errors.AddContext(skymodules.ErrNotEnoughWorkersInWorkerPool, "cannot perform ReadRegistry")
	}

	// Add the response set to the stats after this method is done.
	defer func() {
		_ = r.tg.Launch(func() {
			r.threadedAddResponseSet(r.tg.StopCtx(), span, startTime, responseSet, r.staticLog)
			backgroundCancel()
		})
	}()

	// Collect as many responses as possible before the ctx is closed.
	var best *jobReadRegistryResponse
	resps := responseSet.collect(ctx)
	for _, resp := range resps {
		if isBetterReadRegistryResponse(best, resp) {
			best = resp
		}
	}

	// If no entry was found return all 0s.
	if best == nil || best.staticSignedRegistryValue == nil {
		return skymodules.RegistryEntryHealth{}, nil
	}
	bestSRV := best.staticSignedRegistryValue

	// Count the number of responses that match the best one. We do so by
	// asking for the reason why the individual entries can't update the
	// best one. If ErrSameRevNum is returned, the entries are equal.
	var nTotal, nBestTotal, nPrimary uint64
	for _, resp := range resps {
		if resp.staticSignedRegistryValue == nil {
			// Ignore responses without value.
			continue
		}
		nTotal++
		// We call ShouldUpdateWith without pubkey here because we don't
		// want to prefer primary entries here. We will explicitly check
		// for them afterwards.
		_, reason := bestSRV.ShouldUpdateWith(&resp.staticSignedRegistryValue.RegistryValue, types.SiaPublicKey{})
		if resp == best || errors.Contains(reason, modules.ErrSameRevNum) {
			nBestTotal++
			if resp.staticSignedRegistryValue.IsPrimaryEntry(resp.staticWorker.staticHostPubKey) {
				nPrimary++
			}
		}
	}
	return skymodules.RegistryEntryHealth{
		RevisionNumber:        bestSRV.Revision,
		NumEntries:            nTotal,
		NumBestEntries:        nBestTotal,
		NumBestPrimaryEntries: nPrimary,
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
	// threadedAddResponseSet is done.
	backgroundCtx, backgroundCancel := context.WithCancel(r.tg.StopCtx())

	// Get the start time before launching the workers to avoid negative
	// datapoints later.
	startTime := time.Now()

	responseSet := r.managedLaunchReadRegistryWorkers(backgroundCtx, span, rid, spk, tweak)
	numWorkers := responseSet.left

	// If there are no workers remaining, fail early.
	if numWorkers == 0 {
		backgroundCancel()
		return skymodules.RegistryEntry{}, errors.AddContext(skymodules.ErrNotEnoughWorkersInWorkerPool, "cannot perform ReadRegistry")
	}

	// Add the response set to the stats after this method is done.
	defer func() {
		_ = r.tg.Launch(func() {
			r.threadedAddResponseSet(r.tg.StopCtx(), span, startTime, responseSet, r.staticLog)
			backgroundCancel()
		})
	}()

	// Use the p999 of the registry read stats to determine the timeout.
	nines := r.staticRegReadStats.Percentiles()
	estimate := nines[0][2]
	if estimate < minRegistryReadTimeout {
		estimate = minRegistryReadTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, estimate)
	defer cancel()

	// Prepare a context which will be overwritten by a child context with a timeout
	// when we receive the first response. useHighestRevDefaultTimeout after
	// receiving the first response, this will be closed to abort the search for
	// the highest rev number and return the highest one we have so far.
	var useHighestRevCtx context.Context

	var best *jobReadRegistryResponse
	responses := 0
	for responseSet.responsesLeft() > 0 {
		// Check cancel condition and block for more responses.
		var resp *jobReadRegistryResponse
		if best != nil && best.staticSignedRegistryValue != nil {
			// If we have a successful response already, we wait on the highest
			// rev ctx.
			resp = responseSet.next(useHighestRevCtx)
		} else {
			// Otherwise we don't wait on the usehighestRevCtx since we need a
			// successful response to abort.
			resp = responseSet.next(ctx)
		}
		if resp == nil {
			break // context triggered
		}

		// When we get the first response, we initialize the highest rev
		// timeout.
		if responses == 0 {
			c, cancel := context.WithTimeout(ctx, useHighestRevDefaultTimeout)
			defer cancel()
			useHighestRevCtx = c
		}

		// Increment responses.
		responses++

		// Ignore error responses and responses that returned no entry.
		if resp.staticErr != nil || resp.staticSignedRegistryValue == nil {
			continue
		}

		// Remember the best response.
		if isBetterReadRegistryResponse(best, resp) {
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
func (r *Renter) managedLaunchReadRegistryWorkers(ctx context.Context, span opentracing.Span, rid modules.RegistryEntryID, spk *types.SiaPublicKey, tweak *crypto.Hash) *readResponseSet {
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
	// If specified, increment numWorkers. This will cause the loop to never
	// exit without any of the context being closed since the response set won't
	// be able to read the last response.
	if r.staticDeps.Disrupt("ReadRegistryBlocking") {
		numRegistryWorkers++
	}

	return newReadResponseSet(staticResponseChan, numRegistryWorkers)
}

// managedUpdateRegistry updates the registries on all workers with the given
// registry value.
// NOTE: the input ctx only unblocks the call if it fails to hit the threshold
// before the timeout. It doesn't stop the update jobs. That's because we want
// to always make sure we update as many hosts as possble.
func (r *Renter) managedUpdateRegistry(ctx context.Context, spk types.SiaPublicKey, srv modules.SignedRegistryValue) (err error) {
	// Start tracing.
	start := time.Now()
	tracer := opentracing.GlobalTracer()
	span := tracer.StartSpan("managedUpdateRegistry")
	defer span.Finish()

	// Verify the signature before updating the hosts.
	if err := srv.Verify(spk.ToPublicKey()); err != nil {
		return errors.AddContext(err, "managedUpdateRegistry: failed to verify signature of entry")
	}
	// Get the full list of workers and create a channel to receive all of the
	// results from the workers. The channel is buffered with one slot per
	// worker, so that the workers do not have to block when returning the
	// result of the job, even if this thread is not listening.
	workers := r.staticWorkerPool.callWorkers()
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
		cache := worker.staticCache()
		if build.VersionCmp(cache.staticHostVersion, minRegistryVersion) < 0 {
			continue
		}

		// Skip !goodForUpload workers.
		if !cache.staticContractUtility.GoodForUpload {
			continue
		}

		// check for price gouging
		pt := worker.staticPriceTable().staticPriceTable
		err = checkUploadGougingPT(pt, cache.staticRenterAllowance)
		if err != nil {
			r.staticLog.Debugf("price gouging detected in worker %v, err: %v\n", worker.staticHostPubKeyStr, err)
			continue
		}

		// Create the job.
		jrr := worker.newJobUpdateRegistry(updateTimeoutCtx, span, staticResponseChan, spk, srv)
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
	if len(workers) < MinUpdateRegistrySuccesses {
		return errors.AddContext(skymodules.ErrNotEnoughWorkersInWorkerPool, "cannot performa UpdateRegistry")
	}

	workersLeft := len(workers)
	responses := 0
	successfulResponses := 0

	var respErrs error
	for successfulResponses < MinUpdateRegistrySuccesses && workersLeft+successfulResponses >= MinUpdateRegistrySuccesses {
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
	if successfulResponses < MinUpdateRegistrySuccesses {
		r.staticLog.Printf("RegistryUpdate failed with %v < %v successful responses: %v", successfulResponses, MinUpdateRegistrySuccesses, respErrs)
		return errors.Compose(err, ErrRegistryUpdateInsufficientRedundancy)
	}
	r.staticRegWriteStats.AddDataPoint(time.Since(start))
	return nil
}

// isBetterReadRegistryResponse returns true if resp2 is a better response than
// resp1 and false otherwise. Better means that the response either has a higher
// revision number, more work or was faster.
func isBetterReadRegistryResponse(resp1, resp2 *jobReadRegistryResponse) bool {
	// Check for nil response.
	if resp2 == nil {
		// A nil entry never replaces an existing entry.
		return false
	} else if resp1 == nil {
		// A non-nil entry always replaces a nil entry.
		return true
	}
	// Same but with the entries.
	srv1 := resp1.staticSignedRegistryValue
	srv2 := resp2.staticSignedRegistryValue
	if srv2 == nil {
		return false
	} else if srv1 == nil {
		return true
	}
	// Compare entries. We pass the empty key here since we don't care about
	// whether the entry is a primary or secondary one.
	shouldUpdate, updateErr := srv1.ShouldUpdateWith(&srv2.RegistryValue, types.SiaPublicKey{})

	// If the entry is not capable of updating the existing one and both entries
	// have the same revision number, use the time.
	if !shouldUpdate && errors.Contains(updateErr, modules.ErrSameRevNum) {
		return resp2.staticCompleteTime.Before(resp1.staticCompleteTime)
	}

	// Otherwise we return the result
	return shouldUpdate
}
