package api

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skykey"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
)

var (
	// errIncompleteRangeRequest is the error returned when the range
	// request is incomplete.
	errIncompleteRangeRequest = errors.New("the 'start' and 'end' params must be both blank or provided")

	// errInvalidRangeParams is the error returned when the range params are
	// invalid.
	errInvalidRangeParams = errors.New("'start' param should be less than or equal to 'end' param")

	// errRangeSetTwice is the error returned when the range is set twice,
	// once in the Header and once in the query params
	errRangeSetTwice = errors.New("range request should use either the Header or the query params but not both")

	// errTimeoutTooHigh is returned when a parsed timeout exceeds the max.
	errTimeoutTooHigh = errors.New("'timeout' parameter too high")

	// errZeroTimeout is returned if the timeout is explicitly set to 0.
	errZeroTimeout = errors.New("can't specify a zero timeout")
)

type (
	// skyfileUploadParams is a helper struct that contains all of the query
	// string parameters on download
	skyfileDownloadParams struct {
		attachment           bool
		format               skymodules.SkyfileFormat
		includeLayout        bool
		path                 string
		pricePerMS           types.Currency
		skylink              skymodules.Skylink
		skylinkStringNoQuery string
		timeout              time.Duration
	}

	// skyfileUploadParams is a helper struct that contains all of the query
	// string parameters on upload
	skyfileUploadParams struct {
		baseChunkRedundancy uint8
		defaultPath         string
		convertPath         string
		disableDefaultPath  bool
		tryFiles            []string
		errorPages          map[int]string
		dryRun              bool
		filename            string
		force               bool
		mode                os.FileMode
		root                bool
		siaPath             skymodules.SiaPath
		skyKeyID            skykey.SkykeyID
		skyKeyName          string
	}

	// skyfileUploadHeaders is a helper struct that contains all of the request
	// headers on upload
	skyfileUploadHeaders struct {
		mediaType    string
		disableForce bool
	}
)

// writeReader is a helper type that turns a writer into a io.WriteReader.
type writeReader struct {
	io.Writer
}

// Read implements the io.Reader interface but returns 0 and EOF.
func (wr *writeReader) Read(_ []byte) (int, error) {
	build.Critical("Read method of the writeReader is not intended to be used")
	return 0, io.EOF
}

// newCustomErrorWriter creates a new customErrorWriter.
func newCustomErrorWriter(meta skymodules.SkyfileMetadata, streamer io.ReadSeeker) *customErrorWriter {
	if meta.ErrorPages == nil {
		meta.ErrorPages = make(map[int]string)
	}
	return &customErrorWriter{
		staticMetadata: meta,
		staticStreamer: streamer,
	}
}

// customErrorWriter responds to errors with custom content.
type customErrorWriter struct {
	staticMetadata skymodules.SkyfileMetadata
	staticStreamer io.ReadSeeker
}

// WriteError checks whether there's custom content configured for the
// given error code and writes it the writer, otherwise it sends the standard
// error content.
func (ew customErrorWriter) WriteError(w http.ResponseWriter, e Error, code int) {
	// If we don't have a custom error page for this error code just serve the
	// standard response.
	if _, exist := ew.staticMetadata.ErrorPages[code]; !exist {
		WriteError(w, e, code)
		return
	}
	contentReader, contentType, err := ew.customContent(code)
	if err != nil {
		msg := fmt.Sprintf("Failed to fetch custom error content which should exist.\ntryfiles: %+v\nsubfiles: %+v\nerror: %+v\n", ew.staticMetadata.TryFiles, ew.staticMetadata.Subfiles, err)
		build.Critical(msg)
		WriteError(w, e, code)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(code)
	_, err = io.Copy(w, contentReader)
	if err != nil {
		build.Critical("Failed to write custom error content:", err)
	}
}

// customContent returns the custom error content that matches the given status
// code, as well as its content type.
func (ew *customErrorWriter) customContent(status int) (io.Reader, string, error) {
	errpath, exists := ew.staticMetadata.ErrorPages[status]
	if !exists {
		return nil, "", os.ErrNotExist
	}

	metadataForPath, _, offset, size := ew.staticMetadata.ForPath(errpath)
	if len(metadataForPath.Subfiles) == 0 {
		return nil, "", fmt.Errorf("custom error page for status %d not found", status)
	}
	_, err := ew.staticStreamer.Seek(int64(offset), io.SeekStart)
	if err != nil {
		return nil, "", fmt.Errorf("failed to serve custom contents for status code %d, invalid offset, error '%s'", status, err.Error())
	}
	return io.LimitReader(ew.staticStreamer, int64(size)), metadataForPath.ContentType(), nil
}

// buildETag is a helper function that returns an ETag.
func buildETag(skylink skymodules.Skylink, path string, format skymodules.SkyfileFormat) string {
	return crypto.HashAll(
		skylink.String(),
		path,
		string(format),
		"1", // random variable to cache bust all existing ETags (SkylinkV2 fix)
	).String()
}

// isMultipartRequest is a helper method that checks if the given media type
// matches that of a multipart form.
func isMultipartRequest(mediaType string) bool {
	return strings.HasPrefix(mediaType, "multipart/form-data")
}

// parseSkylinkURL splits a raw skylink URL into its components - a skylink, a
// string representation of the skylink with the query parameters stripped, and
// a path. The input skylink URL should not have been URL-decoded. The path is
// URL-decoded before returning as it is for us to parse and use, while the
// other components remain encoded for the skapp.
func parseSkylinkURL(skylinkURL, apiRoute string) (skylink skymodules.Skylink, skylinkStringNoQuery, path string, err error) {
	s := strings.TrimPrefix(skylinkURL, apiRoute)
	s = strings.TrimPrefix(s, "/")
	// Parse out optional path to a subfile
	path = "/" // default to root
	splits := strings.SplitN(s, "?", 2)
	skylinkStringNoQuery = splits[0]
	splits = strings.SplitN(skylinkStringNoQuery, "/", 2)
	// Check if a path is passed.
	if len(splits) > 1 && len(splits[1]) > 0 {
		path = skymodules.EnsurePrefix(splits[1], "/")
	}
	// Decode the path as it may contain URL-encoded characters.
	path, err = url.QueryUnescape(path)
	if err != nil {
		return
	}
	// Parse skylink
	err = skylink.LoadString(s)
	return
}

// parseTimeout tries to parse the timeout from the query string and validate
// it. If not present, it will default to DefaultSkynetRequestTimeout.
func parseTimeout(queryForm url.Values) (time.Duration, error) {
	timeoutStr := queryForm.Get("timeout")
	if timeoutStr == "" {
		return DefaultSkynetRequestTimeout, nil
	}

	var timeoutInt uint64
	_, err := fmt.Sscan(timeoutStr, &timeoutInt)
	if err != nil {
		return 0, errors.AddContext(err, "unable to parse 'timeout'")
	}
	if timeoutInt > uint64(MaxSkynetRequestTimeout.Seconds()) {
		return 0, errors.AddContext(errTimeoutTooHigh, fmt.Sprintf("maximum allowed timeout is %ds", MaxSkynetRequestTimeout))
	}
	if timeoutInt == 0 {
		return 0, errZeroTimeout
	}
	return time.Duration(timeoutInt) * time.Second, nil
}

// parseRegistryTimeout tries to parse the timeout from the query string and
// validate it. If not present, it will default to the max allowed value.
func parseRegistryTimeout(queryForm url.Values) (time.Duration, error) {
	timeoutStr := queryForm.Get("timeout")
	if timeoutStr == "" {
		return renter.DefaultRegistryHealthTimeout, nil
	}

	var timeoutInt uint64
	_, err := fmt.Sscan(timeoutStr, &timeoutInt)
	if err != nil {
		return 0, errors.AddContext(err, "unable to parse 'timeout'")
	}
	if timeoutInt > uint64(renter.MaxRegistryReadTimeout.Seconds()) {
		return 0, errors.AddContext(errTimeoutTooHigh, fmt.Sprintf("maximum allowed timeout is %ds", MaxSkynetRequestTimeout))
	}
	if timeoutInt == 0 {
		return 0, errZeroTimeout
	}
	return time.Duration(timeoutInt) * time.Second, nil
}

// parseStatsType parses a stats type as string to a specifier. This method
// ensures we never call types.NewSpecifier with invalid (API) input as that
// will panic on runtime.
func parseStatsType(statsType string) (types.Specifier, error) {
	switch statsType {
	case "OverdriveStats":
		return skymodules.OverdriveStats, nil
	default:
		return types.Specifier{}, errors.New("invalid statsType")
	}
}

// parseDownloadRequestParameters is a helper function that parses all of the
// query parameters from a download request
func parseDownloadRequestParameters(req *http.Request) (*skyfileDownloadParams, error) {
	// Parse the skylink from the raw URL of the request. Any special characters
	// in the raw URL are encoded, allowing us to differentiate e.g. the '?'
	// that begins query parameters from the encoded version '%3F'.
	skylink, skylinkStringNoQuery, path, err := parseSkylinkURL(req.URL.String(), "/skynet/skylink/")
	if err != nil {
		return nil, fmt.Errorf("error parsing skylink: %v", err)
	}

	// Parse the query params.
	queryForm, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		return nil, errors.New("failed to parse query params")
	}

	// Parse the 'attachment' query string parameter.
	var attachment bool
	attachmentStr := queryForm.Get("attachment")
	if attachmentStr != "" {
		attachment, err = strconv.ParseBool(attachmentStr)
		if err != nil {
			return nil, fmt.Errorf("unable to parse 'attachment' parameter: %v", err)
		}
	}

	// Parse the 'format' query string parameter.
	format := skymodules.SkyfileFormat(strings.ToLower(queryForm.Get("format")))
	switch format {
	case skymodules.SkyfileFormatNotSpecified:
	case skymodules.SkyfileFormatConcat:
	case skymodules.SkyfileFormatTar:
	case skymodules.SkyfileFormatTarGz:
	case skymodules.SkyfileFormatZip:
	default:
		return nil, errors.New("unable to parse 'format' parameter, allowed values are: 'concat', 'tar', 'targz' and 'zip'")
	}

	// Parse the `include-layout` query string parameter.
	var includeLayout bool
	includeLayoutStr := queryForm.Get("include-layout")
	if includeLayoutStr != "" {
		includeLayout, err = strconv.ParseBool(includeLayoutStr)
		if err != nil {
			return nil, fmt.Errorf("unable to parse 'include-layout' parameter: %v", err)
		}
	}

	// Parse the timeout.
	timeout, err := parseTimeout(queryForm)
	if err != nil {
		return nil, err
	}

	// Parse pricePerMS.
	pricePerMS := skymodules.DefaultSkynetPricePerMS
	pricePerMSStr := queryForm.Get("priceperms")
	if pricePerMSStr != "" {
		_, err = fmt.Sscan(pricePerMSStr, &pricePerMS)
		if err != nil {
			return nil, fmt.Errorf("unable to parse 'pricePerMS' parameter: %v", err)
		}
	}

	// Parse a range request from the query form
	startStr := queryForm.Get("start")
	endStr := queryForm.Get("end")
	var start, end uint64
	rangeParam := startStr != "" && endStr != ""
	if rangeParam {
		// Verify we don't have a range request in both the Header and the params
		headerRange := req.Header.Get("Range")
		if headerRange != "" {
			return nil, errRangeSetTwice
		}
		// Parse start param
		start, err = strconv.ParseUint(startStr, 10, 64)
		if err != nil {
			return nil, errors.AddContext(err, "unable to parse 'start' parameter")
		}
		// Parse end param
		end, err = strconv.ParseUint(endStr, 10, 64)
		if err != nil {
			return nil, errors.AddContext(err, "unable to parse 'end' parameter")
		}
		// Check that start is not greater than end. It is ok for end to
		// equal start as that would indicate a request for a single
		// byte.
		if start > end {
			return nil, errInvalidRangeParams
		}

		// Set the Range field in the Header
		AddRangeHeaderToRequest(req, start, end)
	} else if startStr != "" || endStr != "" {
		return nil, errIncompleteRangeRequest
	}

	return &skyfileDownloadParams{
		attachment:           attachment,
		format:               format,
		includeLayout:        includeLayout,
		path:                 path,
		pricePerMS:           pricePerMS,
		skylink:              skylink,
		skylinkStringNoQuery: skylinkStringNoQuery,
		timeout:              timeout,
	}, nil
}

// parseUploadHeadersAndRequestParameters is a helper function that parses all
// the query parameters and headers from an upload request
func parseUploadHeadersAndRequestParameters(req *http.Request, ps httprouter.Params) (*skyfileUploadHeaders, *skyfileUploadParams, error) {
	var err error

	// parse 'Skynet-Disable-Force' request header
	var disableForce bool
	strDisableForce := req.Header.Get(SkynetDisableForceHeader)
	if strDisableForce != "" {
		disableForce, err = strconv.ParseBool(strDisableForce)
		if err != nil {
			return nil, nil, errors.AddContext(err, "unable to parse 'Skynet-Disable-Force' header")
		}
	}

	// parse 'Content-Type' request header
	ct := req.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return nil, nil, errors.AddContext(err, "failed parsing 'Content-Type' header")
	}

	// parse query
	queryForm, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		return nil, nil, errors.AddContext(err, "failed to parse query")
	}

	// parse 'basechunkredundancy' query parameter
	baseChunkRedundancy := uint8(0)
	if rStr := queryForm.Get("basechunkredundancy"); rStr != "" {
		if _, err := fmt.Sscan(rStr, &baseChunkRedundancy); err != nil {
			return nil, nil, errors.AddContext(err, "unable to parse 'basechunkredundancy' parameter")
		}
	}

	// parse 'convertpath' query parameter
	convertPath := queryForm.Get("convertpath")

	// parse 'defaultpath' query parameter
	defaultPath := queryForm.Get("defaultpath")
	if defaultPath != "" {
		defaultPath = skymodules.EnsurePrefix(defaultPath, "/")
	}

	// parse 'disabledefaultpath' query parameter
	var disableDefaultPath bool
	disableDefaultPathStr := queryForm.Get("disabledefaultpath")
	if disableDefaultPathStr != "" {
		disableDefaultPath, err = strconv.ParseBool(disableDefaultPathStr)
		if err != nil {
			return nil, nil, errors.AddContext(err, "unable to parse 'disabledefaultpath' parameter")
		}
	}

	// parse 'tryfiles' query parameter
	var tryFiles []string
	// There is a difference between the tryfiles value being set to empty or
	// not being set at all. If it's not set at all we'll use the default value
	// but if it's set to empty we will leave it empty.
	if _, isSet := queryForm["tryfiles"]; isSet {
		tryFiles, err = UnmarshalTryFiles(queryForm.Get("tryfiles"))
		if err != nil {
			return nil, nil, errors.AddContext(err, "unable to parse 'tryfiles' parameter")
		}
		if (defaultPath != "" || disableDefaultPath) && len(tryFiles) > 0 {
			return nil, nil, errors.New("defaultpath and disabledefaultpath are not compatible with tryfiles")
		}
	} else {
		// If we don't have any tryfiles defined, and we don't have a defaultpath or
		// disabledefaultpath, we want to default to tryfiles with index.html.
		// This only happens if the tryfiles are not passed at all.
		if defaultPath == "" && disableDefaultPath == false {
			tryFiles = skymodules.DefaultTryFilesValue
		}
	}

	errPages, err := UnmarshalErrorPages(queryForm.Get("errorpages"))
	if err != nil {
		return nil, nil, errors.AddContext(err, "invalid 'errorpages' parameter")
	}

	// parse 'dryrun' query parameter
	var dryRun bool
	dryRunStr := queryForm.Get("dryrun")
	if dryRunStr != "" {
		dryRun, err = strconv.ParseBool(dryRunStr)
		if err != nil {
			return nil, nil, errors.AddContext(err, "unable to parse 'dryrun' parameter")
		}
	}

	// parse 'filename' query parameter
	filename := queryForm.Get("filename")

	// parse 'force' query parameter
	var force bool
	strForce := queryForm.Get("force")
	if strForce != "" {
		force, err = strconv.ParseBool(strForce)
		if err != nil {
			return nil, nil, errors.AddContext(err, "unable to parse 'force' parameter")
		}
	}

	// parse 'mode' query parameter
	modeStr := queryForm.Get("mode")
	var mode os.FileMode
	if modeStr != "" {
		_, err := fmt.Sscanf(modeStr, "%o", &mode)
		if err != nil {
			return nil, nil, errors.AddContext(err, "unable to parse 'mode' parameter")
		}
	}

	// parse 'root' query parameter
	var root bool
	rootStr := queryForm.Get("root")
	if rootStr != "" {
		root, err = strconv.ParseBool(rootStr)
		if err != nil {
			return nil, nil, errors.AddContext(err, "unable to parse 'root' parameter")
		}
	}

	// parse 'siapath' query parameter
	var siaPath skymodules.SiaPath
	siaPathStr := ps.ByName("siapath")
	if root {
		siaPath, err = skymodules.NewSiaPath(siaPathStr)
	} else {
		siaPath, err = skymodules.SkynetFolder.Join(siaPathStr)
	}
	if err != nil {
		return nil, nil, errors.AddContext(err, "unable to parse 'siapath' parameter")
	}

	// parse 'skykeyname' query parameter
	skykeyName := queryForm.Get("skykeyname")

	// parse 'skykeyid' query parameter
	var skykeyID skykey.SkykeyID
	skykeyIDStr := queryForm.Get("skykeyid")
	if skykeyIDStr != "" {
		err = skykeyID.FromString(skykeyIDStr)
		if err != nil {
			return nil, nil, errors.AddContext(err, "unable to parse 'skykeyid'")
		}
	}

	// validate parameter combos

	// verify force is not set if disable force header was set
	if disableForce && force {
		return nil, nil, errors.New("'force' has been disabled on this node")
	}

	// verify the dry-run and force parameter are not combined
	if !disableForce && force && dryRun {
		return nil, nil, errors.New("'dryRun' and 'force' can not be combined")
	}

	// verify disabledefaultpath and defaultpath are not combined
	if disableDefaultPath && defaultPath != "" {
		return nil, nil, errors.AddContext(skymodules.ErrInvalidDefaultPath, "DefaultPath and DisableDefaultPath are mutually exclusive and cannot be set together")
	}

	// verify default path params are not set if it's not a multipart upload
	if !isMultipartRequest(mediaType) && (disableDefaultPath || defaultPath != "") {
		return nil, nil, errors.New("DefaultPath and DisableDefaultPath can only be set on multipart uploads")
	}

	// verify convertpath and filename are not combined
	if convertPath != "" && filename != "" {
		return nil, nil, errors.New("cannot set both a 'convertpath' and a 'filename'")
	}

	// verify skykeyname and skykeyid are not combined
	if skykeyName != "" && skykeyIDStr != "" {
		return nil, nil, errors.New("cannot set both a 'skykeyname' and 'skykeyid'")
	}

	// create headers and parameters
	headers := &skyfileUploadHeaders{
		disableForce: disableForce,
		mediaType:    mediaType,
	}
	params := &skyfileUploadParams{
		baseChunkRedundancy: baseChunkRedundancy,
		convertPath:         convertPath,
		defaultPath:         defaultPath,
		disableDefaultPath:  disableDefaultPath,
		dryRun:              dryRun,
		errorPages:          errPages,
		filename:            filename,
		force:               force,
		mode:                mode,
		root:                root,
		siaPath:             siaPath,
		skyKeyID:            skykeyID,
		skyKeyName:          skykeyName,
		tryFiles:            tryFiles,
	}
	return headers, params, nil
}

// serveArchive serves skyfiles as an archive by reading them from r and writing
// the archive to dst using the given archiveFunc.
func serveArchive(w http.ResponseWriter, src io.ReadSeeker, format skymodules.SkyfileFormat, md skymodules.SkyfileMetadata) (err error) {
	// Based upon the given format, set the Content-Type header, wrap the writer
	// and select an archive function.
	var dst io.Writer
	var archiveFunc archiveFunc
	switch format {
	case skymodules.SkyfileFormatTar:
		archiveFunc = serveTar
		w.Header().Set("Content-Type", "application/x-tar")
		dst = w
	case skymodules.SkyfileFormatTarGz:
		archiveFunc = serveTar
		w.Header().Set("Content-Type", "application/gzip")
		gzw := gzip.NewWriter(w)
		defer func() {
			err = errors.Compose(err, gzw.Close())
		}()
		dst = gzw
	case skymodules.SkyfileFormatZip:
		archiveFunc = serveZip
		w.Header().Set("Content-Type", "application/zip")
		dst = w
	}

	// Get the files to archive.
	var files []skymodules.SkyfileSubfileMetadata
	for _, file := range md.Subfiles {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Offset < files[j].Offset
	})
	// If there are no files, it's a single file download. Manually construct a
	// SkyfileSubfileMetadata from the SkyfileMetadata.
	if len(files) == 0 {
		length := md.Length
		if md.Length == 0 {
			// Fetch the length of the file by seeking to the end and then back
			// to the start.
			seekLen, err := src.Seek(0, io.SeekEnd)
			if err != nil {
				return errors.AddContext(err, "serveArchive: failed to seek to end of skyfile")
			}

			// v150Compat a missing length is fine for legacy links but new
			// links should always have the length set.
			if build.Release == "testing" && seekLen != 0 {
				build.Critical("SkyfileMetadata is missing length")
			}

			// Seek back to the start
			_, err = src.Seek(0, io.SeekStart)
			if err != nil {
				return errors.AddContext(err, "serveArchive: failed to seek to start of skyfile")
			}
			length = uint64(seekLen)
		}
		// Construct the SkyfileSubfileMetadata.
		files = append(files, skymodules.SkyfileSubfileMetadata{
			FileMode: md.Mode,
			Filename: md.Filename,
			Offset:   0,
			Len:      length,
		})
	}
	err = archiveFunc(dst, src, files)
	return err
}

// serveTar is an archiveFunc that implements serving the files from src to dst
// as a tar.
func serveTar(dst io.Writer, src io.Reader, files []skymodules.SkyfileSubfileMetadata) error {
	tw := tar.NewWriter(dst)
	for _, file := range files {
		// Create header.
		header, err := tar.FileInfoHeader(file, file.Name())
		if err != nil {
			return err
		}
		// Modify name to match path within skyfile.
		header.Name = file.Filename
		// Write header.
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		// Write file content.
		if _, err := io.CopyN(tw, src, header.Size); err != nil {
			return err
		}
	}
	return tw.Close()
}

// serveZip is an archiveFunc that implements serving the files from src to dst
// as a zip.
func serveZip(dst io.Writer, src io.Reader, files []skymodules.SkyfileSubfileMetadata) error {
	zw := zip.NewWriter(dst)
	for _, file := range files {
		f, err := zw.Create(file.Filename)
		if err != nil {
			return errors.AddContext(err, "serveZip: failed to add the file to the zip")
		}

		// Write file content.
		_, err = io.CopyN(f, src, int64(file.Len))
		if err != nil {
			return errors.AddContext(err, "serveZip: failed to write file contents to the zip")
		}
	}
	return zw.Close()
}

// handleSkynetError is a handler that returns the correct status code for a
// given error returned by a skynet related method.
func handleSkynetError(w http.ResponseWriter, prefix string, err error) {
	httpErr := Error{fmt.Sprintf("%v: %v", prefix, err)}

	if errors.Contains(err, renter.ErrSkylinkBlocked) {
		WriteError(w, httpErr, http.StatusUnavailableForLegalReasons)
		return
	}
	if errors.Contains(err, renter.ErrRootNotFound) {
		WriteError(w, httpErr, http.StatusNotFound)
		return
	}
	if errors.Contains(err, renter.ErrRegistryEntryNotFound) {
		WriteError(w, httpErr, http.StatusNotFound)
		return
	}
	if errors.Contains(err, renter.ErrRegistryUpdateTimeout) {
		WriteError(w, httpErr, http.StatusRequestTimeout)
		return
	}
	if errors.Contains(err, renter.ErrRegistryLookupTimeout) {
		WriteError(w, httpErr, http.StatusNotFound)
		return
	}
	if errors.Contains(err, skymodules.ErrMalformedSkylink) {
		WriteError(w, httpErr, http.StatusBadRequest)
		return
	}
	if errors.Contains(err, renter.ErrInvalidSkylinkVersion) {
		WriteError(w, httpErr, http.StatusBadRequest)
		return
	}
	if err != nil {
		WriteError(w, httpErr, http.StatusInternalServerError)
		return
	}
}

// attachRegistryEntryProof takes a number of registry entries and parses them.
// The result is then attached to an API response for the client to verify the
// response against.
func attachRegistryEntryProof(w http.ResponseWriter, srvs []skymodules.RegistryEntry) error {
	proofChain := make([]RegistryHandlerGET, 0, len(srvs))
	for _, srv := range srvs {
		proofChain = append(proofChain, RegistryHandlerGET{
			Data:      hex.EncodeToString(srv.Data),
			DataKey:   srv.Tweak,
			Revision:  srv.Revision,
			PublicKey: srv.PubKey,
			Signature: hex.EncodeToString(srv.Signature[:]),
			Type:      srv.Type,
		})
	}
	// If the proof is empty, don't set the header.
	if len(proofChain) == 0 {
		return nil
	}
	// Otherwise marshal the header and attach it.
	b, err := json.Marshal(proofChain)
	if err != nil {
		return err
	}
	w.Header().Set(SkynetProofHeader, string(b))
	return nil
}

// UnmarshalErrorPages unmarshals an errorpages string into an map[int]string.
func UnmarshalErrorPages(s string) (map[int]string, error) {
	errPages := make(map[int]string)
	if len(s) == 0 {
		return errPages, nil
	}
	err := json.Unmarshal([]byte(s), &errPages)
	if err != nil {
		return nil, errors.AddContext(err, "invalid errorpages value")
	}
	return errPages, nil
}

// UnmarshalTryFiles unmarshals a tryfiles string.
func UnmarshalTryFiles(s string) ([]string, error) {
	if len(s) == 0 {
		return []string{}, nil
	}
	var tf []string
	err := json.Unmarshal([]byte(s), &tf)
	if err != nil {
		return nil, errors.AddContext(err, "invalid tryfiles value")
	}
	return tf, nil
}
