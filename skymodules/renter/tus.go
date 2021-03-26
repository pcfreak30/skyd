package renter

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"os"
	"sync"

	"github.com/tus/tusd/pkg/handler"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/skynetlabs/skyd/skymodules"
)

type (
	// skynetTUSUploader implements multiple TUS interfaces for skynet uploads
	// allowing for resumable uploads.
	skynetTUSUploader struct {
		uploads  map[string]*skynetTUSUpload
		uploadMu sync.Mutex

		staticRenter skymodules.Renter
	}

	// skynetTUSUpload implements multiple TUS interfaces for uploads.
	// TODO: build the skyfile metadata and fanout.
	skynetTUSUpload struct {
		fi             handler.FileInfo
		staticUploader *skynetTUSUploader

		err              error
		staticResultChan chan struct{}
		staticPipeWriter *io.PipeWriter
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
	r.uploadMu.Lock()
	r.uploads[info.ID] = upload
	r.uploadMu.Unlock()

	// Create the upload params.
	// TODO: add a mechanism to stop the upload if no data has been received for
	// a while.
	// TODO: set a better siapath. Ideally similar to nginx.
	// TODO: figure out how to get input params for the upload from the metadata
	// to override the defaults.
	up := skymodules.FileUploadParams{
		DisablePartialChunk: true, // Disable partial chunk for this
		SiaPath:             skymodules.RandomSiaPath(),
		CipherType:          crypto.TypeDefaultRenter,
	}

	// Run the upload in the background.
	go func(up skymodules.FileUploadParams) {
		upload.err = r.staticRenter.UploadStreamFromReader(up, pipeReader)
		// Close the reader. If this happens before the writes are done, the
		// writer will return the upload.err.
		pipeReader.CloseWithError(upload.err)

		// Close the result chan.
		close(upload.staticResultChan)
	}(up)
	return upload, nil
}

// GetUpload returns an existing upload.
func (r *skynetTUSUploader) GetUpload(ctx context.Context, id string) (handler.Upload, error) {
	r.uploadMu.Lock()
	upload, exists := r.uploads[id]
	r.uploadMu.Unlock()
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
	// Delete the upload from the map.
	u.staticUploader.uploadMu.Lock()
	delete(u.staticUploader.uploads, u.fi.ID)
	u.staticUploader.uploadMu.Unlock()
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
