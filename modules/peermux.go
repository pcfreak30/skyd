// NOTE:
// The types and interfaces in this file are merely mocks. The sole purpose of
// having these is to be able to use the interfaces before the acutal peermux MR
// gets merged.

package modules

import (
	"net"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/types"
)

var mock = types.Specifier{'M', 'o', 'c', 'k'}

type Stream interface {
	Read(obj interface{}) (int, error)
	Write([]byte) (int, error)
	SetPriority(int) error
	SetTimeout(time.Time) error
}

// For now just have stream wrap a net.Conn
type stream struct{ conn net.Conn }

func (s *stream) Read(obj interface{}) (int, error) {
	// Note we purposefully skip encryption, really just want to have an
	// interface that compiles
	bytes, err := encoding.ReadPrefixedBytes(s.conn, 4096)
	if err != nil {
		return len(bytes), err
	}
	err = encoding.Unmarshal(bytes, obj)
	if err != nil {
		return len(bytes), err
	}
	return len(bytes), nil
}
func (s *stream) Write(b []byte) (int, error)  { return s.conn.Write(b) }
func (s *stream) SetPriority(p int) error      { return nil }
func (s *stream) SetTimeout(t time.Time) error { return s.conn.SetDeadline(t) }

type PeerMux struct {
	streams map[types.Specifier]*stream
}

func NewPeerMux(address string) *PeerMux {
	conn, err := (&net.Dialer{
		Cancel:  make(chan struct{}),
		Timeout: 45 * time.Second,
	}).Dial("tcp", address)
	if err != nil {
		build.Critical(err)
		return nil
	}
	return &PeerMux{streams: map[types.Specifier]*stream{
		mock: &stream{conn: conn},
	}}
}

func (p *PeerMux) Stream(name types.Specifier) Stream { return p.streams[mock] }
func (p *PeerMux) Close() error                       { return nil }

type (
	// The PriceTable is built by the host and contains its pricing relevant to
	// the session. Under RPC the host lists all different RPC calls and their
	// corresponding price. The prices remain valid up until the expiry block
	// height.
	RPCPriceTable struct {
		Costs  map[RemoteRPCID]types.Currency
		Expiry types.BlockHeight
	}

	// RemoteRPCID is a fixed-length 48 byte-array which uniquely identifies an
	// RPC on a host (HostID|RPCID)
	RemoteRPCID = [48]byte
)

// NewRemoteRPCID takes a host public key and an rpc identifier and joins them
// into a remote RPC identifier
func NewRemoteRPCID(host types.SiaPublicKey, rpcID types.Specifier) RemoteRPCID {
	var id RemoteRPCID
	copy(id[:], host.Key[:])
	copy(id[:], rpcID[:])
	return id
}
