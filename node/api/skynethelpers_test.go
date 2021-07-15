package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/julienschmidt/httprouter"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skykey"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter"
)

// testHTTPWriter is a dummy http.ResponseWriter for testing.
type testHTTPWriter struct {
	statusCode int
	write      []byte
	header     http.Header
}

// Header implements http.ResponseWriter.
func (tw *testHTTPWriter) Header() http.Header {
	return tw.header
}

// Write implements http.ResponseWriter.
func (tw *testHTTPWriter) Write(b []byte) (int, error) {
	tw.write = bytes.Trim(b, "\n")
	return len(b), nil
}

// WriteHeader implements http.ResponseWriter.
func (tw *testHTTPWriter) WriteHeader(statusCode int) {
	tw.statusCode = statusCode
}

// newTestHTTPWriter creates a new testHTTPWriter.
func newTestHTTPWriter() *testHTTPWriter {
	return &testHTTPWriter{
		header: make(http.Header),
	}
}

// TestHandleSkynetError is a unit test for handleSkynetError.
func TestHandleSkynetError(t *testing.T) {
	tw := newTestHTTPWriter()

	tests := []struct {
		err        error
		statusCode int
	}{
		{
			err:        renter.ErrSkylinkBlocked,
			statusCode: http.StatusUnavailableForLegalReasons,
		},
		{
			err:        renter.ErrRootNotFound,
			statusCode: http.StatusNotFound,
		},
		{
			err:        renter.ErrRegistryEntryNotFound,
			statusCode: http.StatusNotFound,
		},
		{
			err:        renter.ErrRegistryLookupTimeout,
			statusCode: http.StatusNotFound,
		},
		{
			err:        skymodules.ErrMalformedSkylink,
			statusCode: http.StatusBadRequest,
		},
		{
			err:        renter.ErrInvalidSkylinkVersion,
			statusCode: http.StatusBadRequest,
		},
		{
			err:        errors.New("other"),
			statusCode: http.StatusInternalServerError,
		},
	}

	prefix := "foo"
	for _, test := range tests {
		handleSkynetError(tw, prefix, test.err)

		expectedErr := Error{
			Message: fmt.Sprintf("%v: %v", prefix, test.err),
		}
		errBytes, err := json.Marshal(expectedErr)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(tw.write, errBytes) {
			t.Logf("written '%v'", string(tw.write))
			t.Logf("expected '%v'", string(errBytes))
			t.Fatal("wrong error")
		}
		if test.statusCode != tw.statusCode {
			t.Log("written", tw.statusCode)
			t.Log("expected", test.statusCode)
			t.Fatal("wrong status", test.statusCode, tw.statusCode)
		}
	}
}

// TestSkynetHelpers is a convenience function that wraps all of the Skynet
// helper tests, this ensures these tests are ran when supplying `-run
// TestSkynet` from the command line.
func TestSkynetHelpers(t *testing.T) {
	t.Run("BuildETag", testBuildETag)
	t.Run("ParseSkylinkURL", testParseSkylinkURL)
	t.Run("ParseUploadRequestParameters", testParseUploadRequestParameters)
}

// testBuildETag verifies the functionality of the buildETag helper function
func testBuildETag(t *testing.T) {
	t.Parallel()

	// base case
	path := "/"
	format := skymodules.SkyfileFormatNotSpecified
	var skylink skymodules.Skylink
	err := skylink.LoadString("AACogzrAimYPG42tDOKhS3lXZD8YvlF8Q8R17afe95iV2Q")
	if err != nil {
		t.Fatal(err)
	}

	eTag := buildETag(skylink, "GET", path, format)
	if eTag != "ccc494ae022f451a74fbe6dae21a7ab07e6a14ab1d13084382f72cc6cd6bf55f" {
		t.Fatal("unexpected output")
	}

	// adjust URL and expect different hash value
	path = "/foo"
	eTag2 := buildETag(skylink, "GET", path, format)
	if eTag2 == "" || eTag2 == eTag {
		t.Fatal("unexpected output")
	}

	// adjust query and expect different hash value
	format = skymodules.SkyfileFormatZip
	eTag3 := buildETag(skylink, "GET", path, format)
	if eTag3 == "" || eTag3 == eTag2 {
		t.Fatal("unexpected output")
	}

	// adjust skylink and expect different hash value
	err = skylink.LoadString("BBCogzrAimYPG42tDOKhS3lXZD8YvlF8Q8R17afe95iV2Q")
	if err != nil {
		t.Fatal(err)
	}
	eTag4 := buildETag(skylink, "GET", path, format)
	if eTag4 == "" || eTag4 == eTag3 {
		t.Fatal("unexpected output")
	}

	// adjust method and expect different hash value
	err = skylink.LoadString("BBCogzrAimYPG42tDOKhS3lXZD8YvlF8Q8R17afe95iV2Q")
	if err != nil {
		t.Fatal(err)
	}
	eTag5 := buildETag(skylink, "HEAD", path, format)
	if eTag5 == "" || eTag5 == eTag4 {
		t.Fatal("unexpected output")
	}
}

// testParseSkylinkURL is a table test for the parseSkylinkUrl function.
func testParseSkylinkURL(t *testing.T) {
	tests := []struct {
		name                 string
		strToParse           string
		skylink              string
		skylinkStringNoQuery string
		path                 string
		errMsg               string
	}{
		{
			name:                 "no path",
			strToParse:           "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w",
			skylink:              "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w",
			skylinkStringNoQuery: "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w",
			path:                 "/",
			errMsg:               "",
		},
		{
			name:                 "no path with query",
			strToParse:           "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w?foo=bar",
			skylink:              "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w",
			skylinkStringNoQuery: "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w",
			path:                 "/",
			errMsg:               "",
		},
		{
			name:                 "with path to file",
			strToParse:           "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w/foo/bar.baz",
			skylink:              "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w",
			skylinkStringNoQuery: "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w/foo/bar.baz",
			path:                 "/foo/bar.baz",
			errMsg:               "",
		},
		{
			name:                 "with path to dir with trailing slash",
			strToParse:           "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w/foo/bar/",
			skylink:              "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w",
			skylinkStringNoQuery: "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w/foo/bar/",
			path:                 "/foo/bar/",
			errMsg:               "",
		},
		{
			name:                 "with path to dir without trailing slash",
			strToParse:           "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w/foo/bar",
			skylink:              "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w",
			skylinkStringNoQuery: "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w/foo/bar",
			path:                 "/foo/bar",
			errMsg:               "",
		},
		{
			name:                 "with path to file with query",
			strToParse:           "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w/foo/bar.baz?foobar=nope",
			skylink:              "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w",
			skylinkStringNoQuery: "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w/foo/bar.baz",
			path:                 "/foo/bar.baz",
			errMsg:               "",
		},
		{
			name:                 "with path to dir with query with trailing slash",
			strToParse:           "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w/foo/bar/?foobar=nope",
			skylink:              "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w",
			skylinkStringNoQuery: "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w/foo/bar/",
			path:                 "/foo/bar/",
			errMsg:               "",
		},
		{
			name:                 "with path to dir with query without trailing slash",
			strToParse:           "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w/foo/bar?foobar=nope",
			skylink:              "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w",
			skylinkStringNoQuery: "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w/foo/bar",
			path:                 "/foo/bar",
			errMsg:               "",
		},
		{
			// Test URL-decoding the path.
			name:                 "with path to dir containing both a query and an encoded '?'",
			strToParse:           "/IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w/foo%3Fbar?foobar=nope",
			skylink:              "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w",
			skylinkStringNoQuery: "IAC6CkhNYuWZqMVr1gob1B6tPg4MrBGRzTaDvAIAeu9A9w/foo%3Fbar",
			path:                 "/foo?bar",
			errMsg:               "",
		},
		{
			name:                 "invalid skylink",
			strToParse:           "invalid_skylink/foo/bar?foobar=nope",
			skylink:              "",
			skylinkStringNoQuery: "",
			path:                 "",
			errMsg:               skymodules.ErrSkylinkIncorrectSize.Error(),
		},
		{
			name:                 "empty input",
			strToParse:           "",
			skylink:              "",
			skylinkStringNoQuery: "",
			path:                 "",
			errMsg:               skymodules.ErrSkylinkIncorrectSize.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skylink, skylinkStringNoQuery, path, err := parseSkylinkURL(tt.strToParse, "/skynet/skylink/")
			// Is there an actual or expected error?
			if err != nil || tt.errMsg != "" {
				// Actual err should contain expected err.
				if err == nil || !strings.Contains(err.Error(), tt.errMsg) {
					t.Fatalf("Expected error '%s', got %v\n", tt.errMsg, err)
				} else {
					// The errors match, so the test case passes.
					return
				}
			}
			if skylink.String() != tt.skylink {
				t.Fatalf("Expected skylink '%v', got '%v'\n", tt.skylink, skylink)
			}
			if skylinkStringNoQuery != tt.skylinkStringNoQuery {
				t.Fatalf("Expected skylinkStringNoQuery '%v', got '%v'\n", tt.skylinkStringNoQuery, skylinkStringNoQuery)
			}
			if path != tt.path {
				t.Fatalf("Expected path '%v', got '%v'\n", tt.path, path)
			}
		})
	}
}

// testParseUploadRequestParameters verifies the functionality of
// 'parseUploadHeadersAndRequestParameters'.
func testParseUploadRequestParameters(t *testing.T) {
	t.Parallel()

	// create a siapath
	siapath, err := skymodules.NewSiaPath(t.Name())
	if err != nil {
		t.Fatal(err)
	}

	// buildRequest is a helper function that creates a request object
	buildRequest := func(query url.Values, headers http.Header) *http.Request {
		req, err := http.NewRequest("POST", fmt.Sprintf("/skynet/skyfile/%s?%s", siapath.String(), query.Encode()), nil)
		if err != nil {
			t.Fatal("Could not create request", err)
		}

		for k, v := range headers {
			for _, vv := range v {
				req.Header.Add(k, vv)
			}
		}
		return req
	}

	// parseRequest simply wraps 'parseUploadHeadersAndRequestParameters' to
	// avoid handling the error for every case
	parseRequest := func(req *http.Request, ps httprouter.Params) (*skyfileUploadHeaders, *skyfileUploadParams) {
		// if content type is not set, default to a binary stream
		if req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/octet-stream")
		}
		headers, params, err := parseUploadHeadersAndRequestParameters(req, ps)
		if err != nil {
			t.Fatal("Unexpected error", err)
		}
		return headers, params
	}

	// create empty router params
	param := httprouter.Param{Key: "siapath", Value: siapath.String()}
	defaultParams := httprouter.Params{param}

	trueStr := []string{fmt.Sprintf("%t", true)}
	contentTypeStr := []string{"multipart/form-data; boundary=---------------------------9051914041544843365972754266"}

	// verify 'Skynet-Disable-Force'
	hdrs := http.Header{"Skynet-Disable-Force": trueStr}
	req := buildRequest(url.Values{}, hdrs)
	headers, _ := parseRequest(req, defaultParams)
	if !headers.disableForce {
		t.Fatal("Unexpected")
	}

	// verify 'Skynet-Disable-Force' - combo with 'force'
	req = buildRequest(url.Values{"force": trueStr}, hdrs)
	_, _, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil {
		t.Fatal("Unexpected")
	}

	// verify 'Content-Type'
	req = buildRequest(url.Values{}, http.Header{"Content-Type": []string{"text/html"}})
	headers, _ = parseRequest(req, defaultParams)
	if headers.mediaType != "text/html" {
		t.Fatal("Unexpected")
	}

	// verify 'basechunkredundancy'
	req = buildRequest(url.Values{"basechunkredundancy": []string{fmt.Sprintf("%v", 2)}}, http.Header{})
	_, params := parseRequest(req, defaultParams)
	if params.baseChunkRedundancy != uint8(2) {
		t.Fatal("Unexpected")
	}

	// verify 'convertpath'
	req = buildRequest(url.Values{"convertpath": []string{"/foo/bar"}}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	if params.convertPath != "/foo/bar" {
		t.Fatal("Unexpected")
	}

	// verify 'convertpath' - combo with 'filename
	req = buildRequest(url.Values{"convertpath": []string{"/foo/bar"}, "filename": []string{"foo.txt"}}, http.Header{})
	_, _, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil {
		t.Fatal("Unexpected")
	}

	// verify 'defaultpath'
	req = buildRequest(url.Values{"defaultpath": []string{"/foo/bar.txt"}}, http.Header{"Content-Type": contentTypeStr})
	_, params = parseRequest(req, defaultParams)
	if params.defaultPath != "/foo/bar.txt" {
		t.Fatal("Unexpected")
	}

	// verify 'defaultpath' - combo with a non-multipart request
	req = buildRequest(url.Values{"defaultpath": []string{"/foo/bar.txt"}}, http.Header{"Content-Type": []string{"text/html"}})
	_, _, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil {
		t.Fatal("Unexpected")
	}

	// verify 'disabledefaultpath'
	req = buildRequest(url.Values{"disabledefaultpath": trueStr}, http.Header{"Content-Type": contentTypeStr})
	_, params = parseRequest(req, defaultParams)
	if !params.disableDefaultPath {
		t.Fatal("Unexpected")
	}

	// verify 'disabledefaultpath' - combo with 'defaultpath'
	req = buildRequest(url.Values{"defaultpath": []string{"/foo/bar.txt"}, "disabledefaultpath": trueStr}, http.Header{"Content-Type": contentTypeStr})
	_, _, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil {
		t.Fatal("Unexpected")
	}

	// verify 'disabledefaultpath' - combo with a non-multipart request
	req = buildRequest(url.Values{"disabledefaultpath": trueStr}, http.Header{"Content-Type": []string{"text/html"}})
	_, _, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil {
		t.Fatal("Unexpected")
	}

	// verify 'dirresmode'
	req = buildRequest(url.Values{"dirresmode": []string{skymodules.DirResModeStandard}}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	if params.dirResMode != skymodules.DirResModeStandard {
		t.Fatal("Unexpected")
	}
	req = buildRequest(url.Values{"dirresmode": []string{skymodules.DirResModeWeb}}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	if params.dirResMode != skymodules.DirResModeWeb {
		t.Fatal("Unexpected")
	}
	req = buildRequest(url.Values{"dirresmode": []string{""}}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	if params.dirResMode != skymodules.DirResModeStandard {
		t.Fatal("Unexpected")
	}
	req = buildRequest(url.Values{"dirresmode": []string{"anything_else"}}, http.Header{})
	_, params, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil {
		t.Fatal("Unexpected")
	}

	// verify 'dirresnotfound'
	req = buildRequest(url.Values{"dirresmode": []string{skymodules.DirResModeWeb}, "dirresnotfound": []string{"404.html"}}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	// we also expect the leading slash to be auto-added:
	if params.dirResNotFound != "/404.html" {
		t.Fatal("Unexpected")
	}
	req = buildRequest(url.Values{"dirresnotfound": []string{""}}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	if params.dirResNotFound != "" {
		t.Fatal("Unexpected")
	}
	req = buildRequest(url.Values{"dirresmode": []string{skymodules.DirResModeStandard}, "dirresnotfound": []string{"404.html"}}, http.Header{})
	_, params, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil {
		t.Fatal("Unexpected")
	}

	// verify 'dirresnotfoundcode'
	req = buildRequest(url.Values{"dirresmode": []string{skymodules.DirResModeWeb}, "dirresnotfoundcode": []string{"200"}}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	if params.dirResNotFoundCode != http.StatusOK {
		t.Fatal("Unexpected")
	}
	req = buildRequest(url.Values{"dirresmode": []string{skymodules.DirResModeStandard}, "dirresnotfoundcode": []string{"200"}}, http.Header{})
	_, params, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil {
		t.Fatal("Unexpected")
	}

	// verify that 'dirresmode' 'web' cannot be combined with 'defaultpath' or
	// 'disabledefaultpath'
	req = buildRequest(url.Values{"dirresmode": []string{skymodules.DirResModeWeb}, "defaultpath": []string{"/index.html"}}, http.Header{})
	_, params, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil || !errors.Contains(err, skymodules.ErrInvalidDirectoryResolution) {
		t.Fatalf("Expected error '%s', got %s", skymodules.ErrInvalidDirectoryResolution, err)
	}
	req = buildRequest(url.Values{"dirresmode": []string{skymodules.DirResModeWeb}, "disabledefaultpath": []string{"true"}}, http.Header{})
	_, params, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil || !errors.Contains(err, skymodules.ErrInvalidDirectoryResolution) {
		t.Fatalf("Expected error '%s', got %s", skymodules.ErrInvalidDirectoryResolution, err)
	}

	// verify 'dryrun'
	req = buildRequest(url.Values{"dryrun": trueStr}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	if !params.dryRun {
		t.Fatal("Unexpected")
	}

	// verify 'filename'
	req = buildRequest(url.Values{"filename": []string{"foo.txt"}}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	if params.filename != "foo.txt" {
		t.Fatal("Unexpected")
	}

	// verify 'force'
	req = buildRequest(url.Values{"force": trueStr}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	if !params.force {
		t.Fatal("Unexpected")
	}

	// verify 'force' - combo with 'dryrun
	req = buildRequest(url.Values{"force": trueStr, "dryrun": trueStr}, http.Header{})
	_, _, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil {
		t.Fatal("Unexpected")
	}

	// verify 'mode'
	req = buildRequest(url.Values{"mode": []string{fmt.Sprintf("%o", os.FileMode(0644))}}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	if params.mode != os.FileMode(0644) {
		t.Fatal("Unexpected")
	}

	// verify 'root'
	req = buildRequest(url.Values{"root": trueStr}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	if !params.root {
		t.Fatal("Unexpected")
	}

	// verify 'siapath' (no root)
	req = buildRequest(url.Values{}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	expected, err := skymodules.SkynetFolder.Join(siapath.String())
	if err != nil || params.siaPath != expected {
		t.Fatal("Unexpected", err)
	}

	// verify 'siapath' (at root)
	req = buildRequest(url.Values{"root": trueStr}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	if params.siaPath != siapath {
		t.Fatal("Unexpected")
	}

	// create a test skykey
	km, err := skykey.NewSkykeyManager(build.TempDir("skykey", t.Name()))
	if err != nil {
		t.Fatal(err)
	}
	key, err := km.CreateKey("testkey", skykey.TypePublicID)
	if err != nil {
		t.Fatal(err)
	}
	keyIdStr := key.ID().ToString()

	// verify 'skykeyname'
	req = buildRequest(url.Values{"skykeyname": []string{key.Name}}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	if params.skyKeyName != key.Name {
		t.Fatal("Unexpected")
	}

	// verify 'skykeyid'
	req = buildRequest(url.Values{"skykeyid": []string{keyIdStr}}, http.Header{})
	_, params = parseRequest(req, defaultParams)
	if params.skyKeyID.ToString() != keyIdStr {
		t.Fatal("Unexpected")
	}

	// verify 'skykeyid' - combo with 'skykeyname'
	req = buildRequest(url.Values{"skykeyname": []string{key.Name}, "skykeyid": []string{key.ID().ToString()}}, http.Header{})
	_, _, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil {
		t.Fatal("Unexpected")
	}
}

// TestAttachRegistryEntryProof is a unit test for attachRegistryEntryProof.
func TestAttachRegistryEntryProof(t *testing.T) {
	var h1 crypto.Hash
	fastrand.Read(h1[:])
	var h2 crypto.Hash
	fastrand.Read(h2[:])
	entries := []skymodules.RegistryEntry{
		{
			SignedRegistryValue: modules.NewSignedRegistryValue(h1, fastrand.Bytes(10), fastrand.Uint64n(100), crypto.Signature{}, modules.RegistryTypeWithoutPubkey),
		},
		{
			SignedRegistryValue: modules.NewSignedRegistryValue(h2, fastrand.Bytes(10), fastrand.Uint64n(100), crypto.Signature{}, modules.RegistryTypeWithoutPubkey),
		},
	}

	// Create the proof manually.
	proofChain := make([]RegistryHandlerGET, 0, len(entries))
	for _, srv := range entries {
		proofChain = append(proofChain, RegistryHandlerGET{
			Data:      hex.EncodeToString(srv.Data),
			DataKey:   srv.Tweak,
			Revision:  srv.Revision,
			PublicKey: srv.PubKey,
			Signature: hex.EncodeToString(srv.Signature[:]),
			Type:      srv.Type,
		})
	}
	expectedProof, err := json.Marshal(proofChain)
	if err != nil {
		t.Fatal(err)
	}

	// Attach the proof
	w := newTestHTTPWriter()
	header := w.Header()
	attachRegistryEntryProof(w, entries)

	// Get the attached proof.
	proof := header.Get("Proof")

	// Should match the expected proof.
	if proof != string(expectedProof) {
		t.Log(proof)
		t.Log(string(expectedProof))
		t.Fatal("proof doesn't match expectation")
	}
}

// TestCustomStatusResponseWriter ensures customStatusResponseWriter replaces
// http.StatusOK with the custom status code provided and leaves the other
// status codes untouched.
func TestCustomStatusResponseWriter(t *testing.T) {
	tw := newTestHTTPWriter()
	csw := newCustomStatusResponseWriter(tw, http.StatusTeapot)

	tests := []struct {
		sentStatusCode     int
		expectedStatusCode int
	}{
		{sentStatusCode: http.StatusOK, expectedStatusCode: http.StatusTeapot},
		{sentStatusCode: http.StatusNoContent, expectedStatusCode: http.StatusNoContent},
		{sentStatusCode: http.StatusPartialContent, expectedStatusCode: http.StatusPartialContent},
		{sentStatusCode: http.StatusBadRequest, expectedStatusCode: http.StatusBadRequest},
		{sentStatusCode: http.StatusNotFound, expectedStatusCode: http.StatusNotFound},
	}

	for _, tt := range tests {
		csw.WriteHeader(tt.sentStatusCode)

		if tw.statusCode != tt.expectedStatusCode {
			t.Fatalf("Expected status code %d, got %d", tt.expectedStatusCode, tw.statusCode)
		}
	}
}
