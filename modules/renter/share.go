package renter

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/renter/siafile"
	"gitlab.com/NebulousLabs/errors"
)

// LoadShare loads a shared siafile or folder into the renter, forming contracts
// with hosts as necessary.
func (r *Renter) LoadShare(src string, siaPath modules.SiaPath) error {
	if err := r.tg.Add(); err != nil {
		return err
	}
	defer r.tg.Done()
	// Open the file.
	f, err := os.Open(src)
	if err != nil {
		return errors.AddContext(err, "failed to open shared file")
	}
	defer f.Close()
	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	// Get the destination path.
	dstDir := siaPath.SiaFileSysPath(r.staticFilesDir)

	// Copy the files from the tarball to the new location.
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		dst := filepath.Join(dstDir, header.Name)

		// Check for dir.
		info := header.FileInfo()
		if info.IsDir() {
			if err = os.MkdirAll(dst, info.Mode()); err != nil {
				return err
			}
			continue
		}
		// Load the new file in memory.
		b, err := ioutil.ReadAll(tr)
		if err != nil {
			return err
		}
		if filepath.Ext(info.Name()) == modules.SiaFileExtension {
			// Load the file as a SiaFile.
			reader := bytes.NewReader(b)
			sf, err := siafile.LoadSiaFileFromReader(reader, dst, r.wal)
			if err != nil {
				return err
			}
			// Read the chunks from the reader.
			chunks, err := ioutil.ReadAll(reader)
			if err != nil {
				return err
			}
			// Inform contractor about shared hosts.
			err = r.hostContractor.AddSharedHostKeys(sf.HostPublicKeys())
			if err != nil {
				return errors.AddContext(err, "failed to add shared hosts to contractor")
			}
			// Add the file to the SiaFileSet.
			err = r.staticFileSet.AddExistingSiaFile(sf, chunks)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Renter) createSharedFile(w *tar.Writer, siaPath modules.SiaPath) error {
	// Open SiaFile.
	entry, err := r.staticFileSet.Open(siaPath)
	if err != nil {
		return errors.AddContext(err, "failed to open siafile for snapshotting")
	}
	defer entry.Close()
	// Get snapshot reader.
	sr, err := entry.SnapshotReader()
	if err != nil {
		return errors.AddContext(err, "failed to get snapshot reader")
	}
	defer sr.Close()
	info, err := sr.Stat()
	if err != nil {
		return err
	}
	// Create the header for the file.
	header, err := tar.FileInfoHeader(info, info.Name())
	if err != nil {
		return err
	}
	siaDirPath, err := siaPath.Dir()
	if err != nil {
		return err
	}
	siaFilePath := siaPath.SiaFileSysPath(r.staticFilesDir)
	siaDirSysPath := siaDirPath.SiaDirSysPath(r.staticFilesDir)
	header.Name = strings.TrimPrefix(siaFilePath, siaDirSysPath)
	header.Name = strings.TrimPrefix(header.Name, string(filepath.Separator))
	// Write the header first.
	if err := w.WriteHeader(header); err != nil {
		return err
	}
	// Write the file next.
	_, err = io.Copy(w, sr)
	return err
}

func (r *Renter) createSharedDir(w *tar.Writer, siaPath modules.SiaPath) error {
	// Walk over all the siafiles and add them to the tarball.
	root := siaPath.SiaDirSysPath(r.staticFilesDir)
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		// This error is non-nil if filepath.Walk couldn't stat a file or
		// folder.
		if err != nil {
			return err
		}
		// Nothing to do for non-folders and non-siafiles.
		if !info.IsDir() && filepath.Ext(path) != modules.SiaFileExtension {
			return nil
		}
		// Create the header for the file/dir.
		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}
		relPath := strings.TrimPrefix(path, root)
		relPath = strings.TrimPrefix(relPath, string(filepath.Separator))
		header.Name = relPath
		// If the info is a dir there is nothing more to do besides writing the
		// header.
		if info.IsDir() {
			return w.WriteHeader(header)
		}
		// Get the siafile.
		sp := strings.TrimPrefix(path, r.staticFilesDir)
		sp = strings.TrimSuffix(sp, modules.SiaFileExtension)
		siaPath, err := modules.NewSiaPath(sp)
		if err != nil {
			return err
		}
		entry, err := r.staticFileSet.Open(siaPath)
		if err != nil {
			return err
		}
		defer entry.Close()
		// Get a reader to read from the siafile.
		sr, err := entry.SnapshotReader()
		if err != nil {
			return err
		}
		defer sr.Close()
		// Update the size of the file within the header since it might have changed
		// while we weren't holding the lock.
		fi, err := sr.Stat()
		if err != nil {
			return err
		}
		header.Size = fi.Size()
		// Write the header.
		if err := w.WriteHeader(header); err != nil {
			return err
		}
		// Add the file to the archive.
		_, err = io.Copy(w, sr)
		return err
	})
}

// Share creates a shared file or folder of the file or folder at the specified
// path.
func (r *Renter) Share(w io.Writer, siaPath modules.SiaPath) error {
	if err := r.tg.Add(); err != nil {
		return err
	}
	defer r.tg.Done()

	gzw := gzip.NewWriter(w)
	defer gzw.Close()
	tw := tar.NewWriter(gzw)
	defer tw.Close()

	if r.staticFileSet.Exists(siaPath) {
		return errors.AddContext(r.createSharedFile(tw, siaPath), "createSharedFile failed")
	}
	return errors.AddContext(r.createSharedDir(tw, siaPath), "createSharedDir failed")
}
