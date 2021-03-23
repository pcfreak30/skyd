package skymodules

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
)

// TestDiskBuffer runs all tests related to the disk buffer.
func TestDiskBuffer(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	t.Run("OpenCloseCleanup", testDiskBufferOpenCloseCleanup)
	t.Run("WriteThenRead", testDiskBufferWriteThenRead)
	t.Run("ReadBlocking", testDiskBufferReadBlocking)
	t.Run("CloseUnblockRead", testDiskBufferCloseUnblockRead)
}

// testDiskBufferOpenCloseCleanup tests creating a buffer, closing it and
// cleaning it up.
func testDiskBufferOpenCloseCleanup(t *testing.T) {
	// Get test dir
	testDir := modulesTestDir(t.Name())

	// Create a buffer.
	db, err := newDiskBuffer(testDir)
	if err != nil {
		t.Fatal(err)
	}
	// The file should exist on disk.
	if _, err := os.Stat(db.staticFile.Name()); err != nil {
		t.Fatal(err)
	}
	// Close the buffer.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Cleanup the buffer.
	if err := db.Cleanup(); err != nil {
		t.Fatal(err)
	}
	// The file should be gone.
	if _, err := os.Stat(db.staticFile.Name()); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// testDiskBufferWriteThenRead tests alternating between writes and reads of the
// same length.
func testDiskBufferWriteThenRead(t *testing.T) {
	// Get test dir
	testDir := modulesTestDir(t.Name())

	// Create a buffer.
	db, err := newDiskBuffer(testDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := db.Cleanup(); err != nil {
			t.Fatal(err)
		}
	}()

	writeLen := 10
	for i := 0; i < 10; i++ {
		// Write some bytes.
		writtenData := fastrand.Bytes(writeLen)
		n, err := db.Write(writtenData)
		if err != nil {
			t.Fatal(err)
		}
		if n != len(writtenData) {
			t.Fatal("wrong n", n)
		}
		// The remaining bytes and the offset should increase.
		if db.remaining != n {
			t.Fatal("wrong remaining", db.remaining)
		}
		if db.writeOffset != int64(n)*int64(i+1) {
			t.Fatal("wrong offset", db.writeOffset)
		}
		// Read the data back. It should match.
		readData := make([]byte, len(writtenData))
		n, err = db.Read(readData)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(writtenData, readData) {
			t.Fatal("bytes don't match")
		}
		// Remaining should be 0 again.
		if db.remaining != 0 {
			t.Fatal("wrong remaining", db.remaining)
		}
	}
}

// testDiskBufferReadBlocking tests the blocking behavior of Read.
func testDiskBufferReadBlocking(t *testing.T) {
	// Get test dir
	testDir := modulesTestDir(t.Name())

	// Create a buffer.
	db, err := newDiskBuffer(testDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := db.Cleanup(); err != nil {
			t.Fatal(err)
		}
	}()

	// Read some data. This will block.
	readData := make([]byte, 10)
	unblocked := make(chan struct{})
	go func() {
		defer close(unblocked)
		_, err := io.ReadFull(db, readData)
		if err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-unblocked:
		t.Fatal("unblocked too soon")
	case <-time.After(time.Second):
	}

	// Write some data but not enough. Should still block.
	writtenData := fastrand.Bytes(15)
	_, err = db.Write(writtenData[:5])
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-unblocked:
		t.Fatal("unblocked too soon")
	case <-time.After(time.Second):
	}

	// Write more data. Should unblock.
	_, err = db.Write(writtenData[5:])
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-time.After(5 * time.Second):
		t.Fatal("didn't unblock")
	case <-unblocked:
	}

	// Read data should match.
	if !bytes.Equal(writtenData[:10], readData) {
		t.Fatal("data doesn't match")
	}

	// Read the last bytes as well.
	readData = make([]byte, 5)
	n, err := db.Read(readData)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatal("wrong n", n)
	}
	if !bytes.Equal(writtenData[10:], readData) {
		t.Fatal("data doesn't match")
	}
}

// testDiskBufferCloseUnblockRead makes sure that Close correctly unblocks
// ongoing reads.
func testDiskBufferCloseUnblockRead(t *testing.T) {
	// Get test dir
	testDir := modulesTestDir(t.Name())

	// Create a buffer.
	db, err := newDiskBuffer(testDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Cleanup(); err != nil {
			t.Fatal(err)
		}
	}()

	// Read some data. This will block.
	var n int
	var errRead error
	readData := make([]byte, 10)
	unblocked := make(chan struct{})
	go func() {
		defer close(unblocked)
		n, errRead = db.Read(readData)
		if err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-unblocked:
		t.Fatal("unblocked too soon")
	case <-time.After(time.Second):
	}

	// Write some data but not enough. Should still block.
	writtenData := fastrand.Bytes(10)
	_, err = db.Write(writtenData[:5])
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-unblocked:
		t.Fatal("unblocked too soon")
	case <-time.After(time.Second):
	}

	// Unblock the read by closing the buffer.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-time.After(5 * time.Second):
		t.Fatal("didn't unblock")
	case <-unblocked:
	}

	// Data should match.
	if errRead != nil {
		t.Fatal("wrong err", errRead)
	}
	if n != 5 {
		t.Fatal("wrong n", n)
	}
	if !bytes.Equal(readData[:5], writtenData[:5]) {
		t.Fatal("wrong data")
	}

	// Read one more byte. Should return 0, io.EOF.
	b := make([]byte, 1)
	n, err = db.Read(b)
	if !errors.Contains(err, io.EOF) {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("wrong n")
	}
}
