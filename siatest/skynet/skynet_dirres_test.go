package skynet

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"gitlab.com/SkynetLabs/skyd/siatest"
	"gitlab.com/SkynetLabs/skyd/skykey"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

// testSkynetDirectoryResolution tests whether directory resolution metadata
// parameters work correctly
func testSkynetDirectoryResolution(t *testing.T, tg *siatest.TestGroup) {
	subTests := []siatest.SubTest{
		{Name: "StandardModeDefaultBehaviour", Test: testStdMode},
		{Name: "StandardModeCustomNotFound", Test: testStdModeCustomNotFound},
		{Name: "WebModeNoCustomNotFound", Test: testWebModeNothingCustom},
		{Name: "WebModeCustomResponse", Test: testWebModeCustomResponse},
		{Name: "WebModeCustomNotFoundDoesNotExist", Test: testWebModeCustomNotFoundDoesNotExist},
	}

	// Run subtests
	for _, test := range subTests {
		t.Run(test.Name, func(t *testing.T) {
			test.Test(t, tg)
		})
	}
}

// testStdModeHappyCase ensures that a standard mode skyfile returns the
// standard response to requests for nonexistent files.
func testStdMode(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	filename := "index.html_nil"
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(fc1)},
		{Name: "about.html", Data: []byte(fc2)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, skymodules.DirResModeStandard, "", http.StatusNotFound, false, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	status, _, err := r.SkynetSkylinkHead(skylink + "/nonexistent_file.html")
	if status != http.StatusNotFound {
		t.Log(err)
		t.Fatalf("Expected status 404, got %d", status)
	}
	if status != http.StatusNotFound || (err != nil && !strings.Contains(err.Error(), "failed to download contents for path")) {
		t.Fatal(err)
	}
}

// testStdModeCustomNotFound ensures that we cannot upload a standard mode
// skyfile with custom not found response.
func testStdModeCustomNotFound(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	filename := "index.html_nil"
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(fc1)},
		{Name: "404.html", Data: []byte(fc2)},
	}
	// Try to upload with standard mode and a custom 'not found' file that exists.
	_, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, skymodules.DirResModeStandard, "404.html", http.StatusNotFound, false, nil, "", skykey.SkykeyID{})
	if err == nil || !strings.Contains(err.Error(), skymodules.ErrInvalidDirectoryResolutionMode.Error()) {
		t.Fatalf("Expected to get ErrInvalidDirectoryResolutionMode error, got %+v", err)
	}
	// Try to upload with standard mode and a custom 'not found' status code.
	_, _, _, err = r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, skymodules.DirResModeStandard, "", http.StatusOK, false, nil, "", skykey.SkykeyID{})
	if err == nil || !strings.Contains(err.Error(), skymodules.ErrInvalidDirectoryResolutionMode.Error()) {
		t.Fatalf("Expected to get ErrInvalidDirectoryResolutionMode error, got %+v", err)
	}
}

// testWebModeNothingCustom ensures that a web mode skyfile without any custom
// not found content responds to requests for nonexistent files in the same way
// a standard mode skyfile does.
func testWebModeNothingCustom(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	filename := "index.html_nil"
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(fc1)},
		{Name: "about.html", Data: []byte(fc2)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, skymodules.DirResModeWeb, "", http.StatusNotFound, false, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	status, _, err := r.SkynetSkylinkHead(skylink + "/nonexistent_file.html")
	// TODO Check whether this will error out.
	t.Log(" >>> testWebModeNothingCustom", err)
	if status != http.StatusNotFound || (err != nil && !strings.Contains(err.Error(), "failed to download contents for path")) {
		t.Fatal(err)
	}
}

// testWebModeCustomResponse ensures that a web mode skyfile with custom not
// found response delivers the configured not found response.
func testWebModeCustomResponse(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	filename := "index.html_nil"
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(fc1)},
		{Name: "404.html", Data: []byte(fc2)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, skymodules.DirResModeWeb, "404.html", http.StatusOK, false, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	status, _, err := r.SkynetSkylinkHead(skylink + "/nonexistent_file.html")
	if err != nil {

	}
	if status != http.StatusOK {
		t.Fatalf("Expected status code 200, got %d", status)
	}
	content, err := r.SkynetSkylinkGet(skylink + "/nonexistent_file.html")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, files[1].Data) {
		t.Fatalf("Expected to get content '%s', instead got '%s'", files[1].Data, string(content))
	}
}

// testWebModeCustomNotFoundDoesNotExist ensures that a web mode skyfile with
// custom not found response delivers the configured not found response.
func testWebModeCustomNotFoundDoesNotExist(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	filename := "index.html_nil"
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(fc1)},
		{Name: "about.html", Data: []byte(fc2)},
	}
	_, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, skymodules.DirResModeWeb, "doesntexist.html", http.StatusOK, false, nil, "", skykey.SkykeyID{})
	if err == nil || !strings.Contains(err.Error(), "no such path") {
		t.Fatalf("Expected to get 'no such path' error, got %+v", err)
	}
}

func testSkynetDirectoryResolution_TableTest(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	fc1 := []byte("main index")
	fc2 := []byte("custom not found")
	fc3 := []byte("about index")
	fc4 := []byte("good news index")

	subfiles := []siatest.TestFile{
		{Name: "src/index.html", Data: fc1},
		{Name: "src/404.html", Data: fc2},
		{Name: "about/index.html", Data: fc3},
		{Name: "news/good-news/index.html", Data: fc4},
	}

	skylinkDefaultNotFound, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking("default_not_found", subfiles, "", false, skymodules.DirResModeWeb, "", http.StatusNotFound, true, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal(err)
	}
	skylinkCustomNotFound, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking("custom_not_found", subfiles, "", false, skymodules.DirResModeWeb, "404.html", http.StatusTeapot, true, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal(err)
	}
	skylinkNoExistNotFound, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking("noexist_not_found", subfiles, "", false, skymodules.DirResModeWeb, "noexist.html", http.StatusNotFound, true, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name                   string
		skylink                string
		requestPath            string
		expectedContent        []byte
		expectedStatusCode     int
		expectedErrStrDownload string
	}{
		// Web mode, no custom "not found" settings. Same as standard mode.
		{
			name:               "standard, index, request path ''",
			skylink:            skylinkDefaultNotFound,
			requestPath:        "",
			expectedContent:    fc1,
			expectedStatusCode: 200,
		},
		{
			name:               "standard, index, request path '/index.html'",
			skylink:            skylinkDefaultNotFound,
			requestPath:        "/index.html",
			expectedContent:    fc1,
			expectedStatusCode: 200,
		},
		{
			name:               "standard, about, request path '/about'",
			skylink:            skylinkDefaultNotFound,
			requestPath:        "/about",
			expectedContent:    fc3,
			expectedStatusCode: 200,
		},
		{
			name:               "standard, about, request path '/about/index.html'",
			skylink:            skylinkDefaultNotFound,
			requestPath:        "/about/index.html",
			expectedContent:    fc3,
			expectedStatusCode: 200,
		},
		{
			name:               "standard, news, request path '/news/good-news'",
			skylink:            skylinkDefaultNotFound,
			requestPath:        "/news/good-news",
			expectedContent:    fc4,
			expectedStatusCode: 200,
		},
		{
			name:               "standard, news, request path '/news/good-news/index.html'",
			skylink:            skylinkDefaultNotFound,
			requestPath:        "/news/good-news/index.html",
			expectedContent:    fc4,
			expectedStatusCode: 200,
		},
		// Web mode, valid custom "not found settings".
		{
			name:                   "custom, bad news, request path '/news/bad-news'",
			skylink:                skylinkDefaultNotFound,
			requestPath:            "/news/bad-news",
			expectedContent:        nil,
			expectedStatusCode:     http.StatusNotFound,
			expectedErrStrDownload: "path not found??",
		},
		{
			name:                   "custom, bad news, request path '/news/bad-news/index.html'",
			skylink:                skylinkDefaultNotFound,
			requestPath:            "/news/bad-news/index.html",
			expectedContent:        nil,
			expectedStatusCode:     http.StatusNotFound,
			expectedErrStrDownload: "path not found??",
		},
		{
			name:               "custom, news, request path '/news'",
			skylink:            skylinkCustomNotFound,
			requestPath:        "/news",
			expectedContent:    fc2,
			expectedStatusCode: http.StatusTeapot,
		},
		{
			name:               "custom, news, request path '/news/index.html'",
			skylink:            skylinkCustomNotFound,
			requestPath:        "/news/index.html",
			expectedContent:    fc2,
			expectedStatusCode: http.StatusTeapot,
		},
		{
			name:               "custom, bad news, request path '/news/bad-news'",
			skylink:            skylinkCustomNotFound,
			requestPath:        "/news/bad-news",
			expectedContent:    fc2,
			expectedStatusCode: http.StatusTeapot,
		},
		{
			name:               "custom, bad news, request path '/news/bad-news/index.html'",
			skylink:            skylinkCustomNotFound,
			requestPath:        "/news/bad-news/index.html",
			expectedContent:    fc2,
			expectedStatusCode: http.StatusTeapot,
		},
		// Web mode, invalid custom "not found" settings.
		{
			name:                   "bad custom, bad news, request path '/news/bad-news'",
			skylink:                skylinkNoExistNotFound,
			requestPath:            "/news/bad-news",
			expectedContent:        nil,
			expectedStatusCode:     http.StatusNotFound,
			expectedErrStrDownload: "path not found??",
		},
		{
			name:                   "bad custom, bad news, request path '/news/bad-news/index.html'",
			skylink:                skylinkNoExistNotFound,
			requestPath:            "/news/bad-news/index.html",
			expectedContent:        nil,
			expectedStatusCode:     http.StatusNotFound,
			expectedErrStrDownload: "path not found??",
		},
		{
			name:               "bad custom, news, request path '/news'",
			skylink:            skylinkNoExistNotFound,
			requestPath:        "/news",
			expectedContent:    fc2,
			expectedStatusCode: http.StatusTeapot,
		},
		{
			name:               "bad custom, news, request path '/news/index.html'",
			skylink:            skylinkNoExistNotFound,
			requestPath:        "/news/index.html",
			expectedContent:    fc2,
			expectedStatusCode: http.StatusTeapot,
		},
		{
			name:               "bad custom, bad news, request path '/news/bad-news'",
			skylink:            skylinkNoExistNotFound,
			requestPath:        "/news/bad-news",
			expectedContent:    fc2,
			expectedStatusCode: http.StatusTeapot,
		},
		{
			name:               "bad custom, bad news, request path '/news/bad-news/index.html'",
			skylink:            skylinkNoExistNotFound,
			requestPath:        "/news/bad-news/index.html",
			expectedContent:    fc2,
			expectedStatusCode: http.StatusTeapot,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := r.SkynetSkylinkGet(tt.skylink)
			if err == nil && tt.expectedErrStrDownload != "" {
				t.Fatalf("Expected error '%s', got <nil>", tt.expectedErrStrDownload)
			}
			if err != nil && (tt.expectedErrStrDownload == "" || !strings.Contains(err.Error(), tt.expectedErrStrDownload)) {
				t.Fatalf("Expected error '%s', got '%s'", tt.expectedErrStrDownload, err.Error())
			}
			if tt.expectedErrStrDownload == "" && !bytes.Equal(content, tt.expectedContent) {
				t.Fatalf("Content mismatch! Expected %d bytes, got %d bytes.", len(tt.expectedContent), len(content))
			}
		})
	}
}
