package skynet

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/node"
	"gitlab.com/SkynetLabs/skyd/node/api"
	"gitlab.com/SkynetLabs/skyd/node/api/client"
	"gitlab.com/SkynetLabs/skyd/siatest"
	"gitlab.com/SkynetLabs/skyd/siatest/dependencies"
	"gitlab.com/SkynetLabs/skyd/skykey"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/persist"
	"go.sia.tech/siad/types"
)

// TestSkynetSuiteOne verifies the functionality of Skynet, a decentralized CDN
// and sharing platform.
func TestSkynetSuiteOne(t *testing.T) {
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
		{Name: "Basic", Test: testSkynetBasic},
		{Name: "SkylinkV2Download", Test: testSkylinkV2Download},
		{Name: "ConvertSiaFile", Test: testConvertSiaFile},
		{Name: "MultipartUpload", Test: testSkynetMultipartUpload},
		{Name: "InvalidFilename", Test: testSkynetInvalidFilename},
		{Name: "SubDirDownload", Test: testSkynetSubDirDownload},
		{Name: "DisableForce", Test: testSkynetDisableForce},
		{Name: "Portals", Test: testSkynetPortals},
		{Name: "IncludeLayout", Test: testSkynetIncludeLayout},
		{Name: "RequestTimeout", Test: testSkynetRequestTimeout},
		{Name: "DryRunUpload", Test: testSkynetDryRunUpload},
		{Name: "RegressionTimeoutPanic", Test: testRegressionTimeoutPanic},
		{Name: "RenameSiaPath", Test: testRenameSiaPath},
		{Name: "NoWorkers", Test: testSkynetNoWorkers},
	}

	// Run tests
	if err := siatest.RunSubTests(t, groupParams, groupDir, subTests); err != nil {
		t.Fatal(err)
	}
}

// TestSkynetSuiteTwo verifies the functionality of Skynet, a decentralized CDN
// and sharing platform.
func TestSkynetSuiteTwo(t *testing.T) {
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
		{Name: "DefaultPath", Test: testSkynetDefaultPath},
		{Name: "DefaultPath_TableTest", Test: testSkynetDefaultPath_TableTest},
		{Name: "TryFiles", Test: testSkynetTryFiles},
		{Name: "TryFiles_TableTests", Test: testTryFiles_TableTests},
		{Name: "SingleFileNoSubfiles", Test: testSkynetSingleFileNoSubfiles},
		{Name: "DownloadFormats", Test: testSkynetDownloadFormats},
		{Name: "DownloadBaseSector", Test: testSkynetDownloadBaseSectorNoEncryption},
		{Name: "DownloadBaseSectorEncrypted", Test: testSkynetDownloadBaseSectorEncrypted},
		{Name: "FanoutRegression", Test: testSkynetFanoutRegression},
		{Name: "DownloadRange", Test: testSkynetDownloadRange},
		{Name: "DownloadRangeEncrypted", Test: testSkynetDownloadRangeEncrypted},
		{Name: "Registry", Test: testSkynetRegistryReadWrite},
		{Name: "Stats", Test: testSkynetStats},
		{Name: "RegistryUpdateMulti", Test: testUpdateRegistryMulti},
		{Name: "HostsForRegistryUpdate", Test: testHostsForRegistryUpdate},
		{Name: "RecursiveBaseSector", Test: testRecursiveBaseSector},
	}

	// Run tests
	if err := siatest.RunSubTests(t, groupParams, groupDir, subTests); err != nil {
		t.Fatal(err)
	}
}

// testSkynetBasic provides basic end-to-end testing for uploading skyfiles and
// downloading the resulting skylinks.
func testSkynetBasic(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// Create some data to upload as a skyfile.
	data := fastrand.Bytes(100 + siatest.Fuzz())
	// Need it to be a reader.
	reader := bytes.NewReader(data)
	// Call the upload skyfile client call.
	filename := "testSmall"
	uploadSiaPath, err := skymodules.NewSiaPath("testSmallPath")
	if err != nil {
		t.Fatal(err)
	}
	// Quick fuzz on the force value so that sometimes it is set, sometimes it
	// is not.
	var force bool
	if fastrand.Intn(2) == 0 {
		force = true
	}
	sup := skymodules.SkyfileUploadParameters{
		SiaPath:             uploadSiaPath,
		Force:               force,
		Root:                false,
		BaseChunkRedundancy: 2,
		Filename:            filename,
		Mode:                0640, // Intentionally does not match any defaults.
		Reader:              reader,
	}
	skylink, rshp, err := r.SkynetSkyfilePost(sup)
	if err != nil {
		t.Fatal(err)
	}
	var realSkylink skymodules.Skylink
	err = realSkylink.LoadString(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if rshp.MerkleRoot != realSkylink.MerkleRoot() {
		t.Fatal("mismatch")
	}
	if rshp.Bitfield != realSkylink.Bitfield() {
		t.Fatal("mismatch")
	}

	// Check the redundancy on the file.
	skynetUploadPath, err := skymodules.SkynetFolder.Join(uploadSiaPath.String())
	if err != nil {
		t.Fatal(err)
	}
	err = build.Retry(25, 250*time.Millisecond, func() error {
		uploadedFile, err := r.RenterFileRootGet(skynetUploadPath)
		if err != nil {
			return err
		}
		if uploadedFile.File.Redundancy != 2 {
			return fmt.Errorf("bad redundancy: %v", uploadedFile.File.Redundancy)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Try to download the file behind the skylink.
	fetchedData, err := r.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	h, metadata, err := r.SkynetMetadataGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fetchedData, data) {
		t.Error("upload and download doesn't match")
		t.Log(data)
		t.Log(fetchedData)
	}
	if metadata.Mode != 0640 {
		t.Error("bad mode")
	}
	if metadata.Filename != filename {
		t.Error("bad filename")
	}
	if skylink != h.Get(api.SkynetSkylinkHeader) {
		t.Fatal("skylink mismatch")
	}
	if skylink != h.Get(api.SkynetRequestedSkylinkHeader) {
		t.Fatal("skylink mismatch")
	}

	// Fetch the links metadata and compare it. Should match.
	h2, metadata2, err := r.SkynetMetadataGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(metadata, metadata2) {
		t.Log(metadata)
		t.Log(metadata2)
		t.Fatal("metadata doesn't match")
	}
	if skylink != h2.Get(api.SkynetSkylinkHeader) {
		t.Fatal("skylink mismatch")
	}
	if skylink != h2.Get(api.SkynetRequestedSkylinkHeader) {
		t.Fatal("skylink mismatch")
	}

	// Try to download the file explicitly using the ReaderGet method with the
	// no formatter.
	skylinkReader, err := r.SkynetSkylinkReaderGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	readerData, err := ioutil.ReadAll(skylinkReader)
	if err != nil {
		err = errors.Compose(err, skylinkReader.Close())
		t.Fatal(err)
	}
	err = skylinkReader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readerData, data) {
		t.Fatal("reader data doesn't match data")
	}

	// Try to download the file using the ReaderGet method with the concat
	// formatter.
	skylinkReader, err = r.SkynetSkylinkConcatReaderGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	readerData, err = ioutil.ReadAll(skylinkReader)
	if err != nil {
		err = errors.Compose(err, skylinkReader.Close())
		t.Fatal(err)
	}
	if !bytes.Equal(readerData, data) {
		t.Fatal("reader data doesn't match data")
	}
	err = skylinkReader.Close()
	if err != nil {
		t.Fatal(err)
	}

	// Try to download the file using the ReaderGet method with the zip
	// formatter.
	_, skylinkReader, err = r.SkynetSkylinkZipReaderGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	files, err := readZipArchive(skylinkReader)
	if err != nil {
		t.Fatal(err)
	}
	err = skylinkReader.Close()
	if err != nil {
		t.Fatal(err)
	}

	// verify the contents
	if len(files) != 1 {
		t.Fatal("Unexpected amount of files")
	}
	dataFile1Received, exists := files[filename]
	if !exists {
		t.Fatalf("file at path '%v' not present in zip", filename)
	}
	if !bytes.Equal(dataFile1Received, data) {
		t.Fatal("file data doesn't match expected content")
	}

	// Try to download the file using the ReaderGet method with the tar
	// formatter.
	_, skylinkReader, err = r.SkynetSkylinkTarReaderGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(skylinkReader)
	header, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != filename {
		t.Fatalf("expected filename in archive to be %v but was %v", filename, header.Name)
	}
	readerData, err = ioutil.ReadAll(tr)
	if err != nil {
		err = errors.Compose(err, skylinkReader.Close())
		t.Fatal(err)
	}
	if !bytes.Equal(readerData, data) {
		t.Fatal("reader data doesn't match data")
	}
	_, err = tr.Next()
	if !errors.Contains(err, io.EOF) {
		t.Fatal("expected error to be EOF but was", err)
	}
	err = skylinkReader.Close()
	if err != nil {
		t.Fatal(err)
	}

	// Try to download the file using the ReaderGet method with the targz
	// formatter.
	_, skylinkReader, err = r.SkynetSkylinkTarGzReaderGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	gzr, err := gzip.NewReader(skylinkReader)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := gzr.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	tr = tar.NewReader(gzr)
	header, err = tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != filename {
		t.Fatalf("expected filename in archive to be %v but was %v", filename, header.Name)
	}
	readerData, err = ioutil.ReadAll(tr)
	if err != nil {
		err = errors.Compose(err, skylinkReader.Close())
		t.Fatal(err)
	}
	if !bytes.Equal(readerData, data) {
		t.Fatal("reader data doesn't match data")
	}
	_, err = tr.Next()
	if !errors.Contains(err, io.EOF) {
		t.Fatal("expected error to be EOF but was", err)
	}
	err = skylinkReader.Close()
	if err != nil {
		t.Fatal(err)
	}

	// Get the list of files in the skynet directory and see if the file is
	// present.
	rdg, err := r.RenterDirRootGet(skymodules.SkynetFolder)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range rdg.Files {
		if f.Skylinks[0] == skylink {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expecting a file to be in the SkynetFolder after uploading")
	}

	// Create some data to upload as a skyfile.
	rootData := fastrand.Bytes(100 + siatest.Fuzz())
	// Need it to be a reader.
	rootReader := bytes.NewReader(rootData)
	// Call the upload skyfile client call.
	rootFilename := "rootTestSmall"
	rootUploadSiaPath, err := skymodules.NewSiaPath("rootTestSmallPath")
	if err != nil {
		t.Fatal(err)
	}
	// Quick fuzz on the force value so that sometimes it is set, sometimes it
	// is not.
	var rootForce bool
	if fastrand.Intn(2) == 0 {
		rootForce = true
	}
	rootLup := skymodules.SkyfileUploadParameters{
		SiaPath:             rootUploadSiaPath,
		Force:               rootForce,
		Root:                true,
		BaseChunkRedundancy: 3,
		Filename:            rootFilename,
		Mode:                0600, // Intentionally does not match any defaults.
		Reader:              rootReader,
	}
	_, _, err = r.SkynetSkyfilePost(rootLup)
	if err != nil {
		t.Fatal(err)
	}

	// Get the list of files in the skynet directory and see if the file is
	// present.
	rootRdg, err := r.RenterDirRootGet(skymodules.RootSiaPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(rootRdg.Files) != 1 {
		t.Fatal("expecting a file to be in the root folder after uploading")
	}
	err = build.Retry(250, 250*time.Millisecond, func() error {
		uploadedFile, err := r.RenterFileRootGet(rootUploadSiaPath)
		if err != nil {
			return err
		}
		if uploadedFile.File.Redundancy != 3 {
			return fmt.Errorf("bad redundancy: %v", uploadedFile.File.Redundancy)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Upload another skyfile, this time make it an empty file
	var noData []byte
	emptySiaPath, err := skymodules.NewSiaPath("testEmptyPath")
	if err != nil {
		t.Fatal(err)
	}
	emptySkylink, _, err := r.SkynetSkyfilePost(skymodules.SkyfileUploadParameters{
		SiaPath:             emptySiaPath,
		Force:               false,
		Root:                false,
		BaseChunkRedundancy: 2,
		Filename:            "testEmpty",
		Reader:              bytes.NewReader(noData),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err = r.SkynetSkylinkGet(emptySkylink)
	if err != nil {
		t.Fatal(err)
	}
	h3, metadata, err := r.SkynetMetadataGet(emptySkylink)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatal("Unexpected data")
	}
	if metadata.Length != 0 {
		t.Fatal("Unexpected metadata")
	}
	if emptySkylink != h3.Get(api.SkynetSkylinkHeader) {
		t.Fatal("skylink mismatch")
	}
	if emptySkylink != h3.Get(api.SkynetRequestedSkylinkHeader) {
		t.Fatal("skylink mismatch", emptySkylink)
	}

	// Upload another skyfile, this time ensure that the skyfile is more than
	// one sector.
	largeData := fastrand.Bytes(int(modules.SectorSize*2) + siatest.Fuzz())
	largeReader := bytes.NewReader(largeData)
	largeFilename := "testLarge"
	largeSiaPath, err := skymodules.NewSiaPath("testLargePath")
	if err != nil {
		t.Fatal(err)
	}
	var force2 bool
	if fastrand.Intn(2) == 0 {
		force2 = true
	}
	largeLup := skymodules.SkyfileUploadParameters{
		SiaPath:             largeSiaPath,
		Force:               force2,
		Root:                false,
		BaseChunkRedundancy: 2,
		Filename:            largeFilename,
		Reader:              largeReader,
	}
	largeSkylink, _, err := r.SkynetSkyfilePost(largeLup)
	if err != nil {
		t.Fatal(err)
	}
	largeFetchedData, err := r.SkynetSkylinkGet(largeSkylink)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(largeFetchedData, largeData) {
		t.Log(largeFetchedData)
		t.Log(largeData)
		t.Error("upload and download data does not match for large siafiles", len(largeFetchedData), len(largeData))
	}

	// Fetch the base sector and parse the skyfile layout
	baseSectorReader, err := r.SkynetBaseSectorGet(largeSkylink)
	if err != nil {
		t.Fatal(err)
	}
	baseSector, err := ioutil.ReadAll(baseSectorReader)
	if err != nil {
		t.Fatal(err)
	}
	var skyfileLayout skymodules.SkyfileLayout
	skyfileLayout.Decode(baseSector)

	// Assert the skyfile layout's data and parity pieces matches the defaults
	if int(skyfileLayout.FanoutDataPieces) != skymodules.RenterDefaultDataPieces {
		t.Fatal("unexpected number of data pieces")
	}
	if int(skyfileLayout.FanoutParityPieces) != skymodules.RenterDefaultParityPieces {
		t.Fatal("unexpected number of parity pieces")
	}

	// Check the metadata of the siafile, see that the metadata of the siafile
	// has the skylink referenced.
	largeUploadPath, err := skymodules.NewSiaPath("testLargePath")
	if err != nil {
		t.Fatal(err)
	}
	largeSkyfilePath, err := skymodules.SkynetFolder.Join(largeUploadPath.String())
	if err != nil {
		t.Fatal(err)
	}
	largeRenterFile, err := r.RenterFileRootGet(largeSkyfilePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(largeRenterFile.File.Skylinks) != 1 {
		t.Fatal("expecting one skylink:", len(largeRenterFile.File.Skylinks))
	}
	if largeRenterFile.File.Skylinks[0] != largeSkylink {
		t.Error("skylinks should match")
		t.Log(largeRenterFile.File.Skylinks[0])
		t.Log(largeSkylink)
	}

	// TODO: Need to verify the mode, name, and create-time. At this time, I'm
	// not sure how we can feed those out of the API. They aren't going to be
	// the same as the siafile values, because the siafile was created
	// separately.
	//
	// Maybe this can be accomplished by tagging a flag to the API which has the
	// layout and metadata streamed as the first bytes? Maybe there is some
	// easier way.

	// Pinning test.
	//
	// Try to download the file behind the skylink.
	pinSiaPath, err := skymodules.NewSiaPath("testSmallPinPath")
	if err != nil {
		t.Fatal(err)
	}
	pinLUP := skymodules.SkyfilePinParameters{
		SiaPath:             pinSiaPath,
		Force:               force,
		Root:                false,
		BaseChunkRedundancy: 2,
	}
	err = r.SkynetSkylinkPinPost(skylink, pinLUP)
	if err != nil {
		t.Fatal(err)
	}
	// Get the list of files in the skynet directory and see if the file is
	// present.
	fullPinSiaPath, err := skymodules.SkynetFolder.Join(pinSiaPath.String())
	if err != nil {
		t.Fatal(err)
	}
	// See if the file is present.
	pinnedFile, err := r.RenterFileRootGet(fullPinSiaPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(pinnedFile.File.Skylinks) != 1 {
		t.Fatal("expecting 1 skylink")
	}
	if pinnedFile.File.Skylinks[0] != skylink {
		t.Fatal("skylink mismatch")
	}

	// Unpinning test.
	//
	// Try deleting the file (equivalent to unpin).
	err = r.RenterFileDeleteRootPost(fullPinSiaPath)
	if err != nil {
		t.Fatal(err)
	}
	// Make sure the file is no longer present.
	_, err = r.RenterFileRootGet(fullPinSiaPath)
	if err == nil || !strings.Contains(err.Error(), filesystem.ErrNotExist.Error()) {
		t.Fatal("skyfile still present after deletion")
	}

	// Try another pin test, this time with the large skylink.
	largePinSiaPath, err := skymodules.NewSiaPath("testLargePinPath")
	if err != nil {
		t.Fatal(err)
	}
	largePinLUP := skymodules.SkyfilePinParameters{
		SiaPath:             largePinSiaPath,
		Force:               force,
		Root:                false,
		BaseChunkRedundancy: 2,
	}
	err = r.SkynetSkylinkPinPost(largeSkylink, largePinLUP)
	if err != nil {
		t.Fatal(err)
	}
	// Pin the file again but without specifying the BaseChunkRedundancy.
	// Use a different Siapath to avoid path conflict.
	largePinSiaPath, err = skymodules.NewSiaPath("testLargePinPath2")
	if err != nil {
		t.Fatal(err)
	}
	largePinLUP = skymodules.SkyfilePinParameters{
		SiaPath: largePinSiaPath,
		Force:   force,
		Root:    false,
	}
	err = r.SkynetSkylinkPinPost(largeSkylink, largePinLUP)
	if err != nil {
		t.Fatal(err)
	}
	// See if the file is present.
	fullLargePinSiaPath, err := skymodules.SkynetFolder.Join(largePinSiaPath.String())
	if err != nil {
		t.Fatal(err)
	}
	pinnedFile, err = r.RenterFileRootGet(fullLargePinSiaPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(pinnedFile.File.Skylinks) != 1 {
		t.Fatal("expecting 1 skylink")
	}
	if pinnedFile.File.Skylinks[0] != largeSkylink {
		t.Fatal("skylink mismatch")
	}
	// Try deleting the file.
	err = r.RenterFileDeleteRootPost(fullLargePinSiaPath)
	if err != nil {
		t.Fatal(err)
	}
	// Make sure the file is no longer present.
	_, err = r.RenterFileRootGet(fullLargePinSiaPath)
	if err == nil || !strings.Contains(err.Error(), filesystem.ErrNotExist.Error()) {
		t.Fatal("skyfile still present after deletion")
	}

	// TODO: We don't actually check at all whether the presence of the new
	// skylinks is going to keep the file online. We could do that by deleting
	// the old files and then churning the hosts over, and checking that the
	// renter does a repair operation to keep everyone alive.

	// TODO: Fetch both the skyfile and the siafile that was uploaded, make sure
	// that they both have the new skylink added to their metadata.

	// TODO: Need to verify the mode, name, and create-time. At this time, I'm
	// not sure how we can feed those out of the API. They aren't going to be
	// the same as the siafile values, because the siafile was created
	// separately.
	//
	// Maybe this can be accomplished by tagging a flag to the API which has the
	// layout and metadata streamed as the first bytes? Maybe there is some
	// easier way.
}

// testConvertSiaFile tests converting a siafile to a skyfile. This test checks
// for 1-of-N redundancies and N-of-M redundancies.
func testConvertSiaFile(t *testing.T, tg *siatest.TestGroup) {
	t.Run("1-of-N Conversion", func(t *testing.T) {
		testConversion(t, tg, 1, 2, t.Name())
	})
	t.Run("N-of-M Conversion", func(t *testing.T) {
		testConversion(t, tg, 2, 1, t.Name())
	})
}

// testConversion is a subtest for testConvertSiaFile
func testConversion(t *testing.T, tg *siatest.TestGroup, dp, pp uint64, skykeyName string) {
	r := tg.Renters()[0]
	// Upload a siafile that will then be converted to a skyfile.
	filesize := int(modules.SectorSize) + siatest.Fuzz()
	localFile, remoteFile, err := r.UploadNewFileBlocking(filesize, dp, pp, false)
	if err != nil {
		t.Fatal(err)
	}

	// Get the local and remote data for comparison
	localData, err := localFile.Data()
	if err != nil {
		t.Fatal(err)
	}
	_, remoteData, err := r.DownloadByStream(remoteFile)
	if err != nil {
		t.Fatal(err)
	}

	// Create Skyfile Upload Parameters
	sup := skymodules.SkyfileUploadParameters{
		SiaPath: skymodules.RandomSiaPath(),
	}

	// Try and convert to a Skyfile
	sshp, err := r.SkynetConvertSiafileToSkyfilePost(sup, remoteFile.SiaPath())
	if err != nil {
		t.Fatal("Expected conversion from Siafile to Skyfile Post to succeed.")
	}

	// Try to download the skylink.
	skylink := sshp.Skylink
	fetchedData, err := r.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Compare the data fetched from the Skylink to the local data and the
	// previously uploaded data
	if !bytes.Equal(fetchedData, localData) {
		t.Error("converted skylink data doesn't match local data")
	}
	if !bytes.Equal(fetchedData, remoteData) {
		t.Error("converted skylink data doesn't match remote data")
	}

	// Converting with encryption is not supported. Call the convert method to
	// ensure we do not panic and we return the expected error
	//
	// Add SkyKey
	sk, err := r.SkykeyCreateKeyPost(skykeyName, skykey.TypePrivateID)
	if err != nil {
		t.Fatal(err)
	}

	// Convert file again
	sup.SkykeyName = sk.Name
	sup.Force = true

	// Convert to a Skyfile
	_, err = r.SkynetConvertSiafileToSkyfilePost(sup, remoteFile.SiaPath())
	if err == nil || !strings.Contains(err.Error(), renter.ErrEncryptionNotSupported.Error()) {
		t.Fatalf("Expected error %v, but got %v", renter.ErrEncryptionNotSupported, err)
	}
}

// testSkynetMultipartUpload tests you can perform a multipart upload. It will
// verify the upload without any subfiles, with small subfiles and with large
// subfiles. Small files are files which are smaller than one sector, and thus
// don't need a fanout. Large files are files what span multiple sectors
func testSkynetMultipartUpload(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	sk, err := r.SkykeyCreateKeyPost(t.Name(), skykey.TypePrivateID)
	if err != nil {
		t.Fatal(err)
	}

	// Test no files provided
	fileName := "TestNoFileUpload"
	_, _, _, err = r.UploadNewMultipartSkyfileBlocking(fileName, nil, "", false, false)
	if err == nil || !strings.Contains(err.Error(), "could not find multipart file") {
		t.Fatal("Expected upload to fail because no files are given, err:", err)
	}

	// TEST EMPTY FILE
	fileName = "TestEmptyFileUpload"
	emptyFile := siatest.TestFile{Name: "file", Data: []byte{}}
	skylink, _, _, err := r.UploadNewMultipartSkyfileBlocking(fileName, []siatest.TestFile{emptyFile}, "", false, false)
	if err != nil {
		t.Fatal("Expected upload of empty file to succeed", err)
	}
	data, err := r.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal("Expected download of empty file to succeed", err)
	}
	_, md, err := r.SkynetMetadataGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatal("Unexpected data")
	}
	if md.Length != 0 {
		t.Fatal("Unexpected metadata length", md.Length)
	}

	// TEST SMALL SUBFILE
	//
	// Define test func
	testSmallFunc := func(files []siatest.TestFile, fileName, skykeyName string) {
		skylink, _, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(fileName, files, "", false, []string{}, nil, false, skykeyName, skykey.SkykeyID{})
		if err != nil {
			t.Fatal(err)
		}
		var realSkylink skymodules.Skylink
		err = realSkylink.LoadString(skylink)
		if err != nil {
			t.Fatal(err)
		}

		// Try to download the file behind the skylink.
		_, fileMetadata, err := r.SkynetMetadataGet(skylink)
		if err != nil {
			t.Fatal(err)
		}

		// Check the metadata
		rootFile := files[0]
		nestedFile := files[1]
		expected := skymodules.SkyfileMetadata{
			Filename: fileName,
			Subfiles: map[string]skymodules.SkyfileSubfileMetadata{
				rootFile.Name: {
					FileMode:    os.FileMode(0644),
					Filename:    rootFile.Name,
					ContentType: "application/octet-stream",
					Offset:      0,
					Len:         uint64(len(rootFile.Data)),
				},
				nestedFile.Name: {
					FileMode:    os.FileMode(0644),
					Filename:    nestedFile.Name,
					ContentType: "text/html; charset=utf-8",
					Offset:      uint64(len(rootFile.Data)),
					Len:         uint64(len(nestedFile.Data)),
				},
			},
			Length:   uint64(len(rootFile.Data) + len(nestedFile.Data)),
			TryFiles: skymodules.DefaultTryFilesValue,
		}
		if !reflect.DeepEqual(expected, fileMetadata) {
			t.Log("Expected:", expected)
			t.Log("Actual:  ", fileMetadata)
			t.Fatal("Metadata mismatch")
		}

		// Download the second file
		nestedfile, err := r.SkynetSkylinkGet(fmt.Sprintf("%s/%s", skylink, nestedFile.Name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(nestedfile, nestedFile.Data) {
			t.Fatal("Expected only second file to be downloaded")
		}
	}

	// Add a file at root level and a nested file
	rootFile := siatest.TestFile{Name: "file1", Data: []byte("File1Contents")}
	nestedFile := siatest.TestFile{Name: "nested/file2.html", Data: []byte("File2Contents")}
	files := []siatest.TestFile{rootFile, nestedFile}
	fileName = "TestFolderUpload"
	testSmallFunc(files, fileName, "")

	// Test Encryption
	fileName = "TestFolderUpload_Encrypted"
	testSmallFunc(files, fileName, sk.Name)

	// LARGE SUBFILES
	//
	// Define test function
	largeTestFunc := func(files []siatest.TestFile, fileName, skykeyName string) {
		// Upload the skyfile
		skylink, sup, _, err := r.UploadNewMultipartSkyfileEncryptedBlocking(fileName, files, "", false, skymodules.DefaultTryFilesValue, nil, false, skykeyName, skykey.SkykeyID{})
		if err != nil {
			t.Fatal(err)
		}

		// Define files
		rootFile := files[0]
		nestedFile := files[1]

		// Download the data
		largeFetchedData, err := r.SkynetSkylinkConcatGet(skylink)
		if err != nil {
			t.Fatal(err)
		}
		allData := append(rootFile.Data, nestedFile.Data...)
		if !bytes.Equal(largeFetchedData, allData) {
			t.Fatal("upload and download data does not match for large siafiles", len(largeFetchedData), len(allData))
		}

		// Check the metadata of the siafile, see that the metadata of the siafile
		// has the skylink referenced.
		largeSkyfilePath, err := skymodules.SkynetFolder.Join(sup.SiaPath.String())
		if err != nil {
			t.Fatal(err)
		}
		largeRenterFile, err := r.RenterFileRootGet(largeSkyfilePath)
		if err != nil {
			t.Fatal(err)
		}
		if len(largeRenterFile.File.Skylinks) != 1 {
			t.Fatal("expecting one skylink:", len(largeRenterFile.File.Skylinks))
		}
		if largeRenterFile.File.Skylinks[0] != skylink {
			t.Log(largeRenterFile.File.Skylinks[0])
			t.Log(skylink)
			t.Fatal("skylinks should match")
		}

		// Test the small root file download
		smallFetchedData, err := r.SkynetSkylinkGet(fmt.Sprintf("%s/%s", skylink, rootFile.Name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(smallFetchedData, rootFile.Data) {
			t.Fatal("upload and download data does not match for large siafiles with subfiles", len(smallFetchedData), len(rootFile.Data))
		}

		// Test the large nested file download
		largeFetchedData, err = r.SkynetSkylinkGet(fmt.Sprintf("%s/%s", skylink, nestedFile.Name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(largeFetchedData, nestedFile.Data) {
			t.Fatal("upload and download data does not match for large siafiles with subfiles", len(largeFetchedData), len(nestedFile.Data))
		}
	}

	// Add a small file at root level and a large nested file
	rootFile = siatest.TestFile{Name: "smallFile1.txt", Data: []byte("File1Contents")}
	largeData := fastrand.Bytes(2 * int(modules.SectorSize))
	nestedFile = siatest.TestFile{Name: "nested/largefile2.txt", Data: largeData}
	files = []siatest.TestFile{rootFile, nestedFile}
	fileName = "TestFolderUploadLarge"
	largeTestFunc(files, fileName, "")

	// Test Encryption
	fileName = "TestFolderUploadLarge_Encrypted"
	largeTestFunc(files, fileName, sk.Name)
}

// testSkynetStats tests the validity of the response of /skynet/stats endpoint
// by uploading some test files and verifying that the reported statistics
// change proportionally
func testSkynetStats(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// This test relies on state from the previous tests. Make sure we are
	// starting from a place of updated metadata
	err := r.RenterBubblePost(skymodules.RootSiaPath(), true)
	if err != nil {
		t.Error(err)
	}

	// Enable portal mode.
	rg, err := r.RenterGet()
	if err != nil {
		t.Error(err)
	}
	a := rg.Settings.Allowance
	a.PaymentContractInitialFunding = types.SiacoinPrecision
	err = r.RenterPostAllowance(a)
	if err != nil {
		t.Error(err)
	}

	// Sleep for a few seconds to give all of the health and repair loops time
	// to get settled.
	time.Sleep(time.Second * 3)

	// Get the stats
	stats, err := r.SkynetStatsGet()
	if err != nil {
		t.Fatal(err)
	}
	// Check portal mode.
	if !stats.PortalMode {
		t.Fatal("renter should be in portal mode")
	}

	// Disable portal mode.
	a.PaymentContractInitialFunding = types.ZeroCurrency
	err = r.RenterPostAllowance(a)
	if err != nil {
		t.Error(err)
	}

	// Get the stats again.
	stats, err = r.SkynetStatsGet()
	if err != nil {
		t.Fatal(err)
	}
	// Check portal mode.
	if stats.PortalMode {
		t.Fatal("renter should not be in portal mode")
	}

	// Check that there are files in the filesystem.
	if stats.NumFiles == 0 {
		t.Fatal("test prereq requires files to exist")
	}
	// Check that the system scan duration has been set.
	if stats.SystemHealthScanDurationHours == 0 {
		t.Fatal("system health scan duration is not set")
	}

	// verify it contains the node's version information
	expected := build.NodeVersion
	if build.ReleaseTag != "" {
		expected += "-" + build.ReleaseTag
	}
	if stats.VersionInfo.Version != expected {
		t.Fatalf("Unexpected version return, expected '%v', actual '%v'", expected, stats.VersionInfo.Version)
	}
	if stats.VersionInfo.GitRevision != build.GitRevision {
		t.Fatalf("Unexpected git revision return, expected '%v', actual '%v'", build.GitRevision, stats.VersionInfo.GitRevision)
	}

	// Uptime should be non zero
	if stats.Uptime == 0 {
		t.Error("Uptime is zero")
	}

	// Check registry stats are set
	if stats.RegistryRead15mP99ms == 0 {
		t.Error("readregistry p99 is zero")
	}
	if stats.RegistryRead15mP999ms == 0 {
		t.Error("readregistry p999 is zero")
	}
	if stats.RegistryRead15mP9999ms == 0 {
		t.Error("readregistry p9999 is zero")
	}

	// create two test files with sizes below and above the sector size
	files := make(map[string]uint64)
	files["statfile1"] = 2033
	files["statfile2"] = 2*modules.SectorSize + 123

	// upload the files and keep track of their expected impact on the stats
	var uploadedFilesSize, uploadedFilesCount uint64
	var sps []skymodules.SiaPath
	for name, size := range files {
		_, sup, _, err := r.UploadNewSkyfileBlocking(name, size, false)
		if err != nil {
			t.Fatal(err)
		}

		sp, err := sup.SiaPath.Rebase(skymodules.RootSiaPath(), skymodules.SkynetFolder)
		if err != nil {
			t.Fatal(err)
		}
		sps = append(sps, sp)

		uploadedFilesCount++
		if size < modules.SectorSize {
			// small files get padded up to a full sector
			uploadedFilesSize += modules.SectorSize
		} else {
			// large files have an extra sector with header data
			uploadedFilesSize += size + modules.SectorSize
		}
	}

	// Create a siafile and convert it
	size := 100
	_, rf, err := r.UploadNewFileBlocking(size, 1, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	sup := skymodules.SkyfileUploadParameters{
		SiaPath: rf.SiaPath(),
		Mode:    skymodules.DefaultFilePerm,
		Force:   false,
		Root:    false,
	}
	_, err = r.SkynetConvertSiafileToSkyfilePost(sup, rf.SiaPath())
	if err != nil {
		t.Fatal(err)
	}
	// Increment the file count once for the converted file
	uploadedFilesCount++
	// Increment the file size for the basesector that is uploaded during the
	// conversion as well as the file size of the siafile.
	uploadedFilesSize += modules.SectorSize
	uploadedFilesSize += uint64(size)

	// Check that the right stats were returned.
	statsBefore := stats
	tries := 1
	err = build.Retry(100, 100*time.Millisecond, func() error {
		// Make sure that the filesystem is being updated
		if tries%10 == 0 {
			err = r.RenterBubblePost(skymodules.RootSiaPath(), true)
			if err != nil {
				return err
			}
		}
		tries++
		statsAfter, err := r.SkynetStatsGet()
		if err != nil {
			return err
		}
		var countErr, sizeErr, healthErr error
		if uint64(statsBefore.NumFiles)+uploadedFilesCount != uint64(statsAfter.NumFiles) {
			countErr = fmt.Errorf("stats did not report the correct number of files. expected %d, found %d", uint64(statsBefore.NumFiles)+uploadedFilesCount, statsAfter.NumFiles)
		}
		if statsBefore.Storage+uploadedFilesSize != statsAfter.Storage {
			sizeErr = fmt.Errorf("stats did not report the correct size. expected %d, found %d", statsBefore.Storage+uploadedFilesSize, statsAfter.Storage)
		}
		// Just make sure that a health is returned
		if statsAfter.MaxHealthPercentage == 0 {
			healthErr = errors.New("no MaxHealthPercentage retuned")
		}
		return errors.Compose(countErr, sizeErr, healthErr)
	})
	if err != nil {
		t.Error(err)
	}

	// Delete the files.
	for _, sp := range sps {
		err = r.RenterFileDeleteRootPost(sp)
		if err != nil {
			t.Fatal(err)
		}
		extSP, err := sp.AddSuffixStr(skymodules.ExtendedSuffix)
		if err != nil {
			t.Fatal(err)
		}
		// This might not always succeed which is fine. We know how many files
		// we expect afterwards.
		_ = r.RenterFileDeleteRootPost(extSP)
	}

	// Delete the converted file
	err = r.RenterFileDeletePost(rf.SiaPath())
	if err != nil {
		t.Fatal(err)
	}
	convertSP, err := rf.SiaPath().Rebase(skymodules.RootSiaPath(), skymodules.SkynetFolder)
	if err != nil {
		t.Fatal(err)
	}
	err = r.RenterFileDeleteRootPost(convertSP)
	if err != nil {
		t.Fatal(err)
	}

	// Check the stats after the delete operation. Do it in a retry to account
	// for the bubble.
	tries = 1
	err = build.Retry(100, 100*time.Millisecond, func() error {
		// Make sure that the filesystem is being updated
		if tries%10 == 0 {
			err = r.RenterBubblePost(skymodules.RootSiaPath(), true)
			if err != nil {
				return err
			}
		}
		tries++
		statsAfter, err := r.SkynetStatsGet()
		if err != nil {
			t.Fatal(err)
		}
		var countErr, sizeErr, healthErr error
		if statsAfter.NumFiles != statsBefore.NumFiles {
			countErr = fmt.Errorf("stats did not report the correct number of files. expected %d, found %d", uint64(statsBefore.NumFiles), statsAfter.NumFiles)
		}
		if statsAfter.Storage != statsBefore.Storage {
			sizeErr = fmt.Errorf("stats did not report the correct size. expected %d, found %d", statsBefore.Storage, statsAfter.Storage)
		}
		// Just make sure that a health is returned
		if statsAfter.MaxHealthPercentage == 0 {
			healthErr = errors.New("no MaxHealthPercentage retuned")
		}
		return errors.Compose(countErr, sizeErr, healthErr)
	})
	if err != nil {
		t.Error(err)
	}

	// Check that the throughput information for the various throughput fields
	// is not blank.
	//
	// NOTE: this test depends on other tests to have performed each type of
	// activity already, this test does not do the activity itself.
	stats, err = r.SkynetStatsGet()
	if err != nil {
		t.Fatal(err)
	}
	if stats.BaseSectorUpload15mDataPoints <= 1 {
		t.Error("throughput is being recorded at or below baseline:", stats.BaseSectorUpload15mDataPoints)
	}
	if stats.ChunkUpload15mDataPoints <= 1 {
		t.Error("throughput is being recorded at or below baseline:", stats.ChunkUpload15mDataPoints)
	}
	if stats.RegistryRead15mDataPoints <= 1 {
		t.Error("throughput is being recorded at or below baseline:", stats.RegistryRead15mDataPoints)
	}
	if stats.RegistryWrite15mDataPoints <= 1 {
		t.Error("throughput is being recorded at or below baseline:", stats.RegistryWrite15mDataPoints)
	}
	if stats.StreamBufferRead15mDataPoints <= 1 {
		t.Error("throughput is being recorded at or below baseline:", stats.StreamBufferRead15mDataPoints)
	}
}

// TestSkynetInvalidFilename verifies that posting a Skyfile with invalid
// filenames such as empty filenames, names containing ./ or ../ or names
// starting with a forward-slash fails.
func testSkynetInvalidFilename(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// Create some data to upload as a skyfile.
	data := fastrand.Bytes(100 + siatest.Fuzz())

	filenames := []string{
		"",
		"../test",
		"./test",
		"/test",
		"foo//bar",
		"test/./test",
		"test/../test",
		"/test//foo/../bar/",
	}

	for _, filename := range filenames {
		uploadSiaPath, err := skymodules.NewSiaPath("testInvalidFilename" + persist.RandomSuffix())
		if err != nil {
			t.Fatal(err)
		}

		sup := skymodules.SkyfileUploadParameters{
			SiaPath:             uploadSiaPath,
			Force:               false,
			Root:                false,
			BaseChunkRedundancy: 2,
			Filename:            filename,
			Mode:                0640, // Intentionally does not match any defaults.
			Reader:              bytes.NewReader(data),
		}

		// Try posting the skyfile with an invalid filename
		_, _, err = r.SkynetSkyfilePost(sup)
		if err == nil || !strings.Contains(err.Error(), skymodules.ErrInvalidPathString.Error()) {
			t.Log("Error:", err)
			t.Fatal("Expected SkynetSkyfilePost to fail due to invalid filename")
		}

		// Do the same for a multipart upload
		body := new(bytes.Buffer)
		writer := multipart.NewWriter(body)
		data = []byte("File1Contents")
		subfile, err := skymodules.AddMultipartFile(writer, data, "files[]", filename, 0600, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = writer.Close()
		if err != nil {
			t.Fatal(err)
		}

		// Call the upload skyfile client call.
		uploadSiaPath, err = skymodules.NewSiaPath("testInvalidFilenameMultipart" + persist.RandomSuffix())
		if err != nil {
			t.Fatal(err)
		}

		subfiles := make(skymodules.SkyfileSubfiles)
		subfiles[subfile.Filename] = subfile
		mup := skymodules.SkyfileMultipartUploadParameters{
			SiaPath:             uploadSiaPath,
			Force:               false,
			Root:                false,
			BaseChunkRedundancy: 2,
			Reader:              bytes.NewReader(body.Bytes()),
			ContentType:         writer.FormDataContentType(),
			Filename:            "testInvalidFilenameMultipart",
		}

		_, _, err = r.SkynetSkyfileMultiPartPost(mup)
		if err == nil || (!strings.Contains(err.Error(), skymodules.ErrInvalidPathString.Error()) && !strings.Contains(err.Error(), skymodules.ErrEmptyFilename.Error())) {
			t.Log("Error:", err)
			t.Fatal("Expected SkynetSkyfileMultiPartPost to fail due to invalid filename")
		}
	}

	// These cases should succeed.
	uploadSiaPath, err := skymodules.NewSiaPath("testInvalidFilename")
	if err != nil {
		t.Fatal(err)
	}
	sup := skymodules.SkyfileUploadParameters{
		SiaPath:             uploadSiaPath,
		Force:               false,
		Root:                false,
		BaseChunkRedundancy: 2,
		Filename:            "testInvalidFilename",
		Mode:                0640, // Intentionally does not match any defaults.
		Reader:              bytes.NewReader(data),
	}
	_, _, err = r.SkynetSkyfilePost(sup)
	if err != nil {
		t.Log("Error:", err)
		t.Fatal("Expected SkynetSkyfilePost to succeed if valid filename is provided")
	}

	// recreate the reader
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)

	subfile, err := skymodules.AddMultipartFile(writer, []byte("File1Contents"), "files[]", "testInvalidFilenameMultipart", 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = writer.Close()
	if err != nil {
		t.Fatal(err)
	}

	subfiles := make(skymodules.SkyfileSubfiles)
	subfiles[subfile.Filename] = subfile
	uploadSiaPath, err = skymodules.NewSiaPath("testInvalidFilenameMultipart")
	if err != nil {
		t.Fatal(err)
	}
	mup := skymodules.SkyfileMultipartUploadParameters{
		SiaPath:             uploadSiaPath,
		Force:               false,
		Root:                false,
		BaseChunkRedundancy: 2,
		Reader:              bytes.NewReader(body.Bytes()),
		ContentType:         writer.FormDataContentType(),
		Filename:            "testInvalidFilenameMultipart",
	}

	_, _, err = r.SkynetSkyfileMultiPartPost(mup)
	if err != nil {
		t.Log("Error:", err)
		t.Fatal("Expected SkynetSkyfileMultiPartPost to succeed if filename is provided")
	}
}

// testSkynetDownloadFormats verifies downloading data in different formats
func testSkynetDownloadFormats(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	dataFile1 := []byte("file1.txt")
	dataFile2 := []byte("file2.txt")
	dataFile3 := []byte("file3.txt")
	filePath1 := "a/5.f4f8b583.chunk.js"
	filePath2 := "a/5.f4f.chunk.js.map"
	filePath3 := "b/file3.txt"
	_, err := skymodules.AddMultipartFile(writer, dataFile1, "files[]", filePath1, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = skymodules.AddMultipartFile(writer, dataFile2, "files[]", filePath2, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = skymodules.AddMultipartFile(writer, dataFile3, "files[]", filePath3, 0640, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	uploadSiaPath, err := skymodules.NewSiaPath("testSkynetDownloadFormats")
	if err != nil {
		t.Fatal(err)
	}

	reader := bytes.NewReader(body.Bytes())
	mup := skymodules.SkyfileMultipartUploadParameters{
		SiaPath:             uploadSiaPath,
		Force:               false,
		Root:                false,
		BaseChunkRedundancy: 2,
		Reader:              reader,
		ContentType:         writer.FormDataContentType(),
		Filename:            "testSkynetSubfileDownload",
	}

	skylink, _, err := r.SkynetSkyfileMultiPartPost(mup)
	if err != nil {
		t.Fatal(err)
	}

	// download the data specifying the 'concat' format
	allData, err := r.SkynetSkylinkConcatGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	expected := append(dataFile1, dataFile2...)
	expected = append(expected, dataFile3...)
	if !bytes.Equal(expected, allData) {
		t.Log("expected:", expected)
		t.Log("actual:", allData)
		t.Fatal("Unexpected data for dir A")
	}

	// now specify the zip format
	_, skyfileReader, err := r.SkynetSkylinkZipReaderGet(skylink)
	if err != nil {
		t.Fatal(err)
	}

	// read the zip archive
	files, err := readZipArchive(skyfileReader)
	if err != nil {
		t.Fatal(err)
	}
	err = skyfileReader.Close()
	if err != nil {
		t.Fatal(err)
	}

	// verify the contents
	dataFile1Received, exists := files[filePath1]
	if !exists {
		t.Fatalf("file at path '%v' not present in zip", filePath1)
	}
	if !bytes.Equal(dataFile1Received, dataFile1) {
		t.Fatal("file data doesn't match expected content")
	}
	dataFile2Received, exists := files[filePath2]
	if !exists {
		t.Fatalf("file at path '%v' not present in zip", filePath2)
	}
	if !bytes.Equal(dataFile2Received, dataFile2) {
		t.Fatal("file data doesn't match expected content")
	}
	dataFile3Received, exists := files[filePath3]
	if !exists {
		t.Fatalf("file at path '%v' not present in zip", filePath3)
	}
	if !bytes.Equal(dataFile3Received, dataFile3) {
		t.Log(dataFile3Received)
		t.Log(dataFile3)
		t.Fatal("file data doesn't match expected content")
	}

	// now specify the tar format
	_, skyfileReader, err = r.SkynetSkylinkTarReaderGet(skylink)
	if err != nil {
		t.Fatal(err)
	}

	// read the tar archive
	files, err = readTarArchive(skyfileReader)
	if err != nil {
		t.Fatal(err)
	}
	err = skyfileReader.Close()
	if err != nil {
		t.Fatal(err)
	}

	// verify the contents
	dataFile1Received, exists = files[filePath1]
	if !exists {
		t.Fatalf("file at path '%v' not present in zip", filePath1)
	}
	if !bytes.Equal(dataFile1Received, dataFile1) {
		t.Fatal("file data doesn't match expected content")
	}
	dataFile2Received, exists = files[filePath2]
	if !exists {
		t.Fatalf("file at path '%v' not present in zip", filePath2)
	}
	if !bytes.Equal(dataFile2Received, dataFile2) {
		t.Fatal("file data doesn't match expected content")
	}
	dataFile3Received, exists = files[filePath3]
	if !exists {
		t.Fatalf("file at path '%v' not present in zip", filePath3)
	}
	if !bytes.Equal(dataFile3Received, dataFile3) {
		t.Log(dataFile3Received)
		t.Log(dataFile3)
		t.Fatal("file data doesn't match expected content")
	}

	// now specify the targz format
	_, skyfileReader, err = r.SkynetSkylinkTarGzReaderGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	gzr, err := gzip.NewReader(skyfileReader)
	if err != nil {
		t.Fatal(err)
	}
	files, err = readTarArchive(gzr)
	if err != nil {
		t.Fatal(err)
	}
	err = errors.Compose(skyfileReader.Close(), gzr.Close())
	if err != nil {
		t.Fatal(err)
	}

	// verify the contents
	dataFile1Received, exists = files[filePath1]
	if !exists {
		t.Fatalf("file at path '%v' not present in zip", filePath1)
	}
	if !bytes.Equal(dataFile1Received, dataFile1) {
		t.Fatal("file data doesn't match expected content")
	}
	dataFile2Received, exists = files[filePath2]
	if !exists {
		t.Fatalf("file at path '%v' not present in zip", filePath2)
	}
	if !bytes.Equal(dataFile2Received, dataFile2) {
		t.Fatal("file data doesn't match expected content")
	}
	dataFile3Received, exists = files[filePath3]
	if !exists {
		t.Fatalf("file at path '%v' not present in zip", filePath3)
	}
	if !bytes.Equal(dataFile3Received, dataFile3) {
		t.Log(dataFile3Received)
		t.Log(dataFile3)
		t.Fatal("file data doesn't match expected content")
	}

	// get all data for path "a" using the concat format
	dataDirA, err := r.SkynetSkylinkConcatGet(fmt.Sprintf("%s/a", skylink))
	if err != nil {
		t.Fatal(err)
	}
	expected = append(dataFile1, dataFile2...)
	if !bytes.Equal(expected, dataDirA) {
		t.Log("expected:", expected)
		t.Log("actual:", dataDirA)
		t.Fatal("Unexpected data for dir A")
	}

	// now specify the tar format
	_, skyfileReader, err = r.SkynetSkylinkTarReaderGet(fmt.Sprintf("%s/a", skylink))
	if err != nil {
		t.Fatal(err)
	}

	// read the tar archive
	files, err = readTarArchive(skyfileReader)
	if err != nil {
		t.Fatal(err)
	}
	err = skyfileReader.Close()
	if err != nil {
		t.Fatal(err)
	}

	// verify the contents
	dataFile1Received, exists = files[filePath1]
	if !exists {
		t.Fatalf("file at path '%v' not present in zip", filePath1)
	}
	if !bytes.Equal(dataFile1Received, dataFile1) {
		t.Fatal("file data doesn't match expected content")
	}
	dataFile2Received, exists = files[filePath2]
	if !exists {
		t.Fatalf("file at path '%v' not present in zip", filePath2)
	}
	if !bytes.Equal(dataFile2Received, dataFile2) {
		t.Fatal("file data doesn't match expected content")
	}
	if len(files) != 2 {
		t.Fatal("unexpected amount of files")
	}

	// now specify the targz format
	_, skyfileReader, err = r.SkynetSkylinkTarGzReaderGet(fmt.Sprintf("%s/a", skylink))
	if err != nil {
		t.Fatal(err)
	}
	gzr, err = gzip.NewReader(skyfileReader)
	if err != nil {
		t.Fatal(err)
	}
	files, err = readTarArchive(gzr)
	if err != nil {
		t.Fatal(err)
	}
	err = errors.Compose(skyfileReader.Close(), gzr.Close())
	if err != nil {
		t.Fatal(err)
	}

	// verify the contents
	dataFile1Received, exists = files[filePath1]
	if !exists {
		t.Fatalf("file at path '%v' not present in zip", filePath1)
	}
	if !bytes.Equal(dataFile1Received, dataFile1) {
		t.Fatal("file data doesn't match expected content")
	}
	dataFile2Received, exists = files[filePath2]
	if !exists {
		t.Fatalf("file at path '%v' not present in zip", filePath2)
	}
	if !bytes.Equal(dataFile2Received, dataFile2) {
		t.Fatal("file data doesn't match expected content")
	}
	if len(files) != 2 {
		t.Fatal("unexpected amount of files")
	}

	// now specify the zip format
	_, skyfileReader, err = r.SkynetSkylinkZipReaderGet(fmt.Sprintf("%s/a", skylink))
	if err != nil {
		t.Fatal(err)
	}

	// verify we get a 400 if we supply an unsupported format parameter
	_, err = r.SkynetSkylinkGet(fmt.Sprintf("%s/b?format=raw", skylink))
	if err == nil || !strings.Contains(err.Error(), "unable to parse 'format'") {
		t.Fatal("Expected download to fail because we are downloading a directory and an invalid format was provided, err:", err)
	}

	// verify we default to the `zip` format if it is a directory and we have
	// not specified it (use a HEAD call as that returns the response headers)
	_, header, err := r.SkynetSkylinkHead(skylink)
	if err != nil {
		t.Fatal("unexpected error")
	}
	ct := header.Get("Content-Type")
	if ct != "application/zip" {
		t.Fatal("unexpected content type: ", ct)
	}
}

// testSkynetDownloadRangeEncrypted verifies we can download a certain range
// within an encrypted skyfile. This large file part of this test was added to
// verify whether `DecryptBytesInPlace` was properly decrypting the fanout bytes
// for offsets other than 0.
func testSkynetDownloadRangeEncrypted(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// add a skykey
	sk, err := r.SkykeyCreateKeyPost(t.Name(), skykey.TypePrivateID)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("SmallFile", func(t *testing.T) {
		skynetDownloadRangeTest(t, tg, sk.Name, false)
	})
	t.Run("LargeFile", func(t *testing.T) {
		skynetDownloadRangeTest(t, tg, sk.Name, true)
	})
}

// testSkynetDownloadRange verifies we can download a certain range within a
// skyfile.
func testSkynetDownloadRange(t *testing.T, tg *siatest.TestGroup) {
	t.Run("SmallFile", func(t *testing.T) {
		skynetDownloadRangeTest(t, tg, "", false)
	})
	t.Run("LargeFile", func(t *testing.T) {
		skynetDownloadRangeTest(t, tg, "", true)
	})
}

// skynetDownloadRangeTest verifies different conditions of skynet downloads
// with range requests.
func skynetDownloadRangeTest(t *testing.T, tg *siatest.TestGroup, skykeyName string, largeFile bool) {
	r := tg.Renters()[0]

	// generate file params
	name := t.Name() + persist.RandomSuffix()
	var size uint64
	if largeFile {
		size = uint64(4 * int(modules.SectorSize))
	} else {
		size = 100
	}
	data := fastrand.Bytes(int(size))

	// upload a skyfile
	_, _, sshp, err := r.UploadNewEncryptedSkyfileBlocking(name, data, skykeyName, false)
	if err != nil {
		t.Fatal(err)
	}

	// calculate random range parameters
	segment := uint64(crypto.SegmentSize)
	var offset, length uint64
	if largeFile {
		offset = fastrand.Uint64n(size-modules.SectorSize) + 1
		length = fastrand.Uint64n(size-offset-segment) + 1
	} else {
		offset = fastrand.Uint64n(size-segment) + 1
		length = fastrand.Uint64n(size-offset) + 1
	}

	// fetch the data at given range using the Header range request
	result, err := r.SkynetSkylinkRange(sshp.Skylink, offset, offset+length)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result, data[offset:offset+length]) {
		t.Logf("range %v-%v\n", offset, offset+length)
		t.Log("expected:", data[offset:offset+length], len(data[offset:offset+length]))
		t.Log("actual:", result, len(result))
		t.Fatal("unexpected")
	}

	// fetch the data at given range using the params range request
	result, err = r.SkynetSkylinkRangeParams(sshp.Skylink, offset, offset+length)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result, data[offset:offset+length]) {
		t.Logf("range %v-%v\n", offset, offset+length)
		t.Log("expected:", data[offset:offset+length], len(data[offset:offset+length]))
		t.Log("actual:", result, len(result))
		t.Fatal("unexpected")
	}

	// Test some specific cases
	// Verify some specific cases
	rand := fastrand.Uint64n(size)
	var tests = []struct {
		from uint64
		to   uint64
	}{
		{0, 0},       // Requesting the first byte of a file
		{0, 1},       // Request the first byte of a file
		{rand, rand}, // Requesting a random single byte of the file
	}
	for _, test := range tests {
		_, err = r.SkynetSkylinkRange(sshp.Skylink, test.from, test.to)
		if err != nil {
			t.Log("Failed Test Case:", test)
			t.Fatal(err)
		}
		_, err = r.SkynetSkylinkRangeParams(sshp.Skylink, test.from, test.to)
		if err != nil {
			t.Log("Failed Test Case:", test)
			t.Fatal(err)
		}
	}
}

// testSkynetDownloadBaseSectorEncrypted tests downloading a skylink's encrypted
// baseSector
func testSkynetDownloadBaseSectorEncrypted(t *testing.T, tg *siatest.TestGroup) {
	testSkynetDownloadBaseSector(t, tg, "basesectorkey")
}

// testSkynetDownloadBaseSectorNoEncryption tests downloading a skylink's
// baseSector
func testSkynetDownloadBaseSectorNoEncryption(t *testing.T, tg *siatest.TestGroup) {
	testSkynetDownloadBaseSector(t, tg, "")
}

// testSkynetDownloadBaseSector tests downloading a skylink's baseSector
func testSkynetDownloadBaseSector(t *testing.T, tg *siatest.TestGroup, skykeyName string) {
	r := tg.Renters()[0]

	// Add the SkyKey
	var sk skykey.Skykey
	var err error
	if skykeyName != "" {
		sk, err = r.SkykeyCreateKeyPost(skykeyName, skykey.TypePrivateID)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Upload a small skyfile
	filename := "onlyBaseSector" + persist.RandomSuffix()
	size := 100 + siatest.Fuzz()
	smallFileData := fastrand.Bytes(size)
	skylink, _, sshp, err := r.UploadNewEncryptedSkyfileBlocking(filename, smallFileData, skykeyName, false)
	if err != nil {
		t.Fatal(err)
	}

	// Create a v2 skylink from it.
	var skylinkV1 skymodules.Skylink
	err = skylinkV1.LoadString(skylink)
	if err != nil {
		t.Fatal(err)
	}
	skylinkV2, err := r.NewSkylinkV2(skylinkV1)
	if err != nil {
		t.Fatal(err)
	}

	// Download the BaseSector reader
	baseSectorReader, err := r.SkynetBaseSectorGet(skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Read the baseSector
	baseSector, err := ioutil.ReadAll(baseSectorReader)
	if err != nil {
		t.Fatal(err)
	}

	// Download the BaseSector reader for the V2 link.
	baseSectorReaderV2, err := r.SkynetBaseSectorGet(skylinkV2.String())
	if err != nil {
		t.Fatal(err)
	}

	// Read the baseSector
	baseSectorV2, err := ioutil.ReadAll(baseSectorReaderV2)
	if err != nil {
		t.Fatal(err)
	}

	// They should be the same.
	if !bytes.Equal(baseSector, baseSectorV2) {
		t.Fatal("base sectors don't match")
	}

	// Check for encryption
	encrypted := skymodules.IsEncryptedBaseSector(baseSector)
	if encrypted != (skykeyName != "") {
		t.Fatal("wrong encrypted state", encrypted, skykeyName)
	}
	if encrypted {
		_, err = skymodules.DecryptBaseSector(baseSector, sk)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Parse the skyfile metadata from the baseSector
	_, fanoutBytes, metadata, _, baseSectorPayload, err := skymodules.ParseSkyfileMetadata(baseSector)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the metadata
	expected := skymodules.SkyfileMetadata{
		Filename: filename,
		Length:   uint64(size),
		Mode:     os.FileMode(skymodules.DefaultFilePerm),
	}

	if !reflect.DeepEqual(expected, metadata) {
		siatest.PrintJSON(expected)
		siatest.PrintJSON(metadata)
		t.Error("Metadata not equal")
	}

	// Verify the file data
	if !bytes.Equal(smallFileData, baseSectorPayload) {
		t.Log("FileData bytes:", smallFileData)
		t.Log("BaseSectorPayload bytes:", baseSectorPayload)
		t.Errorf("Bytes not equal")
	}

	// Since this was a small file upload there should be no fanout bytes
	if len(fanoutBytes) != 0 {
		t.Error("Expected 0 fanout bytes:", fanoutBytes)
	}

	// Verify DownloadByRoot gives the same information
	rootSectorReader, err := r.SkynetDownloadByRootGet(sshp.MerkleRoot, 0, modules.SectorSize, -1)
	if err != nil {
		t.Fatal(err)
	}

	// Read the rootSector
	rootSector, err := ioutil.ReadAll(rootSectorReader)
	if err != nil {
		t.Fatal(err)
	}

	// Check for encryption
	encrypted = skymodules.IsEncryptedBaseSector(rootSector)
	if encrypted != (skykeyName != "") {
		t.Fatal("wrong encrypted state", encrypted, skykeyName)
	}
	if encrypted {
		_, err = skymodules.DecryptBaseSector(rootSector, sk)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Parse the skyfile metadata from the rootSector
	_, fanoutBytes, metadata, _, rootSectorPayload, err := skymodules.ParseSkyfileMetadata(rootSector)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the metadata
	if !reflect.DeepEqual(expected, metadata) {
		siatest.PrintJSON(expected)
		siatest.PrintJSON(metadata)
		t.Error("Metadata not equal")
	}

	// Verify the file data
	if !bytes.Equal(smallFileData, rootSectorPayload) {
		t.Log("FileData bytes:", smallFileData)
		t.Log("rootSectorPayload bytes:", rootSectorPayload)
		t.Errorf("Bytes not equal")
	}

	// Since this was a small file upload there should be no fanout bytes
	if len(fanoutBytes) != 0 {
		t.Error("Expected 0 fanout bytes:", fanoutBytes)
	}
}

// TestSkynetDownloadByRoot verifies the functionality of the download by root
// routes. It is separate as it requires an amount of hosts equal to the total
// amount of pieces per chunk.
func TestSkynetDownloadByRoot(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Define the parameters.
	numHosts := 6
	groupParams := siatest.GroupParams{
		Hosts:  numHosts,
		Miners: 1,
	}
	groupDir := skynetTestDir(t.Name())

	// Create a testgroup.
	tg, err := siatest.NewGroupFromTemplate(groupDir, groupParams)
	if err != nil {
		t.Fatal(errors.AddContext(err, "failed to create group"))
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Update the renter's allowance to support 6 hosts
	renterParams := node.Renter(filepath.Join(groupDir, "renter"))
	renterParams.Allowance = siatest.DefaultAllowance
	renterParams.Allowance.Hosts = uint64(numHosts)
	_, err = tg.AddNodes(renterParams)
	if err != nil {
		t.Fatal(err)
	}

	// Test the standard flow.
	t.Run("NoEncryption", func(t *testing.T) {
		testSkynetDownloadByRoot(t, tg, "")
	})
	t.Run("Encrypted", func(t *testing.T) {
		testSkynetDownloadByRoot(t, tg, "rootkey")
	})
}

// testSkynetDownloadByRoot tests downloading by root
func testSkynetDownloadByRoot(t *testing.T, tg *siatest.TestGroup, skykeyName string) {
	r := tg.Renters()[0]

	// Add the SkyKey
	var sk skykey.Skykey
	var err error
	if skykeyName != "" {
		sk, err = r.SkykeyCreateKeyPost(skykeyName, skykey.TypePrivateID)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Upload a skyfile that will have a fanout
	filename := "byRootLargeFile" + persist.RandomSuffix()
	size := 2*int(modules.SectorSize) + siatest.Fuzz()
	fileData := fastrand.Bytes(size)
	_, _, sshp, err := r.UploadNewEncryptedSkyfileBlocking(filename, fileData, skykeyName, false)
	if err != nil {
		t.Fatal(err)
	}

	// Download the base sector
	reader, err := r.SkynetDownloadByRootGet(sshp.MerkleRoot, 0, modules.SectorSize, -1)
	if err != nil {
		t.Fatal(err)
	}

	// Read the baseSector
	baseSector, err := ioutil.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}

	// Check for encryption
	encrypted := skymodules.IsEncryptedBaseSector(baseSector)
	if encrypted != (skykeyName != "") {
		t.Fatal("wrong encrypted state", encrypted, skykeyName)
	}
	var fileKey skykey.Skykey
	if encrypted {
		fileKey, err = skymodules.DecryptBaseSector(baseSector, sk)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Parse the information from the BaseSector
	layout, fanoutBytes, metadata, _, baseSectorPayload, err := skymodules.ParseSkyfileMetadata(baseSector)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the metadata
	expected := skymodules.SkyfileMetadata{
		Filename: filename,
		Length:   uint64(size),
		Mode:     os.FileMode(skymodules.DefaultFilePerm),
	}
	if !reflect.DeepEqual(expected, metadata) {
		siatest.PrintJSON(expected)
		siatest.PrintJSON(metadata)
		t.Error("Metadata not equal")
	}

	// The baseSector should be empty since there is a fanout
	if len(baseSectorPayload) != 0 {
		t.Error("baseSectorPayload should be empty:", baseSectorPayload)
	}

	// For large files there should be fanout bytes
	if len(fanoutBytes) == 0 {
		t.Fatal("no fanout bytes")
	}

	// Decode Fanout
	piecesPerChunk, chunkRootsSize, numChunks, err := skymodules.DecodeFanout(layout, fanoutBytes)
	if err != nil {
		t.Fatal(err)
	}

	// Calculate the expected pieces per chunk, and keep track of the original
	// pieces per chunk. If there's no encryption and there's only 1 data piece,
	// the fanout bytes will only contain a single piece (as the other pieces
	// will be identical, so that would be wasting space). We need to take this
	// into account when recovering the data as the EC will expect the original
	// amount of pieces.
	expectedPPC := layout.FanoutDataPieces + layout.FanoutParityPieces
	originalPPC := expectedPPC
	if layout.FanoutDataPieces == 1 && layout.CipherType == crypto.TypePlain {
		expectedPPC = 1
	}

	// Verify fanout information
	if piecesPerChunk != uint64(expectedPPC) {
		t.Fatal("piecesPerChunk incorrect", piecesPerChunk)
	}
	if chunkRootsSize != crypto.HashSize*piecesPerChunk {
		t.Fatal("chunkRootsSize incorrect", chunkRootsSize)
	}

	// Derive the fanout key
	var fanoutKey crypto.CipherKey
	if encrypted {
		fanoutKey, err = skymodules.DeriveFanoutKey(&layout, fileKey)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Create the erasure coder
	ec, err := skymodules.NewRSSubCode(int(layout.FanoutDataPieces), int(layout.FanoutParityPieces), crypto.SegmentSize)
	if err != nil {
		t.Fatal(err)
	}

	chunkSize := (modules.SectorSize - layout.CipherType.Overhead()) * uint64(layout.FanoutDataPieces)
	// Create list of chunk roots
	chunkRoots := make([][]crypto.Hash, 0, numChunks)
	for i := uint64(0); i < numChunks; i++ {
		root := make([]crypto.Hash, piecesPerChunk)
		for j := uint64(0); j < piecesPerChunk; j++ {
			fanoutOffset := (i * chunkRootsSize) + (j * crypto.HashSize)
			copy(root[j][:], fanoutBytes[fanoutOffset:])
		}
		chunkRoots = append(chunkRoots, root)
	}

	// Download roots
	var rootBytes []byte
	var blankHash crypto.Hash
	for i := uint64(0); i < numChunks; i++ {
		// Create the pieces for this chunk
		pieces := make([][]byte, len(chunkRoots[i]))
		for j := uint64(0); j < piecesPerChunk; j++ {
			// Ignore null roots
			if chunkRoots[i][j] == blankHash {
				continue
			}

			// Download the sector
			reader, err := r.SkynetDownloadByRootGet(chunkRoots[i][j], 0, modules.SectorSize, -1)
			if err != nil {
				t.Log("root", chunkRoots[i][j])
				t.Fatal(err)
			}

			// Read the sector
			sector, err := ioutil.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}

			// Decrypt to data if needed
			if encrypted {
				key := fanoutKey.Derive(i, j)
				_, err = key.DecryptBytesInPlace(sector, 0)
				if err != nil {
					t.Fatal(err)
				}
			}

			// Add the sector to the list of pieces
			pieces[j] = sector
		}

		// Decode the erasure coded chunk
		var chunkBytes []byte
		if ec != nil {
			buf := bytes.NewBuffer(nil)
			if len(pieces) == 1 && originalPPC > 1 {
				deduped := make([][]byte, originalPPC)
				for i := range deduped {
					deduped[i] = make([]byte, len(pieces[0]))
					copy(deduped[i], pieces[0])
				}
				pieces = deduped
			}
			err = ec.Recover(pieces, chunkSize, buf)
			if err != nil {
				t.Fatal(err)
			}
			chunkBytes = buf.Bytes()
		} else {
			// The unencrypted file is not erasure coded so just read the piece
			// data directly
			for _, p := range pieces {
				chunkBytes = append(chunkBytes, p...)
			}
		}
		rootBytes = append(rootBytes, chunkBytes...)
	}

	// Truncate download data to the file length
	rootBytes = rootBytes[:size]

	// Verify bytes
	if !reflect.DeepEqual(fileData, rootBytes) {
		t.Log("FileData bytes:", fileData)
		t.Log("root bytes:", rootBytes)
		t.Error("Bytes not equal")
	}
}

// testSkynetFanoutRegression is a regression test that ensures the fanout bytes
// of a skyfile don't contain any empty hashes
func testSkynetFanoutRegression(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// Add a skykey to the renter
	sk, err := r.SkykeyCreateKeyPost("fanout", skykey.TypePrivateID)
	if err != nil {
		t.Fatal(err)
	}

	// Upload a file with a large number of parity pieces to we can be reasonable
	// confident that the upload will not fully complete before the fanout needs
	// to be generated.
	size := 2*int(modules.SectorSize) + siatest.Fuzz()
	data := fastrand.Bytes(size)
	skylink, _, _, _, err := r.UploadSkyfileCustom("regression", data, sk.Name, 20, false)
	if err != nil {
		t.Fatal(err)
	}

	// Download the basesector to check the fanout bytes
	baseSectorReader, err := r.SkynetBaseSectorGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	baseSector, err := ioutil.ReadAll(baseSectorReader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = skymodules.DecryptBaseSector(baseSector, sk)
	if err != nil {
		t.Fatal(err)
	}
	_, fanoutBytes, _, _, _, err := skymodules.ParseSkyfileMetadata(baseSector)
	if err != nil {
		t.Fatal(err)
	}

	// FanoutBytes should not contain any empty hashes
	for i := 0; i < len(fanoutBytes); {
		end := i + crypto.HashSize
		var emptyHash crypto.Hash
		root := fanoutBytes[i:end]
		if bytes.Equal(root, emptyHash[:]) {
			t.Fatal("empty hash found in fanout")
		}
		i = end
	}
}

// testSkynetSubDirDownload verifies downloading data from a skyfile using a
// path to download single subfiles or subdirectories
func testSkynetSubDirDownload(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)

	dataFile1 := []byte("file1.txt")
	dataFile2 := []byte("file2.txt")
	dataFile3 := []byte("file3.txt")
	filePath1 := "a/5.f4f8b583.chunk.js"
	filePath2 := "a/5.f4f.chunk.js.map"
	filePath3 := "b/file3.txt"
	_, err := skymodules.AddMultipartFile(writer, dataFile1, "files[]", filePath1, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = skymodules.AddMultipartFile(writer, dataFile2, "files[]", filePath2, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = skymodules.AddMultipartFile(writer, dataFile3, "files[]", filePath3, 0640, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(body.Bytes())

	name := "testSkynetSubfileDownload"
	uploadSiaPath, err := skymodules.NewSiaPath(name)
	if err != nil {
		t.Fatal(err)
	}

	mup := skymodules.SkyfileMultipartUploadParameters{
		SiaPath:             uploadSiaPath,
		Force:               false,
		Root:                false,
		BaseChunkRedundancy: 2,
		Reader:              reader,
		ContentType:         writer.FormDataContentType(),
		Filename:            name,
	}

	skylink, _, err := r.SkynetSkyfileMultiPartPost(mup)
	if err != nil {
		t.Fatal(err)
	}

	// get all the data
	data, err := r.SkynetSkylinkConcatGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	_, metadata, err := r.SkynetMetadataGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Filename != name {
		t.Fatal("Unexpected filename", metadata.Filename, name)
	}
	expected := append(dataFile1, dataFile2...)
	expected = append(expected, dataFile3...)
	if !bytes.Equal(data, expected) {
		t.Fatal("Unexpected data")
	}

	// get all data for path "a"
	data, err = r.SkynetSkylinkConcatGet(fmt.Sprintf("%s/a", skylink))
	if err != nil {
		t.Fatal(err)
	}
	expected = append(dataFile1, dataFile2...)
	if !bytes.Equal(data, expected) {
		t.Fatal("Unexpected data")
	}

	// get all data for path "b"
	data, err = r.SkynetSkylinkConcatGet(fmt.Sprintf("%s/b", skylink))
	if err != nil {
		t.Fatal(err)
	}
	expected = dataFile3
	if !bytes.Equal(expected, data) {
		t.Fatal("Unexpected data")
	}
	mdF3, ok := metadata.Subfiles["b/file3.txt"]
	if !ok {
		t.Fatal("Expected subfile metadata of file3 to be present")
	}

	mdF3Expected := skymodules.SkyfileSubfileMetadata{
		FileMode:    os.FileMode(0640),
		Filename:    "b/file3.txt",
		ContentType: "text/plain; charset=utf-8",
		Offset:      uint64(len(dataFile1) + len(dataFile2)),
		Len:         uint64(len(dataFile3)),
	}
	if !reflect.DeepEqual(mdF3, mdF3Expected) {
		t.Log("expected: ", mdF3Expected)
		t.Log("actual: ", mdF3)
		t.Fatal("Unexpected subfile metadata for file 3")
	}

	// get a single sub file
	downloadFile2, err := r.SkynetSkylinkGet(fmt.Sprintf("%s/a/5.f4f.chunk.js.map", skylink))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dataFile2, downloadFile2) {
		t.Log("expected:", dataFile2)
		t.Log("actual:", downloadFile2)
		t.Fatal("Unexpected data for file 2")
	}
}

// testSkynetDisableForce verifies the behavior of force and the header that
// allows disabling forcefully uploading a Skyfile
func testSkynetDisableForce(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// Upload Skyfile
	_, sup, _, err := r.UploadNewSkyfileBlocking(t.Name(), 100, false)
	if err != nil {
		t.Fatal(err)
	}

	// Upload at same path without force, assert this fails
	sup = skymodules.SkyfileUploadParameters{
		Filename: t.Name(),
		Reader:   bytes.NewReader([]byte{1, 2, 3}),
		SiaPath:  sup.SiaPath,
		Force:    false,
	}
	_, _, err = r.SkynetSkyfilePost(sup)
	if err == nil {
		t.Fatal("Expected the upload without force to fail but it didn't.")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatal(err)
	}

	// Upload once more, but now use force. It should allow us to
	// overwrite the file at the existing path
	sup.Force = true
	_, _, err = r.SkynetSkyfilePost(sup)
	if err != nil {
		t.Fatal(err)
	}

	// Upload using the force flag again, however now we set the
	// Skynet-Disable-Force to true, which should prevent us from uploading.
	// Because we have to pass in a custom header, we have to setup the request
	// ourselves and can not use the client.
	_, _, err = r.SkynetSkyfilePostDisableForce(sup, true)
	if err == nil {
		t.Fatal("Unexpected response")
	}
	if !strings.Contains(err.Error(), "'force' has been disabled") {
		t.Log(err)
		t.Fatalf("Unexpected response, expected error to contain a mention of the force flag but instaed received: %v", err.Error())
	}
}

// TestSkynetDownloadStats is a test that verifies whether overdrive downloads
// base sectors and fanout sectors are properly reflected in the stats. This is
// separate test using a custom dependency because this was causing an NDF in
// the TestSkynetStats test.
func TestSkynetDownloadStats(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Hosts:  3,
		Miners: 1,
	}
	groupDir := skynetTestDir(t.Name())
	tg, err := siatest.NewGroupFromTemplate(groupDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Define a portal with a dependency that forces overdrive on downloads
	renterParams := node.Renter(filepath.Join(groupDir, t.Name()))
	renterParams.CreatePortal = true
	deps := dependencies.NewDependencyOverdriveDownload()
	renterParams.RenterDeps = deps
	nodes, err := tg.AddNodes(renterParams)
	if err != nil {
		t.Fatal(err)
	}
	r := nodes[0]

	// Check all stats are 0 still
	ss, err := r.SkynetStatsGet()
	if ss.BaseSectorOverdriveAvg != 0 {
		t.Fatal(err, ss.BaseSectorOverdriveAvg)
	}
	if ss.BaseSectorOverdrivePct != 0 {
		t.Fatal(err, ss.BaseSectorOverdrivePct)
	}
	if ss.FanoutSectorOverdriveAvg != 0 {
		t.Fatal(err, ss.FanoutSectorOverdriveAvg)
	}
	if ss.FanoutSectorOverdrivePct != 0 {
		t.Fatal(err, ss.FanoutSectorOverdrivePct)
	}

	// Upload a small and large file with N-M redundancy
	skylinkSmall, _, _, err := r.UploadNewSkyfileBlocking("small", modules.SectorSize/2, false)
	if err != nil {
		t.Fatal(err)
	}
	_, remoteFile, err := r.UploadNewFileBlocking(int(modules.SectorSize)*2, 2, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	sup := skymodules.SkyfileUploadParameters{
		SiaPath: skymodules.RandomSiaPath(),
	}
	rhsp, err := r.SkynetConvertSiafileToSkyfilePost(sup, remoteFile.SiaPath())
	if err != nil {
		t.Fatal("Expected conversion from Siafile to Skyfile Post to succeed.")
	}
	skylinkLarge := rhsp.Skylink

	// Enable the dependency
	deps.Enable()

	// Download both skylinks
	_, err = r.SkynetSkylinkGet(skylinkSmall)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.SkynetSkylinkGet(skylinkLarge)
	if err != nil {
		t.Fatal(err)
	}

	// Check all download stats are not 0
	ss, err = r.SkynetStatsGet()
	if ss.BaseSectorOverdriveAvg == 0 {
		t.Fatal(err, ss.BaseSectorOverdriveAvg)
	}
	if ss.BaseSectorOverdrivePct == 0 {
		t.Fatal(err, ss.BaseSectorOverdrivePct)
	}
	if ss.FanoutSectorOverdriveAvg == 0 {
		t.Fatal(err, ss.FanoutSectorOverdriveAvg)
	}
	if ss.FanoutSectorOverdrivePct == 0 {
		t.Fatal(err, ss.FanoutSectorOverdrivePct)
	}
}

// TestSkynetBlocklist verifies the functionality of the Skynet blocklist.
func TestSkynetBlocklist(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Hosts:  3,
		Miners: 1,
	}
	groupDir := skynetTestDir(t.Name())
	tg, err := siatest.NewGroupFromTemplate(groupDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Define a portal with dependency
	portalDir := filepath.Join(groupDir, "portal")
	portalParams := node.Renter(portalDir)
	portalParams.CreatePortal = true
	deps := &dependencies.DependencyToggleDisableDeleteBlockedFiles{}
	portalParams.RenterDeps = deps
	_, err = tg.AddNodes(portalParams)
	if err != nil {
		t.Fatal(err)
	}

	// Run subtests
	t.Run("BlocklistHash", func(t *testing.T) {
		testSkynetBlocklistHash(t, tg, deps)
	})
	t.Run("BlocklistSkylinkV1", func(t *testing.T) {
		testSkynetBlocklistSkylink(t, tg, deps, false)
	})
	t.Run("BlocklistSkylinkV2", func(t *testing.T) {
		testSkynetBlocklistSkylink(t, tg, deps, true)
	})
	t.Run("BlocklistUpgrade", func(t *testing.T) {
		testSkynetBlocklistUpgrade(t, tg)
	})
}

// testSkynetBlocklistHash tests the skynet blocklist module when submitting
// hashes of the skylink's merkleroot
func testSkynetBlocklistHash(t *testing.T, tg *siatest.TestGroup, deps *dependencies.DependencyToggleDisableDeleteBlockedFiles) {
	testSkynetBlocklist(t, tg, deps, true, false)
}

// testSkynetBlocklistSkylink tests the skynet blocklist module when submitting
// skylinks
func testSkynetBlocklistSkylink(t *testing.T, tg *siatest.TestGroup, deps *dependencies.DependencyToggleDisableDeleteBlockedFiles, isV2Skylink bool) {
	testSkynetBlocklist(t, tg, deps, false, isV2Skylink)
}

// testSkynetBlocklist tests the skynet blocklist module
func testSkynetBlocklist(t *testing.T, tg *siatest.TestGroup, deps *dependencies.DependencyToggleDisableDeleteBlockedFiles, isHash, isV2Skylink bool) {
	r := tg.Renters()[0]
	deps.DisableDeleteBlockedFiles(true)

	// Create skyfile upload params, data should be larger than a sector size to
	// test large file uploads and the deletion of their extended data.
	size := modules.SectorSize + uint64(100+siatest.Fuzz())
	skylink, sup, sshp, err := r.UploadNewSkyfileBlocking(t.Name(), size, false)
	if err != nil {
		t.Fatal(err)
	}

	// Convert to V2 Skylink
	if isV2Skylink {
		skylinkV2, err := r.NewSkylinkV2FromString(skylink)
		if err != nil {
			t.Fatal(err)
		}
		skylink = skylinkV2.Skylink.String()
	}

	// Remember the siaPaths of the blocked files
	var blockedSiaPaths []skymodules.SiaPath

	// Confirm that the skyfile and its extended info are registered with the
	// renter
	sp, err := skymodules.SkynetFolder.Join(sup.SiaPath.String())
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RenterFileRootGet(sp)
	if err != nil {
		t.Fatal(err)
	}
	spExtended, err := sp.AddSuffixStr(skymodules.ExtendedSuffix)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RenterFileRootGet(spExtended)
	if err != nil {
		t.Fatal(err)
	}
	blockedSiaPaths = append(blockedSiaPaths, sp, spExtended)

	// Download the data
	data, err := r.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Blocklist the skylink
	var add, remove []string
	hash := crypto.HashObject(sshp.MerkleRoot)
	if isHash {
		add = []string{hash.String()}
	} else {
		add = []string{skylink}
	}
	err = r.SkynetBlocklistHashPost(add, remove, isHash)
	if err != nil {
		t.Fatal(err)
	}

	// Confirm that the Skylink is blocked by verifying the hash of the V1
	// skylink is in the blocklist
	sbg, err := r.SkynetBlocklistGet()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, blocked := range sbg.Blocklist {
		if blocked == hash {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Hash not found in blocklist")
	}

	// Try to download the file behind the skylink, this should fail because of
	// the blocklist.
	_, err = r.SkynetSkylinkGet(skylink)
	if err == nil {
		t.Fatal("Download should have failed")
	}
	if !strings.Contains(err.Error(), renter.ErrSkylinkBlocked.Error()) {
		t.Fatalf("Expected error %v but got %v", renter.ErrSkylinkBlocked, err)
	}

	// Try to download the BaseSector
	_, err = r.SkynetBaseSectorGet(skylink)
	if err == nil {
		t.Fatal("BaseSector request should have failed")
	}
	if !strings.Contains(err.Error(), renter.ErrSkylinkBlocked.Error()) {
		t.Fatalf("Expected error %v but got %v", renter.ErrSkylinkBlocked, err)
	}

	// Try to download the BaseSector by Root
	_, err = r.SkynetDownloadByRootGet(sshp.MerkleRoot, 0, modules.SectorSize, -1)
	if err == nil {
		t.Fatal("DownloadByRoot request should have failed")
	}
	if !strings.Contains(err.Error(), renter.ErrSkylinkBlocked.Error()) {
		t.Fatalf("Expected error %v but got %v", renter.ErrSkylinkBlocked, err)
	}

	// Try and upload again with force as true to avoid error of path already
	// existing. Additionally need to recreate the reader again from the file
	// data. This should also fail due to the blocklist
	sup.Force = true
	sup.Reader = bytes.NewReader(data)
	_, _, err = r.SkynetSkyfilePost(sup)
	if err == nil {
		t.Fatal("Expected upload to fail")
	}
	if !strings.Contains(err.Error(), renter.ErrSkylinkBlocked.Error()) {
		t.Fatalf("Expected error %v but got %v", renter.ErrSkylinkBlocked, err)
	}

	// Verify that the SiaPath and Extended SiaPath were removed from the renter
	// due to the upload seeing the blocklist
	_, err = r.RenterFileGet(sp)
	if err == nil {
		t.Fatal("expected error for file not found")
	}
	if !strings.Contains(err.Error(), filesystem.ErrNotExist.Error()) {
		t.Fatalf("Expected error %v but got %v", filesystem.ErrNotExist, err)
	}
	_, err = r.RenterFileGet(spExtended)
	if err == nil {
		t.Fatal("expected error for file not found")
	}
	if !strings.Contains(err.Error(), filesystem.ErrNotExist.Error()) {
		t.Fatalf("Expected error %v but got %v", filesystem.ErrNotExist, err)
	}

	// Try Pinning the file, this should fail due to the blocklist
	pinlup := skymodules.SkyfilePinParameters{
		SiaPath:             sup.SiaPath,
		BaseChunkRedundancy: 2,
		Force:               true,
	}
	// Pinning is only supported for V1 Skylink
	if !isV2Skylink {
		err = r.SkynetSkylinkPinPost(skylink, pinlup)
		if err == nil {
			t.Fatal("Expected pin to fail")
		}
		if !strings.Contains(err.Error(), renter.ErrSkylinkBlocked.Error()) {
			t.Fatalf("Expected error %v but got %v", renter.ErrSkylinkBlocked, err)
		}
	}

	// Remove skylink from blocklist
	add = []string{}
	if isHash {
		remove = []string{hash.String()}
	} else {
		remove = []string{skylink}
	}
	err = r.SkynetBlocklistHashPost(add, remove, isHash)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that removing the same skylink twice is a noop
	err = r.SkynetBlocklistHashPost(add, remove, isHash)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that the skylink is removed from the Blocklist
	sbg, err = r.SkynetBlocklistGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(sbg.Blocklist) != 0 {
		t.Fatalf("Incorrect number of blocklisted merkleroots, expected %v got %v", 0, len(sbg.Blocklist))
	}

	// Try to download the file behind the skylink. Even though the file was
	// removed from the renter node that uploaded it, it should still be
	// downloadable.
	fetchedData, err := r.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fetchedData, data) {
		t.Error("upload and download doesn't match")
		t.Log(data)
		t.Log(fetchedData)
	}

	// Pinning is only supported for V1 Skylink
	if !isV2Skylink {
		// Pinning the skylink should also work now
		err = r.SkynetSkylinkPinPost(skylink, pinlup)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Upload a normal siafile with 1-of-N redundancy
	_, rf, err := r.UploadNewFileBlocking(int(size), 1, 2, false)
	if err != nil {
		t.Fatal(err)
	}

	// Convert to a skyfile
	convertUP := skymodules.SkyfileUploadParameters{
		SiaPath: rf.SiaPath(),
	}
	convertSSHP, err := r.SkynetConvertSiafileToSkyfilePost(convertUP, rf.SiaPath())
	if err != nil {
		t.Fatal(err)
	}
	convertSkylink := convertSSHP.Skylink

	// Convert to V2 Skylink
	if isV2Skylink {
		skylinkV2, err := r.NewSkylinkV2FromString(convertSkylink)
		if err != nil {
			t.Fatal(err)
		}
		convertSkylink = skylinkV2.Skylink.String()
	}

	// Confirm there is a siafile and a skyfile
	_, err = r.RenterFileGet(rf.SiaPath())
	if err != nil {
		t.Fatal(err)
	}
	skyfilePath, err := skymodules.SkynetFolder.Join(rf.SiaPath().String())
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RenterFileRootGet(skyfilePath)
	if err != nil {
		t.Fatal(err)
	}

	// For V2 Skylinks reset the blocklist to account for pinning not being supported for V2 Skylinks.
	if isV2Skylink {
		blockedSiaPaths = []skymodules.SiaPath{}
	}

	// Make sure all blockedSiaPaths are root paths
	sp, err = skymodules.UserFolder.Join(rf.SiaPath().String())
	if err != nil {
		t.Fatal(err)
	}
	blockedSiaPaths = append(blockedSiaPaths, sp, skyfilePath)

	// Blocklist the skylink
	remove = []string{}
	convertHash := crypto.HashObject(convertSSHP.MerkleRoot)
	if isHash {
		add = []string{convertHash.String()}
	} else {
		add = []string{convertSkylink}
	}
	err = r.SkynetBlocklistHashPost(add, remove, isHash)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that adding the same skylink twice is a noop
	err = r.SkynetBlocklistHashPost(add, remove, isHash)
	if err != nil {
		t.Fatal(err)
	}
	sbg, err = r.SkynetBlocklistGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(sbg.Blocklist) != 1 {
		t.Fatalf("Incorrect number of blocklisted merkleroots, expected %v got %v", 1, len(sbg.Blocklist))
	}

	// Confirm skyfile download returns blocklisted error
	//
	// NOTE: Calling DownloadSkylink doesn't attempt to delete any underlying file
	_, err = r.SkynetSkylinkGet(convertSkylink)
	if err == nil || !strings.Contains(err.Error(), renter.ErrSkylinkBlocked.Error()) {
		t.Fatalf("Expected error %v but got %v", renter.ErrSkylinkBlocked, err)
	}

	// Try and convert to skylink again, should fail. Set the Force Flag to true
	// to avoid error for file already existing
	convertUP.Force = true
	_, err = r.SkynetConvertSiafileToSkyfilePost(convertUP, rf.SiaPath())
	if err == nil || !strings.Contains(err.Error(), renter.ErrSkylinkBlocked.Error()) {
		t.Fatalf("Expected error %v but got %v", renter.ErrSkylinkBlocked, err)
	}

	// This should delete the skyfile but not the siafile
	_, err = r.RenterFileGet(rf.SiaPath())
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RenterFileRootGet(skyfilePath)
	if err == nil || !strings.Contains(err.Error(), filesystem.ErrNotExist.Error()) {
		t.Fatalf("Expected error %v but got %v", filesystem.ErrNotExist, err)
	}

	// remove from blocklist
	add = []string{}
	if isHash {
		remove = []string{convertHash.String()}
	} else {
		remove = []string{convertSkylink}
	}
	err = r.SkynetBlocklistHashPost(add, remove, isHash)
	if err != nil {
		t.Fatal(err)
	}
	sbg, err = r.SkynetBlocklistGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(sbg.Blocklist) != 0 {
		t.Fatalf("Incorrect number of blocklisted merkleroots, expected %v got %v", 0, len(sbg.Blocklist))
	}

	// Convert should succeed
	_, err = r.SkynetConvertSiafileToSkyfilePost(convertUP, rf.SiaPath())
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RenterFileRootGet(skyfilePath)
	if err != nil {
		t.Fatal(err)
	}

	// Adding links to the block list does not immediately delete the files, but
	// the health/bubble loops should eventually delete the files.
	//
	// First verify the test assumptions and confirm that the files still exist
	// in the renter.
	for _, siaPath := range blockedSiaPaths {
		_, err = r.RenterFileRootGet(siaPath)
		if err != nil {
			t.Log(siaPath)
			t.Fatal(err)
		}
	}

	// Disable the dependency
	deps.DisableDeleteBlockedFiles(false)

	// Add both skylinks back to the blocklist
	remove = []string{}
	if isHash {
		add = []string{hash.String(), convertHash.String()}
	} else {
		add = []string{skylink, convertSkylink}
	}
	err = r.SkynetBlocklistHashPost(add, remove, isHash)
	if err != nil {
		t.Fatal(err)
	}
	sbg, err = r.SkynetBlocklistGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(sbg.Blocklist) != 2 {
		t.Fatalf("Incorrect number of blocklisted merkleroots, expected %v got %v", 2, len(sbg.Blocklist))
	}

	// Wait until all the files have been deleted
	//
	// Using 15 checks at 1 second intervals because the health loop check
	// interval in testing is 5s and there are potential error sleeps of 3s.
	if err := build.Retry(15, time.Second, func() error {
		for _, siaPath := range blockedSiaPaths {
			_, err = r.RenterFileRootGet(siaPath)
			if err == nil || !strings.Contains(err.Error(), filesystem.ErrNotExist.Error()) {
				return fmt.Errorf("File %v, not deleted; error: %v", siaPath, err)
			}
		}
		return nil
	}); err != nil {
		t.Error(err)
	}

	// Reset the blocklist for other tests
	remove = add
	add = []string{}
	err = r.SkynetBlocklistHashPost(add, remove, isHash)
	if err != nil {
		t.Fatal(err)
	}
	sbg, err = r.SkynetBlocklistGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(sbg.Blocklist) != 0 {
		t.Fatalf("Incorrect number of blocklisted merkleroots, expected %v got %v", 0, len(sbg.Blocklist))
	}
}

// testSkynetBlocklistUpgrade tests the skynet blocklist module when submitting
// skylinks
func testSkynetBlocklistUpgrade(t *testing.T, tg *siatest.TestGroup) {
	// Create renterDir and renter params
	testDir := skynetTestDir(t.Name())
	renterDir := filepath.Join(testDir, "renter")
	err := os.MkdirAll(renterDir, persist.DefaultDiskPermissionsTest)
	if err != nil {
		t.Fatal(err)
	}
	params := node.Renter(testDir)

	// Load compatibility blacklist persistence
	blacklistCompatFile, err := os.Open("../../compatibility/skynetblacklistv143_siatest")
	if err != nil {
		t.Fatal(err)
	}
	blacklistPersist, err := os.Create(filepath.Join(renterDir, "skynetblacklist"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(blacklistPersist, blacklistCompatFile)
	if err != nil {
		t.Fatal(err)
	}
	err = errors.Compose(blacklistCompatFile.Close(), blacklistPersist.Close())
	if err != nil {
		t.Fatal(err)
	}

	// Grab the Skylink that is associated with the blacklist persistence
	skylinkFile, err := os.Open("../../compatibility/skylinkv143_siatest")
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(skylinkFile)
	scanner.Scan()
	skylinkStr := scanner.Text()
	var skylink skymodules.Skylink
	err = skylink.LoadString(skylinkStr)
	if err != nil {
		t.Fatal(err)
	}

	// Add the renter to the group.
	nodes, err := tg.AddNodes(params)
	if err != nil {
		t.Fatal(err)
	}
	r := nodes[0]

	// Verify there is a skylink in the now blocklist and it is the one from the
	// compatibility file
	sbg, err := r.SkynetBlocklistGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(sbg.Blocklist) != 1 {
		t.Fatal("blocklist should have 1 link, found:", len(sbg.Blocklist))
	}
	hash := crypto.HashObject(skylink.MerkleRoot())
	if sbg.Blocklist[0] != hash {
		t.Fatal("unexpected hash")
	}

	// Verify trying to download the skylink fails due to it being blocked
	//
	// NOTE: It doesn't matter if there is a file associated with this Skylink
	// since the blocklist check should cause the download to fail before any look
	// ups occur.
	_, err = r.SkynetSkylinkGet(skylinkStr)
	if !strings.Contains(err.Error(), renter.ErrSkylinkBlocked.Error()) {
		t.Fatal("unexpected error:", err)
	}
}

// testSkynetPortals tests the skynet portals module.
func testSkynetPortals(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	portal1 := skymodules.SkynetPortal{
		Address: modules.NetAddress("siasky.net:9980"),
		Public:  true,
	}
	// loopback address
	portal2 := skymodules.SkynetPortal{
		Address: "localhost:9980",
		Public:  true,
	}
	// address without a port
	portal3 := skymodules.SkynetPortal{
		Address: modules.NetAddress("siasky.net"),
		Public:  true,
	}

	// Add portal.
	add := []skymodules.SkynetPortal{portal1}
	remove := []modules.NetAddress{}
	err := r.SkynetPortalsPost(add, remove)
	if err != nil {
		t.Fatal(err)
	}

	// Confirm that the portal has been added.
	spg, err := r.SkynetPortalsGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(spg.Portals) != 1 {
		t.Fatalf("Incorrect number of portals, expected %v got %v", 1, len(spg.Portals))
	}
	if !reflect.DeepEqual(spg.Portals[0], portal1) {
		t.Fatalf("Portals don't match, expected %v got %v", portal1, spg.Portals[0])
	}

	// Remove the portal.
	add = []skymodules.SkynetPortal{}
	remove = []modules.NetAddress{portal1.Address}
	err = r.SkynetPortalsPost(add, remove)
	if err != nil {
		t.Fatal(err)
	}

	// Confirm that the portal has been removed.
	spg, err = r.SkynetPortalsGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(spg.Portals) != 0 {
		t.Fatalf("Incorrect number of portals, expected %v got %v", 0, len(spg.Portals))
	}

	// Try removing a portal that's not there.
	add = []skymodules.SkynetPortal{}
	remove = []modules.NetAddress{portal1.Address}
	err = r.SkynetPortalsPost(add, remove)
	if err == nil || !strings.Contains(err.Error(), "address "+string(portal1.Address)+" not already present in list of portals or being added") {
		t.Fatal("portal should fail to be removed")
	}

	// Try to add and remove a portal at the same time.
	add = []skymodules.SkynetPortal{portal2}
	remove = []modules.NetAddress{portal2.Address}
	err = r.SkynetPortalsPost(add, remove)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that the portal was not added.
	spg, err = r.SkynetPortalsGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(spg.Portals) != 0 {
		t.Fatalf("Incorrect number of portals, expected %v got %v", 0, len(spg.Portals))
	}

	// Test updating a portal's public status.
	portal1.Public = false
	add = []skymodules.SkynetPortal{portal1}
	remove = []modules.NetAddress{}
	err = r.SkynetPortalsPost(add, remove)
	if err != nil {
		t.Fatal(err)
	}

	spg, err = r.SkynetPortalsGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(spg.Portals) != 1 {
		t.Fatalf("Incorrect number of portals, expected %v got %v", 1, len(spg.Portals))
	}
	if !reflect.DeepEqual(spg.Portals[0], portal1) {
		t.Fatalf("Portals don't match, expected %v got %v", portal1, spg.Portals[0])
	}

	portal1.Public = true
	add = []skymodules.SkynetPortal{portal1}
	remove = []modules.NetAddress{}
	err = r.SkynetPortalsPost(add, remove)
	if err != nil {
		t.Fatal(err)
	}

	spg, err = r.SkynetPortalsGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(spg.Portals) != 1 {
		t.Fatalf("Incorrect number of portals, expected %v got %v", 1, len(spg.Portals))
	}
	if !reflect.DeepEqual(spg.Portals[0], portal1) {
		t.Fatalf("Portals don't match, expected %v got %v", portal1, spg.Portals[0])
	}

	// Test an invalid network address.
	add = []skymodules.SkynetPortal{portal3}
	remove = []modules.NetAddress{}
	err = r.SkynetPortalsPost(add, remove)
	if err == nil || !strings.Contains(err.Error(), "missing port in address") {
		t.Fatal("expected 'missing port' error")
	}

	// Test adding an existing portal with an uppercase address.
	portalUpper := portal1
	portalUpper.Address = modules.NetAddress(strings.ToUpper(string(portalUpper.Address)))
	add = []skymodules.SkynetPortal{portalUpper}
	remove = []modules.NetAddress{}
	err = r.SkynetPortalsPost(add, remove)
	// This does not currently return an error.
	if err != nil {
		t.Fatal(err)
	}

	spg, err = r.SkynetPortalsGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(spg.Portals) != 2 {
		t.Fatalf("Incorrect number of portals, expected %v got %v", 2, len(spg.Portals))
	}
}

// testSkynetIncludeLayout verifies the functionality of sending
// a 'include-layout' query string parameter to the skylink GET route.
func testSkynetIncludeLayout(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// Upload a skyfile
	skylink, _, _, err := r.UploadNewSkyfileBlocking(t.Name(), 100, false)
	if err != nil {
		t.Fatal(err)
	}

	// GET without specifying the 'include-layout' query string parameter
	_, layout, err := r.SkynetSkylinkGetWithLayout(skylink, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(layout, skymodules.SkyfileLayout{}) {
		t.Fatal("unexpected")
	}

	// GET with specifying the 'include-layout' query string parameter
	_, layout, err = r.SkynetSkylinkGetWithLayout(skylink, true)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(layout, skymodules.SkyfileLayout{}) {
		t.Fatal("unexpected")
	}

	// Perform a HEAD call to verify the same thing in the headers directly
	params := url.Values{}
	params.Set("include-layout", fmt.Sprintf("%t", true))
	status, header, err := r.SkynetSkylinkHeadWithParameters(skylink, params)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK {
		t.Fatalf("Unexpected status for HEAD request, expected %v but received %v", http.StatusOK, status)
	}

	strSkynetFileLayout := header.Get(api.SkynetFileLayoutHeader)
	if strSkynetFileLayout == "" {
		t.Fatal("unexpected")
	}
	var layout2 skymodules.SkyfileLayout
	layoutBytes, err := hex.DecodeString(strSkynetFileLayout)
	if err != nil {
		t.Fatal(err)
	}
	layout2.Decode(layoutBytes)
	if !reflect.DeepEqual(layout, layout2) {
		t.Fatal("unexpected")
	}
}

// testSkynetNoWorkers verifies that SkynetSkylinkGet returns an error and does
// not deadlock if there are no workers.
func testSkynetNoWorkers(t *testing.T, tg *siatest.TestGroup) {
	// Create renter, skip setting the allowance so that we can ensure there are
	// no contracts created and therefore no workers in the worker pool
	testDir := skynetTestDir(t.Name())
	renterParams := node.Renter(filepath.Join(testDir, "renter"))
	renterParams.SkipSetAllowance = true
	nodes, err := tg.AddNodes(renterParams)
	if err != nil {
		t.Fatal(err)
	}
	r := nodes[0]
	defer func() {
		err = tg.RemoveNode(r)
		if err != nil {
			t.Fatal(err)
		}
	}()

	// Since the renter doesn't have an allowance, we know the renter doesn't
	// have any contracts and therefore the worker pool will be empty. Confirm
	// that attempting to download a skylink will return an error and not dead
	// lock.
	skylink, err := skymodules.NewSkylinkV1(crypto.Hash{1, 2, 3}, 0, modules.SectorSize)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.SkynetSkylinkGet(skylink.String())
	if err == nil {
		t.Fatal("Error is nil, expected error due to not enough workers")
	} else if !(strings.Contains(err.Error(), skymodules.ErrNotEnoughWorkersInWorkerPool.Error()) || strings.Contains(err.Error(), "not enough workers to complete download")) {
		t.Errorf("Expected error containing '%v' but got %v", skymodules.ErrNotEnoughWorkersInWorkerPool, err)
	}
}

// testSkynetDryRunUpload verifies the --dry-run flag when uploading a Skyfile.
func testSkynetDryRunUpload(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	siaPath, err := skymodules.NewSiaPath(t.Name())
	if err != nil {
		t.Fatal(err)
	}

	// verify basic skyfile upload
	//
	// NOTE: this ensure there's workers in the pool, if we remove this the test
	// fails further down the line because there are no workers
	_, _, err = r.SkynetSkyfilePost(skymodules.SkyfileUploadParameters{
		SiaPath:             siaPath,
		BaseChunkRedundancy: 2,
		Filename:            "testSkynetDryRun",
		Mode:                0640,
		Reader:              bytes.NewReader(fastrand.Bytes(100)),
	})
	if err != nil {
		t.Fatal("Expected skynet upload to be successful, instead received err:", err)
	}

	// verify you can't perform a dry-run using the force parameter
	_, _, err = r.SkynetSkyfilePost(skymodules.SkyfileUploadParameters{
		SiaPath:             siaPath,
		BaseChunkRedundancy: 2,
		Reader:              bytes.NewReader(fastrand.Bytes(100)),
		Filename:            "testSkynetDryRun",
		Mode:                0640,
		Force:               true,
		DryRun:              true,
	})
	if err == nil {
		t.Fatal("Expected failure when both 'force' and 'dryrun' parameter are given")
	}

	verifyDryRun := func(sup skymodules.SkyfileUploadParameters, dataSize int) {
		data := fastrand.Bytes(dataSize)

		sup.DryRun = true
		sup.Reader = bytes.NewReader(data)
		skylinkDry, _, err := r.SkynetSkyfilePost(sup)
		if err != nil {
			t.Fatal(err)
		}

		// verify the skylink can't be found after a dry run
		status, _, err := r.SkynetSkylinkHead(skylinkDry)
		if status != http.StatusNotFound {
			t.Fatal(fmt.Errorf("expected 404 not found when trying to fetch a skylink retrieved from a dry run, instead received status %d and err %v", status, err))
		}

		// verify the skfyile got deleted properly
		skyfilePath, err := skymodules.SkynetFolder.Join(sup.SiaPath.String())
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.RenterFileRootGet(skyfilePath)
		if err == nil || !strings.Contains(err.Error(), "path does not exist") {
			t.Fatal(errors.New("skyfile not deleted after dry run"))
		}

		sup.DryRun = false
		sup.Reader = bytes.NewReader(data)
		skylink, _, err := r.SkynetSkyfilePost(sup)
		if err != nil {
			t.Fatal(err)
		}

		if skylinkDry != skylink {
			t.Log("Expected:", skylink)
			t.Log("Actual:  ", skylinkDry)
			t.Fatalf("VerifyDryRun failed for data size %db, skylink received during the dry-run is not identical to the skylink received when performing the actual upload.", dataSize)
		}
	}

	// verify dry-run of small file
	uploadSiaPath, err := skymodules.NewSiaPath(fmt.Sprintf("%s%s", t.Name(), "S"))
	if err != nil {
		t.Fatal(err)
	}
	verifyDryRun(skymodules.SkyfileUploadParameters{
		SiaPath:             uploadSiaPath,
		BaseChunkRedundancy: 2,
		Filename:            "testSkynetDryRunUploadSmall",
		Mode:                0640,
	}, 100)

	// verify dry-run of large file
	uploadSiaPath, err = skymodules.NewSiaPath(fmt.Sprintf("%s%s", t.Name(), "L"))
	if err != nil {
		t.Fatal(err)
	}
	verifyDryRun(skymodules.SkyfileUploadParameters{
		SiaPath:             uploadSiaPath,
		BaseChunkRedundancy: 2,
		Filename:            "testSkynetDryRunUploadLarge",
		Mode:                0640,
	}, int(modules.SectorSize*2)+siatest.Fuzz())
}

// testSkynetRequestTimeout verifies that the Skylink routes timeout when a
// timeout query string parameter has been passed.
func testSkynetRequestTimeout(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// Upload a skyfile
	skylink, _, _, err := r.UploadNewSkyfileBlocking(t.Name(), 100, false)
	if err != nil {
		t.Fatal(err)
	}

	// Verify we can pin it
	pinSiaPath, err := skymodules.NewSiaPath(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	pinLUP := skymodules.SkyfilePinParameters{
		SiaPath:             pinSiaPath,
		Force:               true,
		Root:                false,
		BaseChunkRedundancy: 2,
	}
	err = r.SkynetSkylinkPinPost(skylink, pinLUP)
	if err != nil {
		t.Fatal(err)
	}

	// Create a renter with a timeout dependency injected
	testDir := skynetTestDir(t.Name())
	renterParams := node.Renter(filepath.Join(testDir, "renter"))
	renterParams.RenterDeps = &dependencies.DependencyTimeoutProjectDownloadByRoot{}
	nodes, err := tg.AddNodes(renterParams)
	if err != nil {
		t.Fatal(err)
	}
	r = nodes[0]
	defer func() {
		if err := tg.RemoveNode(r); err != nil {
			t.Fatal(err)
		}
	}()

	// Verify timeout on head request
	status, _, err := r.SkynetSkylinkHeadWithTimeout(skylink, 1)
	if status != http.StatusNotFound {
		t.Fatalf("Expected http.StatusNotFound for random skylink but received %v", status)
	}

	// Verify timeout on download request
	_, err = r.SkynetSkylinkGetWithTimeout(skylink, 1)
	if errors.Contains(err, renter.ErrProjectTimedOut) {
		t.Fatal("Expected download request to time out")
	}
	if !strings.Contains(err.Error(), "timed out after 1s") {
		t.Log(err)
		t.Fatal("Expected error to specify the timeout")
	}

	// Verify timeout on pin request
	err = r.SkynetSkylinkPinPostWithTimeout(skylink, pinLUP, 2*time.Second)
	if errors.Contains(err, renter.ErrProjectTimedOut) {
		t.Fatal("Expected pin request to time out")
	}
	if err == nil || !strings.Contains(err.Error(), "timed out after 2s") {
		t.Log(err)
		t.Fatal("Expected error to specify the timeout")
	}
}

// testRegressionTimeoutPanic is a regression test for a double channel close
// which happened when a timeout was hit right before a download project was
// resumed.
func testRegressionTimeoutPanic(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// Upload a skyfile
	skylink, _, _, err := r.UploadNewSkyfileBlocking(t.Name(), 100, false)
	if err != nil {
		t.Fatal(err)
	}

	// Create a renter with a BlockResumeJobDownloadUntilTimeout dependency.
	testDir := skynetTestDir(t.Name())
	renterParams := node.Renter(filepath.Join(testDir, "renter"))
	renterParams.RenterDeps = dependencies.NewDependencyBlockResumeJobDownloadUntilTimeout()
	nodes, err := tg.AddNodes(renterParams)
	if err != nil {
		t.Fatal(err)
	}
	r = nodes[0]
	defer func() {
		if err := tg.RemoveNode(r); err != nil {
			t.Fatal(err)
		}
	}()

	// Verify timeout on download request doesn't panic.
	_, err = r.SkynetSkylinkGetWithTimeout(skylink, 1)
	if errors.Contains(err, renter.ErrProjectTimedOut) {
		t.Fatal("Expected download request to time out")
	}
}

// testRenameSiaPath verifies that the siapath to the skyfile can be renamed.
func testRenameSiaPath(t *testing.T, tg *siatest.TestGroup) {
	// Grab Renter
	r := tg.Renters()[0]

	// Create a skyfile
	skylink, sup, _, err := r.UploadNewSkyfileBlocking("testRenameFile", 100, false)
	if err != nil {
		t.Fatal(err)
	}
	siaPath := sup.SiaPath

	// Rename Skyfile with root set to false should fail
	err = r.RenterRenamePost(siaPath, skymodules.RandomSiaPath(), false)
	if err == nil {
		t.Error("Rename should have failed if the root flag is false")
	}
	if err != nil && !strings.Contains(err.Error(), filesystem.ErrNotExist.Error()) {
		t.Errorf("Expected error to contain %v but got %v", filesystem.ErrNotExist, err)
	}

	// Rename Skyfile with root set to true should be successful
	siaPath, err = skymodules.SkynetFolder.Join(siaPath.String())
	if err != nil {
		t.Fatal(err)
	}
	newSiaPath, err := skymodules.SkynetFolder.Join(persist.RandomSuffix())
	if err != nil {
		t.Fatal(err)
	}
	err = r.RenterRenamePost(siaPath, newSiaPath, true)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the skyfile can still be downloaded
	_, err = r.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
}

// testSkynetDefaultPath tests whether defaultPath metadata parameter works
// correctly
func testSkynetDefaultPath(t *testing.T, tg *siatest.TestGroup) {
	// Specify subtests to run
	subTests := []siatest.SubTest{
		{Name: "HasIndexNoDefaultPath", Test: testHasIndexNoDefaultPath},
		{Name: "HasIndexDisabledDefaultPath", Test: testHasIndexDisabledDefaultPath},
		{Name: "HasIndexDifferentDefaultPath", Test: testHasIndexDifferentDefaultPath},
		{Name: "HasIndexInvalidDefaultPath", Test: testHasIndexInvalidDefaultPath},
		{Name: "NoIndexDifferentDefaultPath", Test: testNoIndexDifferentDefaultPath},
		{Name: "NoIndexInvalidDefaultPath", Test: testNoIndexInvalidDefaultPath},
		{Name: "NoIndexNoDefaultPath", Test: testNoIndexNoDefaultPath},
		{Name: "NoIndexSingleFileDisabledDefaultPath", Test: testNoIndexSingleFileDisabledDefaultPath},
		{Name: "NoIndexSingleFileNoDefaultPath", Test: testNoIndexSingleFileNoDefaultPath},
	}

	// Run subtests
	for _, test := range subTests {
		t.Run(test.Name, func(t *testing.T) {
			test.Test(t, tg)
		})
	}
}

// testHasIndexNoDefaultPath Contains index.html but doesn't specify a default
// path (not disabled).
// It should return the content of index.html.
func testHasIndexNoDefaultPath(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	filename := "index.html_nil"
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(fc1)},
		{Name: "about.html", Data: []byte(fc2)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileBlocking(filename, files, "", false, false)
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	content, err := r.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, files[0].Data) {
		t.Fatalf("Expected to get content '%s', instead got '%s'", files[0].Data, string(content))
	}
}

// testHasIndexDisabledDefaultPath Contains index.html but specifies an empty
// default path (disabled).
// It should not return an error and download the file as zip
func testHasIndexDisabledDefaultPath(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	filename := "index.html_empty"
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(fc1)},
		{Name: "about.html", Data: []byte(fc2)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileBlocking(filename, files, "", true, false)
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	_, header, err := r.SkynetSkylinkHead(skylink)
	if err != nil {
		t.Fatal(err)
	}
	ct := header.Get("Content-Type")
	if ct != "application/zip" {
		t.Fatal("expected zip archive")
	}
}

// testHasIndexDifferentDefaultPath Contains index.html but specifies a
// different default, existing path.
// It should return the content of about.html.
func testHasIndexDifferentDefaultPath(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	aboutHtml := "about.html"
	filename := "index.html_about.html"
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(fc1)},
		{Name: "about.html", Data: []byte(fc2)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileBlocking(filename, files, aboutHtml, false, false)
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	content, err := r.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, files[1].Data) {
		t.Fatalf("Expected to get content '%s', instead got '%s'", files[1].Data, string(content))
	}
}

// testHasIndexInvalidDefaultPath Contains index.html but specifies a different
// INVALID default path.
// This should fail on upload with "invalid default path provided".
func testHasIndexInvalidDefaultPath(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	invalidPath := "invalid.js"
	filename := "index.html_invalid"
	files := []siatest.TestFile{
		{Name: "index.html", Data: []byte(fc1)},
		{Name: "about.html", Data: []byte(fc2)},
	}
	_, _, _, err := r.UploadNewMultipartSkyfileBlocking(filename, files, invalidPath, false, false)
	if err == nil || !strings.Contains(err.Error(), skymodules.ErrInvalidDefaultPath.Error()) {
		t.Fatalf("Expected error 'invalid default path provided', got '%+v'", err)
	}
}

// testNoIndexDifferentDefaultPath Does not contain "index.html".
// Contains about.html and specifies it as default path.
// It should return the content of about.html.
func testNoIndexDifferentDefaultPath(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	aboutHtml := "about.html"
	filename := "index.js_about.html"
	files := []siatest.TestFile{
		{Name: "index.js", Data: []byte(fc1)},
		{Name: "about.html", Data: []byte(fc2)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileBlocking(filename, files, aboutHtml, false, false)
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	content, err := r.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, files[1].Data) {
		t.Fatalf("Expected to get content '%s', instead got '%s'", files[1].Data, string(content))
	}
}

// testNoIndexInvalidDefaultPath  Does not contain index.html and specifies an
// INVALID default path.
// This should fail on upload with "invalid default path provided".
func testNoIndexInvalidDefaultPath(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	invalidPath := "invalid.js"
	files := []siatest.TestFile{
		{Name: "index.js", Data: []byte(fc1)},
		{Name: "about.html", Data: []byte(fc2)},
	}
	filename := "index.js_invalid"
	_, _, _, err := r.UploadNewMultipartSkyfileBlocking(filename, files, invalidPath, false, false)
	if err == nil || !strings.Contains(err.Error(), skymodules.ErrInvalidDefaultPath.Error()) {
		t.Fatalf("Expected error 'invalid default path provided', got '%+v'", err)
	}
}

// testNoIndexNoDefaultPath Does not contain index.html and doesn't specify
// default path (not disabled).
// It should not return an error and download the file as zip
func testNoIndexNoDefaultPath(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	fc2 := "File2Contents"
	files := []siatest.TestFile{
		{Name: "index.js", Data: []byte(fc1)},
		{Name: "about.html", Data: []byte(fc2)},
	}
	filename := "index.js_nil"
	skylink, _, _, err := r.UploadNewMultipartSkyfileBlocking(filename, files, "", false, false)
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	_, header, err := r.SkynetSkylinkHead(skylink)
	if err != nil {
		t.Fatal(err)
	}
	ct := header.Get("Content-Type")
	if ct != "application/zip" {
		t.Fatalf("expected zip archive, got '%s'\n", ct)
	}
}

// testNoIndexSingleFileDisabledDefaultPath Does not contain "index.html".
// Contains a single file and specifies an empty default path (disabled).
// It should not return an error and download the file as zip.
func testNoIndexSingleFileDisabledDefaultPath(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	filename := "index.js_empty"
	files := []siatest.TestFile{
		{Name: "index.js", Data: []byte(fc1)},
	}
	skylink, _, _, err := r.UploadNewMultipartSkyfileBlocking(filename, files, "", true, false)
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	_, header, err := r.SkynetSkylinkHead(skylink)
	if err != nil {
		t.Fatal(err)
	}
	ct := header.Get("Content-Type")
	if ct != "application/zip" {
		t.Fatal("expected zip archive")
	}
}

// testNoIndexSingleFileNoDefaultPath Does not contain "index.html".
// Contains a single file and doesn't specify a default path (not disabled).
// It should serve the only file's content.
func testNoIndexSingleFileNoDefaultPath(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]
	fc1 := "File1Contents"
	files := []siatest.TestFile{
		{Name: "index.js", Data: []byte(fc1)},
	}
	filename := "index.js"
	skylink, _, _, err := r.UploadNewMultipartSkyfileBlocking(filename, files, "", false, false)
	if err != nil {
		t.Fatal("Failed to upload multipart file.", err)
	}
	content, err := r.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, files[0].Data) {
		t.Fatalf("Expected to get content '%s', instead got '%s'", files[0].Data, string(content))
	}
}

// testSkynetRegistryReadWrite does some basic reads and writes on the registry
// to build out a buffer of stats.
func testSkynetRegistryReadWrite(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// Create some random skylinks to use later.
	skylink1, err := skymodules.NewSkylinkV1(crypto.HashBytes(fastrand.Bytes(100)), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	skylink2, err := skymodules.NewSkylinkV1(crypto.HashBytes(fastrand.Bytes(100)), 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	// Create a signed registry value.
	sk, pk := crypto.GenerateKeyPair()
	var dataKey crypto.Hash
	fastrand.Read(dataKey[:])
	data1 := skylink1.Bytes()
	data2 := skylink2.Bytes()
	srv1 := modules.NewRegistryValue(dataKey, data1, 0, modules.RegistryTypeWithoutPubkey).Sign(sk) // rev 0
	srv2 := modules.NewRegistryValue(dataKey, data2, 1, modules.RegistryTypeWithoutPubkey).Sign(sk) // rev 1
	spk := types.SiaPublicKey{
		Algorithm: types.SignatureEd25519,
		Key:       pk[:],
	}

	// Force a refresh of the worker pool for testing.
	_, err = r.RenterWorkersGet()
	if err != nil {
		t.Fatal(err)
	}

	// Update the regisry.
	err = r.RegistryUpdate(spk, dataKey, srv1.Revision, srv1.Signature, skylink1)
	if err != nil {
		t.Fatal(err)
	}

	// Read it.
	readSRV, err := r.RegistryRead(spk, dataKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(srv1, readSRV) {
		t.Log(srv1)
		t.Log(readSRV)
		t.Fatal("srvs don't match")
	}

	// Update the registry again, with a higher revision.
	err = r.RegistryUpdate(spk, dataKey, srv2.Revision, srv2.Signature, skylink2)
	if err != nil {
		t.Fatal(err)
	}

	// Read it.
	readSRV, err = r.RegistryRead(spk, dataKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(srv2, readSRV) {
		t.Log(srv2)
		t.Log(readSRV)
		t.Fatal("srvs don't match")
	}

	// Update the registry again using the same revision but higher pow.
	srvHigherPoW := srv2
	slHigherPow := skylink2
	for !srvHigherPoW.HasMoreWork(srv2.RegistryValue) {
		sl, err := skymodules.NewSkylinkV1(crypto.HashBytes(fastrand.Bytes(100)), 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		srvHigherPoW.Data = sl.Bytes()
		srvHigherPoW = srvHigherPoW.Sign(sk)
		slHigherPow = sl
	}
	err = r.RegistryUpdate(spk, dataKey, srvHigherPoW.Revision, srvHigherPoW.Signature, slHigherPow)
	if err != nil {
		t.Fatal(err)
	}
}

// testSkynetDefaultPath_TableTest tests all combinations of inputs in relation
// to default path.
func testSkynetDefaultPath_TableTest(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	fc1 := []byte("File1Contents")
	fc2 := []byte("File2Contents. This one is longer.")

	singleFile := []siatest.TestFile{
		{Name: "about.html", Data: fc1},
	}
	singleDir := []siatest.TestFile{
		{Name: "dir/about.html", Data: fc1},
	}
	multiHasIndex := []siatest.TestFile{
		{Name: "index.html", Data: fc1},
		{Name: "about.html", Data: fc2},
	}
	multiHasIndexIndexJs := []siatest.TestFile{
		{Name: "index.html", Data: fc1},
		{Name: "index.js", Data: fc1},
		{Name: "about.html", Data: fc2},
	}
	multiNoIndex := []siatest.TestFile{
		{Name: "hello.html", Data: fc1},
		{Name: "about.html", Data: fc2},
		{Name: "dir/about.html", Data: fc2},
	}

	about := "/about.html"
	bad := "/bad.html"
	index := "/index.html"
	hello := "/hello.html"
	nonHTML := "/index.js"
	dirAbout := "/dir/about.html"
	tests := []struct {
		name                   string
		files                  []siatest.TestFile
		defaultPath            string
		disableDefaultPath     bool
		expectedContent        []byte
		expectedErrStrDownload string
		expectedErrStrUpload   string
		expectedZipArchive     bool
	}{
		{
			// Single files with valid default path.
			// OK
			name:            "single_correct",
			files:           singleFile,
			defaultPath:     about,
			expectedContent: fc1,
		},
		{
			// Single files without default path.
			// OK
			name:            "single_nil",
			files:           singleFile,
			defaultPath:     "",
			expectedContent: fc1,
		},
		{
			// Single files with default, empty default path (disabled).
			// Expect a zip archive
			name:               "single_def_empty",
			files:              singleFile,
			defaultPath:        "",
			disableDefaultPath: true,
			expectedZipArchive: true,
		},
		{
			// Single files with default, bad default path.
			// Error on upload: invalid default path
			name:                 "single_def_bad",
			files:                singleFile,
			defaultPath:          bad,
			expectedContent:      nil,
			expectedErrStrUpload: "invalid default path provided",
		},

		{
			// Single dir with default path set to a nested file.
			// Error: invalid default path.
			name:                 "single_dir_nested",
			files:                singleDir,
			defaultPath:          dirAbout,
			expectedContent:      nil,
			expectedErrStrUpload: "invalid default path provided",
		},
		{
			// Single dir without default path (not disabled).
			// OK
			name:               "single_dir_nil",
			files:              singleDir,
			defaultPath:        "",
			disableDefaultPath: false,
			expectedContent:    fc1,
		},
		{
			// Single dir with empty default path (disabled).
			// Expect a zip archive
			name:               "single_dir_def_empty",
			files:              singleDir,
			defaultPath:        "",
			disableDefaultPath: true,
			expectedZipArchive: true,
		},
		{
			// Single dir with bad default path.
			// Error on upload: invalid default path
			name:                 "single_def_bad",
			files:                singleDir,
			defaultPath:          bad,
			expectedContent:      nil,
			expectedErrStrUpload: "invalid default path provided",
		},

		{
			// Multi dir with index, correct default path.
			// OK
			name:            "multi_idx_correct",
			files:           multiHasIndex,
			defaultPath:     index,
			expectedContent: fc1,
		},
		{
			// Multi dir with index, no default path (not disabled).
			// OK
			name:               "multi_idx_nil",
			files:              multiHasIndex,
			defaultPath:        "",
			disableDefaultPath: false,
			expectedContent:    fc1,
		},
		{
			// Multi dir with index, empty default path (disabled).
			// Expect a zip archive
			name:               "multi_idx_empty",
			files:              multiHasIndex,
			defaultPath:        "",
			disableDefaultPath: true,
			expectedZipArchive: true,
		},
		{
			// Multi dir with index, non-html default path.
			// OK
			name:               "multi_idx_non_html",
			files:              multiHasIndexIndexJs,
			defaultPath:        nonHTML,
			disableDefaultPath: false,
			expectedContent:    fc1,
		},
		{
			// Multi dir with index, bad default path.
			// Error on upload: invalid default path.
			name:                 "multi_idx_bad",
			files:                multiHasIndex,
			defaultPath:          bad,
			expectedContent:      nil,
			expectedErrStrUpload: "invalid default path provided",
		},

		{
			// Multi dir with no index, correct default path.
			// OK
			name:            "multi_noidx_correct",
			files:           multiNoIndex,
			defaultPath:     hello,
			expectedContent: fc1,
		},
		{
			// Multi dir with no index, no default path (not disabled).
			// Expect a zip archive
			name:               "multi_noidx_nil",
			files:              multiNoIndex,
			defaultPath:        "",
			disableDefaultPath: false,
			expectedZipArchive: true,
		},
		{
			// Multi dir with no index, empty default path (disabled).
			// Expect a zip archive
			name:               "multi_noidx_empty",
			files:              multiNoIndex,
			defaultPath:        "",
			disableDefaultPath: true,
			expectedZipArchive: true,
		},

		{
			// Multi dir with no index, bad default path.
			// Error on upload: invalid default path.
			name:                 "multi_noidx_bad",
			files:                multiNoIndex,
			defaultPath:          bad,
			expectedContent:      nil,
			expectedErrStrUpload: "invalid default path provided",
		},
		{
			// Multi dir with both defaultPath and disableDefaultPath set.
			// Error on upload.
			name:                 "multi_defpath_disabledefpath",
			files:                multiHasIndex,
			defaultPath:          index,
			disableDefaultPath:   true,
			expectedContent:      nil,
			expectedErrStrUpload: "DefaultPath and DisableDefaultPath are mutually exclusive and cannot be set together",
		},
		{
			// Multi dir with defaultPath pointing to a non-root file..
			// Error on upload.
			name:                 "multi_nonroot_defpath",
			files:                multiNoIndex,
			defaultPath:          dirAbout,
			expectedContent:      nil,
			expectedErrStrUpload: "skyfile has invalid default path which refers to a non-root file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skylink, _, _, err := r.UploadNewMultipartSkyfileBlocking(tt.name, tt.files, tt.defaultPath, tt.disableDefaultPath, false)

			// verify the returned error
			if err == nil && tt.expectedErrStrUpload != "" {
				t.Fatalf("Expected error '%s', got <nil>", tt.expectedErrStrUpload)
			}
			if err != nil && (tt.expectedErrStrUpload == "" || !strings.Contains(err.Error(), tt.expectedErrStrUpload)) {
				t.Fatalf("Expected error '%s', got '%s'", tt.expectedErrStrUpload, err.Error())
			}
			if tt.expectedErrStrUpload != "" {
				return
			}

			// verify if it returned an archive if we expected it to
			if tt.expectedZipArchive {
				_, header, err := r.SkynetSkylinkHead(skylink)
				if err != nil {
					t.Fatal(err)
				}
				if header.Get("Content-Type") != "application/zip" {
					t.Fatalf("Expected Content-Type to be 'application/zip', but received '%v'", header.Get("Content-Type"))
				}
				return
			}

			// verify the contents of the skylink
			content, err := r.SkynetSkylinkGet(skylink)
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

// testSkynetSingleFileNoSubfiles ensures that a single file uploaded as a
// skyfile will not have `subfiles` defined in its metadata. This is required by
// the `defaultPath` logic.
func testSkynetSingleFileNoSubfiles(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	skylink, _, _, err := r.UploadNewSkyfileBlocking("testSkynetSingleFileNoSubfiles", modules.SectorSize, false)
	if err != nil {
		t.Fatal("Failed to upload a single file.", err)
	}
	_, metadata, err := r.SkynetMetadataGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Subfiles != nil {
		t.Fatal("Expected empty subfiles on download, got", metadata.Subfiles)
	}
}

// BenchmarkSkynet verifies the functionality of Skynet, a decentralized CDN and
// sharing platform.
// i9 - 51.01 MB/s - dbe75c8436cea64f2664e52f9489e9ac761bc058
func BenchmarkSkynetSingleSector(b *testing.B) {
	testDir := skynetTestDir(b.Name())

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Hosts:   3,
		Miners:  1,
		Portals: 1,
	}
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			b.Fatal(err)
		}
	}()

	// Upload a file that is a single sector big.
	r := tg.Renters()[0]
	skylink, _, _, err := r.UploadNewSkyfileBlocking("foo", modules.SectorSize, false)
	if err != nil {
		b.Fatal(err)
	}

	// Sleep a bit to give the workers time to get set up.
	time.Sleep(time.Second * 5)

	// Reset the timer once the setup is done.
	b.ResetTimer()
	b.SetBytes(int64(b.N) * int64(modules.SectorSize))

	// Download the file.
	for i := 0; i < b.N; i++ {
		_, err := r.SkynetSkylinkGet(skylink)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// TestFormContractBadScore makes sure that a portal won't form a contract with
// a dead score host.
func TestFormContractBadScore(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	testDir := skynetTestDir(t.Name())

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Hosts:  2,
		Miners: 1,
	}
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Set one host to have a bad max duration.
	h := tg.Hosts()[0]
	a := siatest.DefaultAllowance
	err = h.HostModifySettingPost(client.HostParamMaxDuration, a.Period+a.RenewWindow-1)
	if err != nil {
		t.Fatal(err)
	}

	// Add a new renter.
	rt := node.RenterTemplate
	rt.SkipSetAllowance = true
	nodes, err := tg.AddNodes(rt)
	if err != nil {
		t.Fatal(err)
	}
	r := nodes[0]

	// Set the allowance.
	err = r.RenterPostAllowance(a)
	if err != nil {
		t.Fatal(err)
	}

	// Wait to give the renter some time to form contracts. Only 1 contract
	// should be formed.
	time.Sleep(time.Second * 5)
	err = siatest.CheckExpectedNumberOfContracts(r, 1, 0, 0, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Enable portal mode and wait again. We should still only see 1 contract.
	a.PaymentContractInitialFunding = a.Funds.Div64(10)
	err = r.RenterPostAllowance(a)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second * 5)
	err = siatest.CheckExpectedNumberOfContracts(r, 1, 0, 0, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
}

// TestRenewContractBadScore tests that a portal won't renew a contract with a
// host that has a dead score.
func TestRenewContractBadScore(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	testDir := skynetTestDir(t.Name())

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Hosts:  2,
		Miners: 1,
	}
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Add a new renter.
	rt := node.RenterTemplate
	rt.SkipSetAllowance = true
	nodes, err := tg.AddNodes(rt)
	if err != nil {
		t.Fatal(err)
	}
	r := nodes[0]

	// Set the allowance. The renter should act as a portal but only form a
	// regular contract with 1 host and form the other contract with the portal.
	a := siatest.DefaultAllowance
	a.PaymentContractInitialFunding = a.Funds.Div64(10)
	a.Hosts = 1
	err = r.RenterPostAllowance(a)
	if err != nil {
		t.Fatal(err)
	}

	// Should have 2 contracts now. 1 active (regular) and 1 passive (portal).
	err = build.Retry(100, 100*time.Millisecond, func() error {
		return siatest.CheckExpectedNumberOfContracts(r, 2, 0, 0, 0, 0, 0)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Set both hosts to have a bad max duration.
	hosts := tg.Hosts()
	h1, h2 := hosts[0], hosts[1]
	err = h1.HostModifySettingPost(client.HostParamMaxDuration, a.Period+a.RenewWindow-1)
	if err != nil {
		t.Fatal(err)
	}
	err = h2.HostModifySettingPost(client.HostParamMaxDuration, a.Period+a.RenewWindow-1)
	if err != nil {
		t.Fatal(err)
	}

	// Mine through a full period and renew window.
	for i := types.BlockHeight(0); i < a.Period+a.RenewWindow; i++ {
		err = tg.Miners()[0].MineBlock()
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond * 10)
	}

	// There should only be 2 expired contracts.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		return siatest.CheckExpectedNumberOfContracts(r, 0, 0, 0, 0, 2, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRegistryHealth is an integration test for the /skynet/health/entry
// endpoint.
func TestRegistryHealth(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	testDir := skynetTestDir(t.Name())

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Miners: 1,
		Hosts:  renter.MinUpdateRegistrySuccesses,
	}
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Add a renter with dependency.
	params := node.RenterTemplate
	deps := dependencies.NewDependencyDelayRegistryHealthResponses()
	deps.Disable()
	params.RenterDeps = deps
	nodes, err := tg.AddNodes(params)
	if err != nil {
		t.Fatal(err)
	}
	r := nodes[0]

	// Get one of the hosts' pubkey and choose one of the existing hosts to
	// be stopped later. They can't be the same host.
	allHosts := tg.Hosts()
	stoppedHost := allHosts[0]
	hpk, err := allHosts[1].HostPublicKey()
	if err != nil {
		t.Fatal(err)
	}
	hpkh := crypto.HashObject(hpk)

	// Create an entry that's a primary entry on one host but not the other.
	sk, pk := crypto.GenerateKeyPair()
	var dataKey crypto.Hash
	fastrand.Read(dataKey[:])
	spk := types.Ed25519PublicKey(pk)
	rid := modules.DeriveRegistryEntryID(spk, dataKey)

	// Helper function to check the health.
	assertHealth := func(expected skymodules.RegistryEntryHealth) error {
		// Randomly decide what endpoint to use.
		var reh skymodules.RegistryEntryHealth
		var err error
		choice := fastrand.Intn(2)
		if choice == 0 {
			reh, err = r.RegistryEntryHealth(spk, dataKey)
		} else {
			reh, err = r.RegistryEntryHealthRID(rid)
		}
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(reh, expected) {
			got := siatest.PrintJSON(reh)
			expected := siatest.PrintJSON(expected)
			return fmt.Errorf("health doesn't match expected (choice %v) \n got: %v \n expected: %v", choice, got, expected)
		}
		return nil
	}

	// Update the registry with an entry that's a primary entry on one host but not
	// on the others.
	revision := fastrand.Uint64n(1000)
	srv := modules.NewRegistryValue(dataKey, hpkh[:], revision, modules.RegistryTypeWithPubkey).Sign(sk)

	// Update the registry.
	err = r.RegistryUpdateWithEntry(spk, srv)
	if err != nil {
		t.Fatal(err)
	}

	// Check the health.
	err = assertHealth(skymodules.RegistryEntryHealth{
		NumBestEntries:             3,
		NumBestEntriesBeforeCutoff: 2, // cutoff is at 2 of 3 hosts
		NumEntries:                 3,
		NumBestPrimaryEntries:      1,
		RevisionNumber:             srv.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Add a new host.
	_, err = tg.AddNodeN(node.HostTemplate, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Stop the existing host.
	if err := tg.StopNode(stoppedHost); err != nil {
		t.Fatal(err)
	}

	// Update the hosts again.
	revision++
	srv.Revision = revision
	srv = srv.Sign(sk)
	err = r.RegistryUpdateWithEntry(spk, srv)
	if err != nil {
		t.Fatal(err)
	}

	// Check the health.
	err = assertHealth(skymodules.RegistryEntryHealth{
		RevisionNumber:             revision,
		NumEntries:                 uint64(len(tg.Hosts())) - 1,
		NumBestEntries:             uint64(len(tg.Hosts())) - 1,
		NumBestEntriesBeforeCutoff: 2, // cutoff is at 2 of 3 hosts
		NumBestPrimaryEntries:      1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Restart the stopped host.
	if err := tg.StartNode(stoppedHost); err != nil {
		t.Fatal(err)
	}

	// Use a retry since the node might take a while to start.
	err = build.Retry(60, time.Second, func() error {
		// Force a refresh of the worker pool for testing.
		_, err = r.RenterWorkersGet()
		if err != nil {
			t.Fatal(err)
		}
		// Check the health against the expectation.
		// We expect len(hosts)-1 best entries since one entry is
		// outdated and does therefore not count towards the health.
		return assertHealth(skymodules.RegistryEntryHealth{
			RevisionNumber:             revision,
			NumEntries:                 uint64(len(tg.Hosts())),
			NumBestEntries:             uint64(len(tg.Hosts())) - 1,
			NumBestEntriesBeforeCutoff: 2, // cutoff is at 3 of 4 hosts
			NumBestPrimaryEntries:      1,
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	// Enable the delay dependency and test again. None of the lookups
	// should return before the timeout.
	deps.Enable()
	err = assertHealth(skymodules.RegistryEntryHealth{
		RevisionNumber:             revision,
		NumEntries:                 uint64(len(tg.Hosts())),
		NumBestEntries:             uint64(len(tg.Hosts())) - 1,
		NumBestEntriesBeforeCutoff: 0,
		NumBestPrimaryEntries:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRegistryUpdateRead tests setting a registry entry and reading in through
// the API.
func TestRegistryUpdateRead(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	testDir := skynetTestDir(t.Name())

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Renters: 1,
		Miners:  1,
	}
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	r := tg.Renters()[0]

	// Add hosts with a latency dependency.
	deps := dependencies.NewDependencyHostBlockRPC()
	deps.Disable()
	host := node.HostTemplate
	host.HostDeps = deps
	_, err = tg.AddNodeN(host, renter.MinUpdateRegistrySuccesses)
	if err != nil {
		t.Fatal(err)
	}

	// Create some random skylinks to use later.
	skylink1, err := skymodules.NewSkylinkV1(crypto.HashBytes(fastrand.Bytes(100)), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	skylink2, err := skymodules.NewSkylinkV1(crypto.HashBytes(fastrand.Bytes(100)), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	skylink3, err := skymodules.NewSkylinkV1(crypto.HashBytes(fastrand.Bytes(100)), 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	// Create a signed registry value.
	sk, pk := crypto.GenerateKeyPair()
	var dataKey crypto.Hash
	fastrand.Read(dataKey[:])
	data1 := skylink1.Bytes()
	data2 := skylink2.Bytes()
	data3 := skylink3.Bytes()
	srv1 := modules.NewRegistryValue(dataKey, data1, 0, modules.RegistryTypeWithoutPubkey).Sign(sk) // rev 0
	srv2 := modules.NewRegistryValue(dataKey, data2, 1, modules.RegistryTypeWithoutPubkey).Sign(sk) // rev 1
	srv3 := modules.NewRegistryValue(dataKey, data3, 0, modules.RegistryTypeWithoutPubkey).Sign(sk) // rev 0
	spk := types.SiaPublicKey{
		Algorithm: types.SignatureEd25519,
		Key:       pk[:],
	}

	// Force a refresh of the worker pool for testing.
	_, err = r.RenterWorkersGet()
	if err != nil {
		t.Fatal(err)
	}

	// Try to read it from the host. Shouldn't work.
	_, err = r.RegistryRead(spk, dataKey)
	if err == nil || !strings.Contains(err.Error(), renter.ErrRegistryEntryNotFound.Error()) {
		t.Fatal(err)
	}

	// Update the regisry.
	err = r.RegistryUpdate(spk, dataKey, srv1.Revision, srv1.Signature, skylink1)
	if err != nil {
		t.Fatal(err)
	}

	// Read it again. This should work.
	readSRV, err := r.RegistryRead(spk, dataKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(srv1, readSRV) {
		t.Log(srv1)
		t.Log(readSRV)
		t.Fatal("srvs don't match")
	}

	// Update the registry again, with a higher revision.
	err = r.RegistryUpdate(spk, dataKey, srv2.Revision, srv2.Signature, skylink2)
	if err != nil {
		t.Fatal(err)
	}

	// Read it again. This should work.
	readSRV, err = r.RegistryRead(spk, dataKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(srv2, readSRV) {
		t.Log(srv2)
		t.Log(readSRV)
		t.Fatal("srvs don't match")
	}

	// Read it again with a almost zero timeout. This should time out.
	deps.Enable()
	start := time.Now()
	readSRV, err = r.RegistryReadWithTimeout(spk, dataKey, time.Second)
	deps.Disable()
	if err == nil || !strings.Contains(err.Error(), renter.ErrRegistryLookupTimeout.Error()) {
		t.Fatal(err)
	}

	// Make sure it didn't take too long and timed out.
	if time.Since(start) > 2*time.Second {
		t.Fatalf("read took too long to time out %v > %v", time.Since(start), 2*time.Second)
	}

	// Update the registry again, with the same revision and same PoW. Should
	// work.
	err = r.RegistryUpdate(spk, dataKey, srv2.Revision, srv2.Signature, skylink2)
	if err != nil {
		t.Fatal(err)
	}

	// Update the registry again. With the same revision but higher PoW. Should work.
	srvHigherPoW := srv2
	slHigherPow := skylink2
	for !srvHigherPoW.HasMoreWork(srv2.RegistryValue) {
		sl, err := skymodules.NewSkylinkV1(crypto.HashBytes(fastrand.Bytes(100)), 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		srvHigherPoW.Data = sl.Bytes()
		srvHigherPoW = srvHigherPoW.Sign(sk)
		slHigherPow = sl
	}
	err = r.RegistryUpdate(spk, dataKey, srvHigherPoW.Revision, srvHigherPoW.Signature, slHigherPow)
	if err != nil {
		t.Fatal(err)
	}

	// Update the registry again, with a lower revision. Shouldn't work.
	err = r.RegistryUpdate(spk, dataKey, srv3.Revision, srv3.Signature, skylink3)
	if err == nil || !strings.Contains(err.Error(), modules.ErrLowerRevNum.Error()) {
		t.Fatal(err)
	}

	// Update the registry again, with an invalid sig. Shouldn't work.
	var invalidSig crypto.Signature
	fastrand.Read(invalidSig[:])
	err = r.RegistryUpdate(spk, dataKey, srv3.Revision, invalidSig, skylink3)
	if err == nil || !strings.Contains(err.Error(), crypto.ErrInvalidSignature.Error()) {
		t.Fatal(err)
	}
}

// TestSkynetSkyfileStandardUploadRedundancy is a regression test that verifies
// the race that occurred in the overdrive code is properly fixed by ensuring
// the PDC is not accessed from more than one thread. This is a custom test
// seeing as it requires a 10-30 EC schema for the overdrive code to launch
// extra overdrive workers immediatelely after launching the initial workers.
// This requires a test group with at least 10 hosts and a portal with a custom
// dependency seeing as we don't allow (yet) to configure the fanout redundancy.
func TestSkynetSkyfileStandardUploadRedundancy(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Hosts:  10,
		Miners: 1,
	}
	testDir := skynetTestDir(t.Name())
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Add a new renter with a dependency that uploads using a 10-30 EC schema.
	rt := node.RenterTemplate
	rt.Allowance = siatest.DefaultAllowance
	rt.Allowance.Hosts = 10
	rt.Allowance.PaymentContractInitialFunding = siatest.DefaultPaymentContractInitialFunding
	rt.RenterDeps = &dependencies.DependencyStandardUploadRedundancy{}
	nodes, err := tg.AddNodes(rt)
	if err != nil {
		t.Fatal(err)
	}
	r := nodes[0]

	// Upload a large file
	ss := modules.SectorSize
	skylink, _, _, err := r.UploadNewSkyfileBlocking("largefile", ss*2, false)
	if err != nil {
		t.Fatal(err)
	}

	// Download the file, this used to trigger a race to be detected.
	_, err = r.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
}

// TestSkynetCleanupOnError verifies files are cleaned up on upload error
func TestSkynetCleanupOnError(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Hosts:  3,
		Miners: 1,
	}
	testDir := skynetTestDir(t.Name())
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Create a dependency that interrupts uploads.
	deps := dependencies.NewDependencySkyfileUploadFail()

	// Add a new renter with that dependency to interrupt skyfile uploads.
	rt := node.RenterTemplate
	rt.Allowance = siatest.DefaultAllowance
	rt.Allowance.PaymentContractInitialFunding = siatest.DefaultPaymentContractInitialFunding
	rt.RenterDeps = deps
	nodes, err := tg.AddNodes(rt)
	if err != nil {
		t.Fatal(err)
	}
	r := nodes[0]

	// Create a helper function that returns true if the upload failed
	uploadFailed := func(err error) bool {
		return err != nil && strings.Contains(err.Error(), "SkyfileUploadFail")
	}

	// Create a helper function that returns true if the siapath does not exist.
	skyfileDeleted := func(err error) bool {
		return err != nil && strings.Contains(err.Error(), filesystem.ErrNotExist.Error())
	}

	// Upload a small file
	_, small, _, err := r.UploadNewSkyfileBlocking("smallfile", 100, false)
	if !uploadFailed(err) {
		t.Fatal("unexpected error on uploading a small file", err)
	}
	smallPath, err := skymodules.SkynetFolder.Join(small.SiaPath.String())
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RenterFileRootGet(smallPath)
	if !skyfileDeleted(err) {
		t.Fatal("unexpected error on getting root for a small file", err)
	}

	// Upload a large file
	ss := modules.SectorSize
	_, large, _, err := r.UploadNewSkyfileBlocking("largefile", ss*2, false)
	if !uploadFailed(err) {
		t.Fatal("unexpected error on uploading a large file", err)
	}
	largePath, err := skymodules.SkynetFolder.Join(large.SiaPath.String())
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RenterFileRootGet(largePath)
	if !skyfileDeleted(err) {
		t.Fatal("unexpected error on getting root for a large file", err)
	}
	largePathExtended, err := largePath.AddSuffixStr(skymodules.ExtendedSuffix)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RenterFileRootGet(largePathExtended)
	if !skyfileDeleted(err) {
		t.Fatal("unexpected error on getting root for a large file extended", err)
	}

	// Disable the dependency and verify the files are not removed
	deps.Disable()

	// Re-upload the small file and re-test
	_, small, _, err = r.UploadNewSkyfileBlocking("smallfile", 100, true)
	if err != nil {
		t.Fatal("re-uploading a small file should succeed", err)
	}
	_, err = r.RenterSkyfileGet(small.SiaPath, small.Root)
	if err != nil {
		t.Fatal("unexpected error on getting root for a small file", err)
	}

	// Re-upload the large file and re-test
	_, large, _, err = r.UploadNewSkyfileBlocking("largefile", ss*2, true)
	if err != nil {
		t.Fatal("re-uploading a large file should succeed", err)
	}
	_, err = r.RenterSkyfileGet(large.SiaPath, large.Root)
	if err != nil {
		t.Fatal("unexpected error on getting root for a large file", err)
	}
	largePathExtended, err = large.SiaPath.AddSuffixStr(skymodules.ExtendedSuffix)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RenterSkyfileGet(largePathExtended, large.Root)
	if err != nil {
		t.Fatal("unexpected error on getting root for a large file extended", err)
	}
}

// TestReadUnknownRegistryEntry makes sure that reading an unknown entry takes
// the appropriate amount of time.
func TestReadUnknownRegistryEntry(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	testDir := skynetTestDir(t.Name())

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Hosts:  1,
		Miners: 1,
	}
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	rt := node.RenterTemplate
	rt.RenterDeps = &dependencies.DependencyReadRegistryBlocking{}
	nodes, err := tg.AddNodes(rt)
	if err != nil {
		t.Fatal(err)
	}
	r := nodes[0]

	// Get a random pubkey.
	var spk types.SiaPublicKey
	fastrand.Read(spk.Key)

	// Look it up.
	start := time.Now()
	_, err = r.RegistryRead(spk, crypto.Hash{})
	passed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), renter.ErrRegistryEntryNotFound.Error()) {
		t.Fatal(err)
	}

	// The time should have been less than MaxRegistryReadTimeout but greater
	// than readRegistryBackgroundTimeout.
	if passed >= renter.MaxRegistryReadTimeout || passed <= renter.ReadRegistryBackgroundTimeout {
		t.Fatalf("%v not between %v and %v", passed, renter.ReadRegistryBackgroundTimeout, renter.MaxRegistryReadTimeout)
	}

	// The remainder of the test might take a while. Only execute it in vlong
	// tests.
	if !build.VLONG {
		t.SkipNow()
	}

	// Run 200 reads to lower the p99 below the seed. Do it in batches of 10
	// reads.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		for j := 0; j < 10; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := r.RegistryRead(spk, crypto.Hash{})
				if err == nil || !strings.Contains(err.Error(), renter.ErrRegistryEntryNotFound.Error()) {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
	}

	// Verify that the estimate is lower than the timeout after multiple reads
	// with slow hosts.
	err = build.Retry(60, time.Second, func() error {
		ss, err := r.SkynetStatsGet()
		if err != nil {
			t.Fatal(err)
		}
		if ss.RegistryRead15mP99ms >= float64(renter.ReadRegistryBackgroundTimeout)/float64(time.Millisecond) {
			return fmt.Errorf("%v >= %v", ss.RegistryRead15mP99ms, renter.ReadRegistryBackgroundTimeout)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestSkynetFeePaid tests that the renter calls paySkynetFee. The various edge
// cases of paySkynetFee and the exact amounts are tested within its unit test.
func TestSkynetFeePaid(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	testDir := skynetTestDir(t.Name())

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Hosts:  2,
		Miners: 1,
	}
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	// Create an independent wallet node.
	wallet, err := siatest.NewCleanNode(node.Wallet(testDir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := wallet.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	if err != nil {
		t.Fatal(err)
	}

	// Connect it to the group.
	err = wallet.GatewayConnectPost(tg.Hosts()[0].GatewayAddress())
	if err != nil {
		t.Fatal(err)
	}

	// It should start with a 0 balance.
	balance, err := wallet.ConfirmedBalance()
	if err != nil {
		t.Fatal(err)
	}
	if !balance.IsZero() {
		t.Fatal("balance should be 0")
	}

	// Get an address.
	wag, err := wallet.WalletAddressGet()
	if err != nil {
		t.Fatal(err)
	}

	// Create a new renter that thinks the wallet's address is the fee address.
	rt := node.RenterTemplate
	deps := &dependencies.DependencyCustomSkynetAddress{}
	deps.SetAddress(wag.Address)
	rt.RenterDeps = deps
	nodes, err := tg.AddNodes(rt)
	if err != nil {
		t.Fatal(err)
	}
	r := nodes[0]

	// Upload a file.
	_, _, err = r.UploadNewFileBlocking(100, 1, 1, false)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for the next payout.
	time.Sleep(skymodules.SkynetFeePayoutInterval)

	// Mine a block to confirm the txn.
	err = tg.Miners()[0].MineBlock()
	if err != nil {
		t.Fatal(err)
	}

	// Wait for some money to show up on the address.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		balance, err := wallet.ConfirmedBalance()
		if err != nil {
			t.Fatal(err)
		}
		if balance.IsZero() {
			return errors.New("balance is zero")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestSkynetPinUnpin tests pinning and unpinning a skylink
func TestSkynetPinUnpin(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create group
	// Create a testgroup with 2 portals.
	groupParams := siatest.GroupParams{
		Hosts:  5,
		Miners: 1,
	}
	groupDir := skynetTestDir(t.Name())
	tg, err := siatest.NewGroupFromTemplate(groupDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err = tg.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	// Add portals with custom dependency
	rt := node.RenterTemplate
	rt.CreatePortal = true
	deps := dependencies.NewDependencySkipUnpinRequest()
	rt.RenterDeps = deps
	_, err = tg.AddNodeN(rt, 2)
	if err != nil {
		t.Fatal(err)
	}
	// Grab the portals
	portals := tg.Portals()
	p1 := portals[0]
	p2 := portals[1]

	// Test small Skyfile
	t.Run("SmallFile", func(t *testing.T) {
		testSkynetPinUnpin(t, p1, p2, 100, deps)
	})
	// Test Large Skyfile
	t.Run("LargeFile", func(t *testing.T) {
		testSkynetPinUnpin(t, p1, p2, 2*modules.SectorSize, deps)
	})
}

// testSkynetPinUnpin tests pinning and unpinning a skylink
func testSkynetPinUnpin(t *testing.T, p1, p2 *siatest.TestNode, fileSize uint64, deps *dependencies.DependencyWithDisableAndEnable) {
	// Define helper function for checking the number of files
	fileCheck := func(p1Expected, p2Expected uint64) error {
		return build.Retry(100, 100*time.Millisecond, func() error {
			rdg, err := p1.RenterDirRootGet(skymodules.RootSiaPath())
			if err != nil {
				return err
			}
			if rdg.Directories[0].AggregateNumFiles != p1Expected {
				return fmt.Errorf("Portal 1 should have %v files but has %v", p1Expected, rdg.Directories[0].AggregateNumFiles)
			}
			rdg, err = p2.RenterDirRootGet(skymodules.RootSiaPath())
			if err != nil {
				return err
			}
			if rdg.Directories[0].AggregateNumFiles != p2Expected {
				return fmt.Errorf("Portal 2 should have %v files but has %v", p2Expected, rdg.Directories[0].AggregateNumFiles)
			}
			return nil
		})
	}

	// Makes sure the test is starting with the dependency enabled.
	deps.Enable()

	// Verify that the portals are starting with 0 files
	err := fileCheck(0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Upload from one portal
	fileName := "pinnedFile"
	skylink, sup, _, err := p1.UploadNewSkyfileBlocking(fileName, fileSize, false)
	if err != nil {
		t.Fatal(err)
	}
	siaPath := sup.SiaPath

	// A call to unpin with a random siapath should be a no-op with the dependency enabled.
	err = p1.SkynetSkylinkUnpinCustomPost(skylink, skymodules.RandomSiaPath())
	if err != nil {
		t.Fatal(err)
	}
	p1Expected := uint64(1)
	isLargeFile := fileSize > modules.SectorSize
	if isLargeFile {
		p1Expected *= 2
	}
	err = fileCheck(p1Expected, 0)
	if err != nil {
		t.Fatal(err)
	}

	// A call to unpin with the correct siaPath should delete the file.
	fullSiaPath, err := skymodules.SkynetFolder.Join(siaPath.String())
	if err != nil {
		t.Fatal(err)
	}
	err = p1.SkynetSkylinkUnpinCustomPost(skylink, fullSiaPath)
	if err != nil {
		t.Fatal(err)
	}
	err = fileCheck(0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Disable the dependecy and re-upload the siafile
	deps.Disable()
	skylink, sup, _, err = p1.UploadNewSkyfileBlocking(fileName, fileSize, false)
	if err != nil {
		t.Fatal(err)
	}
	siaPath = sup.SiaPath
	fullSiaPath, err = skymodules.SkynetFolder.Join(siaPath.String())
	if err != nil {
		t.Fatal(err)
	}

	// Try pinning the link as v2. Shouldn't work.
	var slV1 skymodules.Skylink
	err = slV1.LoadString(skylink)
	if err != nil {
		t.Fatal(err)
	}
	slV2, err := p1.NewSkylinkV2(slV1)
	err = p1.SkynetSkylinkPinPost(slV2.String(), skymodules.SkyfilePinParameters{
		SiaPath: skymodules.RandomSiaPath(),
	})
	if err == nil || !strings.Contains(err.Error(), "can't pin version 2 skylink") {
		t.Fatal(err)
	}

	// Pin to the other portal a random number of times.
	//
	// This will test the case of the skylink being associated with multiple
	// files.
	numPins := 1 + fastrand.Intn(3)
	var wg sync.WaitGroup
	for i := 0; i < numPins; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			spp := skymodules.SkyfilePinParameters{
				SiaPath: skymodules.RandomSiaPath(),
			}
			err := p2.SkynetSkylinkPinPost(skylink, spp)
			if err != nil {
				t.Error(err)
				return
			}
		}()
	}
	wg.Wait()

	// Rename the file on Portal 1
	//
	// This will test the case of the siapath changing after upload. This also
	// pulls the siafile out of the skynet folder.
	err = p1.RenterRenamePost(fullSiaPath, skymodules.RandomSiaPath(), true)
	if err != nil {
		t.Fatal(err)
	}

	// Download from both
	//
	// NOTE: This helper also verified the bytes are equal
	err = verifyDownloadByAll(p1, p2, skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that each portal has the expected number of files.
	p1Expected = uint64(1)
	p2Expected := uint64(numPins)
	if isLargeFile {
		p1Expected *= 2
		p2Expected *= 2
	}
	err = fileCheck(p1Expected, p2Expected)
	if err != nil {
		t.Fatal(err)
	}

	// Unpin from both portals
	err = p1.SkynetSkylinkUnpinPost(skylink)
	if err != nil {
		t.Fatal(err)
	}
	err = p2.SkynetSkylinkUnpinPost(skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Unpin the v2 skylink. Shouldn't work.
	err = p1.SkynetSkylinkUnpinPost(slV2.String())
	if err == nil || !strings.Contains(err.Error(), "can't unpin version 2 skylink") {
		t.Fatal(err)
	}
	// Verify that all the siafiles have been deleted on both portals
	err = fileCheck(0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Download from all. This still works because the data is still on the hosts.
	err = verifyDownloadByAll(p1, p2, skylink)
	if err != nil {
		t.Fatal(err)
	}
}

// testSkylinkV2Download tests downloading a file by its version 2 skylink.
func testSkylinkV2Download(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// Upload some data.
	data := fastrand.Bytes(100)
	slStr, _, _, err := r.UploadNewSkyfileWithDataBlocking(t.Name(), data, false)
	if err != nil {
		t.Fatal(err)
	}

	var skylink skymodules.Skylink
	err = skylink.LoadString(slStr)
	if err != nil {
		t.Fatal(err)
	}

	// Create a v2 link.
	skylinkV2, err := r.NewSkylinkV2(skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Download the file using that link.
	downloadedDataV2, err := r.SkynetSkylinkGet(skylinkV2.String())
	if err != nil {
		t.Fatal(err)
	}

	// Download it using the v1 link.
	downloadedDataV1, err := r.SkynetSkylinkGet(slStr)
	if err != nil {
		t.Fatal(err)
	}
	// Data should match.
	if !bytes.Equal(downloadedDataV1, downloadedDataV2) {
		t.Fatal("data doesn't match")
	}
	if !bytes.Equal(downloadedDataV1, data) {
		t.Fatal("data doesn't match")
	}

	// Create a recursive v2 link of the max depth.
	recursiveLink := skylink
	var links []skymodules.Skylink
	for i := 0; i < int(renter.MaxSkylinkV2ResolvingDepth); i++ {
		slv2, err := r.NewSkylinkV2(recursiveLink)
		if err != nil {
			t.Fatal(err)
		}
		recursiveLink = slv2.Skylink
		links = append(links, slv2.Skylink)
	}

	// Download the file using that link.
	downloadedDataRecursive, err := r.SkynetSkylinkGet(recursiveLink.String())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(downloadedDataV1, downloadedDataRecursive) {
		t.Fatal("data doesn't match")
	}

	// Fetch the metadata and confirm the headers.
	mdH, _, err := r.SkynetMetadataGet(recursiveLink.String())
	if err != nil {
		t.Fatal(err)
	}
	// The skynet-skylink header should report the v1 skylink.
	if sl := mdH.Get(api.SkynetSkylinkHeader); sl != skylink.String() {
		t.Fatalf("wrong skylink %v != %v", sl, skylink.String())
	}
	// The skynet-requested-skylink should report the v2 skylink.
	if sl := mdH.Get(api.SkynetRequestedSkylinkHeader); sl != recursiveLink.String() {
		t.Fatalf("wrong skylink %v != %v", sl, recursiveLink.String())
	}

	// Fetch the header.
	_, h, err := r.SkynetSkylinkHead(recursiveLink.String())
	if err != nil {
		t.Fatal(err)
	}
	// The skynet-skylink header should report the v1 skylink.
	if sl := h.Get(api.SkynetSkylinkHeader); sl != skylink.String() {
		t.Fatalf("wrong skylink %v != %v", sl, skylink.String())
	}
	// The skynet-requested-skylink should report the v2 skylink.
	if sl := h.Get(api.SkynetRequestedSkylinkHeader); sl != recursiveLink.String() {
		t.Fatalf("wrong skylink %v != %v", sl, recursiveLink.String())
	}
	// It should contain a valid proof.
	var proof []api.RegistryHandlerGET
	err = json.Unmarshal([]byte(h.Get(api.SkynetProofHeader)), &proof)
	if err != nil {
		t.Fatal(err)
	}
	for i, rhg := range proof {
		// Parse the element into a skymodules.RegistryEntry and verify
		// it.
		data, _ := hex.DecodeString(rhg.Data)
		sigBytes, _ := hex.DecodeString(rhg.Signature)
		var sig crypto.Signature
		copy(sig[:], sigBytes)
		v := modules.NewSignedRegistryValue(rhg.DataKey, data, rhg.Revision, sig, rhg.Type)
		entry := skymodules.NewRegistryEntry(rhg.PublicKey, v)
		if err := entry.Verify(); err != nil {
			t.Fatal("verification failed", err)
		}

		// Get the current v2 link that points to the parsed entry as well as the next
		// link which is contained within its entry payload.
		currentLink := skymodules.NewSkylinkV2(entry.PubKey, entry.Tweak)
		var nextLink skymodules.Skylink
		err = nextLink.LoadBytes(data)
		if err != nil {
			t.Fatal(err)
		}
		// The current link should match the link at the opposite end of
		// the links slice.
		if currentLink.String() != links[len(links)-i-1].String() {
			t.Fatal("wrong link", currentLink, links[len(links)-i-1])
		}
		// The nextLink should match the next one. Except for the last
		// one which should match the origin v1 skylink.
		if i == len(proof)-1 && nextLink != skylink {
			t.Fatal("wrong link", nextLink, skylink)
		} else if i < len(proof)-1 && nextLink.String() != links[len(links)-i-2].String() {
			t.Fatal("wrong link", nextLink, links[len(links)-i-2])
		}
	}

	// Add another level of recursion.
	slv2, err := r.NewSkylinkV2(recursiveLink)
	if err != nil {
		t.Fatal(err)
	}
	// Download the file using that link. Should fail.
	_, err = r.SkynetSkylinkGet(slv2.String())
	if err == nil || !strings.Contains(err.Error(), renter.ErrSkylinkNesting.Error()) {
		t.Fatal("should fail to resolve v2 skylink with more than 2 layers of recursion", err)
	}

	// Resolve using resolve endpoint.
	resolvedSkylink, h, err := r.ResolveSkylinkV2Custom(skylinkV2.String(), api.DefaultSkynetRequestTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedSkylink != skylink.String() {
		t.Fatal("skylink resolved wrong", resolvedSkylink, skylink.String())
	}
	skynetSkylink := h.Get(api.SkynetSkylinkHeader)
	if skynetSkylink != resolvedSkylink {
		t.Fatalf("wrong skylink header %v != %v", skynetSkylink, resolvedSkylink)
	}
	skynetRequestedSkylink := h.Get(api.SkynetRequestedSkylinkHeader)
	if skynetRequestedSkylink != skylinkV2.String() {
		t.Fatalf("wrong requested skylink header %v != %v", skynetRequestedSkylink, skylinkV2)
	}

	// Update entry to empty skylink.
	err = r.DeleteSkylinkV2(&skylinkV2)
	if err != nil {
		t.Fatal(err)
	}

	// Download the file using v2 link again.
	_, err = r.SkynetSkylinkGet(skylinkV2.String())
	if err == nil || !strings.Contains(err.Error(), renter.ErrRootNotFound.Error()) {
		t.Fatal(err)
	}
	// Resolve using resolve endpoint.
	_, err = r.ResolveSkylinkV2(skylinkV2.String())
	if err == nil || !strings.Contains(err.Error(), renter.ErrRootNotFound.Error()) {
		t.Fatal(err)
	}
}

// TestSkynetSkylinkHealth tests the /skynet/health/skylink endpoint.
func TestSkynetSkylinkHealth(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Define the parameters.
	groupParams := siatest.GroupParams{
		Hosts:   modules.RenterDefaultDataPieces + modules.RenterDefaultParityPieces,
		Miners:  1,
		Renters: 1,
	}
	groupDir := skynetTestDir(t.Name())

	// Create a testgroup.
	tg, err := siatest.NewGroupFromTemplate(groupDir, groupParams)
	if err != nil {
		t.Fatal(errors.AddContext(err, "failed to create group"))
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	r := tg.Renters()[0]

	// helper function for asserting health
	assertHealth := func(skylink string, baseSectorRedundancy uint64, fanoutHealth float64) error {
		// Get the health.
		var sl skymodules.Skylink
		if err := sl.LoadString(skylink); err != nil {
			t.Fatal(err)
		}
		sh, err := r.SkylinkHealthGET(sl)
		if err != nil {
			return err
		}
		if sh.BaseSectorRedundancy != baseSectorRedundancy {
			return fmt.Errorf("wrong base sector redundancy %v != %v", sh.BaseSectorRedundancy, baseSectorRedundancy)
		}
		if sh.FanoutEffectiveRedundancy != fanoutHealth {
			return fmt.Errorf("fanout not healthy %v != %v", sh.FanoutEffectiveRedundancy, fanoutHealth)
		}
		dataPieces := uint8(skymodules.RenterDefaultDataPieces)
		parityPieces := uint8(skymodules.RenterDefaultParityPieces)
		if sh.FanoutDataPieces != dataPieces {
			return fmt.Errorf("invalid datapieces %v != %v", sh.FanoutDataPieces, dataPieces)
		}
		if sh.FanoutParityPieces != parityPieces {
			return fmt.Errorf("invalid paritypieces %v != %v", sh.FanoutParityPieces, parityPieces)
		}
		for i, health := range sh.FanoutRedundancy {
			if health != fanoutHealth {
				return fmt.Errorf("chunk %v not healthy %v != %v", i, health, fanoutHealth)
			}
		}
		return nil
	}

	// Upload a file with multiple chunks.
	size := modules.SectorSize * 3
	skylink, _, _, err := r.UploadNewSkyfileBlocking(t.Name(), size, false)
	if err != nil {
		t.Fatal(err)
	}

	// Assert its health. The piece should be on 5 hosts and only 1
	// datapiece is needed so the health should be 5.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		return assertHealth(skylink, siatest.DefaulTestingBaseChunkRedundancy, 5)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Upload another file but encrypted. This makes sure we test encrypted
	// skylinks as well as fanouts that can't be compressed.
	_, err = r.SkykeyCreateKeyPost("key", skykey.TypePrivateID)
	if err != nil {
		t.Fatal(err)
	}
	skylink2BaseSectorRedundancy := uint64(5)
	skylink2, _, _, err := r.UploadSkyfileBlockingCustom(t.Name()+"2", fastrand.Bytes(int(size)), "key", uint8(skylink2BaseSectorRedundancy), false)
	if err != nil {
		t.Fatal(err)
	}

	// Assert its health. Same as before.
	err = assertHealth(skylink2, skylink2BaseSectorRedundancy, 5)
	if err != nil {
		t.Fatal(err)
	}

	// Take two hosts offline.
	hosts := tg.Hosts()
	for i := 0; i < 2; i++ {
		err = tg.RemoveNode(hosts[i])
		if err != nil {
			t.Fatal(err)
		}
	}

	// Check health again.
	// The first file should either have a base sector redundancy of 0 to 2
	// depending on whether the hosts we took offline had a piece. 2 of the
	// fanout pieces are missing so the health is 3 instead of 5.
	err1 := assertHealth(skylink, siatest.DefaulTestingBaseChunkRedundancy-2, 3)
	err2 := assertHealth(skylink, siatest.DefaulTestingBaseChunkRedundancy-1, 3)
	err3 := assertHealth(skylink, siatest.DefaulTestingBaseChunkRedundancy, 3)
	if err1 != nil && err2 != nil && err3 != nil {
		t.Fatal(errors.Compose(err1, err2))
	}
	// The second file should have a base sector redundancy of 3. 2 of the
	// fanout pieces are missing so the health is 3 again.
	err = assertHealth(skylink2, skylink2BaseSectorRedundancy-2, 3)
	if err != nil {
		t.Fatal(err)
	}
}

// TestHostLosingRegistryEntry tests the edge case where a host forgets about a
// registry entry that we have fetched before.
func TestHostLosingRegistryEntry(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	testDir := skynetTestDir(t.Name())

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Miners: 1,
		Hosts:  renter.MinUpdateRegistrySuccesses,
	}
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Add renter with a special dependency.
	deps := dependencies.NewDependencyReadRegistryNoEntry()
	deps.Disable()
	params := node.RenterTemplate
	params.RenterDeps = deps
	_, err = tg.AddNodes(params)
	if err != nil {
		t.Fatal(err)
	}

	// Set an entry.
	r := tg.Renters()[0]
	sk, pk := crypto.GenerateKeyPair()
	spk := types.Ed25519PublicKey(pk)
	srv := modules.NewRegistryValue(crypto.Hash{}, []byte{}, 0, modules.RegistryTypeWithoutPubkey).Sign(sk)
	err = r.RegistryUpdateWithEntry(spk, srv)
	if err != nil {
		t.Fatal(err)
	}

	// Enable the dependency and try to read it. Should fail for all
	// workers and therefore error out.
	deps.Enable()
	_, err = r.RegistryRead(spk, srv.Tweak)
	if err == nil || !strings.Contains(err.Error(), renter.ErrRegistryEntryNotFound.Error()) {
		t.Fatal(err)
	}
}

// testUpdateRegistryMulti tests the endpoint for updating multiple host with
// different entries.
func testUpdateRegistryMulti(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// Make sure we got at least 3 hosts.
	// One to update with a primary key.
	// One to update with a secondary key.
	// Two to not update at all.
	hosts := tg.Hosts()
	if len(hosts) < 3 {
		t.Fatal("not enough hosts for test")
	}

	// Ignore the first host. We don't use it.
	hosts = hosts[1:]

	// Prepare an entry that's a primary entry on the second host.
	hpk, err := hosts[0].HostPublicKey()
	if err != nil {
		t.Fatal(err)
	}
	hpkh := crypto.HashObject(hpk)
	sk, pk := crypto.GenerateKeyPair()
	spk := types.Ed25519PublicKey(pk)
	var dataKey crypto.Hash
	fastrand.Read(dataKey[:])
	data := hpkh[:modules.RegistryPubKeyHashSize]
	srv := modules.NewRegistryValue(dataKey, data, 0, modules.RegistryTypeWithPubkey).Sign(sk)
	entry := skymodules.NewRegistryEntry(spk, srv)

	// Set the entry on all hosts from the second till the last.
	srvs := make(map[string]skymodules.RegistryEntry)
	for _, h := range hosts {
		hostKey, err := h.HostPublicKey()
		if err != nil {
			t.Fatal(err)
		}
		srvs[hostKey.String()] = entry
	}
	err = r.RegistryUpdateMulti(srvs)
	if err != nil {
		t.Fatal(err)
	}

	// Check the health. We should find the entry on every host but one and
	// one host should be considered a primary entry.
	reh, err := r.RegistryEntryHealth(spk, dataKey)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(siatest.PrintJSON(reh))
}

// testHostsForRegistryUpdate is a basic smoke test for /skynet/registry/hosts.
func testHostsForRegistryUpdate(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// Get the hosts for updating the registry.
	hg, err := r.HostsForRegistryUpdateGET()
	if err != nil {
		t.Fatal(err)
	}

	// Store them in a map for easier lookup.
	hostMap := make(map[string]struct{})
	for _, host := range hg.Hosts {
		hostMap[host.Pubkey.String()] = struct{}{}
	}

	// Compare them to the active contracts.
	rc, err := r.RenterAllContractsGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.ActiveContracts) != len(hg.Hosts) {
		t.Fatal("wrong length", len(rc.ActiveContracts), len(hg.Hosts))
	}
	for _, c := range rc.ActiveContracts {
		_, ok := hostMap[c.HostPublicKey.String()]
		if !ok {
			t.Fatal("wrong host")
		}
	}
}

// TestRegistryReadRepair tests if reading a registry entry repairs the entry on
// the network.
func TestRegistryReadRepair(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	testDir := skynetTestDir(t.Name())

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Hosts:   renter.MinUpdateRegistrySuccesses,
		Renters: 1,
		Miners:  1,
	}
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	r := tg.Renters()[0]

	// Create a random skylink.
	skylink, err := skymodules.NewSkylinkV1(crypto.HashBytes(fastrand.Bytes(100)), 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	// Create a signed registry value.
	sk, pk := crypto.GenerateKeyPair()
	var dataKey crypto.Hash
	fastrand.Read(dataKey[:])
	data := skylink.Bytes()
	srv := modules.NewRegistryValue(dataKey, data, 0, modules.RegistryTypeWithoutPubkey).Sign(sk) // rev 0
	spk := types.SiaPublicKey{
		Algorithm: types.SignatureEd25519,
		Key:       pk[:],
	}

	// Update the regisry.
	err = r.RegistryUpdate(spk, dataKey, srv.Revision, srv.Signature, skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Check the health of the entry.
	reh, err := r.RegistryEntryHealth(spk, dataKey)
	if err != nil {
		t.Fatal(err)
	}
	if reh.NumBestEntries != uint64(renter.MinUpdateRegistrySuccesses) {
		t.Fatal("wrong number of entries", reh.NumBestEntries, renter.MinUpdateRegistrySuccesses)
	}

	// Add another host. This host won't have the entry.
	_, err = tg.AddNodeN(node.HostTemplate, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Read the entry. This should work and also repair the new host.
	readSRV, err := r.RegistryRead(spk, dataKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(srv, readSRV) {
		t.Log(srv)
		t.Log(readSRV)
		t.Fatal("srvs don't match")
	}

	// Check the health of the entry again. Should find 1 more entry.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		reh, err = r.RegistryEntryHealth(spk, dataKey)
		if err != nil {
			return err
		}
		if reh.NumBestEntries != uint64(renter.MinUpdateRegistrySuccesses)+1 {
			return fmt.Errorf("wrong number of entries %v != %v", reh.NumBestEntries, renter.MinUpdateRegistrySuccesses+1)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRegistrySubscription runs registry subscription related tests.
func TestRegistrySubscription(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()
	testDir := skynetTestDir(t.Name())

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Hosts:   renter.MinUpdateRegistrySuccesses,
		Portals: 1,
		Miners:  1,
	}
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	p := tg.Portals()[0]

	// Run basic test.
	t.Run("Basic", func(t *testing.T) {
		testRegistrySubscriptionBasic(t, p)
	})
	t.Run("Delays", func(t *testing.T) {
		testRegistrySubscriptionDelays(t, p)
	})
}

// testRegistrySubscriptionBasic tests the basic case of subscribing to
// an entry and then updating it.
func testRegistrySubscriptionBasic(t *testing.T, p *siatest.TestNode) {
	// Collect notifications in a slice.
	var notifications []skymodules.RegistryEntry
	var notificationMu sync.Mutex
	notifyFunc := func(entry skymodules.RegistryEntry) {
		if err := entry.Verify(); err != nil {
			t.Fatal("failed to verify entry", err)
		}
		notificationMu.Lock()
		defer notificationMu.Unlock()
		notifications = append(notifications, entry)
	}

	// Start the subscription.
	closeHandler := func(_ int, msg string) error {
		// We are going to close the subscription gracefully so the
		// close handler shouldn't be called on our end.
		t.Fatal("Close handler called:", msg)
		return nil
	}
	subscription, err := p.BeginRegistrySubscription(notifyFunc, closeHandler)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := subscription.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Set an entry.
	sk, pk := crypto.GenerateKeyPair()
	spk := types.Ed25519PublicKey(pk)
	var tweak crypto.Hash
	fastrand.Read(tweak[:])
	srv1 := modules.NewRegistryValue(tweak, []byte{}, 0, modules.RegistryTypeWithoutPubkey).Sign(sk)
	err = p.RegistryUpdateWithEntry(spk, srv1)
	if err != nil {
		t.Fatal(err)
	}

	// Subscribe to that entry.
	err = subscription.Subscribe(spk, srv1.Tweak)
	if err != nil {
		t.Fatal(err)
	}

	// There should be 1 notification now.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		notificationMu.Lock()
		defer notificationMu.Unlock()
		if len(notifications) != 1 {
			return fmt.Errorf("notifications: %v != %v", len(notifications), 2)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Increase the revision number and set again.
	srv2 := srv1
	srv2.Revision++
	srv2 = srv2.Sign(sk)
	err = p.RegistryUpdateWithEntry(spk, srv2)
	if err != nil {
		t.Fatal(err)
	}

	// There should be 2 notifications now.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		notificationMu.Lock()
		defer notificationMu.Unlock()
		if len(notifications) != 2 {
			return fmt.Errorf("notifications: %v != %v", len(notifications), 2)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Unsubscribe
	err = subscription.Unsubscribe(spk, srv2.Tweak)
	if err != nil {
		t.Fatal(err)
	}

	// Increase the revision number and set again.
	srv3 := srv2
	srv3.Revision++
	srv3 = srv3.Sign(sk)
	err = p.RegistryUpdateWithEntry(spk, srv3)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for a bit to give the notification some time.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		// There should still be 2 notifications.
		notificationMu.Lock()
		nNotifications := len(notifications)
		notificationMu.Unlock()
		if nNotifications != 2 {
			return fmt.Errorf("notifications: %v != %v", len(notifications), 2)
		}
		return nil
	})

	// Make sure first notification matches.
	if !reflect.DeepEqual(srv1, notifications[0].SignedRegistryValue) {
		t.Log(srv1)
		t.Log(notifications[0].SignedRegistryValue)
		t.Fatal("notification mismatch")
	}
	// Make sure second notification matches.
	if !reflect.DeepEqual(srv2, *&notifications[1].SignedRegistryValue) {
		t.Log(srv2)
		t.Log(notifications[1].SignedRegistryValue)
		t.Fatal("notification mismatch")
	}
}

// testRegistrySubscriptionDelays tests that the bandwidth limit and general
// delays for notifications are working as expected.
func testRegistrySubscriptionDelays(t *testing.T, p *siatest.TestNode) {
	// Collect notifications in a slice.
	var notifications []time.Time
	var notificationMu sync.Mutex
	notifyFunc := func(entry skymodules.RegistryEntry) {
		if err := entry.Verify(); err != nil {
			t.Fatal("failed to verify entry", err)
		}
		notificationMu.Lock()
		defer notificationMu.Unlock()
		notifications = append(notifications, time.Now())
	}

	// Start the subscription.
	closeHandler := func(_ int, msg string) error {
		// We are going to close the subscription gracefully so the
		// close handler shouldn't be called on our end.
		t.Fatal("Close handler called:", msg)
		return nil
	}
	delay := 500 * time.Millisecond                                 // 0.5 seconds per notification
	limit := uint64(api.RegistrySubscriptionNotificationSize * 0.5) // 0.5 notifications per second
	subscription, err := p.BeginRegistrySubscriptionCustom(limit, delay, notifyFunc, closeHandler)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := subscription.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Set two entries.
	sk, pk := crypto.GenerateKeyPair()
	spk := types.Ed25519PublicKey(pk)
	srv1 := modules.NewRegistryValue(crypto.Hash{1}, []byte{}, 0, modules.RegistryTypeWithoutPubkey).Sign(sk)
	srv2 := modules.NewRegistryValue(crypto.Hash{2}, []byte{}, 0, modules.RegistryTypeWithoutPubkey).Sign(sk)
	err = p.RegistryUpdateWithEntry(spk, srv1)
	if err != nil {
		t.Fatal(err)
	}
	err = p.RegistryUpdateWithEntry(spk, srv2)
	if err != nil {
		t.Fatal(err)
	}

	// Subscribe to those entries and measure the time. This will trigger a
	// notification with a delay and another notification with a delay + an
	// extended delay since the notifications arrived at about the same time.
	start1 := time.Now()
	err = subscription.Subscribe(spk, srv1.Tweak)
	if err != nil {
		t.Fatal(err)
	}
	start2 := time.Now()
	err = subscription.Subscribe(spk, srv2.Tweak)
	if err != nil {
		t.Fatal(err)
	}

	// There should be 2 notifications now.
	err = build.Retry(1000, 10*time.Millisecond, func() error {
		notificationMu.Lock()
		defer notificationMu.Unlock()
		if len(notifications) != 2 {
			return fmt.Errorf("notifications: %v != %v", len(notifications), 2)
		}
		// The first notification should have taken 500 ms to arrive. To
		// account for inaccuracies we check for 500ms < d < 1s.
		d := notifications[0].Sub(start1)
		if d < delay || d >= time.Second {
			t.Fatalf("1: wrong delay: %v < %v < %v", delay, d, time.Second)
		}
		// The second notification should have taken 500ms to arrive +
		// the time between notifications specified by the bandwidth
		// limit which is 2s since we are doing 0.5 notifications per
		// second. So 2.5 seconds should have passed because we already
		// waited 500ms for the first notification and then the second
		// notification is delayed by another 500ms + 1.5s to get to
		// 2 seconds between notifications.
		d = notifications[1].Sub(start2)
		if d < time.Second*5/2 || d >= time.Second*3 {
			t.Fatalf("2: wrong delay: %v < %v < %v", time.Second*5/2, d, time.Second*3)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// testRecursiveBaseSector is a integration test for uploading a file which has
// more metadata + fanout than would fit into the base sector without the
// recursive extension.
func testRecursiveBaseSector(t *testing.T, tg *siatest.TestGroup) {
	r := tg.Renters()[0]

	// Choose a filename that is >SectorSize to guarantee we exceed the base
	// sector. Keep the content small to make it a small upload.
	data := fastrand.Bytes(10)
	largeName := hex.EncodeToString(fastrand.Bytes(int(modules.SectorSize) + 1))
	sp := skymodules.RandomSkynetFilePath()

	// Add a skykey. That way we also confirm that the encryption is
	// working.
	sk, err := r.SkykeyCreateKeyPost(t.Name(), skykey.TypePrivateID)
	if err != nil {
		t.Fatal(err)
	}

	// Add another renter.
	nodes, err := tg.AddNodes(node.RenterTemplate)
	if err != nil {
		t.Fatal(err)
	}
	r2 := nodes[0]
	defer func() {
		if err := tg.RemoveNode(r2); err != nil {
			t.Fatal(err)
		}
	}()
	if err := r2.SkykeyAddKeyPost(sk); err != nil {
		t.Fatal(err)
	}

	sup := skymodules.SkyfileUploadParameters{
		SiaPath:             sp,
		BaseChunkRedundancy: 2,
		Filename:            largeName,
		Mode:                skymodules.DefaultFilePerm,
		Reader:              bytes.NewReader(data),
		Force:               false,
		Root:                true,
		SkykeyName:          t.Name(),
	}

	// upload a skyfile
	skylink, _, err := r.SkynetSkyfilePost(sup)
	if err != nil {
		t.Fatal(err)
	}

	downloadedData, err := r.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, downloadedData) {
		t.Fatalf("data mismatch %v %v", len(data), len(downloadedData))
	}

	// Fetch the metadata.
	_, md, err := r.SkynetMetadataGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	if int(md.Length) != len(data) {
		t.Fatal("md.Length is wrong", md.Length)
	}
	if md.Filename != largeName {
		t.Fatal("name is wrong")
	}

	// Try pinning the file with the new renter.
	err = r2.SkynetSkylinkPinPost(skylink, skymodules.SkyfilePinParameters{
		SiaPath: sp,
		Root:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Both base sector .sia files should have the same size. This verifies
	// that we added the baseSectorExtension when we pinned the file.
	f1, err := r.RenterFileRootGet(sp)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := r2.RenterFileRootGet(sp)
	if err != nil {
		t.Fatal(err)
	}
	if f1.File.Size() != f2.File.Size() {
		t.Fatal("size mismatch", f1.File.Size(), f2.File.Size())
	}

	// Check the actual size. With the settings of this test, raw metadata
	// and fanout should have a length of 8392 bytes. Uploading that results
	// in 3 merkle roots which all fit into the original base sector. So the
	// resulting size is one sector plus 8392 bytes.
	extensionSize := int64(8392)
	if f1.File.Size() != int64(modules.SectorSize)+extensionSize {
		t.Fatal("wrong size", f1.File.Size())
	}
}

// TestSkynetFailedPin is a regression test that catches an edge case in the pin
// code where the Pin is successful even if the portal is unable to download the
// skylink.
func TestSkynetFailedPin(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create test group
	testDir := skynetTestDir(t.Name())
	groupParams := siatest.GroupParams{
		Hosts:   3,
		Portals: 1,
		Miners:  1,
	}
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Grab clean portals
	cleanPortal := tg.Portals()[0]

	// Add a portal with a dependency to not upload the fanout
	rt := node.RenterTemplate
	rt.CreatePortal = true
	deps := dependencies.NewDependencyDoNotUploadFanout()
	rt.RenterDeps = deps
	nodes, err := tg.AddNodes(rt)
	if err != nil {
		t.Fatal(err)
	}

	// Grab dependency portal
	depPortal := nodes[0]
	deps.Disable()

	// Upload large file to depPortal and download and pin on clean portal
	skylink, _, _, err := depPortal.UploadNewSkyfileBlocking("largeFile", 2*modules.SectorSize, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cleanPortal.SkynetSkylinkGet(skylink)
	if err != nil {
		t.Fatal(err)
	}
	spp := skymodules.SkyfilePinParameters{
		SiaPath: skymodules.RandomSiaPath(),
	}
	err = cleanPortal.SkynetSkylinkPinPost(skylink, spp)
	if err != nil {
		t.Fatal(err)
	}

	// Upload a large file that doesn't get it's fanout uploaded
	deps.Enable()
	skylink, _, _, err = depPortal.UploadNewSkyfileBlocking("badLargeFile", 2*modules.SectorSize, false)
	if err != nil {
		t.Fatal(err)
	}
	// Download should fail
	_, err = cleanPortal.SkynetSkylinkGet(skylink)
	if err == nil {
		t.Fatal("Download should fail")
	}
	// Pin should fail
	spp = skymodules.SkyfilePinParameters{
		SiaPath: skymodules.RandomSiaPath(),
	}
	err = cleanPortal.SkynetSkylinkPinPost(skylink, spp)
	if err == nil {
		t.Fatal("Pin should fail")
	}
}
