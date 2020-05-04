package renter

/*
// workerasync.go contains a bunch of state and methods specific to operating
// the worker asynchronously.

import (
	"time"
)

const (
	// We want to measure the total amount of data that comes through the worker
	// within a span of 5 minutes.
	throughputMeasurementWindow = time.Minute * 5
)

// workerPerformanceMetrics attempts to track how well a worker is performing.
type workerPerformanceMetrics struct {
	// The throughput weights are an exponential weighted average of the amount
	// of data that has come in over the past 'throughputMeasurementWindow'
	// amount of time. Some tricks are used to ensure that the number is
	// approximately accurate even if the worker is not doing work consistently
	// throughout the 5 minutes.
	readThroughputTime time.Time
	readThroughputWeight  float64
	writeThroughputTime time.Time
	writeThroughputWeight float64

	mu sync.Mutex
}

// registerRead will add some read data to the worker performance metrics. The
// amount of data read and the time that has passed will be used to update the
// measured read throughput of the worker.
//
// TODO: This needs to move to the siamux, instead of existing on the worker.
// The siamux is closer to the wire and can get a more proper measurement of the
// actual throughput for reads and writes.
func (wpm *workerPerformanceMetrics) registerRead(readStart time.Time, bytesRead uint64) {
	// Determine how much exponential decay to apply. If there have not been any
	// reads since this read started, use the amount of time that the read took
	// instead of the amount of time that has passed since the previous read.
	// This puts the measurement closer to the capabilities of the worker,
	// instead of closer to the actual usage of the worker.
	decayTime := wpm.readThroughputTime
	if readStart.After(decayTime) {
		decayTime = readStart
	}

	// Determine how many bytes the worker typically reads in the same
	// timeframe.
	averageBytesReadInSameTime := wpm.readThroughputWeight / float64(throughputMeasurementWindow / decayTime)
	// Determine a strength level for the decay based on the difference between
	// the speed we are seeing on this read and the speed we typically see. This
	// bias the exponential average towards the higher visible thorughputs.

	// This is a trick to bias the exponential average towards 

	// Determine how much decay to apply to the existing value.
	timePassed := 
}



// When we are looking at the set of workers, and deciding which ones to use,
// the key question we want to ask ourselves is, how long is it going to take
// use to get a response back? And that requires understanding what sort of
// bandwidth is going to be transfered with what priority before us.
//
// Which means that the question we ask the worker needs to have some
// understanding of what the competition is, and what the probability is that we
// have to wait through all of the competition to get a response.
//
// On the write side, I think it pretty much makes sense to linearize things. We
// should have some idea of how many write bytes are in front of us, and then
// divide that by the throughput to get a sense of what the wait time is on
// writing data out to the network.

// Load, means add _this_ much latency to your understanding of how long the
// worker would take otherwise. The load is the number of milliseconds of stuff
// that is in front of you on average, and you will need to add those
// milliseconds to your estimate of how long the job is supposed to take without
// load for this worker.

// So each job is going to need to track how long it takes, and how much
// bandwidth it consumes. And then it's going to need to estimate how much of
// the wait was due to inherent slowness factors related to the job (RT latency,
// etc), versus how much slowness was due to the worker being under load.


// And this load stuff doesn't just apply to the async worker, it applies to the
// worker as well, because the worker worker is using the same pipe.

// Okay. So what we're going to do is estimate how much time it takes to perform
// a job, based on how long it takes the job, combined with how long the
// estimated overhead is for performing the job alongside existing work. Even if
// the estimates are a bit volatile, this should be more or less self
// correcting.

// What we need is for each job to know how mu 




// workerAsync contains all of the state relevant to using the worker
// asynchronously.
type workerAsync struct {
	// workerOutboundUsage is the total amount of bandwidth that the worker is
	// using for outbound jobs.
	workerOutboundUsage float64
	workerInboundUsage float64

	// TODO: Need some way to measure the total amount of throughput available
	// to the worker, and then need to 
}

// outboundLoad attemts to measure how much load the worker is under for
// outbound bandwidth. A load of 0 means that the worker has no jobs in flight
// at the moment, any job that comes through should run at full speed. A load of
// 1 indicates that the worker is at full capacity, jobs are running at
// approximately full speed but more jobs will slow the worker down. A load of 2
// means the worker is running at half capacity - jobs are on average taking
// twice as long as they would if the worker had less load. Load is linear in
// the amount of work the worker has.
//
// Load is measured the average bandwidth required by all jobs currently queued
// up divided by the measured throughput of the worker.
func (wa *workerAsync) outboundLoad() float64 {
	return wa.workerOutboundUsage / wa.workerOutboundThroughput
}

// inboundLoad attemts to measure how much load the worker is under for
// inbound bandwidth. A load of 0 means that the worker has no jobs in flight
// at the moment, any job that comes through should run at full speed. A load of
// 1 indicates that the worker is at full capacity, jobs are running at
// approximately full speed but more jobs will slow the worker down. A load of 2
// means the worker is running at half capacity - jobs are on average taking
// twice as long as they would if the worker had less load. Load is linear in
// the amount of work the worker has.
//
// Load is measured the average bandwidth required by all jobs currently queued
// up divided by the measured throughput of the worker.
func (wa *workerAsync) inboundLoad() float64 {
	return wa.workerInboundUsage / wa.workerInboundThroughput
}

// bandwidthTime estimates the amount of time that it would take a worker to
// do all of the networking transfer for a job based on the amount of data that
// needs to be sent and received for the job.
//
// This does not account for processing time needed from the host, and is at
// best a rough estimate. The purpose of this function is return worse estimates
// if the worker has a lot of data queued already, giving the renter a chance to
// prefer using other workers.
func (wa *workerAsync) bandwidthTime(estimatedLatency float64, outboundBytes, inboundBytes uint64) time.Duration {
	// outboundTime should be fairly accurate because we can measure precisely
	// when data is sent out, and subtract it from the outboundQueue in time.
	totalOutbound := wa.outboundQueue + outboundBytes
	outboundTime := float64(totalOutbound) * wa.estimatedOutboundThroughput

	// inboundTime is significantly more difficult to estimate correctly.
	// Traffic doesn't start getting sent by the host until the job is received
	// by the host. The latency on beginning to use the inbound throughput is
	// therefore however long it takes the full job to reach the host.
	// Furthermore, for inbound data in the queue, we are not certain yet how
	// much of that data has started to send, and whether it cannot start until
	// a large upload job can be completed.
	//
	// So the first major complexity that we are skipping is measuring how much
	// time it takes for the host to begin a job. That is dead time, where the
	// throghput is not being used.
	//
	// The second major complexity that we are skipping is measuring how much
	// data has likely already been sent by the host, and is in flight. This is
	// data that we perceive to be in the queue which is not actually
	// contributing to the total amount of time required.
	//
	// Skipping both of these things at once means they somewhat balance out.
	// This also means that getting more accurate data on one but not the other
	// may actually make the overall estimate worse, so if we want to improve
	// the accuracy of this function, we should solve both issues at the same
	// time.
	totalInbound := wa.inboundQueue + inboundBytes
	inboundTime := float64(totalInbound) * wa.estimatedInboundThroughput

	// Add the outboundTime, the inboundTime, and the round trip latency
	// together into the returned estimate.
	totalTime := outboundTime + inboundTime + estimatedLatency
}
*/
