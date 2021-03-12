package host

import (
	"testing"

	"gitlab.com/skynetlabs/skyd/skymodules"
)

// TestSaneDefaults verifies that the defaults satisfy the ratios
func TestSaneDefaults(t *testing.T) {
	maxBaseRPCPrice := skymodules.DefaultDownloadBandwidthPrice.Mul64(skymodules.MaxBaseRPCPriceVsBandwidth)
	if skymodules.DefaultBaseRPCPrice.Cmp(maxBaseRPCPrice) > 0 {
		t.Log("skymodules.DefaultBaseRPCPrice", skymodules.DefaultBaseRPCPrice.HumanString())
		t.Log("maxBaseRPCPrice", maxBaseRPCPrice.HumanString())
		t.Log("skymodules.DefaultDownloadBandwidthPrice", skymodules.DefaultDownloadBandwidthPrice.HumanString())
		t.Fatal("Default for BaseRPCPrice is bad")
	}
	maxBaseSectorAccessPrice := skymodules.DefaultDownloadBandwidthPrice.Mul64(skymodules.MaxSectorAccessPriceVsBandwidth)
	if skymodules.DefaultSectorAccessPrice.Cmp(maxBaseSectorAccessPrice) > 0 {
		t.Log("defaultSectorAccessPrice", skymodules.DefaultSectorAccessPrice.HumanString())
		t.Log("maxBaseSectorAccessPrice", maxBaseSectorAccessPrice.HumanString())
		t.Log("skymodules.DefaultDownloadBandwidthPrice", skymodules.DefaultDownloadBandwidthPrice.HumanString())
		t.Fatal("Default for SectorAccessPrice is bad")
	}
}
