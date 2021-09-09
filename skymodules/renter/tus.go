package renter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/tus/tusd/pkg/handler"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/persist"
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

// ErrTUSUploadInterrupted is returned if the upload seemingly succeeded but
// didn't actually upload a full chunk.
var ErrTUSUploadInterrupted = errors.New("tus upload was interrupted - please retry")

type (
	// skynetTUSUploader implements multiple TUS interfaces for skynet uploads
	// allowing for resumable uploads.
	skynetTUSUploader struct {
		staticUploadStore skymodules.SkynetTUSUploadStore

		ongoingUploads map[string]*skynetTUSUpload

		staticRenter *Renter
		mu           sync.Mutex
	}

	// skynetTUSUpload implements multiple TUS interfaces for uploads.
	skynetTUSUpload struct {
		staticUpload   skymodules.SkynetTUSUpload
		staticUploader *skynetTUSUploader
		closed         bool
		sl             skymodules.Skylink

		// large upload related fields.
		fanout   []byte
		fileNode *filesystem.FileNode

		// metadata
		isSmall         bool
		smallUploadData []byte
		smBytes         []byte

		staticIsPartial bool

		mu sync.Mutex
	}
)

// newSkynetTUSUploader creates a new uploader.
func newSkynetTUSUploader(renter *Renter, tus skymodules.SkynetTUSUploadStore) *skynetTUSUploader {
	return &skynetTUSUploader{
		staticUploadStore: tus,
		staticRenter:      renter,
	}
}

// SkynetTUSUploader returns the renter's uploader for registering in the API.
func (r *Renter) SkynetTUSUploader() skymodules.SkynetTUSDataStore {
	return r.staticSkynetTUSUploader
}

// AsConcatableUpload implements the ConcaterDataStore interface.
func (stu *skynetTUSUploader) AsConcatableUpload(upload handler.Upload) handler.ConcatableUpload {
	return upload.(*skynetTUSUpload)
}

// NewLock implements the handler.Locker interface by passing on the call to the
// upload storage backend.
func (stu *skynetTUSUploader) NewLock(id string) (handler.Lock, error) {
	return stu.staticUploadStore.NewLock(id)
}

// NewUpload creates a new upload from fileinfo.
func (stu *skynetTUSUploader) NewUpload(ctx context.Context, info handler.FileInfo) (handler.Upload, error) {
	stu.mu.Lock()
	defer stu.mu.Unlock()

	// Create the upload object.
	info.ID = persist.UID()

	// Get a siapath.
	sp := skymodules.RandomSkynetFilePath()

	// Get the filename from either the metadata or path.
	fileName := sp.Name()
	fileNameMD, fileNameFound := info.MetaData["filename"]
	if fileNameFound {
		fileName = fileNameMD
	}
	fileType := info.MetaData["filetype"]

	// Create the skyfile upload params.
	sup := skymodules.SkyfileUploadParameters{
		SiaPath:             sp,
		Filename:            fileName,
		BaseChunkRedundancy: SkyfileDefaultBaseChunkRedundancy,
	}

	// Create metadata.
	sm := skymodules.SkyfileMetadata{
		Filename:     sup.Filename,
		Length:       uint64(info.Size),
		Mode:         sup.Mode,
		Monetization: sup.Monetization,
		Subfiles: skymodules.SkyfileSubfiles{
			sup.Filename: skymodules.SkyfileSubfileMetadata{
				Filename:    fileName,
				ContentType: fileType,
				Len:         uint64(info.Size),
				Offset:      0,
			},
		},
	}

	// check whether it's valid
	err := skymodules.ValidateSkyfileMetadata(sm)
	if err != nil {
		return nil, errors.AddContext(err, "invalid metadata")
	}

	smBytes, err := skymodules.SkyfileMetadataBytes(sm)
	if err != nil {
		return nil, errors.AddContext(err, "failed to marshal skyfile metadata")
	}

	// Create the FileUploadParams
	extendedSP, err := sp.AddSuffixStr(skymodules.ExtendedSuffix)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create SiaPath for large skyfile extended data")
	}
	up, err := fileUploadParams(extendedSP, skymodules.RenterDefaultDataPieces, skymodules.RenterDefaultParityPieces, sup.Force, crypto.TypePlain)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create FileUploadParams for large file")
	}

	// Set the upload params to 'force' to allow overwriting the fileNode.
	sup.Force = true

	// Generate a Cipher Key for the FileUploadParams.
	err = generateCipherKey(&up, sup)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create Cipher key for FileUploadParams")
	}

	// Add the upload to the map of uploads.
	upload, err := stu.createUpload(info, sup, up, smBytes)
	if err != nil {
		return nil, errors.AddContext(err, "failed to save new upload")
	}
	return upload, nil
}

func (stu *skynetTUSUploader) createUpload(fi handler.FileInfo, sup skymodules.SkyfileUploadParameters, up skymodules.FileUploadParams, sm []byte) (*skynetTUSUpload, error) {
	panic("not implemented yet")
}

// GetUpload returns an existing upload.
func (stu *skynetTUSUploader) GetUpload(ctx context.Context, id string) (handler.Upload, error) {
	// Search for ongoing upload.
	stu.mu.Lock()
	upload, exists := stu.ongoingUploads[id]
	if exists {
		stu.mu.Unlock()
		return upload, nil
	}

	// If it doesn't exist create one.
	_, err := stu.staticUploadStore.GetUpload(ctx, id)
	if err != nil {
		return nil, err
	}
	panic("not implemented")
}

// Skylink returns the skylink for the upload with the given ID.
func (stu *skynetTUSUploader) Skylink(id string) (skymodules.Skylink, bool) {
	u, err := stu.staticUploadStore.GetUpload(stu.staticRenter.tg.StopCtx(), id)
	if err != nil {
		return skymodules.Skylink{}, false
	}
	return u.Skylink()
}

// Close closes the upload and underlying filenode.
func (u *skynetTUSUpload) Close() error {
	return u.managedClose()
}

// tryUploadSmallFile checks if the file to upload and its metadata fit within a
// single sector. It returns true or false depending on whether the file is
// small and any buffered data.
func (u *skynetTUSUpload) tryUploadSmallFile(reader io.Reader) (bool, error) {
	// Get skyfile metadata.
	var smBytes []byte
	var fi handler.FileInfo
	if true {
		panic("not implemented")
	}

	// verify if it fits in a single chunk
	headerSize := uint64(skymodules.SkyfileLayoutSize + len(smBytes))
	if uint64(fi.Size)+headerSize > modules.SectorSize {
		return false, nil
	}

	// see if we can fit the entire upload in a single chunk
	buf := make([]byte, fi.Size)
	numBytes, err := io.ReadFull(reader, buf)
	buf = buf[:numBytes] // truncate the buffer

	maybeSmall := errors.Contains(err, io.EOF) || errors.Contains(err, io.ErrUnexpectedEOF)
	if !maybeSmall {
		return false, err
	}

	// small upload detected. Remember the necessary information to upload the
	// base sector later.
	u.isSmall = true
	u.smallUploadData = buf
	return true, nil
}

// WriteChunk writes the chunk to the provided offset.
func (u *skynetTUSUpload) WriteChunk(ctx context.Context, offset int64, src io.Reader) (n int64, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	uploader := u.staticUploader

	// Get fileinfo from upload.
	fi, err := u.staticUpload.GetInfo(ctx)
	if err != nil {
		return 0, errors.AddContext(err, "failed to get fileinfo")
	}

	// If the offset is 0, we try to determine if the upload is large or small.
	if offset == 0 {
		smallFile, err := u.tryUploadSmallFile(src)
		if err != nil {
			return 0, err
		}
		// If it is a small file we are done. Unless it's a partial
		// upload. In that case we still upload it the same way as a
		// large upload to receive a fanout.
		if smallFile && !fi.IsPartial {
			err = u.staticUpload.CommitWriteChunk(fi.Size, time.Now(), nil)
			return n, err
		}
		// If not, prepend the src with the buffer and initialize the upload
		// stream.
		u.fileNode, err = uploader.staticRenter.managedInitUploadStream(u.staticUP)
		if err != nil {
			return 0, err
		}
	}
	// If we get to this point with a small file, something is wrong.
	// Theoretically this is not possible but return an error for extra safety.
	if u.isSmall && !fi.IsPartial {
		return 0, errors.New("can't upload another chunk to a small file upload")
	}

	// Upload is a large upload.
	fileNode := u.fileNode
	ec := fileNode.ErasureCode()

	// Sanity check offset.
	// NOTE: If the offset is not chunk aligned, it means that a previous call
	// to WriteChunk read an incomplete chunk from src and padded it. After
	// uploading a padded chunk, we can't upload more chunks. That's why the
	// client needs to make sure that the chunkSize they use is aligned with the
	// chunkSize of the skyfile's fanout.
	deps := u.staticUploader.staticRenter.staticDeps
	if offset%int64(fileNode.ChunkSize()) != 0 {
		err := fmt.Errorf("offset is not chunk aligned - make sure chunkSize is set to a multiple of %v for these upload params", fileNode.ChunkSize())
		if build.Release == "testing" {
			// In test builds we want to be aware of this.
			build.Critical(err)
		}
		return 0, err
	}

	// Simulate unstable connection.
	if deps.Disrupt("TUSUnstable") {
		// 50% chance that write fails
		if fastrand.Intn(2) == 0 {
			return 0, errors.New("TUSUnstable")
		}
	}

	// Upload.
	onlyOnePieceNeeded := ec.MinPieces() == 1 && fileNode.MasterKey().Type() == crypto.TypePlain
	cr := NewFanoutChunkReader(src, ec, onlyOnePieceNeeded, fileNode.MasterKey())
	var chunks []*unfinishedUploadChunk
	chunks, n, err = uploader.staticRenter.callUploadStreamFromReaderWithFileNodeNoBlock(ctx, fileNode, cr, offset)

	// Simulate loss of connection one byte early.
	if deps.Disrupt("TUSConnectionDropped") {
		n--
		err = nil
	}

	// If less than a full chunk was uploaded, we expect the file to be done. If
	// that's not the case, the connection was closed early. That means the chunk
	// was incorrectly padded and needs to be removed again by shrinking the siafile
	// by one chunk before the user can retry the upload.
	if n%int64(fileNode.ChunkSize()) != 0 && fi.Offset+n != fi.Size {
		shrinkErr := fileNode.Shrink(uint64(fi.Offset) / fileNode.ChunkSize())
		if shrinkErr != nil {
			return 0, shrinkErr
		}
		// Make sure that we return an error if none was returned by the
		// upload. That way the client will know to retry. This usually
		// happens if we reach a timeout in the reverse proxy.
		err = errors.Compose(err, ErrTUSUploadInterrupted)
	}
	// In case of any error, return early.
	if err != nil {
		return 0, err
	}

	// Wait for chunks to become available.
	for _, chunk := range chunks {
		select {
		case <-ctx.Done():
			return 0, errors.New("reached timeout before chunks became available")
		case <-chunk.staticAvailableChan:
		}
		if chunk.err != nil {
			return 0, errors.AddContext(err, "failed to upload chunk")
		}
	}
	return n, u.staticUpload.CommitWriteChunk(fi.Offset+n, time.Now(), cr.Fanout())
}

// GetInfo returns the file info.
func (u *skynetTUSUpload) GetInfo(ctx context.Context) (handler.FileInfo, error) {
	return u.staticUpload.GetInfo(ctx)
}

// GetReader returns a reader for the upload.
// NOTE: This is part of the core upload interface but doesn't seem to be
// required for uploads to work. It is not necessary for this to work on
// incomplete uploads and it's recommended to implement this for completed
// uploads.
func (u *skynetTUSUpload) GetReader(ctx context.Context) (io.Reader, error) {
	return bytes.NewReader([]byte{}), handler.ErrNotImplemented
}

// finishUploadLarge handles finishing up a large upload.
func (u *skynetTUSUpload) finishUploadLarge(ctx context.Context, fi handler.FileInfo, masterKey crypto.CipherKey, ec skymodules.ErasureCoder) (skylink skymodules.Skylink, err error) {
	// Convert the new siafile we just uploaded into a skyfile using the
	// convert function.
	r := u.staticUploader.staticRenter
	sup, _, err := u.staticUpload.UploadParams(ctx)
	if err != nil {
		return skymodules.Skylink{}, err
	}
	return r.managedCreateSkylinkRawMD(ctx, sup, u.smBytes, u.fanout, uint64(fi.Size), masterKey, ec)
}

// finishUploadSmall handles finishing up a small upload.
func (u *skynetTUSUpload) finishUploadSmall(ctx context.Context) (skylink skymodules.Skylink, err error) {
	r := u.staticUploader.staticRenter
	sup, _, err := u.staticUpload.UploadParams(ctx)
	if err != nil {
		return skymodules.Skylink{}, err
	}
	return r.managedUploadSkyfileSmallFile(ctx, sup, u.smBytes, u.smallUploadData)
}

// FinishUpload is called when the upload is done.
func (u *skynetTUSUpload) FinishUpload(ctx context.Context) (err error) {
	// Close upload when done.
	defer func() {
		err = errors.Compose(err, u.Close())
	}()

	u.mu.Lock()
	defer u.mu.Unlock()

	fi, err := u.staticUpload.GetInfo(ctx)
	if err != nil {
		return errors.AddContext(err, "failed to fetch fileinfo")
	}

	var skylink skymodules.Skylink
	if fi.IsPartial {
		// If the upload is a partial upload we are done. We don't need to
		// upload the metadata or create a skylink.
		return nil
	} else if u.isSmall || fi.Size == 0 {
		skylink, err = u.finishUploadSmall(ctx)
	} else {
		skylink, err = u.finishUploadLarge(ctx, fi, u.fileNode.MasterKey(), u.fileNode.ErasureCode())
	}
	if err != nil {
		return errors.AddContext(err, "failed to finish upload")
	}

	return u.staticUpload.CommitFinishUpload(skylink)
}

// managedClose closes the upload and underlying filenode.
func (u *skynetTUSUpload) managedClose() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return nil
	}
	u.closed = true
	// For large files we need to close the additional fileNode.
	if u.fileNode != nil {
		return u.fileNode.Close()
	}
	return nil
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
		toDelete, err := r.staticSkynetTUSUploader.staticUploadStore.ToPrune()
		if err != nil {
			r.staticLog.Print("Failed to get TUS uploads for pruning", err)
		}

		// Delete files.
		for _, upload := range toDelete {
			// Delete on disk.
			_ = r.DeleteFile(skymodules.SiaPath(upload.SiaPath()))

			// Delete from store.
			_ = r.staticSkynetTUSUploader.staticUploadStore.Prune(upload)
		}
	}
}

// TUSPreUploadCreateCallback is called before creating an upload. It is used to
// dynamically check the maximum size of the user's upload according to a set
// header field.
func TUSPreUploadCreateCallback(hook handler.HookEvent) error {
	// Sanity check that the size is not deferred.
	if hook.Upload.SizeIsDeferred {
		err := errors.New("uploads with deferred size are not supported")
		return handler.NewHTTPError(err, http.StatusBadRequest)
	}
	// Get user's max upload size from request.
	maxSizeStr := hook.HTTPRequest.Header.Get("SkynetMaxUploadSize")
	if maxSizeStr == "" {
		err := errors.New("SkynetMaxUploadSize header is missing")
		return handler.NewHTTPError(err, http.StatusBadRequest)
	}
	var maxSize int64
	_, err := fmt.Sscan(maxSizeStr, &maxSize)
	if err != nil {
		err = errors.AddContext(err, "failed to parse SkynetMaxUploadSize")
		return handler.NewHTTPError(err, http.StatusBadRequest)
	}
	// Check upload size against max size.
	if hook.Upload.Size > maxSize {
		err = fmt.Errorf("upload exceeds maximum size: %v > %v", hook.Upload.Size, maxSize)
		return handler.NewHTTPError(err, http.StatusRequestEntityTooLarge)
	}
	return nil
}

// ConcatUploads implements the handler.ConcatableUpload interface. It combines
// the provided partial uploads into a single one.
func (u *skynetTUSUpload) ConcatUploads(ctx context.Context, partialUploads []handler.Upload) error {
	// Get fileinfo.
	fi, err := u.GetInfo(ctx)
	if err != nil {
		return errors.AddContext(err, "failed to fetch fileinfo")
	}

	// Concatenate the uploads by combining their fanouts. Concatenated
	// uploads may never consist of small uploads except for the last
	// upload.
	pu := partialUploads[0].(*skynetTUSUpload)
	sup := pu.staticSUP
	ec := pu.fileNode.ErasureCode()
	masterKey := pu.fileNode.MasterKey()
	for i := range partialUploads {
		pu := partialUploads[i].(*skynetTUSUpload)
		if i < len(partialUploads)-1 && pu.isSmall {
			return errors.New("only last upload is allowed to be small")
		}
		if pu.fileNode.ErasureCode().Identifier() != ec.Identifier() {
			return errors.New("all partial uploads need to use the same erasure coding")
		}
		if pu.fileNode.MasterKey().Type() != masterKey.Type() {
			return errors.New("all masterkeys need to have the same type")
		}
		if !bytes.Equal(pu.fileNode.MasterKey().Key(), masterKey.Key()) {
			return errors.New("all masterkeys need to be the same")
		}
		u.fanout = append(u.fanout, pu.fanout...)
	}
	u.staticSUP = sup
	skylink, err := u.finishUploadLarge(ctx, fi, masterKey, ec)
	if err != nil {
		return err
	}

	// Associate all partial upload siafiles with this skylink.
	// TODO: Once we have the ability to upload without siafiles, we should
	// create a single siafile here and associate it with the skylink.
	for i := range partialUploads {
		pu := partialUploads[i].(*skynetTUSUpload)
		err = pu.fileNode.AddSkylink(skylink)
		if err != nil {
			return errors.AddContext(err, "failed to add skylink to partial uploads")
		}
	}

	// Upon success, we mark the partial uploads as complete to prevent them
	// from being pruned.
	for i := range partialUploads {
		_ = partialUploads[i].(*skynetTUSUpload)
		panic("set partial upload to complete to avoid pruning")
	}
	return u.staticUpload.CommitFinishUpload(skylink)
}
