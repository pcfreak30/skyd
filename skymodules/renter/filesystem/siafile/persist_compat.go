package siafile

import (
	"os"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/writeaheadlog"

	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
)

var (
	// errWrongMetadataVersion is the error returned when the metadata
	// version is wrong
	errWrongMetadataVersion = errors.New("wrong metadata version")

	// metadataVersion is the current version of the siafile Metadata
	metadataVersion = metadataVersion2

	// metadataVersion2 is the second version of the siafile Metadata
	metadataVersion2 = [16]byte{2}

	// metadataVersion1 is the first version of the siafile Metadata
	metadataVersion1 = [16]byte{1}

	// nilMetadataVesion is a helper for identifying an uninitialized
	// metadata version
	nilMetadataVesion = [16]byte{}
)

type (
	// FileData is a helper struct that contains all the relevant information
	// of a file. It simplifies passing the necessary data between modules and
	// keeps the interface clean.
	FileData struct {
		Name        string
		FileSize    uint64
		MasterKey   [crypto.EntropySize]byte
		ErasureCode skymodules.ErasureCoder
		RepairPath  string
		PieceSize   uint64
		Mode        os.FileMode
		Deleted     bool
		UID         SiafileUID
		Chunks      []FileChunk
	}
	// FileChunk is a helper struct that contains data about a chunk.
	FileChunk struct {
		Pieces [][]Piece
	}
)

// NewFromLegacyData creates a new SiaFile from data that was previously loaded
// from a legacy file.
func NewFromLegacyData(fd FileData, siaFilePath string, wal *writeaheadlog.WAL) (*SiaFile, error) {
	// Legacy master keys are always twofish keys.
	mk, err := crypto.NewSiaKey(crypto.TypeTwofish, fd.MasterKey[:])
	if err != nil {
		return nil, errors.AddContext(err, "failed to restore master key")
	}
	currentTime := time.Now()
	ecType, ecParams := marshalErasureCoder(fd.ErasureCode)
	zeroHealth := float64(1 + fd.ErasureCode.MinPieces()/(fd.ErasureCode.NumPieces()-fd.ErasureCode.MinPieces()))
	file := &SiaFile{
		staticMetadata: Metadata{
			AccessTime:              currentTime,
			ChunkOffset:             defaultReservedMDPages * pageSize,
			ChangeTime:              currentTime,
			CreateTime:              currentTime,
			CachedHealth:            zeroHealth,
			CachedStuckHealth:       0,
			CachedRedundancy:        0,
			CachedUserRedundancy:    0,
			CachedUploadProgress:    0,
			FileSize:                int64(fd.FileSize),
			LocalPath:               fd.RepairPath,
			StaticMasterKey:         mk.Key(),
			StaticMasterKeyType:     mk.Type(),
			Mode:                    fd.Mode,
			ModTime:                 currentTime,
			staticErasureCode:       fd.ErasureCode,
			StaticErasureCodeType:   ecType,
			StaticErasureCodeParams: ecParams,
			StaticPagesPerChunk:     numChunkPagesRequired(fd.ErasureCode.NumPieces()),
			StaticPieceSize:         fd.PieceSize,
			UniqueID:                SiafileUID(fd.UID),
		},
		deps:        modules.ProdDependencies,
		deleted:     fd.Deleted,
		numChunks:   len(fd.Chunks),
		siaFilePath: siaFilePath,
		wal:         wal,
	}
	// Update cached fields for 0-Byte files.
	if file.staticMetadata.FileSize == 0 {
		file.staticMetadata.CachedHealth = 0
		file.staticMetadata.CachedStuckHealth = 0
		file.staticMetadata.CachedRedundancy = float64(fd.ErasureCode.NumPieces()) / float64(fd.ErasureCode.MinPieces())
		file.staticMetadata.CachedUserRedundancy = file.staticMetadata.CachedRedundancy
		file.staticMetadata.CachedUploadProgress = 100
	}

	// Create the chunks.
	chunks := make([]chunk, len(fd.Chunks))
	for i := range chunks {
		chunks[i].Pieces = make([][]piece, file.staticMetadata.staticErasureCode.NumPieces())
		chunks[i].Index = i
	}

	// Populate the pubKeyTable of the file and add the pieces.
	pubKeyMap := make(map[string]uint32)
	for chunkIndex, chunk := range fd.Chunks {
		for pieceIndex, pieceSet := range chunk.Pieces {
			for _, p := range pieceSet {
				// Check if we already added that public key.
				tableOffset, exists := pubKeyMap[string(p.HostPubKey.Key)]
				if !exists {
					tableOffset = uint32(len(file.pubKeyTable))
					pubKeyMap[string(p.HostPubKey.Key)] = tableOffset
					file.pubKeyTable = append(file.pubKeyTable, HostPublicKey{
						PublicKey: p.HostPubKey,
						Used:      true,
					})
				}
				// Add the piece to the SiaFile.
				chunks[chunkIndex].Pieces[pieceIndex] = append(chunks[chunkIndex].Pieces[pieceIndex], piece{
					HostTableOffset: tableOffset,
					MerkleRoot:      p.MerkleRoot,
				})
			}
		}
	}

	// Save file to disk.
	if err := file.saveFile(chunks); err != nil {
		return nil, errors.AddContext(err, "unable to save file")
	}

	// Update the cached fields for progress and uploaded bytes.
	_, _, err = file.UploadProgressAndBytes()
	return file, err
}

// metadataCompatCheck handles the compatibility checks for the metadata based
// on the version
//
// NOTE: there is no need to use the backup and restore method of the metadata
// here because this is called on load. If there is an error if means we are
// unable to load the siafile, and therefore cannot use it which makes restoring
// the metadata pointless.
func (sf *SiaFile) metadataCompatCheck() error {
	// Quit early to avoid unnecessary disk write.
	if sf.staticMetadata.StaticVersion == metadataVersion {
		return nil
	}

	// Check uninitialized case
	if sf.staticMetadata.StaticVersion == nilMetadataVesion {
		sf.upgradeMetadataFromNilToV1()
	}

	// Check for version 1 updates.
	if sf.staticMetadata.StaticVersion == metadataVersion1 {
		err := sf.upgradeMetadataFromV1ToV2()
		if err != nil {
			return err
		}
	}

	// Check for current version
	if sf.staticMetadata.StaticVersion != metadataVersion {
		return errWrongMetadataVersion
	}

	// Save Metadata to persist updates
	err := sf.saveMetadata()
	if err != nil {
		return err
	}

	return nil
}

// upgradeMetadataFromNilToV1 upgrades an uninitialized metadata version to
// version 1 with the corresponding compat code
func (sf *SiaFile) upgradeMetadataFromNilToV1() {
	// Sanity Check
	if sf.staticMetadata.StaticVersion != nilMetadataVesion {
		build.Critical("upgradeMetadataFromNilToV1 called with non nil metadata")
		return
	}

	// COMPATv137 legacy files might not have a unique id.
	if sf.staticMetadata.UniqueID == "" {
		sf.staticMetadata.UniqueID = uniqueID()
	}

	// COMPATv140 legacy 0-byte files might not have correct cached
	// fields since we never update them once they are created.
	if sf.staticMetadata.FileSize == 0 {
		ec := sf.staticMetadata.staticErasureCode
		sf.staticMetadata.CachedHealth = 0
		sf.staticMetadata.CachedStuckHealth = 0
		sf.staticMetadata.CachedRedundancy = float64(ec.NumPieces()) / float64(ec.MinPieces())
		sf.staticMetadata.CachedUserRedundancy = sf.staticMetadata.CachedRedundancy
		sf.staticMetadata.CachedUploadProgress = 100
	}

	// Update the version now that we have completed the compat updates
	sf.staticMetadata.StaticVersion = metadataVersion1
}

// upgradeMetadataFromV1ToV2 upgrades a version 1 metadata to a version 2 with
// the corresponding compat code
func (sf *SiaFile) upgradeMetadataFromV1ToV2() error {
	// Sanity Check
	if sf.staticMetadata.StaticVersion != metadataVersion1 {
		err := errors.New("upgradeMetadataFromV1ToV2 called with non version 1 metadata")
		build.Critical(err)
		return err
	}

	// Stuck vs Unfinished files compatibility check.
	//
	// Before unfinished files were introduced a file might have been marked
	// as stuck if the upload failed. In this case, we don't expect the file
	// to ever be recoverable since the upload failed. Therefore, we don't
	// want it marked as stuck, we just want to ignore it and let the
	// unfinished files code eventually prune it.

	// Get the file's stuck status
	stuck := sf.numStuckChunks() > 0

	// Get the file's unique uploaded bytes to compare against the file size
	_, unique, err := sf.uploadedBytes()
	if err != nil {
		return err
	}
	size := uint64(sf.staticMetadata.FileSize)

	// Determine if the file is finished based on if it ever finished
	// uploading or has a localpath defined.
	sf.staticMetadata.Finished = unique >= size || sf.staticMetadata.LocalPath != ""

	// If the File is not finished, and stuck, reset the stuck status
	if !sf.staticMetadata.Finished && stuck {
		err = sf.setAllStuck(false)
		if err != nil {
			return errors.AddContext(err, "unable to mark unfinished file as unstuck")
		}
	}

	// Update the version now that we have completed the compat updates
	sf.staticMetadata.StaticVersion = metadataVersion2
	return nil
}
