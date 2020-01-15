package modules

///////////////////////////////////////////
// IMPORTANT:
//
// Ignore this entire file and its contents.
// It holds all of what is missing in the siamux package,
// however the implementations are nowhere near correct.
//
// This to enable using the interfaces and some of the methods
// without having to create interfaces for them.
//
// TODO: delete me
//
///////////////////////////////////////////

import (
	"io/ioutil"
	"net"
	"time"

	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/NebulousLabs/siamux/mux"
	"golang.org/x/crypto/ed25519"
)

var ErrStreamClosed = errors.New("stream closed")
var ErrStreamTimeout = errors.New("stream timed out")

type Stream struct {
	ms *mux.Stream
}

func (s *Stream) Read(b []byte) (n int, err error) {
	return s.ms.Read(b)
}
func (s *Stream) Write(b []byte) (n int, err error) {
	return s.ms.Write(b)
}
func (s *Stream) Close() error {
	return s.ms.Close()
}

func (s *Stream) LocalAddr() net.Addr {
	panic("not implemented yet")
}

// RemoteAddr implements net.Conn.
func (s *Stream) RemoteAddr() net.Addr {
	panic("not implemented yet")
}

// SetDeadline implements net.Conn.
func (s *Stream) SetDeadline(t time.Time) error {
	panic("not implemented yet")
}

// SetReadDeadline implements net.Conn.
func (s *Stream) SetReadDeadline(t time.Time) error {
	panic("not implemented yet")
}

// SetWriteDeadline implements net.Conn.
func (s *Stream) SetWriteDeadline(t time.Time) error {
	panic("not implemented yet")
}

type SiaMux struct {
	stopChan <-chan struct{}
	mux      *mux.Mux
}

func NewSiaMux(stopChan <-chan struct{}) *SiaMux {

	return &SiaMux{stopChan: stopChan}
}

func (sm *SiaMux) Accept() (net.Conn, error) {
	block := make(chan struct{})
	select {
	case <-block: // For now just block
	case <-sm.stopChan:
		return &Stream{}, nil
	}
	return &Stream{}, nil
}

func (sm *SiaMux) NewClientMux(conn net.Conn) *mux.Mux {
	sm.mux, _ = mux.NewClientMux(conn, mux.ED25519PublicKey{}, persist.NewLogger(ioutil.Discard))
	return sm.mux
}

func (sm *SiaMux) NewServerMux(conn net.Conn) *mux.Mux {
	serverPrivKey, serverPubKey := generateED25519KeyPair()
	sm.mux, _ = mux.NewServerMux(conn, serverPubKey, serverPrivKey, persist.NewLogger(ioutil.Discard))
	return sm.mux
}

// generateED25519KeyPair creates a public-secret keypair that can be used to
// sign and verify messages.
func generateED25519KeyPair() (sk mux.ED25519SecretKey, pk mux.ED25519PublicKey) {
	// no error possible when using fastrand.Reader
	epk, esk, _ := ed25519.GenerateKey(fastrand.Reader)
	copy(sk[:], esk)
	copy(pk[:], epk)
	return
}
