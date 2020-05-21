package skynetportals

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"

	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

const (
	// persistFile is the name of the persist file.
	persistFile string = "skynetportals"

	// persistSize is the size of a persisted portal in the portals list. It is
	// the length of `NetAddress` plus the `public` and `listed` flags.
	persistSize uint64 = modules.MaxEncodedNetAddressLength + 2
)

var (
	// ErrDuplicateAddition is the error indicating a duplicate addition.
	ErrDuplicateAddition = errors.New("duplicate addition")

	// ErrInexistentPortal is the error indicating a portal being removed does
	// not already exist.
	ErrInexistentPortal = errors.New("inexistent portal")

	// ErrSkynetPortalsValidation is the error returned when validation of
	// changes to the Skynet portals list fails.
	ErrSkynetPortalsValidation = errors.New("could not validate additions and removals")

	// metadataHeader is the header of the metadata for the persist file
	metadataHeader = types.NewSpecifier("SkynetPortals\n")

	// metadataVersion is the version of the persistence file
	metadataVersion = types.NewSpecifier("v1.4.8\n")
)

type (
	// SkynetPortals manages a list of known Skynet portals by persisting the
	// list to disk.
	SkynetPortals struct {
		staticAop *persist.AppendOnlyPersist

		// portals is a map of portal addresses to public status.
		portals map[modules.NetAddress]bool

		mu sync.Mutex
	}

	// persistEntry contains a Skynet portal and whether it should be listed as
	// being in the persistence file.
	persistEntry struct {
		address modules.NetAddress
		public  bool
		listed  bool
	}
)

// New returns an initialized SkynetPortals.
func New(persistDir string) (*SkynetPortals, error) {
	// Initialize the persistence of the portal list.
	aop, reader, err := persist.NewAppendOnlyPersist(persistDir, persistFile, metadataHeader, metadataVersion)
	if err != nil {
		return nil, errors.AddContext(err, fmt.Sprintf("unable to initialize the skynet portal list persistence at '%v'", aop.FilePath()))
	}

	sp := &SkynetPortals{
		staticAop: aop,
	}
	portals, err := unmarshalObjects(reader)
	if err != nil {
		return nil, errors.AddContext(err, "unable to unmarshal persist objects")
	}
	sp.portals = portals

	return sp, nil
}

// Close closes and frees associated resources.
func (sp *SkynetPortals) Close() error {
	return sp.staticAop.Close()
}

// FilePath returns the filepath of the persistence.
func (sp *SkynetPortals) FilePath() string {
	return sp.staticAop.FilePath()
}

// Portals returns the list of known Skynet portals.
func (sp *SkynetPortals) Portals() []modules.SkynetPortal {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	var portals []modules.SkynetPortal
	for addr, public := range sp.portals {
		portal := modules.SkynetPortal{
			Address: addr,
			Public:  public,
		}
		portals = append(portals, portal)
	}
	return portals
}

// UpdatePortals updates the list of known Skynet portals.
func (sp *SkynetPortals) UpdatePortals(additions []modules.SkynetPortal, removals []modules.NetAddress) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	// Convert portal addresses to lowercase for case-insensitivity.
	for i, portalInfo := range additions {
		address := modules.NetAddress(strings.ToLower(string(portalInfo.Address)))
		additions[i].Address = address
	}
	for i, address := range removals {
		address = modules.NetAddress(strings.ToLower(string(address)))
		removals[i] = address
	}

	// Validate now before we start making changes.
	err := sp.validateChanges(additions, removals)
	if err != nil {
		return errors.AddContext(err, ErrSkynetPortalsValidation.Error())
	}

	buf, err := sp.marshalObjects(additions, removals)
	if err != nil {
		return errors.AddContext(err, "unable to marshal additions and removals")
	}
	_, err = sp.staticAop.Write(buf.Bytes())
	return errors.AddContext(err, "unable to write to persistence")
}

// marshalObjects marshals the given objects into a byte buffer.
//
// NOTE: this method does not check for duplicate additions. We
// assume the input has been validated.
func (sp *SkynetPortals) marshalObjects(additions []modules.SkynetPortal, removals []modules.NetAddress) (bytes.Buffer, error) {
	// Create buffer for encoder
	var buf bytes.Buffer
	// Create and encode the persist portals
	listed := true
	for _, portal := range additions {
		// Add portal to map
		sp.portals[portal.Address] = portal.Public

		// Marshal the update
		pe := persistEntry{portal.Address, portal.Public, listed}
		err := pe.MarshalSia(&buf)
		if err != nil {
			return bytes.Buffer{}, errors.AddContext(err, "unable to encode persisted portal")
		}
	}
	listed = false
	for _, address := range removals {
		// Remove portal from map
		public, exists := sp.portals[address]
		if !exists {
			return bytes.Buffer{}, errors.AddContext(ErrInexistentPortal, fmt.Sprintf("address %v not found", address))
		}
		delete(sp.portals, address)

		// Marshal the update
		pe := persistEntry{address, public, listed}
		err := pe.MarshalSia(&buf)
		if err != nil {
			return bytes.Buffer{}, errors.AddContext(err, "unable to encode persisted portal")
		}
	}

	return buf, nil
}

// unmarshalObjects unmarshals the sia encoded objects.
func unmarshalObjects(reader io.Reader) (map[modules.NetAddress]bool, error) {
	portals := make(map[modules.NetAddress]bool)
	// Unmarshal portals one by one until EOF.
	for {
		var pe persistEntry
		err := pe.UnmarshalSia(reader)
		if errors.Contains(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if !pe.listed {
			delete(portals, pe.address)
			continue
		}
		portals[pe.address] = pe.public
	}
	return portals, nil
}

// MarshalSia implements the encoding.SiaMarshaler interface.
//
// TODO: Remove these custom marshal functions and use encoding marshal
// functions. Note that removing these changes the marshal format and is not
// backwards-compatible.
func (pe persistEntry) MarshalSia(w io.Writer) error {
	if len(pe.address) > modules.MaxEncodedNetAddressLength {
		return fmt.Errorf("given address %v does not fit in %v bytes", pe.address, modules.MaxEncodedNetAddressLength)
	}
	e := encoding.NewEncoder(w)
	// Create a padded buffer so that we always write the same amount of bytes.
	buf := make([]byte, modules.MaxEncodedNetAddressLength)
	copy(buf, pe.address)
	e.Write(buf)
	e.WriteBool(pe.public)
	e.WriteBool(pe.listed)
	return e.Err()
}

// UnmarshalSia implements the encoding.SiaUnmarshaler interface.
func (pe *persistEntry) UnmarshalSia(r io.Reader) error {
	*pe = persistEntry{}
	d := encoding.NewDecoder(r, encoding.DefaultAllocLimit)
	// Read into a padded buffer and extract the address string.
	buf := make([]byte, modules.MaxEncodedNetAddressLength)
	n, err := d.Read(buf)
	if err != nil {
		return errors.AddContext(err, "unable to read address")
	}
	if n != len(buf) {
		return errors.New("did not read address correctly")
	}
	end := bytes.IndexByte(buf, 0)
	if end == -1 {
		end = len(buf)
	}
	pe.address = modules.NetAddress(string(buf[:end]))
	pe.public = d.NextBool()
	pe.listed = d.NextBool()
	err = d.Err()
	return err
}

// validateChanges validates the changes to be made to the Skynet portals list.
func (sp *SkynetPortals) validateChanges(additions []modules.SkynetPortal, removals []modules.NetAddress) error {
	// Check for nil input.
	if len(additions)+len(removals) == 0 {
		return errors.New("no portals being added or removed")
	}

	// Check additions.
	seenAdditions := make(map[modules.NetAddress]struct{})
	for _, addition := range additions {
		address := addition.Address
		if err := address.IsStdValid(); err != nil {
			return errors.AddContext(err, "invalid network address")
		}

		// Allow additions only if it is to change the public status.
		public, exists := sp.portals[address]
		if exists && public == addition.Public {
			return errors.AddContext(ErrDuplicateAddition, fmt.Sprintf("address %s not found", address))
		}
		// Check for duplicate portals within the ones being added.
		if _, exists := seenAdditions[address]; exists {
			return errors.AddContext(ErrDuplicateAddition, fmt.Sprintf("address %s not found", address))
		}
		seenAdditions[address] = struct{}{}
	}
	// Check removals. Each portal must already exist in the list.
	for _, address := range removals {
		if err := address.IsStdValid(); err != nil {
			return errors.AddContext(err, "invalid network address")
		}

		if _, exists := sp.portals[address]; !exists {
			return errors.AddContext(ErrInexistentPortal, fmt.Sprintf("address %s not found", address))
		}
	}
	return nil
}
