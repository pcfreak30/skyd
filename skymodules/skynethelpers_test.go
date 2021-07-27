package skymodules

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"go.sia.tech/siad/modules"
)

// TestSkynetHelpers is a convenience function that wraps all of the Skynet
// helper tests, this ensures these tests are ran when supplying `-run
// TestSkynet` from the command line.
func TestSkynetHelpers(t *testing.T) {
	t.Run("ValidateDefaultPath", testValidateDefaultPath)
	t.Run("ValidateSkyfileMetadata", testValidateSkyfileMetadata)
	t.Run("EnsurePrefix", testEnsurePrefix)
	t.Run("EnsureSuffix", testEnsureSuffix)
}

// testValidateDefaultPath ensures the functionality of 'validateDefaultPath'
func testValidateDefaultPath(t *testing.T) {
	t.Parallel()

	subfiles := func(filenames ...string) SkyfileSubfiles {
		md := make(SkyfileSubfiles)
		for _, fn := range filenames {
			md[fn] = SkyfileSubfileMetadata{Filename: fn}
		}
		return md
	}

	tests := []struct {
		name       string
		dpQuery    string
		dpExpected string
		subfiles   SkyfileSubfiles
		err        string
	}{
		{
			name:       "empty default path - no files",
			subfiles:   nil,
			dpQuery:    "",
			dpExpected: "",
			err:        "",
		},
		{
			name:       "no default path - files",
			subfiles:   subfiles("a.html"),
			dpQuery:    "",
			dpExpected: "",
			err:        "",
		},
		{
			name:       "existing default path",
			subfiles:   subfiles("a.html"),
			dpQuery:    "/a.html",
			dpExpected: "/a.html",
			err:        "",
		},
		{
			name:       "existing default path - multiple subfiles",
			subfiles:   subfiles("a.html", "b.html"),
			dpQuery:    "/a.html",
			dpExpected: "/a.html",
			err:        "",
		},
		{
			name:       "existing default path - ensure leading slash",
			subfiles:   subfiles("a.html"),
			dpQuery:    "a.html",
			dpExpected: "/a.html",
			err:        "",
		},
		{
			name:       "non existing default path",
			subfiles:   subfiles("b.html"),
			dpQuery:    "a.html",
			dpExpected: "",
			err:        "no such path",
		},
		{
			name:       "HTML file with extension 'htm' as default path",
			subfiles:   subfiles("a.htm"),
			dpQuery:    "a.htm",
			dpExpected: "/a.htm",
			err:        "",
		},
		{
			name:       "default path not at root",
			subfiles:   subfiles("a/b/c.html"),
			dpQuery:    "a/b/c.html",
			dpExpected: "",
			err:        "the default path must point to a file in the root directory of the skyfile",
		},
	}

	for _, subtest := range tests {
		t.Run(subtest.name, func(t *testing.T) {
			dp, err := validateDefaultPath(subtest.dpQuery, subtest.subfiles)
			if subtest.err != "" && !strings.Contains(err.Error(), subtest.err) {
				t.Fatal("Unexpected error", subtest.err)
			}
			if subtest.err == "" && err != nil {
				t.Fatal("Unexpected error", err)
			}
			if dp != subtest.dpExpected {
				t.Fatal("Unexpected default path")
			}
		})
	}
}

// testValidateSkyfileMetadata verifies the functionality of
// `ValidateSkyfileMetadata`
func testValidateSkyfileMetadata(t *testing.T) {
	t.Parallel()

	// happy case
	metadata := SkyfileMetadata{
		Filename: t.Name(),
		Length:   1,
		Subfiles: SkyfileSubfiles{
			"validkey": SkyfileSubfileMetadata{
				Filename: "validkey",
				Len:      1,
			},
		},
	}
	err := ValidateSkyfileMetadata(metadata)
	if err != nil {
		t.Fatal(err)
	}

	// verify invalid filename
	invalid := metadata
	invalid.Filename = "../../" + metadata.Filename
	err = ValidateSkyfileMetadata(invalid)
	if err == nil || !strings.Contains(err.Error(), "invalid filename provided") {
		t.Fatal("unexpected outcome")
	}

	// verify invalid subfile metadata
	invalid = metadata
	invalid.Subfiles = SkyfileSubfiles{
		"invalidkey": SkyfileSubfileMetadata{
			Filename: "keyshouldmatchfilename",
		},
	}
	err = ValidateSkyfileMetadata(invalid)
	if err == nil || !strings.Contains(err.Error(), "subfile name did not match") {
		t.Fatal("unexpected outcome")
	}

	// verify invalid subfile metadata
	invalid = metadata
	invalid.Subfiles = SkyfileSubfiles{
		"foo/../bar": SkyfileSubfileMetadata{
			Filename: "foo/../bar",
		},
	}
	err = ValidateSkyfileMetadata(invalid)
	if err == nil || !strings.Contains(err.Error(), "invalid filename provided for subfile") {
		t.Fatal("unexpected outcome")
	}

	// verify invalid default path
	invalid = metadata
	invalid.DefaultPath = "foo/../bar"
	err = ValidateSkyfileMetadata(invalid)
	if !errors.Contains(err, ErrInvalidDefaultPath) {
		t.Fatal("unexpected outcome")
	}

	invalid.DisableDefaultPath = true
	err = ValidateSkyfileMetadata(invalid)
	if err != nil {
		t.Fatal("unexpected outcome")
	}

	// verify invalid length
	invalid = metadata
	invalid.Subfiles = SkyfileSubfiles{
		"validkey": SkyfileSubfileMetadata{
			Filename: "validkey",
			Len:      1,
		},
		"validkey2": SkyfileSubfileMetadata{
			Filename: "validkey2",
			Len:      1,
			Offset:   1,
		},
	}
	invalid.Length = 1
	err = ValidateSkyfileMetadata(invalid)
	if err == nil || !strings.Contains(err.Error(), "invalid length set on metadata") {
		t.Fatal("unexpected outcome")
	}

	// verify invalid 0 length
	invalid = metadata
	invalid.Subfiles = SkyfileSubfiles{
		"validkey": SkyfileSubfileMetadata{
			Filename: "validkey",
			Len:      1,
		},
	}
	invalid.Length = 0
	invalid.Monetization = &Monetization{}
	err = ValidateSkyfileMetadata(invalid)
	if err == nil || !strings.Contains(err.Error(), "invalid length set on metadata") {
		t.Fatal("unexpected outcome")
	}

	// verify valid 0 length
	invalid = metadata
	invalid.Subfiles = SkyfileSubfiles{
		"validkey": SkyfileSubfileMetadata{
			Filename: "validkey",
			Len:      0,
		},
	}
	invalid.Length = 0
	err = ValidateSkyfileMetadata(invalid)
	if err != nil {
		t.Fatal("unexpected outcome")
	}

	// verify legacy file. It is valid since it only has a single
	// subfile and a zero length.
	valid := metadata
	valid.Subfiles = SkyfileSubfiles{
		"validkey": SkyfileSubfileMetadata{
			Filename: "validkey",
			Len:      10,
		},
	}
	valid.Length = 0
	err = ValidateSkyfileMetadata(valid)
	if err != nil {
		t.Fatal("unexpected outcome")
	}

	// verify legacy file. It is invalid since it only has a single subfile
	// and a non-zero length.
	invalid = metadata
	valid.Subfiles = SkyfileSubfiles{
		"validkey": SkyfileSubfileMetadata{
			Filename: "validkey",
			Len:      10,
		},
	}
	valid.Length = 1
	err = ValidateSkyfileMetadata(valid)
	if err == nil || !strings.Contains(err.Error(), "invalid length set on metadata - length: 1, totalLength: 10, subfiles: 1, monetized: false") {
		t.Fatal("unexpected outcome")
	}
}

// testEnsurePrefix ensures EnsurePrefix is properly adding prefixes.
func testEnsurePrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		str string
		pre string
		out string
	}{
		{"base", "pre", "prebase"},
		{"base", "", "base"},
		{"rebase", "pre", "prerebase"},
		{"", "pre", "pre"},
		{"", "", ""},
	}
	for _, tt := range tests {
		out := EnsurePrefix(tt.str, tt.pre)
		if out != tt.out {
			t.Errorf("Expected string %s and prefix %s to result in %s but got %s\n", tt.str, tt.pre, tt.out, out)
		}
	}
}

// testEnsureSuffix ensures EnsureSuffix is properly adding suffixes.
func testEnsureSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		str string
		suf string
		out string
	}{
		{"base", "suf", "basesuf"},
		{"base", "", "base"},
		{"basesu", "suf", "basesusuf"},
		{"", "suf", "suf"},
		{"", "", ""},
	}
	for _, tt := range tests {
		out := EnsureSuffix(tt.str, tt.suf)
		if out != tt.out {
			t.Errorf("Expected string %s and suffix %s to result in %s but got %s\n", tt.str, tt.suf, tt.out, out)
		}
	}
}

// TestParseSkyfileMetadata checks that the skyfile metadata parser correctly
// catches malformed skyfile layout data.
//
// NOTE: this test will become invalid once the skyfile metadata parser is able
// to fetch larger fanouts and larger metadata than what can fit in the base
// chunk.
func TestParseSkyfileMetadata(t *testing.T) {
	t.Parallel()
	// Try some chosen skyfile layouts.
	//
	// Standard layout, nothing tricky.
	layout := newTestSkyfileLayout()
	layoutBytes := layout.Encode()
	randData := fastrand.Bytes(int(modules.SectorSize))
	copy(randData, layoutBytes)
	ParseSkyfileMetadata(randData) // no error check, just want to know it doesn't panic
	// Overflow the fanout.
	layout.FanoutSize = math.MaxUint64 - 14e3 - 1
	layoutBytes = layout.Encode()
	randData = fastrand.Bytes(int(modules.SectorSize))
	copy(randData, layoutBytes)
	ParseSkyfileMetadata(randData) // no error check, just want to know it doesn't panic
	// Overflow the metadata size
	layout.MetadataSize = math.MaxUint64 - 75e3 - 1
	layout.FanoutSize = 75e3
	layoutBytes = layout.Encode()
	randData = fastrand.Bytes(int(modules.SectorSize))
	copy(randData, layoutBytes)
	ParseSkyfileMetadata(randData) // no error check, just want to know it doesn't panic
	// Make sure monetization is validated.
	sm := SkyfileMetadata{
		Filename: "test",
		Monetization: &Monetization{
			License: "", // invalid license
		},
	}
	smBytes, err := json.Marshal(sm)
	if err != nil {
		t.Fatal(err)
	}
	layout = SkyfileLayout{Version: 1, MetadataSize: uint64(len(smBytes))}
	layoutBytes = layout.Encode()
	copy(randData, layoutBytes)
	baseSector, _ := BuildBaseSector(layoutBytes, nil, smBytes, []byte{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _, _, err = ParseSkyfileMetadata(baseSector)
	if !errors.Contains(err, ErrUnknownLicense) {
		t.Fatal("wrong error:", err)
	}

	// Try a bunch of random data.
	for i := 0; i < 10e3; i++ {
		randData := fastrand.Bytes(int(modules.SectorSize))
		ParseSkyfileMetadata(randData) // no error check, just want to know it doesn't panic

		// Only do 1 iteration for short testing.
		if testing.Short() {
			t.SkipNow()
		}
	}
}
