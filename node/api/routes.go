package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/tus/tusd/pkg/handler"
	siaapi "go.sia.tech/siad/node/api"

	"gitlab.com/NebulousLabs/log"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter"
)

var (
	// httpServerTimeout defines the maximum amount of time before an HTTP call
	// will timeout and an error will be returned.
	httpServerTimeout = build.Select(build.Var{
		Standard: 24 * time.Hour,
		Dev:      1 * time.Hour,
		Testing:  5 * time.Minute,
	}).(time.Duration)
)

// buildHttpRoutes sets up and returns an * httprouter.Router.
// it connected the Router to the given api using the required
// parameters: requiredUserAgent and requiredPassword
func (api *API) buildHTTPRoutes() {
	router := httprouter.New()
	requiredPassword := api.requiredPassword
	requiredUserAgent := api.requiredUserAgent

	router.NotFound = http.HandlerFunc(api.UnrecognizedCallHandler)
	router.RedirectTrailingSlash = false

	// Accounting API Calls
	if api.accounting != nil {
		router.GET("/accounting", api.accountingHandlerGet)
	}

	// Daemon API Calls
	router.GET("/daemon/alerts", api.daemonAlertsHandlerGET)
	router.GET("/daemon/constants", api.daemonConstantsHandler)
	router.GET("/daemon/ready", api.daemonReadyGET)
	router.GET("/daemon/settings", api.daemonSettingsHandlerGET)
	router.POST("/daemon/settings", api.daemonSettingsHandlerPOST)
	router.GET("/daemon/stack", api.daemonStackHandlerGET)
	router.POST("/daemon/startprofile", api.daemonStartProfileHandlerPOST)
	router.GET("/daemon/stop", RequirePassword(api.daemonStopHandler, requiredPassword))
	router.POST("/daemon/stopprofile", api.daemonStopProfileHandlerPOST)
	router.GET("/daemon/version", api.daemonVersionHandler)

	// Consensus API Calls
	if api.cs != nil {
		siaapi.RegisterRoutesConsensus(router, api.cs)
	}

	// Explorer API Calls
	if api.explorer != nil {
		siaapi.RegisterRoutesExplorer(router, api.explorer, api.cs)
	}

	// Gateway API Calls
	if api.gateway != nil {
		siaapi.RegisterRoutesGateway(router, api.gateway, requiredPassword)
	}

	// Host API Calls
	if api.host != nil {
		siaapi.RegisterRoutesHost(router, api.host, api.staticDeps, requiredPassword)

		// Register estiamtescore separately since it depends on a renter.
		router.GET("/host/estimatescore", func(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
			hostEstimateScoreGET(api.host, api.renter, w, req, ps)
		})
	}

	// Miner API Calls
	if api.miner != nil {
		siaapi.RegisterRoutesMiner(router, api.miner, requiredPassword)
	}

	// Renter API Calls
	if api.renter != nil {
		router.GET("/renter", api.renterHandlerGET)
		router.POST("/renter", RequirePassword(api.renterHandlerPOST, requiredPassword))
		router.POST("/renter/allowance/cancel", RequirePassword(api.renterAllowanceCancelHandlerPOST, requiredPassword))
		router.POST("/renter/bubble", api.renterBubbleHandlerPOST)
		router.GET("/renter/backups", RequirePassword(api.renterBackupsHandlerGET, requiredPassword))
		router.POST("/renter/backups/create", RequirePassword(api.renterBackupsCreateHandlerPOST, requiredPassword))
		router.POST("/renter/backups/restore", RequirePassword(api.renterBackupsRestoreHandlerGET, requiredPassword))
		router.POST("/renter/clean", RequirePassword(api.renterCleanHandlerPOST, requiredPassword))
		router.POST("/renter/contract/cancel", RequirePassword(api.renterContractCancelHandler, requiredPassword))
		router.GET("/renter/contracts", api.renterContractsHandler)
		router.GET("/renter/contractorchurnstatus", api.renterContractorChurnStatus)
		router.GET("/renter/downloadinfo/*uid", api.renterDownloadByUIDHandlerGET)
		router.GET("/renter/downloads", api.renterDownloadsHandler)
		router.POST("/renter/downloads/clear", RequirePassword(api.renterClearDownloadsHandler, requiredPassword))
		router.GET("/renter/files", api.renterFilesHandler)
		router.GET("/renter/file/*siapath", api.renterFileHandlerGET)
		router.POST("/renter/file/*siapath", RequirePassword(api.renterFileHandlerPOST, requiredPassword))
		router.GET("/renter/prices", api.renterPricesHandler)
		router.POST("/renter/recoveryscan", RequirePassword(api.renterRecoveryScanHandlerPOST, requiredPassword))
		router.GET("/renter/recoveryscan", api.renterRecoveryScanHandlerGET)
		router.GET("/renter/fuse", api.renterFuseHandlerGET)
		router.POST("/renter/fuse/mount", RequirePassword(api.renterFuseMountHandlerPOST, requiredPassword))
		router.POST("/renter/fuse/unmount", RequirePassword(api.renterFuseUnmountHandlerPOST, requiredPassword))

		router.POST("/renter/delete/*siapath", RequirePassword(api.renterDeleteHandler, requiredPassword))
		router.GET("/renter/download/*siapath", RequirePassword(api.renterDownloadHandler, requiredPassword))
		router.POST("/renter/download/cancel", RequirePassword(api.renterCancelDownloadHandler, requiredPassword))
		router.GET("/renter/downloadasync/*siapath", RequirePassword(api.renterDownloadAsyncHandler, requiredPassword))
		router.POST("/renter/rename/*siapath", RequirePassword(api.renterRenameHandler, requiredPassword))
		router.GET("/renter/stream/*siapath", api.renterStreamHandler)
		router.POST("/renter/upload/*siapath", RequirePassword(api.renterUploadHandler, requiredPassword))
		router.GET("/renter/uploadready", api.renterUploadReadyHandler)
		router.POST("/renter/uploads/pause", RequirePassword(api.renterUploadsPauseHandler, requiredPassword))
		router.POST("/renter/uploads/resume", RequirePassword(api.renterUploadsResumeHandler, requiredPassword))
		router.POST("/renter/uploadstream/*siapath", RequirePassword(api.renterUploadStreamHandler, requiredPassword))
		router.POST("/renter/validatesiapath/*siapath", RequirePassword(api.renterValidateSiaPathHandler, requiredPassword))
		router.GET("/renter/workers", api.renterWorkersHandler)

		// Skynet endpoints
		router.GET("/skynet/basesector/*skylink", api.skynetBaseSectorHandlerGET)
		router.GET("/skynet/blocklist", api.skynetBlocklistHandlerGET)
		router.POST("/skynet/blocklist", RequirePassword(api.skynetBlocklistHandlerPOST, requiredPassword))
		router.GET("/skynet/health/entry", api.registryEntryHealthHandlerGET)
		router.GET("/skynet/metadata/:skylink", api.skynetMetadataHandlerGET)
		router.POST("/skynet/pin/:skylink", RequirePassword(api.skynetSkylinkPinHandlerPOST, requiredPassword))
		router.GET("/skynet/portals", api.skynetPortalsHandlerGET)
		router.POST("/skynet/portals", RequirePassword(api.skynetPortalsHandlerPOST, requiredPassword))
		router.POST("/skynet/registry", RequirePassword(api.registryHandlerPOST, requiredPassword))
		router.POST("/skynet/registrymulti", RequirePassword(api.registryMultiHandlerPOST, requiredPassword))
		router.GET("/skynet/registry", api.registryHandlerGET)
		router.GET("/skynet/registry/hosts", api.skynetHostsForRegistryUpdateGET)
		router.GET("/skynet/registry/subscription", api.skynetRegistrySubscriptionHandler)
		router.GET("/skynet/resolve/:skylink", api.skylinkResolveGET)
		router.POST("/skynet/restore", RequirePassword(api.skynetRestoreHandlerPOST, requiredPassword))
		router.GET("/skynet/root", api.skynetRootHandlerGET)
		router.GET("/skynet/skylink/*skylink", api.skynetSkylinkHandlerGET)
		router.HEAD("/skynet/skylink/*skylink", api.skynetSkylinkHandlerGET)
		router.POST("/skynet/skyfile/*siapath", RequirePassword(api.skynetSkyfileHandlerPOST, requiredPassword))
		router.GET("/skynet/stats", api.skynetStatsHandlerGET)
		router.POST("/skynet/stats/reset", api.skynetStatsResetHandlerPOST)
		router.POST("/skynet/unpin/:skylink", RequirePassword(api.skynetSkylinkUnpinHandlerPOST, requiredPassword))
		router.GET("/skynet/health/skylink/:skylink", api.skynetSkylinkHealthGET)

		// Skykey endpoints
		router.GET("/skynet/skykey", RequirePassword(api.skykeyHandlerGET, requiredPassword))
		router.POST("/skynet/addskykey", RequirePassword(api.skykeyAddKeyHandlerPOST, requiredPassword))
		router.POST("/skynet/createskykey", RequirePassword(api.skykeyCreateKeyHandlerPOST, requiredPassword))
		router.POST("/skynet/deleteskykey", RequirePassword(api.skykeyDeleteHandlerPOST, requiredPassword))
		router.GET("/skynet/skykeys", RequirePassword(api.skykeysHandlerGET, requiredPassword))

		// Create the store composer.
		storeComposer := handler.NewStoreComposer()
		sds := api.renter.SkynetTUSUploader()

		// Add the skynet datastore. This covers the basic functionality of
		// uploading a file with a known size in chunks.
		storeComposer.UseCore(sds)

		// Enable concatenating uploads.
		storeComposer.UseConcater(sds)

		// Enable locking the upload.
		storeComposer.UseLocker(sds)

		// Check if the maxsize can be read from the environment.  Otherwise
		// it's unlimited.
		maxSize, ok := build.TUSMaxSize()
		if ok {
			fmt.Printf("INFO: max size for tus uploads set to %v\n", maxSize)
		}

		// Create the TUS handler and register its routes.
		tusHandler, err := handler.NewUnroutedHandler(handler.Config{
			PreUploadCreateCallback: renter.TUSPreUploadCreateCallback,
			BasePath:                "/skynet/tus",
			MaxSize:                 maxSize,
			RespectForwardedHeaders: true,
			StoreComposer:           storeComposer,

			// NOTE: comment logger out for debugging
			Logger: log.DiscardLogger.Logger, // discard third party logging
		})
		if err != nil {
			build.Critical("failed to create skynet TUS handler", err)
			return
		}
		optionsHandler := func(w http.ResponseWriter, req *http.Request) {}
		router.POST("/skynet/tus", RequireTUSMiddleware(tusHandler.PostFile, tusHandler))
		router.OPTIONS("/skynet/tus", RequireTUSMiddleware(optionsHandler, tusHandler))
		router.HEAD("/skynet/tus/:id", RequireTUSMiddleware(tusHandler.HeadFile, tusHandler))
		router.PATCH("/skynet/tus/:id", RequireTUSMiddleware(tusHandler.PatchFile, tusHandler))
		router.GET("/skynet/tus/:id", RequireTUSMiddleware(tusHandler.GetFile, tusHandler))
		router.OPTIONS("/skynet/tus/:id", RequireTUSMiddleware(optionsHandler, tusHandler))
		router.GET("/skynet/upload/tus/:id", api.skynetTUSUploadSkylinkGET)

		// Directory endpoints
		router.POST("/renter/dir/*siapath", RequirePassword(api.renterDirHandlerPOST, requiredPassword))
		router.GET("/renter/dir/*siapath", api.renterDirHandlerGET)

		// HostDB endpoints.
		router.GET("/hostdb", api.hostdbHandler)
		router.GET("/hostdb/active", api.hostdbActiveHandler)
		router.GET("/hostdb/all", api.hostdbAllHandler)
		router.GET("/hostdb/hosts/:pubkey", api.hostdbHostsHandler)
		router.GET("/hostdb/filtermode", api.hostdbFilterModeHandlerGET)
		router.POST("/hostdb/filtermode", RequirePassword(api.hostdbFilterModeHandlerPOST, requiredPassword))

		// Renter watchdog endpoints.
		router.GET("/renter/contractstatus", api.renterContractStatusHandler)

		// Deprecated endpoints.
		router.POST("/renter/backup", RequirePassword(api.renterBackupHandlerPOST, requiredPassword))
		router.POST("/renter/recoverbackup", RequirePassword(api.renterLoadBackupHandlerPOST, requiredPassword))
		router.GET("/skynet/blacklist", api.skynetBlocklistHandlerGET)
		router.POST("/skynet/blacklist", RequirePassword(api.skynetBlocklistHandlerPOST, requiredPassword))
	}

	// Transaction pool API Calls
	if api.tpool != nil {
		siaapi.RegisterRoutesTransactionPool(router, api.tpool)
	}

	// Wallet API Calls
	if api.wallet != nil {
		siaapi.RegisterRoutesWallet(router, api.wallet, requiredPassword)
	}

	// Apply UserAgent middleware and return the Router
	api.routerMu.Lock()
	api.router = TimeoutHandler(RequireUserAgent(router, requiredUserAgent), httpServerTimeout)
	api.routerMu.Unlock()
	return
}

// TimeoutHandler is a middleware that enforces a specific timeout on the route
// by closing the context after the httpServerTimeout.
func TimeoutHandler(h http.Handler, timeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Create a new context with timeout.
		ctx, cancel := context.WithTimeout(req.Context(), httpServerTimeout)
		defer cancel()

		// Add the new context to the request and call the handler.
		h.ServeHTTP(w, req.WithContext(ctx))
	})
}

// RequireUserAgent is middleware that requires all requests to set a
// UserAgent that contains the specified string.
func RequireUserAgent(h http.Handler, ua string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.Contains(req.UserAgent(), ua) && !isUnrestricted(req) {
			WriteError(w, Error{"Browser access disabled due to security vulnerability. Use Sia-UI or siac."}, http.StatusBadRequest)
			return
		}
		h.ServeHTTP(w, req)
	})
}

// RequirePassword is middleware that requires a request to authenticate with a
// password using HTTP basic auth. Usernames are ignored. Empty passwords
// indicate no authentication is required.
func RequirePassword(h httprouter.Handle, password string) httprouter.Handle {
	// An empty password is equivalent to no password.
	if password == "" {
		return h
	}
	return func(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
		_, pass, ok := req.BasicAuth()
		if !ok || pass != password {
			w.Header().Set("WWW-Authenticate", "Basic realm=\"SiaAPI\"")
			WriteError(w, Error{"API authentication failed."}, http.StatusUnauthorized)
			return
		}
		h(w, req, ps)
	}
}

// RequireTUSMiddleware will apply the provided handler's middleware to the
// handle and return a httprouter.Handle.
func RequireTUSMiddleware(handle http.HandlerFunc, uh *handler.UnroutedHandler) httprouter.Handle {
	return func(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
		uh.Middleware(handle).ServeHTTP(w, req)
	}
}

// isUnrestricted checks if a request may bypass the useragent check.
func isUnrestricted(req *http.Request) bool {
	return strings.HasPrefix(req.URL.Path, "/renter/stream/") || strings.HasPrefix(req.URL.Path, "/skynet/skylink") || strings.HasPrefix(req.URL.Path, "/skynet/tus")
}
