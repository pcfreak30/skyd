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
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	fc3 := "File3Contents"
	fc4 := "File4Contents"
	filename := "with_root_index"
	tf := []string{"/index.html", "index.html"}
	ep := map[int]string{}
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(fc1)},
		{Name: "dir_with_idx/index.html", Data: []byte(fc2)},
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
	// get a non-existent file from a dir with an index
	data, err = r.SkynetSkylinkGet(skylink + "/dir_with_idx/noexist.html")
	if err != nil {
		t.Fatal("Failed to download local index file according to tryfiles rules.", err)
	}
	if bytes.Compare(data, []byte(fc2)) != 0 {
		t.Fatal("Data is different from the expected.")
	}
	// get a non-existent file from a dir without an index
	data, err = r.SkynetSkylinkGet(skylink + "/dir_without_idx/noexist.html")
	if err != nil {
		t.Fatal("Failed to download root index file according to tryfiles rules.", err)
	}
	if bytes.Compare(data, []byte(fc1)) != 0 {
		t.Fatal("Data is different from the expected.")
	}
	// get a non-existent file from root dir
	data, err = r.SkynetSkylinkGet(skylink + "/noexist.html")
	if err != nil {
		t.Fatal("Failed to download root index file according to tryfiles rules.", err)
	}
	if bytes.Compare(data, []byte(fc1)) != 0 {
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
	// get a non-existent file from a dir with an index
	data, err = r.SkynetSkylinkGet(skylink + "/dir_with_idx/noexist.html")
	if err != nil {
		t.Fatal("Failed to download local index file according to tryfiles rules.", err)
	}
	if bytes.Compare(data, []byte(fc2)) != 0 {
		t.Fatal("Data is different from the expected.")
	}
	// get a non-existent file from a dir without an index
	data, err = r.SkynetSkylinkGet(skylink + "/dir_without_idx/noexist.html")
	if err == nil || !strings.Contains(err.Error(), "failed to download contents for path") {
		t.Fatal("Expected the download to fail with 'failed to download contents for path', got", err)
	}
	status, _, err := r.SkynetSkylinkHead(skylink + "/dir_without_idx/noexist.html")
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
	fc1 := "File1Contents"
	filename := "err_pages"
	tf := []string{}
	ep := map[int]string{
		404: "/404.html",
	}
	files := []siatest.TestFile{
		// there is no leading slash on purpose - we shouldn't need it
		{Name: "404.html", Data: []byte(fc1)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, "", false, tf, ep, true, nil, "", skykey.SkykeyID{})
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	// get a non-existent file
	// we expect to receive the custom 404 content and a 404 status code
	data, err := r.SkynetSkylinkGet(skylink + "/noexist.html")
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if bytes.Compare(data, []byte(fc1)) != 0 {
		t.Fatal("Data is different from the expected.")
	}
	status, _, err := r.SkynetSkylinkHead(skylink + "/noexist.html")
	if status != http.StatusNotFound {
		t.Fatalf("Expected status 404, got %d", status)
	}
}

// testTryFiles_TableTests ensures all standard scenarios are properly supported.
func testTryFiles_TableTests(t *testing.T, tg *siatest.TestGroup) {
}
