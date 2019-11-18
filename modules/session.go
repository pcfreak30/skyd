package modules

import (
	"crypto/cipher"
	"net"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
)

const (
	RPCMaxLen = 4096
)

var (
	// OpenSessionTimeout establishes the minimum amount of time that the
	// connection deadline is expected to be set to for a session to be fully
	// set up. The deadline is long enough that the connection should be
	// successful even if both parties are on Tor.
	OpenSessionTimeout = build.Select(build.Var{
		Dev:      120 * time.Second,
		Standard: 120 * time.Second,
		Testing:  3 * time.Second,
	}).(time.Duration)
)

type (
	// A Session contains all the session state
	Session struct {
		Conn       net.Conn
		AEAD       cipher.AEAD
		ContractID types.FileContractID
		Costs      PriceTable
		Closed     bool
	}

	// OpenSessionRequest contains the contract id for the session
	OpenSessionRequest struct {
		ContractID types.FileContractID
	}

	// OpenSessionResponse contains the host's price table
	OpenSessionResponse struct {
		PriceTableJSONEncoded []byte
	}
)

type (
	// The PriceTable is built by the host and contains its pricing relevant to
	// the session. Under RPC the host lists all different RPC calls and their
	// corresponding price. The prices remain valid up until the expiry block
	// height.
	PriceTable struct {
		RPC    map[RemoteRPCID]types.Currency
		Expiry types.BlockHeight
	}

	// RemoteRPCID is a fixed-length 48 byte-array which uniquely identifies an
	// RPC on a host (HostID|RPCID)
	RemoteRPCID = [48]byte
)

// Handshake identifiers
var (
	InitHandshake = newSpecifier("InitHandshake")
)

// Handshake request-response objects
type (
	// SessionHandshakeRequest is the first object sent when forming a session,
	// it initiates a Diffie-Hellman key exchange
	SessionHandshakeRequest struct {
		PublicKey crypto.X25519PublicKey
		Ciphers   []types.Specifier
	}

	// SessionHandshakeResponse is the host's response to the
	// SessionHandshakeRequest
	SessionHandshakeResponse struct {
		PublicKey crypto.X25519PublicKey
		Signature []byte
		Cipher    types.Specifier
	}
)

// Extract payment identifiers
var (
	PayByContract         = newSpecifier("PayByContract")
	PayByEphemeralAccount = newSpecifier("PayByEphemeralAcc")
)

// Extract payment request-response objects
type (
	PaymentRequest struct {
		Type types.Specifier
	}

	PayByEphemeralAccountRequest struct {
		Message   WithdrawalMessage
		Signature crypto.Signature
	}

	PayByEphemeralAccountResponse struct {
		Amount                 types.Currency
		AcceptRejectMessage    string
		AccountManagerResponse error
	}

	PayByContractRequest struct {
		Revision  types.FileContractRevision
		Signature crypto.Signature
	}

	PayByContractResponse struct {
		Amount              types.Currency
		AcceptRejectMessage string
		Signature           crypto.Signature
	}

	// WithdrawalMessage contains all details to spend from an ephemeral account
	WithdrawalMessage struct {
		Id     types.SiaPublicKey
		Expiry types.BlockHeight
		Amount types.Currency
		Nonce  int
	}
)

// RPC identifiers
var (
	RPCInitSession          = newSpecifier("InitSession")
	RPCUpdatePriceTable     = newSpecifier("UpdatePriceTable")
	RPCFundEphemeralAccount = newSpecifier("FundEphemeralAcc")
)

// RPC request-response objects
type (
	// RPCUpdatePriceTableResponse contains the updated prices. Note it has no
	// UpdatePriceTableRequest counterpart as that is implied by the RPC
	RPCUpdatePriceTableResponse struct {
		PriceTableJSONEncoded []byte
	}

	// RPCFundEphemeralAccountRequest
	RPCFundEphemeralAccountRequest struct {
		AccountID string
	}
)

// Close will mark the session as closed and close the underlying connection
func (s *Session) Close() error {
	if s.Closed {
		return nil
	}
	s.Closed = true
	return s.Conn.Close()
}

// ReadRequest reads an encrypted RPC request
func (s *Session) ReadRequest(req interface{}, maxLen uint64) error {
	return ReadRPCRequest(s.Conn, s.AEAD, req, maxLen)
}

// WriteRequest sends an encrypted RPC request
func (s *Session) WriteRequest(rpcID types.Specifier, req interface{}) error {
	return WriteRPCRequest(s.Conn, s.AEAD, rpcID, req)
}

// ReadResponse reads an encrypted RPC response
func (s *Session) ReadResponse(resp interface{}, maxLen uint64) error {
	return ReadRPCResponse(s.Conn, s.AEAD, resp, maxLen)
}

// WriteResponse sends an encrypted RPC response
func (s *Session) WriteResponse(resp interface{}) error {
	return WriteRPCResponse(s.Conn, s.AEAD, resp, nil)
}

// WriteMessage sends an encrypted RPC message
func (s *Session) WriteMessage(msg interface{}) error {
	return WriteRPCMessage(s.Conn, s.AEAD, msg)
}

// WriteError sends an encrypted RPC error
func (s *Session) WriteError(err error) error {
	return WriteRPCResponse(s.Conn, s.AEAD, nil, err)
}

// ExtendDeadline extends the read/write deadline on the underlying connection
func (s *Session) ExtendDeadline(d time.Duration) error {
	return s.Conn.SetDeadline(time.Now().Add(d))
}

// Call is a helper method that calls WriteRequest followed by ReadResponse.
func (s *Session) Call(rpcID types.Specifier, req, resp interface{}, maxLen uint64) error {
	if err := s.WriteRequest(rpcID, req); err != nil {
		return err
	}
	return s.ReadResponse(resp, maxLen)
}

// NewRemoteRPCID takes a host public key and an rpc identifier and joins them
// into a remote RPC identifier
func NewRemoteRPCID(hpk types.SiaPublicKey, rpc types.Specifier) RemoteRPCID {
	var id RemoteRPCID
	copy(id[:], hpk.Key[:])
	copy(id[:], rpc[:])
	return id
}

// newSpecifier takes in a name and returns a fixed-length byte-array specifier
func newSpecifier(name string) types.Specifier {
	if len(name) > 16 {
		panic("ERROR: specifier max length exceeded")
	}
	var s types.Specifier
	copy(s[:], name)
	return s
}
