package renter

import (
	"io/ioutil"
	"os"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/renter/siafile"
	"gitlab.com/NebulousLabs/errors"
)

// LoadSharedFile loads a shared siafile into the renter, forming contracts with
// hosts as necessary.
func (r *Renter) LoadSharedFile(src string, siaPath modules.SiaPath) error {
	// Open the file.
	reader, err := os.Open(src)
	if err != nil {
		return errors.AddContext(err, "failed to open shared file")
	}
	defer reader.Close()
	// Get the destination path.
	dst := siaPath.SiaFileSysPath(r.staticFilesDir)
	// Load the siafile.
	sf, err := siafile.LoadSiaFileFromReader(reader, dst, r.wal)
	if err != nil {
		return errors.AddContext(err, "failed to load siafile")
	}
	// Load the chunks.
	chunks, err := ioutil.ReadAll(reader)
	if err != nil {
		return errors.AddContext(err, "failed to read chunks of shared file")
	}
	// Inform contractor about shared hosts.
	err = r.hostContractor.AddSharedHostKeys(sf.HostPublicKeys())
	if err != nil {
		return errors.AddContext(err, "failed to add shared hosts to contractor")
	}
	// Add the siafile to the renter.
	if err := r.staticFileSet.AddExistingSiaFile(sf, chunks); err != nil {
		return errors.AddContext(err, "failed to add siafile to set")
	}
	return nil
}
