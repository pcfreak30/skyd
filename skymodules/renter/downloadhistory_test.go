package renter

import (
	"fmt"
	"testing"
	"time"

	"gitlab.com/SkynetHQ/skyd/skymodules"

	"gitlab.com/NebulousLabs/Sia/types"
)

// TestClearDownloads tests all the edge cases of the ClearDownloadHistory Method
func TestClearDownloads(t *testing.T) {
	t.Parallel()

	// Create a downloadHistory
	dh := newDownloadHistory()

	// Test clearing empty download history
	if err := dh.managedClearHistory(time.Time{}, time.Time{}); err != nil {
		t.Fatal(err)
	}

	// Check Clearing individual download from history
	// doesn't exist - before
	length, err := clearDownloadHistory(dh, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dh.managedLength() != length {
		t.Fatal("Download should not have been cleared")
	}
	// doesn't exist - after
	length, err = clearDownloadHistory(dh, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if dh.managedLength() != length {
		t.Fatal("Download should not have been cleared")
	}
	// doesn't exist - within range
	length, err = clearDownloadHistory(dh, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if dh.managedLength() != length {
		t.Fatal("Download should not have been cleared")
	}
	// Remove Last Download
	length, err = clearDownloadHistory(dh, 9, 9)
	if err != nil {
		t.Fatal(err)
	}
	if dh.managedLength() != length-1 {
		t.Fatal("Download should have been cleared")
	}
	dh.mu.Lock()
	for _, d := range dh.history {
		if d.staticStartTime.Unix() == 9 {
			t.Fatal("Download not removed")
		}
	}
	dh.mu.Unlock()
	// Remove First Download
	length, err = clearDownloadHistory(dh, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if dh.managedLength() != length-1 {
		t.Fatal("Download should have been cleared")
	}
	dh.mu.Lock()
	for _, d := range dh.history {
		if d.staticStartTime.Unix() == 2 {
			t.Fatal("Download not removed")
		}
	}
	dh.mu.Unlock()
	// Remove download from middle of history
	length, err = clearDownloadHistory(dh, 6, 6)
	if err != nil {
		t.Fatal(err)
	}
	if dh.managedLength() != length-1 {
		t.Fatal("Download should have been cleared")
	}
	dh.mu.Lock()
	for _, d := range dh.history {
		if d.staticStartTime.Unix() == 6 {
			t.Fatal("Download not removed")
		}
	}
	dh.mu.Unlock()

	// Check Clearing range
	// both exist - first and last
	_, err = clearDownloadHistory(dh, 2, 9)
	if err != nil {
		t.Fatal(err)
	}
	if dh.managedLength() != 0 {
		t.Fatal("Download history should have been cleared")
	}
	// both exist - within range
	_, err = clearDownloadHistory(dh, 3, 8)
	if err != nil {
		t.Fatal(err)
	}

	if !checkDownloadHistory(dh.managedHistory(), []int64{9, 2}) {
		t.Fatal("Download history not cleared as expected")
	}
	// exist - within range and doesn't exist - before
	_, err = clearDownloadHistory(dh, 1, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !checkDownloadHistory(dh.managedHistory(), []int64{9, 8, 6}) {
		t.Fatal("Download history not cleared as expected")
	}
	// exist - within range and doesn't exist - after
	_, err = clearDownloadHistory(dh, 6, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !checkDownloadHistory(dh.managedHistory(), []int64{4, 3, 2}) {
		t.Fatal("Download history not cleared as expected")
	}
	// neither exist - within range and before
	_, err = clearDownloadHistory(dh, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !checkDownloadHistory(dh.managedHistory(), []int64{9, 8, 6}) {
		t.Fatal("Download history not cleared as expected")
	}
	// neither exist - within range and after
	_, err = clearDownloadHistory(dh, 5, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !checkDownloadHistory(dh.managedHistory(), []int64{4, 3, 2}) {
		t.Fatal("Download history not cleared as expected")
	}
	// neither exist - outside
	_, err = clearDownloadHistory(dh, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if dh.managedLength() != 0 {
		t.Fatal("Download history should have been cleared")
	}
	// neither exist - inside
	_, err = clearDownloadHistory(dh, 5, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !checkDownloadHistory(dh.managedHistory(), []int64{9, 8, 4, 3, 2}) {
		t.Fatal("Download history not cleared as expected")
	}

	// Check Clear Before
	// exists - within range
	_, err = clearDownloadHistory(dh, 0, 6)
	if err != nil {
		t.Fatal(err)
	}
	if !checkDownloadHistory(dh.managedHistory(), []int64{9, 8}) {
		t.Fatal("Download history not cleared as expected")
	}
	// exists - last
	_, err = clearDownloadHistory(dh, 0, 9)
	if err != nil {
		t.Fatal(err)
	}
	if dh.managedLength() != 0 {
		t.Fatal("Download history should have been cleared")
	}
	// doesn't exist - within range
	_, err = clearDownloadHistory(dh, 0, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !checkDownloadHistory(dh.managedHistory(), []int64{9, 8}) {
		t.Fatal("Download history not cleared as expected")
	}
	// doesn't exist - before
	length, err = clearDownloadHistory(dh, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dh.managedLength() != length {
		t.Fatal("No downloads should not have been cleared")
	}
	// doesn't exist - after
	_, err = clearDownloadHistory(dh, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if dh.managedLength() != 0 {
		t.Fatal("Download history should have been cleared")
	}

	// Check Clear After
	// exists - within range
	_, err = clearDownloadHistory(dh, 6, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !checkDownloadHistory(dh.managedHistory(), []int64{4, 3, 2}) {
		t.Fatal("Download history not cleared as expected")
	}
	// exist - first
	_, err = clearDownloadHistory(dh, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dh.managedLength() != 0 {
		t.Fatal("Download history should have been cleared")
	}
	// doesn't exist - within range
	_, err = clearDownloadHistory(dh, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !checkDownloadHistory(dh.managedHistory(), []int64{4, 3, 2}) {
		t.Fatal("Download history not cleared as expected")
	}
	// doesn't exist - after
	length, err = clearDownloadHistory(dh, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dh.managedLength() != length {
		t.Fatal("No downloads should not have been cleared")
	}
	// doesn't exist - before
	_, err = clearDownloadHistory(dh, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dh.managedLength() != 0 {
		t.Fatal("Download history should have been cleared")
	}

	// Check clearing entire download history
	_, err = clearDownloadHistory(dh, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dh.managedLength() != 0 {
		t.Fatal("Download History not cleared")
	}
}

// clearDownloadHistory is a helper function for TestClearDownloads, it builds and resets the download
// history of the renter and then calls ClearDownloadHistory and returns the length
// of the original download history
func clearDownloadHistory(dh *downloadHistory, after, before int) (int, error) {
	// Build/Reset download History
	// Skipping 5 and 7 so there are clear times missing that can
	// be referenced
	dh.mu.Lock()
	downloads := make(map[skymodules.DownloadID]*download)
	for i := 2; i < 10; i++ {
		if i != 5 && i != 7 {
			d := &download{
				staticUID:       skymodules.DownloadID(fmt.Sprint(i)),
				staticStartTime: time.Unix(int64(i), 0),
			}
			downloads[d.UID()] = d
		}
	}
	dh.history = downloads
	length := len(dh.history)
	dh.mu.Unlock()

	// clear download history
	var afterTime time.Time
	beforeTime := types.EndOfTime
	if before != 0 {
		beforeTime = time.Unix(int64(before), 0)
	}
	if after != 0 {
		afterTime = time.Unix(int64(after), 0)
	}
	if err := dh.managedClearHistory(afterTime, beforeTime); err != nil {
		return 0, err
	}
	return length, nil
}

// checkDownloadHistory is a helper function for TestClearDownloads
// it compares the renter's download history against what is expected
// after ClearDownloadHistory is called
func checkDownloadHistory(downloads []skymodules.DownloadInfo, check []int64) bool {
	if downloads == nil && check == nil {
		return true
	}
	if downloads == nil || check == nil {
		return false
	}
	if len(downloads) != len(check) {
		return false
	}
	for i := range downloads {
		if downloads[i].StartTime.Unix() != check[i] {
			return false
		}
	}
	return true
}
