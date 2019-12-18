package modules

import (
	"io"
	"net"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/types"
)

///////////////////////////////////////////
// IMPORTANT:
//
// The types and interfaces in this file are merely mocks. The sole purpose of
// having these is to be able to use the interfaces before the actual peermux MR
// gets merged.
//
///////////////////////////////////////////

// PeerMux
type PeerMux struct {
	connections map[string]Connection
}

// NewPeerMux returns a PeerMux object
func NewPeerMux() *PeerMux {
	return &PeerMux{
		connections: make(map[string]Connection),
	}
}

// Close will close the peermux
func (p *PeerMux) Close() error { return nil }

// Connection creates a new connection using given address
func (p *PeerMux) Connection(address string) Connection {
	c, exists := p.connections[address]
	if !exists {
		conn, err := (&net.Dialer{
			Cancel:  make(chan struct{}),
			Timeout: 45 * time.Second,
		}).Dial("tcp", address)
		if err != nil {
			build.Critical(err)
		}
		c = connection{
			conn:    conn,
			streams: make(map[types.Specifier]Stream),
		}
		p.connections[address] = c
	}
	return c
}

// Connection is specific to a net address, and holds streams by specifier
type Connection interface {
	Stream(name types.Specifier) Stream
}

// connection contains a net.Conn and a set of streams. A stream is uniquely
// identified by a Specifier.
type connection struct {
	conn    net.Conn
	streams map[types.Specifier]Stream
}

// Stream returns a new stream for given specifier.
func (c connection) Stream(name types.Specifier) Stream {
	// for now just return the same stream simply wrapping the net.Conn
	return stream{conn: c.conn}
}

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
