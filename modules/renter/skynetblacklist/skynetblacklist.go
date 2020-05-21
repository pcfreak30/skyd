package skynetblacklist

import (
	"bytes"
	"fmt"
	"io"
	"sync"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

const (
	// persistFile is the name of the persist file.
	persistFile string = "skynetblacklist"

	// persistSize is the size of a persisted merkleroot in the blacklist. It is
	// the length of `merkleroot` plus the `listed` flag (32 + 1).
	persistSize uint64 = 33
)

var (
	// ErrDuplicateAddition is the error indicating a duplicate addition.
	ErrDuplicateAddition = errors.New("duplicate addition")

	// ErrNotExist is the error indicating a skylink being removed does
	// not already exist.
	ErrNotExist = errors.New("skylink does not exist")

	// metadataHeader is the header of the metadata for the persist file
	metadataHeader = types.NewSpecifier("SkynetBlacklist\n")

	// metadataVersion is the version of the persistence file
	metadataVersion = types.NewSpecifier("v1.4.3\n")
)

type (
	// SkynetBlacklist manages a set of blacklisted skylinks by tracking the
	// merkleroots and persists the list to disk.
	SkynetBlacklist struct {
		staticAop *persist.AppendOnlyPersist

		// merkleRoots is a set of blacklisted links.
		merkleRoots map[crypto.Hash]struct{}

		mu sync.Mutex
	}

	// persistEntry contains a Skynet blacklist link and whether it should be
	// listed as being in the persistence file.
	persistEntry struct {
		MerkleRoot crypto.Hash
		Listed     bool
	}
)

// New returns an initialized SkynetBlacklist.
func New(persistDir string) (*SkynetBlacklist, error) {
	// Initialize the persistence of the blacklist.
	aop, reader, err := persist.NewAppendOnlyPersist(persistDir, persistFile, metadataHeader, metadataVersion)
	if err != nil {
		return nil, errors.AddContext(err, fmt.Sprintf("unable to initialize the skynet blacklist persistence at '%v'", aop.FilePath()))
	}

	sb := &SkynetBlacklist{
		staticAop: aop,
	}
	blacklist, err := unmarshalObjects(reader)
	if err != nil {
		return nil, errors.AddContext(err, "unable to unmarshal persist objects")
	}
	sb.merkleRoots = blacklist

	return sb, nil
}

// Blacklist returns the merkleroots that are blacklisted
func (sb *SkynetBlacklist) Blacklist() []crypto.Hash {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	var blacklist []crypto.Hash
	for mr := range sb.merkleRoots {
		blacklist = append(blacklist, mr)
	}
	return blacklist
}

// Close closes and frees associated resources.
func (sb *SkynetBlacklist) Close() error {
	return sb.staticAop.Close()
}

// FilePath returns the filepath of the persistence.
func (sb *SkynetBlacklist) FilePath() string {
	return sb.staticAop.FilePath()
}

// IsBlacklisted indicates if a skylink is currently blacklisted
func (sb *SkynetBlacklist) IsBlacklisted(skylink modules.Skylink) bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	_, ok := sb.merkleRoots[skylink.MerkleRoot()]
	return ok
}

// UpdateBlacklist updates the list of skylinks that are blacklisted.
func (sb *SkynetBlacklist) UpdateBlacklist(additions, removals []modules.Skylink) error {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	// Validate now before we start making changes.
	err := sb.validateChanges(additions, removals)
	if err != nil {
		return errors.AddContext(err, "could not validate the changes to the blacklist")
	}

	buf, err := sb.marshalObjects(additions, removals)
	if err != nil {
		return errors.AddContext(err, "unable to marshal additions and removals")
	}
	_, err = sb.staticAop.Write(buf.Bytes())
	return errors.AddContext(err, "unable to write to persistence")
}

// marshalObjects marshals the given objects into a byte buffer.
//
// NOTE: this method does not check for duplicate additions or inexistent
// removals. We assume the input has been validated.
func (sb *SkynetBlacklist) marshalObjects(additions, removals []modules.Skylink) (bytes.Buffer, error) {
	// Create buffer for encoder
	var buf bytes.Buffer
	// Create and encode the persist links
	listed := true
	for _, skylink := range additions {
		// Add skylink merkleroot to map
		mr := skylink.MerkleRoot()
		sb.merkleRoots[mr] = struct{}{}

		// Marshal the update
		pe := persistEntry{mr, listed}
		bytes := encoding.Marshal(pe)
		buf.Write(bytes)
	}
	listed = false
	for _, skylink := range removals {
		// Remove skylink merkleroot from map
		mr := skylink.MerkleRoot()
		delete(sb.merkleRoots, mr)

		// Marshal the update
		pe := persistEntry{mr, listed}
		bytes := encoding.Marshal(pe)
		buf.Write(bytes)
	}

	return buf, nil
}

// unmarshalObjects unmarshals the sia encoded objects.
func unmarshalObjects(reader io.Reader) (map[crypto.Hash]struct{}, error) {
	blacklist := make(map[crypto.Hash]struct{})
	// Unmarshal blacklisted links one by one until EOF.
	var offset uint64
	for {
		buf := make([]byte, persistSize)
		_, err := io.ReadFull(reader, buf)
		if errors.Contains(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		var pe persistEntry
		err = encoding.Unmarshal(buf, &pe)
		if err != nil {
			return nil, err
		}
		offset += persistSize

		if !pe.Listed {
			delete(blacklist, pe.MerkleRoot)
			continue
		}
		blacklist[pe.MerkleRoot] = struct{}{}
	}
	return blacklist, nil
}

// validateChanges validates the changes to be made to the Skynet blacklist.
func (sb *SkynetBlacklist) validateChanges(additions, removals []modules.Skylink) error {
	// Check for nil input.
	if len(additions)+len(removals) == 0 {
		return errors.New("no skylinks being added or removed")
	}

	// Check additions.
	seenAdditions := make(map[crypto.Hash]struct{})
	for _, addition := range additions {
		mr := addition.MerkleRoot()
		// Don't allow duplicate additions.
		if _, exists := sb.merkleRoots[mr]; exists {
			return errors.AddContext(ErrDuplicateAddition, fmt.Sprintf("skylink %s already exists", addition))
		}
		// Check for duplicate links within the ones being added.
		if _, exists := seenAdditions[mr]; exists {
			return errors.AddContext(ErrDuplicateAddition, fmt.Sprintf("skylink %s is being added twice", addition))
		}
		seenAdditions[mr] = struct{}{}
	}
	// Check removals. Each skylink must already exist in the list.
	for _, removal := range removals {
		if _, exists := sb.merkleRoots[removal.MerkleRoot()]; !exists {
			return errors.AddContext(ErrNotExist, fmt.Sprintf("skylink %s not found", removal))
		}
	}
	return nil
}
