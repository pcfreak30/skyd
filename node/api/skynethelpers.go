package api

import (
	"archive/tar"
	"archive/zip"
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
	"gitlab.com/NebulousLabs/ratelimit"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skykey"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

type (
	// skyfileUploadParams is a helper struct that contains all of the query
	// string parameters on upload
	skyfileUploadParams struct {
		baseChunkRedundancy uint8
		defaultPath         string
		convertPath         string
		disableDefaultPath  bool
		dirResMode          string
		dirResNotFound      string
		dirResNotFoundCode  int
		dryRun              bool
		filename            string
		force               bool
		mode                os.FileMode
		monetization        *skymodules.Monetization
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

// monetizedResponseWriter is a wrapper for a response writer. It monetizes the
// returned bytes.
type monetizedResponseWriter struct {
	staticInner http.ResponseWriter
	staticW     io.Writer
}

// newMonetizedResponseWriter creates a new response writer wrapped with a
// monetized writer.
func newMonetizedResponseWriter(inner http.ResponseWriter, md skymodules.SkyfileMetadata, wallet modules.SiacoinSenderMulti, cr map[string]types.Currency, mb types.Currency) http.ResponseWriter {
	return &monetizedResponseWriter{
		staticInner: inner,
		staticW:     newMonetizedWriter(inner, md, wallet, cr, mb),
	}
}

// Header calls the inner writers Header method.
func (rw *monetizedResponseWriter) Header() http.Header {
	return rw.staticInner.Header()
}

// WriteHeader calls the inner writers WriteHeader method.
func (rw *monetizedResponseWriter) WriteHeader(statusCode int) {
	rw.staticInner.WriteHeader(statusCode)
}

// Write writes to the underlying monetized writer.
func (rw *monetizedResponseWriter) Write(b []byte) (int, error) {
	return rw.staticW.Write(b)
}

// monetizedWriter is a wrapper for an io.Writer. It monetizes the returned
// bytes.
type monetizedWriter struct {
	staticW      io.Writer
	staticMD     skymodules.SkyfileMetadata
	staticWallet modules.SiacoinSenderMulti

	staticConversionRates  map[string]types.Currency
	staticMonetizationBase types.Currency

	// count is used for sanity checking the number of monetized bytes against
	// the total.
	count int
}

// newMonetizedWriter creates a new wrapped writer.
func newMonetizedWriter(w io.Writer, md skymodules.SkyfileMetadata, wallet modules.SiacoinSenderMulti, cr map[string]types.Currency, mb types.Currency) io.Writer {
	// Ratelimit the writer.
	rl := ratelimit.NewRateLimit(0, 0, 0)
	return &monetizedWriter{
		staticW:                ratelimit.NewRLReadWriter(&writeReader{Writer: w}, rl, make(chan struct{})),
		staticMD:               md,
		staticWallet:           wallet,
		staticConversionRates:  cr,
		staticMonetizationBase: mb,
	}
}

// Write wraps the inner Write and adds monetization.
func (rw *monetizedWriter) Write(b []byte) (int, error) {
	// Handle legacy uploads with length 0 by passing it through to the inner
	// writer.
	if rw.staticMD.Length == 0 && rw.staticMD.Monetization == nil {
		return rw.staticW.Write(b)
	}

	// Sanity check the number of monetized bytes against the total.
	rw.count += len(b)
	if rw.count > int(rw.staticMD.Length) {
		err := fmt.Errorf("monetized more data than the total data of the skylink: %v > %v", rw.count, rw.staticMD.Length)
		build.Critical(err)
		return 0, err
	}

	// Forward data to inner.
	// TODO: instead of directly writing to the ratelimited writer, write to a
	// not ratelimited buffer on disk which forwards the data to the writer.
	// Otherwise we are starving the renter.
	n, err := rw.staticW.Write(b)
	if err != nil {
		return n, err
	}

	// Pay monetizers.
	if build.Release == "testing" {
		err := skymodules.PayMonetizers(rw.staticWallet, rw.staticMD.Monetization, uint64(len(b)), rw.staticMD.Length, rw.staticConversionRates, rw.staticMonetizationBase)
		if err != nil {
			return 0, err
		}
	}
	return n, nil
}

// buildETag is a helper function that returns an ETag.
func buildETag(skylink skymodules.Skylink, method, path string, format skymodules.SkyfileFormat) string {
	return crypto.HashAll(
		skylink.String(),
		method,
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

	timeoutInt, err := strconv.Atoi(timeoutStr)
	if err != nil {
		return 0, errors.AddContext(err, "unable to parse 'timeout'")
	}
	if timeoutInt > MaxSkynetRequestTimeout {
		return 0, errors.AddContext(err, fmt.Sprintf("'timeout' parameter too high, maximum allowed timeout is %ds", MaxSkynetRequestTimeout))
	}
	return time.Duration(timeoutInt) * time.Second, nil
}

// parseUploadHeadersAndRequestParameters is a helper function that parses all
// of the query parameters and headers from an upload request
func parseUploadHeadersAndRequestParameters(req *http.Request, ps httprouter.Params) (*skyfileUploadHeaders, *skyfileUploadParams, error) {
	var err error

	// parse 'Skynet-Disable-Force' request header
	var disableForce bool
	strDisableForce := req.Header.Get("Skynet-Disable-Force")
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

	// parse `dirresmode` query parameter
	dirResMode := strings.ToLower(queryForm.Get("dirresmode"))
	if dirResMode == "" {
		dirResMode = skymodules.DirResModeStandard
	}
	if dirResMode != skymodules.DirResModeStandard && dirResMode != skymodules.DirResModeWeb {
		return nil, nil, errors.AddContext(skymodules.ErrInvalidDirectoryResolution, "invalid dirresmode value")
	}

	// parse 'dirresnotfound' query parameter
	dirResNotFound := queryForm.Get("dirresnotfound")
	if dirResNotFound != "" {
		dirResNotFound = skymodules.EnsurePrefix(dirResNotFound, "/")
	}

	// parse 'dirresnotfoundcode' query parameter
	dirResNotFoundCode := http.StatusNotFound
	dirResNotFoundCodeStr := queryForm.Get("dirresnotfoundcode")
	if dirResNotFoundCodeStr != "" {
		dirResNotFoundCode, err = strconv.Atoi(dirResNotFoundCodeStr)
		if err != nil {
			return nil, nil, errors.AddContext(err, "unable to parse 'dirresnotfoundcode' parameter")
		}
	}

	// verify that we're not trying to override 404 page and code in standard
	// mode
	if dirResMode == skymodules.DirResModeStandard && (dirResNotFound != "" || dirResNotFoundCode != http.StatusNotFound) {
		return nil, nil, skymodules.ErrInvalidDirectoryResolutionMode
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

	// parse 'monetization'.
	var monetization *skymodules.Monetization
	monetizationStr := queryForm.Get("monetization")
	if monetizationStr != "" {
		var m skymodules.Monetization
		err = json.Unmarshal([]byte(monetizationStr), &m)
		if err != nil {
			return nil, nil, errors.AddContext(err, "unable to parse 'monetizers'")
		}
		if err := skymodules.ValidateMonetization(&m); err != nil {
			return nil, nil, err
		}
		monetization = &m
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

	// verify default path params are not set if it's not a multipart upload
	if !isMultipartRequest(mediaType) && dirResMode == skymodules.DirResModeWeb {
		return nil, nil, errors.New("DirResMode 'web' can only be set on multipart uploads")
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
		dirResMode:          dirResMode,
		dirResNotFound:      dirResNotFound,
		dirResNotFoundCode:  dirResNotFoundCode,
		dryRun:              dryRun,
		filename:            filename,
		force:               force,
		mode:                mode,
		monetization:        monetization,
		root:                root,
		siaPath:             siaPath,
		skyKeyID:            skykeyID,
		skyKeyName:          skykeyName,
	}
	return headers, params, nil
}

// serveArchive serves skyfiles as an archive by reading them from r and writing
// the archive to dst using the given archiveFunc.
func serveArchive(dst io.Writer, src io.ReadSeeker, md skymodules.SkyfileMetadata, archiveFunc archiveFunc, monetize func(io.Writer) io.Writer) error {
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
			// v150Compat a missing length is fine for legacy links but new
			// links should always have the length set.
			if build.Release == "testing" {
				build.Critical("SkyfileMetadata is missing length")
			}
			// Fetch the length of the file by seeking to the end and then back
			// to the start.
			seekLen, err := src.Seek(0, io.SeekEnd)
			if err != nil {
				return errors.AddContext(err, "serveArchive: failed to seek to end of skyfile")
			}
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
	return archiveFunc(dst, src, files, monetize)
}

// serveTar is an archiveFunc that implements serving the files from src to dst
// as a tar.
func serveTar(dst io.Writer, src io.Reader, files []skymodules.SkyfileSubfileMetadata, monetize func(io.Writer) io.Writer) error {
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
		if _, err := io.CopyN(monetize(tw), src, header.Size); err != nil {
			return err
		}
	}
	return tw.Close()
}

// serveZip is an archiveFunc that implements serving the files from src to dst
// as a zip.
func serveZip(dst io.Writer, src io.Reader, files []skymodules.SkyfileSubfileMetadata, monetize func(io.Writer) io.Writer) error {
	zw := zip.NewWriter(dst)
	for _, file := range files {
		f, err := zw.Create(file.Filename)
		if err != nil {
			return errors.AddContext(err, "serveZip: failed to add the file to the zip")
		}

		// Write file content.
		_, err = io.CopyN(monetize(f), src, int64(file.Len))
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
	b, err := json.Marshal(proofChain)
	if err != nil {
		return err
	}
	w.Header().Set("Proof", string(b))
	return nil
}
