package siafile

import (
	"path/filepath"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/writeaheadlog"
)

// Merge merges two PartialsSiafiles into one, returning a map which translates
// chunk indices in newFile to indices in sf.
func (sf *SiaFile) Merge(newFile *SiaFile) (map[uint64]uint64, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.merge(newFile)
}

// addCombinedChunk adds a new combined chunk to a combined Siafile. This can't
// be called on a regular SiaFile.
func (sf *SiaFile) addCombinedChunk() (uint64, []writeaheadlog.Update, error) {
	if filepath.Ext(sf.siaFilePath) != modules.PartialsSiaFileExtension {
		return 0, nil, errors.New("can only call addCombinedChunk on combined SiaFiles")
	}
	// Create updates to add a chunk and return index of that new chunk.
	numChunks := sf.numChunks()
	updates, err := sf.growNumChunks(numChunks + 1)
	return numChunks, updates, err
}

// merge merges two PartialsSiafiles into one, returning a map which translates
// chunk indices in newFile to indices in sf.
func (sf *SiaFile) merge(newFile *SiaFile) (map[uint64]uint64, error) {
	if filepath.Ext(sf.siaFilePath) != modules.PartialsSiaFileExtension {
		return nil, errors.New("can only call merge on PartialsSiaFile")
	}
	if filepath.Ext(newFile.SiaFilePath()) != modules.PartialsSiaFileExtension {
		return nil, errors.New("can only merge PartialsSiafiles into a PartialsSiaFile")
	}
	newFile.mu.Lock()
	defer newFile.mu.Unlock()
	indexMap := make(map[uint64]uint64)
	for chunkIndex, chunk := range newFile.fullChunks {
		newIndex := uint64(len(sf.fullChunks))
		sf.fullChunks = append(sf.fullChunks, chunk)
		indexMap[uint64(chunkIndex)] = newIndex
	}
	return indexMap, sf.saveFile()
}
