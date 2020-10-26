package renter

import (
	"context"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/siatest/dependencies"
	"gitlab.com/NebulousLabs/errors"
)

// TestWorkerMaintenanceCoolDown verifies the functionality of the worker's
// cooldown of the RHP3 related subsystems.
func TestWorkerMaintenanceCoolDown(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	disableFundAccount := dependencies.NewDependencyFundAccountFail()
	disableFundAccount.Disable()

	wt, err := newWorkerTesterCustomDependency(t.Name(), &dependencies.DependencyDisableCriticalOnMaxBalance{}, disableFundAccount)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := wt.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()
	w := wt.worker

	// wait until the worker is done with its maintenance tasks - this basically
	// ensures we have a working worker, with valid PT and funded EA
	if err := build.Retry(100, 100*time.Millisecond, func() error {
		if !w.managedMaintenanceSucceeded() {
			return errors.New("worker not ready with maintenance")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// verify the worker is not on an maintenance cooldown
	if w.managedOnMaintenanceCooldown() {
		t.Fatal("Unexpected maintenance cooldown")
	}

	// set a negative balance to make the worker think it needs to refill
	w.staticAccount.mu.Lock()
	w.staticAccount.negativeBalance = w.staticAccount.balance
	w.staticAccount.mu.Unlock()

	// ensure the refill fails on the host
	disableFundAccount.Enable()

	// manually trigger a refill and verify we've been put on maintenance
	// cooldown
	w.managedRefillAccount()
	if !w.managedOnMaintenanceCooldown() {
		t.Fatal("Expected maintenance cooldown")
	}

	// re-enable refills on the host so we can recover from the situation
	disableFundAccount.Disable()

	// the workerloop should have synced the account balance
	if err := build.Retry(100, 100*time.Millisecond, func() error {
		w.staticAccount.mu.Lock()
		defer w.staticAccount.mu.Unlock()
		if !w.staticAccount.negativeBalance.IsZero() {
			return errors.New("worker account balance not reset")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// run a couple of has sector jobs to spend money
	ctx := context.Background()
	rc := make(chan *jobHasSectorResponse)
	jhs := w.newJobHasSector(ctx, rc, crypto.Hash{})
	for i := 0; i < 100; i++ {
		if !w.staticJobHasSectorQueue.callAdd(jhs) {
			t.Fatal("could not add job to queue")
		}
	}

	// manually trigger a refill and verify the account is no longer on cooldown
	w.managedRefillAccount()
	if w.managedOnMaintenanceCooldown() {
		t.Fatal("Worker's RHP3 subsystems should not be on cooldown")
	}
}
