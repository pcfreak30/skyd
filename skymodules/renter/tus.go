package renter

import (
	"bytes"
	"context"
	"encoding/json"
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
		Standard: 24 * time.Hour, // 24 hours to give plenty of time to finish an upload
		Testing:  10 * time.Second,
	}).(time.Duration)

	// PruneTUSUploadInterval is the time that passes between pruning attempts.
	// The smaller the interval, the smaller the batches of uploads we prune at
	// a time.
	PruneTUSUploadInterval = build.Select(build.Var{
		Dev:      time.Minute,
		Standard: time.Hour,
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

		ongoingUploads map[string]*ongoingTUSUpload
		staticRenter   *Renter
		mu             sync.Mutex
	}

	// ongoingTUSUpload implements multiple TUS interfaces for uploads.
	ongoingTUSUpload struct {
		staticUpload   skymodules.SkynetTUSUpload
		staticUploader *skynetTUSUploader

		fileNode *filesystem.FileNode
		closed   bool
		mu       sync.Mutex
	}
)

// newSkynetTUSUploader creates a new uploader.
func newSkynetTUSUploader(renter *Renter, tus skymodules.SkynetTUSUploadStore) *skynetTUSUploader {
	return &skynetTUSUploader{
		staticUploadStore: tus,
		staticRenter:      renter,
		ongoingUploads:    make(map[string]*ongoingTUSUpload),
	}
}

// SkynetTUSUploader returns the renter's uploader for registering in the API.
func (r *Renter) SkynetTUSUploader() skymodules.SkynetTUSDataStore {
	return r.staticSkynetTUSUploader
}

// Close closes the underlying upload store.
func (stu *skynetTUSUploader) Close() error {
	return stu.staticUploadStore.Close()
}

// AsConcatableUpload implements the ConcaterDataStore interface.
func (stu *skynetTUSUploader) AsConcatableUpload(upload handler.Upload) handler.ConcatableUpload {
	return upload.(*ongoingTUSUpload)
}

// NewLock implements the handler.Locker interface by passing on the call to the
// upload storage backend.
func (stu *skynetTUSUploader) NewLock(id string) (handler.Lock, error) {
	return stu.staticUploadStore.NewLock(id)
}

// NewUpload creates a new upload from fileinfo.
func (stu *skynetTUSUploader) NewUpload(ctx context.Context, info handler.FileInfo) (handler.Upload, error) {
	fmt.Println("NewUpload start")
	defer fmt.Println("NewUpload end")
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
		Filename: sup.Filename,
		Length:   uint64(info.Size),
		Mode:     sup.Mode,
		Subfiles: skymodules.SkyfileSubfiles{
			sup.Filename: skymodules.SkyfileSubfileMetadata{
				Filename:    fileName,
				ContentType: fileType,
				Len:         uint64(info.Size),
				Offset:      0,
			},
		},
	}

	// Check whether it's valid
	err := skymodules.ValidateSkyfileMetadata(sm)
	if err != nil {
		return nil, errors.AddContext(err, "invalid metadata")
	}

	// Set the upload params to 'force' to allow overwriting the fileNode.
	sup.Force = true

	// Create the upload.
	upload, err := stu.managedCreateUpload(info, sp, fileName, SkyfileDefaultBaseChunkRedundancy, skymodules.RenterDefaultDataPieces, skymodules.RenterDefaultParityPieces, sm, crypto.TypePlain)
	if err != nil {
		return nil, errors.AddContext(err, "failed to save new upload")
	}
	return upload, nil
}

// managedCreateUpload creates a new ongoing upload.
func (stu *skynetTUSUploader) managedCreateUpload(fi handler.FileInfo, sp skymodules.SiaPath, fileName string, baseChunkRedundancy uint8, fanoutDataPieces, fanoutParityPieces int, sm skymodules.SkyfileMetadata, ct crypto.CipherType) (*ongoingTUSUpload, error) {
	smBytes, err := json.Marshal(sm)
	if err != nil {
		return nil, err
	}
	ctx := stu.staticRenter.tg.StopCtx()
	upload, err := stu.staticUploadStore.CreateUpload(ctx, fi, sp, fileName, baseChunkRedundancy, fanoutDataPieces, fanoutParityPieces, smBytes, ct)
	if err != nil {
		return nil, errors.AddContext(err, "upload store failed to create new upload")
	}
	var smCheck skymodules.SkyfileMetadata
	err = json.Unmarshal(smBytes, &smCheck)
	if err != nil {
		fmt.Println("ERR2", err)
	}
	stu.mu.Lock()
	defer stu.mu.Unlock()
	ou := &ongoingTUSUpload{
		staticUpload:   upload,
		staticUploader: stu,
	}
	stu.ongoingUploads[fi.ID] = ou
	return ou, nil
}

// GetUpload returns an existing upload.
func (stu *skynetTUSUploader) GetUpload(ctx context.Context, id string) (handler.Upload, error) {
	// Get the upload from the db first.
	upload, err := stu.staticUploadStore.GetUpload(ctx, id)
	if err != nil {
		return nil, err
	}
	// Search for ongoing upload.
	stu.mu.Lock()
	defer stu.mu.Unlock()
	ou, exists := stu.ongoingUploads[id]
	if exists {
		// Only swap out the upload we just fetched.
		ou.staticUpload = upload
		return ou, nil
	}

	// If it doesn't exist create one.
	ou = &ongoingTUSUpload{
		staticUpload:   upload,
		staticUploader: stu,
	}
	stu.ongoingUploads[id] = ou
	return ou, nil
}

// Skylink returns the skylink for the upload with the given ID.
func (stu *skynetTUSUploader) Skylink(id string) (skymodules.Skylink, bool) {
	u, err := stu.staticUploadStore.GetUpload(stu.staticRenter.tg.StopCtx(), id)
	if err != nil {
		return skymodules.Skylink{}, false
	}
	return u.GetSkylink()
}

// Close closes the upload and underlying filenode.
func (u *ongoingTUSUpload) Close() error {
	return u.managedClose()
}

// tryUploadSmallFile checks if the file to upload and its metadata fit within a
// single sector. It returns true or false depending on whether the file is
// small and any buffered data.
func (u *ongoingTUSUpload) tryUploadSmallFile(ctx context.Context, reader io.Reader) ([]byte, bool, error) {
	// Get skyfile metadata.
	fi, err := u.staticUpload.GetInfo(ctx)
	if err != nil {
		return nil, false, err
	}
	smBytes, err := u.staticUpload.SkyfileMetadata(ctx)
	if err != nil {
		return nil, false, err
	}

	// Check if the file is indeed a small file.
	headerSize := uint64(skymodules.SkyfileLayoutSize + len(smBytes))
	if uint64(fi.Size)+headerSize > modules.SectorSize {
		return nil, false, nil
	}

	// Download the data and save it.
	buf := make([]byte, fi.Size)
	_, err = io.ReadFull(reader, buf)
	return buf, true, err
}

// WriteChunk writes the chunk to the provided offset.
func (u *ongoingTUSUpload) WriteChunk(ctx context.Context, offset int64, src io.Reader) (_ int64, err error) {
	fmt.Println("WriteChunk start")
	defer fmt.Println("WriteChunk stop")
	u.mu.Lock()
	defer u.mu.Unlock()
	uploader := u.staticUploader

	// Get fileinfo from upload.
	fi, err := u.staticUpload.GetInfo(ctx)
	if err != nil {
		return 0, errors.AddContext(err, "failed to get fileinfo")
	}

	// If the offset is 0, we try to determine if the upload is large or small.
	var isSmall bool
	if offset == 0 {
		var smallFileData []byte
		smallFileData, isSmall, err = u.tryUploadSmallFile(ctx, src)
		if err != nil {
			return 0, err
		}
		// If it is a small file we are done. Unless it's a partial
		// upload. In that case we still upload it the same way as a
		// large upload to receive a fanout.
		if isSmall && !fi.IsPartial {
			// Finish the small upload.
			sup, _, err := u.staticUpload.UploadParams(ctx)
			if err != nil {
				return 0, errors.AddContext(err, "failed to fetch upload params")
			}
			smBytes, err := u.staticUpload.SkyfileMetadata(ctx)
			if err != nil {
				return 0, errors.AddContext(err, "failed to fetch smBytes")
			}
			skylink, err := u.finishUploadSmall(ctx, sup, smBytes, smallFileData)
			if err != nil {
				return 0, err
			}
			err = u.staticUpload.CommitFinishUpload(ctx, skylink)
			if err != nil {
				return 0, errors.AddContext(err, "failed to finish small upload")
			}
			return int64(len(smallFileData)), nil
		}
		// Otherwise we add the data to a reader to be uploaded as a
		// large file.
		src = io.MultiReader(bytes.NewReader(smallFileData), src)
	}

	// If we get to this point with a small file, something is wrong.
	// Theoretically this is not possible but return an error for extra safety.
	if isSmall && !fi.IsPartial {
		return 0, errors.New("can't upload another chunk to a small file upload")
	}

	// Upload is a large upload - we need a filenode for the fanout. If it
	// doesn't exist yet, create it.
	if u.fileNode == nil {
		_, up, err := u.staticUpload.UploadParams(ctx)
		if err != nil {
			return 0, err
		}
		u.fileNode, err = uploader.staticRenter.managedInitUploadStream(up)
		if err != nil {
			return 0, err
		}
	}
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
	chunks, n, err := uploader.staticRenter.callUploadStreamFromReaderWithFileNodeNoBlock(ctx, fileNode, cr, offset)

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
	return n, u.staticUpload.CommitWriteChunk(ctx, fi.Offset+n, time.Now(), isSmall, cr.Fanout())
}

// GetInfo returns the file info.
func (u *ongoingTUSUpload) GetInfo(ctx context.Context) (handler.FileInfo, error) {
	return u.staticUpload.GetInfo(ctx)
}

// GetReader returns a reader for the upload.
// NOTE: This is part of the core upload interface but doesn't seem to be
// required for uploads to work. It is not necessary for this to work on
// incomplete uploads and it's recommended to implement this for completed
// uploads.
func (u *ongoingTUSUpload) GetReader(ctx context.Context) (io.Reader, error) {
	return bytes.NewReader([]byte{}), handler.ErrNotImplemented
}

// finishUploadLarge handles finishing up a large upload.
func (u *ongoingTUSUpload) finishUploadLarge(ctx context.Context, fanout []byte, sup skymodules.SkyfileUploadParameters, fi handler.FileInfo, masterKey crypto.CipherKey, ec skymodules.ErasureCoder, smBytes []byte) (skylink skymodules.Skylink, err error) {
	r := u.staticUploader.staticRenter
	return r.managedCreateSkylinkRawMD(ctx, sup, smBytes, fanout, uint64(fi.Size), masterKey, ec)
}

// finishUploadSmall handles finishing up a small upload.
func (u *ongoingTUSUpload) finishUploadSmall(ctx context.Context, sup skymodules.SkyfileUploadParameters, smBytes, smallUploadData []byte) (skylink skymodules.Skylink, err error) {
	r := u.staticUploader.staticRenter
	return r.managedUploadSkyfileSmallFile(ctx, sup, smBytes, smallUploadData)
}

// FinishUpload is called when the upload is done.
func (u *ongoingTUSUpload) FinishUpload(ctx context.Context) (err error) {
	var sm skymodules.SkyfileMetadata
	smBytes, _ := u.staticUpload.SkyfileMetadata(ctx)
	err = json.Unmarshal(smBytes, &sm)
	if err != nil {
		fmt.Println("ERR FINISH UPLOAD", err, len(smBytes))
	}
	// Close upload when done.
	defer func() {
		err = errors.Compose(err, u.Close())
	}()

	u.mu.Lock()
	defer u.mu.Unlock()

	// If the upload is a partial upload we are done. We don't need to
	// upload the metadata or create a skylink.
	fi, err := u.staticUpload.GetInfo(ctx)
	if err != nil {
		return errors.AddContext(err, "failed to fetch fileinfo")
	}
	if fi.IsPartial {
		return nil
	}
	// If the upload is a small file upload with >0 size we are done because
	// it was already finalised in WriteChunk.
	if u.fileNode == nil && fi.Size > 0 {
		return nil
	}

	// Finish the large or 0-byte upload.
	sup, _, err := u.staticUpload.UploadParams(ctx)
	if err != nil {
		return errors.AddContext(err, "failed to fetch upload params")
	}
	smBytes, err = u.staticUpload.SkyfileMetadata(ctx)
	if err != nil {
		return errors.AddContext(err, "failed to fetch smBytes")
	}
	fanout, err := u.staticUpload.Fanout(ctx)
	if err != nil {
		return errors.AddContext(err, "failed to fetch fanout")
	}
	fmt.Println("final fanout", len(fanout))

	err = json.Unmarshal(smBytes, &sm)
	if err != nil {
		fmt.Println("ERR", err, len(smBytes))
	}
	testUpload, err := u.staticUploader.staticUploadStore.GetUpload(ctx, fi.ID)
	if err != nil {
		fmt.Println("argh", err)
	}
	testUpload2 := testUpload.(*MongoTUSUpload)
	err = json.Unmarshal(testUpload2.Metadata, &sm)
	if err != nil {
		fmt.Println("ERR1.1", err, len(smBytes))
	}

	// If the upload is 0-byte, WriteChunk is skipped. So we need to finish
	// the upload here.
	var skylink skymodules.Skylink
	if fi.Size == 0 {
		skylink, err = u.finishUploadSmall(ctx, sup, smBytes, []byte{})
	} else {
		skylink, err = u.finishUploadLarge(ctx, fanout, sup, fi, u.fileNode.MasterKey(), u.fileNode.ErasureCode(), smBytes)
	}
	if err != nil {
		return errors.AddContext(err, "failed to finish upload")
	}
	return u.staticUpload.CommitFinishUpload(ctx, skylink)
}

// managedClose closes the upload and underlying filenode.
func (u *ongoingTUSUpload) managedClose() error {
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
		toDelete, err := r.staticSkynetTUSUploader.staticUploadStore.ToPrune(r.tg.StopCtx())
		if err != nil {
			r.staticLog.Print("Failed to get TUS uploads for pruning", err)
		}

		// Delete files.
		var prunedIDs []string
		for _, upload := range toDelete {
			uploadID, sp, err := upload.PruneInfo(r.tg.StopCtx())
			if err != nil {
				r.staticLog.Print("WARN: failed to fetch prune info from upload", err)
				continue
			}

			// Delete on disk. We don't care if the extended siapath
			// didn't exist but we print some loging if the regular
			// deletion failed.
			spFanout, err := sp.AddSuffixStr(skymodules.ExtendedSuffix)
			if err != nil {
				r.staticLog.Critical("Failed to append ExtededSuffix to SiaPath", err)
			}
			if err := r.DeleteFile(sp); err != nil {
				r.staticLog.Printf("WARN: failed to delete SiaPath %v: %v", sp.String(), err)
			}
			if err := r.DeleteFile(spFanout); err != nil && !errors.Contains(err, filesystem.ErrNotExist) {
				r.staticLog.Printf("WARN: failed to delete extended SiaPath %v: %v", sp.String(), err)
			}

			// Remember the ID to later prune it from the store.
			prunedIDs = append(prunedIDs, uploadID)

			// Delete from ongoing uploads if it exists.
			r.staticSkynetTUSUploader.mu.Lock()
			ongoingUpload, exists := r.staticSkynetTUSUploader.ongoingUploads[uploadID]
			if exists {
				delete(r.staticSkynetTUSUploader.ongoingUploads, uploadID)
				err = ongoingUpload.Close()
				if err != nil {
					r.staticLog.Critical("failed to close ongoing upload", err)
				}
			}
			r.staticSkynetTUSUploader.mu.Unlock()
		}

		// Prune from the store.
		if len(prunedIDs) > 0 {
			err = r.staticSkynetTUSUploader.staticUploadStore.Prune(r.tg.StopCtx(), prunedIDs)
			if err != nil {
				r.staticLog.Printf("WARN: failed to prune %v uploads from store: %v", len(prunedIDs), err)
			}
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
func (u *ongoingTUSUpload) ConcatUploads(ctx context.Context, partialUploads []handler.Upload) error {
	// Get fileinfo.
	fi, err := u.GetInfo(ctx)
	if err != nil {
		return errors.AddContext(err, "failed to fetch fileinfo")
	}

	// Concatenate the uploads by combining their fanouts. Concatenated
	// uploads may never consist of small uploads except for the last
	// upload.
	pu := partialUploads[0].(*ongoingTUSUpload)
	sup, fup, err := u.staticUpload.UploadParams(ctx)
	if err != nil {
		return err
	}
	chunkSize := skymodules.ChunkSize(fup.CipherType, uint64(fup.ErasureCode.MinPieces()))
	ec := pu.fileNode.ErasureCode()
	masterKey := pu.fileNode.MasterKey()
	var fanout []byte
	for i := range partialUploads {
		fi, err := pu.GetInfo(ctx)
		if err != nil {
			return errors.AddContext(err, "failed to get partial upload's fileinfo")
		}
		isSmall := fi.Size%int64(chunkSize) != 0
		if i < len(partialUploads)-1 && isSmall {
			return errors.New("only last upload is allowed to be small")
		}
		pu := partialUploads[i].(*ongoingTUSUpload)
		if pu.fileNode.ErasureCode().Identifier() != ec.Identifier() {
			return errors.New("all partial uploads need to use the same erasure coding")
		}
		if pu.fileNode.MasterKey().Type() != masterKey.Type() {
			return errors.New("all masterkeys need to have the same type")
		}
		if !bytes.Equal(pu.fileNode.MasterKey().Key(), masterKey.Key()) {
			return errors.New("all masterkeys need to be the same")
		}
		partialFanout, err := pu.staticUpload.Fanout(ctx)
		if err != nil {
			return errors.AddContext(err, "failed to fetch fanout of partial upload")
		}
		fanout = append(fanout, partialFanout...)
	}
	smBytes, err := u.staticUpload.SkyfileMetadata(ctx)
	if err != nil {
		return err
	}
	sup.SiaPath = skymodules.RandomSkynetFilePath()
	skylink, err := u.finishUploadLarge(ctx, fanout, sup, fi, masterKey, ec, smBytes)
	if err != nil {
		return err
	}

	// Associate all partial upload siafiles with this skylink.
	for i := range partialUploads {
		pu := partialUploads[i].(*ongoingTUSUpload)
		err = pu.fileNode.AddSkylink(skylink)
		if err != nil {
			return errors.AddContext(err, "failed to add skylink to partial uploads")
		}
	}

	// Make sure the updates in mongo are atomic.
	return u.staticUploader.staticUploadStore.WithTransaction(ctx, func(sctx context.Context) error {
		// Upon success, we mark the partial uploads as complete to prevent them
		// from being pruned.
		for i := range partialUploads {
			// Commit the partial upload as complete as well.
			pu = partialUploads[i].(*ongoingTUSUpload)
			err := pu.staticUpload.CommitFinishUpload(sctx, skylink)
			if err != nil {
				return errors.AddContext(err, "failed to commit partial upload")
			}
		}
		return errors.AddContext(u.staticUpload.CommitFinishUpload(sctx, skylink), "failed to commit concatenated upload")
	})
}
