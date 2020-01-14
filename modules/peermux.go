package modules

import (
	"errors"
	"io"
	"net"
	"time"

	"gitlab.com/NebulousLabs/Sia/encoding"
)

///////////////////////////////////////////
// IMPORTANT:
//
// The types and interfaces in this file are merely mocks. The sole purpose of
// having these is to be able to use the interfaces before the actual peermux MR
// gets merged. Ignore this entire file.
//
///////////////////////////////////////////

var ErrStreamTimeout = errors.New("stream timed out")
var ErrStreamClosed = errors.New("stream closed")

type SiaMux struct {
	muxes map[string]*PeerMux
}

func NewSiaMux() *SiaMux {
	return &SiaMux{
		muxes: make(map[string]*PeerMux),
	}
}

func (sm *SiaMux) NewMux(address string) *PeerMux {
	m, exists := sm.muxes[address]
	if exists {
		return m
	}

	conn, _ := (&net.Dialer{
		Cancel:  make(chan struct{}),
		Timeout: 45 * time.Second,
	}).Dial("tcp", address)
	sm.muxes[address] = &PeerMux{conn: conn}
	return sm.muxes[address]
}

// PeerMux
type PeerMux struct {
	conn net.Conn
}

// NewStream creates a new stream
func (p *PeerMux) NewStream() Stream {
	var s Stream = stream{conn: p.conn}
	return s
}

// Accept listens for incoming streams and returns it
func (p *PeerMux) Accept() (Stream, error) {
	var s Stream = stream{conn: p.conn}
	return s, nil
}

// Close will close the peermux
func (p *PeerMux) Close() error { return nil }

// Stream
type Stream interface {
	io.ReadWriteCloser
	SetPriority(int) error
	SetTimeout(time.Time) error

	// TODO: nothing to see here - move along
	WriteObjects(objs ...interface{}) error
	ReadObject(obj interface{}) error
}

// For now just have stream wrap a net.Conn
type stream struct{ conn net.Conn }

func (s stream) Read(b []byte) (int, error)   { return s.conn.Read(b) }
func (s stream) Write(b []byte) (int, error)  { return s.conn.Write(b) }
func (s stream) Close() error                 { return s.Close() }
func (s stream) SetPriority(p int) error      { return nil }
func (s stream) SetTimeout(t time.Time) error { return s.conn.SetDeadline(t) }

const maxLen = 4096

// Write writes objects to the stream
func (s stream) WriteObjects(objs ...interface{}) error {
	_, err := s.Write(encoding.MarshalAll(objs))
	return err
}

// Read reads an objects from the stream
func (s stream) ReadObject(obj interface{}) error {
	bytes, err := encoding.ReadPrefixedBytes(s, maxLen)
	if err != nil {
		return err
	}
	err = encoding.Unmarshal(bytes, obj)
	if err != nil {
		return err
	}
	return nil
}
