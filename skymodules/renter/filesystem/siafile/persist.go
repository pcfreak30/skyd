package siafile

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"gitlab.com/SkynetLabs/skyd/siatest/dependencies"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/writeaheadlog"
	"go.sia.tech/siad/modules"

	"gitlab.com/NebulousLabs/encoding"
	"gitlab.com/SkynetLabs/skyd/build"
)

var (
	// errUnknownSiaFileUpdate is returned when applyUpdates finds an update
	// that is unknown
	errUnknownSiaFileUpdate = errors.New("unknown siafile update")
)

// ApplyUpdates is a wrapper for applyUpdates that uses the production
// dependencies.
func ApplyUpdates(updates ...writeaheadlog.Update) error {
	return applyUpdates(modules.ProdDependencies, updates...)
}

// LoadSiaFile is a wrapper for loadSiaFile that uses the production
// dependencies.
func LoadSiaFile(path string, wal *writeaheadlog.WAL) (*SiaFile, error) {
	return loadSiaFile(path, wal, modules.ProdDependencies)
}

// LoadSiaFileFromReader allows loading a SiaFile from a different location that
// directly from disk as long as the source satisfies the SiaFileSource
// interface.
func LoadSiaFileFromReader(r io.ReadSeeker, path string, wal *writeaheadlog.WAL) (*SiaFile, error) {
	return loadSiaFileFromReader(r, path, wal, modules.ProdDependencies)
}

// LoadSiaFileFromReaderWithChunks does not only read the header of the Siafile
// from disk but also the chunks which it returns separately. This is useful if
// the file is read from a buffer in-memory and the chunks can't be read from
// disk later.
func LoadSiaFileFromReaderWithChunks(r io.ReadSeeker, path string, wal *writeaheadlog.WAL) (*SiaFile, Chunks, error) {
	sf, err := LoadSiaFileFromReader(r, path, wal)
	if err != nil {
		return nil, Chunks{}, err
	}
	// Load chunks from reader.
	var chunks []chunk
	chunkBytes := make([]byte, int(sf.staticMetadata.StaticPagesPerChunk)*pageSize)
	for chunkIndex := 0; chunkIndex < sf.numChunks; chunkIndex++ {
		if _, err := r.Read(chunkBytes); err != nil && !errors.Contains(err, io.EOF) {
			return nil, Chunks{}, errors.AddContext(err, fmt.Sprintf("failed to read chunk %v", chunkIndex))
		}
		chunk, err := unmarshalChunk(uint32(sf.staticMetadata.staticErasureCode.NumPieces()), chunkBytes)
		if err != nil {
			return nil, Chunks{}, errors.AddContext(err, fmt.Sprintf("failed to unmarshal chunk %v", chunkIndex))
		}
		chunk.Index = int(chunkIndex)
		chunks = append(chunks, chunk)
	}
	return sf, Chunks{chunks}, nil
}

// SetSiaFilePath sets the path of the siafile on disk.
func (sf *SiaFile) SetSiaFilePath(path string) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	sf.siaFilePath = path
}

// applyUpdates applies a number of writeaheadlog updates to the corresponding
// SiaFile. This method can apply updates from different SiaFiles and should
// only be run before the SiaFiles are loaded from disk right after the startup
// of siad. Otherwise we might run into concurrency issues.
func applyUpdates(deps modules.Dependencies, updates ...writeaheadlog.Update) error {
	for _, u := range updates {
		err := func() error {
			switch u.Name {
			case updateDeleteName:
				return readAndApplyDeleteUpdate(deps, u)
			case updateInsertName:
				return readAndApplyInsertUpdate(deps, u)
			case updateDeletePartialName:
				return readAndApplyDeleteUpdate(deps, u)
			case writeaheadlog.NameDeleteUpdate:
				return writeaheadlog.ApplyDeleteUpdate(u)
			case writeaheadlog.NameTruncateUpdate:
				return writeaheadlog.ApplyTruncateUpdate(u)
			case writeaheadlog.NameWriteAtUpdate:
				return writeaheadlog.ApplyWriteAtUpdate(u)
			default:
				return errUnknownSiaFileUpdate
			}
		}()
		if err != nil {
			return errors.AddContext(err, "failed to apply update")
		}
	}
	return nil
}

// createDeleteUpdate is a helper method that creates a writeaheadlog for
// deleting a file.
func createDeleteUpdate(path string) writeaheadlog.Update {
	return writeaheadlog.Update{
		Name:         updateDeleteName,
		Instructions: []byte(path),
	}
}

// loadSiaFile loads a SiaFile from disk.
func loadSiaFile(path string, wal *writeaheadlog.WAL, deps modules.Dependencies) (*SiaFile, error) {
	// Open the file.
	f, err := deps.Open(path)
	if err != nil {
		return nil, err
	}
	sf, err := loadSiaFileFromReader(f, path, wal, deps)
	return sf, errors.Compose(err, f.Close())
}

// loadSiaFileFromReader allows loading a SiaFile from a different location that
// directly from disk as long as the source satisfies the SiaFileSource
// interface.
func loadSiaFileFromReader(r io.ReadSeeker, path string, wal *writeaheadlog.WAL, deps modules.Dependencies) (*SiaFile, error) {
	// Create the SiaFile
	sf := &SiaFile{
		deps:        deps,
		siaFilePath: path,
		wal:         wal,
	}
	// Load the metadata.
	decoder := json.NewDecoder(r)
	err := decoder.Decode(&sf.staticMetadata)
	if err != nil {
		return nil, errors.AddContext(err, "failed to decode metadata")
	}

	// Create the erasure coder.
	sf.staticMetadata.staticErasureCode, err = unmarshalErasureCoder(sf.staticMetadata.StaticErasureCodeType, sf.staticMetadata.StaticErasureCodeParams)
	if err != nil {
		return nil, err
	}

	// Load the pubKeyTable.
	pubKeyTableLen := sf.staticMetadata.ChunkOffset - sf.staticMetadata.PubKeyTableOffset
	if pubKeyTableLen < 0 {
		return nil, fmt.Errorf("pubKeyTableLen is %v, can't load file", pubKeyTableLen)
	}
	rawPubKeyTable := make([]byte, pubKeyTableLen)
	if _, err := r.Seek(sf.staticMetadata.PubKeyTableOffset, io.SeekStart); err != nil {
		return nil, errors.AddContext(err, "failed to seek to pubKeyTable")
	}
	if _, err := r.Read(rawPubKeyTable); errors.Contains(err, io.EOF) {
		// Empty table.
		sf.pubKeyTable = []HostPublicKey{}
	} else if err != nil {
		// Unexpected error.
		return nil, errors.AddContext(err, "failed to read pubKeyTable from disk")
	} else {
		// Unmarshal table.
		sf.pubKeyTable, err = unmarshalPubKeyTable(rawPubKeyTable)
		if err != nil {
			return nil, errors.AddContext(err, "failed to unmarshal pubKeyTable")
		}
	}

	// Seek to the start of the chunks.
	off, err := r.Seek(sf.staticMetadata.ChunkOffset, io.SeekStart)
	if err != nil {
		return nil, err
	}

	// Sanity check that the offset is page aligned.
	if off%pageSize != 0 {
		return nil, errors.New("chunkOff is not page aligned")
	}

	// Set numChunks field.
	numChunks := sf.staticMetadata.FileSize / int64(sf.staticChunkSize())
	if sf.staticMetadata.FileSize%int64(sf.staticChunkSize()) != 0 || numChunks == 0 {
		numChunks++
	}
	sf.numChunks = int(numChunks)

	// Compat check for metadata static version
	//
	// NOTE: This needs to be last so that the file is fully loaded and all
	// information is available for the compat checks and saving of the
	// metadata.
	err = sf.metadataCompatCheck()
	if err != nil {
		return nil, err
	}

	return sf, nil
}

// readAndApplyDeleteUpdate reads the delete update and applies it. This helper
// assumes that the file is not open
func readAndApplyDeleteUpdate(deps modules.Dependencies, update writeaheadlog.Update) error {
	err := deps.RemoveFile(readDeleteUpdate(update))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// readAndApplyInsertUpdate reads the insert update and applies it. This helper
// assumes that the file is not open and so should only be called on start up
// before any siafiles are loaded from disk
func readAndApplyInsertUpdate(deps modules.Dependencies, update writeaheadlog.Update) (err error) {
	// Decode update.
	path, index, data, err := readInsertUpdate(update)
	if err != nil {
		return err
	}

	// Open the file.
	f, err := deps.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Compose(err, f.Close())
	}()

	// Write data.
	if n, err := f.WriteAt(data, index); err != nil {
		return err
	} else if n < len(data) {
		return fmt.Errorf("update was only applied partially - %v / %v", n, len(data))
	}
	// Sync file.
	return f.Sync()
}

// readDeleteUpdate unmarshals the update's instructions and returns the
// encoded path.
func readDeleteUpdate(update writeaheadlog.Update) string {
	return string(update.Instructions)
}

// readInsertUpdate unmarshals the update's instructions and returns the path, index
// and data encoded in the instructions.
func readInsertUpdate(update writeaheadlog.Update) (path string, index int64, data []byte, err error) {
	if !IsSiaFileUpdate(update) {
		err = errors.New("readUpdate can't read non-SiaFile update")
		build.Critical(err)
		return
	}
	err = encoding.UnmarshalAll(update.Instructions, &path, &index, &data)
	return
}

// allocateHeaderPage allocates a new page for the metadata and publicKeyTable.
// It returns an update that moves the chunkData back by one pageSize if
// applied and also updates the ChunkOffset of the metadata.
func (sf *SiaFile) allocateHeaderPage() (_ writeaheadlog.Update, err error) {
	// Sanity check the chunk offset.
	if sf.staticMetadata.ChunkOffset%pageSize != 0 {
		build.Critical("the chunk offset is not page aligned")
	}
	// Open the file.
	f, err := sf.deps.OpenFile(sf.siaFilePath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return writeaheadlog.Update{}, errors.AddContext(err, "failed to open siafile")
	}
	defer func() {
		err = errors.Compose(err, f.Close())
	}()
	// Seek the chunk offset.
	_, err = f.Seek(sf.staticMetadata.ChunkOffset, io.SeekStart)
	if err != nil {
		return writeaheadlog.Update{}, err
	}
	// Read all the chunk data.
	chunkData, err := ioutil.ReadAll(f)
	if err != nil {
		return writeaheadlog.Update{}, err
	}
	// Move the offset back by a pageSize.
	sf.staticMetadata.ChunkOffset += pageSize

	// Create and return update.
	return sf.createInsertUpdate(sf.staticMetadata.ChunkOffset, chunkData), nil
}

// applyUpdates applies updates to the SiaFile. Only updates that belong to the
// SiaFile on which applyUpdates is called can be applied. Everything else will
// be considered a developer error and cause the update to not be applied to
// avoid corruption.  applyUpdates also syncs the SiaFile for convenience since
// it already has an open file handle.
func (sf *SiaFile) applyUpdates(updates ...writeaheadlog.Update) (err error) {
	// Sanity check that file hasn't been deleted.
	if sf.deleted {
		return errors.New("can't call applyUpdates on deleted file")
	}

	// If the set of updates contains a delete, all updates prior to that delete
	// are irrelevant, so perform the last delete and then process the remaining
	// updates. This also prevents a bug on Windows where we attempt to delete
	// the file while holding a open file handle.
	for i := len(updates) - 1; i >= 0; i-- {
		u := updates[i]
		if u.Name != updateDeleteName {
			continue
		}
		// Read and apply the delete update.
		if err := readAndApplyDeleteUpdate(sf.deps, u); err != nil {
			return err
		}
		// Truncate the updates and break out of the for loop.
		updates = updates[i+1:]
		break
	}
	if len(updates) == 0 {
		return nil
	}

	// Create the path if it doesn't exist yet.
	if err = os.MkdirAll(filepath.Dir(sf.siaFilePath), 0700); err != nil {
		return err
	}
	// Create and/or open the file.
	f, err := sf.deps.OpenFile(sf.siaFilePath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if err == nil {
			// If no error occurred we sync and close the file.
			err = errors.Compose(f.Sync(), f.Close())
		} else {
			// Otherwise we still need to close the file.
			err = errors.Compose(err, f.Close())
		}
	}()

	// Apply updates.
	for _, u := range updates {
		err := func() error {
			switch u.Name {
			case updateDeleteName:
				// Sanity check: all of the updates should be insert updates.
				build.Critical("Unexpected non-insert update", u.Name)
				return nil
			case updateInsertName:
				return sf.readAndApplyInsertUpdate(f, u)
			case updateDeletePartialName:
				return readAndApplyDeleteUpdate(sf.deps, u)
			case writeaheadlog.NameTruncateUpdate:
				return sf.readAndApplyTruncateUpdate(f, u)
			default:
				return errUnknownSiaFileUpdate
			}
		}()
		if err != nil {
			return errors.AddContext(err, "failed to apply update")
		}
	}
	return nil
}

// chunk reads the chunk with index chunkIndex from disk.
func (sf *SiaFile) chunk(chunkIndex int) (_ chunk, err error) {
	// If the file has been deleted we can't call chunk.
	if sf.deleted {
		return chunk{}, errors.AddContext(ErrDeleted, "can't call chunk on deleted file")
	}
	chunkOffset := sf.chunkOffset(chunkIndex)
	chunkBytes := make([]byte, int(sf.staticMetadata.StaticPagesPerChunk)*pageSize)
	f, err := sf.deps.Open(sf.siaFilePath)
	if err != nil {
		return chunk{}, errors.AddContext(err, "failed to open file to read chunk")
	}
	defer func() {
		err = errors.Compose(err, f.Close())
	}()
	if _, err := f.ReadAt(chunkBytes, chunkOffset); err != nil && !errors.Contains(err, io.EOF) {
		return chunk{}, errors.AddContext(err, "failed to read chunk from disk")
	}
	c, err := unmarshalChunk(uint32(sf.staticMetadata.staticErasureCode.NumPieces()), chunkBytes)
	if err != nil {
		return chunk{}, errors.AddContext(err, "failed to unmarshal chunk")
	}
	c.Index = chunkIndex // Set non-persisted field
	return c, nil
}

// iterateChunks iterates over all the chunks on disk and create wal updates for
// each chunk that was modified.
func (sf *SiaFile) iterateChunks(iterFunc func(chunk *chunk) (bool, error)) ([]writeaheadlog.Update, error) {
	if sf.deleted {
		return nil, errors.AddContext(ErrDeleted, "can't call iterateChunks on deleted file")
	}
	var updates []writeaheadlog.Update
	err := sf.iterateChunksReadonly(func(chunk chunk) error {
		modified, err := iterFunc(&chunk)
		if err != nil {
			return err
		}
		if modified {
			updates = append(updates, sf.saveChunkUpdate(chunk))
		}
		return nil
	})
	return updates, err
}

// iterateChunksReadonly iterates over all the chunks on disk and calls iterFunc
// on each one without modifying them.
func (sf *SiaFile) iterateChunksReadonly(iterFunc func(chunk chunk) error) (err error) {
	if sf.deleted {
		return errors.AddContext(err, "can't call iterateChunksReadonly on deleted file")
	}
	// Open the file.
	f, err := os.Open(sf.siaFilePath)
	if err != nil {
		return errors.AddContext(err, "failed to open file")
	}
	defer func() {
		err = errors.Compose(err, f.Close())
	}()

	// Seek to the first chunk.
	_, err = f.Seek(sf.staticMetadata.ChunkOffset, io.SeekStart)
	if err != nil {
		return errors.AddContext(err, "failed to seek to ChunkOffset")
	}
	// Read the chunks one-by-one.
	chunkBytes := make([]byte, int(sf.staticMetadata.StaticPagesPerChunk)*pageSize)
	for chunkIndex := 0; chunkIndex < sf.numChunks; chunkIndex++ {
		var c chunk
		var err error
		if _, err := f.Read(chunkBytes); err != nil && !errors.Contains(err, io.EOF) {
			return errors.AddContext(err, fmt.Sprintf("failed to read chunk %v", chunkIndex))
		}
		c, err = unmarshalChunk(uint32(sf.staticMetadata.staticErasureCode.NumPieces()), chunkBytes)
		if err != nil {
			return errors.AddContext(err, fmt.Sprintf("failed to unmarshal chunk %v", chunkIndex))
		}
		c.Index = chunkIndex
		if err := iterFunc(c); err != nil {
			return errors.AddContext(err, fmt.Sprintf("failed to iterate over chunk %v", chunkIndex))
		}
	}
	return nil
}

// chunkOffset returns the offset of a marshaled chunk within the file.
func (sf *SiaFile) chunkOffset(chunkIndex int) int64 {
	if chunkIndex < 0 {
		panic("chunk index can't be negative")
	}
	return sf.staticMetadata.ChunkOffset + int64(chunkIndex)*int64(sf.staticMetadata.StaticPagesPerChunk)*pageSize
}

// createAndApplyTransaction is a helper method that creates a writeaheadlog
// transaction and applies it.
func (sf *SiaFile) createAndApplyTransaction(updates ...writeaheadlog.Update) (err error) {
	// Sanity check that file hasn't been deleted.
	if sf.deleted {
		return errors.New("can't call createAndApplyTransaction on deleted file")
	}
	if len(updates) == 0 {
		return nil
	}
	// Create the writeaheadlog transaction.
	txn, err := sf.wal.NewTransaction(updates)
	if err != nil {
		return errors.AddContext(err, "failed to create wal txn")
	}
	// No extra setup is required. Signal that it is done.
	if err := <-txn.SignalSetupComplete(); err != nil {
		return errors.AddContext(err, "failed to signal setup completion")
	}
	// Starting at this point the changes to be made are written to the WAL.
	// This means we need to panic in case applying the updates fails.
	defer func() {
		if err != nil && !sf.deps.Disrupt(dependencies.DisruptFaultyFile) {
			panic(err)
		}
	}()
	// Apply the updates.
	if err := sf.applyUpdates(updates...); err != nil {
		return errors.AddContext(err, "failed to apply updates")
	}
	// Updates are applied. Let the writeaheadlog know.
	if err := txn.SignalUpdatesApplied(); err != nil {
		return errors.AddContext(err, "failed to signal that updates are applied")
	}
	return nil
}

// createAndApplyTransaction is a generic version of the
// createAndApplyTransaction method of the SiaFile. This will result in 2 fsyncs
// independent of the number of updates.
func createAndApplyTransaction(wal *writeaheadlog.WAL, updates ...writeaheadlog.Update) (err error) {
	if len(updates) == 0 {
		return nil
	}
	// Create the writeaheadlog transaction.
	txn, err := wal.NewTransaction(updates)
	if err != nil {
		return errors.AddContext(err, "failed to create wal txn")
	}
	// No extra setup is required. Signal that it is done.
	if err := <-txn.SignalSetupComplete(); err != nil {
		return errors.AddContext(err, "failed to signal setup completion")
	}
	// Starting at this point the changes to be made are written to the WAL.
	// This means we need to panic in case applying the updates fails.
	defer func() {
		if err != nil {
			panic(err)
		}
	}()
	// Apply the updates.
	if err := ApplyUpdates(updates...); err != nil {
		return errors.AddContext(err, "failed to apply updates")
	}
	// Updates are applied. Let the writeaheadlog know.
	if err := txn.SignalUpdatesApplied(); err != nil {
		return errors.AddContext(err, "failed to signal that updates are applied")
	}
	return nil
}

// createDeleteUpdate is a helper method that creates a writeaheadlog for
// deleting a file.
func (sf *SiaFile) createDeleteUpdate() writeaheadlog.Update {
	return createDeleteUpdate(sf.siaFilePath)
}

// createInsertUpdate is a helper method which creates a writeaheadlog update for
// writing the specified data to the provided index. It is usually not called
// directly but wrapped into another helper that creates an update for a
// specific part of the SiaFile. e.g. the metadata
func createInsertUpdate(path string, index int64, data []byte) writeaheadlog.Update {
	if index < 0 {
		index = 0
		data = []byte{}
		build.Critical("index passed to createUpdate should never be negative")
	}
	// Create update
	return writeaheadlog.Update{
		Name:         updateInsertName,
		Instructions: encoding.MarshalAll(path, index, data),
	}
}

// createInsertUpdate is a helper method which creates a writeaheadlog update for
// writing the specified data to the provided index. It is usually not called
// directly but wrapped into another helper that creates an update for a
// specific part of the SiaFile. e.g. the metadata
func (sf *SiaFile) createInsertUpdate(index int64, data []byte) writeaheadlog.Update {
	return createInsertUpdate(sf.siaFilePath, index, data)
}

// readAndApplyInsertUpdate reads the insert update for a SiaFile and then
// applies it
func (sf *SiaFile) readAndApplyInsertUpdate(f modules.File, update writeaheadlog.Update) error {
	// Decode update.
	path, index, data, err := readInsertUpdate(update)
	if err != nil {
		return err
	}

	// Sanity check path. Update should belong to SiaFile.
	if sf.siaFilePath != path {
		build.Critical(fmt.Sprintf("can't apply update for file %s to SiaFile %s", path, sf.siaFilePath))
		return nil
	}

	// Write data.
	if n, err := f.WriteAt(data, index); err != nil {
		return err
	} else if n < len(data) {
		return fmt.Errorf("update was only applied partially - %v / %v", n, len(data))
	}
	return nil
}

// ApplyTruncateUpdate parses and applies a truncate update.
func (sf *SiaFile) readAndApplyTruncateUpdate(f modules.File, u writeaheadlog.Update) error {
	if u.Name != writeaheadlog.NameTruncateUpdate {
		return fmt.Errorf("applyTruncateUpdate called on update of type %v", u.Name)
	}
	// Decode update.
	if len(u.Instructions) < 8 {
		return errors.New("instructions slice of update is too short to contain the size and path")
	}
	size := int64(binary.LittleEndian.Uint64(u.Instructions[:8]))
	// Truncate file.
	return f.Truncate(size)
}

// saveFile saves the SiaFile's header and the provided chunks atomically.
func (sf *SiaFile) saveFile(chunks []chunk) (err error) {
	// Sanity check that file hasn't been deleted.
	if sf.deleted {
		return errors.New("can't call saveFile on deleted file")
	}
	// Restore metadata on failure.
	defer func(backup Metadata) {
		if err != nil {
			sf.staticMetadata.restore(backup)
		}
	}(sf.staticMetadata.backup())
	// Update header and chunks.
	headerUpdates, err := sf.saveHeaderUpdates()
	if err != nil {
		return errors.AddContext(err, "failed to to create save header updates")
	}
	var chunksUpdates []writeaheadlog.Update
	for _, chunk := range chunks {
		chunksUpdates = append(chunksUpdates, sf.saveChunkUpdate(chunk))
	}
	err = sf.createAndApplyTransaction(append(headerUpdates, chunksUpdates...)...)
	return errors.AddContext(err, "failed to apply saveFile updates")
}

// saveChunkUpdate creates a writeaheadlog update that saves a single marshaled chunk
// to disk when applied.
// NOTE: For consistency chunk updates always need to be created after the
// header or metadata updates.
func (sf *SiaFile) saveChunkUpdate(chunk chunk) writeaheadlog.Update {
	offset := sf.chunkOffset(chunk.Index)
	chunkBytes := marshalChunk(chunk)
	return sf.createInsertUpdate(offset, chunkBytes)
}

// saveHeaderUpdates creates writeaheadlog updates to saves the metadata and
// pubKeyTable of the SiaFile to disk using the writeaheadlog. If the metadata
// and overlap due to growing too large and would therefore corrupt if they
// were written to disk, a new page is allocated.
// NOTE: For consistency chunk updates always need to be created after the
// header or metadata updates.
func (sf *SiaFile) saveHeaderUpdates() (_ []writeaheadlog.Update, err error) {
	// Create a list of updates which need to be applied to save the metadata.
	var updates []writeaheadlog.Update

	// Marshal the pubKeyTable.
	pubKeyTable, err := marshalPubKeyTable(sf.pubKeyTable)
	if err != nil {
		return nil, errors.AddContext(err, "failed to marshal pubkey table")
	}

	// Update the pubKeyTableOffset. This is not necessarily the final offset
	// but we need to marshal the metadata with this new offset to see if the
	// metadata and the pubKeyTable overlap.
	sf.staticMetadata.PubKeyTableOffset = sf.staticMetadata.ChunkOffset - int64(len(pubKeyTable))

	// Marshal the metadata.
	metadata, err := marshalMetadata(sf.staticMetadata)
	if err != nil {
		return nil, errors.AddContext(err, "failed to marshal metadata")
	}

	// If the metadata and the pubKeyTable overlap, we need to allocate a new
	// page for them. Afterwards we need to marshal the metadata again since
	// ChunkOffset and PubKeyTableOffset change when allocating a new page.
	for int64(len(metadata))+int64(len(pubKeyTable)) > sf.staticMetadata.ChunkOffset {
		// Create update to move chunkData back by a page.
		chunkUpdate, err := sf.allocateHeaderPage()
		if err != nil {
			return nil, errors.AddContext(err, "failed to allocate new header page")
		}
		updates = append(updates, chunkUpdate)
		// Update the PubKeyTableOffset.
		sf.staticMetadata.PubKeyTableOffset = sf.staticMetadata.ChunkOffset - int64(len(pubKeyTable))
		// Marshal the metadata again.
		metadata, err = marshalMetadata(sf.staticMetadata)
		if err != nil {
			return nil, errors.AddContext(err, "failed to marshal metadata again")
		}
	}

	// Create updates for the metadata and pubKeyTable.
	updates = append(updates, sf.createInsertUpdate(0, metadata))
	updates = append(updates, sf.createInsertUpdate(sf.staticMetadata.PubKeyTableOffset, pubKeyTable))
	return updates, nil
}

// saveMetadataUpdates saves the metadata of the SiaFile but not the
// publicKeyTable. Most of the time updates are only made to the metadata and
// not to the publicKeyTable and the metadata fits within a single disk sector
// on the harddrive. This means that using saveMetadataUpdate instead of
// saveHeader is potentially faster for SiaFiles with a header that can not be
// marshaled within a single page.
// NOTE: For consistency chunk updates always need to be created after the
// header or metadata updates.
func (sf *SiaFile) saveMetadataUpdates() ([]writeaheadlog.Update, error) {
	// Marshal the pubKeyTable.
	pubKeyTable, err := marshalPubKeyTable(sf.pubKeyTable)
	if err != nil {
		return nil, err
	}
	// Sanity check the length of the pubKeyTable to find out if the length of
	// the table changed. We should never just save the metadata if the table
	// changed as well as it might lead to corruptions.
	if sf.staticMetadata.PubKeyTableOffset+int64(len(pubKeyTable)) != sf.staticMetadata.ChunkOffset {
		build.Critical("never call saveMetadata if the pubKeyTable changed, call saveHeader instead")
		return sf.saveHeaderUpdates()
	}
	// Marshal the metadata.
	metadata, err := marshalMetadata(sf.staticMetadata)
	if err != nil {
		return nil, err
	}
	// If the header doesn't fit in the space between the beginning of the file
	// and the pubKeyTable, we need to call saveHeader since the pubKeyTable
	// needs to be moved as well and saveHeader is already handling that
	// edgecase.
	if int64(len(metadata)) > sf.staticMetadata.PubKeyTableOffset {
		return sf.saveHeaderUpdates()
	}
	// Otherwise we can create and return the updates.
	return []writeaheadlog.Update{sf.createInsertUpdate(0, metadata)}, nil
}
