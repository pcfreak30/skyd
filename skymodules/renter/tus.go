package renter

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"os"
	"time"

	"github.com/tus/tusd/pkg/handler"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/skynetlabs/skyd/skymodules"
)

// TODO: periodically remove old uploads.

type (
	// skynetTUSUploader implements multiple TUS interfaces for skynet uploads
	// allowing for resumable uploads.
	skynetTUSUploader struct {
		uploads map[string]*skynetTUSUpload

		staticRenter skymodules.Renter
	}

	// skynetTUSUpload implements multiple TUS interfaces for uploads.
	skynetTUSUpload struct {
		fi               handler.FileInfo
		staticPipeWriter *io.PipeWriter
		staticUploader   *skynetTUSUploader

		// Result
		finishTime       time.Time
		staticResultChan chan struct{}
		err              error
	}
)

// newSkynetTUSUploader creates a new uploader.
func newSkynetTUSUploader(renter skymodules.Renter) *skynetTUSUploader {
	return &skynetTUSUploader{
		uploads:      make(map[string]*skynetTUSUpload),
		staticRenter: renter,
	}
}

// SkynetTUSUploader returns the renter's uploader for registering in the API.
func (r *Renter) SkynetTUSUploader() skymodules.SkynetTUSDataStore {
	return r.staticSkynetTUSUploader
}

// NewUpload creates a new upload from fileinfo.
func (r *skynetTUSUploader) NewUpload(ctx context.Context, info handler.FileInfo) (handler.Upload, error) {
	// Create the upload object.
	info.ID = hex.EncodeToString(fastrand.Bytes(16))
	pipeReader, pipeWriter := io.Pipe()
	upload := &skynetTUSUpload{
		fi:               info,
		staticPipeWriter: pipeWriter,
		staticResultChan: make(chan struct{}),
		staticUploader:   r,
	}
	r.uploads[info.ID] = upload

	// Create the upload params.
	// TODO: add a mechanism to stop the upload if no data has been received for
	// a while.
	// TODO: set a better siapath. Ideally similar to nginx.
	// TODO: use info.metadata to create skyfileuploadparameters different from
	// the default.
	sp := skymodules.RandomSiaPath()
	sup := skymodules.SkyfileUploadParameters{
		BaseChunkRedundancy: SkyfileDefaultBaseChunkRedundancy,
		Filename:            sp.Name(),
		SiaPath:             skymodules.RandomSiaPath(),
	}

	// Run the upload in the background.
	go func(sup skymodules.SkyfileUploadParameters) {
		reader := skymodules.NewSkyfileReader(pipeReader, sup)
		var skylink skymodules.Skylink
		skylink, upload.err = r.staticRenter.UploadSkyfile(sup, reader)

		// set the finish time of the upload.
		upload.finishTime = time.Now()

		// Set skylink in the fileinfo.
		info.MetaData["Skylink"] = skylink.String()

		// Close the reader. If this happens before the writes are done, the
		// writer will return the upload.err.
		pipeReader.CloseWithError(upload.err)

		// Close the result chan.
		close(upload.staticResultChan)
	}(sup)
	return upload, nil
}

// GetUpload returns an existing upload.
func (r *skynetTUSUploader) GetUpload(ctx context.Context, id string) (handler.Upload, error) {
	upload, exists := r.uploads[id]
	if !exists {
		return nil, os.ErrNotExist
	}
	return upload, nil
}

// WriteChunk writes the chunk to the provided offset.
func (u *skynetTUSUpload) WriteChunk(ctx context.Context, offset int64, src io.Reader) (int64, error) {
	written, err := io.Copy(u.staticPipeWriter, src)
	u.fi.Offset += written
	return written, err
}

// GetInfo returns the file info.
func (u *skynetTUSUpload) GetInfo(ctx context.Context) (handler.FileInfo, error) {
	return u.fi, nil
}

// GetReader returns a reader for the upload.
func (u *skynetTUSUpload) GetReader(ctx context.Context) (io.Reader, error) {
	return bytes.NewReader([]byte{}), handler.ErrNotImplemented
}

// FinishUpload is called when the upload is done.
func (u *skynetTUSUpload) FinishUpload(ctx context.Context) error {
	// Close the writer to indicate that no more data is coming.
	// We don't care abou the error at this point.
	_ = u.staticPipeWriter.Close()
	// Wait for the result.
	select {
	case <-ctx.Done():
		return errors.New("interrupted by shutdown")
	case <-u.staticResultChan:
	}
	return u.err
}
