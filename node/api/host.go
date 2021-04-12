package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/julienschmidt/httprouter"
	"gitlab.com/NebulousLabs/Sia/modules"
	siaapi "gitlab.com/NebulousLabs/Sia/node/api"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/SkynetHQ/skyd/build"
	"gitlab.com/SkynetHQ/skyd/skymodules"
)

// hostEstimateScoreGET handles the POST request to /host/estimatescore and
// computes an estimated HostDB score for the provided settings.
func hostEstimateScoreGET(host modules.Host, renter skymodules.Renter, w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// This call requires a renter, check that it is present.
	if renter == nil {
		WriteError(w, Error{"cannot call /host/estimatescore without the renter module"}, http.StatusBadRequest)
		return
	}

	settings, err := parseHostSettings(host, req)
	if err != nil {
		WriteError(w, Error{"error parsing host settings: " + err.Error()}, http.StatusBadRequest)
		return
	}
	var totalStorage, remainingStorage uint64
	for _, sf := range host.StorageFolders() {
		totalStorage += sf.Capacity
		remainingStorage += sf.CapacityRemaining
	}
	mergedSettings := modules.HostExternalSettings{
		AcceptingContracts:   settings.AcceptingContracts,
		MaxDownloadBatchSize: settings.MaxDownloadBatchSize,
		MaxDuration:          settings.MaxDuration,
		MaxReviseBatchSize:   settings.MaxReviseBatchSize,
		RemainingStorage:     remainingStorage,
		SectorSize:           modules.SectorSize,
		TotalStorage:         totalStorage,
		WindowSize:           settings.WindowSize,

		Collateral:    settings.Collateral,
		MaxCollateral: settings.MaxCollateral,

		ContractPrice:          settings.MinContractPrice,
		DownloadBandwidthPrice: settings.MinDownloadBandwidthPrice,
		StoragePrice:           settings.MinStoragePrice,
		UploadBandwidthPrice:   settings.MinUploadBandwidthPrice,

		EphemeralAccountExpiry:     settings.EphemeralAccountExpiry,
		MaxEphemeralAccountBalance: settings.MaxEphemeralAccountBalance,

		Version: build.Version,
	}
	entry := skymodules.HostDBEntry{}
	entry.PublicKey = host.PublicKey()
	entry.HostExternalSettings = mergedSettings
	// Use the default allowance for now, since we do not know what sort of
	// allowance the renters may use to attempt to access this host.
	estimatedScoreBreakdown, err := renter.EstimateHostScore(entry, skymodules.DefaultAllowance)
	if err != nil {
		WriteError(w, Error{"error estimating host score: " + err.Error()}, http.StatusInternalServerError)
		return
	}
	e := siaapi.HostEstimateScoreGET{
		EstimatedScore: estimatedScoreBreakdown.Score,
		ConversionRate: estimatedScoreBreakdown.ConversionRate,
	}
	WriteJSON(w, e)
}

// parseHostSettings a request's query strings and returns a
// modules.HostInternalSettings configured with the request's query string
// parameters.
func parseHostSettings(host modules.Host, req *http.Request) (modules.HostInternalSettings, error) {
	settings := host.InternalSettings()

	if req.FormValue("acceptingcontracts") != "" {
		var x bool
		_, err := fmt.Sscan(req.FormValue("acceptingcontracts"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.AcceptingContracts = x
	}
	if req.FormValue("maxdownloadbatchsize") != "" {
		var x uint64
		_, err := fmt.Sscan(req.FormValue("maxdownloadbatchsize"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.MaxDownloadBatchSize = x
	}
	if req.FormValue("maxduration") != "" {
		var x types.BlockHeight
		_, err := fmt.Sscan(req.FormValue("maxduration"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.MaxDuration = x
	}
	if req.FormValue("maxrevisebatchsize") != "" {
		var x uint64
		_, err := fmt.Sscan(req.FormValue("maxrevisebatchsize"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.MaxReviseBatchSize = x
	}
	if req.FormValue("netaddress") != "" {
		var x modules.NetAddress
		_, err := fmt.Sscan(req.FormValue("netaddress"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.NetAddress = x
	}
	if req.FormValue("windowsize") != "" {
		var x types.BlockHeight
		_, err := fmt.Sscan(req.FormValue("windowsize"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.WindowSize = x
	}

	if req.FormValue("collateral") != "" {
		var x types.Currency
		_, err := fmt.Sscan(req.FormValue("collateral"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.Collateral = x
	}
	if req.FormValue("collateralbudget") != "" {
		var x types.Currency
		_, err := fmt.Sscan(req.FormValue("collateralbudget"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.CollateralBudget = x
	}
	if req.FormValue("maxcollateral") != "" {
		var x types.Currency
		_, err := fmt.Sscan(req.FormValue("maxcollateral"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.MaxCollateral = x
	}

	if req.FormValue("minbaserpcprice") != "" {
		var x types.Currency
		_, err := fmt.Sscan(req.FormValue("minbaserpcprice"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.MinBaseRPCPrice = x
	}
	if req.FormValue("mincontractprice") != "" {
		var x types.Currency
		_, err := fmt.Sscan(req.FormValue("mincontractprice"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.MinContractPrice = x
	}
	if req.FormValue("mindownloadbandwidthprice") != "" {
		var x types.Currency
		_, err := fmt.Sscan(req.FormValue("mindownloadbandwidthprice"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.MinDownloadBandwidthPrice = x
	}
	if req.FormValue("minsectoraccessprice") != "" {
		var x types.Currency
		_, err := fmt.Sscan(req.FormValue("minsectoraccessprice"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.MinSectorAccessPrice = x
	}
	if req.FormValue("minstorageprice") != "" {
		var x types.Currency
		_, err := fmt.Sscan(req.FormValue("minstorageprice"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.MinStoragePrice = x
	}
	if req.FormValue("minuploadbandwidthprice") != "" {
		var x types.Currency
		_, err := fmt.Sscan(req.FormValue("minuploadbandwidthprice"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.MinUploadBandwidthPrice = x
	}
	if req.FormValue("ephemeralaccountexpiry") != "" {
		var x uint64
		_, err := fmt.Sscan(req.FormValue("ephemeralaccountexpiry"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.EphemeralAccountExpiry = time.Duration(x) * time.Second
	}
	if req.FormValue("maxephemeralaccountbalance") != "" {
		var x types.Currency
		_, err := fmt.Sscan(req.FormValue("maxephemeralaccountbalance"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.MaxEphemeralAccountBalance = x
	}
	if req.FormValue("maxephemeralaccountrisk") != "" {
		var x types.Currency
		_, err := fmt.Sscan(req.FormValue("maxephemeralaccountrisk"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.MaxEphemeralAccountRisk = x
	}
	if req.FormValue("registrysize") != "" {
		var x uint64
		_, err := fmt.Sscan(req.FormValue("registrysize"), &x)
		if err != nil {
			return modules.HostInternalSettings{}, err
		}
		settings.RegistrySize = x
	}
	if req.FormValue("customregistrypath") != "" {
		settings.CustomRegistryPath = req.FormValue("customregistrypath")
	}

	// Validate the RPC, Sector Access, and Download Prices
	minBaseRPCPrice := settings.MinBaseRPCPrice
	maxBaseRPCPrice := settings.MaxBaseRPCPrice()
	if minBaseRPCPrice.Cmp(maxBaseRPCPrice) > 0 {
		return modules.HostInternalSettings{}, siaapi.ErrInvalidRPCDownloadRatio
	}
	minSectorAccessPrice := settings.MinSectorAccessPrice
	maxSectorAccessPrice := settings.MaxSectorAccessPrice()
	if minSectorAccessPrice.Cmp(maxSectorAccessPrice) > 0 {
		return modules.HostInternalSettings{}, siaapi.ErrInvalidSectorAccessDownloadRatio
	}

	return settings, nil
}
