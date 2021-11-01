package client

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eventials/go-tus"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/node/api"
	"gitlab.com/SkynetLabs/skyd/skykey"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// tusStore is a simple implementation of the tus.Store interface.
type tusStore struct {
	store map[string]string
}

// newTUSStore creates a new in-memory store.
func newTUSStore() *tusStore {
	return &tusStore{
		store: make(map[string]string),
	}
}

// Get implements tus.Store.
func (ts *tusStore) Get(fingerprint string) (string, bool) {
	url, ok := ts.store[fingerprint]
	return url, ok
}

// Set implements tus.Store.
func (ts *tusStore) Set(fingerprint, url string) {
	ts.store[fingerprint] = url
}

// Delete implements tus.Store.
func (ts *tusStore) Delete(fingerprint string) {
	delete(ts.store, fingerprint)
}

// Close implements tus.Store.
func (ts *tusStore) Close() {
	ts.store = nil
}

// HostsForRegistryUpdateGET queries the /skynet/registry/hosts endpoint.
func (c *Client) HostsForRegistryUpdateGET() (hg api.HostsForRegistryUpdateGET, err error) {
	err = c.get("/skynet/registry/hosts", &hg)
	return
}

// SkynetBaseSectorGet uses the /skynet/basesector endpoint to fetch a reader of
// the basesector data.
func (c *Client) SkynetBaseSectorGet(skylink string) (io.ReadCloser, error) {
	_, reader, err := c.getReaderResponse(fmt.Sprintf("/skynet/basesector/%s", skylink))
	return reader, err
}

// SkynetDownloadByRootGet uses the /skynet/root endpoint to fetch a reader of
// a sector.
func (c *Client) SkynetDownloadByRootGet(root crypto.Hash, offset, length uint64, timeout time.Duration) (io.ReadCloser, error) {
	values := url.Values{}
	values.Set("root", root.String())
	values.Set("offset", fmt.Sprint(offset))
	values.Set("length", fmt.Sprint(length))
	if timeout >= 0 {
		values.Set("timeout", fmt.Sprintf("%s", timeout))
	}
	getQuery := fmt.Sprintf("/skynet/root?%v", values.Encode())
	_, reader, err := c.getReaderResponse(getQuery)
	return reader, err
}

// SkynetTUSClient creates a ready-to-use TUS client assuming the default upload
// params.
func (c *Client) SkynetTUSClient(chunkSize int64) (*tus.Client, error) {
	return c.SkynetTUSClientCustom(chunkSize, crypto.TypeDefaultRenter, skymodules.RenterDefaultDataPieces)
}

// SkynetTUSClientCustom creates a ready-to-use TUS client with a custom
// cipherType and dataPieces.
func (c *Client) SkynetTUSClientCustom(chunkSize int64, ct crypto.CipherType, dataPieces int) (*tus.Client, error) {
	fileChunkSize := skymodules.ChunkSize(ct, uint64(dataPieces))
	if chunkSize%int64(fileChunkSize) != 0 {
		return nil, fmt.Errorf("chunkSize needs to be a multiple of file's chunkSize %v", fileChunkSize)
	}
	// Create config for client.
	config := tus.DefaultConfig()
	config.ChunkSize = chunkSize
	config.Resume = true
	config.Store = newTUSStore()

	// Create client.
	return tus.NewClient(fmt.Sprintf("http://%v/skynet/tus", c.Address), config)
}

// SkynetTUSNewUploadFromBytesWithMaxSize returns a ready-to-use tus client and
// upload for some data and chunkSize while also specifying the maxSize of this
// download.
func (c *Client) SkynetTUSNewUploadFromBytesWithMaxSize(data []byte, chunkSize, maxSize int64) (*tus.Client, *tus.Upload, error) {
	// Get client.
	tc, err := c.SkynetTUSClient(chunkSize)
	if err != nil {
		return nil, nil, err
	}

	// Set the maxSize in the header.
	tc.Header.Set("SkynetMaxUploadSize", fmt.Sprint(maxSize))

	// Create the uploader and upload the data.
	r := bytes.NewReader(data)
	fp := crypto.HashBytes(data).String()
	upload := tus.NewUpload(r, r.Size(), tus.Metadata{}, fp)
	return tc, upload, nil
}

// SkynetTUSNewUploadFromBytes returns a ready-to-use tus client and upload for
// some data and chunkSize.
func (c *Client) SkynetTUSNewUploadFromBytes(data []byte, chunkSize int64) (*tus.Client, *tus.Upload, error) {
	return c.SkynetTUSNewUploadFromBytesWithMaxSize(data, chunkSize, int64(len(data)))
}

// SkynetTUSUploadFromBytesWithMaxSize uploads some bytes using the /skynet/tus
// endpoint and the specified chunkSize while also specifying the max size of
// the upload.
func (c *Client) SkynetTUSUploadFromBytesWithMaxSize(data []byte, chunkSize int64, fileName, fileType string, maxSize int64) (string, error) {
	tc, upload, err := c.SkynetTUSNewUploadFromBytesWithMaxSize(data, chunkSize, maxSize)
	if err != nil {
		return "", err
	}
	upload.Metadata["filename"] = fileName
	upload.Metadata["filetype"] = fileType
	uploader, err := tc.CreateUpload(upload)
	if err != nil {
		return "", err
	}
	err = uploader.Upload()
	if err != nil {
		return "", err
	}
	// After the upload, fetch the skylink from the metadata.
	skylink, err := SkylinkFromTUSURL(tc, uploader.Url())
	if err != nil {
		return "", err
	}

	// Also fetch it from the dedicated endpoint and compare them.
	url, err := url.Parse(uploader.Url())
	if err != nil {
		return "", err
	}
	uploadID := filepath.Base(url.Path)
	skylink2, err := c.SkylinkFromTUSID(uploadID)
	if err != nil {
		return "", err
	}
	if skylink != skylink2 {
		return "", err
	}
	return skylink, nil
}

// SkynetTUSUploadFromBytes uploads some bytes using the /skynet/tus endpoint
// and the specified chunkSize.
func (c *Client) SkynetTUSUploadFromBytes(data []byte, chunkSize int64, fileName, fileType string) (string, error) {
	return c.SkynetTUSUploadFromBytesWithMaxSize(data, chunkSize, fileName, fileType, int64(len(data)))
}

// SkynetSkylinkGetWithETag uses the /skynet/skylink endpoint to download a
// skylink file setting the given ETag as value in the If-None-Match request
// header.
func (uc *UnsafeClient) SkynetSkylinkGetWithETag(skylink string, eTag string) (*http.Response, error) {
	return uc.GetWithHeaders(skylinkQueryWithValues(skylink, url.Values{}), http.Header{"If-None-Match": []string{eTag}})
}

// SkynetSkyfilePostRawResponse uses the /skynet/skyfile endpoint to upload a
// skyfile.  This function is unsafe as it returns the raw response alongside
// the http headers.
func (uc *UnsafeClient) SkynetSkyfilePostRawResponse(sup skymodules.SkyfileUploadParameters) (http.Header, []byte, error) {
	encodedValues, err := urlEncodeSkyfileUploadParameters(sup)
	if err != nil {
		return http.Header{}, nil, err
	}
	query := fmt.Sprintf("/skynet/skyfile/%s?%s", sup.SiaPath.String(), encodedValues)
	return uc.postRawResponse(query, sup.Reader)
}

// SkynetSkylinkPinPostRawResponse uses the /skynet/pin endpoint to pin the file
// at the given skylink. This function is unsafe as it returns the raw response
// alongside the http headers.
func (uc *UnsafeClient) SkynetSkylinkPinPostRawResponse(skylink string, spp skymodules.SkyfilePinParameters) (http.Header, []byte, error) {
	values := urlValuesFromSkyfilePinParameters(spp)
	query := fmt.Sprintf("/skynet/pin/%s?%s", skylink, values.Encode())
	return uc.postRawResponse(query, nil)
}

// RenterSkyfileGet wraps RenterFileRootGet to query a skyfile.
func (c *Client) RenterSkyfileGet(siaPath skymodules.SiaPath, root bool) (rf api.RenterFile, err error) {
	if !root {
		siaPath, err = skymodules.SkynetFolder.Join(siaPath.String())
		if err != nil {
			return
		}
	}
	return c.RenterFileRootGet(siaPath)
}

// SkynetSkylinkGet uses the /skynet/skylink endpoint to download a skylink
// file.
func (c *Client) SkynetSkylinkGet(skylink string) ([]byte, error) {
	return c.SkynetSkylinkGetWithTimeout(skylink, -1)
}

// SkynetMetadataGet uses the /skynet/metadata endpoint to fetch a skylink's
// metadata.
func (c *Client) SkynetMetadataGet(skylink string) (_ http.Header, sm skymodules.SkyfileMetadata, _ error) {
	header, body, err := c.getRawResponse(fmt.Sprintf("/skynet/metadata/%s", skylink))
	if err != nil {
		return nil, skymodules.SkyfileMetadata{}, err
	}
	err = json.Unmarshal(body, &sm)
	return header, sm, err
}

// SkynetSkylinkRange uses the /skynet/skylink endpoint to download a range from
// a skylink file.
func (c *Client) SkynetSkylinkRange(skylink string, from, to uint64) ([]byte, error) {
	getQuery := skylinkQueryWithValues(skylink, url.Values{})
	return c.getRawPartialResponse(getQuery, from, to)
}

// SkynetSkylinkRangeParams uses the /skynet/skylink endpoint to download a
// range from a skylink file using the range params instead of the header.
func (c *Client) SkynetSkylinkRangeParams(skylink string, start, end uint64) ([]byte, error) {
	values := url.Values{}
	values.Set("start", strconv.FormatUint(start, 10))
	values.Set("end", strconv.FormatUint(end, 10))
	getQuery := skylinkQueryWithValues(skylink, values)
	_, fileData, err := c.getRawResponse(getQuery)
	return fileData, err
}

// SkynetSkylinkGetWithTimeout uses the /skynet/skylink endpoint to download a
// skylink file, specifying the given timeout.
func (c *Client) SkynetSkylinkGetWithTimeout(skylink string, timeout int) ([]byte, error) {
	params := make(map[string]string)
	// Only set the timeout if it's valid. Seeing as 0 is a valid timeout,
	// callers need to pass -1 to ignore it.
	if timeout >= 0 {
		params["timeout"] = fmt.Sprintf("%d", timeout)
	}
	return c.skynetSkylinkGetWithParameters(skylink, params)
}

// SkynetSkylinkGetWithLayout uses the /skynet/skylink endpoint to download
// a skylink file, specifying the given value for the 'include-layout'
// parameter.
func (c *Client) SkynetSkylinkGetWithLayout(skylink string, incLayout bool) ([]byte, skymodules.SkyfileLayout, error) {
	// Submit get request
	header, fileData, err := c.skynetSkylinkGetWithParametersRaw(skylink, map[string]string{
		"include-layout": fmt.Sprintf("%t", incLayout),
	})
	if err != nil {
		return nil, skymodules.SkyfileLayout{}, errors.AddContext(err, "unable to download skylink with parameters")
	}
	// Parse the Layout from the header
	var layout skymodules.SkyfileLayout
	layoutStr := header.Get(api.SkynetFileLayoutHeader)
	if layoutStr != "" {
		layoutBytes, err := hex.DecodeString(layoutStr)
		if err != nil {
			return nil, skymodules.SkyfileLayout{}, errors.AddContext(err, "unable to decode layout")
		}
		layout.Decode(layoutBytes)
	}
	return fileData, layout, nil
}

// skynetSkylinkGetWithParameters uses the /skynet/skylink endpoint to download
// a skylink file, specifying the given parameters.
// The caller of this function is responsible for validating the parameters!
func (c *Client) skynetSkylinkGetWithParameters(skylink string, params map[string]string) ([]byte, error) {
	_, fileData, err := c.skynetSkylinkGetWithParametersRaw(skylink, params)
	if err != nil {
		return nil, errors.AddContext(err, "unable to fetch skylink data")
	}
	return fileData, nil
}

// skynetSkylinkGetWithParametersRaw uses the /skynet/skylink endpoint to
// download a skylink file, specifying the given parameters.
// The caller of this function is responsible for validating the parameters!
func (c *Client) skynetSkylinkGetWithParametersRaw(skylink string, params map[string]string) (http.Header, []byte, error) {
	values := url.Values{}
	for k, v := range params {
		values.Set(k, v)
	}

	getQuery := skylinkQueryWithValues(skylink, values)
	header, fileData, err := c.getRawResponse(getQuery)
	return header, fileData, errors.AddContext(err, "skynetSkylinkGetWithParametersRaw with parameters failed getRawResponse")
}

// SkynetSkylinkHead uses the /skynet/skylink endpoint to get the headers that
// are returned if the skyfile were to be requested using the SkynetSkylinkGet
// method.
func (c *Client) SkynetSkylinkHead(skylink string) (int, http.Header, error) {
	return c.SkynetSkylinkHeadWithParameters(skylink, url.Values{})
}

// SkynetSkylinkHeadWithTimeout uses the /skynet/skylink endpoint to get the
// headers that are returned if the skyfile were to be requested using the
// SkynetSkylinkGet method. It allows to pass a timeout parameter for the
// request.
func (c *Client) SkynetSkylinkHeadWithTimeout(skylink string, timeout int) (int, http.Header, error) {
	values := url.Values{}
	values.Set("timeout", fmt.Sprintf("%d", timeout))
	return c.SkynetSkylinkHeadWithParameters(skylink, values)
}

// SkynetSkylinkHeadWithAttachment uses the /skynet/skylink endpoint to get the
// headers that are returned if the skyfile were to be requested using the
// SkynetSkylinkGet method. It allows to pass the 'attachment' parameter.
func (c *Client) SkynetSkylinkHeadWithAttachment(skylink string, attachment bool) (int, http.Header, error) {
	values := url.Values{}
	values.Set("attachment", fmt.Sprintf("%t", attachment))
	return c.SkynetSkylinkHeadWithParameters(skylink, values)
}

// SkynetSkylinkHeadWithFormat uses the /skynet/skylink endpoint to get the
// headers that are returned if the skyfile were to be requested using the
// SkynetSkylinkGet method. It allows to pass the 'format' parameter.
func (c *Client) SkynetSkylinkHeadWithFormat(skylink string, format skymodules.SkyfileFormat) (int, http.Header, error) {
	values := url.Values{}
	values.Set("format", string(format))
	return c.SkynetSkylinkHeadWithParameters(skylink, values)
}

// SkynetSkylinkHeadWithParameters uses the /skynet/skylink endpoint to get the
// headers that are returned if the skyfile were to be requested using the
// SkynetSkylinkGet method. The values are encoded in the querystring.
func (c *Client) SkynetSkylinkHeadWithParameters(skylink string, values url.Values) (int, http.Header, error) {
	getQuery := skylinkQueryWithValues(skylink, values)
	return c.head(getQuery)
}

// SkynetSkylinkConcatGet uses the /skynet/skylink endpoint to download a
// skylink file with the 'concat' format specified.
func (c *Client) SkynetSkylinkConcatGet(skylink string) ([]byte, error) {
	values := url.Values{}
	values.Set("format", string(skymodules.SkyfileFormatConcat))
	getQuery := skylinkQueryWithValues(skylink, values)
	var reader io.Reader
	_, body, err := c.getReaderResponse(getQuery)
	if err != nil {
		return nil, errors.AddContext(err, "error fetching api response for GET with format=concat")
	}
	defer func() {
		err = errors.Compose(err, body.Close())
	}()
	reader = body

	// Read the fileData.
	fileData, err := ioutil.ReadAll(reader)
	if err != nil {
		return nil, errors.AddContext(err, "unable to reader data from reader")
	}
	return fileData, errors.AddContext(err, "unable to fetch skylink data")
}

// SkynetSkylinkBackup uses the /skynet/skylink endpoint to fetch the Skyfile's
// basesector, and reader for large Skyfiles, and writes it to the backupDst
// writer.
func (c *Client) SkynetSkylinkBackup(skylinkStr string, backupDst io.Writer) error {
	// Check the skylink
	var skylink skymodules.Skylink
	err := skylink.LoadString(skylinkStr)
	if err != nil {
		return errors.AddContext(err, "unable to load skylink")
	}
	if !skylink.IsSkylinkV1() {
		return errors.New("Skylink backup code only supports V1 skylinks")
	}

	// Download the BaseSector first
	baseSectorReader, err := c.SkynetBaseSectorGet(skylinkStr)
	if err != nil {
		return errors.AddContext(err, "unable to download baseSector")
	}
	baseSector, err := ioutil.ReadAll(baseSectorReader)
	if err != nil {
		return errors.AddContext(err, "unable to read baseSector reader")
	}

	// If the baseSector isn't encrypted, parse out the layout and see if we can
	// return early.
	if !skymodules.IsEncryptedBaseSector(baseSector) {
		// Parse the layout from the baseSector
		sl := skymodules.ParseSkyfileLayout(baseSector)
		if err != nil {
			return errors.AddContext(err, "unable to parse baseSector")
		}
		// If there is no fanout then we just need to backup the baseSector
		if sl.FanoutSize == 0 {
			return skymodules.BackupSkylink(skylinkStr, baseSector, nil, backupDst)
		}
	}

	// To avoid having the caller provide the Skykey, we treat encrypted skyfiles the
	// same as skyfiles that have a fanout. Which means calling /skynet/skylink
	// for the remaining information needed for the backup

	// Fetch the header and reader for the Skylink
	getQuery := fmt.Sprintf("/skynet/skylink/%s", skylinkStr)
	_, reader, err := c.getReaderResponse(getQuery)
	if err != nil {
		return errors.AddContext(err, "unable to fetch skylink data")
	}
	defer drainAndClose(reader)

	// Read the SkyfileMetadata
	_, sm, err := c.SkynetMetadataGet(skylinkStr)
	if err != nil {
		return errors.AddContext(err, "unable to fetch metadata")
	}

	// If there are no subFiles then we have all the information we need to
	// back up the file.
	if len(sm.Subfiles) == 0 {
		return skymodules.BackupSkylink(skylinkStr, baseSector, reader, backupDst)
	}

	// Sort the subFiles by offset
	subFiles := make([]skymodules.SkyfileSubfileMetadata, 0, len(sm.Subfiles))
	for _, sfm := range sm.Subfiles {
		subFiles = append(subFiles, sfm)
	}
	sort.Slice(subFiles, func(i, j int) bool {
		return subFiles[i].Offset < subFiles[j].Offset
	})

	// Build a backup reader by downloading all the subFile data
	var backupReader io.Reader
	var backupReaderMu sync.Mutex
	var wg sync.WaitGroup
	errChan := make(chan error)
	for _, sf := range subFiles {
		wg.Add(1)
		go func(fileName string) {
			defer wg.Done()
			// Download the data from the subFile
			subgetQuery := fmt.Sprintf("%s/%s", getQuery, fileName)
			_, subreader, err := c.getReaderResponse(subgetQuery)
			if err != nil {
				// Use a select to not block on errors from other go routines.
				select {
				case errChan <- errors.AddContext(err, "unable to download subFile"):
				default:
				}
				return
			}
			backupReaderMu.Lock()
			defer backupReaderMu.Unlock()
			if backupReader == nil {
				backupReader = io.MultiReader(subreader)
				return
			}
			backupReader = io.MultiReader(backupReader, subreader)
		}(sf.Filename)
	}

	// Wait for all calls to return and check for an error
	wg.Wait()
	if err := modules.PeekErr(errChan); err != nil {
		return err
	}

	// Create the backup file on disk
	return skymodules.BackupSkylink(skylinkStr, baseSector, backupReader, backupDst)
}

// SkynetSkylinkRestorePost uses the /skynet/restore endpoint to restore
// a skylink from the backup.
func (c *Client) SkynetSkylinkRestorePost(backup io.Reader) (string, error) {
	// Submit the request
	_, resp, err := c.postRawResponse("/skynet/restore", backup)
	if err != nil {
		return "", errors.AddContext(err, "post call to /skynet/restore failed")
	}
	var srp api.SkynetRestorePOST
	err = json.Unmarshal(resp, &srp)
	if err != nil {
		return "", errors.AddContext(err, "unable to unmarshal response")
	}
	return srp.Skylink, nil
}

// SkynetSkylinkReaderGet uses the /skynet/skylink endpoint to fetch a reader of
// the file data.
func (c *Client) SkynetSkylinkReaderGet(skylink string) (io.ReadCloser, error) {
	getQuery := fmt.Sprintf("/skynet/skylink/%s", skylink)
	_, reader, err := c.getReaderResponse(getQuery)
	return reader, errors.AddContext(err, "unable to fetch skylink data")
}

// SkynetSkylinkConcatReaderGet uses the /skynet/skylink endpoint to fetch a
// reader of the file data with the 'concat' format specified.
func (c *Client) SkynetSkylinkConcatReaderGet(skylink string) (io.ReadCloser, error) {
	_, reader, err := c.SkynetSkylinkFormatGet(skylink, skymodules.SkyfileFormatConcat)
	return reader, err
}

// SkynetSkylinkFormatGet uses the /skynet/skylink endpoint to fetch a reader of
// the file data with the format specified.
func (c *Client) SkynetSkylinkFormatGet(skylink string, format skymodules.SkyfileFormat) (http.Header, io.ReadCloser, error) {
	values := url.Values{}
	values.Set("format", string(format))
	getQuery := skylinkQueryWithValues(skylink, values)
	header, reader, err := c.getReaderResponse(getQuery)
	return header, reader, errors.AddContext(err, "unable to fetch skylink data")
}

// SkynetSkylinkTarReaderGet uses the /skynet/skylink endpoint to fetch a
// reader of the file data with the 'tar' format specified.
func (c *Client) SkynetSkylinkTarReaderGet(skylink string) (http.Header, io.ReadCloser, error) {
	return c.SkynetSkylinkFormatGet(skylink, skymodules.SkyfileFormatTar)
}

// SkynetSkylinkTarGzReaderGet uses the /skynet/skylink endpoint to fetch a
// reader of the file data with the 'targz' format specified.
func (c *Client) SkynetSkylinkTarGzReaderGet(skylink string) (http.Header, io.ReadCloser, error) {
	return c.SkynetSkylinkFormatGet(skylink, skymodules.SkyfileFormatTarGz)
}

// SkynetSkylinkZipReaderGet uses the /skynet/skylink endpoint to fetch a
// reader of the file data with the 'zip' format specified.
func (c *Client) SkynetSkylinkZipReaderGet(skylink string) (http.Header, io.ReadCloser, error) {
	return c.SkynetSkylinkFormatGet(skylink, skymodules.SkyfileFormatZip)
}

// SkynetSkylinkPinPost uses the /skynet/pin endpoint to pin the file at the
// given skylink.
func (c *Client) SkynetSkylinkPinPost(skylink string, spp skymodules.SkyfilePinParameters) error {
	return c.SkynetSkylinkPinPostWithTimeout(skylink, spp, api.DefaultSkynetRequestTimeout)
}

// SkynetSkylinkPinPostWithTimeout uses the /skynet/pin endpoint to pin the file
// at the given skylink, specifying the given timeout.
func (c *Client) SkynetSkylinkPinPostWithTimeout(skylink string, spp skymodules.SkyfilePinParameters, timeout time.Duration) error {
	values := urlValuesFromSkyfilePinParameters(spp)
	values.Set("timeout", fmt.Sprintf("%d", uint64(timeout.Seconds())))

	query := fmt.Sprintf("/skynet/pin/%s?%s", skylink, values.Encode())
	_, _, err := c.postRawResponse(query, nil)
	if err != nil {
		return errors.AddContext(err, "post call to "+query+" failed")
	}
	return nil
}

// SkynetSkyfilePost uses the /skynet/skyfile endpoint to upload a skyfile.  The
// resulting skylink is returned along with an error.
func (c *Client) SkynetSkyfilePost(sup skymodules.SkyfileUploadParameters) (string, api.SkynetSkyfileHandlerPOST, error) {
	// Make the call to upload the file.
	encodedValues, err := urlEncodeSkyfileUploadParameters(sup)
	if err != nil {
		return "", api.SkynetSkyfileHandlerPOST{}, errors.AddContext(err, "failed to encode url values")
	}
	query := fmt.Sprintf("/skynet/skyfile/%s?%s", sup.SiaPath.String(), encodedValues)
	_, resp, err := c.postRawResponse(query, sup.Reader)
	if err != nil {
		return "", api.SkynetSkyfileHandlerPOST{}, errors.AddContext(err, "post call to "+query+" failed")
	}

	// Parse the response to get the skylink.
	var rshp api.SkynetSkyfileHandlerPOST
	err = json.Unmarshal(resp, &rshp)
	if err != nil {
		return "", api.SkynetSkyfileHandlerPOST{}, errors.AddContext(err, "unable to parse the skylink upload response")
	}
	return rshp.Skylink, rshp, err
}

// SkynetSkyfilePostDisableForce uses the /skynet/skyfile endpoint to upload a
// skyfile. This method allows to set the Disable-Force header. The resulting
// skylink is returned along with an error.
func (c *Client) SkynetSkyfilePostDisableForce(sup skymodules.SkyfileUploadParameters, disableForce bool) (string, api.SkynetSkyfileHandlerPOST, error) {
	// Set the headers
	headers := http.Header{"Content-Type": []string{"application/x-www-form-urlencoded"}}
	if disableForce {
		headers.Add(api.SkynetDisableForceHeader, strconv.FormatBool(disableForce))
	}

	// Make the call to upload the file.
	encodedValues, err := urlEncodeSkyfileUploadParameters(sup)
	if err != nil {
		return "", api.SkynetSkyfileHandlerPOST{}, errors.AddContext(err, "failed to encode url values")
	}
	query := fmt.Sprintf("/skynet/skyfile/%s?%s", sup.SiaPath.String(), encodedValues)
	_, resp, err := c.postRawResponseWithHeaders(query, sup.Reader, headers)
	if err != nil {
		return "", api.SkynetSkyfileHandlerPOST{}, errors.AddContext(err, "post call to "+query+" failed")
	}

	// Parse the response to get the skylink.
	var rshp api.SkynetSkyfileHandlerPOST
	err = json.Unmarshal(resp, &rshp)
	if err != nil {
		return "", api.SkynetSkyfileHandlerPOST{}, errors.AddContext(err, "unable to parse the skylink upload response")
	}
	return rshp.Skylink, rshp, err
}

// SkynetSkyfileMultiPartPost uses the /skynet/skyfile endpoint to upload a
// skyfile using multipart form data.  The resulting skylink is returned along
// with an error.
func (c *Client) SkynetSkyfileMultiPartPost(smup skymodules.SkyfileMultipartUploadParameters) (string, api.SkynetSkyfileHandlerPOST, error) {
	return c.SkynetSkyfileMultiPartEncryptedPost(smup, "", skykey.SkykeyID{})
}

// SkynetSkyfileMultiPartEncryptedPost uses the /skynet/skyfile endpoint to
// upload a skyfile using multipart form data with Skykey params.  The resulting
// skylink is returned along with an error.
func (c *Client) SkynetSkyfileMultiPartEncryptedPost(smup skymodules.SkyfileMultipartUploadParameters, skykeyName string, skykeyID skykey.SkykeyID) (string, api.SkynetSkyfileHandlerPOST, error) {
	// Make the call to upload the file.
	values, err := urlValuesFromSkyfileMultipartUploadParameters(smup)
	if err != nil {
		return "", api.SkynetSkyfileHandlerPOST{}, errors.AddContext(err, "failed to get url values")
	}
	query := fmt.Sprintf("/skynet/skyfile/%s?%s", smup.SiaPath.String(), values.Encode())

	headers := http.Header{"Content-Type": []string{smup.ContentType}}
	_, resp, err := c.postRawResponseWithHeaders(query, smup.Reader, headers)
	if err != nil {
		return "", api.SkynetSkyfileHandlerPOST{}, errors.AddContext(err, "post call to "+query+" failed")
	}

	// Parse the response to get the skylink.
	var rshp api.SkynetSkyfileHandlerPOST
	err = json.Unmarshal(resp, &rshp)
	if err != nil {
		return "", api.SkynetSkyfileHandlerPOST{}, errors.AddContext(err, "unable to parse the skylink upload response")
	}
	return rshp.Skylink, rshp, err
}

// SkynetConvertSiafileToSkyfilePost uses the /skynet/skyfile endpoint to
// convert an existing siafile to a skyfile. The input SiaPath 'convert' is the
// siapath of the siafile that should be converted. The siapath provided inside
// of the upload params is the name that will be used for the base sector of the
// skyfile.
func (c *Client) SkynetConvertSiafileToSkyfilePost(sup skymodules.SkyfileUploadParameters, convert skymodules.SiaPath) (api.SkynetSkyfileHandlerPOST, error) {
	values, err := urlValuesFromSkyfileUploadParameters(sup)
	if err != nil {
		return api.SkynetSkyfileHandlerPOST{}, errors.AddContext(err, "unable to get url values")
	}
	values.Add("convertpath", convert.String())

	// Make the call to upload the file.
	query := fmt.Sprintf("/skynet/skyfile/%s?%s", sup.SiaPath.String(), values.Encode())
	_, resp, err := c.postRawResponse(query, sup.Reader)
	if err != nil {
		return api.SkynetSkyfileHandlerPOST{}, errors.AddContext(err, "post call to "+query+" failed")
	}

	// Parse the response to get the skylink.
	var rshp api.SkynetSkyfileHandlerPOST
	err = json.Unmarshal(resp, &rshp)
	if err != nil {
		return api.SkynetSkyfileHandlerPOST{}, errors.AddContext(err, "unable to parse the skylink upload response")
	}
	return rshp, nil
}

// SkynetBlocklistGet requests the /skynet/blocklist Get endpoint
func (c *Client) SkynetBlocklistGet() (blocklist api.SkynetBlocklistGET, err error) {
	err = c.get("/skynet/blocklist", &blocklist)
	return
}

// SkynetBlocklistHashPost requests the /skynet/blocklist Post endpoint
func (c *Client) SkynetBlocklistHashPost(additions, removals []string, isHash bool) (err error) {
	sbp := api.SkynetBlocklistPOST{
		Add:    additions,
		Remove: removals,
		IsHash: isHash,
	}
	data, err := json.Marshal(sbp)
	if err != nil {
		return err
	}
	err = c.post("/skynet/blocklist", string(data), nil)
	return
}

// SkynetBlocklistPost requests the /skynet/blocklist Post endpoint
func (c *Client) SkynetBlocklistPost(additions, removals []string) (err error) {
	err = c.SkynetBlocklistHashPost(additions, removals, false)
	return
}

// SkynetPortalsGet requests the /skynet/portals Get endpoint.
func (c *Client) SkynetPortalsGet() (portals api.SkynetPortalsGET, err error) {
	err = c.get("/skynet/portals", &portals)
	return
}

// SkynetPortalsPost requests the /skynet/portals Post endpoint.
func (c *Client) SkynetPortalsPost(additions []skymodules.SkynetPortal, removals []modules.NetAddress) (err error) {
	spp := api.SkynetPortalsPOST{
		Add:    additions,
		Remove: removals,
	}
	data, err := json.Marshal(spp)
	if err != nil {
		return err
	}
	err = c.post("/skynet/portals", string(data), nil)
	return
}

// SkynetStatsGet requests the /skynet/stats Get endpoint
func (c *Client) SkynetStatsGet() (stats api.SkynetStatsGET, err error) {
	err = c.get("/skynet/stats", &stats)
	return
}

// SkykeyGetByName requests the /skynet/skykey Get endpoint using the key name.
func (c *Client) SkykeyGetByName(name string) (skykey.Skykey, error) {
	values := url.Values{}
	values.Set("name", name)
	getQuery := fmt.Sprintf("/skynet/skykey?%s", values.Encode())

	var skykeyGet api.SkykeyGET
	err := c.get(getQuery, &skykeyGet)
	if err != nil {
		return skykey.Skykey{}, err
	}

	var sk skykey.Skykey
	err = sk.FromString(skykeyGet.Skykey)
	if err != nil {
		return skykey.Skykey{}, err
	}

	return sk, nil
}

// SkykeyGetByID requests the /skynet/skykey Get endpoint using the key ID.
func (c *Client) SkykeyGetByID(id skykey.SkykeyID) (skykey.Skykey, error) {
	values := url.Values{}
	values.Set("id", id.ToString())
	getQuery := fmt.Sprintf("/skynet/skykey?%s", values.Encode())

	var skykeyGet api.SkykeyGET
	err := c.get(getQuery, &skykeyGet)
	if err != nil {
		return skykey.Skykey{}, err
	}

	var sk skykey.Skykey
	err = sk.FromString(skykeyGet.Skykey)
	if err != nil {
		return skykey.Skykey{}, err
	}

	return sk, nil
}

// SkykeyDeleteByIDPost requests the /skynet/deleteskykey POST endpoint using the key ID.
func (c *Client) SkykeyDeleteByIDPost(id skykey.SkykeyID) error {
	values := url.Values{}
	values.Set("id", id.ToString())
	return c.post("/skynet/deleteskykey", values.Encode(), nil)
}

// SkykeyDeleteByNamePost requests the /skynet/deleteskykey POST endpoint using
// the key name.
func (c *Client) SkykeyDeleteByNamePost(name string) error {
	values := url.Values{}
	values.Set("name", name)
	return c.post("/skynet/deleteskykey", values.Encode(), nil)
}

// SkykeyCreateKeyPost requests the /skynet/createskykey POST endpoint.
func (c *Client) SkykeyCreateKeyPost(name string, skType skykey.SkykeyType) (skykey.Skykey, error) {
	// Set the url values.
	values := url.Values{}
	values.Set("name", name)
	values.Set("type", skType.ToString())

	var skykeyGet api.SkykeyGET
	err := c.post("/skynet/createskykey", values.Encode(), &skykeyGet)
	if err != nil {
		return skykey.Skykey{}, errors.AddContext(err, "createskykey POST request failed")
	}

	var sk skykey.Skykey
	err = sk.FromString(skykeyGet.Skykey)
	if err != nil {
		return skykey.Skykey{}, errors.AddContext(err, "failed to decode skykey string")
	}
	return sk, nil
}

// SkykeyAddKeyPost requests the /skynet/addskykey POST endpoint.
func (c *Client) SkykeyAddKeyPost(sk skykey.Skykey) error {
	values := url.Values{}
	skString, err := sk.ToString()
	if err != nil {
		return errors.AddContext(err, "failed to encode skykey as string")
	}
	values.Set("skykey", skString)

	err = c.post("/skynet/addskykey", values.Encode(), nil)
	if err != nil {
		return errors.AddContext(err, "addskykey POST request failed")
	}

	return nil
}

// SkykeySkykeysGet requests the /skynet/skykeys GET endpoint.
func (c *Client) SkykeySkykeysGet() ([]skykey.Skykey, error) {
	var skykeysGet api.SkykeysGET
	err := c.get("/skynet/skykeys", &skykeysGet)
	if err != nil {
		return nil, errors.AddContext(err, "allskykeys GET request failed")
	}

	res := make([]skykey.Skykey, len(skykeysGet.Skykeys))
	for i, skGET := range skykeysGet.Skykeys {
		err = res[i].FromString(skGET.Skykey)
		if err != nil {
			return nil, errors.AddContext(err, "failed to decode skykey string")
		}
	}
	return res, nil
}

// SkylinkHealthGET queries the /skynet/health/skylink/:skylink endpoint.
func (c *Client) SkylinkHealthGET(sl skymodules.Skylink) (sh skymodules.SkylinkHealth, err error) {
	err = c.get(fmt.Sprintf("/skynet/health/skylink/%s", sl.String()), &sh)
	return
}

// RegistryRead queries the /skynet/registry [GET] endpoint.
func (c *Client) RegistryRead(spk types.SiaPublicKey, dataKey crypto.Hash) (modules.SignedRegistryValue, error) {
	return c.RegistryReadWithTimeout(spk, dataKey, 0)
}

// ResolveSkylinkV2 queries the /skynet/resolve/:skylink [GET] endpoint.
func (c *Client) ResolveSkylinkV2(skylink string) (string, error) {
	return c.ResolveSkylinkV2WithTimeout(skylink, 0)
}

// ResolveSkylinkV2WithTimeout queries the /skynet/resolve/:skylink [GET]
// endpoint.
func (c *Client) ResolveSkylinkV2WithTimeout(skylink string, timeout time.Duration) (string, error) {
	sl, _, err := c.ResolveSkylinkV2Custom(skylink, timeout)
	return sl, err
}

// ResolveSkylinkV2Custom queries the /skynet/resolve/:skylink [GET] endpoint.
func (c *Client) ResolveSkylinkV2Custom(skylink string, timeout time.Duration) (string, http.Header, error) {
	// Set the values.
	values := url.Values{}
	if timeout > 0 {
		values.Set("timeout", fmt.Sprint(int(timeout.Seconds())))
	}

	// Send request.
	h, b, err := c.getRawResponse(fmt.Sprintf("/skynet/resolve/%v?%v", skylink, values.Encode()))
	if err != nil {
		return "", nil, err
	}
	var srg api.SkylinkResolveGET
	err = json.Unmarshal(b, &srg)
	if err != nil {
		return "", nil, err
	}
	return srg.Skylink, h, err
}

// RegistryReadWithTimeout queries the /skynet/registry [GET] endpoint with the
// specified timeout.
func (c *Client) RegistryReadWithTimeout(spk types.SiaPublicKey, dataKey crypto.Hash, timeout time.Duration) (modules.SignedRegistryValue, error) {
	// Set the values.
	values := url.Values{}
	values.Set("publickey", spk.String())
	values.Set("datakey", dataKey.String())
	if timeout > 0 {
		values.Set("timeout", fmt.Sprint(int(timeout.Seconds())))
	}

	// Send request.
	var rhg api.RegistryHandlerGET
	err := c.get(fmt.Sprintf("/skynet/registry?%v", values.Encode()), &rhg)
	if err != nil {
		return modules.SignedRegistryValue{}, err
	}

	// Decode data.
	data, err := hex.DecodeString(rhg.Data)
	if err != nil {
		return modules.SignedRegistryValue{}, errors.AddContext(err, "failed to decode signature")
	}

	// Decode signature.
	var sig crypto.Signature
	sigBytes, err := hex.DecodeString(rhg.Signature)
	if err != nil {
		return modules.SignedRegistryValue{}, errors.AddContext(err, "failed to decode signature")
	}
	if len(sigBytes) != len(sig) {
		return modules.SignedRegistryValue{}, fmt.Errorf("unexpected signature length %v != %v", len(sigBytes), len(sig))
	}
	copy(sig[:], sigBytes)

	// Verify pubkey.
	if !rhg.PublicKey.Equals(spk) {
		return modules.SignedRegistryValue{}, fmt.Errorf("unexpected pubkey %v != %v", rhg.PublicKey, spk)
	}

	srv := modules.NewSignedRegistryValue(dataKey, data, rhg.Revision, sig, rhg.Type)
	return srv, srv.Verify(spk.ToPublicKey())
}

// RegistryEntryHealth queries the /skynet/health/entry endpoint to get a
// registry entry's health.
func (c *Client) RegistryEntryHealth(spk types.SiaPublicKey, dataKey crypto.Hash) (reh skymodules.RegistryEntryHealth, err error) {
	values := url.Values{}
	values.Set("publickey", spk.String())
	values.Set("datakey", dataKey.String())
	err = c.get(fmt.Sprintf("/skynet/health/entry?%s", values.Encode()), &reh)
	return
}

// RegistryEntryHealthRID queries the /skynet/health/entry endpoint to get a
// registry entry's health.
func (c *Client) RegistryEntryHealthRID(rid modules.RegistryEntryID) (reh skymodules.RegistryEntryHealth, err error) {
	values := url.Values{}
	values.Set("entryid", crypto.Hash(rid).String())
	err = c.get(fmt.Sprintf("/skynet/health/entry?%s", values.Encode()), &reh)
	return
}

// RegistryUpdate queries the /skynet/registry [POST] endpoint.
func (c *Client) RegistryUpdate(spk types.SiaPublicKey, dataKey crypto.Hash, revision uint64, sig crypto.Signature, skylink skymodules.Skylink) error {
	return c.RegistryUpdateWithEntry(spk, modules.NewSignedRegistryValue(dataKey, skylink.Bytes(), revision, sig, modules.RegistryTypeWithoutPubkey))
}

// RegistryUpdateMulti queries the /skynet/registrymulti [POST] endpoint.
func (c *Client) RegistryUpdateMulti(srvs map[string]skymodules.RegistryEntry) error {
	req := make([]api.RegistryHandlerMultiRequestPOST, 0, len(srvs))
	for hk, srv := range srvs {
		var hpk types.SiaPublicKey
		if err := hpk.LoadString(hk); err != nil {
			return fmt.Errorf("invalid hostkey %v", hk)
		}
		req = append(req, api.RegistryHandlerMultiRequestPOST{
			RegistryHandlerRequestPOST: api.RegistryHandlerRequestPOST{
				PublicKey: srv.PubKey,
				DataKey:   srv.Tweak,
				Revision:  srv.Revision,
				Signature: srv.Signature,
				Data:      srv.Data,
				Type:      srv.Type,
			},
			HostKey: hpk,
		})
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return c.post("/skynet/registrymulti", string(reqBytes), nil)
}

// RegistryUpdateWithEntry queries the /skynet/registry [POST] endpoint.
func (c *Client) RegistryUpdateWithEntry(spk types.SiaPublicKey, srv modules.SignedRegistryValue) error {
	req := api.RegistryHandlerRequestPOST{
		PublicKey: spk,
		DataKey:   srv.Tweak,
		Revision:  srv.Revision,
		Signature: srv.Signature,
		Data:      srv.Data,
		Type:      srv.Type,
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return c.post("/skynet/registry", string(reqBytes), nil)
}

// SkylinkFromTUSURL is a helper to fetch the skylink of a finished upload.
func SkylinkFromTUSURL(tc *tus.Client, url string) (_ string, err error) {
	// After the upload, fetch the skylink from the metadata.
	req, err := http.NewRequest("HEAD", url, bytes.NewReader([]byte{}))
	if err != nil {
		return "", err
	}
	resp, err := tc.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Compose(err, resp.Body.Close())
	}()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch upload info: %v", resp.StatusCode)
	}

	// Get upload metadata from header.
	metadata, found := resp.Header["Upload-Metadata"]
	if !found {
		return "", errors.New("metadata header not set")
	}

	// Find the skylink in the metadata.
	var skylink64 string
	for _, md := range metadata {
		for _, entry := range strings.Split(md, ",") {
			splitEntry := strings.Split(entry, " ")
			if splitEntry[0] != "Skylink" {
				continue
			}
			if len(splitEntry) != 2 {
				return "", fmt.Errorf("invalid skylink metadata '%v'", entry)
			}
			if skylink64 != "" {
				return "", errors.New("skylink field exists more than once")
			}
			skylink64 = splitEntry[1]
		}
	}
	if skylink64 == "" {
		return "", fmt.Errorf("skylink metadata missing")
	}

	// Decode skylink.
	skylink, err := base64.StdEncoding.DecodeString(skylink64)
	if err != nil {
		return "", errors.New("failed to decode skylink")
	}
	return string(skylink), nil
}

// SkylinkFromTUSID fetches the skylink of a finished TUS upload by the upload's
// ID.
func (c *Client) SkylinkFromTUSID(id string) (string, error) {
	header, data, err := c.getRawResponse(fmt.Sprintf("/skynet/upload/tus/%s", id))
	if err != nil {
		return "", err
	}
	var sshp api.SkynetSkyfileHandlerPOST
	err = json.Unmarshal(data, &sshp)
	if err != nil {
		return "", err
	}
	skylinkHeader := header[api.SkynetSkylinkHeader]
	if len(skylinkHeader) != 1 {
		return "", errors.New("SkylinkFromTUSID: Skynet-Skylink header has wrong length")
	}
	bodySkylink := sshp.Skylink
	headerSkylink := skylinkHeader[0]
	if headerSkylink != bodySkylink {
		return "", fmt.Errorf("SkylinkFromTUSID: skylink mismatch %v != %v", headerSkylink, bodySkylink)
	}
	var sl skymodules.Skylink
	err = sl.LoadString(headerSkylink)
	if err != nil {
		return "", err
	}
	if sl.MerkleRoot() != sshp.MerkleRoot {
		return "", errors.New("returned merkleroot doesn't match skylink's")
	}
	if sl.Bitfield() != sshp.Bitfield {
		return "", errors.New("returned bitfield doesn't match skylink's")
	}
	return headerSkylink, nil
}

// skylinkQueryWithValues returns a skylink query based on the given skylink and
// values. If the values are empty it will not append a `?` to the query.
func skylinkQueryWithValues(skylink string, values url.Values) string {
	getQuery := fmt.Sprintf("/skynet/skylink/%s", skylink)
	if len(values) > 0 {
		getQuery = fmt.Sprintf("%s?%s", getQuery, values.Encode())
	}
	return getQuery
}

// urlValuesFromSkyfileMultipartUploadParameters is a helper function that
// transforms the given SkyfileMultipartUploadParameters into an url values
// object.
func urlValuesFromSkyfileMultipartUploadParameters(sup skymodules.SkyfileMultipartUploadParameters) (url.Values, error) {
	values := url.Values{}
	values.Set("siapath", sup.SiaPath.String())
	values.Set("force", fmt.Sprintf("%t", sup.Force))
	values.Set("root", fmt.Sprintf("%t", sup.Root))
	values.Set("basechunkredundancy", fmt.Sprintf("%v", sup.BaseChunkRedundancy))
	values.Set("filename", sup.Filename)
	values.Set("defaultpath", sup.DefaultPath)
	values.Set("disabledefaultpath", strconv.FormatBool(sup.DisableDefaultPath))

	// We check the length because we want to only serialize this when its
	// length is more than zero in order to match the behaviour of
	// url.Values{}.Encode().
	if len(sup.TryFiles) > 0 {
		b, err := json.Marshal(sup.TryFiles)
		if err != nil {
			return url.Values{}, err
		}
		values.Set("tryfiles", string(b))
	}

	b, err := json.Marshal(sup.ErrorPages)
	if err != nil {
		return url.Values{}, err
	}
	values.Set("errorpages", string(b))

	return values, nil
}

// urlValuesFromSkyfilePinParameters is a helper function that transforms the
// given SkyfilePinParameters into a url values object.
func urlValuesFromSkyfilePinParameters(sup skymodules.SkyfilePinParameters) url.Values {
	values := url.Values{}
	values.Set("siapath", sup.SiaPath.String())
	values.Set("force", fmt.Sprintf("%t", sup.Force))
	values.Set("root", fmt.Sprintf("%t", sup.Root))
	values.Set("basechunkredundancy", fmt.Sprintf("%v", sup.BaseChunkRedundancy))
	return values
}

// urlEncodeSkyfileUploadParameters is a helper function that transforms the
// given SkyfileUploadParameters into a url values object.
func urlValuesFromSkyfileUploadParameters(sup skymodules.SkyfileUploadParameters) (url.Values, error) {
	values := url.Values{}
	values.Set("siapath", sup.SiaPath.String())
	values.Set("dryrun", fmt.Sprintf("%t", sup.DryRun))
	values.Set("force", fmt.Sprintf("%t", sup.Force))
	values.Set("root", fmt.Sprintf("%t", sup.Root))
	values.Set("basechunkredundancy", fmt.Sprintf("%v", sup.BaseChunkRedundancy))
	values.Set("filename", sup.Filename)
	values.Set("mode", fmt.Sprintf("%o", sup.Mode))
	values.Set("defaultpath", sup.DefaultPath)
	values.Set("disabledefaultpath", strconv.FormatBool(sup.DisableDefaultPath))

	b, err := json.Marshal(sup.TryFiles)
	if err != nil {
		return url.Values{}, err
	}
	values.Set("tryfiles", string(b))
	b, err = json.Marshal(sup.ErrorPages)
	if err != nil {
		return url.Values{}, err
	}
	values.Set("errorpages", string(b))

	// encode encryption parameters
	if sup.SkykeyName != "" {
		values.Set("skykeyname", sup.SkykeyName)
	}
	if sup.SkykeyID != (skykey.SkykeyID{}) {
		values.Set("skykeyid", sup.SkykeyID.ToString())
	}
	return values, nil
}

// urlEncodeSkyfileUploadParameters is a helper function that url encodes the
// given SkyfileUploadParameters
func urlEncodeSkyfileUploadParameters(sup skymodules.SkyfileUploadParameters) (string, error) {
	values, err := urlValuesFromSkyfileUploadParameters(sup)
	if err != nil {
		return "", err
	}
	return values.Encode(), nil
}

// SkynetSkylinkUnpinPost uses the /skynet/unpin endpoint to remove the any
// files associated with the given skylink from the renter.
func (c *Client) SkynetSkylinkUnpinPost(skylink string) error {
	return c.SkynetSkylinkUnpinCustomPost(skylink, skymodules.SiaPath{})
}

// SkynetSkylinkUnpinCustomPost uses the /skynet/unpin endpoint to remove the
// any files associated with the given skylink from the renter.
func (c *Client) SkynetSkylinkUnpinCustomPost(skylink string, siaPath skymodules.SiaPath) error {
	values := url.Values{}
	if !siaPath.IsEmpty() {
		values.Set("siapath", siaPath.String())
	}
	query := fmt.Sprintf("/skynet/unpin/%s?%s", skylink, values.Encode())
	_, _, err := c.postRawResponse(query, nil)
	return err
}
