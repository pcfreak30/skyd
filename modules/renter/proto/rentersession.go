package proto

import (
	"crypto/cipher"
	"encoding/json"
	"net"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"golang.org/x/crypto/chacha20poly1305"
)

type (
	// RenterSession is an ongoing exchange of RPC request and responses
	RenterSession struct {
		Session *modules.Session
	}
)

// NewRenterSession will intialize a new session with the given host
func NewRenterSession(conn *net.Conn, hostPublicKey types.SiaPublicKey, id types.FileContractID) (_ *RenterSession, err error) {
	rs := &RenterSession{
		Session: &modules.Session{
			Conn:       *conn,
			ContractID: id,
		},
	}
	rs.Session.ExtendDeadline(modules.OpenSessionTimeout)

	// perform handshake
	if rs.Session.AEAD, err = performHandshake(rs.Session.Conn, hostPublicKey); err != nil {
		return nil, err
	}

	// open the session
	if err = openRenterSession(rs.Session, id); err != nil {
		return nil, err
	}

	// TODO: we should subscribe to consensus changes in order to request an
	// updated price table from the Host before the TTL expires

	return rs, nil
}

// performHandshake performs the initial handshake during session setup
func performHandshake(conn net.Conn, hostPublicKey types.SiaPublicKey) (cipher.AEAD, error) {
	// generate a session key
	xsk, xpk := crypto.GenerateX25519KeyPair()

	// send our half of the key exchange
	req := modules.SessionHandshakeRequest{
		PublicKey: xpk,
		Ciphers:   []types.Specifier{modules.CipherChaCha20Poly1305},
	}
	if err := encoding.NewEncoder(conn).EncodeAll(modules.InitHandshake, req); err != nil {
		return nil, err
	}

	// read host's half of the key exchange
	var resp modules.SessionHandshakeResponse
	if err := encoding.NewDecoder(conn, encoding.DefaultAllocLimit).Decode(&resp); err != nil {
		return nil, err
	}

	// validate the signature before doing anything else; don't want to punish
	// the "host" if we're talking to an imposter
	var hpk crypto.PublicKey
	copy(hpk[:], hostPublicKey.Key)
	var sig crypto.Signature
	copy(sig[:], resp.Signature)
	if err := crypto.VerifyHash(crypto.HashAll(req.PublicKey, resp.PublicKey), hpk, sig); err != nil {
		return nil, err
	}

	// check for compatible cipher
	if resp.Cipher != modules.CipherChaCha20Poly1305 {
		return nil, modules.ErrNoSupportedCiphers
	}
	// derive shared secret, which we'll use as an encryption key
	cipherKey := crypto.DeriveSharedSecret(xsk, resp.PublicKey)

	// use cipherKey to initialize an AEAD cipher
	aead, err := chacha20poly1305.New(cipherKey[:])
	if err != nil {
		build.Critical("could not create cipher")
		return nil, err
	}

	return aead, nil
}

// openRenterSession will open the session by sending the contract ID and verify
// the host's pricing table
func openRenterSession(s *modules.Session, id types.FileContractID) error {
	// send OpenSessionRequest
	if err := encoding.NewEncoder(s.Conn).Encode(modules.OpenSessionRequest{
		ContractID: id,
	}); err != nil {
		return err
	}

	// read OpenSessionResponse
	var resp modules.OpenSessionResponse
	if err := encoding.NewDecoder(s.Conn, encoding.DefaultAllocLimit).Decode(&resp); err != nil {
		return err
	}

	// Unmarshal the price table
	s.Costs = modules.PriceTable{}
	if err := json.Unmarshal(resp.PriceTableJSONEncoded, &s.Costs); err != nil {
		return err
	}

	// TODO: validate host's priceTable:
	// - expiry should be in the future (-> we need blockheight passed in)
	// - prices should be reasonable (what's reasonable?)
	// - should always contain baseRPCPrice

	return nil
}
