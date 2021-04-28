package renter

import (
	"fmt"
	"strings"
	"sync/atomic"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/encoding"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/contractor"
)

// errCausedByRevisionMismatch returns true if (we suspect) the given error is
// caused by a revision number mismatch. Unfortunately we can not know this for
// sure, because hosts before v1.4.12 did not perform the revision number check
// as the very first check when validating a revision.
func errCausedByRevisionMismatch(err error) bool {
	return err != nil &&
		(strings.Contains(err.Error(), "bad revision number") ||
			strings.Contains(err.Error(), "unexpected number of outputs") ||
			strings.Contains(err.Error(), "high paying renter valid output") ||
			strings.Contains(err.Error(), "low paying host missed output"))
}

// externTryFixRevisionMismatch attempts to fix a mismatch in revision numbers,
// it does so by instantiating a session, which has a handshake where revisions
// are exchanged and we learn the host's revision number, and goes on to try and
// sync them if they do not match.
//
// NOTE: the 'extern' refers to the fact that this function need to be called
// from the primary work thread of the worker.
func (w *worker) externTryFixRevisionMismatch() {
	// Do not attempt to try and fix a revision mismatch if the worker's RHP3
	// subystem is on cooldown.
	if w.managedOnMaintenanceCooldown() {
		return
	}

	// Unset the flag indicating mismatch suspicion.
	atomic.StoreUint64(&w.staticLoopState.atomicSuspectRevisionMismatch, 0)

	// Initiate a session, this performs a handshake with the host and syncs up
	// the revision if necessary.
	err := w.managedSyncRevision()

	// Track the outcome of the revision mismatch fix - this ensures a proper
	// working of the maintenance cooldown mechanism.
	// NOTE: A renewal is a serial job so the renewal is actually done at this
	// point. A ErrContractRenewing simply indicates that the contractor hasn't
	// set the contract back to not being renewed. We don't increment the
	// cooldown to make sure operations can continue.
	if !errors.Contains(err, contractor.ErrContractRenewing) {
		w.managedTrackRevisionMismatchFixErr(err)
	}

	if err != nil {
		w.staticRenter.staticLog.Printf("could not fix revision number mismatch, could not retrieve a session with host %v, err: %v\n", w.staticHostPubKeyStr, err)
		return
	}

	// Log that we have attempted to fix a revision number mismatch.
	w.staticRenter.staticLog.Debugf("%v revision resync triggered\n", w.staticHostPubKeyStr)
}

// staticSetSuspectRevisionMismatch sets the atomicSuspectRevisionMismatch flag.
func (w *worker) staticSetSuspectRevisionMismatch() {
	atomic.StoreUint64(&w.staticLoopState.atomicSuspectRevisionMismatch, 1)
}

// staticSetSuspectRevisionMismatch returns whether or not the
// atomicSuspectRevisionMismatch flag has been set.
func (w *worker) staticSuspectRevisionMismatch() bool {
	return atomic.LoadUint64(&w.staticLoopState.atomicSuspectRevisionMismatch) == 1
}

// managedSyncRevision fetches the latest revision from the host and tries to
// sync the worker's revision with the host's again.
func (w *worker) managedSyncRevision() error {
	// Build the program.
	pt := w.staticPriceTable().staticPriceTable
	fcid := w.staticCache().staticContractID
	pb := modules.NewProgramBuilder(&pt, 0)
	pb.AddRevisionInstruction()

	program, programData := pb.Program()
	cost, _, _ := pb.Cost(true)

	// TODO: account for bandwidth more accurately.
	ulBandwidth, dlBandwidth := uint64(4096), uint64(4096)
	bandwidthCost := modules.MDMBandwidthCost(pt, ulBandwidth, dlBandwidth)
	cost = cost.Add(bandwidthCost)

	// Execute the program and parse the response.
	// TODO: f/u extend the spending details with a category for revision
	// downloads.
	responses, _, err := w.managedExecuteProgram(program, programData, fcid, categoryDownload, cost)
	if err != nil {
		return errors.AddContext(err, "Unable to execute revision program")
	}
	if len(responses) != 1 {
		return fmt.Errorf("managedSyncRevision: invalid number of responses %v != %v", len(responses), 1)
	}
	response := responses[0]
	if response.Error != nil {
		return errors.AddContext(response.Error, "managedSyncRevision: output error")
	}

	// Unmarshal response
	var resp modules.MDMInstructionRevisionResponse
	err = encoding.Unmarshal(response.Output, &resp)
	if err != nil {
		return errors.AddContext(err, "managedSyncRevision: failed to unmarshal revision from response")
	}

	// Finishing syncing revision.
	err = w.staticRenter.staticHostContractor.SyncRevision(fcid, resp.RevisionTxn)
	if err != nil {
		return errors.AddContext(err, "managedSyncRevision: failed to sync revision")
	}
	return nil
}
