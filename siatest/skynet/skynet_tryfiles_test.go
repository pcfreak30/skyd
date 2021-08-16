package skynet

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"gitlab.com/SkynetLabs/skyd/siatest"
	"gitlab.com/SkynetLabs/skyd/skykey"
)

// testSkynetTryFiles ensures that the tryfiles metadata information is treated
// correctly
func testSkynetTryFiles(t *testing.T, tg *siatest.TestGroup) {
	subTests := []siatest.SubTest{
		{Name: "WithRootIndex", Test: testTryFilesWithRootIndex},
		{Name: "WithoutRootIndex", Test: testTryFilesWithoutRootIndex},
		{Name: "ErrorPages", Test: testSkynetErrorPages},
	}
	// Run subtests
	for _, test := range subTests {
		t.Run(test.Name, func(t *testing.T) {
			test.Test(t, tg)
		})
	}
}

// testTryFilesWithRootIndex ensures we act correctly on skyfiles with an index
// file in their root directory.
func testTryFilesWithRootIndex(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	idx := "FileContentsIndex"
	dirWithIdxIdx := "FileContentsDirWithIdxIdx"
	dirWithIdxAbout := "FileContentsDirWithIdxAbout"
	dirWithoutIdxAbout := "FileContentsDirWithoutIdxAbout"
	filename := "with_root_index"
	tf := []string{"index.html", "/index.html"}
	ep := map[int]string{}
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(idx)},
		{Name: "dir_with_idx/index.html", Data: []byte(dirWithIdxIdx)},
		{Name: "dir_with_idx/about.html", Data: []byte(dirWithIdxAbout)},
		{Name: "dir_without_idx/about.html", Data: []byte(dirWithoutIdxAbout)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, tf, ep, true, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	// get an existing file
	data, err := r.SkynetSkylinkGet(skylink + "/dir_with_idx/about.html")
	if err != nil {
		t.Fatal("Failed to download existing file.", err)
	}
	if bytes.Compare(data, []byte(dirWithIdxAbout)) != 0 {
		t.Fatal("Data is different from the expected.")
	}
	// get a non-existent file from a dir with an index
	// this will check for a file called noexist.html and when it doesn't find
	// it, it will assume it's a dir and check for /dir_with_idx/noexist.html/index.html
	// when it doesn't find that either it will serve /index.html
	data, err = r.SkynetSkylinkGet(skylink + "/dir_with_idx/noexist.html")
	if err != nil {
		t.Fatal("Failed to download local index file according to tryfiles rules.", err)
	}
	if bytes.Compare(data, []byte(idx)) != 0 {
		t.Log("Expected data:", idx)
		t.Log("Actual data:  ", string(data))
		t.Fatal("Data is different from the expected.")
	}
	// request a dir with an index
	data, err = r.SkynetSkylinkGet(skylink + "/dir_with_idx/")
	if err != nil {
		t.Fatal("Failed to download local index file according to tryfiles rules.", err)
	}
	if bytes.Compare(data, []byte(dirWithIdxIdx)) != 0 {
		t.Log("Expected data:", dirWithIdxIdx)
		t.Log("Actual data:  ", string(data))
		t.Fatal("Data is different from the expected.")
	}
	// request a dir with an index without a trailing slash
	data, err = r.SkynetSkylinkGet(skylink + "/dir_with_idx")
	if err != nil {
		t.Fatal("Failed to download local index file according to tryfiles rules.", err)
	}
	if bytes.Compare(data, []byte(dirWithIdxIdx)) != 0 {
		t.Log("Expected data:", dirWithIdxIdx)
		t.Log("Actual data:  ", string(data))
		t.Fatal("Data is different from the expected.")
	}
	// get a non-existent file from a dir without an index
	data, err = r.SkynetSkylinkGet(skylink + "/dir_without_idx/noexist.html")
	if err != nil {
		t.Fatal("Failed to download root index file according to tryfiles rules.", err)
	}
	if bytes.Compare(data, []byte(idx)) != 0 {
		t.Fatal("Data is different from the expected.")
	}
	// request a dir without an index
	data, err = r.SkynetSkylinkGet(skylink + "/dir_without_idx/")
	if err != nil {
		t.Fatal("Failed to download root index file according to tryfiles rules.", err)
	}
	if bytes.Compare(data, []byte(idx)) != 0 {
		t.Fatal("Data is different from the expected.")
	}
	// get a non-existent file from root dir
	data, err = r.SkynetSkylinkGet(skylink + "/noexist.html")
	if err != nil {
		t.Fatal("Failed to download root index file according to tryfiles rules.", err)
	}
	if bytes.Compare(data, []byte(idx)) != 0 {
		t.Fatal("Data is different from the expected.")
	}
}

// testTryFilesWithoutRootIndex ensures we act correctly on skyfiles without an
// index file in their root directory.
func testTryFilesWithoutRootIndex(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc2 := "File2Contents"
	fc3 := "File3Contents"
	fc4 := "File4Contents"
	filename := "without_root_index"
	// we'll call the default file index.js in order to make sure non-HTML files
	// are accepted
	tf := []string{"index.js"}
	ep := map[int]string{}
	files := []siatest.TestFile{
		{Name: "dir_with_idx/index.js", Data: []byte(fc2)},
		{Name: "dir_with_idx/about.html", Data: []byte(fc3)},
		{Name: "dir_without_idx/about.html", Data: []byte(fc4)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, tf, ep, true, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	// get an existing file
	data, err := r.SkynetSkylinkGet(skylink + "/dir_with_idx/about.html")
	if err != nil {
		t.Fatal("Failed to download existing file.", err)
	}
	if bytes.Compare(data, []byte(fc3)) != 0 {
		t.Fatal("Data is different from the expected.")
	}
	// get a dir with an index
	data, err = r.SkynetSkylinkGet(skylink + "/dir_with_idx")
	if err != nil {
		t.Fatal("Failed to download local index file according to tryfiles rules.", err)
	}
	if bytes.Compare(data, []byte(fc2)) != 0 {
		t.Fatal("Data is different from the expected.")
	}
	// get a non-existent file from a dir with an index
	data, err = r.SkynetSkylinkGet(skylink + "/dir_with_idx/noexist.html")
	if err == nil || !strings.Contains(err.Error(), "failed to download contents for path") {
		t.Fatal("Expected the download to fail with 'failed to download contents for path', got", err)
	}
	status, _, err := r.SkynetSkylinkHead(skylink + "/dir_without_idx/noexist.html")
	if status != http.StatusNotFound {
		t.Fatalf("Expected status 404, got %d", status)
	}
	// get a non-existent file from a dir without an index
	data, err = r.SkynetSkylinkGet(skylink + "/dir_without_idx/noexist.html")
	if err == nil || !strings.Contains(err.Error(), "failed to download contents for path") {
		t.Fatal("Expected the download to fail with 'failed to download contents for path', got", err)
	}
	status, _, err = r.SkynetSkylinkHead(skylink + "/dir_without_idx/noexist.html")
	if status != http.StatusNotFound {
		t.Fatalf("Expected status 404, got %d", status)
	}
	// get a non-existent file from root dir without an index
	data, err = r.SkynetSkylinkGet(skylink + "/noexist.html")
	if err == nil || !strings.Contains(err.Error(), "failed to download contents for path") {
		t.Fatal("Expected the download to fail with 'failed to download contents for path', got", err)
	}
	status, _, err = r.SkynetSkylinkHead(skylink + "/noexist.html")
	if status != http.StatusNotFound {
		t.Fatalf("Expected status 404, got %d", status)
	}
}

// testSkynetErrorPages ensures that the errorpages metadata information is
// treated correctly
func testSkynetErrorPages(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc404 := "File404Contents"
	filename := "err_pages"
	tf := []string{}
	ep := map[int]string{
		404: "/404.html",
	}
	files := []siatest.TestFile{
		// there is no leading slash on purpose - we shouldn't need it
		{Name: "404.html", Data: []byte(fc404)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, tf, ep, true, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	// get a non-existent file
	// we expect to receive the custom 404 content and a 404 status code
	status, _, err := r.SkynetSkylinkHead(skylink + "/noexist.html")
	if status != http.StatusNotFound {
		t.Fatalf("Expected status 404, got %d", status)
	}
	data, err := r.SkynetSkylinkGet(skylink + "/noexist.html")
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if bytes.Compare(data, []byte(fc404)) != 0 {
		t.Log("Expected data:", fc404)
		t.Log("Actual data:  ", string(data))
		t.Fatal("Data is different from the expected.")
	}
}

// testTryFiles_TableTests ensures all standard scenarios are properly supported.
func testTryFiles_TableTests(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	fMainIndex := []byte("main index")
	fCustomNotFound := []byte("custom not found")
	fAboutIndex := []byte("about index")
	fGoodNewsIndex := []byte("good news index")
	fImage := []byte("this is an image")

	subfiles := []siatest.TestFile{
		{Name: "index.html", Data: fMainIndex},
		{Name: "404.html", Data: fCustomNotFound},
		{Name: "about/index.html", Data: fAboutIndex},
		{Name: "news/good-news/index.html", Data: fGoodNewsIndex},
		{Name: "img/image.png", Data: fImage},
	}

	ep := map[int]string{404: "/404.html"}
	tfWithGlobalIndex := []string{"good-news/index.html", "index.html", "/index.html"}
	withGlobalIndex, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking("global_index", subfiles, "", false, tfWithGlobalIndex, ep, true, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal(err)
	}
	tfNoGlobalIndex := []string{"index.html", "good-news/index.html"}
	noGlobalIndex, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking("no_global_index", subfiles, "", false, tfNoGlobalIndex, ep, true, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal(err)
	}
	tfNoIndex := []string{"good-news/index.html"}
	noIndex, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking("no_global_index", subfiles, "", false, tfNoIndex, ep, true, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name                    string
		skylink                 string
		requestPath             string
		expectedContent         []byte
		expectedStatusCode      int
		expectedErrStrDownload  string
		expectZippedDirAsOutput bool
	}{
		// Global index
		{
			name:               "global index, request path ''",
			skylink:            withGlobalIndex,
			requestPath:        "",
			expectedContent:    fMainIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "global index, request path '/about'",
			skylink:            withGlobalIndex,
			requestPath:        "/about",
			expectedContent:    fAboutIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "global index, request path '/news/noexist.html'",
			skylink:            withGlobalIndex,
			requestPath:        "/news/noexist.html",
			expectedContent:    fMainIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "global index, request path '/news/bad-news'",
			skylink:            withGlobalIndex,
			requestPath:        "/news/bad-news",
			expectedContent:    fMainIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "global index, request path '/news/good-news'",
			skylink:            withGlobalIndex,
			requestPath:        "/news/good-news",
			expectedContent:    fGoodNewsIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "global index, request path '/img/noexist.png'",
			skylink:            withGlobalIndex,
			requestPath:        "/img/noexist.png",
			expectedContent:    fMainIndex,
			expectedStatusCode: http.StatusOK,
		},
		// No global index
		{
			name:               "no global index, request path ''",
			skylink:            noGlobalIndex,
			requestPath:        "",
			expectedContent:    fMainIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "no global index, request path '/index.html'",
			skylink:            noGlobalIndex,
			requestPath:        "/index.html",
			expectedContent:    fMainIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "no global index, request path '/about'",
			skylink:            noGlobalIndex,
			requestPath:        "/about",
			expectedContent:    fAboutIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "no global index, request path '/about/index.html'",
			skylink:            noGlobalIndex,
			requestPath:        "/about/index.html",
			expectedContent:    fAboutIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "no global index, request path '/news/noexist.html'",
			skylink:            noGlobalIndex,
			requestPath:        "/news/noexist.html",
			expectedContent:    fCustomNotFound,
			expectedStatusCode: http.StatusNotFound,
		},
		{
			name:               "no global index, request path '/news/bad-news'",
			skylink:            noGlobalIndex,
			requestPath:        "/news/bad-news",
			expectedContent:    fCustomNotFound,
			expectedStatusCode: http.StatusNotFound,
		},
		{
			name:               "no global index, request path '/news/good-news'",
			skylink:            noGlobalIndex,
			requestPath:        "/news/good-news",
			expectedContent:    fGoodNewsIndex,
			expectedStatusCode: http.StatusOK,
		},
		// No index
		{
			name:                    "no index, request path ''",
			skylink:                 noIndex,
			requestPath:             "",
			expectZippedDirAsOutput: true,
			expectedStatusCode:      http.StatusOK,
		},
		{
			name:               "no index, request path '/index.html'",
			skylink:            noIndex,
			requestPath:        "/index.html",
			expectedContent:    fMainIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:                    "no index, request path '/about'",
			skylink:                 noIndex,
			requestPath:             "/about",
			expectedStatusCode:      http.StatusOK,
			expectZippedDirAsOutput: true,
		},
		{
			name:               "no index, request path '/about/index.html'",
			skylink:            noIndex,
			requestPath:        "/about/index.html",
			expectedContent:    fAboutIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "no index, request path '/news/noexist.html'",
			skylink:            noIndex,
			requestPath:        "/news/noexist.html",
			expectedContent:    fCustomNotFound,
			expectedStatusCode: http.StatusNotFound,
		},
		{
			name:               "no index, request path '/news/bad-news'",
			skylink:            noIndex,
			requestPath:        "/news/bad-news",
			expectedContent:    fCustomNotFound,
			expectedStatusCode: http.StatusNotFound,
		},
		{
			name:               "no index, request path '/news'",
			skylink:            noIndex,
			requestPath:        "/news",
			expectedContent:    fGoodNewsIndex,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:                    "no index, request path '/news/good-news'",
			skylink:                 noIndex,
			requestPath:             "/news/good-news",
			expectedStatusCode:      http.StatusOK,
			expectZippedDirAsOutput: true,
		},
	}

	var content []byte
	var status int
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err = r.SkynetSkylinkGet(tt.skylink + tt.requestPath)
			if err == nil && tt.expectedErrStrDownload != "" {
				t.Log(string(content))
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
				t.Logf("Expected content: %s\n", string(tt.expectedContent))
				t.Logf("Actual content:   %s\n", string(content))
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
