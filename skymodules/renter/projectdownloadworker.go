package renter

import (
	"time"

	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/types"
)

type (
	// chimeraWorker is a worker that's built from unresolved workers until the
	// chance it has a piece is exactly 1. At that point we can treat a chimera
	// worker exactly the same as a resolved worker in the download algorithm
	// that constructs the best worker set.
	chimeraWorker struct {
		// cachedDistribution contains a distribution that is the weighted
		// combination of all worker distrubtions in this chimera worker, it is
		// cached meaning it will only be calculated the first time the
		// distribution is requested after the chimera worker was finalized.
		cachedDistribution *skymodules.Distribution

		distributions []*skymodules.Distribution
		weights       []float64
		workers       []*worker
	}

	// individualWorker is a struct that represents a single worker object, both
	// resolved and unresolved workers in the pdc can be represented by an
	// individual worker. An individual worker can be used to build a chimera
	// worker with.
	//
	// NOTE: extending this struct requires an update to the `split` method.
	individualWorker struct {
		staticLaunchedAt       time.Time
		staticPieceIndices     []uint64
		staticResolveChance    float64
		staticReadDistribution *skymodules.Distribution
		staticWorker           *worker
	}
)

// NewChimeraWorker returns a new chimera worker object.
func NewChimeraWorker() *chimeraWorker {
	return &chimeraWorker{
		distributions: make([]*skymodules.Distribution, 0),
		weights:       make([]float64, 0),
		workers:       make([]*worker, 0),
	}
}

// addWorker adds the given worker to the chimera worker.
func (cw *chimeraWorker) addWorker(w *individualWorker) (remainder *individualWorker) {
	// calculate the remaining chance this chimera worker needs to be complete
	remaining := float64(1)
	for _, weight := range cw.weights {
		remaining -= weight
	}
	if remaining == 0 {
		return w
	}

	// the given worker's chance can be higher than the remaining chance of this
	// chimera worker, in that case we have to split the worker in a part we
	// want to add, and a remainder we'll use to build the next chimera with
	toAdd := w
	if w.staticResolveChance > remaining {
		toAdd, remainder = w.split(remaining)
	}

	// add the worker to the chimera
	cw.distributions = append(cw.distributions, toAdd.staticReadDistribution)
	cw.weights = append(cw.weights, toAdd.staticResolveChance)
	cw.workers = append(cw.workers, toAdd.staticWorker)
	return
}

// cost implements the downloadWorker interface.
func (cw *chimeraWorker) cost(length uint64) types.Currency {
	numWorkers := uint64(len(cw.workers))
	if numWorkers == 0 {
		return types.ZeroCurrency
	}

	var total types.Currency
	for _, w := range cw.workers {
		total = total.Add(w.staticJobReadQueue.callExpectedJobCost(length))
	}
	return total.Div64(numWorkers)
}

// distribution implements the downloadWorker interface.
func (cw *chimeraWorker) distribution() *skymodules.Distribution {
	if cw.remaining() != 0 {
		build.Critical("developer error, chimera is not complete")
	}

	if cw.cachedDistribution == nil {
		halfLife := cw.distributions[0].HalfLife()
		cw.cachedDistribution = skymodules.NewDistribution(halfLife)
		for i, distribution := range cw.distributions {
			cw.cachedDistribution.MergeWith(distribution, cw.weights[i])
		}
	}
	return cw.cachedDistribution
}

// pieces implements the downloadWorker interface.
func (cw *chimeraWorker) pieces() []uint64 {
	return nil
}

// remaining returns the remaining chance this chimera worker needs to be
// complete and to have a 100% chance of resolving a piece
func (cw *chimeraWorker) remaining() float64 {
	remaining := float64(1)
	for _, weight := range cw.weights {
		remaining -= weight
	}
	return remaining
}

// worker implements the downloadWorker interface.
func (cw *chimeraWorker) worker() *worker {
	return nil
}

// cost implements the downloadWorker interface.
func (iw *individualWorker) cost(length uint64) types.Currency {
	// workers that have already been launched have a zero cost
	if iw.isLaunched() {
		return types.ZeroCurrency
	}
	return iw.staticWorker.staticJobReadQueue.callExpectedJobCost(length)
}

// distribution implements the downloadWorker interface.
func (iw *individualWorker) distribution() *skymodules.Distribution {
	// if the worker has been launched already, we want to shift the
	// distribution with the time that elapsed since it was launched
	//
	// NOTE: we always shift on a clone of the original read distribution to
	// avoid shifting the same distribution multiple times
	if iw.isLaunched() {
		clone := iw.staticReadDistribution.Clone()
		clone.Shift(time.Since(iw.staticLaunchedAt))
		return clone
	}
	return iw.staticReadDistribution
}

// isLaunched returns true when this workers has been launched.
func (iw *individualWorker) isLaunched() bool {
	return iw.staticLaunchedAt != (time.Time{})
}

// pieces implements the downloadWorker interface.
func (iw *individualWorker) pieces() []uint64 {
	return iw.staticPieceIndices
}

// worker implements the downloadWorker interface.
func (iw *individualWorker) worker() *worker {
	return iw.staticWorker
}

// split will split the download worker into two workers, the first worker will
// have the given chance, the second worker will have the remainder as its
// chance value.
func (iw *individualWorker) split(chance float64) (*individualWorker, *individualWorker) {
	if chance >= iw.staticResolveChance {
		build.Critical("chance value on which we split should be strictly less than the worker's resolve chance")
		return nil, nil
	}

	main := &individualWorker{
		staticLaunchedAt:       iw.staticLaunchedAt,
		staticPieceIndices:     iw.staticPieceIndices,
		staticResolveChance:    chance,
		staticReadDistribution: iw.staticReadDistribution,
		staticWorker:           iw.staticWorker,
	}
	remainder := &individualWorker{
		staticLaunchedAt:       iw.staticLaunchedAt,
		staticPieceIndices:     iw.staticPieceIndices,
		staticResolveChance:    iw.staticResolveChance - chance,
		staticReadDistribution: iw.staticReadDistribution,
		staticWorker:           iw.staticWorker,
	}

	return main, remainder
}
