package renter

import (
	"time"

	"github.com/opentracing/opentracing-go"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"

	"gitlab.com/NebulousLabs/errors"
)

type (
	// jobRead contains information about a Read query.
	jobRead struct {
		staticLength       uint64
		staticResponseChan chan *jobReadResponse

		// staticSpan is used for tracing. Note that this can be nil, and
		// therefore should always be checked. Not all read jobs require
		// tracing. By allowing it to be nil we avoid the extra overhead.
		staticSpan opentracing.Span

		*jobGeneric
	}

	// jobReadQueue is a list of Read queries that have been assigned to the
	// worker. The queue also tracks performance metrics, which can then be used
	// by projects to optimize job scheduling between workers.
	jobReadQueue struct {
		// These float64s are converted time.Duration values. They are float64
		// to get better precision on the exponential decay which gets applied
		// with each new data point.
		staticDT64k *skymodules.DistributionTracker
		staticDT1m  *skymodules.DistributionTracker
		staticDT4m  *skymodules.DistributionTracker

		*jobGenericQueue
	}

	// jobReadResponse contains the result of a Read query.
	jobReadResponse struct {
		// The response data.
		staticData []byte
		staticErr  error

		// Metadata related to the job.
		staticMetadata jobReadMetadata

		// The time it took for this job to complete.
		staticJobTime time.Duration
	}

	// jobReadMetadata contains meta information about a read job.
	jobReadMetadata struct {
		staticSectorRoot          crypto.Hash
		staticPieceRootIndex      uint64
		staticLaunchedWorkerIndex uint64

		// the category specifies what type of function the read job fulfils,
		// this is necessary to pass along as the generic MDM executor needs to
		// be update spending details and read jobs can be used for downloads
		// but might also be used for snapshots for example
		staticSpendingCategory spendingCategory

		staticWorker *worker
	}
)

// staticJobReadMetadata returns the read job's metadata.
func (j *jobRead) staticJobReadMetadata() jobReadMetadata {
	var metadata jobReadMetadata
	md, ok := j.staticGetMetadata().(jobReadMetadata)
	if ok {
		metadata = md
	}
	return metadata
}

// callDiscard will discard a job, forwarding the error to the caller.
func (j *jobRead) callDiscard(err error) {
	// Log info and finish span.
	if j.staticSpan != nil {
		j.staticSpan.LogKV("callDiscard", err)
		j.staticSpan.SetTag("success", false)
		j.staticSpan.Finish()
	}

	w := j.staticQueue.staticWorker()
	errLaunch := w.staticRenter.tg.Launch(func() {
		response := &jobReadResponse{
			staticErr:      errors.Extend(err, ErrJobDiscarded),
			staticMetadata: j.staticJobReadMetadata(),
		}
		select {
		case j.staticResponseChan <- response:
		case <-w.staticRenter.tg.StopChan():
		case <-j.staticCtx.Done():
		}
	})
	if errLaunch != nil {
		w.staticRenter.staticLog.Print("callDiscard: launch failed", err)
	}
}

// managedFinishExecute will execute code that is shared by multiple read jobs
// after execution. It updates the performance metrics, records whether the
// execution was successful and returns the response.
func (j *jobRead) managedFinishExecute(readData []byte, readErr error, readJobTime time.Duration) {
	// Log result and finish
	if j.staticSpan != nil {
		j.staticSpan.LogKV(
			"err", readErr,
			"duration", readJobTime,
		)
		j.staticSpan.SetTag("success", readErr == nil)
		j.staticSpan.Finish()
	}

	// Send the response in a goroutine so that the worker resources can be
	// released faster. Need to check if the job was canceled so that the
	// goroutine will exit.
	response := &jobReadResponse{
		staticData: readData,
		staticErr:  readErr,

		staticMetadata: j.staticJobReadMetadata(),
		staticJobTime:  readJobTime,
	}
	w := j.staticQueue.staticWorker()
	err := w.staticRenter.tg.Launch(func() {
		select {
		case j.staticResponseChan <- response:
		case <-j.staticCtx.Done():
		case <-w.staticRenter.tg.StopChan():
		}
	})
	if err != nil {
		j.staticQueue.staticWorker().staticRenter.staticLog.Print("managedFinishExecute: launch failed", err)
	}

	// Report success or failure to the queue.
	if readErr != nil {
		j.staticQueue.callReportFailure(readErr)
		return
	}
	j.staticQueue.callReportSuccess()

	// Job succeeded.
	//
	// Update the metrics in the read sector queue based on the amount of
	// time the read took. Stats should only be added if the job did not
	// result in an error. Because there was no failure, the consecutive
	// failures stat can be reset.
	jq := j.staticQueue.(*jobReadQueue)
	jq.callUpdateJobTimeMetrics(j.staticLength, readJobTime)
}

// callExpectedBandwidth returns the bandwidth that gets consumed by a
// Read program.
func (j *jobRead) callExpectedBandwidth() (ul, dl uint64) {
	ul = 1 << 12                                      // 4 KiB
	dl = uint64(float64(j.staticLength)*1.01) + 1<<12 // (readSize * 1.01 + 4 KiB)
	return
}

// managedRead returns the sector data for the given read program and the merkle
// proof.
func (j *jobRead) managedRead(w *worker, program modules.Program, programData []byte, cost types.Currency) ([]programResponse, error) {
	// execute it
	responses, _, err := w.managedExecuteProgram(program, programData, w.staticCache().staticContractID, j.staticJobReadMetadata().staticSpendingCategory, cost)
	if err != nil {
		return []programResponse{}, err
	}

	// Sanity check number of responses.
	if len(responses) > len(program) {
		build.Critical("managedExecuteProgram should return at most len(program) instructions")
	}
	if len(responses) == 0 {
		build.Critical("managedExecuteProgram should at least return one instruction when err == nil")
	}
	// If the number of responses doesn't match, the last response should
	// contain an error message.
	if len(responses) != len(program) {
		err := responses[len(responses)-1].Error
		return []programResponse{}, errors.AddContext(err, "managedRead: program execution was interrupted")
	}

	// The last instruction is the actual download.
	response := responses[len(responses)-1]
	if response.Error != nil {
		return []programResponse{}, response.Error
	}
	sectorData := response.Output

	// Check that we received the amount of data that we were expecting.
	if uint64(len(sectorData)) != j.staticLength {
		return []programResponse{}, errors.New("worker returned the wrong amount of data")
	}
	return responses, nil
}

// callAddWithEstimate will add a job to the job read queue while providing an
// estimate for when the job is expected to return.
func (jq *jobReadQueue) callAddWithEstimate(j *jobReadSector) (ResolveTime, bool) {
	jq.mu.Lock()
	defer jq.mu.Unlock()

	estimate := jq.expectedJobTime(j.staticLength)
	if !jq.add(j) {
		return ResolveTime{}, false
	}
	return estimate.ResolveTime(time.Now()), true
}

// callExpectedJobTime will return the recent performance of the worker
// attempting to complete read jobs. The call distinguishes based on the
// size of the job, breaking the jobs into 3 categories: less than 64kb, less
// than 1mb, and up to a full sector in size.
//
// The breakout is performed because low latency, low throughput workers are
// common, and will have very different performance characteristics across the
// three categories.
//
// TODO: Make this smarter.
func (jq *jobReadQueue) callExpectedJobTime(length uint64) JobTime {
	jq.mu.Lock()
	defer jq.mu.Unlock()
	return jq.expectedJobTime(length)
}

// expectedJobTime returns the expected job time, based on recent performance,
// for the given read length.
func (jq *jobReadQueue) expectedJobTime(length uint64) JobTime {
	if length <= 1<<16 {
		return jq.staticDT64k.PercentilesCustom(jobTimePercentiles)[0]
	} else if length <= 1<<20 {
		return jq.staticDT1m.PercentilesCustom(jobTimePercentiles)[0]
	} else {
		return jq.staticDT4m.PercentilesCustom(jobTimePercentiles)[0]
	}
}

// callExpectedJobCost returns an estimate for the price of performing a read
// job with the given length.
func (jq *jobReadQueue) callExpectedJobCost(length uint64) types.Currency {
	pt := &jq.staticWorker().staticPriceTable().staticPriceTable

	// Calculate init cost. The program we use has a 48 byte program data and 1
	// instruction. 48 = 8 bytes length + 8 bytes offset + 32 bytes merkle root
	cost := modules.MDMInitCost(pt, 48, 1)

	// Add the execution cost.
	cost = cost.Add(modules.MDMReadCost(pt, length))

	// Add the memory cost.
	memory := modules.MDMInitMemory() + modules.MDMReadMemory()
	time := uint64(modules.MDMTimeReadSector)
	cost = cost.Add(modules.MDMMemoryCost(pt, memory, time))

	// Add the bandwidth cost.
	ulBandwidth, dlBandwidth := new(jobReadSector).callExpectedBandwidth()
	cost = cost.Add(modules.MDMBandwidthCost(*pt, ulBandwidth, dlBandwidth))
	return cost
}

// callUpdateJobTimeMetrics takes a length and the duration it took to fulfil
// that job and uses it to update the job performance metrics on the queue.
func (jq *jobReadQueue) callUpdateJobTimeMetrics(length uint64, jobTime time.Duration) {
	jq.mu.Lock()
	defer jq.mu.Unlock()
	if length <= 1<<16 {
		jq.staticDT64k.AddDataPoint(jobTime)
	} else if length <= 1<<20 {
		jq.staticDT1m.AddDataPoint(jobTime)
	} else {
		jq.staticDT4m.AddDataPoint(jobTime)
	}
}

// initJobReadQueue will initialize a queue for downloading sectors by
// their root for the worker. This is only meant to be run once at startup.
func (w *worker) initJobReadQueue() {
	// Sanity check that there is no existing job queue.
	if w.staticJobReadQueue != nil {
		w.staticRenter.staticLog.Critical("incorret call on initJobReadQueue")
	}
	w.staticJobReadQueue = &jobReadQueue{
		staticDT64k:     skymodules.NewDistributionTrackerStandard(),
		staticDT1m:      skymodules.NewDistributionTrackerStandard(),
		staticDT4m:      skymodules.NewDistributionTrackerStandard(),
		jobGenericQueue: newJobGenericQueue(w),
	}
}

// initJobLowPrioReadQueue will initialize a queue for downloading sectors by
// their root for the worker. This is only meant to be run once at startup.
func (w *worker) initJobLowPrioReadQueue() {
	// Sanity check that there is no existing job queue.
	if w.staticJobLowPrioReadQueue != nil {
		w.staticRenter.staticLog.Critical("incorret call on initJobReadQueue")
	}
	w.staticJobLowPrioReadQueue = &jobReadQueue{
		jobGenericQueue: newJobGenericQueue(w),
	}
}
