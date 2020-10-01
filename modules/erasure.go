package modules

// erasure.go defines an interface for an erasure coder, as well as an erasure
// type for data that is not erasure coded.

// TODO: move the other ErasureCoderTypes here so they are all in one place.

// TODO: Testing for the passthrough

import (
	"io"

	"gitlab.com/NebulousLabs/Sia/crypto"
)

var (
	// ECPassthrough defines the erasure coder type for an erasure coder that
	// does nothing.
	ECPassthrough = ErasureCoderType{0, 0, 0, 3}
)

type (
	// ErasureCoderType is an identifier for the individual types of erasure
	// coders.
	ErasureCoderType [4]byte

	// ErasureCoderIdentifier is an identifier that only matches another
	// ErasureCoder's identifier if they both are of the same type and settings.
	ErasureCoderIdentifier string

	// An ErasureCoder is an error-correcting encoder and decoder.
	ErasureCoder interface {
		// NumPieces is the number of pieces returned by Encode.
		NumPieces() int

		// MinPieces is the minimum number of pieces that must be present to
		// recover the original data.
		MinPieces() int

		// Encode splits data into equal-length pieces, with some pieces
		// containing parity data.
		Encode(data []byte) ([][]byte, error)

		// Identifier returns the ErasureCoderIdentifier of the ErasureCoder.
		Identifier() ErasureCoderIdentifier

		// EncodeShards encodes the input data like Encode but accepts an already
		// sharded input.
		EncodeShards(data [][]byte) ([][]byte, error)

		// Reconstruct recovers the full set of encoded shards from the provided
		// pieces, of which at least MinPieces must be non-nil.
		Reconstruct(pieces [][]byte) error

		// Recover recovers the original data from pieces and writes it to w.
		// pieces should be identical to the slice returned by Encode (length and
		// order must be preserved), but with missing elements set to nil. n is
		// the number of bytes to be written to w; this is necessary because
		// pieces may have been padded with zeros during encoding.
		Recover(pieces [][]byte, n uint64, w io.Writer) error

		// SupportsPartialEncoding returns true if partial encoding is
		// supported. The piece segment size will be returned. Otherwise the
		// numerical return value is set to zero.
		SupportsPartialEncoding() (uint64, bool)

		// Type returns the type identifier of the ErasureCoder.
		Type() ErasureCoderType
	}
)

// PassthroughErasureCoder is a blank type that signifies no erasure coding.
type PassthroughErasureCoder struct{}

// These functions implement the ErasureCoder interface.
func (pec *PassthroughErasureCoder) NumPieces() int                       { return 1 }
func (pec *PassthroughErasureCoder) MinPieces() int                       { return 1 }
func (pec *PassthroughErasureCoder) Encode(data []byte) ([][]byte, error) { return [][]byte{data}, nil }
func (pec *PassthroughErasureCoder) Identifier() ErasureCoderIdentifier   { return "ECPassthrough" }
func (pec *PassthroughErasureCoder) EncodeShards(pieces [][]byte) ([][]byte, error) {
	return pieces, nil
}
func (pec *PassthroughErasureCoder) Reconstruct(pieces [][]byte) error { return nil }
func (pec *PassthroughErasureCoder) Recover(pieces [][]byte, n uint64, w io.Writer) error {
	_, err := w.Write(pieces[0][:n])
	return err
}
func (pec *PassthroughErasureCoder) SupportsPartialEncoding() (uint64, bool) {
	// The actual protocol is in some places restricted to using an atomic
	// request size of crypto.SegmentSize, so that's what we use here.
	//
	// TODO: I'm not sure if the above comment is completely true, may be okay
	// to return a segment size of 1.
	return crypto.SegmentSize, true
}
func (pec *PassthroughErasureCoder) Type() ErasureCoderType { return ECPassthrough }

// NewPassthroughErasureCoder will return an erasure coder that does not encode
// the data. It uses 1-of-1 redundancy and always returns itself or some subset
// of itself.
func NewPassthroughErasureCoder() ErasureCoder {
	return new(PassthroughErasureCoder)
}
