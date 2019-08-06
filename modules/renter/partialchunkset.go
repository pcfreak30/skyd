package renter

// TODO: ??? Combined chunks need to be deleted once they have reached full redundancy
// TODO: Support repairing a partial chunk if redundancy dropped below 1x, the
// complete chunk was deleted but the source is available locally
// TODO: how to figure out which combined chunks are no longer useful?
// TODO: how to prune the mega files?
// TODO: force snapshots not to use partial uploads
// TODO: make sure we don't push the same combined chunk into the repair heap multiple times in parallel
// TODO: merge incomplete chunks on recovery
// TODO: siafile batching

import (
	"archive/tar"
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
)

// splitCombinedChunkName splits the name of a combined chunk into the erasure
// code identifier and combined chunk id.
func splitCombinedChunkName(name string) (modules.ErasureCoderIdentifier, modules.CombinedChunkID, error) {
	name = strings.TrimSuffix(name, modules.UnfinishedChunkExtension)
	name = strings.TrimSuffix(name, modules.CombinedChunkExtension)
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
		if filepath.Ext(info.Name()) != modules.UnfinishedChunkExtension {
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
	path := filepath.Join(pcs.combinedChunkRoot, combinedChunkName(ec, chunkID)+modules.CombinedChunkExtension)
	if unfinished {
		path += modules.UnfinishedChunkExtension
	}
	return path
}

// combinedChunkMDPath returns the path for a given combined chunk's metadata
// given its ID and erasure coder.
func (pcs *partialChunkSet) combinedChunkMDPath(chunkID modules.CombinedChunkID, ec modules.ErasureCoder) string {
	return filepath.Join(pcs.combinedChunkRoot, combinedChunkName(ec, chunkID)) + modules.ChunkMetadataExtension
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

// UntarCombinedChunk untars a combined chunk or combined chunk related metadata
// files.
func (pcs *partialChunkSet) UntarCombinedChunk(b []byte, relPath string) error {
	pcs.mu.Lock()
	defer pcs.mu.Unlock()
	// Sanity check file extension.
	if !pcs.isPartialChunkSetFileExtension(relPath) {
		return errors.New("unknown file extension")
	}
	// Open the file.
	dst := filepath.Join(pcs.combinedChunkRoot, relPath)
	// Trim the "unfinished" suffix from the dst. Imported unfinished chunks become
	// finished chunks.
	dst = strings.TrimSuffix(dst, modules.UnfinishedChunkExtension)
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return nil // importing in same renter
	} else if err != nil {
		return err
	}
	// Write file.
	_, err = f.Write(b)
	if err != nil {
		return errors.Compose(err, f.Close())
	}
	// Close file again.
	if err := f.Close(); err != nil {
		return err
	}
	// If the file was an unfinished combined chunk and we don't have one yet for that
	return nil
}

// TarCombinedChunks adds the combined chunks and their metadata to a tar
// archive.
func (pcs *partialChunkSet) TarCombinedChunks(tw *tar.Writer) error {
	pcs.mu.Lock()
	defer pcs.mu.Unlock()
	// Walk over all the partials siafiles and add them to the tarball.
	return filepath.Walk(pcs.combinedChunkRoot, func(path string, info os.FileInfo, err error) error {
		// This error is non-nil if filepath.Walk couldn't stat a file or
		// folder.
		if err != nil {
			return err
		}
		// Nothing to do for files that are not partial chunk set related.
		if !pcs.isPartialChunkSetFileExtension(path) {
			return nil
		}
		// Create the header for the file/dir.
		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}
		relPath := strings.TrimPrefix(path, pcs.combinedChunkRoot)
		header.Name = relPath
		// Open the file.
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		// Update the size of the file within the header since it might have changed
		// while we weren't holding the lock.
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		header.Size = fi.Size()
		// Write the header.
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		// Add the file to the archive.
		_, err = io.Copy(tw, f)
		return err
	})
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
			return nil, errors.AddContext(err, "failed to open combined chunk")
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

// isPartialChunkSetFileExtension returns true if the provided path points to a
// file related to the partial chunk set.
func (pcs *partialChunkSet) isPartialChunkSetFileExtension(path string) bool {
	return filepath.Ext(path) == modules.CombinedChunkExtension || filepath.Ext(path) == modules.UnfinishedChunkExtension ||
		filepath.Ext(path) == modules.ChunkMetadataExtension
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
	// If the partial chunk is not yet part of a combined chunk regardless of
	// whether it's a complete or incomplete one an error is returned.
	if !chunk.fileEntry.IsCompletePartialChunk(chunk.index) {
		return false, errors.New("can't fetch logical combined chunk for an incomplete partial chunk")
	}
	// Get the correct chunkID.
	var chunkID modules.CombinedChunkID
	chunkIDs := chunk.fileEntry.CombinedChunkIDs()
	switch len(chunkIDs) {
	case 1:
		chunkID = chunkIDs[0]
	case 2:
		chunkID = chunkIDs[chunk.fileEntry.NumChunks()-chunk.index-2]
	default:
		return false, fmt.Errorf("invalid number of chunkIDs: '%v'", len(chunkIDs))
	}
	// If the chunk is incomplete there is nothing we can do right now.
	if pcs.isUnfinished(chunkID, chunk.fileEntry.ErasureCode()) {
		return false, nil
	}
	// If it is complete we load it and add it to the chunk.
	f, err := os.Open(pcs.combinedChunkPath(chunkID, chunk.fileEntry.ErasureCode(), false))
	if err != nil {
		return false, errors.AddContext(err, "failed to open combined chunk file")
	}
	defer f.Close()
	// If the chunk is complete and the siafile's status hasn't been updated yet do
	// it now.
	// TODO: this isn't quite true. Some partial chunks consist of 2 combined
	// chunks which might not both be complete. We probably should only set this if
	// both are complete and tracked by the renter.
	if chunk.fileEntry.CombinedChunkStatus() < siafile.CombinedChunkStatusCompleted {
		if err := chunk.fileEntry.SetChunkStatusCompleted(); err != nil {
			return false, err
		}
	}
	// Read the combined chunk.
	_, err = chunk.readLogicalData(f)
	return true, err
}
