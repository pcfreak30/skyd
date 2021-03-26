package renter

import (
	"context"

	"github.com/tus/tusd/pkg/handler"
	"gitlab.com/skynetlabs/skyd/skymodules"
)

type (
	// skynetTUSUploader implements multiple TUS interfaces for skynet uploads
	// allowing for resumable uploads.
	skynetTUSUploader struct {
		staticRenter *Renter
	}

	// SkynetTUSUpload implements multiple TUS interfaces for uploads.
	SkynetTUSUpload struct {
	}
)

// newSkynetTUSUploader creates a new uploader.
func newSkynetTUSUploader() *skynetTUSUploader {
	return &skynetTUSUploader{}
}

// SkynetTUSUploader returns the renter's uploader for registering in the API.
func (r *Renter) SkynetTUSUploader() skymodules.SkynetTUSDataStore {
	return r.staticSkynetTUSUploader
}

// NewUpload creates a new upload from fileinfo.
func (r *skynetTUSUploader) NewUpload(ctx context.Context, info handler.FileInfo) (upload handler.Upload, err error) {
	panic("not implemented yet")
}

// GetUpload returns an existing upload.
func (r *skynetTUSUploader) GetUpload(ctx context.Context, id string) (upload handler.Upload, err error) {
	panic("not implemented yet")
}

// AsConcatableUpload implements the ConcaterDataStore interface which allows
// for combining partial upoads into full uploads.
func (r *skynetTUSUploader) AsConcatableUpload(upload handler.Upload) handler.ConcatableUpload {
	panic("not implemented yet")
}

// AsLengthDeclarableUpload implements the LengthDeferrerDataStore that allows
// for uploading data before knowing its size.
func (r *skynetTUSUploader) AsLengthDeclarableUpload(upload handler.Upload) handler.LengthDeclarableUpload {
	panic("not implemented yet")
}

// AsTerminatableUpload implements the TerminatorDataStore which enables
// terminating ongoing uploads.
func (r *skynetTUSUploader) AsTerminatableUpload(upload handler.Upload) handler.TerminatableUpload {
	panic("not implemented yet")
}
