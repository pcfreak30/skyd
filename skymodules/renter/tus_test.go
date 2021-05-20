package renter

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/tus/tusd/pkg/handler"
	"gitlab.com/NebulousLabs/fastrand"
	"go.sia.tech/siad/modules"
)

// TestWriteChunkSetLastWrite is a unit test that confirms lastWrite is updated
// correctly on uploads.
func TestWriteChunkSetLastWrite(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	wt, err := newWorkerTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := wt.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	r := wt.rt.renter
	tu := r.staticSkynetTUSUploader

	// Small upload.
	info := handler.FileInfo{
		MetaData: make(handler.MetaData),
		Size:     1,
	}
	upload, err := tu.NewUpload(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	stu := upload.(*skynetTUSUpload)
	if stu.lastWrite.IsZero() {
		t.Fatal("lastWrite wasn't initialized")
	}
	start := time.Now()
	n, err := upload.WriteChunk(context.Background(), 0, bytes.NewReader(fastrand.Bytes(int(info.Size))))
	if err != nil {
		t.Fatal(err)
	}
	if n != info.Size {
		t.Fatal("wrong n", n)
	}
	if start.After(stu.lastWrite) {
		t.Fatal("last write wasn't updated", start, stu.lastWrite)
	}

	// Large upload.
	info = handler.FileInfo{
		MetaData: make(handler.MetaData),
		Size:     int64(modules.SectorSize) + 1,
	}
	upload, err = tu.NewUpload(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	stu = upload.(*skynetTUSUpload)
	if stu.lastWrite.IsZero() {
		t.Fatal("lastWrite wasn't initialized")
	}
	start = time.Now()
	n, err = upload.WriteChunk(context.Background(), 0, bytes.NewReader(fastrand.Bytes(int(info.Size))))
	if err != nil {
		t.Fatal(err)
	}
	if n != info.Size {
		t.Fatal("wrong n", n)
	}
	if start.After(stu.lastWrite) {
		t.Fatal("last write wasn't updated", start, stu.lastWrite)
	}
}
