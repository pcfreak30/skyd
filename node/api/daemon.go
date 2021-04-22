package api

import (
	"fmt"
	"math/big"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/julienschmidt/httprouter"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/profile"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

type (
	// DaemonAlertsGet contains information about currently registered alerts
	// across all loaded skymodules.
	DaemonAlertsGet struct {
		Alerts         []modules.Alert `json:"alerts"`
		CriticalAlerts []modules.Alert `json:"criticalalerts"`
		ErrorAlerts    []modules.Alert `json:"erroralerts"`
		WarningAlerts  []modules.Alert `json:"warningalerts"`
	}

	// DaemonVersionGet contains information about the running daemon's version.
	DaemonVersionGet struct {
		Version     string
		GitRevision string
		BuildTime   string
	}

	// DaemonUpdateGet contains information about a potential available update for
	// the daemon.
	DaemonUpdateGet struct {
		Available bool   `json:"available"`
		Version   string `json:"version"`
	}

	// UpdateInfo indicates whether an update is available, and to what
	// version.
	UpdateInfo struct {
		Available bool   `json:"available"`
		Version   string `json:"version"`
	}

	// SiaConstants is a struct listing all of the constants in use.
	SiaConstants struct {
		BlockFrequency         types.BlockHeight `json:"blockfrequency"`
		BlockSizeLimit         uint64            `json:"blocksizelimit"`
		ExtremeFutureThreshold types.Timestamp   `json:"extremefuturethreshold"`
		FutureThreshold        types.Timestamp   `json:"futurethreshold"`
		GenesisTimestamp       types.Timestamp   `json:"genesistimestamp"`
		MaturityDelay          types.BlockHeight `json:"maturitydelay"`
		MedianTimestampWindow  uint64            `json:"mediantimestampwindow"`
		SiafundCount           types.Currency    `json:"siafundcount"`
		SiafundPortion         *big.Rat          `json:"siafundportion"`
		TargetWindow           types.BlockHeight `json:"targetwindow"`

		InitialCoinbase uint64 `json:"initialcoinbase"`
		MinimumCoinbase uint64 `json:"minimumcoinbase"`

		RootTarget types.Target `json:"roottarget"`
		RootDepth  types.Target `json:"rootdepth"`

		DefaultAllowance skymodules.Allowance `json:"defaultallowance"`

		// DEPRECATED: same values as MaxTargetAdjustmentUp and
		// MaxTargetAdjustmentDown.
		MaxAdjustmentUp   *big.Rat `json:"maxadjustmentup"`
		MaxAdjustmentDown *big.Rat `json:"maxadjustmentdown"`

		MaxTargetAdjustmentUp   *big.Rat `json:"maxtargetadjustmentup"`
		MaxTargetAdjustmentDown *big.Rat `json:"maxtargetadjustmentdown"`

		SiacoinPrecision types.Currency `json:"siacoinprecision"`
	}

	// DaemonStackGet contains information about the daemon's stack.
	DaemonStackGet struct {
		Stack string `json:"stack"`
	}

	// DaemonSettingsGet contains information about global daemon settings.
	DaemonSettingsGet struct {
		MaxDownloadSpeed int64         `json:"maxdownloadspeed"`
		MaxUploadSpeed   int64         `json:"maxuploadspeed"`
		Modules          configModules `json:"modules"`
	}

	// DaemonVersion holds the version information for siad
	DaemonVersion struct {
		Version     string `json:"version"`
		GitRevision string `json:"gitrevision"`
		BuildTime   string `json:"buildtime"`
	}
)

// daemonAlertsHandlerGET handles the API call that returns the alerts of all
// loaded skymodules.
func (api *API) daemonAlertsHandlerGET(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	// initialize slices to avoid "null" in response.
	crit := make([]modules.Alert, 0, 6)
	err := make([]modules.Alert, 0, 6)
	warn := make([]modules.Alert, 0, 6)
	if api.gateway != nil {
		c, e, w := api.gateway.Alerts()
		crit = append(crit, c...)
		err = append(err, e...)
		warn = append(warn, w...)
	}
	if api.cs != nil {
		c, e, w := api.cs.Alerts()
		crit = append(crit, c...)
		err = append(err, e...)
		warn = append(warn, w...)
	}
	if api.tpool != nil {
		c, e, w := api.tpool.Alerts()
		crit = append(crit, c...)
		err = append(err, e...)
		warn = append(warn, w...)
	}
	if api.wallet != nil {
		c, e, w := api.wallet.Alerts()
		crit = append(crit, c...)
		err = append(err, e...)
		warn = append(warn, w...)
	}
	if api.renter != nil {
		c, e, w := api.renter.Alerts()
		crit = append(crit, c...)
		err = append(err, e...)
		warn = append(warn, w...)
	}
	if api.host != nil {
		c, e, w := api.host.Alerts()
		crit = append(crit, c...)
		err = append(err, e...)
		warn = append(warn, w...)
	}
	// Sort alerts by severity. Critical first, then Error and finally Warning.
	alerts := append(crit, append(err, warn...)...)
	WriteJSON(w, DaemonAlertsGet{
		Alerts:         alerts,
		CriticalAlerts: crit,
		ErrorAlerts:    err,
		WarningAlerts:  warn,
	})
}

// debugConstantsHandler prints a json file containing all of the constants.
func (api *API) daemonConstantsHandler(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	sc := SiaConstants{
		BlockFrequency:         types.BlockFrequency,
		BlockSizeLimit:         types.BlockSizeLimit,
		ExtremeFutureThreshold: types.ExtremeFutureThreshold,
		FutureThreshold:        types.FutureThreshold,
		GenesisTimestamp:       types.GenesisTimestamp,
		MaturityDelay:          types.MaturityDelay,
		MedianTimestampWindow:  types.MedianTimestampWindow,
		SiafundCount:           types.SiafundCount,
		SiafundPortion:         types.SiafundPortion,
		TargetWindow:           types.TargetWindow,

		InitialCoinbase: types.InitialCoinbase,
		MinimumCoinbase: types.MinimumCoinbase,

		RootTarget: types.RootTarget,
		RootDepth:  types.RootDepth,

		DefaultAllowance: skymodules.DefaultAllowance,

		// DEPRECATED: same values as MaxTargetAdjustmentUp and
		// MaxTargetAdjustmentDown.
		MaxAdjustmentUp:   types.MaxTargetAdjustmentUp,
		MaxAdjustmentDown: types.MaxTargetAdjustmentDown,

		MaxTargetAdjustmentUp:   types.MaxTargetAdjustmentUp,
		MaxTargetAdjustmentDown: types.MaxTargetAdjustmentDown,

		SiacoinPrecision: types.SiacoinPrecision,
	}

	WriteJSON(w, sc)
}

// daemonStackHandlerGET handles the API call that requests the daemon's stack trace.
func (api *API) daemonStackHandlerGET(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	// Get the stack traces of all running goroutines.
	stack := make([]byte, skymodules.StackSize)
	n := runtime.Stack(stack, true)
	if n == 0 {
		WriteError(w, Error{"no stack trace pulled"}, http.StatusInternalServerError)
		return
	}

	// Return the n bytes of the stack that were used.
	WriteJSON(w, DaemonStackGet{
		Stack: string(stack[:n]),
	})
}

// daemonStartProfileHandlerPOST handles the API call that starts a profile for the daemon.
func (api *API) daemonStartProfileHandlerPOST(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Parse profile string
	profileStr := req.FormValue("profileFlags")
	if profileStr == "" {
		WriteError(w, Error{"profile flags cannot be blank"}, http.StatusBadRequest)
		return
	}
	profileStr, err := profile.ProcessProfileFlags(profileStr)
	if err != nil {
		WriteError(w, Error{"unable to process profile flags:" + err.Error()}, http.StatusBadRequest)
		return
	}
	profileCPU := strings.Contains(profileStr, "c")
	profileMem := strings.Contains(profileStr, "m")
	profileTrace := strings.Contains(profileStr, "t")

	// Parse profile directory
	profileDir := req.FormValue("profileDir")
	if profileDir == "" {
		profileDir = build.ProfileDir()
	}
	err = os.MkdirAll(profileDir, skymodules.DefaultDirPerm)
	if err != nil {
		WriteError(w, Error{"unable to create directory for profiles:" + err.Error()}, http.StatusBadRequest)
		return
	}

	go profile.StartContinuousProfile(profileDir, profileCPU, profileMem, profileTrace)
	WriteSuccess(w)
}

// daemonStopProfileHandlerPOST handles the API call that stops a profile for the daemon.
func (api *API) daemonStopProfileHandlerPOST(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	// Stop any CPU or Trace profiles. Memory Profiles do not have a stop function
	profile.StopCPUProfile()
	profile.StopTrace()
	WriteSuccess(w)
}

// daemonVersionHandler handles the API call that requests the daemon's version.
func (api *API) daemonVersionHandler(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	version := build.NodeVersion
	if build.ReleaseTag != "" {
		version += "-" + build.ReleaseTag
	}
	WriteJSON(w, DaemonVersion{Version: version, GitRevision: build.GitRevision, BuildTime: build.BuildTime})
}

// daemonStopHandler handles the API call to stop the daemon cleanly.
func (api *API) daemonStopHandler(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	// can't write after we stop the server, so lie a bit.
	WriteSuccess(w)

	// Shutdown in a separate goroutine to prevent a deadlock.
	go func() {
		if err := api.Shutdown(); err != nil {
			build.Critical(err)
		}
	}()
}

// daemonSettingsHandlerGET handles the API call asking for the daemon's
// settings.
func (api *API) daemonSettingsHandlerGET(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	gmds, gmus, _ := skymodules.GlobalRateLimits.Limits()
	WriteJSON(w, DaemonSettingsGet{
		MaxDownloadSpeed: gmds,
		MaxUploadSpeed:   gmus,
		Modules:          api.staticConfigModules,
	})
}

// daemonSettingsHandlerPOST handles the API call changing daemon specific
// settings.
func (api *API) daemonSettingsHandlerPOST(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	maxDownloadSpeed, maxUploadSpeed, _ := skymodules.GlobalRateLimits.Limits()
	// Scan the download speed limit. (optional parameter)
	if d := req.FormValue("maxdownloadspeed"); d != "" {
		var downloadSpeed int64
		if _, err := fmt.Sscan(d, &downloadSpeed); err != nil {
			WriteError(w, Error{"unable to parse downloadspeed: " + err.Error()}, http.StatusBadRequest)
			return
		}
		maxDownloadSpeed = downloadSpeed
	}
	// Scan the upload speed limit. (optional parameter)
	if u := req.FormValue("maxuploadspeed"); u != "" {
		var uploadSpeed int64
		if _, err := fmt.Sscan(u, &uploadSpeed); err != nil {
			WriteError(w, Error{"unable to parse uploadspeed: " + err.Error()}, http.StatusBadRequest)
			return
		}
		maxUploadSpeed = uploadSpeed
	}
	// Set the limit.
	if err := api.siadConfig.SetRatelimit(maxDownloadSpeed, maxUploadSpeed); err != nil {
		WriteError(w, Error{"unable to set limits: " + err.Error()}, http.StatusBadRequest)
		return
	}
	WriteSuccess(w)
}
