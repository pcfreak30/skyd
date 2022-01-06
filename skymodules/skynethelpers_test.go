package skymodules

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"go.sia.tech/siad/crypto"
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
	t.Run("BuildBaseSectorExtension", testBuildBaseSectorExtension)
	t.Run("ResolveCompressedDataOffsetLength", testTranslateBaseSectorExtensionOffset)
	t.Run("ExpectedFanoutBytes", testExpectedFanoutBytes)
}

// testExpectedFanoutBytes is a unit test for ExpectedFanoutBytes.
func testExpectedFanoutBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		fileSize           uint64
		fanoutDataPieces   int
		fanoutParityPieces int
		ct                 crypto.CipherType
		result             uint64
	}{
		// 1 of 10 tests
		{
			name:               "1b-1dp-9pp-plain",
			fileSize:           1,
			fanoutDataPieces:   1,
			fanoutParityPieces: 9,
			ct:                 crypto.TypePlain,
			result:             crypto.HashSize,
		},
		{
			name:               "1b-1dp-9pp-encrypted",
			fileSize:           1,
			fanoutDataPieces:   1,
			fanoutParityPieces: 9,
			ct:                 crypto.TypeThreefish,
			result:             crypto.HashSize * 10,
		},
		{
			name:               "2chunks-1dp-9pp-plain",
			fileSize:           ChunkSize(crypto.TypePlain, 1) + 1,
			fanoutDataPieces:   1,
			fanoutParityPieces: 9,
			ct:                 crypto.TypePlain,
			result:             2 * crypto.HashSize,
		},
		{
			name:               "2chunks-1dp-9pp-encrypted",
			fileSize:           ChunkSize(crypto.TypeThreefish, 1) + 1,
			fanoutDataPieces:   1,
			fanoutParityPieces: 9,
			ct:                 crypto.TypeThreefish,
			result:             2 * 10 * crypto.HashSize,
		},
		// 10 of 30 tests
		{
			name:               "1b-10dp-20pp-plain",
			fileSize:           1,
			fanoutDataPieces:   10,
			fanoutParityPieces: 20,
			ct:                 crypto.TypePlain,
			result:             30 * crypto.HashSize,
		},
		{
			name:               "1b-10dp-20pp-encrypted",
			fileSize:           1,
			fanoutDataPieces:   10,
			fanoutParityPieces: 20,
			ct:                 crypto.TypeThreefish,
			result:             30 * crypto.HashSize,
		},
		{
			name:               "2chunks-10dp-20pp-plain",
			fileSize:           ChunkSize(crypto.TypePlain, 10) + 1,
			fanoutDataPieces:   10,
			fanoutParityPieces: 20,
			ct:                 crypto.TypePlain,
			result:             2 * 30 * crypto.HashSize,
		},
		{
			name:               "2chunks-10dp-20pp-encrypted",
			fileSize:           ChunkSize(crypto.TypeThreefish, 10) + 1,
			fanoutDataPieces:   10,
			fanoutParityPieces: 20,
			ct:                 crypto.TypeThreefish,
			result:             2 * 30 * crypto.HashSize,
		},
		{
			name:               "asdf",
			fileSize:           1090519040,
			fanoutDataPieces:   10,
			fanoutParityPieces: 30,
			ct:                 crypto.TypePlain,
			result:             0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			l := ExpectedFanoutBytesLen(test.fileSize, test.fanoutDataPieces, test.fanoutParityPieces, test.ct)
			if l != test.result {
				t.Fatalf("invalid length: expected %v got %v", test.result, l)
			}
		})
	}
}

// testTranslateBaseSectorExtensionOffset is a unit test for
// TranslateBaseSectorExtensionOffset.
func testTranslateBaseSectorExtensionOffset(t *testing.T) {
	hashesPerSector := modules.SectorSize / crypto.HashSize

	tests := []struct {
		name     string
		offset   uint64
		length   uint64
		dataSize uint64
		maxSize  uint64

		result           []ChunkSpan
		translatedOffset uint64
	}{
		{
			name:             "NoCompression",
			offset:           1,
			length:           crypto.HashSize,
			dataSize:         crypto.HashSize,
			maxSize:          crypto.HashSize,
			result:           []ChunkSpan{},
			translatedOffset: 1,
		},
		{
			name:     "FullSector-DownloadAll-SingleHash",
			offset:   0,
			length:   modules.SectorSize,
			dataSize: modules.SectorSize,
			maxSize:  crypto.HashSize,
			result: []ChunkSpan{
				{
					MinIndex: 0,
					MaxIndex: 0,
				},
			},
			translatedOffset: 0,
		},
		{
			name:     "TwoFullSectors-DownloadAll-TwoHashes",
			offset:   0,
			length:   2 * modules.SectorSize,
			dataSize: 2 * modules.SectorSize,
			maxSize:  2 * crypto.HashSize,
			result: []ChunkSpan{
				{
					MinIndex: 0,
					MaxIndex: 1,
				},
			},
			translatedOffset: 0,
		},
		{
			name:     "TwoFullSectors-DownloadFirstHalf-TwoHashes",
			offset:   0,
			length:   modules.SectorSize,
			dataSize: 2 * modules.SectorSize,
			maxSize:  2 * crypto.HashSize,
			result: []ChunkSpan{
				{
					MinIndex: 0,
					MaxIndex: 0,
				},
			},
			translatedOffset: 0,
		},
		{
			name:     "TwoFullSectors-DownloadSecondHalf-TwoHashes",
			offset:   modules.SectorSize,
			length:   modules.SectorSize,
			dataSize: 2 * modules.SectorSize,
			maxSize:  2 * crypto.HashSize,
			result: []ChunkSpan{
				{
					MinIndex: 1,
					MaxIndex: 1,
				},
			},
			translatedOffset: 0,
		},
		{
			name:     "Depth1-FullDownload-SingleHash",
			offset:   0,
			length:   modules.SectorSize * hashesPerSector,
			dataSize: modules.SectorSize * hashesPerSector,
			maxSize:  crypto.HashSize,
			result: []ChunkSpan{
				{
					MinIndex: 0,
					MaxIndex: 0,
				},
				{
					MinIndex: 0,
					MaxIndex: hashesPerSector - 1,
				},
			},
			translatedOffset: 0,
		},
		{
			name:     "Depth1-FullDownload-TwoHashes",
			offset:   0,
			length:   modules.SectorSize * hashesPerSector,
			dataSize: modules.SectorSize * hashesPerSector,
			maxSize:  2 * crypto.HashSize,
			result: []ChunkSpan{
				{
					MinIndex: 0,
					MaxIndex: 0,
				},
				{
					MinIndex: 0,
					MaxIndex: hashesPerSector - 1,
				},
			},
			translatedOffset: 0,
		},
		{
			name:     "Depth1-FirstHalf-SingleHash",
			offset:   0,
			length:   modules.SectorSize * hashesPerSector / 2,
			dataSize: modules.SectorSize * hashesPerSector,
			maxSize:  crypto.HashSize,
			result: []ChunkSpan{
				{
					MinIndex: 0,
					MaxIndex: 0,
				},
				{
					MinIndex: 0,
					MaxIndex: hashesPerSector/2 - 1,
				},
			},
			translatedOffset: 0,
		},
		{
			name:     "Depth1-SecondHalf-SingleHash",
			offset:   modules.SectorSize * hashesPerSector / 2,
			length:   modules.SectorSize * hashesPerSector / 2,
			dataSize: modules.SectorSize * hashesPerSector,
			maxSize:  crypto.HashSize,
			result: []ChunkSpan{
				{
					MinIndex: 0,
					MaxIndex: 0,
				},
				{
					MinIndex: hashesPerSector / 2,
					MaxIndex: hashesPerSector - 1,
				},
			},
			translatedOffset: 0,
		},
		{
			name:     "Depth1-LastByte-SingleHash",
			offset:   modules.SectorSize*hashesPerSector - 1,
			length:   1,
			dataSize: modules.SectorSize * hashesPerSector,
			maxSize:  crypto.HashSize,
			result: []ChunkSpan{
				{
					MinIndex: 0,
					MaxIndex: 0,
				},
				{
					MinIndex: hashesPerSector - 1,
					MaxIndex: hashesPerSector - 1,
				},
			},
			translatedOffset: modules.SectorSize - 1,
		},
		{
			name:     "Depth2-LastByte-SingleHash",
			offset:   modules.SectorSize*hashesPerSector*hashesPerSector - 1,
			length:   1,
			dataSize: modules.SectorSize * hashesPerSector * hashesPerSector,
			maxSize:  crypto.HashSize,
			result: []ChunkSpan{
				{
					MinIndex: 0,
					MaxIndex: 0,
				},
				{
					MinIndex: hashesPerSector - 1,
					MaxIndex: hashesPerSector - 1,
				},
				{
					MinIndex: hashesPerSector - 1,
					MaxIndex: hashesPerSector - 1,
				},
			},
			translatedOffset: modules.SectorSize - 1,
		},
		{
			name:     "Depth2-MiddleByte-SingleHash",
			offset:   modules.SectorSize * hashesPerSector * hashesPerSector / 2,
			length:   1,
			dataSize: modules.SectorSize * hashesPerSector * hashesPerSector,
			maxSize:  crypto.HashSize,
			result: []ChunkSpan{
				{
					MinIndex: 0,
					MaxIndex: 0,
				},
				{
					MinIndex: hashesPerSector / 2,
					MaxIndex: hashesPerSector / 2,
				},
				{
					MinIndex: 0,
					MaxIndex: 0,
				},
			},
			translatedOffset: 0,
		},
	}
	// Run tests.
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			offset, offsets := TranslateBaseSectorExtensionOffset(test.offset, test.length, test.dataSize, test.maxSize)
			if !reflect.DeepEqual(test.result, offsets) {
				t.Log("wanted:", test.result)
				t.Log("got:", offsets)
				t.Fatal("wrong result")
			}
			if offset != test.translatedOffset {
				t.Fatalf("wrong offset: %v != %v", offset, test.translatedOffset)
			}
		})
	}
}

// testBuildBaseSectorExtension is a unit test for buildBaseSectorExtension.
func testBuildBaseSectorExtension(t *testing.T) {
	hashesPerSector := modules.SectorSize / crypto.HashSize

	tests := []struct {
		name     string
		dataSize int
		size     uint64

		fanoutSizes []int
	}{
		{
			// Test having less data than the size. That means no
			// compression is necessary.
			name:     "LessDataThanSize",
			dataSize: crypto.HashSize - 1,
			size:     crypto.HashSize,
		},
		{
			// Same amount of data as size should also not require
			// compression.
			name:     "SameDataAsSize",
			dataSize: crypto.HashSize,
			size:     crypto.HashSize,
		},
		{
			// Try more data than size. Should compress into a singe
			// sector.
			name:        "MoreDataThanSize",
			dataSize:    crypto.HashSize + 1, // more than one hash
			size:        crypto.HashSize,
			fanoutSizes: []int{crypto.HashSize},
		},
		{
			// Try a full sector of data. Should be compressed into
			// a single hash.
			name:        "FullSectorToSingleHash",
			dataSize:    int(modules.SectorSize),
			size:        crypto.HashSize,
			fanoutSizes: []int{crypto.HashSize},
		},
		{
			// Try a full sector of data. This time we have 2 hashes
			// of size remaining in the base sector. Since we can
			// fit all into a single sector still, we end up with
			// the same result as FullSectorToSingleHash.
			name:        "FullSectorToTwoHashes",
			dataSize:    int(modules.SectorSize),
			size:        2 * crypto.HashSize,
			fanoutSizes: []int{crypto.HashSize},
		},
		{
			// Try compressing two sectors of data into a single
			// hash. At first this will compress the data into 2
			// hashes (one for each sector). Since the base sector
			// only has room for 1 hash, those 2 hashes are
			// compressed again into a single one.
			name:        "TwoSectorsToSingleHash",
			dataSize:    2 * int(modules.SectorSize),
			size:        crypto.HashSize,
			fanoutSizes: []int{2 * crypto.HashSize, crypto.HashSize},
		},
		{
			// Try two sectors of data with 2 hashes of space. This
			// results in the same 2 hash fanout as before but it
			// doesn't need to be compressed further since it fits
			// into the remaining space.
			name:        "TwoSectorsToTwoHashes",
			dataSize:    2 * int(modules.SectorSize),
			size:        2 * crypto.HashSize,
			fanoutSizes: []int{2 * crypto.HashSize},
		},
		{
			// Try compressing hashesPerSector sectors of data into
			// a single hash. This means that after compressing the
			// initial data we have enough hashes to fill a full
			// sector. Which will be compressed again into a single
			// hash.
			name:        "HashesPerSectorSectorsToSingleHash",
			dataSize:    int(modules.SectorSize * hashesPerSector),
			size:        crypto.HashSize,
			fanoutSizes: []int{int(modules.SectorSize), crypto.HashSize},
		},
		{
			// Try compressing hashesPerSector sectors of data into
			// a two hashes. This means that after compressing the
			// initial data we have enough hashes to fill a full
			// sector. Which will be compressed again into a single
			// hash. So the result will be the same.
			name:        "HashesPerSectorSectorsToTwoHashes",
			dataSize:    int(modules.SectorSize * hashesPerSector),
			size:        2 * crypto.HashSize,
			fanoutSizes: []int{int(modules.SectorSize), crypto.HashSize},
		},
		{
			// Try compressing hashesPerSector sectors plus one
			// additional byte of data into a one hash. This means
			// that after compressing the initial data we have
			// enough hashes to fill a full sector plus one more.
			// Which will be compressed again into two hashes. Which
			// will be compressed into one.
			name:        "HashesPerSectorPlusOneSectorsToOneHash",
			dataSize:    int(modules.SectorSize*hashesPerSector + 1),
			size:        crypto.HashSize,
			fanoutSizes: []int{int(modules.SectorSize) + crypto.HashSize, 2 * crypto.HashSize, crypto.HashSize},
		},
		{
			// Try compressing hashesPerSector sectors plus one
			// additional byte of data into two hashes. This means
			// that after compressing the initial data we have
			// enough hashes to fill a full sector plus one more.
			// Which will be compressed again into two hashes. Which
			// is compressed enough to be put into the base sector.
			// name:
			// "HashesPerSectorPlusOneSectorsToOneHash",
			dataSize:    int(modules.SectorSize*hashesPerSector + 1),
			size:        2 * crypto.HashSize,
			fanoutSizes: []int{int(modules.SectorSize) + crypto.HashSize, 2 * crypto.HashSize},
		},
	}

	// Run tests.
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseSectorPart, uploadPart := buildBaseSectorExtension(fastrand.Bytes(test.dataSize), test.size)
			var fanouts [][]byte
			if baseSectorPart != nil {
				fanouts = append(uploadPart, baseSectorPart)
			}
			if len(fanouts) != len(test.fanoutSizes) {
				t.Fatalf("invalid fanouts length: %v != %v", len(fanouts), len(test.fanoutSizes))
			}
			for i, fanout := range fanouts {
				if len(fanout) != test.fanoutSizes[i] {
					t.Fatalf("%v: invalid fanout size: %v != %v", i, len(fanout), test.fanoutSizes[i])
				}
			}
			expectedUsedHashes, expectedDepth := BaseSectorExtensionSize(uint64(test.dataSize), test.size)
			if expectedDepth != uint64(len(fanouts)) {
				t.Fatalf("depth %v != %v", len(fanouts), expectedDepth)
			}
			if len(fanouts) > 0 {
				usedHashes := uint64(len(fanouts[len(fanouts)-1])) / crypto.HashSize
				if expectedUsedHashes != usedHashes {
					t.Fatalf("used hashes %v != %v", usedHashes, expectedUsedHashes)
				}
			}
		})
	}

	// Test sanity check edge case.
	t.Run("SizeSanityCheck", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "can't compress to a size smaller than a single hash") {
				t.Fatal("unexpected", r)
			}
		}()
		buildBaseSectorExtension(fastrand.Bytes(10), crypto.HashSize-1)
	})
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
			name:       "non html default path",
			subfiles:   subfiles("a.txt"),
			dpQuery:    "a.txt",
			dpExpected: "/a.txt",
			err:        "",
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
			err:        "skyfile has invalid default path which refers to a non-root file",
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
				t.Fatal("Unexpected default path", dp, subtest.dpExpected)
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
	if err == nil {
		t.Fatal("unexpected outcome")
	}

	// verify that tryfiles + defaultpath is an invalid combination
	invalid = metadata
	metadata.Subfiles["index.html"] = SkyfileSubfileMetadata{
		Filename: "index.html",
	}
	invalid.DefaultPath = "index.html"
	invalid.TryFiles = []string{"index.html"}
	err = ValidateSkyfileMetadata(invalid)
	if err == nil || !strings.Contains(err.Error(), "tryfiles are incompatible with defaultpath and disabledefaultpath") {
		t.Fatalf("unexpected outcome: %+v", err)
	}

	// verify valid tryfiles and errorpages
	valid := metadata
	valid.TryFiles = []string{"index.html"}
	valid.ErrorPages = map[int]string{
		404: "/404.html",
	}
	valid.Subfiles = SkyfileSubfiles{
		"404.html": SkyfileSubfileMetadata{
			Filename:    "404.html",
			ContentType: "text/html",
			Len:         1,
		},
	}
	err = ValidateSkyfileMetadata(valid)
	if err != nil {
		t.Fatalf("unexpected error %+v", err)
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
	valid = metadata
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
	if err == nil || !strings.Contains(err.Error(), "invalid length set on metadata - length: 1, totalLength: 10, subfiles: 1") {
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
	// No fanout
	layout.FanoutSize = 0
	layoutBytes = layout.Encode()
	randData = fastrand.Bytes(int(modules.SectorSize))
	copy(randData, layoutBytes)
	_, _, _, _, _, err := ParseSkyfileMetadata(randData)
	if errors.Contains(err, ErrMalformedBaseSector) {
		t.Fatal(err)
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

// TestValidateErrorPages ensures that ValidateErrorPages functions correctly.
func TestValidateErrorPages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ep   map[int]string
		sub  SkyfileSubfiles
		err  string
	}{
		{
			name: "test code under 400",
			ep:   map[int]string{399: "/399.html"},
			sub:  SkyfileSubfiles{},
			err:  "overriding status codes under 400 and above 599 is not supported",
		},
		{
			name: "test code at or above 400",
			ep:   map[int]string{400: "/400.html"},
			sub: SkyfileSubfiles{
				"400.html": SkyfileSubfileMetadata{},
			},
		},
		{
			name: "test code at or below 599",
			ep:   map[int]string{599: "/599.html"},
			sub: SkyfileSubfiles{
				"599.html": SkyfileSubfileMetadata{},
			},
		},
		{
			name: "test code above 599",
			ep:   map[int]string{600: "/600.html"},
			sub:  SkyfileSubfiles{},
			err:  "overriding status codes under 400 and above 599 is not supported",
		},
		{
			name: "test empty filename",
			ep:   map[int]string{404: ""},
			sub:  SkyfileSubfiles{},
			err:  "an errorpage cannot be an empty string, it needs to be a valid file name",
		},
		{
			name: "test relative filename",
			ep:   map[int]string{404: "404.html"},
			sub:  SkyfileSubfiles{},
			err:  "all errorpages need to have absolute paths",
		},
		{
			name: "test non-existent file",
			ep:   map[int]string{404: "/404.html"},
			sub:  SkyfileSubfiles{},
			err:  "all errorpage files must exist",
		},
		{
			name: "test a valid setup",
			ep:   map[int]string{404: "/404.html"},
			sub: SkyfileSubfiles{
				"404.html": SkyfileSubfileMetadata{},
			},
		},
	}

	for _, tt := range tests {
		err := ValidateErrorPages(tt.ep, tt.sub)
		if (err == nil && tt.err != "") || (err != nil && !strings.Contains(err.Error(), tt.err)) {
			t.Log("Failing test:", tt.name)
			t.Fatalf("Expected error '%s', got '%v'", tt.err, err)
		}
	}
}

// TestValidateTryFiles ensures that ValidateTryFiles functions correctly.
func TestValidateTryFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tf   []string
		sub  SkyfileSubfiles
		err  string
	}{
		{
			name: "test non-existent absolute path file",
			tf:   []string{"/index.html"},
			sub:  SkyfileSubfiles{},
			err:  "any absolute path tryfile in the list must exist",
		},
		{
			name: "test bad filename",
			tf:   []string{""},
			sub:  SkyfileSubfiles{},
			err:  "a tryfile cannot be an empty string, it needs to be a valid file name",
		},
		{
			name: "test non-existent relative path",
			tf:   []string{"index.html"},
			sub:  SkyfileSubfiles{},
			err:  "",
		},
		{
			name: "test single existent absolute path",
			tf:   []string{"/index.html"},
			sub: SkyfileSubfiles{
				"index.html": SkyfileSubfileMetadata{},
			},
			err: "",
		},
		{
			// this is pointless but allowed
			name: "test multiple absolute paths",
			tf:   []string{"/about.html", "/index.html"},
			sub: SkyfileSubfiles{
				"index.html": SkyfileSubfileMetadata{},
				"about.html": SkyfileSubfileMetadata{},
			},
			err: "only one absolute path tryfile is permitted",
		},
		{
			name: "test empty tryfiles",
			tf:   []string{},
			sub:  SkyfileSubfiles{},
			err:  "",
		},
	}

	for _, tt := range tests {
		err := ValidateTryFiles(tt.tf, tt.sub)
		if (err == nil && tt.err != "") || (err != nil && !strings.Contains(err.Error(), tt.err)) {
			t.Fatalf("Expected error '%s', got '%v'", tt.err, err)
		}
	}
}
