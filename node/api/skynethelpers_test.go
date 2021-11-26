package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/julienschmidt/httprouter"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skykey"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"

	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter"
)

// testHTTPWriter is a dummy http.ResponseWriter for testing.
type testHTTPWriter struct {
	statusCode int
	write      []byte
	header     http.Header
}

// WrittenContent is a helper for checking what was written to the writer
func (tw *testHTTPWriter) WrittenContent() []byte {
	return tw.write
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
			err:        renter.ErrRegistryUpdateTimeout,
			statusCode: http.StatusRequestTimeout,
		},
		{
			err:        modules.ErrLowerRevNum,
			statusCode: http.StatusBadRequest,
		},
		{
			err:        modules.ErrInsufficientWork,
			statusCode: http.StatusBadRequest,
		},
		{
			err:        modules.ErrSameRevNum,
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
	t.Run("ParseDownloadRequestParameters", testParseDownloadRequestParameters)
	t.Run("ParseStatsType", testParseStatsType)
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

	eTag := buildETag(skylink, path, format)
	if eTag != "7b4d5f4aa61144f4ab0ca37da17238a93dc9a8d514a76d374b2557bf86c04d21" {
		t.Fatal("unexpected output")
	}

	// adjust URL and expect different hash value
	path = "/foo"
	eTag2 := buildETag(skylink, path, format)
	if eTag2 == "" || eTag2 == eTag {
		t.Fatal("unexpected output")
	}

	// adjust query and expect different hash value
	format = skymodules.SkyfileFormatZip
	eTag3 := buildETag(skylink, path, format)
	if eTag3 == "" || eTag3 == eTag2 {
		t.Fatal("unexpected output")
	}

	// adjust skylink and expect different hash value
	err = skylink.LoadString("BBCogzrAimYPG42tDOKhS3lXZD8YvlF8Q8R17afe95iV2Q")
	if err != nil {
		t.Fatal(err)
	}
	eTag4 := buildETag(skylink, path, format)
	if eTag4 == "" || eTag4 == eTag3 {
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
	if testing.Short() {
		t.SkipNow()
	}
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
	parseRequest := func(req *http.Request, ps httprouter.Params) (*skyfileUploadHeaders, *skyfileUploadParams, error) {
		// if content type is not set, default to a binary stream
		if req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/octet-stream")
		}
		headers, params, err := parseUploadHeadersAndRequestParameters(req, ps)
		if err != nil {
			return nil, nil, err
		}
		return headers, params, nil
	}

	// create empty router params
	param := httprouter.Param{Key: "siapath", Value: siapath.String()}
	defaultParams := httprouter.Params{param}

	trueStr := []string{fmt.Sprintf("%t", true)}
	contentTypeStr := []string{"multipart/form-data; boundary=---------------------------9051914041544843365972754266"}

	// verify 'Skynet-Disable-Force'
	hdrs := http.Header{SkynetDisableForceHeader: trueStr}
	req := buildRequest(url.Values{}, hdrs)
	headers, _, err := parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
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
	headers, _, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if headers.mediaType != "text/html" {
		t.Fatal("Unexpected")
	}

	// verify 'basechunkredundancy'
	req = buildRequest(url.Values{"basechunkredundancy": []string{fmt.Sprintf("%v", 2)}}, http.Header{})
	_, params, err := parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if params.baseChunkRedundancy != uint8(2) {
		t.Fatal("Unexpected")
	}

	// verify 'convertpath'
	req = buildRequest(url.Values{"convertpath": []string{"/foo/bar"}}, http.Header{})
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
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
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
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
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
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

	// verify tryfiles
	req = buildRequest(url.Values{"tryfiles": nil}, http.Header{})
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if !reflect.DeepEqual(params.tryFiles, skymodules.DefaultTryFilesValue) {
		t.Fatalf("Expected '%s', got '%s'\n", skymodules.DefaultTryFilesValue, params.tryFiles)
	}
	req = buildRequest(url.Values{"tryfiles": []string{}}, http.Header{})
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if !reflect.DeepEqual(params.tryFiles, skymodules.DefaultTryFilesValue) {
		t.Fatalf("Expected '%s', got '%s'\n", skymodules.DefaultTryFilesValue, params.tryFiles)
	}
	req = buildRequest(url.Values{"tryfiles": []string{""}}, http.Header{})
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if !reflect.DeepEqual(params.tryFiles, []string{}) {
		t.Fatalf("Expected '%s', got '%s'\n", []string{}, params.tryFiles)
	}
	req = buildRequest(url.Values{"tryfiles": []string{"[\"index.html\"]"}}, http.Header{})
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if len(params.tryFiles) != 1 || params.tryFiles[0] != "index.html" {
		t.Fatalf("Expected '%s', got '%s'\n", skymodules.DefaultTryFilesValue, params.tryFiles)
	}

	// verify errorpages
	req = buildRequest(url.Values{"errorpages": []string{}}, http.Header{})
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if len(params.errorPages) > 0 {
		t.Fatal("Expected length of error pages to be zero, got", len(params.errorPages))
	}
	req = buildRequest(url.Values{"errorpages": []string{"{\"404\":\"notfound.html\"}"}}, http.Header{})
	_, params, err = parseRequest(req, defaultParams)
	if params.errorPages[404] != "notfound.html" {
		t.Fatalf("Unexpected 404 errorpage - expected 'notfound.html', got %s\n", params.errorPages[404])
	}

	// verify that 'tryfiles' cannot be combined with 'defaultpath' or
	// 'disabledefaultpath'
	req = buildRequest(url.Values{"tryfiles": []string{"[\"index.html\"]"}, "defaultpath": skymodules.DefaultTryFilesValue}, http.Header{"Content-type": []string{"text/html"}})
	_, params, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil || !strings.Contains(err.Error(), "defaultpath and disabledefaultpath are not compatible with tryfiles") {
		t.Fatal("Unexpected", err)
	}
	req = buildRequest(url.Values{"tryfiles": []string{"[\"index.html\"]"}, "disabledefaultpath": trueStr}, http.Header{"Content-type": []string{"text/html"}})
	_, params, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil || !strings.Contains(err.Error(), "defaultpath and disabledefaultpath are not compatible with tryfiles") {
		t.Fatal("Unexpected", err)
	}

	// verify 'dryrun'
	req = buildRequest(url.Values{"dryrun": trueStr}, http.Header{"Content-type": []string{"text/html"}})
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if !params.dryRun {
		t.Fatal("Unexpected")
	}

	// verify 'filename'
	req = buildRequest(url.Values{"filename": []string{"foo.txt"}}, http.Header{"Content-type": []string{"text/html"}})
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if params.filename != "foo.txt" {
		t.Fatal("Unexpected")
	}

	// verify 'force'
	req = buildRequest(url.Values{"force": trueStr}, http.Header{"Content-type": []string{"text/html"}})
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if !params.force {
		t.Fatal("Unexpected")
	}

	// verify 'force' - combo with 'dryrun
	req = buildRequest(url.Values{"force": trueStr, "dryrun": trueStr}, http.Header{"Content-type": []string{"text/html"}})
	_, _, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil {
		t.Fatal("Unexpected")
	}

	// verify 'mode'
	req = buildRequest(url.Values{"mode": []string{fmt.Sprintf("%o", os.FileMode(0644))}}, http.Header{"Content-type": []string{"text/html"}})
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if params.mode != os.FileMode(0644) {
		t.Fatal("Unexpected")
	}

	// verify 'root'
	req = buildRequest(url.Values{"root": trueStr}, http.Header{"Content-type": []string{"text/html"}})
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if !params.root {
		t.Fatal("Unexpected")
	}

	// verify 'siapath' (no root)
	req = buildRequest(url.Values{}, http.Header{"Content-type": []string{"text/html"}})
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	expected, err := skymodules.SkynetFolder.Join(siapath.String())
	if err != nil || params.siaPath != expected {
		t.Fatal("Unexpected", err)
	}

	// verify 'siapath' (at root)
	req = buildRequest(url.Values{"root": trueStr}, http.Header{"Content-type": []string{"text/html"}})
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
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
	req = buildRequest(url.Values{"skykeyname": []string{key.Name}}, http.Header{"Content-type": []string{"text/html"}})
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if params.skyKeyName != key.Name {
		t.Fatal("Unexpected")
	}

	// verify 'skykeyid'
	req = buildRequest(url.Values{"skykeyid": []string{keyIdStr}}, http.Header{"Content-type": []string{"text/html"}})
	_, params, err = parseRequest(req, defaultParams)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if params.skyKeyID.ToString() != keyIdStr {
		t.Fatal("Unexpected")
	}

	// verify 'skykeyid' - combo with 'skykeyname'
	req = buildRequest(url.Values{"skykeyname": []string{key.Name}, "skykeyid": []string{key.ID().ToString()}}, http.Header{"Content-type": []string{"text/html"}})
	_, _, err = parseUploadHeadersAndRequestParameters(req, defaultParams)
	if err == nil {
		t.Fatal("Unexpected")
	}
}

// testParseDownloadRequestParameters verifies the functionality of
// 'parseDownloadRequestParameters'.
func testParseDownloadRequestParameters(t *testing.T) {
	t.Parallel()

	// Load Skylink
	skylinkStr := "AABEKWZ_wc2R9qlhYkzbG8mImFVi08kBu1nsvvwPLBtpEg"
	var skylink skymodules.Skylink
	err := skylink.LoadString(skylinkStr)
	if err != nil {
		t.Fatal(err)
	}

	// buildRequest is a helper function that creates a request object
	buildRequest := func(values url.Values, headers http.Header) (*http.Request, error) {
		req, err := http.NewRequest("GET", fmt.Sprintf("/skynet/skylink/%s?%s", skylink.String(), values.Encode()), nil)
		if err != nil {
			return nil, errors.AddContext(err, "Could not create request")
		}

		for k, v := range headers {
			for _, vv := range v {
				req.Header.Add(k, vv)
			}
		}
		return req, nil
	}
	// baseParams returns the minimum params for the base case
	baseParams := func() *skyfileDownloadParams {
		return &skyfileDownloadParams{
			path:                 "/",
			pricePerMS:           skymodules.DefaultSkynetPricePerMS,
			skylink:              skylink,
			skylinkStringNoQuery: skylinkStr,
			timeout:              DefaultSkynetRequestTimeout,
		}
	}

	// Test base case of just skylink
	req, err := buildRequest(url.Values{}, http.Header{"Content-type": []string{"text/html"}})
	if err != nil {
		t.Fatal(err)
	}
	sdp, err := parseDownloadRequestParameters(req)
	if err != nil {
		t.Fatal(err)
	}
	expected := baseParams()
	if !reflect.DeepEqual(sdp, expected) {
		t.Log("skyfileDownloadParams", sdp)
		t.Log("expected", expected)
		t.Fatal("unexpected")
	}

	// Test attachment
	trueStr := []string{fmt.Sprintf("%t", true)}
	req, err = buildRequest(url.Values{"attachment": trueStr}, http.Header{"Content-type": []string{"text/html"}})
	if err != nil {
		t.Fatal(err)
	}
	sdp, err = parseDownloadRequestParameters(req)
	if err != nil {
		t.Fatal(err)
	}
	expected = baseParams()
	expected.attachment = true
	if !reflect.DeepEqual(sdp, expected) {
		t.Log("skyfileDownloadParams", sdp)
		t.Log("expected", expected)
		t.Fatal("unexpected")
	}

	// Test Format
	formatTest := func(format skymodules.SkyfileFormat) error {
		req, err := buildRequest(url.Values{"format": []string{string(format)}}, http.Header{"Content-type": []string{"text/html"}})
		if err != nil {
			return err
		}
		sdp, err = parseDownloadRequestParameters(req)
		if err != nil {
			return err
		}
		expected = baseParams()
		expected.format = format
		if !reflect.DeepEqual(sdp, expected) {
			t.Log("skyfileDownloadParams", sdp)
			t.Log("expected", expected)
			return errors.New("download params unexpected")
		}
		return nil
	}
	formats := []skymodules.SkyfileFormat{skymodules.SkyfileFormatNotSpecified, skymodules.SkyfileFormatConcat, skymodules.SkyfileFormatTar, skymodules.SkyfileFormatTarGz, skymodules.SkyfileFormatZip}
	for _, format := range formats {
		err = formatTest(format)
		if err != nil {
			t.Fatalf("error with format %v:%v", string(format), err)
		}
	}

	// Test include layout
	req, err = buildRequest(url.Values{"include-layout": trueStr}, http.Header{"Content-type": []string{"text/html"}})
	if err != nil {
		t.Fatal(err)
	}
	sdp, err = parseDownloadRequestParameters(req)
	if err != nil {
		t.Fatal(err)
	}
	expected = baseParams()
	expected.includeLayout = true
	if !reflect.DeepEqual(sdp, expected) {
		t.Log("skyfileDownloadParams", sdp)
		t.Log("expected", expected)
		t.Fatal("unexpected")
	}

	// Test timeout
	var timeoutInt int = 100
	timeout := time.Duration(timeoutInt) * time.Second
	timeoutStr := []string{fmt.Sprintf("%d", timeoutInt)}
	req, err = buildRequest(url.Values{"timeout": timeoutStr}, http.Header{"Content-type": []string{"text/html"}})
	if err != nil {
		t.Fatal(err)
	}
	sdp, err = parseDownloadRequestParameters(req)
	if err != nil {
		t.Fatal(err)
	}
	expected = baseParams()
	expected.timeout = timeout
	if !reflect.DeepEqual(sdp, expected) {
		t.Log("skyfileDownloadParams", sdp)
		t.Log("expected", expected)
		t.Fatal("unexpected")
	}

	// Test pricePerMS
	pricePerMS := skymodules.DefaultSkynetPricePerMS
	pricePerMSStr := "1000"
	_, err = fmt.Sscan(pricePerMSStr, &pricePerMS)
	if err != nil {
		t.Fatal(err)
	}
	req, err = buildRequest(url.Values{"priceperms": []string{pricePerMSStr}}, http.Header{"Content-type": []string{"text/html"}})
	if err != nil {
		t.Fatal(err)
	}
	sdp, err = parseDownloadRequestParameters(req)
	if err != nil {
		t.Fatal(err)
	}
	expected = baseParams()
	expected.pricePerMS = pricePerMS
	if !reflect.DeepEqual(sdp, expected) {
		t.Log("skyfileDownloadParams", sdp)
		t.Log("expected", expected)
		t.Fatal("unexpected")
	}

	// Test range params
	var rangeTests = []struct {
		start     string
		end       string
		setHeader bool
		err       error
	}{
		// Happy Cases
		{"0", "0", false, nil}, // start = end, no header set
		{"1", "1", false, nil}, // start = end, non zero, no header set
		{"1", "5", false, nil}, // start < end,  no header set

		// Error Cases
		{"0", "0", true, errRangeSetTwice},          // start = end, header set
		{"1", "1", true, errRangeSetTwice},          // start = end, non zero, header set
		{"1", "5", true, errRangeSetTwice},          // start < end,  header set
		{"1", "0", false, errInvalidRangeParams},    // start > end, no header set
		{"1", "0", true, errRangeSetTwice},          // start > end, header set
		{"", "0", false, errIncompleteRangeRequest}, // start not set, header not set
		{"", "0", true, errIncompleteRangeRequest},  // start not set, header set
		{"0", "", false, errIncompleteRangeRequest}, // end not set, header not set
		{"0", "", true, errIncompleteRangeRequest},  // end not set, header set
	}
	for _, rt := range rangeTests {
		// Set url values
		values := url.Values{
			"start": []string{rt.start},
			"end":   []string{rt.end},
		}

		// Check if Header should be set
		var headers http.Header
		if rt.setHeader {
			rangeStr := fmt.Sprintf("bytes=%s-%s", rt.start, rt.end)
			headers = http.Header{"Range": []string{rangeStr}}
		}

		req, err = buildRequest(values, headers)
		if err != nil {
			t.Fatal(err)
		}
		sdp, err = parseDownloadRequestParameters(req)
		if err != rt.err {
			t.Log("Test Case: ", rt)
			t.Fatalf("Expected error '%v' but got '%v'", rt.err, err)
		}
	}
}

// testParseStatsType is a unit test that validates the stats type parser.
func testParseStatsType(t *testing.T) {
	t.Parallel()

	statsType, err := parseStatsType("overdrive")
	if err != nil {
		t.Fatal("bad")
	}
	if statsType != skymodules.OverdriveStats {
		t.Fatal("bad")
	}

	_, err = parseStatsType("InvalidStatsTypeBecauseItsTooLong")
	if err == nil {
		t.Fatal("bad")
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
	proof := header.Get(SkynetProofHeader)

	// Should match the expected proof.
	if proof != string(expectedProof) {
		t.Log(proof)
		t.Log(string(expectedProof))
		t.Fatal("proof doesn't match expectation")
	}

	// Attach an empty proof. Shouldn't set the header.
	w = newTestHTTPWriter()
	header = w.Header()
	attachRegistryEntryProof(w, []skymodules.RegistryEntry{})
	_, set := header["Skynet-Proof"]
	if set {
		t.Fatal("set")
	}
}

// TestUnmarshalErrorPages ensures that we properly handle all string inputs.
func TestUnmarshalErrorPages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		out  map[int]string
		err  string
	}{
		{
			name: "empty",
			in:   "",
			out:  map[int]string{},
			err:  "",
		},
		{
			name: "404",
			in:   "{\"404\":\"notfound.html\"}",
			out:  map[int]string{404: "notfound.html"},
			err:  "",
		},
		{
			name: "404,403",
			in:   "{\"404\":\"notfound.html\",\"403\":\"bla.html\"}",
			out: map[int]string{
				404: "notfound.html",
				403: "bla.html",
			},
			err: "",
		},
		{
			name: "not a json",
			in:   "this is not a JSON",
			out:  map[int]string{},
			err:  "invalid errorpages value",
		},
	}

	for _, tt := range tests {
		out, err := UnmarshalErrorPages(tt.in)
		if err != nil && tt.err == "" {
			t.Log("Failing test:", tt.name)
			t.Fatal("Unexpected error", err)
		}
		if tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)) {
			t.Log("Failing test:", tt.name)
			t.Fatalf("Expected error '%s', got '%s'\n", tt.err, err.Error())
		}
		// only compare outputs if we expect to not encounter an error
		if tt.err == "" && !reflect.DeepEqual(out, tt.out) {
			t.Log("Failing test:", tt.name)
			t.Logf("Expected: %+v\n", tt.out)
			t.Logf("Actual  : %+v\n", out)
			t.Fatal("Unexpected output.")
		}
	}
}

// TestUnmarshalTryFiles ensures that we properly handle all string inputs.
func TestUnmarshalTryFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		out  []string
		err  string
	}{
		{
			name: "empty",
			in:   "",
			out:  []string{},
			err:  "",
		},
		{
			name: "empty arr",
			in:   "[]",
			out:  []string{},
			err:  "",
		},
		{
			name: "index",
			in:   "[\"index.html\"]",
			out:  []string{"index.html"},
			err:  "",
		},
		{
			name: "index, bla",
			in:   "[\"index.html\",\"bla.info\"]",
			out:  []string{"index.html", "bla.info"},
			err:  "",
		},
		{
			name: "not a json",
			in:   "this is not a JSON",
			out:  nil,
			err:  "invalid tryfiles value",
		},
	}

	for _, tt := range tests {
		out, err := UnmarshalTryFiles(tt.in)
		if err != nil && tt.err == "" {
			t.Log("Failing test:", tt.name)
			t.Fatal("Unexpected error", err)
		}
		if tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)) {
			t.Log("Failing test:", tt.name)
			t.Fatalf("Expected error '%s', got '%s'\n", tt.err, err.Error())
		}
		// only compare outputs if we expect to not encounter an error
		if tt.err == "" && !reflect.DeepEqual(out, tt.out) {
			t.Log("Failing test:", tt.name)
			t.Logf("Expected: %+v\n", tt.out)
			t.Logf("Actual  : %+v\n", out)
			t.Fatal("Unexpected output.")
		}
	}
}

// TestCustomErrorWriter ensures that customErrorWriter responds with the right
// content.
func TestCustomErrorWriter(t *testing.T) {
	t.Parallel()

	subfiles := skymodules.SkyfileSubfiles{
		"400.html": skymodules.SkyfileSubfileMetadata{
			Filename:    "400.html",
			ContentType: "text/html",
			Offset:      0,
			Len:         14,
		},
		"404.html": skymodules.SkyfileSubfileMetadata{
			Filename:    "404.html",
			ContentType: "text/html",
			Offset:      14,
			Len:         14,
		},
		"418.html": skymodules.SkyfileSubfileMetadata{
			Filename:    "418.html",
			ContentType: "text/html",
			Offset:      28,
			Len:         14,
		},
		"500.html": skymodules.SkyfileSubfileMetadata{
			Filename:    "500.html",
			ContentType: "text/html",
			Offset:      42,
			Len:         14,
		},
		"502.html": skymodules.SkyfileSubfileMetadata{
			Filename:    "502.html",
			ContentType: "text/html",
			Offset:      56,
			Len:         14,
		},
	}
	eps := map[int]string{
		400: "/400.html",
		404: "/404.html",
		418: "/418.html",
		500: "/500.html",
		502: "/502.html",
	}
	meta := skymodules.SkyfileMetadata{
		Filename:   t.Name(),
		Length:     60,
		Subfiles:   subfiles,
		ErrorPages: eps,
	}
	data := []byte("FileContent400FileContent404FileContent418FileContent500FileContent502")
	rawMD, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	streamer := renter.SkylinkStreamerFromSlice(data, meta, rawMD, skymodules.Skylink{}, skymodules.SkyfileLayout{})

	ew := newCustomErrorWriter(meta, streamer)
	w := newTestHTTPWriter()

	// test all errorpage codes
	for code := range eps {
		codeStr := strconv.Itoa(code)
		ew.WriteError(w, Error{"This is an error with status " + codeStr}, code)
		sf, exists := subfiles[codeStr+".html"]
		if !exists {
			t.Fatalf("Expected to find a subfile with name %s", codeStr+".html")
		}
		expectedData := data[sf.Offset : sf.Offset+sf.Len]
		if !reflect.DeepEqual(expectedData, w.WrittenContent()) {
			t.Fatalf("Expected content '%s', got '%s'", string(expectedData), string(w.WrittenContent()))
		}
	}

	// test a non-errorpages code
	errmsg := "we want to see this"
	ew.WriteError(w, Error{errmsg}, 401)
	if !strings.Contains(string(w.WrittenContent()), errmsg) {
		t.Fatalf("Expected content to contain '%s', got '%s'", errmsg, string(w.WrittenContent()))
	}
}

// TestParseTimeout is a unit test for parseTimeout and parseRegistryTimeout.
func TestParseTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		timeout     string
		timeoutFunc func(url.Values) (time.Duration, error)

		result time.Duration
		err    error
	}{
		{
			name:        "SkynetTimeout/Default",
			timeout:     "",
			timeoutFunc: parseTimeout,

			result: DefaultSkynetRequestTimeout,
			err:    nil,
		},
		{
			name:        "SkynetTimeout/Zero",
			timeout:     "0",
			timeoutFunc: parseTimeout,

			result: 0,
			err:    errZeroTimeout,
		},
		{
			name:        "SkynetTimeout/Max",
			timeout:     fmt.Sprint(MaxSkynetRequestTimeout.Seconds()),
			timeoutFunc: parseTimeout,

			result: MaxSkynetRequestTimeout,
			err:    nil,
		},
		{
			name:        "SkynetTimeout/AboveMax",
			timeout:     fmt.Sprint(MaxSkynetRequestTimeout.Seconds() + 1),
			timeoutFunc: parseTimeout,

			result: 0,
			err:    errTimeoutTooHigh,
		},
		{
			name:        "RegistryTimeout/Default",
			timeout:     "",
			timeoutFunc: parseRegistryTimeout,

			result: renter.DefaultRegistryHealthTimeout,
			err:    nil,
		},
		{
			name:        "RegistryTimeout/Zero",
			timeout:     "0",
			timeoutFunc: parseRegistryTimeout,

			result: 0,
			err:    errZeroTimeout,
		},
		{
			name:        "RegistryTimeout/Max",
			timeout:     fmt.Sprint(renter.MaxRegistryReadTimeout.Seconds()),
			timeoutFunc: parseRegistryTimeout,

			result: renter.MaxRegistryReadTimeout,
			err:    nil,
		},
		{
			name:        "RegistryTimeout/AboveMax",
			timeout:     fmt.Sprint(renter.MaxRegistryReadTimeout.Seconds() + 1),
			timeoutFunc: parseRegistryTimeout,

			result: 0,
			err:    errTimeoutTooHigh,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := url.Values{}
			values.Set("timeout", test.timeout)

			d, err := test.timeoutFunc(values)
			if test.err != nil && !errors.Contains(err, test.err) {
				t.Fatal(err)
			}
			if test.err == nil && err != nil {
				t.Fatal(err)
			}
			if d != test.result {
				t.Fatalf("%v != %v", d, test.result)
			}
		})
	}
}
