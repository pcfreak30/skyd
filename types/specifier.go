package types

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// TODO: remove ShortSpecifier.

const (
	// SpecifierLen is the length in bytes of a Specifier.
	SpecifierLen = 16

	// ShortSpecifierLen is the length in bytes of a short Specifier.
	ShortSpecifierLen = 8
)

type (
	// A Specifier is a fixed-length byte-array that serves two purposes. In
	// the wire protocol, they are used to identify a particular encoding
	// algorithm, signature algorithm, etc. This allows nodes to communicate on
	// their own terms; for example, to reduce bandwidth costs, a node might
	// only accept compressed messages.
	//
	// Internally, Specifiers are used to guarantee unique IDs. Various
	// consensus types have an associated ID, calculated by hashing the data
	// contained in the type. By prepending the data with Specifier, we can
	// guarantee that distinct types will never produce the same hash.
	Specifier [SpecifierLen]byte

	// A ShortSpecifier is identical to a Specifier, but shorter in size.
	ShortSpecifier [ShortSpecifierLen]byte
)

// specifierMap is used for tracking unique specifiers
var specifierMap = newSpecifierMap()

// NewSpecifier returns a specifier for given name, a specifier can only be 16
// bytes so we panic if the given name is too long.
func NewSpecifier(name string) Specifier {
	if err := validateSpecifier(name, SpecifierLen); err != nil {
		panic(err.Error())
	}
	if _, ok := specifierMap[name]; ok {
		err := fmt.Sprint("ERROR: specifier name already in use ", name)
		panic(err)
	}
	specifierMap[name] = struct{}{}
	var s Specifier
	copy(s[:], name)
	return s
}

// NewShortSpecifier returns a short specifier for given name, a specifier can
// only be 8 bytes so we panic if the given name is too long.
func NewShortSpecifier(name string) Specifier {
	if err := validateSpecifier(name, ShortSpecifierLen); err != nil {
		panic(err.Error())
	}
	if _, ok := specifierMap[name]; ok {
		err := fmt.Sprint("ERROR: specifier name already in use ", name)
		panic(err)
	}
	specifierMap[name] = struct{}{}
	var s Specifier
	copy(s[:], name)
	return s
}

// newSpecifierMap makes a new map for tracking specifiers
func newSpecifierMap() map[string]struct{} {
	return make(map[string]struct{})
}

// MarshalText implements the TextMarshaler interface
func (t Specifier) MarshalText() (text []byte, err error) {
	return t[:], nil
}

// UnmarshalText implements the TextUnmarshaler interface
func (t *Specifier) UnmarshalText(text []byte) error {
	// Note that we validate using SpecifierLen, in the case of short specifiers
	// this validation won't error for short specifiers that are too long in
	// size.
	if err := validateSpecifier(string(text), SpecifierLen); err != nil {
		return err
	}
	copy(t[:], text)
	return nil
}

// validateSpecifier performs validation checks on the specifier name, it panics
// when the input is invalid seeing we want to catch this on runtime.
func validateSpecifier(name string, length int) error {
	if !isASCII(name) {
		return errors.New("ERROR: specifier has to be ASCII")
	}
	if len(name) > length {
		return errors.New("ERROR: specifier max length exceeded")
	}
	return nil
}

// isASCII returns whether or not the given string contains only ASCII
// characters
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
