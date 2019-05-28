package siafile

import (
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/NebulousLabs/writeaheadlog"
)

type (
	// PartialChunkInfo contains information required to find a partial chunk
	// within a combined chunk.
	PartialChunkInfo struct {
		length uint64
		offset uint64
		sf     *SiaFile
	}

	// SiafileUID is a unique identifier for siafile which is used to track
	// siafiles even after renaming them.
	SiafileUID string

	// Metadata is the metadata of a SiaFile and is JSON encoded.
	Metadata struct {
		StaticUniqueID SiafileUID `json:"uniqueid"` // unique identifier for file

		StaticPagesPerChunk uint8    `json:"pagesperchunk"` // number of pages reserved for storing a chunk.
		StaticVersion       [16]byte `json:"version"`       // version of the sia file format used
		FileSize            int64    `json:"filesize"`      // total size of the file
		StaticPieceSize     uint64   `json:"piecesize"`     // size of a single piece of the file
		LocalPath           string   `json:"localpath"`     // file to the local copy of the file used for repairing

		// Fields for encryption
		StaticMasterKey      []byte            `json:"masterkey"` // masterkey used to encrypt pieces
		StaticMasterKeyType  crypto.CipherType `json:"masterkeytype"`
		StaticSharingKey     []byte            `json:"sharingkey"` // key used to encrypt shared pieces
		StaticSharingKeyType crypto.CipherType `json:"sharingkeytype"`

		// Fields for partial uploads
		DisablePartialChunk bool   `json:"disablepartialchunk"` // determines whether the file should be treated like legacy files
		CombinedChunkID     string `json:"combinedchunkid"`     // id to find the combined chunk on disk
		CombinedChunkIndex  uint64 `json:"combinedchunkindex"`  // index of the chunk in the combined chunks siafile
		CombinedChunkStatus uint8  `json:"combinedchunkstatus"` // status of the file's partial chunk
		CombinedChunkOffset uint64 `json:"combinedchunkoffset"` // offset of partial chunk within combined chunk
		CombinedChunkLength uint64 `json:"combinedchunklength"` // length of partial chunk within combined chunk

		// The following fields are the usual unix timestamps of files.
		ModTime    time.Time `json:"modtime"`    // time of last content modification
		ChangeTime time.Time `json:"changetime"` // time of last metadata modification
		AccessTime time.Time `json:"accesstime"` // time of last access
		CreateTime time.Time `json:"createtime"` // time of file creation

		// Cached fields. These fields are cached fields and are only meant to be used
		// to create FileInfos for file related API endpoints. There is no guarantee
		// that these fields are up-to-date. Neither in memory nor on disk. Updates to
		// these fields aren't persisted immediately. Instead they will only be
		// persisted whenever another method persists the metadata or when the SiaFile
		// is closed.
		//
		// CachedRedundancy is the redundancy of the file on the network and is
		// updated within the 'Redundancy' method which is periodically called by the
		// repair code.
		//
		// CachedHealth is the health of the file on the network and is also
		// periodically updated by the health check loop whenever 'Health' is called.
		//
		// CachedStuckHealth is the health of the stuck chunks of the file. It is
		// updated by the health check loop. CachedExpiration is the lowest height at
		// which any of the file's contracts will expire. Also updated periodically by
		// the health check loop whenever 'Health' is called.
		//
		// CachedUploadedBytes is the number of bytes of the file that have been
		// uploaded to the network so far. Is updated every time a piece is added to
		// the siafile.
		//
		// CachedUploadProgress is the upload progress of the file and is updated
		// every time a piece is added to the siafile.
		//
		CachedRedundancy     float64           `json:"cachedredundancy"`
		CachedHealth         float64           `json:"cachedhealth"`
		CachedStuckHealth    float64           `json:"cachedstuckhealth"`
		CachedExpiration     types.BlockHeight `json:"cachedexpiration"`
		CachedUploadedBytes  uint64            `json:"cacheduploadedbytes"`
		CachedUploadProgress float64           `json:"cacheduploadprogress"`

		// Repair loop fields
		//
		// Health is the worst health of the file's unstuck chunks and
		// represents the percent of redundancy missing
		//
		// LastHealthCheckTime is the timestamp of the last time the SiaFile's
		// health was checked by Health()
		//
		// NumStuckChunks is the number of all the SiaFile's chunks that have
		// been marked as stuck by the repair loop
		//
		// Redundancy is the cached value of the last time the file's redundancy
		// was checked
		//
		// StuckHealth is the worst health of any of the file's stuck chunks
		//
		Health              float64   `json:"health"`
		LastHealthCheckTime time.Time `json:"lasthealthchecktime"`
		NumStuckChunks      uint64    `json:"numstuckchunks"`
		Redundancy          float64   `json:"redundancy"`
		StuckHealth         float64   `json:"stuckhealth"`

		// File ownership/permission fields.
		Mode    os.FileMode `json:"mode"`    // unix filemode of the sia file - uint32
		UserID  int         `json:"userid"`  // id of the user who owns the file
		GroupID int         `json:"groupid"` // id of the group that owns the file

		// staticChunkMetadataSize is the amount of space allocated within the
		// siafile for the metadata of a single chunk. It allows us to do
		// random access operations on the file in constant time.
		StaticChunkMetadataSize uint64 `json:"chunkmetadatasize"`

		// The following fields are the offsets for data that is written to disk
		// after the pubKeyTable. We reserve a generous amount of space for the
		// table and extra fields, but we need to remember those offsets in case we
		// need to resize later on.
		//
		// chunkOffset is the offset of the first chunk, forced to be a factor of
		// 4096, default 4kib
		//
		// pubKeyTableOffset is the offset of the publicKeyTable within the
		// file.
		//
		ChunkOffset       int64 `json:"chunkoffset"`
		PubKeyTableOffset int64 `json:"pubkeytableoffset"`

		// erasure code settings.
		//
		// StaticErasureCodeType specifies the algorithm used for erasure coding
		// chunks. Available types are:
		//   0 - Invalid / Missing Code
		//   1 - Reed Solomon Code
		//
		// erasureCodeParams specifies possible parameters for a certain
		// StaticErasureCodeType. Currently params will be parsed as follows:
		//   Reed Solomon Code - 4 bytes dataPieces / 4 bytes parityPieces
		//
		StaticErasureCodeType   [4]byte              `json:"erasurecodetype"`
		StaticErasureCodeParams [8]byte              `json:"erasurecodeparams"`
		staticErasureCode       modules.ErasureCoder // not persisted, exists for convenience
	}

	// BubbledMetadata is the metadata of a siafile that gets bubbled
	BubbledMetadata struct {
		Health              float64
		LastHealthCheckTime time.Time
		ModTime             time.Time
		NumStuckChunks      uint64
		Redundancy          float64
		Size                uint64
		StuckHealth         float64
	}

	// CachedHealthMetadata is a healper struct that contains the siafile health
	// metadata fields that are cached
	CachedHealthMetadata struct {
		Health      float64
		Redundancy  float64
		StuckHealth float64
	}
)

// AccessTime returns the AccessTime timestamp of the file.
func (sf *SiaFile) AccessTime() time.Time {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.staticMetadata.AccessTime
}

// ChangeTime returns the ChangeTime timestamp of the file.
func (sf *SiaFile) ChangeTime() time.Time {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.staticMetadata.ChangeTime
}

// CombinedChunkIndex returns the CombinedChunkIndex of the file.
func (sf *SiaFile) CombinedChunkIndex() uint64 {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.staticMetadata.CombinedChunkIndex
}

// CombinedChunkStatus returns the CombinedChunkStatus of the file's metadata.
func (sf *SiaFile) CombinedChunkStatus() uint8 {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.staticMetadata.CombinedChunkStatus
}

// CreateTime returns the CreateTime timestamp of the file.
func (sf *SiaFile) CreateTime() time.Time {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.staticMetadata.CreateTime
}

// ChunkSize returns the size of a single chunk of the file.
func (sf *SiaFile) ChunkSize() uint64 {
	return sf.staticChunkSize()
}

// LastHealthCheckTime returns the LastHealthCheckTime timestamp of the file
func (sf *SiaFile) LastHealthCheckTime() time.Time {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.staticMetadata.LastHealthCheckTime
}

// LocalPath returns the path of the local data of the file.
func (sf *SiaFile) LocalPath() string {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.staticMetadata.LocalPath
}

// NewPartialChunkInfo creates a new PartialChunkInfo from a given length,
// offset and SiaFile.
func NewPartialChunkInfo(length, offset uint64, sf *SiaFile) PartialChunkInfo {
	return PartialChunkInfo{
		length: length,
		offset: offset,
		sf:     sf,
	}
}

// SetCombinedChunk adds a combined chunk to the SiaFile, corresponding
// combined SiaFile, and deletes the .partial file. All of that happens
// atomically.
func SetCombinedChunk(cci []PartialChunkInfo, combinedChunkID string, combinedChunk []byte, dir string) error {
	sf0 := cci[0].sf
	sf0.mu.RLock()
	partialsSiaFile := sf0.partialsSiaFile
	sf0.mu.RUnlock()

	// Sanity check combined chunk length.
	if uint64(len(combinedChunk)) != partialsSiaFile.ChunkSize() {
		return fmt.Errorf("size of combined chunk should be %v but was %v",
			partialsSiaFile.staticChunkSize(), len(combinedChunk))
	}
	// Get the wal updates to grow the partialsSiaFile by a chunk while holding its
	// lock during the whole operation. We don't want other chunks to be added
	// while we are modifying the SiaFiles in this function.
	partialsSiaFile.mu.Lock()
	defer partialsSiaFile.mu.Unlock()
	chunkIndex, addCombinedChunkUpdates, err := partialsSiaFile.addCombinedChunk()
	if err != nil {
		return err
	}

	// Get updates to delete all the .partial files and update all the siafiles.
	identifier := partialsSiaFile.staticMetadata.staticErasureCode.Identifier()
	updates := addCombinedChunkUpdates
	for _, ci := range cci {
		sf := ci.sf
		// Sanity check that all the SiaFile's have the same erasure code settings.
		if sf.ErasureCode().Identifier() != identifier {
			return errors.New("combined files don't have matching erasure coders")
		}
		sf.mu.Lock()
		if sf.staticMetadata.CombinedChunkStatus != CombinedChunkStatusIncomplete {
			sf.mu.Unlock()
			return fmt.Errorf("previous combined chunk status needs to be %v but was %v",
				CombinedChunkStatusIncomplete, sf.staticMetadata.CombinedChunkStatus)
		}
		// Prepare delete update of .partial file.
		deleteUpdate := createDeletePartialUpdate(sf.partialFilePath())
		// Get updates to add chunk to combined SiaFile.
		sf.staticMetadata.CombinedChunkStatus = CombinedChunkStatusCompleted
		sf.staticMetadata.CombinedChunkID = combinedChunkID
		sf.staticMetadata.CombinedChunkIndex = chunkIndex
		sf.staticMetadata.CombinedChunkLength = ci.length
		sf.staticMetadata.CombinedChunkOffset = ci.offset
		metadataUpdates, err := sf.saveMetadataUpdates()
		if err != nil {
			sf.mu.Unlock()
			return err
		}
		// Append new updates.
		updates = append(updates, deleteUpdate)
		updates = append(updates, metadataUpdates...)
		sf.mu.Unlock()
	}
	// Add update to write combined chunk to disk.
	updates = append(updates, createInsertUpdate(filepath.Join(dir, combinedChunkID), 0, combinedChunk))
	return createAndApplyTransaction(partialsSiaFile.wal, updates...)
}

// MasterKey returns the masterkey used to encrypt the file.
func (sf *SiaFile) MasterKey() crypto.CipherKey {
	sk, err := crypto.NewSiaKey(sf.staticMetadata.StaticMasterKeyType, sf.staticMetadata.StaticMasterKey)
	if err != nil {
		// This should never happen since the constructor of the SiaFile takes
		// a CipherKey as an argument which guarantees that it is already a
		// valid key.
		panic(errors.AddContext(err, "failed to create masterkey of siafile"))
	}
	return sk
}

// Metadata returns the metadata of the SiaFile.
func (sf *SiaFile) Metadata() Metadata {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.staticMetadata
}

// Mode returns the FileMode of the SiaFile.
func (sf *SiaFile) Mode() os.FileMode {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.staticMetadata.Mode
}

// ModTime returns the ModTime timestamp of the file.
func (sf *SiaFile) ModTime() time.Time {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.staticMetadata.ModTime
}

// NumStuckChunks returns the Number of Stuck Chunks recorded in the file's
// metadata
func (sf *SiaFile) NumStuckChunks() uint64 {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.staticMetadata.NumStuckChunks
}

// PieceSize returns the size of a single piece of the file.
func (sf *SiaFile) PieceSize() uint64 {
	return sf.staticMetadata.StaticPieceSize
}

// Rename changes the name of the file to a new one.
func (sf *SiaFile) Rename(newSiaFilePath string) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	// Create path to renamed location.
	dir, _ := filepath.Split(newSiaFilePath)
	err := os.MkdirAll(dir, 0700)
	if err != nil {
		return err
	}
	// Create the delete update before changing the path to the new one.
	updates := []writeaheadlog.Update{sf.createDeleteUpdate()}
	// If there is a .partial file, delete it too.
	var partialChunk []byte
	if sf.staticMetadata.CombinedChunkStatus == CombinedChunkStatusIncomplete {
		updates = append(updates, createDeletePartialUpdate(sf.partialFilePath()))
		partialChunk, err = ioutil.ReadFile(sf.partialFilePath())
		if err != nil {
			return err
		}
	}
	// Rename file in memory.
	sf.siaFilePath = newSiaFilePath
	// Update the ChangeTime because the metadata changed.
	sf.staticMetadata.ChangeTime = time.Now()
	// Write the header to the new location.
	headerUpdate, err := sf.saveHeaderUpdates()
	if err != nil {
		return err
	}
	updates = append(updates, headerUpdate...)
	// Write the chunks to the new location.
	chunksUpdates := sf.saveChunksUpdates()
	updates = append(updates, chunksUpdates...)
	// Write a potential .partial file to the new location.
	if sf.staticMetadata.CombinedChunkStatus == CombinedChunkStatusIncomplete {
		updates = append(updates, createInsertUpdate(sf.partialFilePath(), 0, partialChunk))
	}
	// Apply updates.
	return createAndApplyTransaction(sf.wal, updates...)
}

// SetMode sets the filemode of the sia file.
func (sf *SiaFile) SetMode(mode os.FileMode) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	sf.staticMetadata.Mode = mode
	sf.staticMetadata.ChangeTime = time.Now()

	// Save changes to metadata to disk.
	updates, err := sf.saveMetadataUpdates()
	if err != nil {
		return err
	}
	return sf.createAndApplyTransaction(updates...)
}

// SetLastHealthCheckTime sets the LastHealthCheckTime in memory to the current
// time but does not update and write to disk.
//
// NOTE: This call should be used in conjunction with a method that saves the
// SiaFile metadata
func (sf *SiaFile) SetLastHealthCheckTime() {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	sf.staticMetadata.LastHealthCheckTime = time.Now()
}

// SetLocalPath changes the local path of the file which is used to repair
// the file from disk.
func (sf *SiaFile) SetLocalPath(path string) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	sf.staticMetadata.LocalPath = path

	// Save changes to metadata to disk.
	updates, err := sf.saveMetadataUpdates()
	if err != nil {
		return err
	}
	return sf.createAndApplyTransaction(updates...)
}

// Size returns the file's size.
func (sf *SiaFile) Size() uint64 {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return uint64(sf.staticMetadata.FileSize)
}

// UpdateAccessTime updates the AccessTime timestamp to the current time.
func (sf *SiaFile) UpdateAccessTime() error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	sf.staticMetadata.AccessTime = time.Now()

	// Save changes to metadata to disk.
	updates, err := sf.saveMetadataUpdates()
	if err != nil {
		return err
	}
	return sf.createAndApplyTransaction(updates...)
}

// UpdateLastHealthCheckTime updates the LastHealthCheckTime timestamp to the
// current time.
func (sf *SiaFile) UpdateLastHealthCheckTime() error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	sf.staticMetadata.LastHealthCheckTime = time.Now()
	// Save changes to metadata to disk.
	updates, err := sf.saveMetadataUpdates()
	if err != nil {
		return err
	}
	return sf.createAndApplyTransaction(updates...)
}

// UpdateCachedHealthMetadata updates the siafile metadata fields that are the
// cached health values
func (sf *SiaFile) UpdateCachedHealthMetadata(metadata CachedHealthMetadata) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	// Update the number of stuck chunks
	var numStuckChunks uint64
	for _, chunk := range sf.allChunks() {
		if chunk.Stuck {
			numStuckChunks++
		}
	}
	sf.staticMetadata.Health = metadata.Health
	sf.staticMetadata.NumStuckChunks = numStuckChunks
	sf.staticMetadata.Redundancy = metadata.Redundancy
	sf.staticMetadata.StuckHealth = metadata.StuckHealth
	// Save changes to metadata to disk.
	updates, err := sf.saveMetadataUpdates()
	if err != nil {
		return err
	}
	return sf.createAndApplyTransaction(updates...)
}

// staticChunkSize returns the size of a single chunk of the file.
func (sf *SiaFile) staticChunkSize() uint64 {
	return sf.staticMetadata.StaticPieceSize * uint64(sf.staticMetadata.staticErasureCode.MinPieces())
}

// uniqueID creates a random unique SiafileUID.
func uniqueID() SiafileUID {
	return SiafileUID(hex.EncodeToString(fastrand.Bytes(20)))
}
