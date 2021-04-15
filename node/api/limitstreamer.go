package api

import (
	"encoding/json"
	"io"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/skynetlabs/skyd/skymodules"
)

// limitStreamer is a helper struct that wraps a skymodules.Streamer so it starts
// at a certain offset, and can only be read from until a certain limit. It
// wraps both Read and Seek calls and handles the offset and returned bytes
// appropriately.
//
// Note that the limitStreamer is not thread safe, if you call Seek and Read on
// it from different threads, you are going to have unexpected behavior.
// Further more, it is advised to only wrap a skymodules.Streamer once, wrapping it
// multiple times might lead to unexpected behavior and was not tested.
type limitStreamer struct {
	stream       skymodules.SkyfileStreamer
	base         uint64
	off          uint64
	limit        uint64
	staticLayout skymodules.SkyfileLayout
	staticMD     skymodules.SkyfileMetadata
	staticRawMD  []byte
}

// NewLimitStreamer wraps the given skymodules.Streamer and ensures it can only
// be read from within the given offset and size boundary. It does this by
// wrapping both the Read and Seek calls and adjusting the offset and size of
// the returned byte slice appropriately. It also replaces the metadata with the
// provided metadata. That's because we return a different sub-metadata when
// downloading subfiles.
func NewLimitStreamer(s skymodules.SkyfileStreamer, md skymodules.SkyfileMetadata, layout skymodules.SkyfileLayout, offset, size uint64) (skymodules.SkyfileStreamer, error) {
	ls := &limitStreamer{
		stream:       s,
		base:         offset,
		off:          offset,
		limit:        offset + size,
		staticLayout: layout,
		staticMD:     md,
	}
	_, err := ls.Seek(0, io.SeekStart) // SeekStart to ensure the initial offset
	if err != nil {
		return nil, err
	}
	ls.staticRawMD, err = json.Marshal(md)
	return ls, err
}

// Read implements the io.Reader interface
func (ls *limitStreamer) Read(p []byte) (n int, err error) {
	if ls.off >= ls.limit {
		return 0, io.EOF
	}
	if max := ls.limit - ls.off; uint64(len(p)) > max {
		p = p[0:max]
	}

	n, err = ls.stream.Read(p)
	ls.off += uint64(n)
	return
}

// Layout implements the skymodules.SkyfileStreamer interface.
func (ls *limitStreamer) Layout() skymodules.SkyfileLayout {
	return ls.staticLayout
}

// Metadata implements the skymodules.SkyfileStreamer interface.
func (ls *limitStreamer) Metadata() skymodules.SkyfileMetadata {
	return ls.staticMD
}

// RawMetadata implements the skymodules.SkyfileStreamer interface.
func (ls *limitStreamer) RawMetadata() []byte {
	return ls.staticRawMD
}

// Seek implements the io.Seeker interface
func (ls *limitStreamer) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		offset += int64(ls.base)
	case io.SeekCurrent:
		offset += int64(ls.off)
	case io.SeekEnd:
		offset += int64(ls.limit)
	default:
		return 0, errors.New("invalid value for 'whence' in call to seek")
	}

	if uint64(offset) < ls.base {
		return 0, errors.New("invalid offset")
	}

	ls.off = uint64(offset)
	_, err := ls.stream.Seek(int64(ls.off), io.SeekStart)
	if err != nil {
		return offset - int64(ls.base), err
	}

	return offset - int64(ls.base), nil
}

// Close implements the io.Closer interface
func (ls *limitStreamer) Close() error {
	return ls.stream.Close()
}
