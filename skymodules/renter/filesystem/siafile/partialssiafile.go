package siafile

import (
	"path/filepath"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

// Merge merges two PartialsSiafiles into one, returning a map which translates
// chunk indices in newFile to indices in sf.
func (sf *SiaFile) Merge(newFile *SiaFile) (map[uint64]uint64, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.merge(newFile)
}

// managedIndexMap creates and indexMap and a list of newChunks for merging
// siafiles.
func (sf *SiaFile) managedIndexMap(numChunks int) (map[uint64]uint64, []chunk, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if sf.deleted {
		return nil, nil, ErrDeleted
	}
	var newChunks []chunk
	indexMap := make(map[uint64]uint64)
	err := sf.iterateChunksReadonly(func(chunk chunk) error {
		newIndex := numChunks
		indexMap[uint64(chunk.Index)] = uint64(newIndex)
		chunk.Index = newIndex
		newChunks = append(newChunks, chunk)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return indexMap, newChunks, nil
}

// merge merges two PartialsSiafiles into one, returning a map which translates
// chunk indices in newFile to indices in sf.
func (sf *SiaFile) merge(newFile *SiaFile) (map[uint64]uint64, error) {
	if sf.deleted {
		return nil, errors.New("can't merge into deleted file")
	}
	if filepath.Ext(sf.siaFilePath) != skymodules.PartialsSiaFileExtension {
		return nil, errors.New("can only call merge on PartialsSiaFile")
	}
	if filepath.Ext(newFile.SiaFilePath()) != skymodules.PartialsSiaFileExtension {
		return nil, errors.New("can only merge PartialsSiafiles into a PartialsSiaFile")
	}
	indexMap, newChunks, err := newFile.managedIndexMap(sf.numChunks)
	if err != nil {
		return nil, err
	}
	return indexMap, sf.saveFile(newChunks)
}
