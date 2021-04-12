package renter

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetHQ/skyd/skymodules"
)

// downloadHistory contains a history of the downloads that have occurred during
// a session
type downloadHistory struct {
	history map[skymodules.DownloadID]*download
	mu      sync.Mutex
}

// newDownloadHistory returns a newly initialized download history.
func newDownloadHistory() *downloadHistory {
	return &downloadHistory{
		history: make(map[skymodules.DownloadID]*download),
	}
}

// callAddDownload adds a download to the history
func (dh *downloadHistory) callAddDownload(d *download) {
	dh.mu.Lock()
	defer dh.mu.Unlock()
	dh.history[d.UID()] = d
}

// callFetchDownload fetches the download with the corresponding UID
func (dh *downloadHistory) callFetchDownload(uid skymodules.DownloadID) (*download, bool) {
	dh.mu.Lock()
	defer dh.mu.Unlock()
	d, ok := dh.history[uid]
	return d, ok
}

// managedClearHistory clears the download history inclusive of the provided
// before and after timestamps
func (dh *downloadHistory) managedClearHistory(after, before time.Time) error {
	dh.mu.Lock()
	defer dh.mu.Unlock()

	// Check to confirm there are downloads to clear
	if len(dh.history) == 0 {
		return nil
	}

	// Timestamp validation
	if before.Before(after) {
		return errors.New("before timestamp can not be newer then after timestamp")
	}

	// Clear download history if both before and after timestamps are zero values
	if before.Equal(types.EndOfTime) && after.IsZero() {
		dh.history = make(map[skymodules.DownloadID]*download)
		return nil
	}

	// Find and return downloads that are not within the given range
	withinTimespan := func(t time.Time) bool {
		return (t.After(after) || t.Equal(after)) && (t.Before(before) || t.Equal(before))
	}
	filtered := make(map[skymodules.DownloadID]*download)
	for _, d := range dh.history {
		if !withinTimespan(d.staticStartTime) {
			filtered[d.UID()] = d
		}
	}
	dh.history = filtered
	return nil
}

// managedHistory returns the list of downloads that have been performed. Will
// include downloads that have not yet completed. Downloads will be roughly, but
// not precisely, sorted according to start time.
func (dh *downloadHistory) managedHistory() []skymodules.DownloadInfo {
	dh.mu.Lock()
	defer dh.mu.Unlock()

	// Get a slice of the history sorted from least recent to most recent.
	downloadHistory := make([]*download, 0, len(dh.history))
	for _, d := range dh.history {
		downloadHistory = append(downloadHistory, d)
	}
	sort.Slice(downloadHistory, func(i, j int) bool {
		return downloadHistory[i].staticStartTime.Before(downloadHistory[j].staticStartTime)
	})

	downloads := make([]skymodules.DownloadInfo, len(downloadHistory))
	for i := range downloadHistory {
		// Order from most recent to least recent.
		d := downloadHistory[len(dh.history)-i-1]
		d.mu.Lock() // Lock required for d.endTime only.
		downloads[i] = skymodules.DownloadInfo{
			Destination:     d.destinationString,
			DestinationType: d.staticDestinationType,
			Length:          d.staticLength,
			Offset:          d.staticOffset,
			SiaPath:         d.staticSiaPath,

			Completed:            d.staticComplete(),
			EndTime:              d.endTime,
			Received:             atomic.LoadUint64(&d.atomicDataReceived),
			StartTime:            d.staticStartTime,
			StartTimeUnix:        d.staticStartTime.UnixNano(),
			TotalDataTransferred: atomic.LoadUint64(&d.atomicTotalDataTransferred),
		}
		// Release download lock before calling d.Err(), which will acquire the
		// lock. The error needs to be checked separately because we need to
		// know if it's 'nil' before grabbing the error string.
		d.mu.Unlock()
		if d.Err() != nil {
			downloads[i].Error = d.Err().Error()
		} else {
			downloads[i].Error = ""
		}
	}
	return downloads
}

// managedLength returns the length of the download history
func (dh *downloadHistory) managedLength() int {
	dh.mu.Lock()
	defer dh.mu.Unlock()
	return len(dh.history)
}

// ClearDownloadHistory clears the renter's download history inclusive of the
// provided before and after timestamps
//
// TODO: This function can be improved by implementing a binary search, the
// trick will be making the binary search be just as readable while handling
// all the edge cases
func (r *Renter) ClearDownloadHistory(after, before time.Time) error {
	if err := r.tg.Add(); err != nil {
		return err
	}
	defer r.tg.Done()
	return r.staticDownloadHistory.managedClearHistory(after, before)
}

// DownloadHistory returns the list of downloads that have been performed. Will
// include downloads that have not yet completed. Downloads will be roughly,
// but not precisely, sorted according to start time.
//
// TODO: Currently the DownloadHistory only contains downloads from this
// session, does not contain downloads that were executed for the purposes of
// repairing, and has no way to clear the download history if it gets long or
// unwieldy. It's not entirely certain which of the missing features are
// actually desirable, please consult core team + app dev community before
// deciding what to implement.
func (r *Renter) DownloadHistory() ([]skymodules.DownloadInfo, error) {
	if err := r.tg.Add(); err != nil {
		return nil, err
	}
	defer r.tg.Done()
	return r.staticDownloadHistory.managedHistory(), nil
}
