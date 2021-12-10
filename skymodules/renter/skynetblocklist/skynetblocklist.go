package skynetblocklist

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/encoding"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/persist"
	"go.sia.tech/siad/types"
)

const (
	// persistFile is the name of the persist file
	persistFile string = "skynetblocklist.dat"

	// persistSize is the size of a persisted merkleroot in the blocklist. It is
	// the length of `merkleroot` plus the int64 probationary period plus
	// the `listed` flag (32 + 8 + 1).
	persistSize uint64 = 41
)

var (
	// DefaultProbationaryPeriod is the default length in seconds of the
	// blocklist probationary period. During this time, skylinks will be
	// blocked but not deleted
	DefaultProbationaryPeriod = build.Select(build.Var{
		Standard: int64(30 * 24 * 60 * 60), // 30 days
		Dev:      int64(24 * 60 * 60),      // 1 day
		Testing:  int64(60),
	}).(int64)

	// metadataHeader is the header of the metadata for the persist file
	metadataHeader = types.NewSpecifier("SkynetBlocklist\n")

	// metadataVersion is the version of the persistence file
	metadataVersion = metadataVersionV1510
)

type (
	// SkynetBlocklist manages a set of blocked skylinks by tracking the
	// merkleroots and persists the list to disk.
	SkynetBlocklist struct {
		staticAop *persist.AppendOnlyPersist

		// hashes is a map of hashed blocked merkleroots to the uinx
		// timestamp of the end of their probationary period. During the
		// probationary period, the skylinks are blocked but the
		// underlying content is not deleted. This allows portal
		// operations time to validate blocklist requests to protect
		// against malicious blocklisting.
		hashes map[crypto.Hash]int64

		mu sync.Mutex
	}

	// persistEntry contains a hash and whether it should be listed as being in
	// the current blocklist.
	persistEntry struct {
		Hash                  crypto.Hash
		ProbationaryPeriodEnd int64
		Listed                bool
	}
)

// New returns an initialized SkynetBlocklist.
func New(persistDir string) (*SkynetBlocklist, error) {
	// Load the persistence of the blocklist.
	aop, reader, err := loadPersist(persistDir)
	if err != nil {
		return nil, errors.AddContext(err, "unable to load the skynet blocklist persistence")
	}

	sb := &SkynetBlocklist{
		staticAop: aop,
	}
	hashes, err := unmarshalObjects(reader)
	if err != nil {
		err = errors.Compose(err, aop.Close())
		return nil, errors.AddContext(err, "unable to unmarshal persist objects")
	}
	sb.hashes = hashes

	return sb, nil
}

// Blocklist returns the hashes of the merkleroots that are blocked
func (sb *SkynetBlocklist) Blocklist() []crypto.Hash {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	var blocklist []crypto.Hash
	for hash := range sb.hashes {
		blocklist = append(blocklist, hash)
	}
	return blocklist
}

// Close closes and frees associated resources.
func (sb *SkynetBlocklist) Close() error {
	return sb.staticAop.Close()
}

// IsBlocked indicates if a skylink is currently blocked and if it should be
// deleted.
func (sb *SkynetBlocklist) IsBlocked(skylink skymodules.Skylink) (bool, bool) {
	if !skylink.IsSkylinkV1() {
		build.Critical("IsBlocked requires V1 skylink")
		return false, false
	}
	hash := crypto.HashObject(skylink.MerkleRoot())
	return sb.IsHashBlocked(hash)
}

// IsHashBlocked indicates if a hash is currently blocked and if it should be
// deleted.
func (sb *SkynetBlocklist) IsHashBlocked(hash crypto.Hash) (isBlocked bool, shouldDelete bool) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	probationaryPeriodEnd, ok := sb.hashes[hash]
	// If the hash exists it is blocked, and if the probationaryPeriod is in
	// the past we should delete the data.
	return ok, time.Now().Unix() >= probationaryPeriodEnd
}

// UpdateBlocklist updates the list of skylinks that are blocked.
func (sb *SkynetBlocklist) UpdateBlocklist(additions, removals []crypto.Hash, probationaryPeriod int64) error {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	buf, err := sb.marshalObjects(additions, removals, probationaryPeriod)
	if err != nil {
		return errors.AddContext(err, fmt.Sprintf("unable to update skynet blocklist persistence at '%v'", sb.staticAop.FilePath()))
	}
	_, err = sb.staticAop.Write(buf.Bytes())
	return errors.AddContext(err, fmt.Sprintf("unable to update skynet blocklist persistence at '%v'", sb.staticAop.FilePath()))
}

// marshalObjects marshals the given objects into a byte buffer.
func (sb *SkynetBlocklist) marshalObjects(additions, removals []crypto.Hash, probationaryPeriod int64) (bytes.Buffer, error) {
	// Create buffer for encoder
	var buf bytes.Buffer
	// Create and encode the persist links
	listed := true
	for _, hash := range additions {
		probationaryPeriodEnd := time.Now().Unix() + probationaryPeriod
		// Check if the hash is already blocked
		if _, ok := sb.hashes[hash]; ok {
			// Update the probationaryPeriod
			sb.hashes[hash] = probationaryPeriodEnd
			continue
		}

		// Add hash to map
		sb.hashes[hash] = probationaryPeriodEnd

		// Marshal the update
		pe := persistEntry{hash, probationaryPeriodEnd, listed}
		data := encoding.Marshal(pe)
		_, err := buf.Write(data)
		if err != nil {
			return bytes.Buffer{}, errors.AddContext(err, "unable to write addition to the buffer")
		}
	}
	listed = false
	for _, hash := range removals {
		// Check if the hash is already removed
		if _, ok := sb.hashes[hash]; !ok {
			continue
		}

		// Remove hash from map
		delete(sb.hashes, hash)

		// Marshal the update
		pe := persistEntry{hash, 0, listed}
		data := encoding.Marshal(pe)
		_, err := buf.Write(data)
		if err != nil {
			return bytes.Buffer{}, errors.AddContext(err, "unable to write removal to the buffer")
		}
	}

	return buf, nil
}

// unmarshalObjects unmarshals the sia encoded objects.
func unmarshalObjects(reader io.Reader) (map[crypto.Hash]int64, error) {
	blocklist := make(map[crypto.Hash]int64)
	// Unmarshal blocked links one by one until EOF.
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
			delete(blocklist, pe.Hash)
			continue
		}
		blocklist[pe.Hash] = pe.ProbationaryPeriodEnd
	}
	return blocklist, nil
}
