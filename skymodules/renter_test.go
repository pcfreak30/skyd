package skymodules

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gitlab.com/NebulousLabs/fastrand"

	"gitlab.com/SkynetLabs/skyd/build"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/persist"
	"go.sia.tech/siad/types"
)

// TestMerkleRootSetCompatibility checks that the persist encoding for the
// MerkleRootSet type is compatible with the previous encoding for the data,
// which was a slice of type crypto.Hash.
func TestMerkleRootSetCompatibility(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}

	// Create some fake headers for the files.
	meta := persist.Metadata{
		Header:  "Test Header",
		Version: "1.1.1",
	}

	// Try multiple sizes of array.
	for i := 0; i < 10; i++ {
		// Create a []crypto.Hash of length i.
		type chStruct struct {
			Hashes []crypto.Hash
		}
		var chs chStruct
		for j := 0; j < i; j++ {
			var ch crypto.Hash
			fastrand.Read(ch[:])
			chs.Hashes = append(chs.Hashes, ch)
		}

		// Save and load, check that they are the same.
		dir := build.TempDir("modules", t.Name())
		err := os.MkdirAll(dir, 0700)
		if err != nil {
			t.Fatal(err)
		}
		filename := filepath.Join(dir, "file")
		err = persist.SaveJSON(meta, chs, filename)
		if err != nil {
			t.Fatal(err)
		}

		// Load and verify equivalence.
		var loadCHS chStruct
		err = persist.LoadJSON(meta, &loadCHS, filename)
		if err != nil {
			t.Fatal(err)
		}
		if len(chs.Hashes) != len(loadCHS.Hashes) {
			t.Fatal("arrays should be the same size")
		}
		for j := range chs.Hashes {
			if chs.Hashes[j] != loadCHS.Hashes[j] {
				t.Error("loading failed", i, j)
			}
		}

		// Load into MerkleRootSet and verify equivalence.
		type mrStruct struct {
			Hashes MerkleRootSet
		}
		var loadMRS mrStruct
		err = persist.LoadJSON(meta, &loadMRS, filename)
		if err != nil {
			t.Fatal(err)
		}
		if len(chs.Hashes) != len(loadMRS.Hashes) {
			t.Fatal("arrays should be the same size")
		}
		for j := range chs.Hashes {
			if chs.Hashes[j] != loadMRS.Hashes[j] {
				t.Error("loading failed", i, j)
			}
		}

		// Save as a MerkleRootSet and verify it can be loaded again.
		var mrs mrStruct
		mrs.Hashes = MerkleRootSet(chs.Hashes)
		err = persist.SaveJSON(meta, mrs, filename)
		if err != nil {
			t.Fatal(err)
		}
		err = persist.LoadJSON(meta, &loadMRS, filename)
		if err != nil {
			t.Fatal(err)
		}
		if len(mrs.Hashes) != len(loadMRS.Hashes) {
			t.Fatal("arrays should be the same size")
		}
		for j := range mrs.Hashes {
			if mrs.Hashes[j] != loadMRS.Hashes[j] {
				t.Error("loading failed", i, j)
			}
		}
	}
}

// TestMaintenanceSpending_Add is a small unit test for the Add method on
// MaintenanceSpending
func TestMaintenanceSpending_Add(t *testing.T) {
	t.Parallel()

	x := MaintenanceSpending{}
	y := MaintenanceSpending{}
	sum := x.Add(y)
	if !sum.AccountBalanceCost.Equals(x.AccountBalanceCost) ||
		!sum.FundAccountCost.Equals(x.FundAccountCost) ||
		!sum.UpdatePriceTableCost.Equals(x.UpdatePriceTableCost) {
		t.Fatal("unexpected")
	}

	y = MaintenanceSpending{
		AccountBalanceCost:   types.NewCurrency64(1),
		FundAccountCost:      types.NewCurrency64(2),
		UpdatePriceTableCost: types.NewCurrency64(3),
	}

	// verify associative property
	sum = x.Add(y)
	if !sum.AccountBalanceCost.Equals64(1) ||
		!sum.FundAccountCost.Equals64(2) ||
		!sum.UpdatePriceTableCost.Equals64(3) {
		t.Fatal("unexpected")
	}
	sum = y.Add(x)
	if !sum.AccountBalanceCost.Equals64(1) ||
		!sum.FundAccountCost.Equals64(2) ||
		!sum.UpdatePriceTableCost.Equals64(3) {
		t.Fatal("unexpected")
	}
}

// TestMaintenanceSpending_Sum is a small unit test for the Sum method on
// MaintenanceSpending
func TestMaintenanceSpending_Sum(t *testing.T) {
	t.Parallel()

	ms := MaintenanceSpending{}
	if !ms.Sum().IsZero() {
		t.Fatal("unexpected")
	}

	ms = MaintenanceSpending{
		AccountBalanceCost:   types.NewCurrency64(1),
		FundAccountCost:      types.NewCurrency64(2),
		UpdatePriceTableCost: types.NewCurrency64(3),
	}
	if !ms.Sum().Equals64(6) {
		t.Fatal("unexpected")
	}
}

// BenchmarkMerkleRootSetEncode clocks how fast large MerkleRootSets can be
// encoded and written to disk.
func BenchmarkMerkleRootSetEncode(b *testing.B) {
	// Create a []crypto.Hash of length i.
	type chStruct struct {
		Hashes MerkleRootSet
	}
	var chs chStruct
	for i := 0; i < 1e3; i++ {
		var ch crypto.Hash
		fastrand.Read(ch[:])
		chs.Hashes = append(chs.Hashes, ch)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := json.Marshal(chs)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSliceCryptoHashEncode clocks how fast large []crypto.Hashes can be
// encoded and written to disk.
func BenchmarkSliceCryptoHashEncode(b *testing.B) {
	// Create a []crypto.Hash of length i.
	type chStruct struct {
		Hashes []crypto.Hash
	}
	var chs chStruct
	for i := 0; i < 1e3; i++ {
		var ch crypto.Hash
		fastrand.Read(ch[:])
		chs.Hashes = append(chs.Hashes, ch)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := json.Marshal(chs)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMerkleRootSetSave clocks how fast large MerkleRootSets can be
// encoded and written to disk.
func BenchmarkMerkleRootSetSave(b *testing.B) {
	// Create some fake headers for the files.
	meta := persist.Metadata{
		Header:  "Bench Header",
		Version: "1.1.1",
	}

	// Create a []crypto.Hash of length i.
	type chStruct struct {
		Hashes MerkleRootSet
	}
	var chs chStruct
	for i := 0; i < 1e3; i++ {
		var ch crypto.Hash
		fastrand.Read(ch[:])
		chs.Hashes = append(chs.Hashes, ch)
	}

	// Save through the persist.
	dir := build.TempDir("modules", "BenchmarkSliceCryptoHashSave")
	err := os.MkdirAll(dir, 0700)
	if err != nil {
		b.Fatal(err)
	}
	filename := filepath.Join(dir, "file")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err = persist.SaveJSON(meta, chs, filename)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSliceCryptoHashSave clocks how fast large []crypto.Hashes can be
// encoded and written to disk.
func BenchmarkSliceCryptoHashSave(b *testing.B) {
	// Create some fake headers for the files.
	meta := persist.Metadata{
		Header:  "Bench Header",
		Version: "1.1.1",
	}

	// Create a []crypto.Hash of length i.
	type chStruct struct {
		Hashes []crypto.Hash
	}
	var chs chStruct
	for i := 0; i < 1e3; i++ {
		var ch crypto.Hash
		fastrand.Read(ch[:])
		chs.Hashes = append(chs.Hashes, ch)
	}

	// Save through the persist.
	dir := build.TempDir("modules", "BenchmarkSliceCryptoHashSave")
	err := os.MkdirAll(dir, 0700)
	if err != nil {
		b.Fatal(err)
	}
	filename := filepath.Join(dir, "file")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err = persist.SaveJSON(meta, chs, filename)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// TestRenterPayoutsPreTax probes the RenterPayoutsPreTax function
func TestRenterPayoutsPreTax(t *testing.T) {
	// Initialize inputs
	var host HostDBEntry
	var period types.BlockHeight
	var baseCollateral types.Currency
	var expectedStorage uint64

	// Set currency values to trigger underflow check
	funding := types.NewCurrency64(10)
	txnFee := types.NewCurrency64(5)
	basePrice := types.NewCurrency64(5)

	// Check for underflow condition
	_, _, _, err := RenterPayoutsPreTax(host, funding, txnFee, basePrice, baseCollateral, period, expectedStorage)
	if err == nil {
		t.Fatal("Expected underflow error but got nil")
	}

	// Confirm no underflow
	funding = types.NewCurrency64(11)
	renterPayout, hostPayout, hostCollateral, err := RenterPayoutsPreTax(host, funding, txnFee, basePrice, baseCollateral, period, expectedStorage)
	if err != nil {
		t.Fatal(err)
	}
	if renterPayout.Cmp(types.ZeroCurrency) < 0 {
		t.Fatal("Negative currency returned for renter payout", renterPayout)
	}
	if hostPayout.Cmp(types.ZeroCurrency) < 0 {
		t.Fatal("Negative currency returned for host payout", hostPayout)
	}
	if hostCollateral.Cmp(types.ZeroCurrency) < 0 {
		t.Fatal("Negative currency returned for host collateral", hostCollateral)
	}
}

// TestContractorSpending_SpendingBreakdown is a unit test that probes
// SpendingBreakdown
func TestContractorSpending_SpendingBreakdown(t *testing.T) {
	randCurrency := func() types.Currency {
		return types.NewCurrency64(fastrand.Uint64n(1e3))
	}

	cs := &ContractorSpending{
		ContractFees:        randCurrency(),
		DownloadSpending:    randCurrency(),
		FundAccountSpending: randCurrency(),
		MaintenanceSpending: MaintenanceSpending{
			AccountBalanceCost:   randCurrency(),
			FundAccountCost:      randCurrency(),
			UpdatePriceTableCost: randCurrency(),
		},
		StorageSpending:  randCurrency(),
		TotalAllocated:   randCurrency(),
		UploadSpending:   randCurrency(),
		Unspent:          randCurrency(),
		WithheldFunds:    randCurrency(),
		PreviousSpending: randCurrency(),
	}

	totalSpent, unspentAllocated, unspentUnallocated := cs.SpendingBreakdown()

	// compare total spent vs expected value
	expectedTotalSpent := cs.ContractFees.Add(cs.UploadSpending).Add(cs.DownloadSpending).Add(cs.StorageSpending).Add(cs.FundAccountSpending).Add(cs.MaintenanceSpending.Sum())
	if !expectedTotalSpent.Equals(totalSpent) {
		t.Fatal("unexpected")
	}

	// compare unspentAllocated vs expected value
	var expectedUnspentAllocated types.Currency
	if cs.TotalAllocated.Cmp(expectedTotalSpent) >= 0 {
		expectedUnspentAllocated = cs.TotalAllocated.Sub(expectedTotalSpent)
	}
	if !expectedUnspentAllocated.Equals(unspentAllocated) {
		t.Fatal("unexpected")
	}

	// compare unspentUnallocated vs expected value
	var expectedUnspentUnallocated types.Currency
	if cs.Unspent.Cmp(expectedUnspentAllocated) >= 0 {
		expectedUnspentUnallocated = cs.Unspent.Sub(expectedUnspentAllocated)
	}
	if !expectedUnspentUnallocated.Equals(unspentUnallocated) {
		t.Fatal("unexpected")
	}
}

// TestDownloadOverdriveStats is a collection of unit tests that verify the
// functionality of the DownloadOverdriveStats object.
func TestDownloadOverdriveStats(t *testing.T) {
	t.Parallel()

	t.Run("AddDataPoint", testDownloadOverdriveStats_AddDataPoint)
	t.Run("NumOverdriveWorkersAvg", testDownloadOverdriveStats_NumOverdriveWorkersAvg)
	t.Run("OverdrivePct", testDownloadOverdriveStats_OverdrivePct)
	t.Run("Reset", testDownloadOverdriveStats_Reset)
}

// testDownloadOverdriveStats_AddDataPoint is a unit test for the AddDataPoint
// method on the DownloadOverdriveStats
func testDownloadOverdriveStats_AddDataPoint(t *testing.T) {
	t.Parallel()

	// assert initial state
	stats := NewSectorDownloadStats()
	if stats.total != 0 ||
		stats.overdrive != 0 ||
		stats.overdriveWorkersLaunched != 0 {
		t.Fatal("bad")
	}

	// add datapoint with no overdrive workers
	stats.AddDataPoint(0)
	if stats.total != 1 ||
		stats.overdrive != 0 ||
		stats.overdriveWorkersLaunched != 0 {
		t.Fatal("bad")
	}

	// add datapoint with overdrive workers
	stats.AddDataPoint(2)
	if stats.total != 2 ||
		stats.overdrive != 1 ||
		stats.overdriveWorkersLaunched != 2 {
		t.Fatal("bad")
	}
}

// testDownloadOverdriveStats_OverdrivePct is a unit test for the OverdrivePct
// method on the DownloadOverdriveStats
func testDownloadOverdriveStats_OverdrivePct(t *testing.T) {
	t.Parallel()

	stats := NewSectorDownloadStats()
	if stats.OverdrivePct() != 0 {
		t.Fatal("bad")
	}

	stats.AddDataPoint(0)
	if stats.OverdrivePct() != 0 {
		t.Fatal("bad")
	}

	stats.AddDataPoint(1)
	if stats.OverdrivePct() != 0.5 {
		t.Fatal("bad")
	}

	stats.AddDataPoint(2)
	stats.AddDataPoint(3)
	if stats.OverdrivePct() != 0.75 {
		t.Fatal("bad")
	}
}

// testDownloadOverdriveStats_Reset is a unit test for the Reset
// method on the DownloadOverdriveStats
func testDownloadOverdriveStats_Reset(t *testing.T) {
	t.Parallel()

	assertZero := func(s *DownloadOverdriveStats) bool {
		return s.OverdrivePct() == 0 && s.NumOverdriveWorkersAvg() == 0
	}

	stats := NewSectorDownloadStats()
	if !assertZero(stats) {
		t.Fatal("bad")
	}

	stats.AddDataPoint(1)
	if assertZero(stats) {
		t.Fatal("bad")
	}

	stats.Reset()
	if !assertZero(stats) {
		t.Fatal("bad")
	}
}

// testDownloadOverdriveStats_NumOverdriveWorkersAvg is a unit test for the
// NumOverdriveWorkersAvg method on the DownloadOverdriveStats
func testDownloadOverdriveStats_NumOverdriveWorkersAvg(t *testing.T) {
	t.Parallel()

	stats := NewSectorDownloadStats()
	if stats.NumOverdriveWorkersAvg() != 0 {
		t.Fatal("bad")
	}

	stats.AddDataPoint(0)
	if stats.NumOverdriveWorkersAvg() != 0 {
		t.Fatal("bad")
	}

	// this asserts that num overdrive avg includes all downloads
	stats.AddDataPoint(2)
	if stats.NumOverdriveWorkersAvg() != 1 {
		t.Fatal("bad", stats.NumOverdriveWorkersAvg())
	}

	// assert the average returns floats, use 1e-9 as an equality threshold to
	// cope with float point errors
	stats.AddDataPoint(5)
	expected := 2.3333
	if math.Abs(expected-stats.NumOverdriveWorkersAvg()) <= 1e-9 {
		t.Fatal("bad", stats.NumOverdriveWorkersAvg())
	}
}

// TestContractUtilityMerge is a unit test for the ContractUtility type's Merge
// method.
func TestContractUtilityMerge(t *testing.T) {
	goodUtility := ContractUtility{
		GoodForUpload: true,
		GoodForRenew:  true,
	}

	utility := func(gfu, gfr, bc, locked bool, lastOOSErr types.BlockHeight) ContractUtility {
		return ContractUtility{
			GoodForUpload: gfu,
			GoodForRenew:  gfr,
			BadContract:   bc,
			Locked:        locked,
			LastOOSErr:    lastOOSErr,
		}
	}

	assert := func(cu1, cu2 ContractUtility) {
		if !reflect.DeepEqual(cu1, cu2) {
			t.Fatalf("%v != %v", cu1, cu2)
		}
	}

	// Good utilities
	result := goodUtility.Merge(goodUtility)
	assert(result, goodUtility)

	// !gfu
	u := utility(false, true, false, false, 0)
	result = goodUtility.Merge(u)
	assert(result, u)

	// !gfr
	u = utility(true, false, false, false, 0)
	result = goodUtility.Merge(u)
	assert(result, u)

	// bad contract
	u = utility(true, true, true, false, 0)
	result = goodUtility.Merge(u)
	assert(result, u)

	// lastOOSErr
	u = utility(true, true, false, false, 1)
	result = goodUtility.Merge(u)
	assert(result, u)

	// locked
	u = utility(true, true, false, true, 0)
	result = goodUtility.Merge(u)
	assert(result, u)

	// everything bad
	u = utility(false, false, true, true, 1)
	result = goodUtility.Merge(u)
	assert(result, u)
}

// TestNumChunks is a unit test for NumChunks.
func TestNumChunks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		fileSize  uint64
		numChunks uint64
	}{
		{
			name:      "EmptyFile",
			fileSize:  0,
			numChunks: 0,
		},
		{
			name:      "SingleByte",
			fileSize:  1,
			numChunks: 1,
		},
		{
			name:      "FullChunks",
			fileSize:  modules.SectorSize,
			numChunks: 1,
		},
		{
			name:      "SingleByteOver",
			fileSize:  modules.SectorSize + 1,
			numChunks: 2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Use plaintext and 1 datapiece for chunksize to equal
			// sectorsize.
			numChunks := NumChunks(crypto.TypePlain, test.fileSize, 1)
			if numChunks != test.numChunks {
				t.Errorf("mismatch %v != %v", numChunks, test.numChunks)
			}
		})
	}
}
