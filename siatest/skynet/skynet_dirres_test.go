package skynet

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"gitlab.com/NebulousLabs/fastrand"

	"gitlab.com/SkynetLabs/skyd/siatest"
	"gitlab.com/SkynetLabs/skyd/skykey"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

// testSkynetDirectoryResolution tests whether directory resolution metadata
// parameters work correctly
func testSkynetDirectoryResolution(t *testing.T, tg *siatest.TestGroup) {
	subTests := []siatest.SubTest{
		{Name: "StandardModeDefaultBehaviour", Test: testDirResStdMode},
		{Name: "StandardModeCustomNotFound", Test: testDirResStdModeCustomNotFound},
		{Name: "WebModeNoCustomNotFound", Test: testDirResWebModeNothingCustom},
		{Name: "WebModeCustomResponse", Test: testDirResWebModeCustomResponse},
		{Name: "WebModeCustomNotFoundDoesNotExist", Test: testDirResWebModeCustomNotFoundDoesNotExist},
	}

	// Run subtests
	for _, test := range subTests {
		t.Run(test.Name, func(t *testing.T) {
			test.Test(t, tg)
		})
	}
}

// testStdModeHappyCase ensures that a std mode skyfile returns the standard
// response to requests for nonexistent files.
func testDirResStdMode(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	filename := "index.html_nil"
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(fc1)},
		{Name: "about.html", Data: []byte(fc2)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, skymodules.DirResModeStandard, "", http.StatusNotFound, true, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	status, _, err := r.SkynetSkylinkHead(skylink + "/nonexistent_file.html")
	if status != http.StatusNotFound {
		t.Fatalf("Expected status 404, got %d", status)
	}
	if err != nil && !strings.Contains(err.Error(), "failed to download contents for path") {
		t.Fatal(err)
	}
}

// testDirResStdModeCustomNotFound ensures that we cannot upload a std mode
// skyfile with custom not found response.
func testDirResStdModeCustomNotFound(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	filename := "index.html_nil"
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(fc1)},
		{Name: "404.html", Data: []byte(fc2)},
	}
	// Try to upload with standard mode and a custom 'not found' file that exists.
	_, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, skymodules.DirResModeStandard, "404.html", http.StatusNotFound, true, nil, "", skykey.SkykeyID{})
	if err == nil || !strings.Contains(err.Error(), skymodules.ErrInvalidDirectoryResolutionMode.Error()) {
		t.Fatalf("Expected to get ErrInvalidDirectoryResolutionMode error, got %+v", err)
	}
	// Try to upload with standard mode and a custom 'not found' status code.
	_, _, _, err = r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, skymodules.DirResModeStandard, "", http.StatusOK, true, nil, "", skykey.SkykeyID{})
	if err == nil || !strings.Contains(err.Error(), skymodules.ErrInvalidDirectoryResolutionMode.Error()) {
		t.Fatalf("Expected to get ErrInvalidDirectoryResolutionMode error, got %+v", err)
	}
}

// testDirResWebModeNothingCustom ensures that a web mode skyfile without any custom
// not found content responds to requests for nonexistent files in the same way
// a standard mode skyfile does.
func testDirResWebModeNothingCustom(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	filename := "index.html_nil"
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(fc1)},
		{Name: "about.html", Data: []byte(fc2)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, skymodules.DirResModeWeb, "", http.StatusNotFound, true, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	status, _, err := r.SkynetSkylinkHead(skylink + "/nonexistent_file.html")
	if status != http.StatusNotFound || (err != nil && !strings.Contains(err.Error(), "failed to download contents for path")) {
		t.Fatal(err)
	}
}

// testDirResWebModeCustomResponse ensures that a web mode skyfile with custom
// not found response delivers the configured not found response.
func testDirResWebModeCustomResponse(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	filename := "index.html_nil"
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(fc1)},
		{Name: "404.html", Data: []byte(fc2)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, skymodules.DirResModeWeb, "404.html", http.StatusOK, true, nil, "", skykey.SkykeyID{})
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

// testDirResWebModeCustomNotFoundDoesNotExist ensures that a web mode skyfile with
// custom not found response delivers the configured not found response.
func testDirResWebModeCustomNotFoundDoesNotExist(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	filename := "index.html_nil"
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(fc1)},
		{Name: "about.html", Data: []byte(fc2)},
	}
	_, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, skymodules.DirResModeWeb, "doesntexist.html", http.StatusOK, true, nil, "", skykey.SkykeyID{})
	if err == nil || !strings.Contains(err.Error(), "no such path") {
		t.Fatalf("Expected to get 'no such path' error, got %+v", err)
	}
}

// testDirRes_TableTest ensures all standard scenarios are properly supported.
func testDirRes_TableTest(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	fMainIndex := []byte("main index")
	fCustomNotFound := []byte("custom not found")
	fAboutIndex := []byte("about index")
	fGoodNewsIndex := []byte("good news index")

	subfiles := []siatest.TestFile{
		{Name: "index.html", Data: fMainIndex},
		{Name: "404.html", Data: fCustomNotFound},
		{Name: "about/index.html", Data: fAboutIndex},
		{Name: "news/good-news/index.html", Data: fGoodNewsIndex},
	}

	skylinkStandard, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking("standard", subfiles, "", false, skymodules.DirResModeStandard, "", http.StatusNotFound, true, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal(err)
	}
	skylinkDefaultNotFound, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking("default_not_found", subfiles, "", false, skymodules.DirResModeWeb, "", http.StatusNotFound, true, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal(err)
	}
	skylinkCustomNotFound, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking("custom_not_found", subfiles, "", false, skymodules.DirResModeWeb, "404.html", http.StatusIMUsed, true, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal(err)
	}
	// TODO How do I create a file with bad meta? Do we even need to test for that? Do we need to?
	//   The idea is that we might come across a file with bad meta uploaded via a custom portal.
	//   The goal is not to serve a meaningful error but to make sure that the bad meta can't crash us.

	tests := []struct {
		name                    string
		skylink                 string
		requestPath             string
		expectedContent         []byte
		expectedStatusCode      int
		expectedErrStrDownload  string
		expectZippedDirAsOutput bool
	}{
		// Standard mode
		{
			name:               "standard, index, request path ''",
			skylink:            skylinkStandard,
			requestPath:        "",
			expectedContent:    fMainIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "standard, index, request path '/index.html'",
			skylink:            skylinkStandard,
			requestPath:        "/index.html",
			expectedContent:    fMainIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:                    "standard, about, request path '/about'",
			skylink:                 skylinkStandard,
			requestPath:             "/about",
			expectedStatusCode:      http.StatusOK,
			expectZippedDirAsOutput: true,
		},
		{
			name:               "standard, about, request path '/about/index.html'",
			skylink:            skylinkStandard,
			requestPath:        "/about/index.html",
			expectedContent:    fAboutIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:                    "standard, news, request path '/news/good-news'",
			skylink:                 skylinkStandard,
			requestPath:             "/news/good-news",
			expectedStatusCode:      http.StatusOK,
			expectZippedDirAsOutput: true,
		},
		{
			name:               "standard, news, request path '/news/good-news/index.html'",
			skylink:            skylinkStandard,
			requestPath:        "/news/good-news/index.html",
			expectedContent:    fGoodNewsIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:                   "standard, bad news, request path '/news/bad-news'",
			skylink:                skylinkStandard,
			requestPath:            "/news/bad-news",
			expectedContent:        nil,
			expectedStatusCode:     http.StatusNotFound,
			expectedErrStrDownload: "failed to download contents for path",
		},
		{
			name:                   "standard, bad news, request path '/news/bad-news/index.html'",
			skylink:                skylinkStandard,
			requestPath:            "/news/bad-news/index.html",
			expectedContent:        nil,
			expectedStatusCode:     http.StatusNotFound,
			expectedErrStrDownload: "failed to download contents for path",
		},
		// Web mode, no custom "not found" settings. Same as standard mode.
		{
			name:               "web-default, index, request path ''",
			skylink:            skylinkDefaultNotFound,
			requestPath:        "",
			expectedContent:    fMainIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "web-default, index, request path '/index.html'",
			skylink:            skylinkDefaultNotFound,
			requestPath:        "/index.html",
			expectedContent:    fMainIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			// We don't expect this to be zipped because this is the actual
			// use case of web mode.
			name:               "web-default, about, request path '/about'",
			skylink:            skylinkDefaultNotFound,
			requestPath:        "/about",
			expectedContent:    fAboutIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "web-default, about, request path '/about/index.html'",
			skylink:            skylinkDefaultNotFound,
			requestPath:        "/about/index.html",
			expectedContent:    fAboutIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			// We don't expect this to be zipped because this is the actual
			// use case of web mode.
			name:               "web-default, news, request path '/news/good-news'",
			skylink:            skylinkDefaultNotFound,
			requestPath:        "/news/good-news",
			expectedContent:    fGoodNewsIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "web-default, news, request path '/news/good-news/index.html'",
			skylink:            skylinkDefaultNotFound,
			requestPath:        "/news/good-news/index.html",
			expectedContent:    fGoodNewsIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:                   "web-default, bad news, request path '/news/bad-news'",
			skylink:                skylinkDefaultNotFound,
			requestPath:            "/news/bad-news",
			expectedContent:        nil,
			expectedStatusCode:     http.StatusNotFound,
			expectedErrStrDownload: "failed to download contents for path",
		},
		{
			name:                   "web-default, bad news, request path '/news/bad-news/index.html'",
			skylink:                skylinkDefaultNotFound,
			requestPath:            "/news/bad-news/index.html",
			expectedContent:        nil,
			expectedStatusCode:     http.StatusNotFound,
			expectedErrStrDownload: "failed to download contents for path",
		},
		// Web mode, custom "not found settings".
		{
			name:               "web-custom, index, request path ''",
			skylink:            skylinkCustomNotFound,
			requestPath:        "",
			expectedContent:    fMainIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "web-custom, index, request path '/index.html'",
			skylink:            skylinkCustomNotFound,
			requestPath:        "/index.html",
			expectedContent:    fMainIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			// We don't expect this to be zipped because this is the actual
			// use case of web mode.
			name:               "web-custom, about, request path '/about'",
			skylink:            skylinkCustomNotFound,
			requestPath:        "/about",
			expectedContent:    fAboutIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "web-custom, about, request path '/about/index.html'",
			skylink:            skylinkCustomNotFound,
			requestPath:        "/about/index.html",
			expectedContent:    fAboutIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			// We don't expect this to be zipped because this is the actual
			// use case of web mode.
			name:               "web-custom, news, request path '/news/good-news'",
			skylink:            skylinkCustomNotFound,
			requestPath:        "/news/good-news",
			expectedContent:    fGoodNewsIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "web-custom, news, request path '/news/good-news/index.html'",
			skylink:            skylinkCustomNotFound,
			requestPath:        "/news/good-news/index.html",
			expectedContent:    fGoodNewsIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "web-custom, bad news, request path '/news/bad-news'",
			skylink:            skylinkCustomNotFound,
			requestPath:        "/news/bad-news",
			expectedContent:    fCustomNotFound,
			expectedStatusCode: http.StatusIMUsed,
		},
		{
			name:               "web-custom, bad news, request path '/news/bad-news/index.html'",
			skylink:            skylinkCustomNotFound,
			requestPath:        "/news/bad-news/index.html",
			expectedContent:    fCustomNotFound,
			expectedStatusCode: http.StatusIMUsed,
		},
	}

	var content []byte
	var status int
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err = r.SkynetSkylinkGet(tt.skylink + tt.requestPath)
			if err == nil && tt.expectedErrStrDownload != "" {
				t.Fatalf("Test name: %s. Expected error '%s', got <nil>", tt.name, tt.expectedErrStrDownload)
			}
			if err != nil && (tt.expectedErrStrDownload == "" || !strings.Contains(err.Error(), tt.expectedErrStrDownload)) {
				t.Fatalf("Test name: %s. Expected error '%s', got '%s'", tt.name, tt.expectedErrStrDownload, err.Error())
			}
			if tt.expectZippedDirAsOutput {
				ct := http.DetectContentType(content)
				if ct != "application/x-gzip" && ct != "application/zip" {
					t.Fatalf("Expected zipped content, got %s", string(content))

				}
			} else if tt.expectedErrStrDownload == "" && !bytes.Equal(content, tt.expectedContent) {
				t.Fatalf("Test name: %s. Content mismatch! Expected %d bytes, got %d bytes.", tt.name, len(tt.expectedContent), len(content))
			}
			status, _, err = r.SkynetSkylinkHead(tt.skylink + tt.requestPath)
			if err != nil && (tt.expectedErrStrDownload == "" || !strings.Contains(err.Error(), tt.expectedErrStrDownload)) {
				t.Fatalf("Test name: %s. (HEAD) Expected error '%s', got '%s'", tt.name, tt.expectedErrStrDownload, err.Error())
			}
			if status != tt.expectedStatusCode {
				t.Fatalf("Test name: %s. Expected status code %d, got %d", tt.name, tt.expectedStatusCode, status)
			}
		})
	}
}
