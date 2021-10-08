package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skykey"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/skynetportals"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

const (
	// DefaultSkynetRequestTimeout is the default request timeout for routes
	// that have a timeout query string parameter. If the request can not be
	// resolved within the given amount of time, it times out. This is used for
	// Skynet routes where a request times out if the DownloadByRoot project
	// does not finish in due time.
	DefaultSkynetRequestTimeout = 30 * time.Second

	// MaxSkynetRequestTimeout is the maximum a user is allowed to set as
	// request timeout. This to prevent an attack vector where the attacker
	// could cause a go-routine leak by creating a bunch of requests with very
	// high timeouts.
	MaxSkynetRequestTimeout = 15 * time.Minute

	// SkynetDisableForceHeader allows disabling the force-update feature.
	SkynetDisableForceHeader = "Skynet-Disable-Force"

	// SkynetFileLayoutHeader holds the layout of this skyfile.
	SkynetFileLayoutHeader = "Skynet-File-Layout"

	// SkynetFileMetadataHeader holds an encoded JSON object with the metadata
	// of the skyfile *or* the subdirectory of the skyfile that has been
	// requested.
	SkynetFileMetadataHeader = "Skynet-File-Metadata"

	// SkynetProofHeader holds an encoded JSON object with the registry proofs
	// for this skylink.
	SkynetProofHeader = "Skynet-Proof"

	// SkynetSkylinkHeader is a string representation of the base64 encoded
	// v1 Skylink that was served.
	SkynetSkylinkHeader = "Skynet-Skylink"

	// SkynetRequestedSkylinkHeader is a string representation of the base64 encoded
	// Skylink that was requested.
	SkynetRequestedSkylinkHeader = "Skynet-Requested-Skylink"
)

var (
	// DefaultSkynetPricePerMS is the default price per millisecond the renter
	// is able to spend on faster workers when downloading a Skyfile. By default
	// this is a sane default of 100 nS.
	DefaultSkynetPricePerMS = types.SiacoinPrecision.MulFloat(1e-7) // 100 nS
)

type (
	// HostsForRegistryUpdateGET is the response that the api returns after
	// a request to /skynet/registry/hosts.
	HostsForRegistryUpdateGET struct {
		Hosts []skymodules.HostForRegistryUpdate `json:"hosts"`
	}

	// SkynetSkyfileHandlerPOST is the response that the api returns after the
	// /skynet/ POST endpoint has been used.
	SkynetSkyfileHandlerPOST struct {
		Skylink    string      `json:"skylink"`
		MerkleRoot crypto.Hash `json:"merkleroot"`
		Bitfield   uint16      `json:"bitfield"`
	}

	// SkynetBlocklistGET contains the information queried for the
	// /skynet/blocklist GET endpoint
	//
	// NOTE: With v1.5.0 the return value for the Blocklist changed. Pre v1.5.0
	// the []crypto.Hash was a slice of MerkleRoots. Post v1.5.0 the []crypto.Hash
	// is a slice of the Hashes of the MerkleRoots
	SkynetBlocklistGET struct {
		Blacklist []crypto.Hash `json:"blacklist"` // Deprecated, kept for backwards compatibility
		Blocklist []crypto.Hash `json:"blocklist"`
	}

	// SkynetBlocklistPOST contains the information needed for the
	// /skynet/blocklist POST endpoint to be called
	SkynetBlocklistPOST struct {
		Add    []string `json:"add"`
		Remove []string `json:"remove"`

		// IsHash indicates if the supplied Add and Remove strings are already
		// hashes of Skylinks
		IsHash bool `json:"ishash"`
	}

	// SkynetPortalsGET contains the information queried for the /skynet/portals
	// GET endpoint.
	SkynetPortalsGET struct {
		Portals []skymodules.SkynetPortal `json:"portals"`
	}

	// SkynetPortalsPOST contains the information needed for the /skynet/portals
	// POST endpoint to be called.
	SkynetPortalsPOST struct {
		Add    []skymodules.SkynetPortal `json:"add"`
		Remove []modules.NetAddress      `json:"remove"`
	}

	// SkynetRestorePOST is the response that the api returns after the
	// /skynet/restore POST endpoint has been used.
	SkynetRestorePOST struct {
		Skylink string `json:"skylink"`
	}

	// SkynetStatsGET contains the information queried for the /skynet/stats
	// GET endpoint
	SkynetStatsGET struct {
		// Base Sector Upload Stats
		BaseSectorUpload15mDataPoints float64 `json:"basesectorupload15mdatapoints"`
		BaseSectorUpload15mP99ms      float64 `json:"basesectorupload15mp99ms"`
		BaseSectorUpload15mP999ms     float64 `json:"basesectorupload15mp999ms"`
		BaseSectorUpload15mP9999ms    float64 `json:"basesectorupload15mp9999ms"`

		// Sector Download Stats
		BaseSectorOverdrivePct   float64 `json:"basesectoroverdrivepct"`
		BaseSectorOverdriveAvg   float64 `json:"basesectoroverdriveavg"`
		FanoutSectorOverdrivePct float64 `json:"fanoutsectoroverdrivepct"`
		FanoutSectorOverdriveAvg float64 `json:"fanoutsectoroverdriveavg"`

		// Chunk Upload Stats
		ChunkUpload15mDataPoints float64 `json:"chunkupload15mdatapoints"`
		ChunkUpload15mP99ms      float64 `json:"chunkupload15mp99ms"`
		ChunkUpload15mP999ms     float64 `json:"chunkupload15mp999ms"`
		ChunkUpload15mP9999ms    float64 `json:"chunkupload15mp9999ms"`

		// Registry performance stats, unit is given in milliseconds.
		RegistryRead15mDataPoints float64 `json:"registryread15mdatapoints"`
		RegistryRead15mP99ms      float64 `json:"registryread15mp99ms"`
		RegistryRead15mP999ms     float64 `json:"registryread15mp999ms"`
		RegistryRead15mP9999ms    float64 `json:"registryread15mp9999ms"`

		// Registry performance stats, unit is given in milliseconds.
		RegistryWrite15mDataPoints float64 `json:"registrywrite15mdatapoints"`
		RegistryWrite15mP99ms      float64 `json:"registrywrite15mp99ms"`
		RegistryWrite15mP999ms     float64 `json:"registrywrite15mp999ms"`
		RegistryWrite15mP9999ms    float64 `json:"registrywrite15mp9999ms"`

		// Stream Buffer Download Stats
		StreamBufferRead15mDataPoints float64 `json:"streambufferread15mdatapoints"`
		StreamBufferRead15mP99ms      float64 `json:"streambufferread15mp99ms"`
		StreamBufferRead15mP999ms     float64 `json:"streambufferread15mp999ms"`
		StreamBufferRead15mP9999ms    float64 `json:"streambufferread15mp9999ms"`

		// The amount of computational time that it takes the health loop to
		// scan the entire filesystem. Unit is given in hours.
		SystemHealthScanDurationHours float64 `json:"systemhealthscandurationhours"`

		// General Statuses
		AllowanceStatus string         `json:"allowancestatus"` // 'low', 'good', 'high'
		ContractStorage uint64         `json:"contractstorage"` // bytes
		MaxStoragePrice types.Currency `json:"maxstorageprice"` // Hastings per byte per block
		NumCritAlerts   int            `json:"numcritalerts"`
		NumFiles        uint64         `json:"numfiles"`
		PortalMode      bool           `json:"portalmode"`
		Repair          uint64         `json:"repair"`  // bytes
		Storage         uint64         `json:"storage"` // bytes
		StuckChunks     uint64         `json:"stuckchunks"`
		WalletStatus    string         `json:"walletstatus"` // 'low', 'good', 'high'

		// Update and version information.
		Uptime      int64         `json:"uptime"`
		VersionInfo SkynetVersion `json:"versioninfo"`
	}

	// SkynetVersion contains version information
	SkynetVersion struct {
		Version     string `json:"version"`
		GitRevision string `json:"gitrevision"`
	}

	// SkykeyGET contains a base64 encoded Skykey.
	SkykeyGET struct {
		Skykey string `json:"skykey"` // base64 encoded Skykey
		Name   string `json:"name"`
		ID     string `json:"id"`   // base64 encoded Skykey ID
		Type   string `json:"type"` // human-readable Skykey Type
	}

	// SkykeysGET contains a slice of Skykeys.
	SkykeysGET struct {
		Skykeys []SkykeyGET `json:"skykeys"`
	}

	// RegistryHandlerGET is the response returned by the registryHandlerGET
	// handler.
	RegistryHandlerGET struct {
		Data      string                    `json:"data"`
		Revision  uint64                    `json:"revision"`
		DataKey   crypto.Hash               `json:"datakey"`
		PublicKey types.SiaPublicKey        `json:"publickey"`
		Signature string                    `json:"signature"`
		Type      modules.RegistryEntryType `json:"type"`
	}

	// RegistryHandlerRequestPOST is the expected format of the json request for
	// /skynet/registry [POST].
	RegistryHandlerRequestPOST struct {
		PublicKey types.SiaPublicKey        `json:"publickey"`
		DataKey   crypto.Hash               `json:"datakey"`
		Revision  uint64                    `json:"revision"`
		Signature crypto.Signature          `json:"signature"`
		Data      []byte                    `json:"data"`
		Type      modules.RegistryEntryType `json:"type"`
	}

	// RegistryHandlerMultiRequestPOST is the expected format of the json request for
	// /skynet/registry [POST].
	RegistryHandlerMultiRequestPOST struct {
		RegistryHandlerRequestPOST
		HostKey types.SiaPublicKey `json:"hostkey"`
	}

	// SkylinkResolveGET is the response returned by the /skylink/resolve
	// endpoint.
	SkylinkResolveGET struct {
		Skylink string `json:"skylink"`
	}

	// archiveFunc is a function that serves subfiles from src to dst and
	// archives them using a certain algorithm.
	archiveFunc func(dst io.Writer, src io.Reader, files []skymodules.SkyfileSubfileMetadata) error
)

// skynetBaseSectorHandlerGET accepts a skylink as input and will return the
// encoded basesector.
func (api *API) skynetBaseSectorHandlerGET(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Parse the skylink from the raw URL of the request. Any special characters
	// in the raw URL are encoded, allowing us to differentiate e.g. the '?'
	// that begins query parameters from the encoded version '%3F'.
	skylink, _, _, err := parseSkylinkURL(req.URL.String(), "/skynet/basesector/")
	if err != nil {
		WriteError(w, Error{fmt.Sprintf("error parsing skylink: %v", err)}, http.StatusBadRequest)
		return
	}

	// Parse the query params.
	queryForm, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		WriteError(w, Error{"failed to parse query params"}, http.StatusBadRequest)
		return
	}

	// Parse the timeout.
	timeout, err := parseTimeout(queryForm)
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}

	// Parse pricePerMS.
	pricePerMS := DefaultSkynetPricePerMS
	pricePerMSStr := queryForm.Get("priceperms")
	if pricePerMSStr != "" {
		_, err = fmt.Sscan(pricePerMSStr, &pricePerMS)
		if err != nil {
			WriteError(w, Error{"unable to parse 'pricePerMS' parameter: " + err.Error()}, http.StatusBadRequest)
			return
		}
	}

	// Fetch the skyfile's streamer to serve the basesector of the file
	streamer, srvs, _, err := api.renter.DownloadSkylinkBaseSector(skylink, timeout, pricePerMS)
	if err != nil {
		handleSkynetError(w, "failed to fetch base sector", err)
		return
	}
	defer func() {
		// At this point we have already responded so we can't write a potential
		// error here.
		_ = streamer.Close()
	}()

	// Attach proof.
	err = attachRegistryEntryProof(w, srvs)
	if err != nil {
		WriteError(w, Error{"unable to attach proof: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	// Serve the basesector
	http.ServeContent(w, req, "", time.Time{}, streamer)
	return
}

// skynetBlocklistHandlerGET handles the API call to get the list of blocked
// skylinks.
func (api *API) skynetBlocklistHandlerGET(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	// Get the Blocklist
	blocklist, err := api.renter.Blocklist()
	if err != nil {
		WriteError(w, Error{"unable to get the blocklist: " + err.Error()}, http.StatusBadRequest)
		return
	}

	WriteJSON(w, SkynetBlocklistGET{
		Blocklist: blocklist,
	})
}

// skynetBlocklistHandlerPOST handles the API call to block certain skylinks.
func (api *API) skynetBlocklistHandlerPOST(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Parse the query params.
	queryForm, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		WriteError(w, Error{"failed to parse query params"}, http.StatusBadRequest)
		return
	}

	// Parse parameters
	var params SkynetBlocklistPOST
	err = json.NewDecoder(req.Body).Decode(&params)
	if err != nil {
		WriteError(w, Error{"invalid parameters: " + err.Error()}, http.StatusBadRequest)
		return
	}

	// Check for nil input
	if len(append(params.Add, params.Remove...)) == 0 {
		WriteError(w, Error{"no skylinks submitted"}, http.StatusBadRequest)
		return
	}

	// Parse the timeout.
	timeout, err := parseTimeout(queryForm)
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}

	// Generate context
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()

	// Update the Skynet Blocklist
	err = api.renter.UpdateSkynetBlocklist(ctx, params.Add, params.Remove, params.IsHash)
	if err != nil {
		WriteError(w, Error{"unable to update the skynet blocklist: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	WriteSuccess(w)
}

// skynetPortalsHandlerGET handles the API call to get the list of known skynet
// portals.
func (api *API) skynetPortalsHandlerGET(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	// Get the list of portals.
	portals, err := api.renter.Portals()
	if err != nil {
		WriteError(w, Error{"unable to get the portals list: " + err.Error()}, http.StatusBadRequest)
		return
	}

	WriteJSON(w, SkynetPortalsGET{
		Portals: portals,
	})
}

// skynetPortalsHandlerPOST handles the API call to add and remove portals from
// the list of known skynet portals.
func (api *API) skynetPortalsHandlerPOST(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Parse parameters.
	var params SkynetPortalsPOST
	err := json.NewDecoder(req.Body).Decode(&params)
	if err != nil {
		WriteError(w, Error{"invalid parameters: " + err.Error()}, http.StatusBadRequest)
		return
	}

	// Update the list of known skynet portals.
	err = api.renter.UpdateSkynetPortals(params.Add, params.Remove)
	if err != nil {
		// If validation fails, return a bad request status.
		errStatus := http.StatusInternalServerError
		if strings.Contains(err.Error(), skynetportals.ErrSkynetPortalsValidation.Error()) {
			errStatus = http.StatusBadRequest
		}
		WriteError(w, Error{"unable to update the list of known skynet portals: " + err.Error()}, errStatus)
		return
	}

	WriteSuccess(w)
}

// skynetRootHandlerGET handles the api call for a download by root request.
// This call returns the encoded sector.
func (api *API) skynetRootHandlerGET(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Parse the query params.
	queryForm, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		WriteError(w, Error{"failed to parse query params"}, http.StatusBadRequest)
		return
	}

	// Parse the timeout.
	timeout, err := parseTimeout(queryForm)
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}

	// Parse the root.
	rootStr := queryForm.Get("root")
	if rootStr == "" {
		WriteError(w, Error{"no root hash provided"}, http.StatusBadRequest)
		return
	}
	var root crypto.Hash
	err = root.LoadString(rootStr)
	if err != nil {
		WriteError(w, Error{"unable to parse 'root' parameter: " + err.Error()}, http.StatusBadRequest)
		return
	}

	// Parse the offset.
	offsetStr := queryForm.Get("offset")
	if offsetStr == "" {
		WriteError(w, Error{"no offset provided"}, http.StatusBadRequest)
		return
	}
	offset, err := strconv.ParseUint(offsetStr, 10, 64)
	if err != nil {
		WriteError(w, Error{"unable to parse 'offset' parameter: " + err.Error()}, http.StatusBadRequest)
		return
	}

	// Parse the length.
	lengthStr := queryForm.Get("length")
	if lengthStr == "" {
		WriteError(w, Error{"no length provided"}, http.StatusBadRequest)
		return
	}
	length, err := strconv.ParseUint(lengthStr, 10, 64)
	if err != nil {
		WriteError(w, Error{"unable to parse 'length' parameter: " + err.Error()}, http.StatusBadRequest)
		return
	}

	// Parse pricePerMS.
	pricePerMS := DefaultSkynetPricePerMS
	pricePerMSStr := queryForm.Get("priceperms")
	if pricePerMSStr != "" {
		_, err = fmt.Sscan(pricePerMSStr, &pricePerMS)
		if err != nil {
			WriteError(w, Error{"unable to parse 'pricePerMS' parameter: " + err.Error()}, http.StatusBadRequest)
			return
		}
	}

	// Fetch the skyfile's  streamer to serve the basesector of the file
	sector, err := api.renter.DownloadByRoot(root, offset, length, timeout, pricePerMS)
	if err != nil {
		handleSkynetError(w, "failed to fetch root", err)
		return
	}

	streamer := renter.StreamerFromSlice(sector)
	defer func() {
		// At this point we have already responded so we can't write a potential
		// error here.
		_ = streamer.Close()
	}()

	// Serve the basesector
	http.ServeContent(w, req, "", time.Time{}, streamer)
	return
}

// skynetSkylinkHandlerGET accepts a skylink as input and will stream the data
// from the skylink out of the response body as output.
func (api *API) skynetSkylinkHandlerGET(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Parse the request parameters
	params, err := parseDownloadRequestParameters(req)
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}
	path := params.path
	format := params.format

	// Fetch the skyfile's metadata and a streamer to download the file
	streamer, srvs, err := api.renter.DownloadSkylink(params.skylink, params.timeout, params.pricePerMS)
	if err != nil {
		handleSkynetError(w, "failed to fetch skylink", err)
		return
	}
	defer func() {
		// At this point we have already responded so we can't write a potential
		// error here.
		_ = streamer.Close()
	}()

	metadata := streamer.Metadata()
	ew := newCustomErrorWriter(metadata, streamer)

	// Attach proof.
	err = attachRegistryEntryProof(w, srvs)
	if err != nil {
		ew.WriteError(w, Error{"unable to attach proof: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	// Only validate default path and tryfiles if the format is not specified,
	// this way the file can still be downloaded should it have been uploaded
	// with incorrect metadata, which is possible seeing as it may have been
	// uploaded by a private portal.
	if format == skymodules.SkyfileFormatNotSpecified {
		// The path we actually want to serve based on defaultpath and tryfiles.
		servePath := metadata.ServePath(path)
		isMulti := len(metadata.Subfiles) > 1
		// If we don't have a subPath and the skylink doesn't end with a
		// trailing slash we need to redirect in order to add the trailing
		// slash. This is only true for skapps - they need it in order to
		// properly work with relative paths. We also don't need to redirect if
		// this is a HEAD request or if it's a download as attachment.
		if isMulti && path == "/" && servePath != path && !params.attachment && req.Method == http.MethodGet && !strings.HasSuffix(params.skylinkStringNoQuery, "/") {
			location := params.skylinkStringNoQuery + "/"
			if req.URL.RawQuery != "" {
				location += "?" + req.URL.RawQuery
			}
			w.Header().Set("Location", location)
			w.WriteHeader(http.StatusPermanentRedirect)
			return
		}
		path = servePath
	}
	var isSubfile bool
	// Serve the contents of the skyfile at path if one is set
	if path != "/" {
		metadataForPath, isFile, offset, size := metadata.ForPath(path)
		if len(metadataForPath.Subfiles) == 0 {
			ew.WriteError(w, Error{fmt.Sprintf("failed to download contents for path: %v", path)}, http.StatusNotFound)
			return
		}
		// NOTE: we don't have an actual raw metadata for the subpath. So we are
		// marshaling the temporary metadata. This should be good enough since
		// the metadata can't be used to create a skylink anyway.
		rawMetadataForPath, err := json.Marshal(metadataForPath)
		if err != nil {
			ew.WriteError(w, Error{fmt.Sprintf("failed to marshal subfile metadata for path %v", path)}, http.StatusNotFound)
			return
		}
		streamer, err = NewLimitStreamer(streamer, metadataForPath, rawMetadataForPath, streamer.Skylink(), streamer.Layout(), offset, size)
		if err != nil {
			ew.WriteError(w, Error{fmt.Sprintf("failed to download contents for path: %v, could not create limit streamer", path)}, http.StatusInternalServerError)
			return
		}

		isSubfile = isFile
		metadata = metadataForPath
	}
	// If we are serving more than one file, and the format is not
	// specified, default to downloading it as a zip archive.
	if !isSubfile && metadata.IsDirectory() && format == skymodules.SkyfileFormatNotSpecified {
		format = skymodules.SkyfileFormatZip
	}

	// Encode the Layout
	encLayout := streamer.Layout().Encode()

	// Set the common Header fields
	//
	// Set the Skylink response header
	if !streamer.Skylink().IsSkylinkV1() {
		build.Critical("skylink attached in skynet-skylink header is not v1")
	}
	w.Header().Set(SkynetSkylinkHeader, streamer.Skylink().String())
	w.Header().Set(SkynetRequestedSkylinkHeader, params.skylink.String())

	// Set the ETag response header
	//
	// NOTE: we use the Skylink returned by the streamer to build the ETag with,
	// this is very important as the incoming skylink might have been a V2
	// skylink that got resolved to a v1 skylink. We don't want to build the
	// ETag on the V2 skylink as that is constant, even though the data might
	// change.
	eTag := buildETag(streamer.Skylink(), path, format)
	w.Header().Set("ETag", fmt.Sprintf("\"%v\"", eTag))

	// Set the Layout
	if params.includeLayout {
		w.Header().Set(SkynetFileLayoutHeader, hex.EncodeToString(encLayout))
	}

	// Set an appropriate Content-Disposition header
	var cdh string
	filename := filepath.Base(metadata.Filename)
	if format.IsArchive() {
		cdh = fmt.Sprintf("attachment; filename=%s", strconv.Quote(filename+format.Extension()))
	} else if params.attachment {
		cdh = fmt.Sprintf("attachment; filename=%s", strconv.Quote(filename))
	} else {
		cdh = fmt.Sprintf("inline; filename=%s", strconv.Quote(filename))
	}
	w.Header().Set("Content-Disposition", cdh)

	// If requested, serve the content as a tar archive, compressed tar
	// archive or zip archive.
	if format.IsArchive() {
		err = serveArchive(w, streamer, format, metadata)
		if err != nil {
			ew.WriteError(w, Error{fmt.Sprintf("failed to serve skyfile as %v archive: %v", format, err)}, http.StatusInternalServerError)
		}
		return
	}

	// Only set the Content-Type header when the metadata defines one, if we
	// were to set the header to an empty string, it would prevent the http
	// library from sniffing the file's content type.
	if metadata.ContentType() != "" {
		w.Header().Set("Content-Type", metadata.ContentType())
	}
	http.ServeContent(w, req, metadata.Filename, time.Time{}, streamer)
}

// skynetSkylinkPinHandlerPOST will pin a skylink to this Sia node, ensuring
// uptime even if the original uploader stops paying for the file.
func (api *API) skynetSkylinkPinHandlerPOST(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	// Parse the query params.
	queryForm, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		WriteError(w, Error{"failed to parse query params"}, http.StatusBadRequest)
		return
	}

	strLink := ps.ByName("skylink")
	var skylink skymodules.Skylink
	err = skylink.LoadString(strLink)
	if err != nil {
		WriteError(w, Error{fmt.Sprintf("error parsing skylink: %v", err)}, http.StatusBadRequest)
		return
	}

	// Parse whether the siapath should be from root or from the skynet folder.
	var root bool
	rootStr := queryForm.Get("root")
	if rootStr != "" {
		root, err = strconv.ParseBool(rootStr)
		if err != nil {
			WriteError(w, Error{"unable to parse 'root' parameter: " + err.Error()}, http.StatusBadRequest)
			return
		}
	}

	// Parse out the intended siapath.
	var siaPath skymodules.SiaPath
	siaPathStr := queryForm.Get("siapath")
	if root {
		siaPath, err = skymodules.NewSiaPath(siaPathStr)
	} else {
		siaPath, err = skymodules.SkynetFolder.Join(siaPathStr)
	}
	if err != nil {
		WriteError(w, Error{"invalid siapath provided: " + err.Error()}, http.StatusBadRequest)
		return
	}

	// Parse the timeout.
	timeout, err := parseTimeout(queryForm)
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}

	// Parse pricePerMS.
	pricePerMS := DefaultSkynetPricePerMS
	pricePerMSStr := queryForm.Get("priceperms")
	if pricePerMSStr != "" {
		_, err = fmt.Sscan(pricePerMSStr, &pricePerMS)
		if err != nil {
			WriteError(w, Error{"unable to parse 'pricePerMS' parameter: " + err.Error()}, http.StatusBadRequest)
			return
		}
	}

	// Check whether force upload is allowed. Skynet portals might disallow
	// passing the force flag, if they want to they can set overrule the force
	// flag by passing in the 'Skynet-Disable-Force' header
	allowForce := true
	strDisableForce := req.Header.Get(SkynetDisableForceHeader)
	if strDisableForce != "" {
		disableForce, err := strconv.ParseBool(strDisableForce)
		if err != nil {
			WriteError(w, Error{"unable to parse 'Skynet-Disable-Force' header: " + err.Error()}, http.StatusBadRequest)
			return
		}
		allowForce = !disableForce
	}

	// Check whether existing file should be overwritten
	force := false
	if strForce := queryForm.Get("force"); strForce != "" {
		force, err = strconv.ParseBool(strForce)
		if err != nil {
			WriteError(w, Error{"unable to parse 'force' parameter: " + err.Error()}, http.StatusBadRequest)
			return
		}
	}

	// Notify the caller force has been disabled
	if !allowForce && force {
		WriteError(w, Error{"'force' has been disabled on this node: " + err.Error()}, http.StatusBadRequest)
		return
	}

	// Check whether the redundancy has been set.
	redundancy := uint8(0)
	if rStr := queryForm.Get("basechunkredundancy"); rStr != "" {
		if _, err := fmt.Sscan(rStr, &redundancy); err != nil {
			WriteError(w, Error{"unable to parse basechunkredundancy: " + err.Error()}, http.StatusBadRequest)
			return
		}
	}

	// Create the upload parameters. Notably, the fanout redundancy, the file
	// metadata and the filename are not included. Changing those would change
	// the skylink, which is not the goal.
	lup := skymodules.SkyfileUploadParameters{
		SiaPath:             siaPath,
		Force:               force,
		BaseChunkRedundancy: redundancy,
	}

	err = api.renter.PinSkylink(skylink, lup, timeout, pricePerMS)
	if err != nil {
		handleSkynetError(w, "failed to pin file to skynet", err)
		return
	}
	w.Header().Set(SkynetSkylinkHeader, skylink.String())
	WriteSuccess(w)
}

// skynetTUSUploadSkylinkGET is the handler for the /skynet/tus/skylink/:id
// endpoint.
func (api *API) skynetTUSUploadSkylinkGET(w http.ResponseWriter, _ *http.Request, ps httprouter.Params) {
	// Get id from path.
	id := ps.ByName("id")

	// Get the uploader from the renter.
	tusUploader := api.renter.SkynetTUSUploader()

	// Fetch the skylink.
	skylink, found := tusUploader.Skylink(id)
	if !found {
		WriteError(w, Error{"failed to fetch skylink for upload"}, http.StatusNotFound)
		return
	}

	// Set the Skylink response header
	w.Header().Set(SkynetSkylinkHeader, skylink.String())

	// Respond with the skylink in the body as well.
	WriteJSON(w, SkynetSkyfileHandlerPOST{
		Bitfield:   skylink.Bitfield(),
		MerkleRoot: skylink.MerkleRoot(),
		Skylink:    skylink.String(),
	})
}

// skynetSkyfileHandlerPOST is a dual purpose endpoint. If the 'convertpath'
// field is set, this endpoint will create a skyfile using an existing siafile.
// The original siafile and the skyfile will both need to be kept in order for
// the file to remain available on Skynet. If the 'convertpath' field is not
// set, this is essentially an upload streaming endpoint for Skynet which
// returns a skylink.
func (api *API) skynetSkyfileHandlerPOST(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	// parse the request headers and parameters
	headers, params, err := parseUploadHeadersAndRequestParameters(req, ps)
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}

	// build the upload parameters
	sup := skymodules.SkyfileUploadParameters{
		BaseChunkRedundancy: params.baseChunkRedundancy,
		DryRun:              params.dryRun,
		Force:               params.force,
		SiaPath:             params.siaPath,

		// Set filename and mode
		Filename: params.filename,
		Mode:     params.mode,

		// Set the default path params
		DefaultPath:        params.defaultPath,
		DisableDefaultPath: params.disableDefaultPath,

		// Set encryption key details
		SkykeyName: params.skyKeyName,
		SkykeyID:   params.skyKeyID,

		TryFiles:   params.tryFiles,
		ErrorPages: params.errorPages,
	}

	// set the reader
	var reader skymodules.SkyfileUploadReader
	if isMultipartRequest(headers.mediaType) {
		reader, err = skymodules.NewSkyfileMultipartReaderFromRequest(req, sup)
	} else {
		reader = skymodules.NewSkyfileReader(req.Body, sup)
	}
	if err != nil {
		WriteError(w, Error{fmt.Sprintf("unable to create multipart reader: %v", err)}, http.StatusBadRequest)
		return
	}

	// Check whether this is a streaming upload or a siafile conversion. If no
	// convert path is provided, assume that the req.Body will be used as a
	// streaming upload.
	if params.convertPath == "" {
		skylink, err := api.renter.UploadSkyfile(req.Context(), sup, reader)
		if err != nil {
			handleSkynetError(w, "failed to upload file to skynet", err)
			return
		}

		// Determine whether the file is large or not, and update the
		// appropriate bucket.
		//
		// The way we have to count is a bit gross, because there are two files
		// that we need to consider when looking for the size of the final
		// upload. The first is the siapath, and then the second is the siapath
		// of the extended file, which needs to be separated out because it can
		// have different erasure code settings. To get the full filesize we add
		// the size of the normal file, and then the size of the extended file.
		// But the extended file may not exist, so we have to be careful with
		// how we consider extending it. And then just in general the error
		// handling here is a bit messy.
		//
		// It seems that in practice, all files report a size of 4 MB,
		// regardless of how big the actual upload was. I didn't think this was
		// the case, but to handle it correctly we consider anything that is
		// smaller than 4300e3 bytes to be "small". Just a little fudging to
		// match the performance bucket to the thing we are actually trying to
		// measure.
		file, err := api.renter.File(sup.SiaPath)
		extendedPath := sup.SiaPath
		extendedPath.Path = extendedPath.Path + ".extended"
		file2, err2 := api.renter.File(extendedPath)
		var filesize uint64
		if err == nil {
			filesize = file.Filesize
		}
		if err == nil && err2 == nil {
			filesize += file2.Filesize
		}

		// Set the Skylink response header
		w.Header().Set(SkynetSkylinkHeader, skylink.String())

		WriteJSON(w, SkynetSkyfileHandlerPOST{
			Skylink:    skylink.String(),
			MerkleRoot: skylink.MerkleRoot(),
			Bitfield:   skylink.Bitfield(),
		})
		return
	}

	// There is a convert path.
	convertPath, err := skymodules.NewSiaPath(params.convertPath)
	if err != nil {
		WriteError(w, Error{"invalid convertpath provided: " + err.Error()}, http.StatusBadRequest)
		return
	}
	convertPath, err = rebaseInputSiaPath(convertPath)
	if err != nil {
		WriteError(w, Error{"invalid convertpath provided - can't rebase: " + err.Error()}, http.StatusBadRequest)
		return
	}
	skylink, err := api.renter.CreateSkylinkFromSiafile(sup, convertPath)
	if err != nil {
		handleSkynetError(w, "failed to convert siafile to skyfile", err)
		return
	}

	// Set the Skylink response header
	w.Header().Set(SkynetSkylinkHeader, skylink.String())

	WriteJSON(w, SkynetSkyfileHandlerPOST{
		Skylink:    skylink.String(),
		MerkleRoot: skylink.MerkleRoot(),
		Bitfield:   skylink.Bitfield(),
	})
}

// skynetStatsHandlerGET responds with a JSON with statistical data about
// skynet, e.g. number of files uploaded, total size, etc.
func (api *API) skynetStatsHandlerGET(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	// Pull the skynet stats from the root directory
	dirs, err := api.renter.DirList(skymodules.RootSiaPath())
	if err != nil {
		WriteError(w, Error{"unable to get root directory status: " + err.Error()}, http.StatusBadRequest)
		return
	}
	rootDir := dirs[0]

	// get version
	version := build.NodeVersion
	if build.ReleaseTag != "" {
		version += "-" + build.ReleaseTag
	}

	// Grab the siad uptime
	uptime := time.Since(api.StartTime()).Seconds()

	// Get the registry stats.
	renterPerf, err := api.renter.Performance()
	if err != nil {
		WriteError(w, Error{"unable to get renter registry status: " + err.Error()}, http.StatusBadRequest)
		return
	}

	// Check for any critical alerts.
	numCritAlerts := 0
	if api.gateway != nil {
		a, _, _ := api.gateway.Alerts()
		numCritAlerts += len(a)
	}
	if api.cs != nil {
		a, _, _ := api.cs.Alerts()
		numCritAlerts += len(a)
	}
	if api.tpool != nil {
		a, _, _ := api.tpool.Alerts()
		numCritAlerts += len(a)
	}
	if api.wallet != nil {
		a, _, _ := api.wallet.Alerts()
		numCritAlerts += len(a)
	}
	if api.renter != nil {
		a, _, _ := api.renter.Alerts()
		numCritAlerts += len(a)
	}
	if api.host != nil {
		a, _, _ := api.host.Alerts()
		numCritAlerts += len(a)
	}

	// Determine the wallet status.
	var walletStatus string
	var allowance skymodules.Allowance
	unlocked, err := api.wallet.Unlocked()
	if err != nil {
		WriteError(w, Error{"unable to get wallet lock status: " + err.Error()}, http.StatusBadRequest)
		return
	}
	walletFunds, _, _, err := api.wallet.ConfirmedBalance()
	if err != nil {
		WriteError(w, Error{"unable to get wallet balance: " + err.Error()}, http.StatusBadRequest)
		return
	}
	renterSettings, err := api.renter.Settings()
	if err != nil {
		WriteError(w, Error{"unable to get renter settings: " + err.Error()}, http.StatusBadRequest)
		return
	}
	allowance = renterSettings.Allowance
	if !unlocked {
		walletStatus = "locked"
	} else if walletFunds.Cmp(allowance.Funds.Div64(3)) < 0 {
		walletStatus = "low"
	} else if walletFunds.Cmp(allowance.Funds.Mul64(3)) > 0 {
		walletStatus = "high"
	} else {
		walletStatus = "healthy"
	}

	// Determine the allowance status.
	financialMetrics, err := api.renter.PeriodSpending()
	if err != nil {
		WriteError(w, Error{"unable to get renter financial breakdonw: " + err.Error()}, http.StatusBadRequest)
		return
	}
	_, _, unspentUnallocated := financialMetrics.SpendingBreakdown()
	var allowanceStatus string
	if unspentUnallocated.Cmp(types.NewCurrency64(10e3)) < 0 {
		allowanceStatus = "low"
	} else if unspentUnallocated.Cmp(allowance.Funds.Div64(5)) < 0 {
		allowanceStatus = "low"
	} else if unspentUnallocated.Cmp(allowance.Funds.Mul64(3).Div64(4)) > 0 && allowance.Funds.Cmp(types.NewCurrency64(50e3)) > 0 {
		allowanceStatus = "high"
	} else {
		allowanceStatus = "healthy"
	}

	// Get information about the total contracts size.
	var totalStorage uint64
	for _, c := range api.renter.Contracts() {
		totalStorage += c.Size()
	}

	// Get the sector stats
	baseSectorStats := renterPerf.BaseSectorDownloadOverdriveStats
	fanoutSectorStats := renterPerf.FanoutSectorDownloadOverdriveStats

	WriteJSON(w, &SkynetStatsGET{
		BaseSectorUpload15mDataPoints: renterPerf.BaseSectorUploadStats.DataPoints[0],
		BaseSectorUpload15mP99ms:      float64(renterPerf.BaseSectorUploadStats.Nines[0][1]) / float64(time.Millisecond),
		BaseSectorUpload15mP999ms:     float64(renterPerf.BaseSectorUploadStats.Nines[0][2]) / float64(time.Millisecond),
		BaseSectorUpload15mP9999ms:    float64(renterPerf.BaseSectorUploadStats.Nines[0][3]) / float64(time.Millisecond),

		BaseSectorOverdrivePct:   baseSectorStats.OverdrivePct(),
		BaseSectorOverdriveAvg:   baseSectorStats.NumOverdriveWorkersAvg(),
		FanoutSectorOverdrivePct: fanoutSectorStats.OverdrivePct(),
		FanoutSectorOverdriveAvg: fanoutSectorStats.NumOverdriveWorkersAvg(),

		ChunkUpload15mDataPoints: renterPerf.ChunkUploadStats.DataPoints[0],
		ChunkUpload15mP99ms:      float64(renterPerf.ChunkUploadStats.Nines[0][1]) / float64(time.Millisecond),
		ChunkUpload15mP999ms:     float64(renterPerf.ChunkUploadStats.Nines[0][2]) / float64(time.Millisecond),
		ChunkUpload15mP9999ms:    float64(renterPerf.ChunkUploadStats.Nines[0][3]) / float64(time.Millisecond),

		RegistryRead15mDataPoints: renterPerf.RegistryReadStats.DataPoints[0],
		RegistryRead15mP99ms:      float64(renterPerf.RegistryReadStats.Nines[0][1]) / float64(time.Millisecond),
		RegistryRead15mP999ms:     float64(renterPerf.RegistryReadStats.Nines[0][2]) / float64(time.Millisecond),
		RegistryRead15mP9999ms:    float64(renterPerf.RegistryReadStats.Nines[0][3]) / float64(time.Millisecond),

		RegistryWrite15mDataPoints: renterPerf.RegistryWriteStats.DataPoints[0],
		RegistryWrite15mP99ms:      float64(renterPerf.RegistryWriteStats.Nines[0][1]) / float64(time.Millisecond),
		RegistryWrite15mP999ms:     float64(renterPerf.RegistryWriteStats.Nines[0][2]) / float64(time.Millisecond),
		RegistryWrite15mP9999ms:    float64(renterPerf.RegistryWriteStats.Nines[0][3]) / float64(time.Millisecond),

		StreamBufferRead15mDataPoints: renterPerf.StreamBufferReadStats.DataPoints[0],
		StreamBufferRead15mP99ms:      float64(renterPerf.StreamBufferReadStats.Nines[0][1]) / float64(time.Millisecond),
		StreamBufferRead15mP999ms:     float64(renterPerf.StreamBufferReadStats.Nines[0][2]) / float64(time.Millisecond),
		StreamBufferRead15mP9999ms:    float64(renterPerf.StreamBufferReadStats.Nines[0][3]) / float64(time.Millisecond),

		SystemHealthScanDurationHours: float64(renterPerf.SystemHealthScanDuration) / float64(time.Hour),

		AllowanceStatus: allowanceStatus,
		ContractStorage: totalStorage,
		NumCritAlerts:   numCritAlerts,
		NumFiles:        rootDir.AggregateSkynetFiles,
		PortalMode:      allowance.PortalMode(),
		MaxStoragePrice: allowance.MaxStoragePrice,
		Repair:          rootDir.AggregateRepairSize,
		Storage:         rootDir.AggregateSkynetSize,
		StuckChunks:     rootDir.AggregateNumStuckChunks,
		WalletStatus:    walletStatus,

		Uptime: int64(uptime),
		VersionInfo: SkynetVersion{
			Version:     version,
			GitRevision: build.GitRevision,
		},
	})
}

// skykeyHandlerGET handles the API call to get a Skykey and its ID using its
// name or ID.
func (api *API) skykeyHandlerGET(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Parse Skykey id and name.
	name := req.FormValue("name")
	idString := req.FormValue("id")

	if idString == "" && name == "" {
		WriteError(w, Error{"you must specify the name or ID of the skykey"}, http.StatusInternalServerError)
		return
	}
	if idString != "" && name != "" {
		WriteError(w, Error{"you must specify either the name or ID of the skykey, not both"}, http.StatusInternalServerError)
		return
	}

	var sk skykey.Skykey
	var err error
	if name != "" {
		sk, err = api.renter.SkykeyByName(name)
	} else if idString != "" {
		var id skykey.SkykeyID
		err = id.FromString(idString)
		if err != nil {
			WriteError(w, Error{"failed to decode ID string: "}, http.StatusInternalServerError)
			return
		}
		sk, err = api.renter.SkykeyByID(id)
	}
	if err != nil {
		WriteError(w, Error{"failed to retrieve skykey: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	skString, err := sk.ToString()
	if err != nil {
		WriteError(w, Error{"failed to decode skykey: " + err.Error()}, http.StatusInternalServerError)
		return
	}
	WriteJSON(w, SkykeyGET{
		Skykey: skString,
		Name:   sk.Name,
		ID:     sk.ID().ToString(),
		Type:   sk.Type.ToString(),
	})
}

// skykeyDeleteHandlerGET handles the API call to delete a Skykey using its name
// or ID.
func (api *API) skykeyDeleteHandlerPOST(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Parse Skykey id and name.
	name := req.FormValue("name")
	idString := req.FormValue("id")

	if idString == "" && name == "" {
		WriteError(w, Error{"you must specify the name or ID of the skykey"}, http.StatusBadRequest)
		return
	}
	if idString != "" && name != "" {
		WriteError(w, Error{"you must specify either the name or ID of the skykey, not both"}, http.StatusBadRequest)
		return
	}

	var err error
	if name != "" {
		err = api.renter.DeleteSkykeyByName(name)
	} else if idString != "" {
		var id skykey.SkykeyID
		err = id.FromString(idString)
		if err != nil {
			WriteError(w, Error{"Invalid skykey ID: " + err.Error()}, http.StatusBadRequest)
			return
		}
		err = api.renter.DeleteSkykeyByID(id)
	}

	if err != nil {
		WriteError(w, Error{"failed to delete skykey: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	WriteSuccess(w)
}

// skykeyCreateKeyHandlerPost handles the API call to create a skykey using the renter's
// skykey manager.
func (api *API) skykeyCreateKeyHandlerPOST(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Parse skykey name and type
	name := req.FormValue("name")
	skykeyTypeString := req.FormValue("type")

	if name == "" {
		WriteError(w, Error{"you must specify the name of the skykey"}, http.StatusInternalServerError)
		return
	}

	if skykeyTypeString == "" {
		WriteError(w, Error{"you must specify the type of the skykey"}, http.StatusInternalServerError)
		return
	}

	var skykeyType skykey.SkykeyType
	err := skykeyType.FromString(skykeyTypeString)
	if err != nil {
		WriteError(w, Error{"failed to decode skykey type: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	sk, err := api.renter.CreateSkykey(name, skykeyType)
	if err != nil {
		WriteError(w, Error{"failed to create skykey: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	keyString, err := sk.ToString()
	if err != nil {
		WriteError(w, Error{"failed to decode skykey: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	WriteJSON(w, SkykeyGET{
		Skykey: keyString,
		Name:   name,
		ID:     sk.ID().ToString(),
		Type:   skykeyTypeString,
	})
}

// skykeyAddKeyHandlerPost handles the API call to add a skykey to the renter's
// skykey manager.
func (api *API) skykeyAddKeyHandlerPOST(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Parse skykey.
	skString := req.FormValue("skykey")
	if skString == "" {
		WriteError(w, Error{"you must specify the name of the skykey"}, http.StatusInternalServerError)
		return
	}

	var sk skykey.Skykey
	err := sk.FromString(skString)
	if err != nil {
		WriteError(w, Error{"failed to decode skykey: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	err = api.renter.AddSkykey(sk)
	if err != nil {
		WriteError(w, Error{"failed to add skykey: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	WriteSuccess(w)
}

// skykeysHandlerGET handles the API call to get all of the renter's skykeys.
func (api *API) skykeysHandlerGET(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	skykeys, err := api.renter.Skykeys()
	if err != nil {
		WriteError(w, Error{"Unable to get skykeys: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	res := SkykeysGET{
		Skykeys: make([]SkykeyGET, len(skykeys)),
	}
	for i, sk := range skykeys {
		skStr, err := sk.ToString()
		if err != nil {
			WriteError(w, Error{"failed to write skykey string: " + err.Error()}, http.StatusInternalServerError)
			return
		}
		res.Skykeys[i] = SkykeyGET{
			Skykey: skStr,
			Name:   sk.Name,
			ID:     sk.ID().ToString(),
			Type:   sk.Type.ToString(),
		}
	}
	WriteJSON(w, res)
}

// registryHandlerPOST handles the POST calls to /skynet/registry.
func (api *API) registryHandlerPOST(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Decode request.
	dec := json.NewDecoder(req.Body)
	var rhp RegistryHandlerRequestPOST
	err := dec.Decode(&rhp)
	if err != nil {
		WriteError(w, Error{"Failed to decode request: " + err.Error()}, http.StatusBadRequest)
		return
	}

	// If the type wasn't set, default to no pubkey to preserve
	// compatibility.
	if rhp.Type == modules.RegistryTypeInvalid {
		rhp.Type = modules.RegistryTypeWithoutPubkey
	}

	// Check data length here to be able to offer a better and faster error
	// message than when the hosts return it.
	if len(rhp.Data) > modules.RegistryDataSize {
		WriteError(w, Error{fmt.Sprintf("Registry data is too big: %v > %v", len(rhp.Data), modules.RegistryDataSize)}, http.StatusBadRequest)
		return
	}

	// Prepare a context for the timeout.
	ctx, cancel := context.WithTimeout(req.Context(), renter.DefaultRegistryUpdateTimeout)
	defer cancel()

	// Update the registry.
	srv := modules.NewSignedRegistryValue(rhp.DataKey, rhp.Data, rhp.Revision, rhp.Signature, rhp.Type)
	err = api.renter.UpdateRegistry(ctx, rhp.PublicKey, srv)
	if err != nil {
		handleSkynetError(w, "Unable to update the registry", err)
		return
	}
	WriteSuccess(w)
}

// registryMultiHandlerPOST handles the POST calls to /skynet/registrymulti.
func (api *API) registryMultiHandlerPOST(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Decode request.
	dec := json.NewDecoder(req.Body)
	var rhps []RegistryHandlerMultiRequestPOST
	err := dec.Decode(&rhps)
	if err != nil {
		WriteError(w, Error{"Failed to decode request: " + err.Error()}, http.StatusBadRequest)
		return
	}

	// Check request and combine them into a map from host to entry.
	srvs := make(map[string]skymodules.RegistryEntry, len(rhps))
	for i, rhp := range rhps {
		// If the type wasn't set, default to no pubkey to preserve
		// compatibility.
		if rhp.Type == modules.RegistryTypeInvalid {
			rhps[i].Type = modules.RegistryTypeWithoutPubkey
		}

		// Check data length here to be able to offer a better and faster error
		// message than when the hosts return it.
		if len(rhp.Data) > modules.RegistryDataSize {
			WriteError(w, Error{fmt.Sprintf("Registry data is too big: %v > %v", len(rhp.Data), modules.RegistryDataSize)}, http.StatusBadRequest)
			return
		}
		srv := modules.NewSignedRegistryValue(rhp.DataKey, rhp.Data, rhp.Revision, rhp.Signature, rhp.Type)

		// Each update should be for a different host.
		_, exists := srvs[rhp.HostKey.String()]
		if exists {
			WriteError(w, Error{"Updating multiple entries on one host is not supported"}, http.StatusBadRequest)
			return
		}
		srvs[rhp.HostKey.String()] = skymodules.NewRegistryEntry(rhp.PublicKey, srv)
	}

	// Prepare a context for the timeout.
	ctx, cancel := context.WithTimeout(req.Context(), renter.DefaultRegistryUpdateTimeout)
	defer cancel()

	// Update the registry.
	err = api.renter.UpdateRegistryMulti(ctx, srvs)
	if err != nil {
		handleSkynetError(w, "Unable to update the registry", err)
		return
	}
	WriteSuccess(w)
}

// registryHandlerGET handles the GET calls to /skynet/registry.
func (api *API) registryHandlerGET(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Parse the query params.
	queryForm, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		WriteError(w, Error{"failed to parse query params"}, http.StatusBadRequest)
		return
	}
	// Parse public key
	var spk types.SiaPublicKey
	err = spk.LoadString(queryForm.Get("publickey"))
	if err != nil {
		WriteError(w, Error{"Unable to parse publickey param: " + err.Error()}, http.StatusBadRequest)
		return
	}

	// Parse datakey.
	var dataKey crypto.Hash
	err = dataKey.LoadString(queryForm.Get("datakey"))
	if err != nil {
		WriteError(w, Error{"Unable to decode dataKey param: " + err.Error()}, http.StatusBadRequest)
		return
	}

	// Parse the timeout.
	timeout, err := parseRegistryTimeout(queryForm)
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}

	// Read registry.
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()
	srv, err := api.renter.ReadRegistry(ctx, spk, dataKey)
	if err != nil {
		handleSkynetError(w, "unable to read from the registry", err)
		return
	}

	// Send response.
	WriteJSON(w, RegistryHandlerGET{
		Data:      hex.EncodeToString(srv.Data),
		DataKey:   srv.Tweak,
		Revision:  srv.Revision,
		PublicKey: srv.PubKey,
		Signature: hex.EncodeToString(srv.Signature[:]),
		Type:      srv.Type,
	})
}

// registryEntryHealthHandlerGET is the handler for the /skynet/registry/health
// endpoint.
func (api *API) registryEntryHealthHandlerGET(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Parse the query params.
	queryForm, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		WriteError(w, Error{"failed to parse query params"}, http.StatusBadRequest)
		return
	}
	spkStr := queryForm.Get("publickey")
	tweakStr := queryForm.Get("datakey")
	ridStr := queryForm.Get("entryid")

	// Parse the timeout.
	timeout, err := parseRegistryTimeout(queryForm)
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()

	// If the publickey and datakey are set we use them. Otherwise we look
	// for the entryid.
	var reh skymodules.RegistryEntryHealth
	if spkStr != "" && tweakStr != "" {
		// Parse public key
		var spk types.SiaPublicKey
		err := spk.LoadString(spkStr)
		if err != nil {
			WriteError(w, Error{"Unable to parse 'publickey' param: " + err.Error()}, http.StatusBadRequest)
			return
		}

		// Parse datakey.
		var dataKey crypto.Hash
		err = dataKey.LoadString(tweakStr)
		if err != nil {
			WriteError(w, Error{"Unable to decode 'dataKey' param: " + err.Error()}, http.StatusBadRequest)
			return
		}
		reh, err = api.renter.RegistryEntryHealth(ctx, spk, dataKey)
	} else if ridStr != "" {
		// Parse rid.
		var rid crypto.Hash
		err := rid.LoadString(ridStr)
		if err != nil {
			WriteError(w, Error{"Unable to decode 'entryid' param: " + err.Error()}, http.StatusBadRequest)
			return
		}
		reh, err = api.renter.RegistryEntryHealthRID(ctx, modules.RegistryEntryID(rid))
	} else {
		WriteError(w, Error{"Need to specify either publickey and datakey or entryid"}, http.StatusBadRequest)
		return
	}
	if err != nil {
		handleSkynetError(w, "unable to read from the registry", err)
		return
	}

	// Send response.
	WriteJSON(w, reh)
}

// skylinkResolveGET handles the GET calls to /skylink/resolve/:skylink.
func (api *API) skylinkResolveGET(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	// Parse the query params.
	queryForm, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		WriteError(w, Error{"failed to parse query params"}, http.StatusBadRequest)
		return
	}

	// Parse Skylink
	var sl skymodules.Skylink
	err = sl.LoadString(ps.ByName("skylink"))
	if err != nil {
		WriteError(w, Error{"Unable to parse skylink" + err.Error()}, http.StatusBadRequest)
		return
	}
	if !sl.IsSkylinkV2() {
		if err != nil {
			WriteError(w, Error{"Can only resolve v2 skylinks" + err.Error()}, http.StatusBadRequest)
			return
		}
	}

	// Parse the timeout.
	timeout, err := parseTimeout(queryForm)
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}

	// Resolve skylink.
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()
	slV1, srv, err := api.renter.ResolveSkylinkV2(ctx, sl)
	if err != nil {
		handleSkynetError(w, "Failed to resolve skylink", err)
		return
	}

	// Attach proof.
	var proof []skymodules.RegistryEntry
	if srv != nil {
		proof = append(proof, srv...)
	}
	err = attachRegistryEntryProof(w, proof)
	if err != nil {
		WriteError(w, Error{"unable to attach proof: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	// Set the Skylink response headers.
	if !slV1.IsSkylinkV1() {
		build.Critical("skylink attached in skynet-skylink header is not v1")
	}
	w.Header().Set(SkynetSkylinkHeader, slV1.String())
	w.Header().Set(SkynetRequestedSkylinkHeader, sl.String())

	// Send response.
	WriteJSON(w, SkylinkResolveGET{
		Skylink: slV1.String(),
	})
}

// skynetRestoreHandlerPOST handles the POST calls to /skynet/restore.
func (api *API) skynetRestoreHandlerPOST(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Restore Skyfile
	skylink, err := api.renter.RestoreSkyfile(req.Body)
	if err != nil {
		WriteError(w, Error{"unable to restore skyfile: " + err.Error()}, http.StatusBadRequest)
		return
	}

	WriteJSON(w, SkynetRestorePOST{
		Skylink: skylink.String(),
	})
}

// skynetMetadataHandlerGET is the handler for the /skynet/metadata endpoint.
func (api *API) skynetMetadataHandlerGET(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Parse the skylink from the raw URL of the request. Any special characters
	// in the raw URL are encoded, allowing us to differentiate e.g. the '?'
	// that begins query parameters from the encoded version '%3F'.
	skylink, _, _, err := parseSkylinkURL(req.URL.String(), "/skynet/metadata/")
	if err != nil {
		WriteError(w, Error{fmt.Sprintf("error parsing skylink: %v", err)}, http.StatusBadRequest)
		return
	}

	// Parse the query params.
	queryForm, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		WriteError(w, Error{"failed to parse query params"}, http.StatusBadRequest)
		return
	}

	// Parse the timeout.
	timeout, err := parseTimeout(queryForm)
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}

	// Parse pricePerMS.
	pricePerMS := DefaultSkynetPricePerMS
	pricePerMSStr := queryForm.Get("priceperms")
	if pricePerMSStr != "" {
		_, err = fmt.Sscan(pricePerMSStr, &pricePerMS)
		if err != nil {
			WriteError(w, Error{"unable to parse 'pricePerMS' parameter: " + err.Error()}, http.StatusBadRequest)
			return
		}
	}

	// Fetch the skyfile's streamer to serve the basesector of the file
	streamer, srvs, resolvedLink, err := api.renter.DownloadSkylinkBaseSector(skylink, timeout, pricePerMS)
	if err != nil {
		handleSkynetError(w, "failed to fetch base sector", err)
		return
	}
	defer func() {
		// At this point we have already responded so we can't write a potential
		// error here.
		_ = streamer.Close()
	}()

	// Set the Skylink response header
	if !resolvedLink.IsSkylinkV1() {
		build.Critical("skylink attached in skynet-skylink header is not v1")
	}
	w.Header().Set(SkynetSkylinkHeader, resolvedLink.String())
	w.Header().Set(SkynetRequestedSkylinkHeader, skylink.String())

	// Attach proof.
	err = attachRegistryEntryProof(w, srvs)
	if err != nil {
		WriteError(w, Error{"unable to attach proof: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	// Read base sector.
	baseSector, err := ioutil.ReadAll(streamer)
	if err != nil {
		WriteError(w, Error{fmt.Sprintf("failed to read base sector: %v", err)}, http.StatusInternalServerError)
		return
	}

	// Decrypt it if necessary.
	encrypted := skymodules.IsEncryptedBaseSector(baseSector)
	if encrypted {
		_, err = api.renter.DecryptBaseSector(baseSector)
		if err != nil {
			WriteError(w, Error{fmt.Sprintf("failed to decrypt base sector: %v", err)}, http.StatusInternalServerError)
			return
		}
	}

	// Parse it.
	_, _, _, rawMD, _, err := skymodules.ParseSkyfileMetadata(baseSector)
	if err != nil {
		WriteError(w, Error{fmt.Sprintf("failed to fetch skylink: %v", err)}, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	http.ServeContent(w, req, "", time.Time{}, bytes.NewReader(rawMD))
}

// skynetSkylinkHealthGET is the handler for the /skynet/health/:skylink
// endpoint.
func (api *API) skynetSkylinkHealthGET(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	strLink := ps.ByName("skylink")
	var skylink skymodules.Skylink
	err := skylink.LoadString(strLink)
	if err != nil {
		WriteError(w, Error{fmt.Sprintf("error parsing skylink: %v", err)}, http.StatusBadRequest)
		return
	}

	// Parse the query params.
	queryForm, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		WriteError(w, Error{fmt.Sprintf("failed to parse query params: %v", err)}, http.StatusBadRequest)
		return
	}

	// Parse timeout.
	timeout, err := parseTimeout(queryForm)
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()

	// Get health.
	sh, err := api.renter.SkylinkHealth(ctx, skylink, DefaultSkynetPricePerMS)
	if err != nil {
		handleSkynetError(w, "failed to get skylink health", err)
		return
	}
	WriteJSON(w, sh)
}

// skynetSkylinkUnpinHandlerPOST will unpin a skylink from this Sia node.
func (api *API) skynetSkylinkUnpinHandlerPOST(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	strLink := ps.ByName("skylink")
	var skylink skymodules.Skylink
	err := skylink.LoadString(strLink)
	if err != nil {
		WriteError(w, Error{fmt.Sprintf("error parsing skylink: %v", err)}, http.StatusBadRequest)
		return
	}

	// Parse the query params.
	queryForm, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		WriteError(w, Error{fmt.Sprintf("failed to parse query params: %v", err)}, http.StatusBadRequest)
		return
	}

	// Parse the siaPath
	var siaPath skymodules.SiaPath
	siaPathStr := queryForm.Get("siapath")
	if siaPathStr != "" {
		// If a siaPath was provided, load it and also generate an extended
		// siaPath
		err = siaPath.LoadString(siaPathStr)
		if err != nil {
			WriteError(w, Error{fmt.Sprintf("failed to parse siapath: %v", err)}, http.StatusBadRequest)
			return
		}
		extendedSiaPath, err := siaPath.AddSuffixStr(skymodules.ExtendedSuffix)
		if err != nil {
			WriteError(w, Error{fmt.Sprintf("failed to create extended siapath: %v", err)}, http.StatusBadRequest)
			return
		}

		// Try and delete skyfile
		err = api.renter.DeleteFile(siaPath)
		if err != nil && !strings.Contains(err.Error(), filesystem.ErrNotExist.Error()) {
			WriteError(w, Error{fmt.Sprintf("failed to delete skyfile: %v", err)}, http.StatusBadRequest)
			return
		}

		// Try ad delete extended skyfile
		err = api.renter.DeleteFile(extendedSiaPath)
		if err != nil && !strings.Contains(err.Error(), filesystem.ErrNotExist.Error()) {
			WriteError(w, Error{fmt.Sprintf("failed to delete extended skyfile: %v", err)}, http.StatusBadRequest)
			return
		}
	}

	// Unpin the Skylink
	err = api.renter.UnpinSkylink(skylink)
	if err != nil {
		handleSkynetError(w, "failed to unpin skylink", err)
		return
	}
	WriteSuccess(w)
}

// skynetHostsForRegistryUpdateGET is the handler for the /skynet/registry/hosts
// GET endpoint.
func (api *API) skynetHostsForRegistryUpdateGET(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	hosts, err := api.renter.HostsForRegistryUpdate()
	if err != nil {
		handleSkynetError(w, "failed to fetch hosts for registry update", err)
		return
	}
	WriteJSON(w, HostsForRegistryUpdateGET{
		Hosts: hosts,
	})
}
