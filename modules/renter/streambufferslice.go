package renter

// streambufferslice.go implements a streambuffer using a slice as the data
// source.

import (
	"gitlab.com/NebulousLabs/Sia/modules"
)

// sliceDataSource is a streamBufferDataSource that uses an in memory buffer as
// the data source.
type sliceDataSource struct {
	staticData     []byte
	staticMetadata modules.SkyfileMetadata
	staticID       streamDataSourceID
}

// newStreamBufferDataSourceFromSlice returns a streamBufferDataSource based on
// an input slice.
func (sbs *streamBufferSet) newStreamBufferDataSourceFromSlice(metadata modules.SkyfileMetadata, data []byte, id streamDataSourceID) modules.Streamer {
	sds := &sliceDataSource{
		staticData:     data,
		staticMetadata: metadata,
		staticID:       id,
	}
	stream := sbs.callNewStream(sds, 0)
	return stream
}

// DataSize implements streamBufferDataSource
func (sds *sliceDataSource) DataSize() uint64 {
	return uint64(len(sds.staticData))
}

// ID implements streamBufferDataSource
func (sds *sliceDataSource) ID() streamDataSourceID {
	return sds.staticID
}

// Metadata implements streamBufferDataSource
func (sds *sliceDataSource) Metadata() modules.SkyfileMetadata {
	return sds.staticMetadata
}

// RequestSize implements streamBufferDataSource. The request size is the whole
// thing because it's all in memory anyway.
func (sds *sliceDataSource) RequestSize() uint64 {
	return uint64(len(sds.staticData))
}

// SilentClose implements streamBufferDataSource
func (sds *sliceDataSource) SilentClose() {
	// Nothing to do, no resources here.
	return
}

// ReadAt implements streamBufferDataSource
func (sds *sliceDataSource) ReadAt(b []byte, offset int64) (int, error) {
	n := copy(b, sds.staticData[offset:])
	return n, nil
}
