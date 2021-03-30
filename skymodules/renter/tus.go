package renter

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"os"

	"github.com/tus/tusd/pkg/handler"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/skynetlabs/skyd/build"
	"gitlab.com/skynetlabs/skyd/skymodules"
	"gitlab.com/skynetlabs/skyd/skymodules/renter/filesystem"
)

// TODO: periodically remove old uploads.

type (
	// skynetTUSUploader implements multiple TUS interfaces for skynet uploads
	// allowing for resumable uploads.
	skynetTUSUploader struct {
		uploads map[string]*skynetTUSUpload

		staticRenter *Renter
	}

	// skynetTUSUpload implements multiple TUS interfaces for uploads.
	skynetTUSUpload struct {
		fi             handler.FileInfo
		staticUploader *skynetTUSUploader
		staticSUP      skymodules.SkyfileUploadParameters
		staticUP       skymodules.FileUploadParams
		staticFN       *filesystem.FileNode
	}
)

// newSkynetTUSUploader creates a new uploader.
func newSkynetTUSUploader(renter *Renter) *skynetTUSUploader {
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
	upload := &skynetTUSUpload{
		fi:             info,
		staticUploader: r,
	}
	r.uploads[info.ID] = upload

	// Get a siapath from the metadata if possible.
	spStr, exists := upload.fi.MetaData["SiaPath"]
	if !exists {
		// TODO: set a better siapath. Ideally similar to nginx.
		spStr = skymodules.RandomSiaPath().String()
		upload.fi.MetaData["SiaPath"] = spStr
	}
	sp, err := skymodules.NewSiaPath(spStr)
	if err != nil {
		return nil, err
	}

	// Create the skyfile upload params.
	// TODO: use info.metadata to create skyfileuploadparameters different from
	// the default.
	upload.staticSUP = skymodules.SkyfileUploadParameters{
		SiaPath:             sp,
		Filename:            sp.Name(),
		BaseChunkRedundancy: SkyfileDefaultBaseChunkRedundancy,
	}

	// Create the FileUploadParams
	upload.staticUP, err = fileUploadParams(sp, skymodules.RenterDefaultDataPieces, skymodules.RenterDefaultParityPieces, upload.staticUP.Force, crypto.TypePlain)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create FileUploadParams for large file")
	}

	// Generate a Cipher Key for the FileUploadParams.
	err = generateCipherKey(&upload.staticUP, upload.staticSUP)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create Cipher key for FileUploadParams")
	}

	// Check the upload params first and create a fileNode.
	upload.staticFN, err = r.staticRenter.managedInitUploadStream(upload.staticUP)
	if err != nil {
		return nil, err
	}
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
	uploader := u.staticUploader
	fileNode := u.staticFN

	// Sanity check offset.
	// NOTE: If the offset is not chunk aligned, it means that a previous call
	// to WriteChunk read an incomplete chunk from src and padded it. After
	// uploading a padded chunk, we can't upload more chunks. That's why the
	// client needs to make sure that the chunkSize they use is aligned with the
	// chunkSize of the skyfile's fanout.
	if offset%int64(fileNode.ChunkSize()) != 0 {
		err := errors.New("can't append chunk to offset that is not chunk aligned")
		if build.Release == "testing" {
			// In test builds we want to be aware of this.
			build.Critical(err)
		}
		// Upload failed. Remove and close fileNode.
		return 0, errors.Compose(err, fileNode.Delete(), fileNode.Close())
	}

	// Upload
	n, err := uploader.staticRenter.callUploadStreamFromReaderWithFileNode(fileNode, src, offset)
	u.fi.Offset += n
	return n, err
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
	// Close fileNode.
	_ = u.staticFN.Close()

	// Create metadata.
	sup := u.staticSUP
	_ = skymodules.SkyfileMetadata{
		Filename:     sup.Filename,
		Length:       uint64(u.fi.Size),
		Mode:         sup.Mode,
		Monetization: sup.Monetization,
	}

	// Get fanout.

	// TODO: compute skylink.
	return nil
}
