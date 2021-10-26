package renter

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/tus/tusd/pkg/handler"
	"github.com/tus/tusd/pkg/memorylocker"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
)

type (
	// skynetTUSInMemoryUploadStore is an in-memory skynetTUSUploadStore
	// implementation.
	skynetTUSInMemoryUploadStore struct {
		uploads      map[string]*skynetInMemoryUpload
		mu           sync.Mutex
		staticLocker *memorylocker.MemoryLocker
	}

	// skynetInMemoryUpload represents an upload within the
	// skynetTUSInMemoryUploadStore.
	skynetInMemoryUpload struct {
		complete       bool
		fanout         []byte
		fi             handler.FileInfo
		lastWrite      time.Time
		staticFilename string
		staticSP       skymodules.SiaPath

		// Base chunk related fields.
		staticBaseChunkRedundancy uint8
		staticMetadata            []byte

		// Fanout related fields.
		staticFanoutDataPieces   int
		staticFanoutParityPieces int
		staticCipherType         crypto.CipherType

		// utilities
		mu sync.Mutex
	}
)

// NewSkynetTUSInMemoryUploadStore creates a new skynetTUSInMemoryUploadStore.
func NewSkynetTUSInMemoryUploadStore() skymodules.SkynetTUSUploadStore {
	return &skynetTUSInMemoryUploadStore{
		uploads:      make(map[string]*skynetInMemoryUpload),
		staticLocker: memorylocker.New(),
	}
}

// Close implements the io.Closer and is a no-op for the in-memory store.
func (us *skynetTUSInMemoryUploadStore) Close() error { return nil }

// CreateUpload creates a new upload and adds it to the store.
func (us *skynetTUSInMemoryUploadStore) CreateUpload(_ context.Context, fi handler.FileInfo, sp skymodules.SiaPath, fileName string, baseChunkRedundancy uint8, fanoutDataPieces, fanoutParityPieces int, sm []byte, ct crypto.CipherType) (skymodules.SkynetTUSUpload, error) {
	us.mu.Lock()
	defer us.mu.Unlock()
	upload := &skynetInMemoryUpload{
		complete:       false,
		fanout:         nil,
		fi:             fi,
		lastWrite:      time.Now(),
		staticFilename: fileName,
		staticSP:       sp,

		staticBaseChunkRedundancy: baseChunkRedundancy,
		staticMetadata:            sm,

		staticFanoutDataPieces:   fanoutDataPieces,
		staticFanoutParityPieces: fanoutParityPieces,
		staticCipherType:         ct,
	}
	us.uploads[fi.ID] = upload
	return upload, nil
}

// GetUpload returns an upload from the upload store.
func (us *skynetTUSInMemoryUploadStore) GetUpload(_ context.Context, id string) (skymodules.SkynetTUSUpload, error) {
	us.mu.Lock()
	defer us.mu.Unlock()
	upload, exists := us.uploads[id]
	if !exists {
		return nil, os.ErrNotExist
	}
	return upload, nil
}

// NewLock implements handler.Locker by forwarding the call to an in-memory
// locker.
func (us *skynetTUSInMemoryUploadStore) NewLock(id string) (handler.Lock, error) {
	return us.staticLocker.NewLock(id)
}

// CommitFinishUpload commits a finished upload to the upload store. This means
// setting the skylink of the finished upload.
func (u *skynetInMemoryUpload) CommitFinishUpload(_ context.Context, skylink skymodules.Skylink) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.complete {
		return ErrUploadFinished
	}
	u.fi.Offset = u.fi.Size
	u.complete = true
	u.lastWrite = time.Now()
	u.fi.MetaData["Skylink"] = skylink.String()
	return nil
}

// CommitWriteChunk commits the changes to the upload after successfully writing
// a chunk to the store.
func (u *skynetInMemoryUpload) CommitWriteChunk(_ context.Context, newOffset int64, newLastWrite time.Time, isSmall bool, fanout []byte) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.fanout = append(u.fanout, fanout...)
	return u.commitWriteChunk(newOffset, newLastWrite, isSmall)
}

// GetInfo returns the upload's underlying handler.FileInfo.
func (u *skynetInMemoryUpload) GetInfo(ctx context.Context) (handler.FileInfo, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.fi, nil
}

// PruneInfo returns the relevant info for pruning an upload. This includes the
// upload id and the siapath.
func (u *skynetInMemoryUpload) PruneInfo(ctx context.Context) (id string, sp skymodules.SiaPath, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	id = u.fi.ID
	sp = u.staticSP
	return
}

// UploadParams return skymodules.SkyfileUploadParameters and
// skymodules.FileUploadParams for the upload.
func (u *skynetInMemoryUpload) UploadParams(ctx context.Context) (skymodules.SkyfileUploadParameters, skymodules.FileUploadParams, error) {
	sup := skymodules.SkyfileUploadParameters{
		BaseChunkRedundancy: u.staticBaseChunkRedundancy,
		Filename:            u.staticFilename,
		SiaPath:             u.staticSP,
	}
	fanoutSiaPath, err := u.staticSP.AddSuffixStr(skymodules.ExtendedSuffix)
	if err != nil {
		return skymodules.SkyfileUploadParameters{}, skymodules.FileUploadParams{}, err
	}
	up, err := fileUploadParams(fanoutSiaPath, u.staticFanoutDataPieces, u.staticFanoutParityPieces, true, u.staticCipherType)
	if err != nil {
		return skymodules.SkyfileUploadParameters{}, skymodules.FileUploadParams{}, err
	}
	return sup, up, nil
}

// Fanout returns the fanout of the upload.
func (u *skynetInMemoryUpload) Fanout(ctx context.Context) ([]byte, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]byte{}, u.fanout...), nil
}

// SkyfileMetadata returns the raw SkyfileMetadata of the upload.
func (u *skynetInMemoryUpload) SkyfileMetadata(ctx context.Context) ([]byte, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]byte{}, u.staticMetadata...), nil
}

// Skylink returns the skylink of the upload if it was set already.
func (u *skynetInMemoryUpload) GetSkylink() (skymodules.Skylink, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	sl, exists := u.fi.MetaData["Skylink"]
	if !exists {
		return skymodules.Skylink{}, false
	}
	var skylink skymodules.Skylink
	if err := skylink.LoadString(sl); err != nil {
		build.Critical("upload contains invalid skylink")
		return skymodules.Skylink{}, false
	}
	return skylink, true
}

// Prune removes uploads that have been idle for too long.
func (us *skynetTUSInMemoryUploadStore) ToPrune(_ context.Context) ([]skymodules.SkynetTUSUpload, error) {
	us.mu.Lock()
	defer us.mu.Unlock()
	var toDelete []skymodules.SkynetTUSUpload
	for _, u := range us.uploads {
		u.mu.Lock()
		lastWrite := u.lastWrite
		complete := u.complete
		u.mu.Unlock()
		if time.Since(lastWrite) < PruneTUSUploadTimeout {
			continue // nothing to do
		}
		// If the upload wasn't completed, delete the files on disk.
		if !complete {
			toDelete = append(toDelete, u)
		}
	}
	return toDelete, nil
}

// Prune removes uploads that have been idle for too long.
func (us *skynetTUSInMemoryUploadStore) Prune(_ context.Context, uploadIDs []string) error {
	us.mu.Lock()
	defer us.mu.Unlock()
	for _, uploadID := range uploadIDs {
		delete(us.uploads, uploadID)
	}
	return nil
}

// WithTransaction allows for grouping multiple database operations into a
// single atomic transaction.
func (us *skynetTUSInMemoryUploadStore) WithTransaction(ctx context.Context, handler func(context.Context) error) error {
	return handler(ctx)
}

// commitWriteChunk commits the changes to the upload after successfully
// writing a chunk to the store.
func (u *skynetInMemoryUpload) commitWriteChunk(newOffset int64, newLastWrite time.Time, smallFile bool) error {
	u.fi.Offset = newOffset
	u.lastWrite = newLastWrite
	return nil
}
