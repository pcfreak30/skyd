package skynet

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/node"
	"gitlab.com/SkynetLabs/skyd/node/api"
	"gitlab.com/SkynetLabs/skyd/node/api/client"
	"gitlab.com/SkynetLabs/skyd/siatest"
	"gitlab.com/SkynetLabs/skyd/siatest/dependencies"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/modules"
)

// TestSkynetDownloads verifies the functionality of Skynet downloads.
func TestSkynetDownloads(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Hosts:   3,
		Miners:  1,
		Portals: 1,
	}
	groupDir := skynetTestDir(t.Name())

	// Specify subtests to run
	subTests := []siatest.SubTest{
		{Name: "SingleFileRegular", Test: testDownloadSingleFileRegular},
		{Name: "SingleFileMultiPart", Test: testDownloadSingleFileMultiPart},
		{Name: "DirectoryBasic", Test: testDownloadDirectoryBasic},
		{Name: "DirectoryNested", Test: testDownloadDirectoryNested},
		{Name: "ContentDisposition", Test: testDownloadContentDisposition},
		{Name: "SkynetSkylinkHeader", Test: testSkynetSkylinkHeader},
		{Name: "ETag", Test: testETag},
	}

	// Run tests
	if err := siatest.RunSubTests(t, groupParams, groupDir, subTests); err != nil {
		t.Fatal(err)
	}
}

// testDownloadSingleFileRegular tests the download of a single skyfile,
// uploaded using a regular stream.
func testDownloadSingleFileRegular(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// upload a single file using a stream
	testName := "SingleFileRegular"
	size := fastrand.Uint64n(100) + 100
	data := fastrand.Bytes(int(size))
	skylink, _, _, err := r.UploadNewSkyfileWithDataBlocking("SingleFileRegular", data, false)
	if err != nil {
		t.Fatal(err)
	}

	// verify downloads
	//
	// note: these switch from un-cached to cached downloads partway through. By
	// passing verification on all pieces of the test, we are confirming that
	// the caching is correct.
	err = verifyDownloadRaw(t, r, skylink, data, testName)
	if err != nil {
		t.Fatal(err)
	}
	err = verifyDownloadDirectory(t, r, skylink, data, testName)
	if err != nil {
		t.Fatal(err)
	}
	err = verifyDownloadAsArchive(t, r, skylink, fileMap{"SingleFileRegular": data}, testName)
	if err != nil {
		t.Fatal(err)
	}
}

// testDownloadSingleFileMultiPart tests the download of a single skyfile,
// uploaded using a multipart upload.
func testDownloadSingleFileMultiPart(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// TEST: non-html default path - expect the file's content dut to the single
	// file exception from the HTML-only default path restriction.
	testName := "SingleFileMultiPart"
	data := []byte("contents_file1.png")
	files := []siatest.TestFile{{Name: "file1.png", Data: data}}
	skylink, _, _, err := r.UploadNewMultipartSkyfileBlocking("SingleFileMultiPartPNG", files, "", false, false)
	if err != nil {
		t.Fatal(err)
	}

	// verify downloads
	err = verifyDownloadRaw(t, r, skylink, data, testName)
	if err != nil {
		t.Fatal(err)
	}
	err = verifyDownloadAsArchive(t, r, skylink, fileMapFromFiles(files), testName)
	if err != nil {
		t.Fatal(err)
	}

	// TEST: html default path - expect success
	data = []byte("contents_file1.html")
	files = []siatest.TestFile{{Name: "file1.html", Data: data}}
	skylink, _, _, err = r.UploadNewMultipartSkyfileBlocking("SingleFileMultiPartHTML", files, "", false, false)
	if err != nil {
		t.Fatal(err)
	}

	// verify downloads
	err = verifyDownloadRaw(t, r, skylink, data, testName)
	if err != nil {
		t.Fatal(err)
	}
	err = verifyDownloadAsArchive(t, r, skylink, fileMapFromFiles(files), testName)
	if err != nil {
		t.Fatal(err)
	}

	// verify non existing default path
	_, _, _, err = r.UploadNewMultipartSkyfileBlocking("multipartUploadSingle", files, "notexists.png", false, false)
	if err == nil || !strings.Contains(err.Error(), skymodules.ErrInvalidDefaultPath.Error()) {
		t.Errorf("Expected '%v' instead error was '%v'", skymodules.ErrInvalidDefaultPath, err)
	}

	// verify trying to set no default path on single file upload
	_, _, _, err = r.UploadNewMultipartSkyfileBlocking("multipartUploadSingle", files, "", false, false)
	if err != nil {
		t.Errorf("Expected success, instead error was '%v'", err)
	}
}

// testDownloadDirectoryBasic tests the download of a directory skyfile
func testDownloadDirectoryBasic(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// upload a multi-file skyfile
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte("index.html_contents")},
		{Name: "about.html", Data: []byte("about.html_contents")},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileBlocking("DirectoryBasic", files, "", false, false)
	if err != nil {
		t.Fatal(err)
	}

	// construct the metadata object we expect to be returned
	expectedMetadata := skymodules.SkyfileMetadata{
		Filename: "DirectoryBasic",
		Length:   uint64(len(files[0].Data) + len(files[1].Data)),
		Subfiles: map[string]skymodules.SkyfileSubfileMetadata{
			"index.html": {
				FileMode:    os.FileMode(0644),
				Filename:    "index.html",
				ContentType: "text/html; charset=utf-8",
				Offset:      0,
				Len:         uint64(len(files[0].Data)),
			},
			"about.html": {
				FileMode:    os.FileMode(0644),
				Filename:    "about.html",
				ContentType: "text/html; charset=utf-8",
				Offset:      uint64(len(files[0].Data)),
				Len:         uint64(len(files[1].Data)),
			}},
		DefaultPath:        "",
		DisableDefaultPath: false,
		DirResMode:         skymodules.DirResModeStandard,
		DirResNotFound:     "",
		DirResNotFoundCode: 404,
	}

	_, md, err := r.SkynetMetadataGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(md, expectedMetadata) {
		t.Fatal("mismatch")
	}

	testName := "BasicDirIndexAboutDefaultIndex"

	// verify downloads
	err = verifyDownloadRaw(t, r, skylink, files[0].Data, testName)
	if err != nil {
		t.Fatal(err)
	}
	err = verifyDownloadDirectory(t, r, skylink, append(files[0].Data, files[1].Data...), testName)
	if err != nil {
		t.Fatal(err)
	}
	err = verifyDownloadAsArchive(t, r, skylink, fileMapFromFiles(files), testName)
	if err != nil {
		t.Fatal(err)
	}

	// upload the same files but with a different default path
	skylink, _, _, err = r.UploadNewMultipartSkyfileBlocking("DirectoryBasic", files, "about.html", false, true)
	if err != nil {
		t.Fatal(err)
	}

	// construct the metadata object we expect to be returned
	expectedMetadata = skymodules.SkyfileMetadata{
		Filename: "DirectoryBasic",
		Length:   uint64(len(files[0].Data) + len(files[1].Data)),
		Subfiles: map[string]skymodules.SkyfileSubfileMetadata{
			"index.html": {
				FileMode:    os.FileMode(0644),
				Filename:    "index.html",
				ContentType: "text/html; charset=utf-8",
				Offset:      0,
				Len:         uint64(len(files[0].Data)),
			},
			"about.html": {
				FileMode:    os.FileMode(0644),
				Filename:    "about.html",
				ContentType: "text/html; charset=utf-8",
				Offset:      uint64(len(files[0].Data)),
				Len:         uint64(len(files[1].Data)),
			}},
		DefaultPath:        "/about.html",
		DisableDefaultPath: false,
		DirResMode:         skymodules.DirResModeStandard,
		DirResNotFound:     "",
		DirResNotFoundCode: 404,
	}

	testName = "BasicDirAboutDefaultEmpty"

	// verify downloads
	err = verifyDownloadRaw(t, r, skylink, files[1].Data, testName)
	if err != nil {
		t.Fatal(err)
	}
	err = verifyDownloadAsArchive(t, r, skylink, fileMapFromFiles(files), testName)
	if err != nil {
		t.Fatal(err)
	}

	// upload the same files but with no default path
	skylink, _, _, err = r.UploadNewMultipartSkyfileBlocking("DirectoryBasic", files, "", true, true)
	if err != nil {
		t.Fatal(err)
	}

	testName = "BasicDirIndexAboutDefaultDisabled"

	// construct the metadata object we expect to be returned
	expectedMetadata = skymodules.SkyfileMetadata{
		Filename: "DirectoryBasic",
		Length:   uint64(len(files[0].Data) + len(files[1].Data)),
		Subfiles: map[string]skymodules.SkyfileSubfileMetadata{
			"index.html": {
				FileMode:    os.FileMode(0644),
				Filename:    "index.html",
				ContentType: "text/html; charset=utf-8",
				Offset:      0,
				Len:         uint64(len(files[0].Data)),
			},
			"about.html": {
				FileMode:    os.FileMode(0644),
				Filename:    "about.html",
				ContentType: "text/html; charset=utf-8",
				Offset:      uint64(len(files[0].Data)),
				Len:         uint64(len(files[1].Data)),
			},
		},
		DefaultPath:        "",
		DisableDefaultPath: true,
		DirResMode:         skymodules.DirResModeStandard,
		DirResNotFound:     "",
		DirResNotFoundCode: 404,
	}

	// verify downloads
	err = verifyDownloadDirectory(t, r, skylink, append(files[0].Data, files[1].Data...), testName)
	if err != nil {
		t.Fatal(err)
	}

	// verify some errors on upload
	skylink, _, _, err = r.UploadNewMultipartSkyfileBlocking("DirectoryBasic", files, "notexists.html", false, false)
	if err == nil || !strings.Contains(err.Error(), skymodules.ErrInvalidDefaultPath.Error()) {
		t.Errorf("Expected '%v' instead error was '%v'", skymodules.ErrInvalidDefaultPath, err)
	}
}

// testDownloadDirectoryNested tests the download of a directory skyfile with
// a nested directory structure
func testDownloadDirectoryNested(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// upload a multi-file skyfile with a nested file structure
	files := []siatest.TestFile{
		{Name: "assets/images/file1.png", Data: []byte("file1.png_contents")},
		{Name: "assets/images/file2.png", Data: []byte("file2.png_contents")},
		{Name: "assets/index.html", Data: []byte("assets_index.html_contents")},
		{Name: "index.html", Data: []byte("index.html_contents")},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileBlocking("DirectoryNested", files, "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	var length uint64
	for _, file := range files {
		length += uint64(len(file.Data))
	}

	// note that index.html is listed first but is uploaded as the last file
	expectedMetadata := skymodules.SkyfileMetadata{
		Filename: "DirectoryNested",
		Length:   length,
		Subfiles: map[string]skymodules.SkyfileSubfileMetadata{
			"index.html": {
				FileMode:    os.FileMode(0644),
				Filename:    "index.html",
				ContentType: "text/html; charset=utf-8",
				Offset:      uint64(len(files[0].Data) + len(files[1].Data) + len(files[2].Data)),
				Len:         uint64(len(files[3].Data)),
			},
			"assets/images/file1.png": {
				FileMode:    os.FileMode(0644),
				Filename:    "assets/images/file1.png",
				ContentType: "image/png",
				Offset:      0,
				Len:         uint64(len(files[0].Data)),
			},
			"assets/images/file2.png": {
				FileMode:    os.FileMode(0644),
				Filename:    "assets/images/file2.png",
				ContentType: "image/png",
				Offset:      uint64(len(files[0].Data)),
				Len:         uint64(len(files[1].Data)),
			},
			"assets/index.html": {
				FileMode:    os.FileMode(0644),
				Filename:    "assets/index.html",
				ContentType: "text/html; charset=utf-8",
				Offset:      uint64(len(files[0].Data) + len(files[1].Data)),
				Len:         uint64(len(files[2].Data)),
			},
		},
		DefaultPath:        "",
		DisableDefaultPath: false,
		DirResMode:         skymodules.DirResModeStandard,
		DirResNotFound:     "",
		DirResNotFoundCode: 404,
	}

	testName := "NestedDirIndexDefaultPathIndex"

	// verify downloads
	err = verifyDownloadRaw(t, r, skylink, files[3].Data, testName)
	if err != nil {
		t.Fatal(err)
	}
	err = verifyDownloadAsArchive(t, r, skylink, fileMapFromFiles(files), testName)
	if err != nil {
		t.Fatal(err)
	}
	_, md, err := r.SkynetMetadataGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(md, expectedMetadata) {
		t.Fatal("mismatch")
	}

	testName = "NestedDirNoIndexDefaultPathEmpty"

	err = verifyDownloadDirectory(t, r, skylink+"/assets/images", append(files[0].Data, files[1].Data...), testName)
	if err != nil {
		t.Fatal(err)
	}
	err = verifyDownloadAsArchive(t, r, skylink+"/assets/images", fileMapFromFiles(files[:2]), testName)
	if err != nil {
		t.Fatal(err)
	}

	testName = "NestedDirSingleDefaultPathEmpty"

	expectedMetadata = skymodules.SkyfileMetadata{
		Filename: "/assets/index.html",
		Length:   uint64(len(files[2].Data)),
		Subfiles: map[string]skymodules.SkyfileSubfileMetadata{
			"assets/index.html": {
				FileMode:    os.FileMode(0644),
				Filename:    "assets/index.html",
				ContentType: "text/html; charset=utf-8",
				Offset:      0,
				Len:         uint64(len(files[2].Data)),
			},
		},
	}

	// verify downloading a nested file
	err = verifyDownloadRaw(t, r, skylink+"/assets/index.html", files[2].Data, testName)
	if err != nil {
		t.Fatal(err)
	}

	// upload the same files with the nested index.html as default
	// expect an error since nested default paths are not allowed
	files = []siatest.TestFile{
		{Name: "assets/images/file1.png", Data: []byte("file1.png_contents")},
		{Name: "assets/images/file2.png", Data: []byte("file2.png_contents")},
		{Name: "assets/index.html", Data: []byte("assets_index.html_contents")},
		{Name: "index.html", Data: []byte("index.html_contents")},
	}
	skylink, _, _, err = r.UploadNewMultipartSkyfileBlocking("DirectoryNested", files, "assets/index.html", false, true)
	if err == nil || !strings.Contains(err.Error(), "invalid default path provided") {
		t.Fatalf("expected error 'invalid default path provided', got %+v\n", err)
	}
}

// testDownloadContentDisposition tests that downloads have the correct
// 'Content-Disposition' header set when downloading as an attachment or as an
// archive.
func testDownloadContentDisposition(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// define a helper function that validates the 'Content-Disposition' header
	verifyCDHeader := func(header http.Header, value string) error {
		actual := header.Get("Content-Disposition")
		if actual != value {
			return fmt.Errorf("unexpected 'Content-Disposition' header, '%v' != '%v'", actual, value)
		}
		return nil
	}

	// define all possible values for the 'Content-Disposition' header
	name := "TestContentDisposition"
	inline := fmt.Sprintf("inline; filename=\"%v\"", name)
	attachment := fmt.Sprintf("attachment; filename=\"%v\"", name)
	attachmentZip := fmt.Sprintf("attachment; filename=\"%v.zip\"", name)
	attachmentTar := fmt.Sprintf("attachment; filename=\"%v.tar\"", name)
	attachmentTarGz := fmt.Sprintf("attachment; filename=\"%v.tar.gz\"", name)

	var header http.Header

	// upload a single file
	skylink, _, _, err := r.UploadNewSkyfileBlocking(name, 100, false)

	// no params
	_, header, err = r.SkynetSkylinkHead(skylink)
	err = errors.Compose(err, verifyCDHeader(header, inline))
	if err != nil {
		t.Fatal(errors.AddContext(err, "noparams"))
	}

	// 'attachment=false'
	_, header, err = r.SkynetSkylinkHeadWithAttachment(skylink, false)
	err = errors.Compose(err, verifyCDHeader(header, inline))
	if err != nil {
		t.Fatal(err)
	}

	// 'attachment=true'
	_, header, err = r.SkynetSkylinkHeadWithAttachment(skylink, true)
	err = errors.Compose(err, verifyCDHeader(header, attachment))
	if err != nil {
		t.Fatal(err)
	}

	// 'format=concat'
	_, header, err = r.SkynetSkylinkHeadWithFormat(skylink, skymodules.SkyfileFormatConcat)
	err = errors.Compose(err, verifyCDHeader(header, inline))
	if err != nil {
		t.Fatal(err)
	}

	// 'format=zip'
	_, header, err = r.SkynetSkylinkHeadWithFormat(skylink, skymodules.SkyfileFormatZip)
	err = errors.Compose(err, verifyCDHeader(header, attachmentZip))
	if err != nil {
		t.Fatal(err)
	}

	// 'format=tar'
	_, header, err = r.SkynetSkylinkHeadWithFormat(skylink, skymodules.SkyfileFormatTar)
	err = errors.Compose(err, verifyCDHeader(header, attachmentTar))
	if err != nil {
		t.Fatal(err)
	}

	// 'format=targz'
	_, header, err = r.SkynetSkylinkHeadWithFormat(skylink, skymodules.SkyfileFormatTarGz)
	err = errors.Compose(err, verifyCDHeader(header, attachmentTarGz))
	if err != nil {
		t.Fatal(err)
	}

	// if both attachment and format are set, format should take precedence
	values := url.Values{}
	values.Set("attachment", fmt.Sprintf("%t", true))
	values.Set("format", string(skymodules.SkyfileFormatZip))
	_, header, err = r.SkynetSkylinkHeadWithParameters(skylink, values)
	err = errors.Compose(err, verifyCDHeader(header, attachmentZip))
	if err != nil {
		t.Fatal(err)
	}
}

// testETag verifies the functionality of the ETag response header
func testETag(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// use the function used by the http library itself to compare etags
	etagStrongMatch := func(a, b string) bool {
		return a == b && a != "" && a[0] == '"'
	}

	// upload a single file
	file := make([]byte, 100)
	fastrand.Read(file)
	skylink, _, _, err := r.UploadNewSkyfileWithDataBlocking("testNotModified", file, false)
	if err != nil {
		t.Fatal(err)
	}

	// we use an unsafe client as it directly returns the http response object,
	// and we don't want to expose such methods on the actual client.
	uc := client.NewUnsafeClient(r.Client)

	// download the skylink
	resp, err := uc.SkynetSkylinkGetWithETag(skylink, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatal("Unexpected status code")
	}

	// verify ETag header is set
	eTag := resp.Header.Get("ETag")
	if eTag == "" {
		t.Fatal("Unexpected ETag response header")
	}

	// verify the ETag header is different for a HEAD requests
	status, header, err := r.SkynetSkylinkHead(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK {
		t.Fatal("Unexpected status code")
	}
	if header.Get("ETag") == eTag {
		t.Fatal("Unexpected ETag response header")
	}

	// download the skylink but now pass the ETag in the request header
	resp, err = uc.SkynetSkylinkGetWithETag(skylink, eTag)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// verify status code is 304 and no data was returned
	if resp.StatusCode != http.StatusNotModified {
		t.Fatal("Unexpected status code", resp.StatusCode)
	}
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if len(data) != 0 {
		t.Fatal("Unexpected response data")
	}

	// verify we miss the cache if the path is altered
	resp, err = uc.SkynetSkylinkGetWithETag(skylink+"/foo", eTag)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatal("Unexpected status code", resp.StatusCode)
	}

	// verify we miss the cache if format is passed
	resp, err = uc.SkynetSkylinkGetWithETag(skylink+"?format="+string(skymodules.SkyfileFormatZip), eTag)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatal("Unexpected status code", resp.StatusCode)
	}

	// verify this has an affect on the returned ETag
	if etagStrongMatch(eTag, resp.Header.Get("ETag")) {
		t.Fatal("Unexpected ETag")
	}

	// verify random query string params do not affect the ETag value
	resp, err = uc.SkynetSkylinkGetWithETag(skylink+"?foo=bar", "")
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatal("Unexpected status code", resp.StatusCode)
	}
	if !etagStrongMatch(eTag, resp.Header.Get("ETag")) {
		t.Fatal("Unexpected ETag", resp.Header.Get("ETag"))
	}

	// verify manipulating the ETag slightly misses the cache
	ETagCorrupted := "abcd" + eTag[4:]
	resp, err = uc.SkynetSkylinkGetWithETag(skylink+"?foo=bar", ETagCorrupted)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatal("Unexpected status code", resp.StatusCode)
	}
	data, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatal("Unexpected error", err)
	}
	if !bytes.Equal(file, data) {
		t.Fatal("Unexpected response data")
	}

	// turn the skylink into a V2 skylink and verify we get the same ETag
	var skylinkV1 skymodules.Skylink
	err = skylinkV1.LoadString(skylink)
	if err != nil {
		t.Fatal(err)
	}
	skylinkV2, err := r.NewSkylinkV2(skylinkV1)
	if err != nil {
		t.Fatal(err)
	}

	// download the skylink using the V2 skylink
	resp, err = uc.SkynetSkylinkGetWithETag(skylinkV2.String(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatal("Unexpected status code")
	}

	// verify ETag header is set
	eTagForV2Skylink := resp.Header.Get("ETag")
	if eTagForV2Skylink == "" {
		t.Fatal("Unexpected ETag response header")
	}
	if eTagForV2Skylink != eTag {
		t.Fatal("Unexpected ETag")
	}
}

// testSkynetSkylinkHeader tests that the 'Skynet-Skylink' is set both on the
// Skynet upload - and download route.
func testSkynetSkylinkHeader(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// create the siapath
	siapath, err := skymodules.NewSiaPath(t.Name())
	if err != nil {
		t.Fatal(err)
	}

	// create the upload params
	reader := bytes.NewReader(fastrand.Bytes(100))
	sup := skymodules.SkyfileUploadParameters{
		SiaPath:             siapath,
		BaseChunkRedundancy: 2,
		Filename:            t.Name(),
		Mode:                skymodules.DefaultFilePerm,
		Reader:              reader,
	}

	// upload the skyfile, use an unsafe client to get access to the response
	// headers
	uc := client.NewUnsafeClient(r.Client)
	header, body, err := uc.SkynetSkyfilePostRawResponse(sup)
	if err != nil {
		t.Fatal(err)
	}

	// parse the response manually
	var rshp api.SkynetSkyfileHandlerPOST
	err = json.Unmarshal(body, &rshp)
	if err != nil {
		t.Fatal(err)
	}

	// verify we get a valid skylink
	var skylink skymodules.Skylink
	err = skylink.LoadString(rshp.Skylink)
	if err != nil {
		t.Fatal(err)
	}

	// verify the response header contains the same Skylink
	if header.Get("Skynet-Skylink") != skylink.String() {
		t.Fatal("unexpected")
	}

	// verify a HEAD call has the response header as well, we use HEAD call as
	// this verifies both the HEAD and GET endpoint
	_, header, err = r.SkynetSkylinkHead(skylink.String())
	if err != nil {
		t.Fatal(err)
	}
	if header.Get("Skynet-Skylink") != skylink.String() {
		t.Fatal("unexpected")
	}
}

// TestSkynetSlowDownload is a regression test that verifies a download can take
// longer than the default skynet request timeout if data is being actively
// downloaded from the host. This test was added after a bug was found where the
// streambuffer context would timeout after 30s, even though a download of a
// large file was ongoing, but just slow due to its size.
func TestSkynetSlowDownload(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Miners:  1,
		Portals: 1,
	}
	testDir := skynetTestDir(t.Name())
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		t.Fatal("failed to create test group", err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	r := tg.Renters()[0]

	// Add hosts that have the slow download dependency
	deps := &dependencies.HostSlowDownload{}
	hostParams1 := node.Host(filepath.Join(testDir, "host1"))
	hostParams2 := node.Host(filepath.Join(testDir, "host2"))
	hostParams3 := node.Host(filepath.Join(testDir, "host3"))
	hostParams1.HostDeps = deps
	hostParams2.HostDeps = deps
	hostParams3.HostDeps = deps
	_, err = tg.AddNodes(hostParams1, hostParams2, hostParams3)
	if err != nil {
		t.Fatal(err)
	}

	// Upload a multi-part skyfile that consists out of multiple files, designed
	// in such a way that the overall time to download this file exceeds the
	// default request timeout of 30s. Keeping in mind that every read from a
	// slow host takes at least 1 second due to our dependency.
	sector := int(modules.SectorSize)
	files := []siatest.TestFile{
		{Name: "song1.flac", Data: fastrand.Bytes(sector * 3)},
		{Name: "song2.flac", Data: fastrand.Bytes(sector * 3)},
		{Name: "song3.flac", Data: fastrand.Bytes(sector * 3)},
		{Name: "song4.flac", Data: fastrand.Bytes(sector * 3)},
		{Name: "song5.flac", Data: fastrand.Bytes(sector * 3)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileBlocking(t.Name(), files, "", false, false)
	if err != nil {
		t.Fatal(err)
	}

	// Verify we can download the file
	start := time.Now()
	_, err = r.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the download took longer than the default request timeout
	elapsed := time.Since(start)
	if elapsed < api.DefaultSkynetRequestTimeout {
		t.Fatal("the download request should have taken longer than the default request timeout")
	}
}

// fileMapFromFiles is a helper that converts a list of test files to a file map
func fileMapFromFiles(tfs []siatest.TestFile) fileMap {
	fm := make(fileMap)
	for _, tf := range tfs {
		fm[tf.Name] = tf.Data
	}
	return fm
}

// verifyDownloadRaw is a helper function that downloads the content for the
// given skylink and verifies the response data and response headers.
func verifyDownloadRaw(t *testing.T, r *siatest.TestNode, skylink string, expectedData []byte, testName string) error {
	data, err := r.SkynetSkylinkGet(skylink)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, expectedData) {
		t.Log("Test:", testName)
		t.Log("expected data: ")
		siatest.PrintJSON(expectedData)
		t.Log("actual   data: ")
		siatest.PrintJSON(data)
		return errors.New("Unexpected data")
	}
	return nil
}

// verifyDownloadRaw is a helper function that downloads the content for the
// given skylink and verifies the response data and response headers. It will
// download the file using the `concat` format to be able to compare the data
// without it having to be an archive.
func verifyDownloadDirectory(t *testing.T, r *siatest.TestNode, skylink string, expectedData []byte, testName string) error {
	data, err := r.SkynetSkylinkConcatGet(skylink)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, expectedData) {
		t.Log("Test:", testName)
		t.Log("expected data: ", expectedData)
		t.Log("actual   data: ", data)
		return errors.New("Unexpected data")
	}
	return nil
}

// verifyDownloadAsArchive is a helper function that downloads the content for
// the given skylink and verifies the response data and response headers. It
// will download the file using all of the archive formats we support, verifying
// the contents of the archive for every type.
func verifyDownloadAsArchive(t *testing.T, r *siatest.TestNode, skylink string, expectedFiles fileMap, testName string) error {
	// zip
	header, reader, err := r.SkynetSkylinkZipReaderGet(skylink)
	if err != nil {
		return err
	}

	files, err := readZipArchive(reader)
	if err != nil {
		return err
	}
	err = reader.Close()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(files, expectedFiles) {
		t.Log("Test:", testName)
		t.Log("expected:", expectedFiles)
		t.Log("actual  :", files)
		return errors.New("Unexpected files")
	}
	ct := header.Get("Content-type")
	if ct != "application/zip" {
		return fmt.Errorf("unexpected 'Content-Type' header, expected 'application/zip' actual '%v'", ct)
	}

	// tar
	header, reader, err = r.SkynetSkylinkTarReaderGet(skylink)
	if err != nil {
		return err
	}
	files, err = readTarArchive(reader)
	if err != nil {
		return err
	}
	err = reader.Close()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(files, expectedFiles) {
		t.Log("Test:", testName)
		t.Log("expected:", expectedFiles)
		t.Log("actual  :", files)
		return errors.New("Unexpected files")
	}
	ct = header.Get("Content-type")
	if ct != "application/x-tar" {
		return fmt.Errorf("unexpected 'Content-Type' header, expected 'application/x-tar' actual '%v'", ct)
	}

	// tar gz
	header, reader, err = r.SkynetSkylinkTarGzReaderGet(skylink)
	if err != nil {
		return err
	}
	gzr, err := gzip.NewReader(reader)
	if err != nil {
		return err
	}
	files, err = readTarArchive(gzr)
	if err != nil {
		return err
	}
	err = errors.Compose(reader.Close(), gzr.Close())
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(files, expectedFiles) {
		t.Log("expected:", expectedFiles)
		t.Log("actual  :", files)
		return errors.New("Unexpected files")
	}
	ct = header.Get("Content-type")
	if ct != "application/gzip" {
		return fmt.Errorf("unexpected 'Content-Type' header, expected 'application/gzip' actual '%v'", ct)
	}
	return nil
}
