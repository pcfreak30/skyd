package renter

import (
	"reflect"
	"testing"

	"go.sia.tech/siad/modules"
)

// testWQueue is a type implementing the weightedJobQueue interface for testing.
type testWQueue struct {
	staticMW uint64
	length   int
}

// newTestWQueue creates a new testWQueue.
func newTestWQueue(mw uint64, length int) *testWQueue {
	return &testWQueue{
		staticMW: mw,
		length:   length,
	}
}

// callLen implements the weightedJobQueue interface.
func (tq *testWQueue) callLen() int {
	return tq.length
}

// staticMaxWeight implements the weightedJobQueue interface.
func (tq *testWQueue) staticMaxWeight() uint64 {
	return tq.staticMW
}

// callNextWithWeight implements the weightedJobQueue interface.
func (tq *testWQueue) callNextWithWeight(minWeight uint64) workerJob {
	if tq.staticMW < minWeight || tq.length == 0 {
		return nil
	}
	tq.length--
	return &jobTest{}
}

// TestIWRR is the root test running all the tests related to the iwrr.
func TestIWRR(t *testing.T) {
	t.Parallel()

	t.Run("New", testNewIWRR)
	t.Run("MaxWeights", testMaxWeights)
	t.Run("Weights", testWeights)
	t.Run("Next", testNext)
}

// testNewIWRR tests creating a new iwrr from queues.
func testNewIWRR(t *testing.T) {
	queues := []weightedJobQueue{
		newTestWQueue(1, 1),
		newTestWQueue(3, 3),
		newTestWQueue(2, 4),
	}
	iwrr := newIWRR(queues)
	if !reflect.DeepEqual(iwrr.staticQueues, queues) {
		t.Fatal("queues not set")
	}
	if iwrr.currentIndex != 0 || iwrr.currentRound != 0 {
		t.Fatal("index or queue wrong")
	}
	if iwrr.staticMaxWeight != 3 {
		t.Fatal("maxWeight should be 3", iwrr.staticMaxWeight)
	}
	if iwrr.numJobs() != 8 {
		t.Fatal("wrong number of jobs", iwrr.numJobs())
	}
}

// testMaxWeights make sure that all jobs queues return the right max weight.
func testMaxWeights(t *testing.T) {
	mw := (&jobRenewQueue{}).staticMaxWeight()
	if mw != renewQueueMaxWeight {
		t.Error("wrong weight")
	}
	mw = (&jobHasSectorQueue{}).staticMaxWeight()
	if mw != hasSectorQueueWeight {
		t.Error("wrong weight")
	}
	mw = (&jobReadRegistryQueue{}).staticMaxWeight()
	if mw != readRegistryQueueWeight {
		t.Error("wrong weight")
	}
	mw = (&jobUpdateRegistryQueue{}).staticMaxWeight()
	if mw != updateRegistryQueueWeight {
		t.Error("wrong weight")
	}
	mw = (&jobReadQueue{staticLowPrio: false}).staticMaxWeight()
	if mw != readQueueMaxWeight {
		t.Error("wrong weight")
	}
	mw = (&jobDownloadSnapshotQueue{}).staticMaxWeight()
	if mw != downloadSnapshotQueueWeight {
		t.Error("wrong weight")
	}
	mw = (&jobUploadSnapshotQueue{}).staticMaxWeight()
	if mw != uploadSnapshotQueueWeight {
		t.Error("wrong weight")
	}
	mw = (&jobReadQueue{staticLowPrio: true}).staticMaxWeight()
	if mw != lowPrioReadQueueWeight {
		t.Error("wrong weight")
	}
}

// testWeights tests the weights of the jobs.
func testWeights(t *testing.T) {
	w := (&jobRenew{}).callWeight()
	if w != renewQueueMaxWeight {
		t.Error("wrong weight")
	}
	w = (&jobHasSector{}).callWeight()
	if w != hasSectorQueueWeight {
		t.Error("wrong weight")
	}
	w = (&jobReadRegistry{}).callWeight()
	if w != readRegistryQueueWeight {
		t.Error("wrong weight")
	}
	w = (&jobUpdateRegistry{}).callWeight()
	if w != updateRegistryQueueWeight {
		t.Error("wrong weight")
	}
	w = (&jobDownloadSnapshot{}).callWeight()
	if w != downloadSnapshotQueueWeight {
		t.Error("wrong weight")
	}
	w = (&jobUploadSnapshot{}).callWeight()
	if w != uploadSnapshotQueueWeight {
		t.Error("wrong weight")
	}
	w = (&jobRead{staticLength: modules.SectorSize}).callWeight()
	if w != readQueueMaxWeight {
		t.Error("wrong weight", w)
	}
	w = (&jobRead{staticLength: modules.SectorSize / 2}).callWeight()
	if w != readQueueMinWeight+(readQueueMaxWeight-readQueueMinWeight)/2 {
		t.Error("wrong weight", w)
	}
	w = (&jobRead{staticLength: 1}).callWeight()
	if w != readQueueMinWeight {
		t.Error("wrong weight", w)
	}
	w = (&jobRead{staticLowPrio: true, staticLength: modules.SectorSize}).callWeight()
	if w != lowPrioReadQueueWeight {
		t.Error("wrong weight", w)
	}
	w = (&jobRead{staticLowPrio: true, staticLength: modules.SectorSize / 2}).callWeight()
	if w != lowPrioReadQueueWeight {
		t.Error("wrong weight", w)
	}
	w = (&jobRead{staticLowPrio: true, staticLength: 1}).callWeight()
	if w != lowPrioReadQueueWeight {
		t.Error("wrong weight", w)
	}
}

// testNext is a unit test for the iwrr's next method.
func testNext(t *testing.T) {
	q1 := newTestWQueue(2, 2)
	q2 := newTestWQueue(1, 1)
	q3 := newTestWQueue(0, 2)
	queues := []weightedJobQueue{q1, q2, q3}

	iwrr := newIWRR(queues)

	// The round should be 1 and index should be 0.
	if iwrr.currentIndex != 0 || iwrr.currentRound != 0 {
		t.Fatal("wrong index or round", iwrr.currentIndex, iwrr.currentRound)
	}

	// The next element should be from q1.
	wj := iwrr.next()
	if wj == nil {
		t.Fatal("job is nil")
	}
	if q1.callLen() != 1 {
		t.Fatal("q1 got wrong length", q1.callLen())
	}
	if q2.callLen() != 1 {
		t.Fatal("q2 got wrong length", q2.callLen())
	}
	if q3.callLen() != 2 {
		t.Fatal("q3 got wrong length", q3.callLen())
	}
	if iwrr.currentIndex != 1 || iwrr.currentRound != 0 {
		t.Fatal("wrong index or round", iwrr.currentIndex, iwrr.currentRound)
	}

	// The next element should be from q2 since q2 got weight 1 and we are in
	// round 0 with index 1.
	wj = iwrr.next()
	if wj == nil {
		t.Fatal("job is nil")
	}
	if q1.callLen() != 1 {
		t.Fatal("q1 got wrong length", q1.callLen())
	}
	if q2.callLen() != 0 {
		t.Fatal("q2 got wrong length", q2.callLen())
	}
	if q3.callLen() != 2 {
		t.Fatal("q3 got wrong length", q3.callLen())
	}
	if iwrr.currentIndex != 2 || iwrr.currentRound != 0 {
		t.Fatal("wrong index or round", iwrr.currentIndex, iwrr.currentRound)
	}

	// The next element should be from q3 since it got weight 0 and we are in
	// round 0 with index 2.
	wj = iwrr.next()
	if wj == nil {
		t.Fatal("job is nil")
	}
	if q1.callLen() != 1 {
		t.Fatal("q1 got wrong length", q1.callLen())
	}
	if q2.callLen() != 0 {
		t.Fatal("q2 got wrong length", q2.callLen())
	}
	if q3.callLen() != 1 {
		t.Fatal("q3 got wrong length", q3.callLen())
	}
	if iwrr.currentIndex != 3 || iwrr.currentRound != 0 {
		t.Fatal("wrong index or round", iwrr.currentIndex, iwrr.currentRound)
	}

	// The next element should be from q1 again in round 1.
	wj = iwrr.next()
	if wj == nil {
		t.Fatal("job is nil")
	}
	if q1.callLen() != 0 {
		t.Fatal("q1 got wrong length", q1.callLen())
	}
	if q2.callLen() != 0 {
		t.Fatal("q2 got wrong length", q2.callLen())
	}
	if q3.callLen() != 1 {
		t.Fatal("q3 got wrong length", q3.callLen())
	}
	if iwrr.currentIndex != 1 || iwrr.currentRound != 1 {
		t.Fatal("wrong index or round", iwrr.currentIndex, iwrr.currentRound)
	}

	// The next element should be from q2 but the queue is empty so we move
	// through the cycle until we are back in round 0 and get q3.
	wj = iwrr.next()
	if wj == nil {
		t.Fatal("job is nil")
	}
	if q1.callLen() != 0 {
		t.Fatal("q1 got wrong length", q1.callLen())
	}
	if q2.callLen() != 0 {
		t.Fatal("q2 got wrong length", q2.callLen())
	}
	if q3.callLen() != 0 {
		t.Fatal("q3 got wrong length", q3.callLen())
	}
	if iwrr.currentIndex != 3 || iwrr.currentRound != 0 {
		t.Fatal("wrong index or round", iwrr.currentIndex, iwrr.currentRound)
	}
}
