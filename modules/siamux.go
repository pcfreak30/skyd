package modules

import (
	"net"
	"time"
)

// SiaMux lists all methods that can be performed on the SiaMux
type SiaMux interface {
	NewStream(string) (Stream, error)
}

// NewSiaMux returns a new sia mux
func NewSiaMux() SiaMux {
	return &MockSiaMux{}
}

// MockSiaMux is a mock implementing the SiaMux interface
type MockSiaMux struct{}

// NewStream returns a new stream object
func (s *MockSiaMux) NewStream(address string) (Stream, error) {
	conn, _ := (&net.Dialer{
		Cancel:  make(chan struct{}),
		Timeout: 45 * time.Second,
	}).Dial("tcp", address)
	return &MockStream{conn: conn}, nil
}

// Stream lists all the stream methods
type Stream interface {
	net.Conn
	SetPriority(int) error
}

// MockStream is a mock object implementing the Stream Interface
type MockStream struct {
	conn net.Conn
}

// Read implements Stream interface
func (s *MockStream) Read(b []byte) (n int, err error) {
	return s.conn.Read(b)
}

// Write implements Stream interface
func (s *MockStream) Write(b []byte) (n int, err error) {
	return s.conn.Write(b)
}

// Close implements Stream interface
func (s *MockStream) Close() error {
	return s.conn.Close()
}

// LocalAddr implements Stream interface
func (s *MockStream) LocalAddr() net.Addr {
	panic("not implemented yet")
}

// RemoteAddr implements net.Conn.
func (s *MockStream) RemoteAddr() net.Addr {
	panic("not implemented yet")
}

// SetDeadline implements net.Conn.
func (s *MockStream) SetDeadline(t time.Time) error {
	panic("not implemented yet")
}

// SetReadDeadline implements net.Conn.
func (s *MockStream) SetReadDeadline(t time.Time) error {
	panic("not implemented yet")
}

// SetWriteDeadline implements net.Conn.
func (s *MockStream) SetWriteDeadline(t time.Time) error {
	panic("not implemented yet")
}

// SetPriority implements Stream interface
func (s *MockStream) SetPriority(p int) error {
	panic("not implemented yet")
}
