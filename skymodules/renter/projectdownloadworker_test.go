package renter

import (
	"sort"
	"testing"
	"time"

	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/types"
)

type testDownloadWorker struct {
	mockCost         types.Currency
	mockChance       float64
	mockDistribution *skymodules.Distribution
}

func (tdw testDownloadWorker) chanceCompleteAfter(dur time.Duration) float64 {
	return tdw.mockChance
}
func (tdw testDownloadWorker) cost(length uint64) types.Currency {
	return tdw.mockCost
}
func (tdw testDownloadWorker) distribution() *skymodules.Distribution {
	return tdw.mockDistribution
}
func (tdw testDownloadWorker) pieces() []uint64 {
	return nil
}
func (tdw testDownloadWorker) worker() *worker {
	return nil
}

func TestDownloadWorkerSort(t *testing.T) {
	workers := []downloadWorker{
		testDownloadWorker{mockChance: .5},
		testDownloadWorker{mockChance: .2},
		testDownloadWorker{mockChance: .3},
		testDownloadWorker{mockChance: .8},
		testDownloadWorker{mockChance: .9},
	}

	dur := time.Millisecond
	sort.Slice(workers, func(i, j int) bool {
		return workers[i].chanceCompleteAfter(dur) > workers[j].chanceCompleteAfter(dur)
	})

	prev := float64(1)
	for _, w := range workers {
		curr := w.chanceCompleteAfter(dur)
		if curr > prev {
			t.Fatal("unexpected", curr, prev)
		}
		t.Log(curr)
		prev = curr
	}
}

func TestIndividualToChimeraWorkers(t *testing.T) {
	var workers individualWorkers

	mockWorkerWithChance := func(chance float64) *individualWorker {
		dt := skymodules.NewDistributionTrackerStandard()
		return &individualWorker{
			staticReadDistribution: dt.Distribution(0),
			staticResolveChance:    chance,
			staticWorker:           new(worker),
		}
	}
	workers = append(workers, mockWorkerWithChance(.2))
	workers = append(workers, mockWorkerWithChance(.5))
	workers = append(workers, mockWorkerWithChance(.4))
	// 1st chimera done and .1 remaining
	workers = append(workers, mockWorkerWithChance(.1))
	workers = append(workers, mockWorkerWithChance(.3))
	workers = append(workers, mockWorkerWithChance(.4))
	workers = append(workers, mockWorkerWithChance(.3))
	// 2nd chimera done and .2 remaining

	chimeras := workers.toChimeraWorkers()
	if len(chimeras) != 2 {
		t.Fatal("unexpected", len(chimeras))
	}
}
