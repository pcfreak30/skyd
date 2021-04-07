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
type downloadSkylinkTrace struct {
	// NOTE: the sub-traces are not pointers, this is by design. They live on
	// the parent object, and get passed around as references from the parent.
	staticStart             time.Time
	skylinkDataSourceTrace  skylinkDataSourceTrace
	staticStreamerAvailable time.Time
	skyfileStreamerTrace    skyfileStreamerTrace

	staticRenter *Renter
}

// skylinkDataSourceTrace traces a call to skylinkDataSource().
type skylinkDataSourceTrace struct {
	// Timings for initializing the data source.
	staticStart                time.Time
	staticDownloadByRootTime   time.Time
	staticFetcherCreateTimes   []time.Time
	staticChunkFetchersCreated time.Time
	staticNumChunkFetchers     int

	// Timings for satisfying datasource requests.
	totalReadRequests int
	randomRequest     skylinkDataSourceReadTrace
	slowRequests      []skylinkDataSourceReadTrace
	allRequests       []skylinkDataSourceReadTrace // TODO: Remove this, we just have it for initial debugging.
	mu                sync.Mutex
}

// skylinkDataSourceReadTrace traces a call to ReadStream in a
// skylinkDataSource.
type skylinkDataSourceReadTrace struct {
	staticStart    time.Time
	staticLaunch   time.Time
	staticComplete time.Time
}

// skyfileStreamerTrace traces calls to a SkyfileStreamer.
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
