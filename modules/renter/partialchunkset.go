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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gitlab.com/NebulousLabs/fastrand"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/renter/siafile"
)

type (
	combinedChunkID string

	// partialChunkSet is a set used by the repair code to combine partial chunks
	// into full chunks. Chunks won't be combined right away but instead the set
	// waits for enough requests to build the best chunk and only then creates the
	// full chunk on disk.
	// NOTE: Currently the implementation assumes that every file will have at most
	// 1 partial chunk and that's the chunk at the end.
	partialChunkSet struct {
		mu                      sync.Mutex
		combinedChunkRoot       string
		unfinishedCombinedChunk map[modules.ErasureCoderIdentifier]combinedChunkID
	}
)

var (
	combinedChunkNameSeparator = "-"
	unfinishedChunkExtension   = ".unfinished"
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
	ucc := make(map[modules.ErasureCoderIdentifier]combinedChunkID)
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
		chunkID := combinedChunkID(strings.TrimSuffix(s[1], unfinishedChunkExtension))
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

// combinedChunkName returns the filename of a combined chunk.
func combinedChunkName(ec modules.ErasureCoder, chunkID combinedChunkID) string {
	return fmt.Sprintf("%v%v%v", ec.Identifier(), combinedChunkNameSeparator, chunkID)
}

// randomChunkID generates a new combinedChunkID.
func randomChunkID() combinedChunkID {
	return combinedChunkID(hex.EncodeToString(fastrand.Bytes(16)))
}

func (pcs *partialChunkSet) SavePartialChunk(sf *siafile.SiaFile, partialChunk []byte) error {
	// TODO: create update to append partial chunk to combined chunk
	// TODO:
	panic("not implemented yet")
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
