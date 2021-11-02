package renter

import (
	"sync"
	"time"

	"github.com/opentracing/opentracing-go"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"

	"gitlab.com/NebulousLabs/errors"
)

const (
	// jobReadPerformanceDecay defines how much decay gets applied to the
	// historic performance of jobRead each time new data comes back.
	// Setting a low value makes the performance more volatile. If the worker
	// tends to have inconsistent performance, having the decay be a low value
	// (0.9 or lower) will be highly detrimental. A higher decay means that the
	// predictor tends to be more accurate over time, but is less responsive to
	// things like network load.
	jobReadPerformanceDecay = 0.9

	// jobLength64k is the threshold we use to label a download as 64kb
	// jobLength1m is the threshold we use to label a download as 1m
	// jobLength4m is the threshold we use to label a download as 4m
	//
	// usually the length is evaluated using an if-else structure, comparing the
	// length to these threshold in ascending fashion, so we first check to see
	// whether it's a 64kb, then a 1mb and so on
	jobLength64k = uint64(1 << 16)
	jobLength1m  = uint64(1 << 20)
	jobLength4m  = uint64(1 << 24)
)

type (
	// jobRead contains information about a Read query.
	jobRead struct {
		staticLength       uint64
		staticLowPrio      bool
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
		staticLowPrio bool
		staticStats   *jobReadStats
		*jobGenericQueue

		// staticBaseCost is applied to download costs, defined in SC/TB
		staticBaseCost types.Currency
	}

	// jobReadStats contains statistics about read jobs. This object is
	// thread safe and can be shared between multiple queues.
	jobReadStats struct {
		// These float64s are converted time.Duration values. They are float64
		// to get better precision on the exponential decay which gets applied
		// with each new data point.
		weightedJobTime64k float64
		weightedJobTime1m  float64
		weightedJobTime4m  float64

		// These distribution trackers keep track of the read durations for
		// every length category.
		staticDT64k *skymodules.DistributionTracker
		staticDT1m  *skymodules.DistributionTracker
		staticDT4m  *skymodules.DistributionTracker

		*jobGenericQueue
		mu sync.Mutex
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

// staticMaxWeight returns the max weight of the queue.
func (jq *jobReadQueue) staticMaxWeight() uint64 {
	weight := readQueueWeight
	if jq.staticLowPrio {
		weight /= lowPrioWeightPenalty
	}
	return weight
}

// callWeight returns the weight of the job.
func (j *jobRead) callWeight() uint64 {
	weight := readQueueWeight
	if j.staticLowPrio {
		weight /= lowPrioWeightPenalty
	}
	// Adjust the weight according to actual bandwidth.
	return weight + modules.SectorSize - j.staticLength
}

// NewJobReadStats returns an initialized jobReadStats object.
func NewJobReadStats() *jobReadStats {
	return &jobReadStats{
		staticDT64k: skymodules.NewDistributionTrackerStandard(),
		staticDT1m:  skymodules.NewDistributionTrackerStandard(),
		staticDT4m:  skymodules.NewDistributionTrackerStandard(),
	}
}

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
	errLaunch := w.staticTG.Launch(func() {
		response := &jobReadResponse{
			staticErr:      err,
			staticMetadata: j.staticJobReadMetadata(),
		}
		select {
		case j.staticResponseChan <- response:
		case <-w.staticTG.StopChan():
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
	err := w.staticTG.Launch(func() {
		select {
		case j.staticResponseChan <- response:
		case <-j.staticCtx.Done():
		case <-w.staticTG.StopChan():
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
	jq.staticStats.callUpdateJobTimeMetrics(j.staticLength, readJobTime)
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
func (jq *jobReadQueue) callAddWithEstimate(j *jobReadSector) (time.Time, bool) {
	estimate := jq.staticStats.callExpectedJobTime(j.staticLength)

	jq.mu.Lock()
	defer jq.mu.Unlock()

	if !jq.add(j) {
		return time.Time{}, false
	}
	return time.Now().Add(estimate), true
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
func (jrs *jobReadStats) callExpectedJobTime(length uint64) time.Duration {
	jrs.mu.Lock()
	defer jrs.mu.Unlock()
	return jrs.expectedJobTime(length)
}

// expectedJobTime returns the expected job time, based on recent performance,
// for the given read length.
func (jrs *jobReadStats) expectedJobTime(length uint64) time.Duration {
	if length <= jobLength64k {
		return time.Duration(jrs.weightedJobTime64k)
	} else if length <= jobLength1m {
		return time.Duration(jrs.weightedJobTime1m)
	} else {
		return time.Duration(jrs.weightedJobTime4m)
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

	// Add the base cost.
	cost = cost.Add(jq.staticBaseCost.Mul64(dlBandwidth))
	return cost
}

// callUpdateJobTimeMetrics takes a length and the duration it took to fulfil
// that job and uses it to update the job performance metrics on the queue.
func (jrs *jobReadStats) callUpdateJobTimeMetrics(length uint64, jobTime time.Duration) {
	jrs.mu.Lock()
	defer jrs.mu.Unlock()
	if length <= jobLength64k {
		jrs.weightedJobTime64k = expMovingAvgHotStart(jrs.weightedJobTime64k, float64(jobTime), jobReadPerformanceDecay)
	} else if length <= jobLength1m {
		jrs.weightedJobTime1m = expMovingAvgHotStart(jrs.weightedJobTime1m, float64(jobTime), jobReadPerformanceDecay)
	} else {
		jrs.weightedJobTime4m = expMovingAvgHotStart(jrs.weightedJobTime4m, float64(jobTime), jobReadPerformanceDecay)
	}

	// update distribution tracker
	dt := jrs.distributionTrackerForLength(length)
	dt.AddDataPoint(jobTime)
}

// distributionTrackerForLength returns the distribution tracker that
// corresponds to the given length.
func (jrs *jobReadStats) distributionTrackerForLength(length uint64) *skymodules.DistributionTracker {
	if length <= jobLength64k {
		return jrs.staticDT64k
	} else if length <= jobLength1m {
		return jrs.staticDT1m
	} else {
		return jrs.staticDT4m
	}
}

// initJobReadQueue will initialize a queue for downloading sectors by
// their root for the worker. This is only meant to be run once at startup.
func (w *worker) initJobReadQueue(jrs *jobReadStats) {
	// Sanity check that there is no existing job queue.
	if w.staticJobReadQueue != nil {
		w.staticRenter.staticLog.Critical("incorrect call on initJobReadQueue")
	}

	w.staticJobReadQueue = &jobReadQueue{
		jobGenericQueue: newJobGenericQueue(w),
		staticLowPrio:   false,
		staticBaseCost:  skymodules.DefaultSkynetBaseCost,
		staticStats:     jrs,
	}
}

// initJobLowPrioReadQueue will initialize a queue for downloading sectors by
// their root for the worker. This is only meant to be run once at startup.
func (w *worker) initJobLowPrioReadQueue(jrs *jobReadStats) {
	// Sanity check that there is no existing job queue.
	if w.staticJobLowPrioReadQueue != nil {
		w.staticRenter.staticLog.Critical("incorret call on initJobReadQueue")
	}

	w.staticJobLowPrioReadQueue = &jobReadQueue{
		jobGenericQueue: newJobGenericQueue(w),
		staticLowPrio:   true,
		staticStats:     jrs,
		staticBaseCost:  skymodules.DefaultSkynetBaseCost,
	}
}
