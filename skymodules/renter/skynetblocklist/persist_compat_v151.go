package skynetblocklist

import (
	"bytes"
	"io"
	"path/filepath"

	"gitlab.com/NebulousLabs/encoding"
	"gitlab.com/NebulousLabs/errors"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
)

// createTempFileFromPersistFilev151 copies the data from the persist file into
// a temporary file and returns a reader for the data. This function checks for
// the existence of a temp file first and will return a reader for the temporary
// file if the temporary file contains a valid checksum.
//
// This method should be used for compat code up through v1.5.1.
func createTempFileFromPersistFilev151(persistDir, fileName string, header, version types.Specifier) (_ io.Reader, err error) {
	// Try and load the temporary file first. This is done first because an
	// unclean shutdown could result in a valid temporary file existing but no
	// persist file existing. In this case we do not want a call to
	// NewAppendOnlyPersist to create a new persist file resulting in a loss of
	// the data in the temporary file
	tempFilePath := filepath.Join(persistDir, tempPersistFileName(fileName))
	reader, err := loadTempFilev151(tempFilePath)
	if err == nil {
		// Temporary file is valid, return the reader
		return reader, nil
	}

	// Clear any old temp file and read the persist data
	data, err := removeTempFileAndReadPersistData(tempFilePath, persistDir, fileName, header, version)
	if err != nil {
		return nil, err
	}

	// write data to file
	return writeDataAndChecksumToFile(tempFilePath, data)
}

// loadTempFilev151 will load a temporary file and verifies the checksum that was
// prefixed. If the checksum is valid a reader will be returned.
func loadTempFilev151(tempFilePath string) (_ io.Reader, err error) {
	// Load and verify the checksum
	fileBytes, err := loadTempFileAndVerifyChecksum(tempFilePath)
	if err != nil {
		return nil, err
	}

	// Return the data after the checksum as a reader
	return bytes.NewReader(fileBytes), nil
}

// unmarshalObjectsV151 unmarshals the sia encoded objects up through compat
// version v1.5.1.
func unmarshalObjectsV151(reader io.Reader) (map[crypto.Hash]struct{}, error) {
	blocklist := make(map[crypto.Hash]struct{})
	// Unmarshal blocked links one by one until EOF.
	var offset uint64
	for {
		buf := make([]byte, persistSizeV151)
		_, err := io.ReadFull(reader, buf)
		if errors.Contains(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		var pe persistEntryV151
		err = encoding.Unmarshal(buf, &pe)
		if err != nil {
			return nil, err
		}
		offset += persistSizeV151

		if !pe.Listed {
			delete(blocklist, pe.Hash)
			continue
		}
		blocklist[pe.Hash] = struct{}{}
	}
	return blocklist, nil
}
