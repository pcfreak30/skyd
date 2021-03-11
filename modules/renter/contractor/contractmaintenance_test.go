package contractor

import (
	"io/ioutil"
	"math"
	"reflect"
	"testing"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
)

// TestCheckFormContractGouging checks that the upload price gouging checker is
// correctly detecting price gouging from a host.
//
// Test looks a bit funny because it was adapated from the other price gouging
// tests.
func TestCheckFormContractGouging(t *testing.T) {
	oneCurrency := types.NewCurrency64(1)

	// minAllowance contains only the fields necessary to test the price gouging
	// function. The min allowance doesn't include any of the max prices,
	// because the function will ignore them if they are not set.
	minAllowance := modules.Allowance{}
	// minHostSettings contains only the fields necessary to test the price
	// gouging function.
	//
	// The cost is set to be exactly equal to the price gouging limit, such that
	// slightly decreasing any of the values evades the price gouging detector.
	minHostSettings := modules.HostExternalSettings{
		BaseRPCPrice:  types.SiacoinPrecision,
		ContractPrice: types.SiacoinPrecision,
	}

	// Set min settings on the allowance that are just below that should be
	// acceptable.
	maxAllowance := minAllowance
	maxAllowance.Funds = maxAllowance.Funds.Add(oneCurrency)
	maxAllowance.MaxRPCPrice = types.SiacoinPrecision.Add(oneCurrency)
	maxAllowance.MaxContractPrice = types.SiacoinPrecision.Add(oneCurrency)
	maxAllowance.MaxDownloadBandwidthPrice = oneCurrency
	maxAllowance.MaxSectorAccessPrice = oneCurrency
	maxAllowance.MaxStoragePrice = oneCurrency
	maxAllowance.MaxUploadBandwidthPrice = oneCurrency

	// The max allowance should have no issues with price gouging.
	err := checkFormContractGouging(maxAllowance, minHostSettings)
	if err != nil {
		t.Fatal(err)
	}

	// Should fail if the MaxRPCPrice is dropped.
	failAllowance := maxAllowance
	failAllowance.MaxRPCPrice = types.SiacoinPrecision.Sub(oneCurrency)
	err = checkFormContractGouging(failAllowance, minHostSettings)
	if err == nil {
		t.Fatal("expecting price gouging check to fail")
	}

	// Should fail if the MaxContractPrice is dropped.
	failAllowance = maxAllowance
	failAllowance.MaxContractPrice = types.SiacoinPrecision.Sub(oneCurrency)
	err = checkFormContractGouging(failAllowance, minHostSettings)
	if err == nil {
		t.Fatal("expecting price gouging check to fail")
	}
}

// TestMinimumContractRenewalFunding is a unit test for
// minimumContractRenewalFunding.
func TestMinimumContractRenewalFunding(t *testing.T) {
	a := modules.Allowance{
		Funds: types.SiacoinPrecision,
		Hosts: 10,
	}
	f := minimumContractRenewalFunding(a, 0)
	if !f.Equals(a.Funds.MulFloat(fileContractMinimumFunding).Div64(a.Hosts)) {
		t.Fatal("wrong result")
	}
	f = minimumContractRenewalFunding(a, a.Hosts*2)
	if !f.Equals(a.Funds.MulFloat(fileContractMinimumFunding).Div64(a.Hosts * 2)) {
		t.Fatal("wrong result")
	}
}

// TestInitialContractFunding is a unit test for
// initialContractFunding.
func TestInitialContractFunding(t *testing.T) {
	// Declare inputs.
	tests := []struct {
		pcif          uint64
		contractPrice uint64
		txnFee        uint64
		min           uint64
		max           uint64
		result        uint64
	}{
		{
			// Portal mode overrules everything.
			pcif:          42,
			contractPrice: fastrand.Uint64n(1000),
			txnFee:        fastrand.Uint64n(1000),
			min:           fastrand.Uint64n(1000),
			max:           fastrand.Uint64n(1000),
			result:        42,
		},
		{
			// Regular case without hitting min or max.
			pcif:          0,
			contractPrice: 100,
			txnFee:        200,
			min:           0,
			max:           math.MaxUint64,
			result:        3000,
		},
		{
			// Hit max.
			pcif:          0,
			contractPrice: 100,
			txnFee:        200,
			min:           0,
			max:           0,
			result:        0,
		},
		{
			// Hit min.
			pcif:          0,
			contractPrice: 100,
			txnFee:        200,
			min:           math.MaxUint64,
			max:           math.MaxUint64,
			result:        math.MaxUint64,
		},
	}

	// Run tests
	for i, test := range tests {
		a := modules.Allowance{
			PaymentContractInitialFunding: types.NewCurrency64(test.pcif),
		}
		host := modules.HostDBEntry{
			HostExternalSettings: modules.HostExternalSettings{
				ContractPrice: types.NewCurrency64(test.contractPrice),
			},
		}

		result := initialContractFunding(a, host, types.NewCurrency64(test.txnFee), types.NewCurrency64(test.min), types.NewCurrency64(test.max))
		if !result.Equals(types.NewCurrency64(test.result)) {
			t.Fatalf("%v: %v != %v", i, result, test.result)
		}
	}
}

// TestHostsForPortalFormation is a unit test for hostsForPortalFormation.
func TestHostsForPortalFormation(t *testing.T) {
	a := modules.Allowance{
		MaxRPCPrice:                   types.SiacoinPrecision,
		PaymentContractInitialFunding: types.SiacoinPrecision,
	}

	// helpers
	randomID := func() types.FileContractID {
		var id types.FileContractID
		fastrand.Read(id[:])
		return id
	}
	randomPK := func() types.SiaPublicKey {
		var spk types.SiaPublicKey
		spk.Key = fastrand.Bytes(crypto.PublicKeySize)
		return spk
	}

	// declare 5 known hosts.
	// 0 will be valid
	// 1 will be skipped due to an existing contract.
	// 2 will be skipped due a dead score.
	// 3 will be skipped due to a recoverable contract.
	// 4 will be skipped due to gouging.
	var activeHosts []modules.HostDBEntry
	for i := 0; i < 5; i++ {
		activeHosts = append(activeHosts, modules.HostDBEntry{
			PublicKey: randomPK(),
		})
	}
	// The existing contract contain a contract with host 1.
	allContracts := []modules.RenterContract{
		{
			ID:            randomID(),
			HostPublicKey: activeHosts[1].PublicKey,
		},
	}
	// Host 2 gets a dead score.
	scoreBreakdown := func(host modules.HostDBEntry) (modules.HostScoreBreakdown, error) {
		var sb modules.HostScoreBreakdown
		if host.PublicKey.Equals(activeHosts[2].PublicKey) {
			sb.Score = types.NewCurrency64(1)
		} else {
			sb.Score = types.NewCurrency64(2)
		}
		return sb, nil
	}
	// The recoverable contracts contain a contract with host 3.
	recoverableContracts := []modules.RecoverableContract{
		{
			ID:            randomID(),
			HostPublicKey: activeHosts[3].PublicKey,
		},
	}
	l, err := persist.NewLogger(ioutil.Discard)
	if err != nil {
		t.Fatal(err)
	}
	// Host 4 got a ridiculous base price.
	activeHosts[4].BaseRPCPrice = types.SiacoinPrecision.Mul64(math.MaxUint64)

	// 1 host should be returned and 4 should be skipped.
	needed, hosts := hostsForPortalFormation(a, allContracts, recoverableContracts, activeHosts, l, scoreBreakdown)
	if len(hosts) != 1 {
		t.Fatal("wrong number of hosts", len(hosts))
	}
	// needed is simply set to len(hosts) for portals.
	if needed != len(hosts) {
		t.Fatal("needed not set")
	}
}

// TestHostsForRegularFormation is a unit test for hostsForRegularFormation.
func TestHostsForRegularFormation(t *testing.T) {
	a := modules.Allowance{
		Hosts: 5,
	}

	// helpers
	randomID := func() types.FileContractID {
		var id types.FileContractID
		fastrand.Read(id[:])
		return id
	}
	randomPK := func() types.SiaPublicKey {
		var spk types.SiaPublicKey
		spk.Key = fastrand.Bytes(crypto.PublicKeySize)
		return spk
	}

	// Create one active contract and one inactive contract for each reason.
	allContracts := []modules.RenterContract{
		// Active
		{
			ID:            randomID(),
			HostPublicKey: randomPK(),
		},
		// Locked
		{
			ID:            randomID(),
			HostPublicKey: randomPK(),
			Utility: modules.ContractUtility{
				Locked: true,
			},
		},
		// Not good for renew.
		{
			ID:            randomID(),
			HostPublicKey: randomPK(),
			Utility: modules.ContractUtility{
				GoodForRenew: true,
			},
		},
		// Not good for upload.
		{
			ID:            randomID(),
			HostPublicKey: randomPK(),
			Utility: modules.ContractUtility{
				GoodForUpload: true,
			},
		},
	}
	// Create one recoverable contracts.
	recoverableContracts := []modules.RecoverableContract{
		{
			ID:            randomID(),
			HostPublicKey: randomPK(),
		},
	}
	l, err := persist.NewLogger(ioutil.Discard)
	if err != nil {
		t.Fatal(err)
	}

	// Make sure random hosts is called with the right args.
	var returnedHosts []modules.HostDBEntry
	randomHosts := func(n int, blacklist, addressBlacklist []types.SiaPublicKey) ([]modules.HostDBEntry, error) {
		// Check the expected n. -1 comes from the one host we have in
		// allContracts that's gfu.
		expectedN := (a.Hosts-1)*4 + uint64(randomHostsBufferForScore)
		if uint64(n) != expectedN {
			t.Fatal("random host called with wrong n", n, expectedN)
		}
		// Compute expected blacklist.
		var expectedBlacklist []types.SiaPublicKey
		for _, c := range allContracts {
			expectedBlacklist = append(expectedBlacklist, c.HostPublicKey)
		}
		expectedBlacklist = append(expectedBlacklist, recoverableContracts[0].HostPublicKey)

		// Compute expected address blacklist.
		var expectedAddressBlacklist []types.SiaPublicKey
		for _, c := range allContracts {
			u := c.Utility
			if !u.Locked || u.GoodForRenew || u.GoodForUpload {
				expectedAddressBlacklist = append(expectedAddressBlacklist, c.HostPublicKey)
			}
		}

		// Compare them.
		if !reflect.DeepEqual(blacklist, expectedBlacklist) {
			t.Log(blacklist)
			t.Log(expectedBlacklist)
			t.Fatal("wrong blacklist")
		}
		if !reflect.DeepEqual(addressBlacklist, expectedAddressBlacklist) {
			t.Log(addressBlacklist)
			t.Log(expectedAddressBlacklist)
			t.Fatal("wrong address blacklist")
		}

		// Return some random hosts and remember them for later.
		var hosts []modules.HostDBEntry
		for i := 0; i < n; i++ {
			hosts = append(hosts, modules.HostDBEntry{
				PublicKey: randomPK(),
			})
		}
		returnedHosts = hosts
		return hosts, nil
	}

	// Check returned hosts and needed hosts.
	needed, hosts := hostsForRegularFormation(a, allContracts, recoverableContracts, randomHosts, l)
	if !reflect.DeepEqual(hosts, returnedHosts) {
		t.Fatal("wrong hosts returned")
	}
	if needed != int(a.Hosts)-1 {
		t.Fatal("needed not set")
	}
}
