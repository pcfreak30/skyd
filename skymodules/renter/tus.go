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
		staticUploads skynetTUSUploadStore

		staticRenter *Renter
		mu           sync.Mutex
	}

	// skynetTUSUpload implements multiple TUS interfaces for uploads.
	skynetTUSUpload struct {
		fi             handler.FileInfo
		lastWrite      time.Time
		closed         bool
		complete       bool
		sm             skymodules.SkyfileMetadata
		sl             skymodules.Skylink
		staticUploader *skynetTUSUploader
		staticSUP      skymodules.SkyfileUploadParameters

		// large upload related fields.
		fanout   []byte
		fileNode *filesystem.FileNode
		staticUP skymodules.FileUploadParams

		// small upload related fields.
		isSmall bool
		smBytes []byte
		buf     []byte

		mu sync.Mutex
	}
)

// newSkynetTUSUploader creates a new uploader.
func newSkynetTUSUploader(renter *Renter) *skynetTUSUploader {
	return &skynetTUSUploader{
		staticUploads: newSkynetTUSInMemoryUploadStore(renter),
		staticRenter:  renter,
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
	return stu.staticUploads.NewLock(id)
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

	// Get a siapath.
	sp := skymodules.RandomSkynetFilePath()

	// Get the filename from either the metadata or path.
	fileName := sp.Name()
	fileNameMD, fileNameFound := upload.fi.MetaData["filename"]
	if fileNameFound {
		fileName = fileNameMD
	}
	fileType := upload.fi.MetaData["filetype"]

	// Create the skyfile upload params.
	// TODO: use info.metadata to create skyfileuploadparameters different from
	// the default.
	upload.staticSUP = skymodules.SkyfileUploadParameters{
		SiaPath:             sp,
		Filename:            fileName,
		BaseChunkRedundancy: SkyfileDefaultBaseChunkRedundancy,
	}
	sup := upload.staticSUP

	// Create metadata.
	upload.sm = skymodules.SkyfileMetadata{
		Filename:     sup.Filename,
		Mode:         sup.Mode,
		Monetization: sup.Monetization,
		Subfiles: skymodules.SkyfileSubfiles{
			sup.Filename: skymodules.SkyfileSubfileMetadata{
				Filename:    fileName,
				ContentType: fileType,
				Offset:      0,
			},
		},
	}

	// Create the FileUploadParams
	extendedSP, err := sp.AddSuffixStr(skymodules.ExtendedSuffix)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create SiaPath for large skyfile extended data")
	}
	upload.staticUP, err = fileUploadParams(extendedSP, skymodules.RenterDefaultDataPieces, skymodules.RenterDefaultParityPieces, upload.staticUP.Force, crypto.TypePlain)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create FileUploadParams for large file")
	}

	// Set the upload params to 'force' to allow overwriting the fileNode.
	upload.staticUP.Force = true

	// Generate a Cipher Key for the FileUploadParams.
	err = generateCipherKey(&upload.staticUP, upload.staticSUP)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create Cipher key for FileUploadParams")
	}

	// Add the upload to the map of uploads.
	err = stu.staticUploads.SaveUpload(upload)
	if err != nil {
		return nil, errors.AddContext(err, "failed to save new upload")
	}
	return upload, nil
}

// GetUpload returns an existing upload.
func (stu *skynetTUSUploader) GetUpload(ctx context.Context, id string) (handler.Upload, error) {
	return stu.staticUploads.Upload(id)
}

// Skylink returns the skylink for the upload with the given ID.
func (stu *skynetTUSUploader) Skylink(id string) (skymodules.Skylink, bool) {
	upload, err := stu.staticUploads.Upload(id)
	if err != nil {
		return skymodules.Skylink{}, false
	}
	upload.mu.Lock()
	defer upload.mu.Unlock()
	_, exists := upload.fi.MetaData["Skylink"]
	return upload.sl, exists
}

// Close closes the upload and underlying filenode.
func (u *skynetTUSUpload) Close() error {
	return u.managedClose()
}

// tryUploadSmallFile checks if the file to upload and its metadata fit within a
// single sector. It returns true or false depending on whether the file is
// small and any buffered data.
func (u *skynetTUSUpload) tryUploadSmallFile(reader io.Reader) ([]byte, bool, error) {
	// For files where we know the size ahead of time, we can save time by
	// checking against the specified size first.
	if u.fi.Size > int64(modules.SectorSize) {
		return nil, false, nil
	}

	// see if we can fit the entire upload in a single chunk
	buf := make([]byte, modules.SectorSize)
	numBytes, err := io.ReadFull(reader, buf)
	buf = buf[:numBytes] // truncate the buffer

	maybeSmall := errors.Contains(err, io.EOF) || errors.Contains(err, io.ErrUnexpectedEOF)
	if !maybeSmall {
		return buf, false, err
	}

	// prepare the metadata.
	sm := u.sm
	sm.Length = uint64(numBytes)
	ssm := sm.Subfiles[sm.Filename]
	ssm.Len = sm.Length
	sm.Subfiles[sm.Filename] = ssm

	// check whether it's valid
	err = skymodules.ValidateSkyfileMetadata(sm)
	if err != nil {
		return nil, false, errors.AddContext(err, "invalid metadata")
	}
	// marshal the skyfile metadata into bytes
	smBytes, err := skymodules.SkyfileMetadataBytes(sm)
	if err != nil {
		return nil, false, errors.AddContext(err, "failed to marshal skyfile metadata")
	}

	// verify if it fits in a single chunk
	headerSize := uint64(skymodules.SkyfileLayoutSize + len(smBytes))
	if uint64(numBytes)+headerSize > modules.SectorSize {
		return buf, false, nil
	}

	// small upload detected. Remember the necessary information to upload the
	// base sector later.
	u.isSmall = true
	u.smBytes = smBytes
	u.buf = buf
	return buf, true, nil
}

// WriteChunk writes the chunk to the provided offset.
func (u *skynetTUSUpload) WriteChunk(ctx context.Context, offset int64, src io.Reader) (n int64, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	uploader := u.staticUploader

	// Update the lastWrite time if more than 0 bytes were written.
	defer func() {
		if n > 0 {
			u.lastWrite = time.Now()
		}
	}()

	// If the offset is 0, we try to determine if the upload is large or small.
	if offset == 0 {
		buf, smallFile, err := u.tryUploadSmallFile(src)
		if err != nil {
			return 0, err
		}
		// If it is a small file we are done. Unless it's a partial
		// upload. In that case we still upload it the same way as a
		// large upload to receive a fanout.
		n = int64(len(buf))
		if smallFile && !u.fi.IsPartial {
			u.fi.Offset += n
			return n, nil
		}
		// If not, prepend the src with the buffer and initialize the upload
		// stream.
		src = io.MultiReader(bytes.NewReader(buf), src)
		u.fileNode, err = uploader.staticRenter.managedInitUploadStream(u.staticUP)
		if err != nil {
			return 0, err
		}
	}
	// If we get to this point with a small file, something is wrong.
	// Theoretically this is not possible but return an error for extra safety.
	if u.isSmall && !u.fi.IsPartial {
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
	if n%int64(fileNode.ChunkSize()) != 0 && u.fi.Offset+n != u.fi.Size {
		shrinkErr := fileNode.Shrink(uint64(u.fi.Offset) / fileNode.ChunkSize())
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

	// Increment offset and append fanout.
	u.fi.Offset += n
	u.fanout = append(u.fanout, cr.Fanout()...)
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

// finishUploadLarge handles finishing up a large upload.
func (u *skynetTUSUpload) finishUploadLarge(ctx context.Context, masterKey crypto.CipherKey, ec skymodules.ErasureCoder) (skylink skymodules.Skylink, err error) {
	// Finish metadata and check its validity.
	r := u.staticUploader.staticRenter
	sup := u.staticSUP
	sm := u.sm
	sm.Length = uint64(u.fi.Size)
	ssm := sm.Subfiles[sm.Filename]
	ssm.Len = sm.Length
	sm.Subfiles[sm.Filename] = ssm
	err = skymodules.ValidateSkyfileMetadata(sm)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "metadata is invalid")
	}

	// Convert the new siafile we just uploaded into a skyfile using the
	// convert function.
	return r.managedCreateSkylink(ctx, sup, sm, u.fanout, uint64(u.fi.Size), masterKey, ec)
}

// finishUploadSmall handles finishing up a small upload.
func (u *skynetTUSUpload) finishUploadSmall(ctx context.Context) (skylink skymodules.Skylink, err error) {
	r := u.staticUploader.staticRenter
	sup := u.staticSUP
	// edge case 0 byte file
	if u.fi.Size == 0 {
		u.smBytes, err = skymodules.SkyfileMetadataBytes(u.sm)
		if err != nil {
			return
		}
	}
	return r.managedUploadSkyfileSmallFile(ctx, sup, u.smBytes, u.buf)
}

// FinishUpload is called when the upload is done.
func (u *skynetTUSUpload) FinishUpload(ctx context.Context) (err error) {
	// Close upload when done.
	defer func() {
		err = errors.Compose(err, u.Close())
	}()

	u.mu.Lock()

	// Update the last write before starting to wait for the chunks to avoid
	// having the chunk pruned. This mostly happens in testing but won't
	// hurt now that we no longer wait for every chunk to become available
	// right away.
	u.lastWrite = time.Now()

	// If the upload is a partial upload we are done. We don't need to
	// upload the metadata or create a skylink.
	if u.fi.IsPartial {
		u.mu.Unlock()
		return nil
	}
	u.mu.Unlock()

	u.mu.Lock()
	defer u.mu.Unlock()

	var skylink skymodules.Skylink
	if u.isSmall || u.fi.Size == 0 {
		skylink, err = u.finishUploadSmall(ctx)
	} else {
		skylink, err = u.finishUploadLarge(ctx, u.fileNode.MasterKey(), u.fileNode.ErasureCode())
	}
	if err != nil {
		return errors.AddContext(err, "failed to finish upload")
	}

	// Set the skylink on the metadata.
	u.sl = skylink
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
		r.staticSkynetTUSUploader.staticUploads.Prune()
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
// the provded partial uploads into a single one.
func (u *skynetTUSUpload) ConcatUploads(ctx context.Context, partialUploads []handler.Upload) error {
	// Concatenate the uploads by combining their fanouts. Concatenated
	// uploads may never consist of small uploads except for the last
	// upload.
	sup := partialUploads[0].(*skynetTUSUpload).staticSUP
	ec := partialUploads[0].(*skynetTUSUpload).fileNode.ErasureCode()
	masterKey := partialUploads[0].(*skynetTUSUpload).fileNode.MasterKey()
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
	skylink, err := u.finishUploadLarge(ctx, masterKey, ec)
	if err != nil {
		return err
	}

	// Set the skylink in the metadata.
	u.sl = skylink
	u.fi.MetaData["Skylink"] = skylink.String()

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
	return nil
}
