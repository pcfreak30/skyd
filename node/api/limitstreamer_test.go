package api

import (
	"bytes"
	"io"
	"io/ioutil"
	"testing"

	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter"
)

// TestLimitStreamer verifies the limit streamer properly returns the data
// between the offset and size boundaries.
func TestLimitStreamer(t *testing.T) {
	data := []byte("Hello, this is some not so random text")

	// test simple case where we do not wrap the streamer
	streamer := renter.SkylinkStreamerFromSlice(data, skymodules.SkyfileMetadata{}, []byte{}, skymodules.SkyfileLayout{})
	allData, err := ioutil.ReadAll(streamer)
	if !bytes.Equal(allData, data) {
		t.Fatal("Expected streamer to return all data")
	}
	if err != nil {
		t.Fatal(err)
	}

	// test the limitreader where we wrap it, but at offset 0 and for the full
	// length of the data
	streamer = renter.SkylinkStreamerFromSlice(data, skymodules.SkyfileMetadata{}, []byte{}, skymodules.SkyfileLayout{})
	streamer, err = NewLimitStreamer(streamer, skymodules.SkyfileMetadata{}, skymodules.SkyfileLayout{}, 0, uint64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	allData, err = ioutil.ReadAll(streamer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(allData, data) {
		t.Fatal("Expected streamer to return all data")
	}

	// test limit reader at offset
	streamer = renter.SkylinkStreamerFromSlice(data, skymodules.SkyfileMetadata{}, []byte{}, skymodules.SkyfileLayout{})
	streamer, err = NewLimitStreamer(streamer, skymodules.SkyfileMetadata{}, skymodules.SkyfileLayout{}, 20, uint64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	allData, err = ioutil.ReadAll(streamer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(allData, []byte("not so random text")) {
		t.Log(string(allData))
		t.Fatal("Expected streamer to return all data")
	}

	// test limit reader at offset with length
	streamer = renter.SkylinkStreamerFromSlice(data, skymodules.SkyfileMetadata{}, []byte{}, skymodules.SkyfileLayout{})
	streamer, err = NewLimitStreamer(streamer, skymodules.SkyfileMetadata{}, skymodules.SkyfileLayout{}, 20, 13)
	if err != nil {
		t.Fatal(err)
	}
	allData, err = ioutil.ReadAll(streamer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(allData, []byte("not so random")) {
		t.Log(string(allData))
		t.Fatal("Expected streamer to return all data")
	}

	// try to force it outside of the bounds
	streamer = renter.SkylinkStreamerFromSlice(data, skymodules.SkyfileMetadata{}, []byte{}, skymodules.SkyfileLayout{})
	streamer, err = NewLimitStreamer(streamer, skymodules.SkyfileMetadata{}, skymodules.SkyfileLayout{}, 20, 13)
	if err != nil {
		t.Fatal(err)
	}
	_, err = streamer.Seek(34, io.SeekCurrent)
	if err != nil {
		t.Fatal(err)
	}

	allData, err = ioutil.ReadAll(streamer)
	if err != nil {
		t.Fatal(err)
	}
	if len(allData) != 0 {
		t.Fatal("Expected no data after seeking outside the limit bounds")
	}

	// try to force it outside of the bounds
	streamer = renter.SkylinkStreamerFromSlice(data, skymodules.SkyfileMetadata{}, []byte{}, skymodules.SkyfileLayout{})
	streamer, err = NewLimitStreamer(streamer, skymodules.SkyfileMetadata{}, skymodules.SkyfileLayout{}, 20, 13)
	if err != nil {
		t.Fatal(err)
	}
	_, err = streamer.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatal(err)
	}

	allData, err = ioutil.ReadAll(streamer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(allData, []byte("not so random")) {
		t.Fatal("Expected data to be offset by 'off' after seeking to 0")
	}
}

// streamerFromReader is wraps a bytes.Reader to give it a Close() method, which
// allows it to satisfy the skymodules.Streamer interface.
type streamerFromReader struct {
	*bytes.Reader
}

// Close is a no-op because a bytes.Reader doesn't need to be closed.
func (sfr *streamerFromReader) Close() error {
	return nil
}
