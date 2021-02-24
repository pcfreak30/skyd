package renter

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/Sia/skykey"
	"gitlab.com/NebulousLabs/errors"
)

var (
	// The following are the errors returned when checking if the
	// SkyfileUploadParameters are valid for batching.
	errBatchDefaultPath = errors.New("batching does not supports the use of DefaultPath or DisableDefaultPath")
	errBatchDryRun      = errors.New("cannot perform a dry run with batched uploads")
	errBatchEncrypted   = errors.New("cannot batch encrypted uploads")
	errBatchForce       = errors.New("cannot use force param with batching")
	errBatchNotEnabled  = errors.New("SkyfileUploadParameters do not indicate file should be batched")
	errBatchRedundancy  = errors.New("batching only supports the default base chunk redundancy value")

	// errFileTooLarge is returned if a file that is too large is submitted to the
	// batch manager
	errFileTooLarge = fmt.Errorf("upload is too large for batching, max size is %v", maxBatchFileSize)

	// maxBatchFileSize is the maximum size of a skyfile that will be batched
	maxBatchFileSize = modules.SectorSize / 2

	// maxBatchSize is the maximum size of a batch of skyfile uploads
	//
	// TODO: If we want to increase this and limit edge cases, we will want to
	// link this with the packing code so that the remaining memory takes into
	// account the padding needed for packing to avoid edge cases of the data
	// being packed into batches exceeding a sector in size.
	maxBatchSize = modules.SectorSize / 2

	// maxBatchTime is the maximum amount of time that the skylinkBatchManager
	// will wait before batching the uploads
	maxBatchTime = build.Select(build.Var{
		Dev:      time.Second,
		Standard: time.Second * 10,
		Testing:  time.Second, // Set to 1 second to help with NDFs
	}).(time.Duration)
)

// batchUID is a unique ID for the batch element
type batchUID string

// newBatchUID returns a batchUID
func newBatchUID() batchUID {
	return batchUID(persist.UID())
}

type (
	// skylinkBatchManager handles the batching of skyfile uploads. The batch
	// manager manages one active batch at a time that  will execute when it
	// exceeds 2 MiB, i.e. half a sector size.  Files will be batched if they are
	// under 2 MiB.  This makes code that handles edge cases nice and easy.
	//
	// NOTE: only one batch manager should be active at a time
	skylinkBatchManager struct {
		// activeBatch is the batch that is currently collecting files to be uploaded
		activeBatch *skylinkBatch

		// Utilities
		staticRenter *Renter
		mu           sync.Mutex
	}

	// skylinkBatch contains the information about batching of skyfile uploads.
	skylinkBatch struct {
		// available is the channel used to signal that the upload is complete and the
		// externSkylinkData is available to be accessed
		available chan struct{}

		// currentFiles are the files currently being batched
		currentFiles map[batchUID]*skyFileObj

		// externSkylinkData is the data associated with the Skylink from the upload.
		//
		// NOTE: the externSkylinkData should be handled as an extern struct. The
		// batch will continue to work with this skylinkData until the upload is
		// complete, at which point the available chan will be closed, signaling
		// that the skylinkData is safe to handle.
		externSkylinkData map[batchUID]*skylinkData

		// finalized indicates if the batch is finalized and ready for uploading
		finalized bool

		// remainingMemory indicates the remaining memory available in the batch
		remainingMemory uint64

		// staticBatchManager is the global batchManager
		staticBatchManager *skylinkBatchManager

		err error
	}

	// skylinkData is the information returned to the upload caller. It contains
	// the resulting skylink that points to the uploaded file within the batch.
	skylinkData struct {
		err     error
		skylink modules.Skylink
	}

	// skyFileObj contains the information about a skyfile that is needed for
	// batching and uploading
	skyFileObj struct {
		// Batch information
		uid batchUID

		// File data
		data []byte
		size uint64

		// Packing information
		fp modules.FilePlacement
	}
)

// BatchSkyfile will submit a skyfile to the batch manager to be uploaded as
// a batch to skynet.
func (r *Renter) BatchSkyfile(sup modules.SkyfileUploadParameters, reader modules.SkyfileUploadReader) (modules.Skylink, error) {
	err := r.tg.Add()
	if err != nil {
		return modules.Skylink{}, err
	}
	defer r.tg.Done()
	return r.staticBatchManager.managedAddFile(sup, reader)
}

// newSkylinkBatchManager creates a new skylinkBatchManager for the Renter
func (r *Renter) newSkylinkBatchManager() {
	// Only one batch manager should be active at a time. We should not allow
	// overwriting an existing batch manager.
	if r.staticBatchManager != nil {
		build.Critical("skylink batch manager already initialized")
		return
	}

	// Initialize the batch manager and a batch
	bm := &skylinkBatchManager{staticRenter: r}
	bm.createNewBatch()
	r.staticBatchManager = bm
	return
}

// createNewBatch creates a new batch and sets it as the batch manager's active
// batch.
func (sbm *skylinkBatchManager) createNewBatch() {
	sbm.activeBatch = &skylinkBatch{
		staticBatchManager: sbm,
		currentFiles:       make(map[batchUID]*skyFileObj),
		externSkylinkData:  make(map[batchUID]*skylinkData),
		remainingMemory:    maxBatchSize,
		available:          make(chan struct{}),
	}
}

// managedAddFile will add a file to the batch manager.
//
// NOTE: we call the method on the batch manager to ensure we are only adding
// files to the current active batch.
func (sbm *skylinkBatchManager) managedAddFile(sup modules.SkyfileUploadParameters, reader modules.SkyfileUploadReader) (modules.Skylink, error) {
	err := validBatchSUP(sup)
	if err != nil {
		return modules.Skylink{}, err
	}

	// Acquire the batchManager lock
	sbm.mu.Lock()

	// Read the data from the reader
	buf := make([]byte, maxBatchFileSize)
	numBytes, err := io.ReadFull(reader, buf)
	buf = buf[:numBytes] // truncate the buffer

	// If we did not reach the EOF then they file is too large to be batched.
	if !(errors.Contains(err, io.EOF) || errors.Contains(err, io.ErrUnexpectedEOF)) {
		// NOTE: We don't bother adding the data back to the reader because this
		// upload should fail and the caller should resubmit without a batch
		// attempt.
		sbm.mu.Unlock()
		return modules.Skylink{}, errFileTooLarge
	}

	// Define the skyFileObj
	f := &skyFileObj{
		data: buf,
		size: uint64(numBytes),
	}

	// addFile does not block, instead it returns a channel that
	// will be closed when the batch is completed. The activeBatch
	// is covered by the mutex of the batchManager.
	externSkylinkData, finalChan := sbm.activeBatch.addFile(f)

	// File has successfully been added, release the lock on the batch manager
	// while we wait for the batch to be finalized.
	sbm.mu.Unlock()

	// Block until the batched upload is complete. It is not safe to look at
	// externSkylinkData until 'finalChan' has closed.  The batching code will be
	// updating the information in the externSkylinkData throughout the batching
	// process.
	select {
	case <-finalChan:
	case <-sbm.staticRenter.tg.StopChan():
		return modules.Skylink{}, errors.New("renter shutdown before batch could complete")
	}
	return externSkylinkData.skylink, externSkylinkData.err
}

// addFile adds a file to the skylinkBatch. If this is the first file in the
// batch, the batch will be initialized. If the file exceeds the remaining
// memory for the batch, the batch will be finalized and the file will be added
// to a new batch.
//
// NOTE: the skylinkData returned should be handled as an extern struct. The
// batch will continue to work with this skylinkData until the upload is
// complete, at which point the return chan will be closed, signaling that the
// skylinkData is safe to handle.
func (sb *skylinkBatch) addFile(f *skyFileObj) (*skylinkData, chan struct{}) {
	// First check if there is space for this file
	if f.size > sb.remainingMemory {
		// Finalize this batch
		sb.finalized = true

		// Create a new batch
		sb.staticBatchManager.createNewBatch()

		// Upload current batch
		go sb.threadedUploadData()

		// Add this file to the new batch
		return sb.staticBatchManager.activeBatch.addFile(f)
	}

	// Initialize the batch if this is the first file added
	if len(sb.currentFiles) == 0 {
		sb.initSkylinkBatch()
	}

	// Add to file to the batch
	//
	// Decrement the remaining memory
	sb.remainingMemory -= f.size

	// Add to the currentFiles
	uid := newBatchUID()
	f.uid = uid
	sb.currentFiles[uid] = f

	// Initialize the skylink data that will be returned
	res := &skylinkData{}
	sb.externSkylinkData[uid] = res
	return res, sb.available
}

// initSkylinkBatch is called the first time a file is added to the skylink
// batch. This will trigger a background timer to check on the status of the
// batch.
func (sb *skylinkBatch) initSkylinkBatch() {
	// Launch background timer
	time.AfterFunc(maxBatchTime, func() {
		sb.staticBatchManager.mu.Lock()
		defer sb.staticBatchManager.mu.Unlock()
		if sb.finalized {
			// Batch was already finalized because it filled up
			return
		}
		sb.finalized = true

		// Create a new active batch for the Batch Managed. This ensures that
		// nothing is referencing the current batch anymore.
		sb.staticBatchManager.createNewBatch()

		// Upload the data from the batch
		//
		// Ignore error from Launch as that just indicates that the renter has
		// shutdown, in which case threadedUploadData won't be called.
		r := sb.staticBatchManager.staticRenter
		_ = r.tg.Launch(sb.threadedUploadData)
	})
}

// threadedUploadData handles uploading the batch.
//
// By the time threadedUploadData is called, this thread is the only thread with
// access to the object. We guarantee that by creating a new batch while holding
// the skylinkBatchManager lock before calling threadedUploadData.
func (sb *skylinkBatch) threadedUploadData() {
	// Close the available chan at the end to signal the batch is complete. This
	// will signal the original file upload callers that it is OK to look at the
	// skylinkData.
	defer close(sb.available)

	defer func() {
		if sb.err == nil {
			return
		}
		// Set errors in the extern data and null out the skylinks in the event of
		// an error.
		for _, esd := range sb.externSkylinkData {
			esd.skylink = modules.Skylink{}
			esd.err = sb.err
		}
	}()

	// Package the files
	//
	// Build Files Map
	filesMap := make(map[string]uint64)
	for uid, f := range sb.currentFiles {
		filesMap[string(uid)] = f.size
	}

	// Pack Files
	fps, numSectors, err := modules.PackFiles(filesMap)
	if err != nil {
		sb.err = errors.AddContext(err, "batch upload failed to pack files")
		return
	}

	// Sanity check that we are only packing files into a single sector.
	if numSectors != 1 {
		sb.err = errors.New("batch upload failed due to unexpected number of sectors")
		build.Critical(sb.err)
		return
	}

	// Sort slice by offset
	sort.Slice(fps, func(i, j int) bool { return fps[i].SectorOffset < fps[j].SectorOffset })

	// Determine the total size of the batch
	lastFP := fps[len(fps)-1]
	totalSize := lastFP.SectorOffset + lastFP.Size

	// Move file placements back to skyFileObj and build basesector data based on
	// packed files.
	baseSectorData := make([]byte, totalSize)
	for _, fp := range fps {
		sfo, ok := sb.currentFiles[batchUID(fp.FileID)]
		// Sanity check that the UIDs used in the file packing are the same as the
		// ones used in the batch
		if !ok {
			sb.err = errors.New("file placement FileID not found in current files")
			build.Critical(sb.err)
			return
		}
		sfo.fp = fp

		// Write the file data to the offset
		data := sfo.data
		offset := fp.SectorOffset
		copy(baseSectorData[offset:], data)
	}

	// Generate SkyfileMetadata for the basesector.
	//
	// NOTE: the skyfile metadata is for the batched basesector and will be
	// returned with all the batched skylink downloads. As such we probably
	// shouldn't include any information about the individual files for privacy
	// and security reasons. This ultimately makes the metadata pretty generic and
	// useless.
	baseSectorLen := uint64(len(baseSectorData))
	metadata := modules.SkyfileMetadata{
		Filename: fmt.Sprintf("batched_file_%v", time.Now().UnixNano()),
		Length:   baseSectorLen,
	}
	metadataBytes, err := modules.SkyfileMetadataBytes(metadata)
	if err != nil {
		sb.err = errors.AddContext(err, "batch upload failed, unable to get skyfile metadata bytes")
		return
	}

	// Create Skyfile Layout
	sl := modules.SkyfileLayout{
		Version:      modules.SkyfileVersion,
		Filesize:     baseSectorLen,
		MetadataSize: uint64(len(metadataBytes)),
		CipherType:   crypto.TypePlain,
	}

	// Generate the BaseSector
	baseSector, fetchSize := modules.BuildBaseSector(sl.Encode(), nil, metadataBytes, baseSectorData)

	// Generate Merkleroot of the basesector
	merkleroot := crypto.MerkleRoot(baseSector)

	// Generate the baseSectorSkylink, this is used to upload the baseSector but
	// is never returned since the skylinks that are important are the skylinks
	// for the packed files
	baseSectorSkylink, err := modules.NewSkylinkV1(merkleroot, 0, fetchSize)
	if err != nil {
		sb.err = errors.AddContext(err, "batch upload failed to generate skylink for base sector")
		return
	}

	// Check if the skylink for the basesector is blocked. We only need to check
	// this because the blocklist contains the hash of the merkleroot. Since all
	// the batched files will have the same merkleroot that means that if any
	// skylink from this batch has been blocked then they are all blocked.
	r := sb.staticBatchManager.staticRenter
	if r.staticSkynetBlocklist.IsBlocked(baseSectorSkylink) {
		sb.err = errors.AddContext(ErrSkylinkBlocked, "batch upload failed, batch is blocked")
		return
	}

	// Generate skylinks for all the batched files.
	for _, f := range sb.currentFiles {
		// Generate skylink
		skylink, err := modules.NewSkylinkV1(merkleroot, f.fp.SectorOffset, f.size)
		if err != nil {
			sb.err = errors.AddContext(err, "batch upload failed to generate skylink for batched file")
			return
		}
		// Assign to skylinkData
		sd, ok := sb.externSkylinkData[f.uid]
		if !ok {
			sb.err = errors.New("skylinkData not found")
			build.Critical(sb.err)
			return
		}
		sd.skylink = skylink
	}

	// Create the SkyfileUploadParameters for the batch
	siaPath, err := modules.NewSiaPath(metadata.Filename)
	if err != nil {
		sb.err = errors.AddContext(err, "batch upload failed to create siapath for batch")
		return
	}
	sup := modules.SkyfileUploadParameters{
		BaseChunkRedundancy: SkyfileDefaultBaseChunkRedundancy,
		SiaPath:             siaPath,
	}
	skyfileEstablishDefaults(&sup)

	// Upload the base sector. We do not call managedUploadBaseSector because we
	// want to have access to the filenode to add the skylinks for all the batched
	// files to it.
	fileUploadParams, err := baseSectorUploadParamsFromSUP(sup)
	if err != nil {
		sb.err = errors.AddContext(err, "batch upload failed to create siafile upload parameters")
		return
	}

	// Normally this is set because the baseSector should be encrypted by the
	// caller. In this instance we also are setting it to TypePlain because batched
	// files do not currently support encryption.
	fileUploadParams.CipherType = crypto.TypePlain

	// Create a reader from the basesector and upload.
	baseSectorReader := bytes.NewReader(baseSector)
	fileNode, err := r.callUploadStreamFromReader(fileUploadParams, baseSectorReader)
	if err != nil {
		sb.err = errors.AddContext(err, "batch upload failed to stream upload small skyfile")
		return
	}
	defer func() {
		sb.err = errors.Compose(sb.err, fileNode.Close())
	}()

	// Add all the skylinks to the Siafile. We have already checked if any of the
	// are blocked so we can safely add them all
	//
	// TODO: currently we don't try and clean up the file if adding a skylink
	// fails. Should we? If we return an error we don't return the skylink
	// resulting in lost files that users don't have access to.
	err = fileNode.AddSkylink(baseSectorSkylink)
	if err != nil {
		sb.err = errors.AddContext(err, "batch upload failed to add basesector skylink to the file node")
		return
	}
	for _, sd := range sb.externSkylinkData {
		err = fileNode.AddSkylink(sd.skylink)
		if err != nil {
			sb.err = errors.AddContext(err, "batch upload failed to add batched file skylink to the file node")
			return
		}
	}
}

// validBatchSUP checks if the SkyfileUploadParameters are valid for batching
func validBatchSUP(sup modules.SkyfileUploadParameters) error {
	// Check that the file should be batched.
	if !sup.Batch {
		return errBatchNotEnabled
	}
	// Cannot use force param with batching
	if sup.Force {
		return errBatchForce
	}
	// Encrypted uploads cannot be batched
	isEncrypted := sup.SkykeyName != "" || sup.SkykeyID != skykey.SkykeyID{}
	if isEncrypted {
		return errBatchEncrypted
	}
	// Not currently supporting dryRun with batching
	if sup.DryRun {
		return errBatchDryRun
	}
	// Batched files should use the default BaseChunkRedundancy
	if sup.BaseChunkRedundancy != 0 && sup.BaseChunkRedundancy != SkyfileDefaultBaseChunkRedundancy {
		return errBatchRedundancy
	}
	// DefaultPath and DisableDefaultPath cannot be set.
	if sup.DisableDefaultPath || sup.DefaultPath != "" {
		return errBatchDefaultPath
	}

	// SiaPath, Root, Filename and Mode are just ignored. It is not an issue if
	// they are set because they do not impact the other files in the batch nor
	// are they needed for the download. They will just not be used.
	//
	// The Reader is ignored because the SkyfileUploadReader should be submitted
	// and used for the batch.
	return nil
}
