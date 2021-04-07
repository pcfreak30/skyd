package renter

// NOTE: The structs in this file are sorted temporally as opposed to
// alphabetically. We've done our best possible to sort the fields such that the
// things which happen first are listed first.

import (
	"sync"
	"time"
)

type downloadSkylinkStats struct {
}

// downloadSkylinkTrace traces a call to DownloadSkylink.
//
// NOTE: the sub-traces are not pointers, this is by design. They live on the
// parent object, and get passed around as references from the parent.
type downloadSkylinkTrace struct {
	staticStart time.Time

	skylinkDataSourceTrace  skylinkDataSourceTrace
	staticStreamerAvailable time.Duration
	skyfileStreamerTrace    skyfileStreamerTrace

	staticRenter *Renter
}

// skylinkDataSourceTrace traces a call to skylinkDataSource().
type skylinkDataSourceTrace struct {
	staticStart time.Time

	// Timings for initializing the data source.
	downloadByRootTrace        downloadByRootTrace
	staticDownloadByRootTime   time.Duration
	staticFetcherCreateTimes   []int64
	staticChunkFetchersCreated time.Duration

	// Timings for satisfying datasource requests.
	totalReadRequests int
	randomRequest     skylinkDataSourceReadTrace
	slowRequests      []skylinkDataSourceReadTrace
	allRequests       []skylinkDataSourceReadTrace // TODO: Remove this, we just have it for initial debugging.
	mu                sync.Mutex
}

// downloadByRootTrace traces a call to downloadByRoot().
type downloadByRootTrace struct {
	staticStart time.Time

	staticPCWSCreated       time.Duration
	staticDownloadQueued    time.Duration
	staticDownloadCompleted time.Duration

	pdcDownloadTrace        pdcDownloadTrace
}

// pdcDownloadTrace traces a call to managedDownload on a PCWS.
type pdcDownloadTrace struct {
	staticStart time.Time

	staticPDCBuilt             time.Duration
	staticExpectedCompleteTime time.Duration
	staticWorkersLaunched      time.Duration

	staticWorkerFailureTimes   []int64
	staticWorkerSuccessTimes   []int64
	staticOverdriveLaunchTimes []int64
}

// skylinkDataSourceReadTrace traces a call to ReadStream in a
// skylinkDataSource.
type skylinkDataSourceReadTrace struct {
	staticStart time.Time

	staticLaunch   time.Duration
	staticComplete time.Duration

	staticPDCDownloadTraces []pdcDownloadTrace
}

// skyfileStreamerTrace traces calls to a SkyfileStreamer.
//
// TODO: This one will probably give us the best idea of how nginx is
// interacting with skyd, what times it's making requests and how far apart
// those requests are.
type skyfileStreamerTrace struct {
}

// NewDownloadSkylinkTrace will return a trace with timings about the progress
// of a download.
func (r *Renter) NewDownloadSkylinkTrace() *downloadSkylinkTrace {
	return &downloadSkylinkTrace{
		staticStart: time.Now(),

		staticRenter: r,
	}
}
