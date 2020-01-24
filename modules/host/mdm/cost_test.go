package mdm

import (
	"testing"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
)

// TestCostSub tests subtracting a cost from another one.
func TestCostSub(t *testing.T) {
	cost := modules.Cost{
		Compute:      10,
		DiskAccesses: 10,
		DiskRead:     10,
		DiskWrite:    10,
		Memory:       10,
	}

	// Check if consuming exactly all resources works.
	result, err := cost.Sub(modules.Cost{
		Compute:      10,
		DiskAccesses: 10,
		DiskRead:     10,
		DiskWrite:    10,
		Memory:       10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Compute+result.DiskAccesses+result.DiskRead+result.DiskWrite+result.Memory > 0 {
		t.Fatal("expected all resources to be consumed")
	}
	// Check if consuming fewer resources works.
	result, err = cost.Sub(modules.Cost{
		Compute:      9,
		DiskAccesses: 9,
		DiskRead:     9,
		DiskWrite:    9,
		Memory:       9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Compute+result.DiskAccesses+result.DiskRead+result.DiskWrite+result.Memory != 5 {
		t.Fatal("expected 5 resources to be left")
	}
	// Underflow all resources.
	result, err = cost.Sub(modules.Cost{
		Compute:      11,
		DiskAccesses: 11,
		DiskRead:     11,
		DiskWrite:    11,
		Memory:       11,
	})
	if !errors.Contains(err, modules.ErrInsufficientBudget) {
		t.Fatal("expected ErrInsufficientBudget error")
	}
	if !errors.Contains(err, modules.ErrInsufficientBudget) {
		t.Fatal("expected err to contain", modules.ErrInsufficientBudget)
	}
	if !errors.Contains(err, modules.ErrInsufficientComputeBudget) {
		t.Fatal("expected err to contain", modules.ErrInsufficientComputeBudget)
	}
	if !errors.Contains(err, modules.ErrInsufficientDiskAccessesBudget) {
		t.Fatal("expected err to contain", modules.ErrInsufficientDiskAccessesBudget)
	}
	if !errors.Contains(err, modules.ErrInsufficientDiskReadBudget) {
		t.Fatal("expected err to contain", modules.ErrInsufficientDiskReadBudget)
	}
	if !errors.Contains(err, modules.ErrInsufficientDiskWriteBudget) {
		t.Fatal("expected err to contain", modules.ErrInsufficientDiskWriteBudget)
	}
	if !errors.Contains(err, modules.ErrInsufficientMemoryBudget) {
		t.Fatal("expected err to contain", modules.ErrInsufficientMemoryBudget)
	}
}
