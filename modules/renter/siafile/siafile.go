package siafile

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"

	"gitlab.com/NebulousLabs/writeaheadlog"
)

var (
	// ErrPathOverload is an error when a file already exists at that location
	ErrPathOverload = errors.New("a file already exists at that location")
	// ErrUnknownPath is an error when a file cannot be found with the given path
	ErrUnknownPath = errors.New("no file known with that path")
	// ErrUnknownThread is an error when a SiaFile is trying to be closed by a
	// thread that is not in the threadMap
	ErrUnknownThread = errors.New("thread should not be calling Close(), does not have control of the siafile")
)

type (
	// SiaFile is the disk format for files uploaded to the Sia network.  It
	// contains all the necessary information to recover a file from its hosts and
	// allows for easy constant-time updates of the file without having to read or
	// write the whole file.
	SiaFile struct {
		// staticMetadata is the mostly static staticMetadata of a SiaFile. The reserved
		// size of the staticMetadata on disk should always be a multiple of 4kib.
		// The staticMetadata is also the only part of the file that is JSON encoded
		// and can therefore be easily extended.
		staticMetadata Metadata

		// pubKeyTable stores the public keys of the hosts this file's pieces are uploaded to.
		// Since multiple pieces from different chunks might be uploaded to the same host, this
		// allows us to deduplicate the rather large public keys.
		pubKeyTable []HostPublicKey

		// fullChunks are the chunks the file was split into. A SiaFile might also
		// contain a partial chunk which resides within the partialsSiaFile.
		fullChunks []chunk

		// utility fields. These are not persisted.
		deleted bool
		deps    modules.Dependencies
		mu      sync.RWMutex
		wal     *writeaheadlog.WAL // the wal that is used for SiaFiles

		// siaFilePath is the path to the .sia file on disk.
		siaFilePath string

		// partialSiaFile is the SiaFile that holds or could hold the partial chunk of
		// this siafile. Since we don't know if a file is going to have a partial
		// chunk we simply keep the megafiles always open and assign them to SiaFiles
		// with matching redundancy.
		partialsSiaFile *SiaFileSetEntry
	}

	// chunk represents a single chunk of a file on disk
	chunk struct {
		// ExtensionInfo is some reserved space for each chunk that allows us
		// to indicate if a chunk is special.
		ExtensionInfo [16]byte

		// Pieces are the Pieces of the file the chunk consists of.
		Pieces [][]piece

		// Stuck indicates if the chunk was not repaired as expected by the
		// repair loop
		Stuck bool
	}

	// Chunk is an exported chunk. It contains exported pieces.
	Chunk struct {
		Pieces [][]Piece
	}

	// piece represents a single piece of a chunk on disk
	piece struct {
		offset          uint32      // offset of the piece within the sector
		length          uint32      // length of the piece within the sector
		HostTableOffset uint32      // offset of the host's key within the pubKeyTable
		MerkleRoot      crypto.Hash // merkle root of the piece
	}

	// Piece is an exported piece. It contains a resolved public key instead of
	// the table offset.
	Piece struct {
		HostPubKey types.SiaPublicKey // public key of the host
		MerkleRoot crypto.Hash        // merkle root of the piece
	}

	// HostPublicKey is an entry in the HostPubKey table.
	HostPublicKey struct {
		PublicKey types.SiaPublicKey // public key of host
		Used      bool               // indicates if we currently use this host
	}
)

// MarshalSia implements the encoding.SiaMarshaler interface.
func (hpk HostPublicKey) MarshalSia(w io.Writer) error {
	e := encoding.NewEncoder(w)
	e.Encode(hpk.PublicKey)
	e.WriteBool(hpk.Used)
	return e.Err()
}

// SiaFilePath returns the siaFilePath field of the SiaFile.
func (sf *SiaFile) SiaFilePath() string {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.siaFilePath
}

// UnmarshalSia implements the encoding.SiaUnmarshaler interface.
func (hpk *HostPublicKey) UnmarshalSia(r io.Reader) error {
	d := encoding.NewDecoder(r, encoding.DefaultAllocLimit)
	d.Decode(&hpk.PublicKey)
	hpk.Used = d.NextBool()
	return d.Err()
}

// numPieces returns the total number of pieces uploaded for a chunk. This
// means that numPieces can be greater than the number of pieces created by the
// erasure coder.
func (c *chunk) numPieces() (numPieces int) {
	for _, c := range c.Pieces {
		numPieces += len(c)
	}
	return
}

// New create a new SiaFile.
func New(siaFilePath, source string, wal *writeaheadlog.WAL, erasureCode modules.ErasureCoder, masterKey crypto.CipherKey, fileSize uint64, fileMode os.FileMode, partialsSiaFile *SiaFileSetEntry) (*SiaFile, error) {
	currentTime := time.Now()
	ecType, ecParams := marshalErasureCoder(erasureCode)
	zeroHealth := float64(1 + erasureCode.MinPieces()/(erasureCode.NumPieces()-erasureCode.MinPieces()))
	file := &SiaFile{
		staticMetadata: Metadata{
			AccessTime:              currentTime,
			ChunkOffset:             defaultReservedMDPages * pageSize,
			ChangeTime:              currentTime,
			CombinedChunkStatus:     combinedChunkStatusNoChunk,
			CreateTime:              currentTime,
			CachedHealth:            zeroHealth,
			CachedStuckHealth:       0,
			CachedRedundancy:        0,
			CachedUploadProgress:    0,
			FileSize:                int64(fileSize),
			LocalPath:               source,
			StaticMasterKey:         masterKey.Key(),
			StaticMasterKeyType:     masterKey.Type(),
			Mode:                    fileMode,
			ModTime:                 currentTime,
			staticErasureCode:       erasureCode,
			StaticErasureCodeType:   ecType,
			StaticErasureCodeParams: ecParams,
			StaticPagesPerChunk:     numChunkPagesRequired(erasureCode.NumPieces()),
			StaticPieceSize:         modules.SectorSize - masterKey.Type().Overhead(),
			StaticUniqueID:          uniqueID(),
		},
		deps:            modules.ProdDependencies,
		partialsSiaFile: partialsSiaFile,
		siaFilePath:     siaFilePath,
		wal:             wal,
	}
	// Init chunks.
	numChunks := fileSize / file.staticChunkSize()
	if fileSize%file.staticChunkSize() != 0 || numChunks == 0 {
		numChunks++
		panic("handle partial chunk")
	}
	file.fullChunks = make([]chunk, numChunks)
	for i := range file.fullChunks {
		file.fullChunks[i].Pieces = make([][]piece, erasureCode.NumPieces())
	}
	// Update cached fields for 0-Byte files.
	if file.staticMetadata.FileSize == 0 {
		file.staticMetadata.CachedHealth = 0
		file.staticMetadata.CachedStuckHealth = 0
		file.staticMetadata.CachedRedundancy = float64(erasureCode.NumPieces()) / float64(erasureCode.MinPieces())
		file.staticMetadata.CachedUploadProgress = 100
	}
	// Save file.
	return file, file.saveFile()
}

// GrowByOneChunk grows a SiaFile by exactly one chunk.
func (sf *SiaFile) GrowByOneChunk() error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	numChunks := sf.numChunks()
	return sf.growNumChunks(numChunks + 1)
}

// GrowNumChunks increases the number of chunks in the SiaFile to numChunks. If
// the file already contains >= numChunks chunks then GrowNumChunks is a no-op.
func (sf *SiaFile) GrowNumChunks(numChunks uint64) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.growNumChunks(numChunks)
}

// SetFileSize changes the fileSize of the SiaFile.
func (sf *SiaFile) SetFileSize(fileSize uint64) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	sf.staticMetadata.FileSize = int64(fileSize)
	updates, err := sf.saveMetadataUpdates()
	if err != nil {
		return err
	}
	return sf.createAndApplyTransaction(updates...)
}

// AddPiece adds an uploaded piece to the file. It also updates the host table
// if the public key of the host is not already known.
func (sf *SiaFile) AddPiece(pk types.SiaPublicKey, chunkIndex, pieceIndex uint64, merkleRoot crypto.Hash) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	// If the file was deleted we can't add a new piece since it would write
	// the file to disk again.
	if sf.deleted {
		return errors.New("can't add piece to deleted file")
	}

	// Update cache.
	defer sf.uploadProgressAndBytes()

	// Handle piece being added to the partial chunk.
	if chunkIndex == sf.numChunks()-1 && sf.staticMetadata.CombinedChunkStatus > combinedChunkStatusNoChunk {
		return sf.partialsSiaFile.AddPiece(pk, sf.staticMetadata.CombinedChunkIndex, pieceIndex, merkleRoot)
	}

	// Get the index of the host in the public key table.
	tableIndex := -1
	for i, hpk := range sf.pubKeyTable {
		if hpk.PublicKey.Algorithm == pk.Algorithm && bytes.Equal(hpk.PublicKey.Key, pk.Key) {
			tableIndex = i
			break
		}
	}
	// If we don't know the host yet, we add it to the table.
	tableChanged := false
	if tableIndex == -1 {
		sf.pubKeyTable = append(sf.pubKeyTable, HostPublicKey{
			PublicKey: pk,
			Used:      true,
		})
		tableIndex = len(sf.pubKeyTable) - 1
		tableChanged = true
	}
	// Check if the chunkIndex is valid.
	if chunkIndex >= uint64(len(sf.fullChunks)) {
		return fmt.Errorf("chunkIndex %v out of bounds (%v)", chunkIndex, len(sf.fullChunks))
	}
	// Check if the pieceIndex is valid.
	if pieceIndex >= uint64(len(sf.fullChunks[chunkIndex].Pieces)) {
		return fmt.Errorf("pieceIndex %v out of bounds (%v)", pieceIndex, len(sf.fullChunks[chunkIndex].Pieces))
	}
	// Add the piece to the chunk.
	sf.fullChunks[chunkIndex].Pieces[pieceIndex] = append(sf.fullChunks[chunkIndex].Pieces[pieceIndex], piece{
		HostTableOffset: uint32(tableIndex),
		MerkleRoot:      merkleRoot,
	})

	// Update the AccessTime, ChangeTime and ModTime.
	sf.staticMetadata.AccessTime = time.Now()
	sf.staticMetadata.ChangeTime = sf.staticMetadata.AccessTime
	sf.staticMetadata.ModTime = sf.staticMetadata.AccessTime

	// Defrag the chunk if necessary.
	chunk := &sf.fullChunks[chunkIndex]
	chunkSize := marshaledChunkSize(chunk.numPieces())
	maxChunkSize := int64(sf.staticMetadata.StaticPagesPerChunk) * pageSize
	if chunkSize > maxChunkSize {
		sf.defragChunk(chunk)
	}

	// If the chunk is still too large after the defrag, we abort.
	chunkSize = marshaledChunkSize(chunk.numPieces())
	if chunkSize > maxChunkSize {
		return fmt.Errorf("chunk doesn't fit into allocated space %v > %v", chunkSize, maxChunkSize)
	}

	// Update the file atomically.
	var updates []writeaheadlog.Update
	var err error
	// Get the updates for the header.
	if tableChanged {
		// If the table changed we update the whole header.
		updates, err = sf.saveHeaderUpdates()
	} else {
		// Otherwise just the metadata.
		updates, err = sf.saveMetadataUpdates()
	}
	if err != nil {
		return err
	}
	// Save the changed chunk to disk.
	chunkUpdate := sf.saveChunkUpdate(int(chunkIndex))
	return sf.createAndApplyTransaction(append(updates, chunkUpdate)...)
}

// chunkHealth returns the health of the chunk which is defined as the percent
// of parity pieces remaining.
//
// health = 0 is full redundancy, health <= 1 is recoverable, health > 1 needs
// to be repaired from disk or repair by upload streaming
func (sf *SiaFile) chunkHealth(chunkIndex int, offlineMap map[string]bool, goodForRenewMap map[string]bool) float64 {
	// Handle returning health of partial chunk.
	if chunkIndex == int(sf.numChunks())-1 && sf.staticMetadata.CombinedChunkStatus > combinedChunkStatusNoChunk {
		return sf.partialsSiaFile.ChunkHealth(int(sf.staticMetadata.CombinedChunkIndex), offlineMap, goodForRenewMap)
	}
	// The max number of good pieces that a chunk can have is NumPieces()
	numPieces := sf.staticMetadata.staticErasureCode.NumPieces()
	minPieces := sf.staticMetadata.staticErasureCode.MinPieces()
	targetPieces := float64(numPieces - minPieces)
	// Find the good pieces that are good for renew
	goodPieces, _ := sf.goodPieces(chunkIndex, offlineMap, goodForRenewMap)
	// Sanity Check, if something went wrong, default to minimum health
	if int(goodPieces) > numPieces || goodPieces < 0 {
		build.Critical("unexpected number of goodPieces for chunkHealth")
		goodPieces = 0
	}
	return 1 - (float64(int(goodPieces)-minPieces) / targetPieces)
}

// ChunkHealth returns the health of the chunk which is defined as the percent
// of parity pieces remaining.
func (sf *SiaFile) ChunkHealth(index int, offlineMap map[string]bool, goodForRenewMap map[string]bool) float64 {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.chunkHealth(index, offlineMap, goodForRenewMap)
}

// ChunkIndexByOffset will return the chunkIndex that contains the provided
// offset of a file and also the relative offset within the chunk. If the
// offset is out of bounds, chunkIndex will be equal to NumChunk().
func (sf *SiaFile) ChunkIndexByOffset(offset uint64) (chunkIndex uint64, off uint64) {
	chunkIndex = offset / sf.staticChunkSize()
	off = offset % sf.staticChunkSize()
	return
}

// Delete removes the file from disk and marks it as deleted. Once the file is
// deleted, certain methods should return an error.
func (sf *SiaFile) Delete() error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	// We can't delete a file multiple times.
	if sf.deleted {
		return errors.New("requested file has already been deleted")
	}
	update := sf.createDeleteUpdate()
	err := sf.createAndApplyTransaction(update)
	sf.deleted = true
	return err
}

// Deleted indicates if this file has been deleted by the user.
func (sf *SiaFile) Deleted() bool {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.deleted
}

// ErasureCode returns the erasure coder used by the file.
func (sf *SiaFile) ErasureCode() modules.ErasureCoder {
	return sf.staticMetadata.staticErasureCode
}

// Save saves the entire file to disk.
func (sf *SiaFile) Save() error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.saveFile()
}

// SaveMetadata saves the file's metadata to disk.
func (sf *SiaFile) SaveMetadata() error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	updates, err := sf.saveMetadataUpdates()
	if err != nil {
		return err
	}
	return sf.createAndApplyTransaction(updates...)
}

// Expiration updates CachedExpiration with the lowest height at which any of
// the file's contracts will expire and returns the new value.
func (sf *SiaFile) Expiration(contracts map[string]modules.RenterContract) types.BlockHeight {
	// TODO: how to handle the partial chunk here?
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if len(sf.pubKeyTable) == 0 {
		sf.staticMetadata.CachedExpiration = 0
		return 0
	}

	lowest := ^types.BlockHeight(0)
	for _, pk := range sf.pubKeyTable {
		contract, exists := contracts[pk.PublicKey.String()]
		if !exists {
			continue
		}
		if contract.EndHeight < lowest {
			lowest = contract.EndHeight
		}
	}
	sf.staticMetadata.CachedExpiration = lowest
	return lowest
}

// Health calculates the health of the file to be used in determining repair
// priority. Health of the file is the lowest health of any of the chunks and is
// defined as the percent of parity pieces remaining. The NumStuckChunks will be
// calculated for the SiaFile and returned.
//
// NOTE: The cached values of the health and stuck health will be set but not
// saved to disk as Health() does not write to disk. If the cached values need
// to be updated on disk then a metadata save method should be called in
// conjunction with Health()
//
// health = 0 is full redundancy, health <= 1 is recoverable, health > 1 needs
// to be repaired from disk
func (sf *SiaFile) Health(offline map[string]bool, goodForRenew map[string]bool) (h float64, sh float64, nsc uint64) {
	numPieces := float64(sf.staticMetadata.staticErasureCode.NumPieces())
	minPieces := float64(sf.staticMetadata.staticErasureCode.MinPieces())
	worstHealth := 1 - ((0 - minPieces) / (numPieces - minPieces))

	sf.mu.Lock()
	defer sf.mu.Unlock()

	// Update the cache.
	defer func() {
		sf.staticMetadata.CachedHealth = h
		sf.staticMetadata.CachedStuckHealth = sh
	}()

	// Check if siafile is deleted
	if sf.deleted {
		// Don't return health information of a deleted file to prevent
		// misrepresenting the health information of a directory
		return 0, 0, 0
	}
	// Check for Zero byte files
	if sf.staticMetadata.FileSize == 0 {
		// Return default health information for zero byte files to prevent
		// misrepresenting the health information of a directory
		return 0, 0, 0
	}
	var health, stuckHealth float64
	var numStuckChunks uint64
	allChunks := sf.allChunks()
	for chunkIndex, chunk := range allChunks {
		chunkHealth := sf.chunkHealth(chunkIndex, offline, goodForRenew)

		// Update the health or stuckHealth of the file according to the health
		// of the chunk. The health of the file is the worst health (highest
		// number) of all the chunks in the file.
		if chunk.Stuck {
			numStuckChunks++
			if chunkHealth > stuckHealth {
				stuckHealth = chunkHealth
			}
		} else if chunkHealth > health {
			health = chunkHealth
		}
	}

	// TODO: Handle partial chunk

	// Check if all chunks are stuck, if so then set health to max health to
	// avoid file being targetted for repair
	if int(numStuckChunks) == len(allChunks) {
		health = float64(0)
	}
	// Sanity check, verify that the calculated health is not worse (greater)
	// than the worst health.
	if health > worstHealth {
		build.Critical("WARN: health out of bounds. Max value, Min value, health found", worstHealth, 0, health)
		health = worstHealth
	}
	// Sanity check, verify that the calculated stuck health is not worse
	// (greater) than the worst health.
	if stuckHealth > worstHealth {
		build.Critical("WARN: stuckHealth out of bounds. Max value, Min value, stuckHealth found", worstHealth, 0, stuckHealth)
		stuckHealth = worstHealth
	}
	// Sanity Check that the number of stuck chunks makes sense
	if numStuckChunks != sf.staticMetadata.NumStuckChunks {
		build.Critical("WARN: the number of stuck chunks found does not match metadata", numStuckChunks, sf.staticMetadata.NumStuckChunks)
	}
	return health, stuckHealth, numStuckChunks
}

// HostPublicKeys returns all the public keys of hosts the file has ever been
// uploaded to. That means some of those hosts might no longer be in use.
func (sf *SiaFile) HostPublicKeys() (spks []types.SiaPublicKey) {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	// Only return the keys, not the whole entry.
	keys := make([]types.SiaPublicKey, 0, len(sf.pubKeyTable))
	for _, key := range sf.pubKeyTable {
		keys = append(keys, key.PublicKey)
	}
	return keys
}

// MarkAllUnhealthyChunksAsStuck marks all unhealthy chunks as stuck in the
// siafile
func (sf *SiaFile) MarkAllUnhealthyChunksAsStuck(offline map[string]bool, goodForRenew map[string]bool) (err error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	// If the file has been deleted we can't mark a chunk as stuck.
	if sf.deleted {
		return errors.New("can't call SetStuck on deleted file")
	}
	var updates []writeaheadlog.Update
	for chunkIndex := range sf.fullChunks {
		// Check if chunk is already stuck
		if sf.fullChunks[chunkIndex].Stuck {
			continue
		}
		// Check health of chunk
		chunkHealth := sf.chunkHealth(chunkIndex, offline, goodForRenew)
		// If chunk is healthy then we don't need to mark it as stuck
		if chunkHealth < RemoteRepairDownloadThreshold {
			continue
		}
		// In case an error happens we need to revert the changes we are going
		// to make.
		defer func() {
			if err != nil {
				sf.fullChunks[chunkIndex].Stuck = false
				sf.staticMetadata.NumStuckChunks--
			}
		}()
		// Update chunk and NumStuckChunks in siafile metadata
		sf.fullChunks[chunkIndex].Stuck = true
		sf.staticMetadata.NumStuckChunks++
		// Create chunk update
		update := sf.saveChunkUpdate(chunkIndex)
		updates = append(updates, update)
	}
	// Set partial chunk too if necessary.
	if sf.staticMetadata.CombinedChunkStatus > combinedChunkStatusNoChunk {
		chunkHealth := sf.partialsSiaFile.ChunkHealth(int(sf.staticMetadata.CombinedChunkIndex), offline, goodForRenew)
		if chunkHealth >= RemoteRepairDownloadThreshold {
			if err := sf.partialsSiaFile.SetStuck(sf.staticMetadata.CombinedChunkIndex, true); err != nil {
				return err
			}
		}
	}
	// Create metadata update and apply updates on disk
	metadataUpdates, err := sf.saveMetadataUpdates()
	if err != nil {
		return err
	}
	updates = append(updates, metadataUpdates...)
	return sf.createAndApplyTransaction(updates...)
}

// NumChunks returns the number of chunks the file consists of. This will
// return the number of chunks the file consists of even if the file is not
// fully uploaded yet.
func (sf *SiaFile) NumChunks() uint64 {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.numChunks()
}

// Pieces returns all the pieces for a chunk in a slice of slices that contains
// all the pieces for a certain index.
func (sf *SiaFile) Pieces(chunkIndex uint64) ([][]Piece, error) {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	allChunks := sf.allChunks()
	if chunkIndex >= sf.numChunks() {
		err := fmt.Errorf("index %v out of bounds (%v)", chunkIndex, allChunks)
		build.Critical(err)
		return nil, err
	}
	// TODO: handle partial chunk

	// Return a deep-copy to avoid race conditions.
	pieces := make([][]Piece, len(allChunks[chunkIndex].Pieces))
	for pieceIndex := range pieces {
		pieces[pieceIndex] = make([]Piece, len(allChunks[chunkIndex].Pieces[pieceIndex]))
		for i, piece := range allChunks[chunkIndex].Pieces[pieceIndex] {
			pieces[pieceIndex][i] = Piece{
				HostPubKey: sf.hostKey(piece.HostTableOffset).PublicKey,
				MerkleRoot: piece.MerkleRoot,
			}
		}
	}
	return pieces, nil
}

// Redundancy returns the redundancy of the least redundant chunk. A file
// becomes available when this redundancy is >= 1. Assumes that every piece is
// unique within a file contract. -1 is returned if the file has size 0. It
// takes two arguments, a map of offline contracts for this file and a map that
// indicates if a contract is goodForRenew.
func (sf *SiaFile) Redundancy(offlineMap map[string]bool, goodForRenewMap map[string]bool) (r float64) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	// Update the cache.
	defer func() {
		sf.staticMetadata.CachedRedundancy = r
	}()
	allChunks := sf.allChunks()
	if sf.staticMetadata.FileSize == 0 {
		// TODO change this once tiny files are supported.
		if len(allChunks) != 1 {
			// should never happen
			return -1
		}
		ec := sf.staticMetadata.staticErasureCode
		return float64(ec.NumPieces()) / float64(ec.MinPieces())
	}

	minRedundancy := math.MaxFloat64
	minRedundancyNoRenew := math.MaxFloat64
	for chunkIndex := range allChunks {
		// Loop over chunks and remember how many unique pieces of the chunk
		// were goodForRenew and how many were not.
		numPiecesRenew, numPiecesNoRenew := sf.goodPieces(chunkIndex, offlineMap, goodForRenewMap)
		redundancy := float64(numPiecesRenew) / float64(sf.staticMetadata.staticErasureCode.MinPieces())
		if redundancy < minRedundancy {
			minRedundancy = redundancy
		}
		redundancyNoRenew := float64(numPiecesNoRenew) / float64(sf.staticMetadata.staticErasureCode.MinPieces())
		if redundancyNoRenew < minRedundancyNoRenew {
			minRedundancyNoRenew = redundancyNoRenew
		}
	}

	// If the redundancy is smaller than 1x we return the redundancy that
	// includes contracts that are not good for renewal. The reason for this is
	// a better user experience. If the renter operates correctly, redundancy
	// should never go above numPieces / minPieces and redundancyNoRenew should
	// never go below 1.
	if minRedundancy < 1 && minRedundancyNoRenew >= 1 {
		return 1
	} else if minRedundancy < 1 {
		return minRedundancyNoRenew
	}
	return minRedundancy
}

// SetAllStuck sets the Stuck field of all chunks to stuck.
func (sf *SiaFile) SetAllStuck(stuck bool) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()

	// If the file has been deleted we can't mark a chunk as stuck.
	if sf.deleted {
		return errors.New("can't call SetStuck on deleted file")
	}
	// Update all the Stuck field for each chunk.
	for chunkIndex := range sf.fullChunks {
		sf.fullChunks[chunkIndex].Stuck = stuck
	}
	if sf.staticMetadata.CombinedChunkStatus > combinedChunkStatusNoChunk {
		err := sf.partialsSiaFile.SetStuck(sf.staticMetadata.CombinedChunkIndex, stuck)
		if err != nil {
			return err
		}
	}
	// Update NumStuckChunks in siafile metadata
	if stuck {
		sf.staticMetadata.NumStuckChunks = sf.numChunks()
	} else {
		sf.staticMetadata.NumStuckChunks = 0
	}
	// Save the whole file atomically.
	return sf.saveFile()
}

// SetStuck sets the Stuck field of the chunk at the given index
func (sf *SiaFile) SetStuck(index uint64, stuck bool) (err error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()

	// Handle partial chunk.
	if index == sf.numChunks()-1 && sf.staticMetadata.CombinedChunkStatus > combinedChunkStatusNoChunk {
		return sf.partialsSiaFile.SetStuck(sf.staticMetadata.CombinedChunkIndex, stuck)
	}

	// If the file has been deleted we can't mark a chunk as stuck.
	if sf.deleted {
		return errors.New("can't call SetStuck on deleted file")
	}
	// Check for change
	if stuck == sf.fullChunks[index].Stuck {
		return nil
	}
	// Remember the currenct number of stuck chunks in case an error happens.
	nsc := sf.staticMetadata.NumStuckChunks
	s := sf.fullChunks[index].Stuck
	defer func() {
		if err != nil {
			sf.staticMetadata.NumStuckChunks = nsc
			sf.fullChunks[index].Stuck = s
		}
	}()
	// Update chunk and NumStuckChunks in siafile metadata
	sf.fullChunks[index].Stuck = stuck
	if stuck {
		sf.staticMetadata.NumStuckChunks++
	} else {
		sf.staticMetadata.NumStuckChunks--
	}
	// Update chunk and metadata on disk
	updates, err := sf.saveMetadataUpdates()
	if err != nil {
		return err
	}
	update := sf.saveChunkUpdate(int(index))
	updates = append(updates, update)
	return sf.createAndApplyTransaction(updates...)
}

// StuckChunkByIndex returns if the chunk at the index is marked as Stuck or not
func (sf *SiaFile) StuckChunkByIndex(index uint64) bool {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.allChunks()[index].Stuck
}

// UID returns a unique identifier for this file.
func (sf *SiaFile) UID() SiafileUID {
	return sf.staticMetadata.StaticUniqueID
}

// UpdateUsedHosts updates the 'Used' flag for the entries in the pubKeyTable
// of the SiaFile. The keys of all used hosts should be passed to the method
// and the SiaFile will update the flag for hosts it knows of to 'true' and set
// hosts which were not passed in to 'false'.
func (sf *SiaFile) UpdateUsedHosts(used []types.SiaPublicKey) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	// Can't update used hosts on deleted file.
	if sf.deleted {
		return errors.New("can't call UpdateUsedHosts on deleted file")
	}
	// Create a map of the used keys for faster lookups.
	usedMap := make(map[string]struct{})
	for _, key := range used {
		usedMap[key.String()] = struct{}{}
	}
	// Mark the entries in the table. If the entry exists 'Used' is true.
	// Otherwise it's 'false'.
	var unusedHosts uint
	for i, entry := range sf.pubKeyTable {
		_, used := usedMap[entry.PublicKey.String()]
		sf.pubKeyTable[i].Used = used
		if !used {
			unusedHosts++
		}
	}
	// Prune the pubKeyTable if necessary.
	pruned := false
	if unusedHosts > pubKeyTablePruneThreshold {
		sf.pruneHosts()
		pruned = true
	}
	// Save the header to disk.
	updates, err := sf.saveHeaderUpdates()
	if err != nil {
		return err
	}
	// If we pruned the hosts we also need to save the body.
	if pruned {
		chunkUpdates := sf.saveChunksUpdates()
		updates = append(updates, chunkUpdates...)
	}
	err = sf.createAndApplyTransaction(updates...)
	if err != nil {
		return err
	}
	// Also update used hosts for potential partial chunk.
	if sf.staticMetadata.CombinedChunkStatus > combinedChunkStatusNoChunk {
		return sf.partialsSiaFile.UpdateUsedHosts(used)
	}
	return nil
}

// defragChunk removes pieces which belong to bad hosts and if that wasn't
// enough to reduce the chunkSize below the maximum size, it will remove
// redundant pieces.
func (sf *SiaFile) defragChunk(chunk *chunk) {
	// Calculate how many pieces every pieceSet can contain.
	maxChunkSize := int64(sf.staticMetadata.StaticPagesPerChunk) * pageSize
	maxPieces := (maxChunkSize - marshaledChunkOverhead) / marshaledPieceSize
	maxPiecesPerSet := maxPieces / int64(len(chunk.Pieces))

	// Filter out pieces with unused hosts since we don't have contracts with
	// those anymore.
	for i, pieceSet := range chunk.Pieces {
		var newPieceSet []piece
		for _, piece := range pieceSet {
			if int64(len(newPieceSet)) == maxPiecesPerSet {
				break
			}
			if sf.hostKey(piece.HostTableOffset).Used {
				newPieceSet = append(newPieceSet, piece)
			}
		}
		chunk.Pieces[i] = newPieceSet
	}
}

// hostKey fetches a host's key from the map. It also checks an offset against
// the hostTable to make sure it's not out of bounds. If it is, build.Critical
// is called and to avoid a crash in production, dummy hosts are added.
func (sf *SiaFile) hostKey(offset uint32) HostPublicKey {
	// Add dummy hostkeys to the table in case of siafile corruption and mark
	// them as unused. The next time the table is pruned, the keys will be
	// removed which is fine. This doesn't fix heavy corruption and the file but
	// still be lost but it's better than crashing.
	if offset >= uint32(len(sf.pubKeyTable)) {
		// Causes tests to fail. The following for loop will try to fix the
		// corruption on release builds.
		build.Critical("piece.HostTableOffset >= len(sf.pubKeyTable)")
		for offset >= uint32(len(sf.pubKeyTable)) {
			sf.pubKeyTable = append(sf.pubKeyTable, HostPublicKey{Used: false})
		}
	}
	return sf.pubKeyTable[offset]
}

// pruneHosts prunes the unused hostkeys from the file, updates the
// HostTableOffset of the pieces and removes pieces which do no longer have a
// host.
func (sf *SiaFile) pruneHosts() {
	var prunedTable []HostPublicKey
	// Create a map to track how the indices of the hostkeys changed when being
	// pruned.
	offsetMap := make(map[uint32]uint32)
	for i := uint32(0); i < uint32(len(sf.pubKeyTable)); i++ {
		if sf.pubKeyTable[i].Used {
			prunedTable = append(prunedTable, sf.pubKeyTable[i])
			offsetMap[i] = uint32(len(prunedTable) - 1)
		}
	}
	sf.pubKeyTable = prunedTable
	// With this map we loop over all the chunks and pieces and update the ones
	// who got a new offset and remove the ones that no longer have one.
	for chunkIndex := range sf.fullChunks {
		for pieceIndex, pieceSet := range sf.fullChunks[chunkIndex].Pieces {
			var newPieceSet []piece
			for i, piece := range pieceSet {
				newOffset, exists := offsetMap[piece.HostTableOffset]
				if exists {
					pieceSet[i].HostTableOffset = newOffset
					newPieceSet = append(newPieceSet, pieceSet[i])
				}
			}
			sf.fullChunks[chunkIndex].Pieces[pieceIndex] = newPieceSet
		}
	}
}

// GoodPieces loops over the pieces of a chunk and tracks the number of unique
// pieces that are good for upload, meaning the host is online, and the number
// of unique pieces that are good for renew, meaning the contract is set to
// renew.
func (sf *SiaFile) GoodPieces(chunkIndex int, offlineMap map[string]bool, goodForRenewMap map[string]bool) (uint64, uint64) {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.goodPieces(chunkIndex, offlineMap, goodForRenewMap)
}

// goodPieces loops over the pieces of a chunk and tracks the number of unique
// pieces that are good for upload, meaning the host is online, and the number
// of unique pieces that are good for renew, meaning the contract is set to
// renew.
func (sf *SiaFile) goodPieces(chunkIndex int, offlineMap map[string]bool, goodForRenewMap map[string]bool) (uint64, uint64) {
	numPiecesGoodForRenew := uint64(0)
	numPiecesGoodForUpload := uint64(0)

	// Handle partial chunk.
	if chunkIndex == int(sf.numChunks()) && sf.staticMetadata.CombinedChunkStatus > combinedChunkStatusNoChunk {
		return sf.partialsSiaFile.GoodPieces(int(sf.staticMetadata.CombinedChunkIndex), offlineMap, goodForRenewMap)
	}

	for _, pieceSet := range sf.fullChunks[chunkIndex].Pieces {
		// Remember if we encountered a goodForRenew piece or a
		// !goodForRenew piece that was at least online.
		foundGoodForRenew := false
		foundOnline := false
		for _, piece := range pieceSet {
			offline, exists1 := offlineMap[sf.hostKey(piece.HostTableOffset).PublicKey.String()]
			goodForRenew, exists2 := goodForRenewMap[sf.hostKey(piece.HostTableOffset).PublicKey.String()]
			if exists1 != exists2 {
				build.Critical("contract can't be in one map but not in the other")
			}
			if !exists1 || offline {
				continue
			}
			// If we found a goodForRenew piece we can stop.
			if goodForRenew {
				foundGoodForRenew = true
				break
			}
			// Otherwise we continue since there might be other hosts with
			// the same piece that are goodForRenew. We still remember that
			// we found an online piece though.
			foundOnline = true
		}
		if foundGoodForRenew {
			numPiecesGoodForRenew++
			numPiecesGoodForUpload++
		} else if foundOnline {
			numPiecesGoodForUpload++
		}
	}
	return numPiecesGoodForRenew, numPiecesGoodForUpload
}

// numChunks returns the number of chunks the file consists of. This will
// return the number of chunks the file consists of even if the file is not
// fully uploaded yet.
func (sf *SiaFile) numChunks() uint64 {
	numChunks := uint64(len(sf.fullChunks))
	if sf.staticMetadata.CombinedChunkStatus > combinedChunkStatusNoChunk {
		numChunks++
	}
	return numChunks
}

// UploadProgressAndBytes is the exportee wrapped for uploadProgressAndBytes.
func (sf *SiaFile) UploadProgressAndBytes() (float64, uint64) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.uploadProgressAndBytes()
}

// Chunk returns the chunk of a SiaFile at a given index.
func (sf *SiaFile) Chunk(chunkIndex uint64) chunk {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.allChunks()[chunkIndex]
}

// allChunks returns the allChunks of the SiaFile, including a potential partial one.
func (sf *SiaFile) allChunks() []chunk {
	chunks := sf.allChunks()
	if sf.staticMetadata.CombinedChunkStatus > combinedChunkStatusNoChunk {
		chunks = append(chunks, sf.partialsSiaFile.Chunk(sf.staticMetadata.CombinedChunkIndex))
	}
	return chunks
}

// growNumChunks increases the number of chunks in the SiaFile to numChunks. If
// the file already contains >= numChunks chunks then GrowNumChunks is a no-op.
func (sf *SiaFile) growNumChunks(numChunks uint64) error {
	// Don't allow a SiaFile with a partial chunk to grow.
	if sf.staticMetadata.CombinedChunkIndex > combinedChunkStatusNoChunk {
		return errors.New("can't grow a siafile with a partial chunk (yet)")
	}
	// Check if we need to grow the file.
	if uint64(len(sf.fullChunks)) >= numChunks {
		// Handle edge case where file has 1 chunk but has a size of 0. When we grow
		// such a file to 1 chunk we want to increment the size to >0.
		sf.staticMetadata.FileSize = int64(sf.staticChunkSize() * uint64(len(sf.fullChunks)))
		return nil
	}
	// Update the chunks.
	var updates []writeaheadlog.Update
	for uint64(len(sf.fullChunks)) < numChunks {
		sf.fullChunks = append(sf.fullChunks, chunk{
			Pieces: make([][]piece, sf.staticMetadata.staticErasureCode.NumPieces()),
		})
		updates = append(updates, sf.saveChunkUpdate(len(sf.fullChunks)-1))
	}
	// Update the fileSize.
	sf.staticMetadata.FileSize = int64(sf.staticChunkSize() * uint64(len(sf.fullChunks)))
	mdu, err := sf.saveMetadataUpdates()
	if err != nil {
		return err
	}
	updates = append(updates, mdu...)
	// Update the filesize in the metadata.
	return sf.createAndApplyTransaction(updates...)
}

// setStuck sets the Stuck field of the chunk at the given index
func (sf *SiaFile) setStuck(index uint64, stuck bool) (err error) {
	// Handle partial chunk.
	if index == sf.numChunks()-1 && sf.staticMetadata.CombinedChunkStatus > combinedChunkStatusNoChunk {
		return sf.partialsSiaFile.SetStuck(sf.staticMetadata.CombinedChunkIndex, stuck)
	}

	// If the file has been deleted we can't mark a chunk as stuck.
	if sf.deleted {
		return errors.New("can't call SetStuck on deleted file")
	}
	// Check for change
	if stuck == sf.fullChunks[index].Stuck {
		return nil
	}
	// Remember the currenct number of stuck chunks in case an error happens.
	nsc := sf.staticMetadata.NumStuckChunks
	s := sf.fullChunks[index].Stuck
	defer func() {
		if err != nil {
			sf.staticMetadata.NumStuckChunks = nsc
			sf.fullChunks[index].Stuck = s
		}
	}()
	// Update chunk and NumStuckChunks in siafile metadata
	sf.fullChunks[index].Stuck = stuck
	if stuck {
		sf.staticMetadata.NumStuckChunks++
	} else {
		sf.staticMetadata.NumStuckChunks--
	}
	// Update chunk and metadata on disk
	updates, err := sf.saveMetadataUpdates()
	if err != nil {
		return err
	}
	update := sf.saveChunkUpdate(int(index))
	updates = append(updates, update)
	return sf.createAndApplyTransaction(updates...)
}

// uploadProgressAndBytes updates the CachedUploadProgress and
// CachedUploadedBytes fields to indicate what percentage of the file has been
// uploaded based on the unique pieces that have been uploaded and also how many
// bytes have been uploaded of that file in total. Note that a file may be
// Available long before UploadProgress reaches 100%.
func (sf *SiaFile) uploadProgressAndBytes() (float64, uint64) {
	if true {
		panic("not implemented yet")
	}
	_, uploaded := sf.uploadedBytes()
	if sf.staticMetadata.FileSize == 0 {
		// Update cache.
		sf.staticMetadata.CachedUploadProgress = 100
		return 100, uploaded
	}
	desired := uint64(sf.numChunks()) * modules.SectorSize * uint64(sf.staticMetadata.staticErasureCode.NumPieces())
	// Update cache.
	sf.staticMetadata.CachedUploadProgress = math.Min(100*(float64(uploaded)/float64(desired)), 100)
	return sf.staticMetadata.CachedUploadProgress, uploaded
}

// uploadedBytes indicates how many bytes of the file have been uploaded via
// current file contracts in total as well as unique uploaded bytes. Note that
// this includes padding and redundancy, so uploadedBytes can return a value
// much larger than the file's original filesize.
func (sf *SiaFile) uploadedBytes() (uint64, uint64) {
	var total, unique uint64
	for _, chunk := range sf.allChunks() {
		for _, pieceSet := range chunk.Pieces {
			// Move onto the next pieceSet if nothing has been uploaded yet
			if len(pieceSet) == 0 {
				continue
			}

			// Note: we need to multiply by SectorSize here instead of
			// f.pieceSize because the actual bytes uploaded include overhead
			// from TwoFish encryption
			//
			// Sum the total bytes uploaded
			total += uint64(len(pieceSet)) * modules.SectorSize
			// Sum the unique bytes uploaded
			unique += modules.SectorSize
		}
	}
	// Update cache.
	sf.staticMetadata.CachedUploadedBytes = total
	return total, unique
}
