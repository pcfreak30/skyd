package renter

// TODO: ??? Combined chunks need to be deleted once they have reached full redundancy
// TODO: Support repairing a partial chunk if redundancy dropped below 1x, the
// complete chunk was deleted but the source is available locally
// TODO: how to figure out which combined chunks are no longer useful?
// TODO: how to prune the mega files?
// TODO: force snapshots not to use partial uploads
// TODO: make sure we don't push the same combined chunk into the repair heap multiple times in parallel
// TODO: backup combined chunks

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gitlab.com/NebulousLabs/writeaheadlog"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/renter/siafile"
)

type (
	// partialChunkSet is a set used by the repair code to combine partial chunks
	// into full chunks. Chunks won't be combined right away but instead the set
	// waits for enough requests to build the best chunk and only then creates the
	// full chunk on disk.
	// NOTE: Currently the implementation assumes that every file will have at most
	// 1 partial chunk and that's the chunk at the end.
	partialChunkSet struct {
		mu                      sync.Mutex
		combinedChunkRoot       string
		r                       *Renter
		unfinishedCombinedChunk map[modules.ErasureCoderIdentifier]modules.CombinedChunkID
	}

	dependentFile struct {
		UID     siafile.SiafileUID `json:"uid"`
		SiaPath modules.SiaPath    `json:"siapath"`
	}

	combinedChunkMetadata struct {
		SiaFiles []dependentFile `json:"siafiles"`
	}
)

var (
	combinedChunkNameSeparator = "-"
	combinedChunkExtension     = ".cc"
	unfinishedChunkExtension   = ".unfinished"
	chunkMetadataExtension     = ".ccmd"
)

// splitCombinedChunkName splits the name of a combined chunk into the erasure
// code identifier and combined chunk id.
func splitCombinedChunkName(name string) (modules.ErasureCoderIdentifier, modules.CombinedChunkID, error) {
	name = strings.TrimSuffix(name, unfinishedChunkExtension)
	name = strings.TrimSuffix(name, combinedChunkExtension)
	split := strings.Split(name, combinedChunkNameSeparator)
	if len(split) != 2 {
		build.Critical("filename should be split into exactly 2 halves but was", len(split))
		return "", "", fmt.Errorf("filename should be split into exactly 2 halves but was %v", split)
	}
	return modules.ErasureCoderIdentifier(split[0]), modules.CombinedChunkID(split[1]), nil
}

// newPartialChunkSet creates a partial chunk set ready to combine partial
// chunks.
func newPartialChunkSet(combinedChunkRoot string) (*partialChunkSet, error) {
	// Create the root dir.
	err := os.MkdirAll(combinedChunkRoot, 0600)
	if err != nil {
		return nil, err
	}
	// Search for unfinished combined chunks.
	ucc := make(map[modules.ErasureCoderIdentifier]modules.CombinedChunkID)
	err = filepath.Walk(combinedChunkRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if filepath.Ext(info.Name()) != unfinishedChunkExtension {
			return nil
		}
		// Get the erasure code identifier and chunkID from the filename.
		ecIdentifier, chunkID, err := splitCombinedChunkName(info.Name())
		if err != nil {
			return err
		}
		// Check for conflicts.
		if conflictingID, exists := ucc[ecIdentifier]; exists {
			return fmt.Errorf("found multiple unfinished chunks for the same erasure coding: '%v' '%v'",
				chunkID, conflictingID)
		}
		ucc[ecIdentifier] = chunkID
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &partialChunkSet{
		combinedChunkRoot:       combinedChunkRoot,
		unfinishedCombinedChunk: ucc,
	}, nil
}

// combinedChunkPath returns the path for a given finished or unfinished
// combined chunk given its ID and erasure coder.
func (pcs *partialChunkSet) combinedChunkPath(chunkID modules.CombinedChunkID, ec modules.ErasureCoder, unfinished bool) string {
	path := filepath.Join(pcs.combinedChunkRoot, combinedChunkName(ec, chunkID))
	if unfinished {
		path += unfinishedChunkExtension
	}
	return path
}

// combinedChunkMDPath returns the path for a given combined chunk's metadata
// given its ID and erasure coder.
func (pcs *partialChunkSet) combinedChunkMDPath(chunkID modules.CombinedChunkID, ec modules.ErasureCoder) string {
	return filepath.Join(pcs.combinedChunkRoot, combinedChunkName(ec, chunkID)) + chunkMetadataExtension
}

// combinedChunkName returns the filename of a combined chunk.
func combinedChunkName(ec modules.ErasureCoder, chunkID modules.CombinedChunkID) string {
	return fmt.Sprintf("%v%v%v%v", ec.Identifier(), combinedChunkNameSeparator, chunkID, combinedChunkExtension)
}

// loadChunkMetadata loads the metadata for a combined chunk given its ID and
// erasure coder.
func (pcs *partialChunkSet) loadChunkMetadata(chunkID modules.CombinedChunkID, ec modules.ErasureCoder) (combinedChunkMetadata, error) {
	// Open file.
	f, err := os.Open(pcs.combinedChunkMDPath(chunkID, ec))
	if err != nil {
		return combinedChunkMetadata{}, err
	}
	defer f.Close()
	// Decode metadata.
	var md combinedChunkMetadata
	dec := json.NewDecoder(f)
	err = dec.Decode(&md)
	return md, err
}

// saveChunkMetadata saves the metadata for a combined chunk given its ID and
// erasure coder.
func (pcs *partialChunkSet) saveCombinedChunkMetadata(chunkID modules.CombinedChunkID, ec modules.ErasureCoder, md combinedChunkMetadata) error {
	mdBytes, err := json.Marshal(combinedChunkMetadata{})
	if err != nil {
		return err
	}
	mdPath := pcs.combinedChunkMDPath(chunkID, ec)
	mdUpdate := writeaheadlog.WriteAtUpdate(mdPath, 0, mdBytes)
	truncateUpdate := writeaheadlog.TruncateUpdate(mdPath, int64(len(mdBytes)))
	return writeaheadlog.ApplyUpdates(mdUpdate, truncateUpdate)
}

// newUnfinishedCombinedChunk atomically creates a new unfinished chunk and its
// corresponding metadata.
func (pcs *partialChunkSet) newUnfinishedCombinedChunk(ec modules.ErasureCoder) (modules.CombinedChunkID, []writeaheadlog.Update, error) {
	ccid := randomChunkID()
	chunkPath := pcs.combinedChunkPath(ccid, ec, true)
	mdPath := pcs.combinedChunkMDPath(ccid, ec)
	mdBytes, err := json.Marshal(combinedChunkMetadata{})
	if err != nil {
		return "", nil, err
	}
	chunkUpdate := writeaheadlog.WriteAtUpdate(chunkPath, 0, []byte{})
	mdUpdate := writeaheadlog.WriteAtUpdate(mdPath, 0, mdBytes)
	return ccid, []writeaheadlog.Update{chunkUpdate, mdUpdate}, nil
}

// randomChunkID generates a new combinedChunkID.
func randomChunkID() modules.CombinedChunkID {
	return modules.CombinedChunkID(hex.EncodeToString(fastrand.Bytes(16)))
}

// appendToIncompleteChunk will append as much data of partialChunk to the
// incomplete chunk as possible and return how many bytes were apended. If the
// chunk gets filled in the process, the chunk will be renamed accordingly. None
// of the changes are written to disk right away. Instead the corresponding
// writeaheadlog updates are returned.
func (pcs *partialChunkSet) appendToIncompleteChunk(chunkID modules.CombinedChunkID, ec modules.ErasureCoder, partialChunk []byte, chunkSize int64) (int, int64, []writeaheadlog.Update, error) {
	// Figure out how much data we can write to the incomplete chunk.
	var updates []writeaheadlog.Update
	chunkPath := pcs.combinedChunkPath(chunkID, ec, true)
	maxLength := int(chunkSize)
	offset := int64(0)
	fi, err := os.Stat(chunkPath)
	if err == nil {
		maxLength = int(chunkSize - fi.Size())
		offset = fi.Size()
	} else if !os.IsNotExist(err) {
		return 0, 0, updates, errors.AddContext(err, "failed to determine size of incomplete combined chunk")
	}
	// Write as much data as possible to the incomplete chunk. If we need to split
	// the partial chunk over 2 combined chunks, we also need to rename the chunk
	// that turned from incomplete to complete.
	length := len(partialChunk)
	if length > maxLength {
		length = maxLength
		// Rename the existing incomplete chunk since it's going to be filled.
		b, err := ioutil.ReadFile(chunkPath)
		if err != nil {
			return 0, 0, updates, fmt.Errorf("failed to read incomplete chunk for renaming: %v", err)
		}
		updates = append(updates, writeaheadlog.DeleteUpdate(chunkPath))
		chunkPath = pcs.combinedChunkPath(chunkID, ec, false)
		updates = append(updates, writeaheadlog.WriteAtUpdate(chunkPath, 0, b))
	}
	updates = append(updates, writeaheadlog.WriteAtUpdate(chunkPath, offset, partialChunk[:length]))
	return int(length), offset, updates, nil
}

// SavePartialChunk saves a siafile's partial chunk within one or two unfinished
// combined chunks, updates the siafile's metadata and also the corresponding
// partials siafile's metadata.
func (pcs *partialChunkSet) SavePartialChunk(sf *siafile.SiaFile, partialChunk []byte) (err error) {
	pcs.mu.Lock()
	defer pcs.mu.Unlock()
	// Sanity check partial chunk size.
	if uint64(len(partialChunk)) >= sf.ChunkSize() || len(partialChunk) == 0 {
		return fmt.Errorf("can't call SavePartialChunk with a partial chunk >= chunkSize (%v >= %v) or 0",
			len(partialChunk), sf.ChunkSize())
	}

	// Check if there is an existing incomplete chunk that matches the erasure
	// coder of the sf. If there isn't, prepare a new one.
	var chunks []modules.CombinedChunk
	var updates []writeaheadlog.Update
	ec := sf.ErasureCode()
	ucid, exists := pcs.unfinishedCombinedChunk[ec.Identifier()]
	if !exists {
		// If no unfinished chunk exists, create a new one.
		var newChunkUpdates []writeaheadlog.Update
		ucid, newChunkUpdates, err = pcs.newUnfinishedCombinedChunk(ec)
		if err != nil {
			return errors.AddContext(err, "failed to create new unfinished combined chunk")
		}
		updates = append(updates, newChunkUpdates...)
		// Only add the new unfinished chunk to the partial chunk set in case of a
		// success.
		defer func() {
			if err == nil {
				pcs.unfinishedCombinedChunk[ec.Identifier()] = ucid
			}
		}()
	}
	// Remember the ID of the chunk and whether it already exists in the partials
	// SiaFile. The latter should be the case if the incomplete chunk already
	// existed.
	chunks = append(chunks, modules.CombinedChunk{
		ChunkID:          ucid,
		HasPartialsChunk: exists,
	})

	// Write as much data as possible to the incomplete chunk.
	n, offset, appendUpdates, err := pcs.appendToIncompleteChunk(ucid, ec, partialChunk, int64(sf.ChunkSize()))
	if err != nil {
		return err
	}
	updates = append(updates, appendUpdates...)
	remaining := len(partialChunk) - n
	// Sanity check that the remaining data fits within a chunk.
	if remaining > int(sf.ChunkSize()) {
		return fmt.Errorf("remaining data doesn't fit into chunk: %v > %v",
			remaining, sf.ChunkSize())
	}

	// If there is any remaining data, write it to a new chunk.
	if remaining > 0 {
		// Create a new chunk.
		ucid2, newChunkUpdates, err := pcs.newUnfinishedCombinedChunk(ec)
		if err != nil {
			return errors.AddContext(err, "failed to create new unfinished combined chunk")
		}
		// Remember its ID and updates.
		chunks = append(chunks, modules.CombinedChunk{
			ChunkID:          ucid2,
			HasPartialsChunk: false, // 'false' since it was just created
		})
		updates = append(updates, newChunkUpdates...)
		// Append the remaining data to the new chunk.
		_, _, appendUpdates, err := pcs.appendToIncompleteChunk(ucid2, ec, partialChunk[n:], int64(sf.ChunkSize()))
		if err != nil {
			return errors.AddContext(err, "failed to append second half of partial chunk to new incomplete combined chunk")
		}
		updates = append(updates, appendUpdates...)
		// Only add the new unfinished chunk to the partial chunk set in case of a
		// success.
		defer func() {
			if err == nil {
				pcs.unfinishedCombinedChunk[ec.Identifier()] = ucid2
			}
		}()
	}
	return sf.SetCombinedChunk(offset, int64(len(partialChunk)), chunks, updates)
}

// LoadPartialChunk loads a partial chunk from disk.
func (pcs *partialChunkSet) LoadPartialChunk(chunk *unfinishedDownloadChunk) ([]byte, error) {
	pcs.mu.Lock()
	defer pcs.mu.Unlock()
	snap := chunk.renterFile
	if _, isPartial := snap.IsCompletePartialChunk(uint64(chunk.staticChunkIndex)); !isPartial {
		return nil, errors.New("can only call LoadPartialChunk if partial chunk has been included in a combined chunk")
	}
	chunkIDs := snap.CombinedChunkIDs()
	chunkOffset := snap.CombinedChunkOffset()
	chunkLength := snap.CombinedChunkLength()
	ec := snap.ErasureCode()
	if len(chunkIDs) != 1 && len(chunkIDs) != 2 {
		return nil, errors.New("file should contain one or two indices")
	}
	partialChunk := make([]byte, chunkLength)
	remaining := chunkLength
	offset := chunkOffset
	for _, ci := range chunkIDs {
		path := pcs.combinedChunkPath(ci, ec, pcs.isUnfinished(ci, ec))
		f, err := os.Open(path)
		if err != nil {
			return nil, errors.New("failed to open combined chunk")
		}
		defer f.Close()
		n, err := f.ReadAt(partialChunk[chunkLength-remaining:], int64(offset))
		if err != nil && err != io.EOF {
			return nil, errors.New("failed to read partial chunk")
		}
		remaining -= uint64(n)
		offset = 0
	}
	if remaining != 0 {
		return nil, fmt.Errorf("expected 0 bytes to be remaining but was %v", remaining)
	}
	return partialChunk, nil
}

// isUnfinished returns true if the provided chunk id is an unfinished chunk.
// 'true' means that it is but 'false' doesn't imply that the chunk is
// completed. It might also be an invalid id.
func (pcs *partialChunkSet) isUnfinished(cid modules.CombinedChunkID, ec modules.ErasureCoder) bool {
	ucid, exists := pcs.unfinishedCombinedChunk[ec.Identifier()]
	return exists && cid == ucid
}

// FetchLogicalCombinedChunk fetches the logical data for an
// unfinishedUploadChunk from a combined chunk.
func (pcs *partialChunkSet) FetchLogicalCombinedChunk(chunk *unfinishedUploadChunk) (bool, error) {
	pcs.mu.Lock()
	defer pcs.mu.Unlock()
	return false, nil
}
