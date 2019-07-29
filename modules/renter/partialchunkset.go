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
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gitlab.com/NebulousLabs/writeaheadlog"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"

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
	unfinishedChunkExtension   = ".unfinished"
	chunkMetadataExtension     = ".md"
)

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
		s := strings.Split(info.Name(), combinedChunkNameSeparator)
		if len(s) != 2 {
			return fmt.Errorf("filename '%v' was split in more than 2 halves", info.Name())
		}
		ecIdentifier := modules.ErasureCoderIdentifier(s[0])
		chunkID := modules.CombinedChunkID(strings.TrimSuffix(s[1], unfinishedChunkExtension))
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
	return fmt.Sprintf("%v%v%v", ec.Identifier(), combinedChunkNameSeparator, chunkID)
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

func (pcs *partialChunkSet) appendToIncompleteChunk(chunkID modules.CombinedChunkID, ec modules.ErasureCoder, partialChunk []byte, chunkSize int64) (int, []writeaheadlog.Update, error) {
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
		return 0, updates, errors.AddContext(err, "failed to determine size of incomplete combined chunk")
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
			return 0, updates, fmt.Errorf("failed to read incomplete chunk for renaming: %v", err)
		}
		updates = append(updates, writeaheadlog.DeleteUpdate(chunkPath))
		chunkPath = pcs.combinedChunkPath(chunkID, ec, false)
		updates = append(updates, writeaheadlog.WriteAtUpdate(chunkPath, 0, b))
	}
	updates = append(updates, writeaheadlog.WriteAtUpdate(chunkPath, offset, partialChunk[:length]))
	return int(length), updates, nil
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
	var ucids []modules.CombinedChunkID
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
	// Remember the ID of the chunk.
	ucids = append(ucids, ucid)

	// Write as much data as possible to the incomplete chunk.
	n, appendUpdates, err := pcs.appendToIncompleteChunk(ucid, ec, partialChunk, sf.ChunkSize())
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
		ucids = append(ucids, ucid2)
		updates = append(updates, newChunkUpdates...)
		// Append the remaining data to the new chunk.
		_, appendUpdates, err := pcs.appendToIncompleteChunk(ucid2, ec, partialChunk[n:], int64(sf.ChunkSize()))
		if err != nil {
			return errors.AddContext(err, "failed to append second half of partial chunk to new incomplete combined chunk")
		}
		// Only add the new unfinished chunk to the partial chunk set in case of a
		// success.
		defer func() {
			if err == nil {
				pcs.unfinishedCombinedChunk[ec.Identifier()] = ucid2
			}
		}()
	}

	// Let the siafile know about the index, offset, length and chunk ids
	if true {
		panic("TODO: not impelemented yet")
	}
	// TODO: update both partials siafile and siafile + apply 'updates'
	return sf.SavePartialChunk(offset, length, ucids, 0, updates)
}

// FetchLogicalCombinedChunk fetches the logical data for an
// unfinishedUploadChunk. It does so by checking if enough partial chunks are
// waiting to be repaired and combining them. If not enough partial chunks exist
// at the moment, it remembers the request and returns 'false'.
func (pcs *partialChunkSet) FetchLogicalCombinedChunk(chunk *unfinishedUploadChunk) (bool, error) {
	pcs.mu.Lock()
	defer pcs.mu.Unlock()
	panic("todo not implemented yet")
}
