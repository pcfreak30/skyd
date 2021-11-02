package renter

import (
	"context"
	"fmt"
	"time"

	"github.com/opentracing/opentracing-go"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
)

type (
	// jobReadSector contains information about a readSector query.
	jobReadSector struct {
		jobRead

		staticOffset uint64
		staticSector crypto.Hash
	}
)

// staticMinHostVersion returns the minimum host version needed to execute the
// job.
func (j *jobReadSector) staticMinHostVersion() string {
	return skymodules.FoundationHardforkVersion
}

// callExecute executes the jobReadSector.
func (j *jobReadSector) callExecute() {
	if j.staticSpan != nil {
		// Capture callExecute in new span.
		span := opentracing.StartSpan("callExecute", opentracing.ChildOf(j.staticSpan.Context()))
		defer span.Finish()
	}

	// Track how long the job takes.
	start := time.Now()
	data, err := j.managedReadSector()
	jobTime := time.Since(start)

	// Finish the execution.
	j.jobRead.managedFinishExecute(data, err, jobTime)
}

// managedReadSector returns the sector data for given root.
func (j *jobReadSector) managedReadSector() ([]byte, error) {
	// create the program
	w := j.staticQueue.staticWorker()
	pt := w.staticPriceTable().staticPriceTable
	pb := modules.NewProgramBuilder(&pt, 0) // 0 duration since ReadSector doesn't depend on it.
	pb.AddReadSectorInstruction(j.staticLength, j.staticOffset, j.staticSector, true)
	program, programData := pb.Program()
	cost, _, _ := pb.Cost(true)

	// take into account bandwidth costs
	ulBandwidth, dlBandwidth := j.callExpectedBandwidth()
	bandwidthCost := modules.MDMBandwidthCost(pt, ulBandwidth, dlBandwidth)
	cost = cost.Add(bandwidthCost)

	responses, err := j.jobRead.managedRead(w, program, programData, cost)
	if err != nil {
		return nil, errors.AddContext(err, "jobReadSector: failed to execute managedRead")
	}
	data := responses[0].Output
	proof := responses[0].Proof

	// verify proof
	proofStart := int(j.staticOffset) / crypto.SegmentSize
	proofEnd := int(j.staticOffset+j.staticLength) / crypto.SegmentSize
	if !crypto.VerifyRangeProof(data, proof, proofStart, proofEnd, j.staticSector) {
		return nil, errors.New("proof verification failed")
	}
	return data, nil
}

// newJobReadSector creates a new read sector job.
func (w *worker) newJobReadSector(ctx context.Context, queue *jobReadQueue, respChan chan *jobReadResponse, metadata jobReadMetadata, root crypto.Hash, offset, length uint64) *jobReadSector {
	// Create a job span if the given context has a reference span.
	var jobSpan opentracing.Span
	if span := opentracing.SpanFromContext(ctx); span != nil {
		spanRef := opentracing.ChildOf(span.Context())
		jobSpan = opentracing.StartSpan("ReadSectorJob", spanRef)
		jobSpan.SetTag("root", root)
		jobSpan.SetTag("worker", w.staticHostPubKey.ShortString())
	}

	return &jobReadSector{
		jobRead: jobRead{
			staticResponseChan: respChan,
			staticLength:       length,

			staticLowPrio: queue.staticLowPrio,
			staticSpan:    jobSpan,

			jobGeneric: newJobGeneric(ctx, w.staticJobReadQueue, metadata),
		},
		staticOffset: offset,
		staticSector: root,
	}
}

// ReadSector is a helper method to run a ReadSector job with low priority on a
// worker.
func (w *worker) ReadSectorLowPrio(ctx context.Context, category spendingCategory, root crypto.Hash, offset, length uint64) ([]byte, error) {
	// Create the job metadata
	jobMetadata := jobReadMetadata{
		staticWorker:           w,
		staticSectorRoot:       root,
		staticSpendingCategory: category,
	}

	// Create the job
	readSectorRespChan := make(chan *jobReadResponse)
	jro := w.newJobReadSector(ctx, w.staticJobLowPrioReadQueue, readSectorRespChan, jobMetadata, root, offset, length)

	// Add the job to the queue.
	if !w.staticJobReadQueue.callAdd(jro) {
		// If the Add call fails then the worker is unavailable either for the
		// queue being killed of the queue being on cooldown.
		contextStr := "worker unavailable:"
		if w.staticJobReadQueue.callIsKilled() {
			contextStr = fmt.Sprintf("%v: job queue killed", contextStr)
		} else {
			contextStr = fmt.Sprintf("%v: job queue on cooldown", contextStr)
		}
		return nil, errors.New(contextStr)
	}

	// Wait for the response.
	var resp *jobReadResponse
	select {
	case <-ctx.Done():
		return nil, errors.New("Read interrupted")
	case resp = <-readSectorRespChan:
	}
	return resp.staticData, resp.staticErr
}

// ReadSector is a helper method to run a ReadSector job on a worker.
func (w *worker) ReadSector(ctx context.Context, category spendingCategory, root crypto.Hash, offset, length uint64) ([]byte, error) {
	// Create the job metadata.
	jobMetadata := jobReadMetadata{
		staticWorker:           w,
		staticSectorRoot:       root,
		staticSpendingCategory: category,
	}

	// Create the job.
	readSectorRespChan := make(chan *jobReadResponse)
	jro := w.newJobReadSector(ctx, w.staticJobReadQueue, readSectorRespChan, jobMetadata, root, offset, length)

	// Add the job to the queue.
	if !w.staticJobReadQueue.callAdd(jro) {
		return nil, errors.New("worker unavailable")
	}

	// Wait for the response.
	var resp *jobReadResponse
	select {
	case <-ctx.Done():
		return nil, errors.New("Read interrupted")
	case resp = <-readSectorRespChan:
	}
	return resp.staticData, resp.staticErr
}

// readSectorJobExpectedBandwidth is a helper function that returns the expected
// bandwidth consumption of a read sector job. This helper function takes a
// length parameter and is used to get the expected bandwidth without having to
// instantiate a job.
func readSectorJobExpectedBandwidth(length uint64) (ul, dl uint64) {
	ul = 1 << 15                              // 32 KiB
	dl = uint64(float64(length)*1.01) + 1<<14 // (readSize * 1.01 + 16 KiB)
	return
}
