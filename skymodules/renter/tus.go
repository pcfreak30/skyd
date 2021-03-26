package renter

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"

	"github.com/tus/tusd/pkg/handler"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/skynetlabs/skyd/skymodules"
)

type (
	// skynetTUSUploader implements multiple TUS interfaces for skynet uploads
	// allowing for resumable uploads.
	skynetTUSUploader struct {
		uploads map[string]*skynetTUSUpload

		staticRenter *Renter
	}

	// skynetTUSUpload implements multiple TUS interfaces for uploads.
	skynetTUSUpload struct {
		fi handler.FileInfo
	}
)

// newSkynetTUSUploader creates a new uploader.
func newSkynetTUSUploader() *skynetTUSUploader {
	return &skynetTUSUploader{
		uploads: make(map[string]*skynetTUSUpload),
	}
}

// SkynetTUSUploader returns the renter's uploader for registering in the API.
func (r *Renter) SkynetTUSUploader() skymodules.SkynetTUSDataStore {
	return r.staticSkynetTUSUploader
}

// NewUpload creates a new upload from fileinfo.
func (r *skynetTUSUploader) NewUpload(ctx context.Context, info handler.FileInfo) (handler.Upload, error) {
	i, _ := json.MarshalIndent(info, "", "\t")
	println(string(i))
	info.ID = hex.EncodeToString(fastrand.Bytes(16))
	upload := &skynetTUSUpload{
		fi: info,
	}
	r.uploads[info.ID] = upload
	fmt.Println("created with", info.ID)
	return upload, nil
}

// GetUpload returns an existing upload.
func (r *skynetTUSUploader) GetUpload(ctx context.Context, id string) (handler.Upload, error) {
	upload, exists := r.uploads[id]
	if !exists {
		return nil, os.ErrNotExist
	}
	fmt.Println("get upload", id, upload.fi.Offset)
	return upload, nil
}

// WriteChunk writes the chunk to the provided offset.
func (u *skynetTUSUpload) WriteChunk(ctx context.Context, offset int64, src io.Reader) (int64, error) {
	b, _ := ioutil.ReadAll(src)
	n := int64(len(b))
	fmt.Println("offset", offset, len(b))
	u.fi.Offset += n
	return n, nil
}

// GetInfo returns the file info.
func (u *skynetTUSUpload) GetInfo(ctx context.Context) (handler.FileInfo, error) {
	return u.fi, nil
}

// GetReader returns a reader for the upload.
func (u *skynetTUSUpload) GetReader(ctx context.Context) (io.Reader, error) {
	fmt.Println("get reader")
	return bytes.NewReader([]byte{}), nil
}

// FinishUpload is called when the upload is done.
func (u *skynetTUSUpload) FinishUpload(ctx context.Context) error {
	fmt.Println("finish")
	return nil
}
