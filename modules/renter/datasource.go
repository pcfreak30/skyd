package renter

import (
	"io"
	"os"
	"time"
)

// logicalChunkDataSource is a special type of reader used during file repairs.
// The repair loop will use it to fetch the logical data of a chunk.
type logicalChunkDataSource interface {
	io.Reader
}

// dataSourceFile is the implementation of logicalChunkDataSource for loading a
// chunk from disk.
type dataSourceFile struct {
	source      string
	chunkOffset int64
	chunkLength int64
}

// dataSourceSia is the implementation of logicalChunkDataSource for loading a
// chunk from the Sia network.
type dataSourceSia struct {
	renter      *Renter
	siapath     string
	chunkOffset int64
	chunkLength int64
}

// dataSourceFromFile creates a logicalDataSource which reads the data from
// disk.
func dataSourceFromFile(source string, chunkOffset, chunkLength int64) (logicalChunkDataSource, error) {
	// Make sure the path is valid.
	if _, err := os.Stat(source); err != nil {
		return nil, err
	}
	return &dataSourceFile{
		source:      source,
		chunkOffset: chunkOffset,
		chunkLength: chunkLength,
	}, nil
}

// dataSourceFromSia creates a logicalDataSource which downloads the data from
// the Sia network.
func dataSourceFromSia(r *Renter, siapath string, chunkOffset, chunkLength int64) logicalChunkDataSource {
	return &dataSourceSia{
		renter:      r,
		siapath:     siapath,
		chunkOffset: chunkOffset,
		chunkLength: chunkLength,
	}
}

// Read implements the logicalDataSource interface.
func (dsf *dataSourceFile) Read(d []byte) (int, error) {
	f, err := os.Open(dsf.source)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sr := io.NewSectionReader(f, dsf.chunkOffset, dsf.chunkLength)
	return sr.Read(d)
}

// Read implements the logicalDataSource interface.
func (dss *dataSourceSia) Read(d []byte) (int, error) {
	_, s, err := dss.renter.managedStreamer(dss.siapath, false, 0, 0, 200*time.Millisecond)
	if err != nil {
		return 0, err
	}
	defer s.Close()
	// Seek to the correct offset.
	_, err = s.Seek(dss.chunkOffset, io.SeekStart)
	if err != nil {
		return 0, err
	}
	return s.Read(d)
}
