package host

import (
	"encoding/json"
	"net"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
	"golang.org/x/crypto/chacha20poly1305"
)

// managedRPCOpenSession initializes a session by performing a DH key exchange
// with the caller, it will open the session after verify the session's contract
// id and communicating the host's pricing table to the caller
func (h *Host) managedRPCOpenSession(conn net.Conn) (err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// create the session object
	s := &modules.Session{
		Conn: conn,
	}

	s.Conn.SetDeadline(time.Now().Add(modules.OpenSessionTimeout))

	// read caller's half of key exchange
	var req modules.SessionHandshakeRequest
	if err := encoding.NewDecoder(conn, encoding.DefaultAllocLimit).Decode(&req); err != nil {
		return err
	}

	// check for a supported cipher
	var supportsChaCha bool
	for _, c := range req.Ciphers {
		if c == modules.CipherChaCha20Poly1305 {
			supportsChaCha = true
		}
	}
	if !supportsChaCha {
		_ = encoding.NewEncoder(conn).Encode(modules.SessionHandshakeResponse{
			Cipher: modules.CipherNoOverlap,
		})
		return modules.ErrNoSupportedCiphers
	}

	// generate a session key, sign it, and derive the shared secret
	xsk, xpk := crypto.GenerateX25519KeyPair()
	pubkeySig := crypto.SignHash(crypto.HashAll(req.PublicKey, xpk), h.secretKey)
	cipherKey := crypto.DeriveSharedSecret(xsk, req.PublicKey)

	// send our half of the key exchange
	resp := modules.SessionHandshakeResponse{
		Cipher:    modules.CipherChaCha20Poly1305,
		PublicKey: xpk,
		Signature: pubkeySig[:],
	}
	if err := encoding.NewEncoder(conn).Encode(resp); err != nil {
		return err
	}

	// use cipherKey to initialize an AEAD cipher
	s.AEAD, err = chacha20poly1305.New(cipherKey[:])
	if err != nil {
		build.Critical("could not create cipher")
		return err
	}

	// read the open session request
	var ssReq modules.OpenSessionRequest
	if err := s.ReadRequest(&ssReq, modules.RPCMinLen); err != nil {
		return errors.Compose(s.WriteError(err), err)
	}
	s.ContractID = ssReq.ContractID // TODO verify contract id

	// respond by sending the price table
	s.Costs = h.buildPriceTable()
	ptBytes, err := json.Marshal(s.Costs)
	if err != nil {
		return errors.Compose(s.WriteError(err), err)
	}
	if err := s.WriteResponse(&modules.OpenSessionResponse{
		PriceTableJSONEncoded: ptBytes,
	}); err != nil {
		return errors.Compose(s.WriteError(err), err)
	}

	// declare the session as opened
	s.Closed = false

	for {
		err1 := s.Conn.SetDeadline(time.Now().Add(rpcRequestInterval))
		id, err2 := modules.ReadRPCID(s.Conn, s.AEAD)
		err := errors.Compose(err1, err2)
		if err != nil {
			h.log.Debugf("WARN: could not read RPC ID: %v", err)
			// try to write, even though connection is probably faulty
			return errors.Compose(s.WriteError(err), err)
		}

		switch id {
		case modules.RPCUpdatePriceTable:
			if err := h.managedRPCUpdatePriceTable(s); err != nil {
				return extendErr("incoming RPC"+id.String()+" failed: ", err)
			}
		case modules.RPCFundEphemeralAccount:
			if err := h.managedRPCFundEphemeralAccount(s); err != nil {
				return extendErr("incoming RPC"+id.String()+" failed: ", err)
			}
		default:
			return errors.New("invalid or unknown RPC ID: " + id.String())
		}
	}
}
