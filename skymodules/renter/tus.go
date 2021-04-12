package renter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/tus/tusd/pkg/handler"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetHQ/skyd/build"
	"gitlab.com/SkynetHQ/skyd/skymodules"
	"gitlab.com/SkynetHQ/skyd/skymodules/renter/filesystem"
)

var (
	// PruneTUSUploadTimeout is the time of inactivity after which a
	// skynetTUSUpload is pruned from skynetTUSUploader. Inactivity refers to
	// the time passed since WriteChunk was called.
	PruneTUSUploadTimeout = build.Select(build.Var{
		Dev:      5 * time.Minute,
		Standard: 20 * time.Minute,
		Testing:  5 * time.Second,
	}).(time.Duration)

	// PruneTUSUploadInterval is the time that passes between pruning attempts.
	// The smaller the interval, the smaller the batches of uploads we prune at
	// a time.
	PruneTUSUploadInterval = build.Select(build.Var{
		Dev:      time.Minute,
		Standard: 5 * time.Minute,
		Testing:  time.Second,
	}).(time.Duration)
)

type (
	// skynetTUSUploader implements multiple TUS interfaces for skynet uploads
	// allowing for resumable uploads.
	skynetTUSUploader struct {
		uploads map[string]*skynetTUSUpload

		staticRenter *Renter
		mu           sync.Mutex
	}

	// skynetTUSUpload implements multiple TUS interfaces for uploads.
	skynetTUSUpload struct {
		fi        handler.FileInfo
		fanout    []byte
		lastWrite time.Time
		closed    bool
		complete  bool

		staticUploader *skynetTUSUploader
		staticSUP      skymodules.SkyfileUploadParameters
		staticUP       skymodules.FileUploadParams
		staticFN       *filesystem.FileNode

		mu sync.Mutex
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
func (stu *skynetTUSUploader) NewUpload(ctx context.Context, info handler.FileInfo) (handler.Upload, error) {
	stu.mu.Lock()
	defer stu.mu.Unlock()

	// Create the upload object.
	info.ID = persist.UID()
	upload := &skynetTUSUpload{
		fi:             info,
		lastWrite:      time.Now(),
		staticUploader: stu,
	}
	stu.uploads[info.ID] = upload

	// Get a siapath.
	sp := skymodules.RandomSkynetFilePath()
	upload.fi.MetaData["SiaPath"] = sp.String()

	// Create the skyfile upload params.
	// TODO: use info.metadata to create skyfileuploadparameters different from
	// the default.
	upload.staticSUP = skymodules.SkyfileUploadParameters{
		SiaPath:             sp,
		Filename:            sp.Name(),
		BaseChunkRedundancy: SkyfileDefaultBaseChunkRedundancy,
	}

	// Create the FileUploadParams
	extendedSP, err := skymodules.NewSiaPath(sp.String() + skymodules.ExtendedSuffix)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create SiaPath for large skyfile extended data")
	}
	upload.staticUP, err = fileUploadParams(extendedSP, skymodules.RenterDefaultDataPieces, skymodules.RenterDefaultParityPieces, upload.staticUP.Force, crypto.TypePlain)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create FileUploadParams for large file")
	}

	// Generate a Cipher Key for the FileUploadParams.
	err = generateCipherKey(&upload.staticUP, upload.staticSUP)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create Cipher key for FileUploadParams")
	}

	// Check the upload params first and create a fileNode.
	upload.staticFN, err = stu.staticRenter.managedInitUploadStream(upload.staticUP)
	if err != nil {
		return nil, err
	}
	return upload, nil
}

// GetUpload returns an existing upload.
func (stu *skynetTUSUploader) GetUpload(ctx context.Context, id string) (handler.Upload, error) {
	stu.mu.Lock()
	defer stu.mu.Unlock()
	upload, exists := stu.uploads[id]
	if !exists {
		return nil, os.ErrNotExist
	}
	return upload, nil
}

// PruneUploads removes uploads that have been idle for too long.
func (stu *skynetTUSUploader) PruneUploads() {
	stu.mu.Lock()
	var toDelete []skymodules.SiaPath
	for id, upload := range stu.uploads {
		upload.mu.Lock()
		lastWrite := upload.lastWrite
		complete := upload.complete
		upload.mu.Unlock()
		if time.Since(lastWrite) < PruneTUSUploadTimeout {
			continue // nothing to do
		}
		// Prune
		_ = upload.Close()
		delete(stu.uploads, id)

		// If the upload wasn't completed, delete the files on disk.
		if !complete {
			toDelete = append(toDelete, upload.staticSUP.SiaPath)
			toDelete = append(toDelete, upload.staticUP.SiaPath)
		}
	}
	stu.mu.Unlock()

	// Delete files outside of lock.
	for _, sp := range toDelete {
		_ = stu.staticRenter.DeleteFile(sp)
	}
}

// Close closes the upload and underlying filenode.
func (u *skynetTUSUpload) Close() error {
	return u.managedClose()
}

// WriteChunk writes the chunk to the provided offset.
func (u *skynetTUSUpload) WriteChunk(ctx context.Context, offset int64, src io.Reader) (int64, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	uploader := u.staticUploader
	fileNode := u.staticFN
	ec := fileNode.ErasureCode()

	// Sanity check offset.
	// NOTE: If the offset is not chunk aligned, it means that a previous call
	// to WriteChunk read an incomplete chunk from src and padded it. After
	// uploading a padded chunk, we can't upload more chunks. That's why the
	// client needs to make sure that the chunkSize they use is aligned with the
	// chunkSize of the skyfile's fanout.
	if offset%int64(fileNode.ChunkSize()) != 0 {
		err := fmt.Errorf("offset is not chunk aligned - make sure chunkSize is set to a multiple of %v for these upload params", fileNode.ChunkSize())
		if build.Release == "testing" {
			// In test builds we want to be aware of this.
			build.Critical(err)
		}
		return 0, err
	}

	// Simulate unstable connection.
	if u.staticUploader.staticRenter.staticDeps.Disrupt("TUSUnstable") {
		// 50% chance that write fails
		if fastrand.Intn(2) == 0 {
			return 0, errors.New("TUSUnstable")
		}
	}

	// Upload.
	onlyOnePieceNeeded := ec.MinPieces() == 1 && fileNode.MasterKey().Type() == crypto.TypePlain
	cr := NewFanoutChunkReader(src, ec, onlyOnePieceNeeded, fileNode.MasterKey())
	n, err := uploader.staticRenter.callUploadStreamFromReaderWithFileNode(fileNode, cr, offset)

	// Increment offset and append fanout.
	u.fi.Offset += n
	u.fanout = append(u.fanout, cr.Fanout()...)

	// Update the lastWrite time if more than 0 bytes were written.
	if n > 0 {
		u.lastWrite = time.Now()
	}
	return n, err
}

// GetInfo returns the file info.
func (u *skynetTUSUpload) GetInfo(ctx context.Context) (handler.FileInfo, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.fi, nil
}

// GetReader returns a reader for the upload.
// NOTE: This is part of the core upload interface but doesn't seem to be
// required for uploads to work. It is not necessary for this to work on
// incomplete uploads and it's recommended to implement this for completed
// uploads.
func (u *skynetTUSUpload) GetReader(ctx context.Context) (io.Reader, error) {
	return bytes.NewReader([]byte{}), handler.ErrNotImplemented
}

// FinishUpload is called when the upload is done.
func (u *skynetTUSUpload) FinishUpload(ctx context.Context) (err error) {
	// Close upload when done.
	defer func() {
		err = errors.Compose(err, u.Close())
	}()

	u.mu.Lock()
	defer u.mu.Unlock()

	// Create metadata.
	sup := u.staticSUP
	sm := skymodules.SkyfileMetadata{
		Filename:     sup.Filename,
		Length:       uint64(u.fi.Size),
		Mode:         sup.Mode,
		Monetization: sup.Monetization,
	}

	// Get fanout.
	fanout := u.fanout

	// Convert the new siafile we just uploaded into a skyfile using the
	// convert function.
	r := u.staticUploader.staticRenter
	skylink, err := r.managedCreateSkylinkFromFileNode(sup, sm, u.staticFN, fanout)
	if err != nil {
		return errors.AddContext(err, "FinishUpload: unable to create skylink from filenode")
	}

	// Set the skylink on the metadata.
	u.fi.MetaData["Skylink"] = skylink.String()

	// Mark it as complete.
	u.complete = true
	return nil
}

// managedClose closes the upload and underlying filenode.
func (u *skynetTUSUpload) managedClose() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return nil
	}
	u.closed = true
	return u.staticFN.Close()
}

// threadedPruneTUSUploads periodically cleans up the uploads launched by the
// TUS endpoints.
func (r *Renter) threadedPruneTUSUploads() {
	ticker := time.NewTicker(PruneTUSUploadInterval)
	for {
		select {
		case <-r.tg.StopChan():
			return // shutdown
		case <-ticker.C:
		}
		r.staticSkynetTUSUploader.PruneUploads()
	}
}
