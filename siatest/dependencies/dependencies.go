package dependencies

import (
	"net"
	"sync"
	"time"

	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/modules"
)

type (
	// DependencyForceLegacyRepairDownload forces the remote repair download
	// to use the legacy download for both stuck and not stuck repairs. If
	// that fails, it won't fall back to the skynet download.
	DependencyForceLegacyRepairDownload struct {
		skymodules.SkynetDependencies
	}
	// DependencyFailLegacyRepairDownload forces the remote repair download
	// to use the skynet version by failing the legacy download.
	DependencyFailLegacyRepairDownload struct {
		skymodules.SkynetDependencies
	}
	// DependencyDelayChunkDistribution delays the chunk distribution in
	// callAddUploadChunk by 1 second and skips the actual distribution.
	DependencyDelayChunkDistribution struct {
		skymodules.SkynetDependencies
	}
	// DependencyAcceptHostRevision tells a contract set to accept a host's
	// revision without having a corresponding open wal txn.
	DependencyAcceptHostRevision struct {
		skymodules.SkynetDependencies
	}
	// DependencyUnstableTUSUpload causes every TUS upload to fail and to only
	// append half the uploaded data.
	DependencyUnstableTUSUpload struct {
		skymodules.SkynetDependencies
	}
	// DependencyReadRegistryBlocking will block the read registry call by
	// making it think that it got one more worker than it actually has.
	// Therefore, waiting for a response that never comes.
	DependencyReadRegistryBlocking struct {
		skymodules.SkynetDependencies
	}
	// DependencyLegacyRenew forces the contractor to use the legacy behavior
	// when renewing a contract. This is useful for unit testing since it
	// doesn't require a renter, workers etc.
	DependencyLegacyRenew struct {
		skymodules.SkynetDependencies
	}
	// DependencyStandardUploadRedundancy uses the standard amount of data
	// pieces and parity pieces as if the upload were to happen when using the
	// Standard release.
	DependencyStandardUploadRedundancy struct {
		skymodules.SkynetDependencies
	}
	// DependencyNoSnapshotSync prevents the renter from syncing snapshots.
	DependencyNoSnapshotSync struct {
		skymodules.SkynetDependencies
	}
	// DependencyInvalidateStatsCache invalidates the
	// threadeInvalidateStatsCache loop.
	DependencyInvalidateStatsCache struct {
		skymodules.SkynetDependencies
	}
	// DependencyRegistryUpdateLyingHost causes RegistryUpdate to return the
	// most recent known value for a lookup together with a ErrSameRevNum error.
	DependencyRegistryUpdateLyingHost struct {
		skymodules.SkynetDependencies
	}
	// DependencyRenewFail causes the renewal to fail on the host side.
	DependencyRenewFail struct {
		skymodules.SkynetDependencies
	}
	// DependencyDisableWorker will disable the worker's work loop, the health
	// loop, the repair loop and the snapshot loop.
	DependencyDisableWorker struct {
		skymodules.SkynetDependencies
	}
	// DependencyDisableHostSiamux will disable siamux in the host.
	DependencyDisableHostSiamux struct {
		skymodules.SkynetDependencies
	}
	// DependencyStorageObligationNotFound will cause the host to return that it
	// wasn't able to find a storage obligation in managedPayByContract.
	DependencyStorageObligationNotFound struct {
		skymodules.SkynetDependencies
	}

	// DependencyPreventEARefill prevents EAs from being refilled automatically.
	DependencyPreventEARefill struct {
		skymodules.SkynetDependencies
	}

	// DependencyLowFundsFormationFail will cause contract formation to fail due
	// to low funds in the allowance.
	DependencyLowFundsFormationFail struct {
		skymodules.SkynetDependencies
	}

	// DependencyLowFundsRenewalFail will cause contract renewal to fail due to
	// low funds in the allowance.
	DependencyLowFundsRenewalFail struct {
		skymodules.SkynetDependencies
	}

	// DependencyLowFundsRefreshFail will cause contract renewal to fail due to
	// low funds in the allowance.
	DependencyLowFundsRefreshFail struct {
		skymodules.SkynetDependencies
	}

	// DependencyDisableAsyncStartup prevents the async part of a module's
	// creation from being executed.
	DependencyDisableAsyncStartup struct {
		skymodules.SkynetDependencies
	}

	// DependencyDisableCriticalOnMaxBalance prevents a build.Critical to be
	// thrown when we encounter a `MaxBalanceExceeded` error on the host
	DependencyDisableCriticalOnMaxBalance struct {
		skymodules.SkynetDependencies
	}

	// DependencyDisableStreamClose prevents the stream from being closed.
	DependencyDisableStreamClose struct {
		skymodules.SkynetDependencies
	}

	// DependencyDisableContractRecovery prevents recoverable contracts from
	// being recovered in threadedContractMaintenance.
	DependencyDisableContractRecovery struct {
		skymodules.SkynetDependencies
	}

	// DependencyDisableRecoveryStatusReset prevents the fields scanInProgress
	// and atomicRecoveryScanHeight from being reset after the scan is done.
	DependencyDisableRecoveryStatusReset struct {
		skymodules.SkynetDependencies
	}

	// DependencyDisableRenewal prevents contracts from being renewed.
	DependencyDisableRenewal struct {
		skymodules.SkynetDependencies
	}

	// DependencySkipDeleteContractAfterRenewal prevents the old contract from
	// being deleted after a renewal.
	DependencySkipDeleteContractAfterRenewal struct {
		skymodules.SkynetDependencies
	}

	// DependencyTimeoutOnHostGET times out when the client performs the HTTP
	// call to GET /host.
	DependencyTimeoutOnHostGET struct {
		skymodules.SkynetDependencies
	}

	// DependencyInterruptOnceOnKeyword is a generic dependency that interrupts
	// the flow of the program if the argument passed to Disrupt equals str and
	// if f was set to true by calling Fail.
	DependencyInterruptOnceOnKeyword struct {
		f bool // indicates if the next download should fail
		skymodules.SkynetDependencies
		mu  sync.Mutex
		str string
	}

	// DependencyInterruptAfterNCalls is a generic dependency that behaves the
	// same way as DependencyInterruptOnceOnKeyword, expect that after calling
	// "Fail", "Disrupt" needs to be called n times for the actual disrupt to
	// happen.
	DependencyInterruptAfterNCalls struct {
		DependencyInterruptOnceOnKeyword
		n    int
		cntr int
	}

	// DependencyPostponeWritePiecesRecovery adds a random sleep in the WritePieces
	// method between calling Seek and Recover as a regression test for randomly
	// corrupting downloads.
	DependencyPostponeWritePiecesRecovery struct {
		skymodules.SkynetDependencies
	}

	// DependencyInterruptAccountSaveOnShutdown will interrupt the account save
	// when the renter shuts down.
	DependencyInterruptAccountSaveOnShutdown struct {
		skymodules.SkynetDependencies
	}

	// DependencyNoSnapshotSyncInterruptAccountSaveOnShutdown will interrupt the
	// account save when the renter shuts down and also disable the snapshot
	// syncing thread.
	DependencyNoSnapshotSyncInterruptAccountSaveOnShutdown struct {
		skymodules.SkynetDependencies
	}

	// DependencyBlockResumeJobDownloadUntilTimeout blocks in
	// managedResumeJobDownloadByRoot until the timeout for the download project
	// is reached.
	DependencyBlockResumeJobDownloadUntilTimeout struct {
		DependencyTimeoutProjectDownloadByRoot
		c chan struct{}
	}

	// DependencyDisableRotateFingerprintBuckets prevents rotation of the
	// fingerprint buckets on disk.
	DependencyDisableRotateFingerprintBuckets struct {
		skymodules.SkynetDependencies
	}

	// DependencyDefaultRenewSettings causes the contractor to use default
	// settings when renewing a contract.
	DependencyDefaultRenewSettings struct {
		skymodules.SkynetDependencies
		enabled bool
		mu      sync.Mutex
	}

	// DependencyResolveSkylinkToFixture will disable downloading skylinks and
	// will replace it with fetching from a set of predefined fixtures.
	DependencyResolveSkylinkToFixture struct {
		skymodules.SkynetDependencies
	}

	// DependencyMultiDisrupt is a generic dependency that will disrupt the
	// normal code flow based on multiple keywords.
	DependencyMultiDisrupt struct {
		skymodules.SkynetDependencies
		strs []string
	}
)

// NewDependencyDelayRegistryHealthResponses simulates a delay to the
// staticComplete time of all responses in managedRegistryEntryHealth.`
func NewDependencyDelayRegistryHealthResponses() *DependencyWithDisableAndEnable {
	return newDependencywithDisableAndEnable("DelayRegistryHealthResponses")
}

// NewDependencyOverdriveDownload is a dependency that will overdrive at least
// one worker on every download.
func NewDependencyOverdriveDownload() *DependencyWithDisableAndEnable {
	return newDependencywithDisableAndEnable("OverdriveDownload")
}

// NewDependencyTUSConnectionDrop creates a new DependencyTUSConnectionDrop
// which simulates a dropped connection during a TUS upload.
func NewDependencyTUSConnectionDrop() *DependencyInterruptOnceOnKeyword {
	return newDependencyInterruptOnceOnKeyword("TUSConnectionDropped")
}

// NewDependencyCorruptMDMOutput returns a dependency that can be used to
// manually corrupt the MDM output returned by hosts.
func NewDependencyCorruptMDMOutput() *DependencyInterruptOnceOnKeyword {
	return newDependencyInterruptOnceOnKeyword("CorruptMDMOutput")
}

// NewDependencyCorruptReadSector returns a dependency that can be used to
// ensure ReadSector instructions on the host fail due to corruption of the MDM
// output.
//
// NOTE: this dependency is very similar to 'NewDependencyCorruptMDMOutput' and
// even uses the same disrupt string, the difference is that this is an
// enable-disable, and not interrupt once.
func NewDependencyCorruptReadSector() *DependencyWithDisableAndEnable {
	return newDependencywithDisableAndEnable("CorruptMDMOutput")
}

// NewDependencyBlockResumeJobDownloadUntilTimeout blocks in
// managedResumeJobDownloadByRoot until the timeout for the download project is
// reached.
func NewDependencyBlockResumeJobDownloadUntilTimeout() skymodules.SkydDependencies {
	return &DependencyBlockResumeJobDownloadUntilTimeout{
		c: make(chan struct{}),
	}
}

// NewDependencyContractRenewalFail creates a new dependency that simulates
// getting an error while renewing a contract.
func NewDependencyContractRenewalFail() *DependencyWithDisableAndEnable {
	return newDependencywithDisableAndEnable("ContractRenewFail")
}

// NewDependencySkyfileUploadFail creates a new dependency that simulates
// getting an error while uploading a skyfile.
func NewDependencySkyfileUploadFail() *DependencyWithDisableAndEnable {
	return newDependencywithDisableAndEnable("SkyfileUploadFail")
}

// NewDependencyCustomResolver creates a dependency from a given lookupIP
// method which returns a custom resolver that uses the specified lookupIP
// method to resolve hostnames.
func NewDependencyCustomResolver(lookupIP func(string) ([]net.IP, error)) modules.Dependencies {
	return &dependencyCustomResolver{lookupIP: lookupIP}
}

// NewDependencyDisruptUploadStream creates a new dependency that closes the
// reader used for upload streaming to simulate failing connection after
// numChunks uploaded chunks.
func NewDependencyDisruptUploadStream(numChunks int) *DependencyInterruptAfterNCalls {
	return newDependencyInterruptAfterNCalls("DisruptUploadStream", numChunks)
}

// NewDependencyDisableCommitPaymentIntent creates a new dependency that
// prevents the contractor for committing a payment intent, this essentially
// ensures the renter's revision is not in sync with the host's revision.
func NewDependencyDisableCommitPaymentIntent() *DependencyWithDisableAndEnable {
	return newDependencywithDisableAndEnable("DisableCommitPaymentIntent")
}

// NewDependencyInterruptContractSaveToDiskAfterDeletion creates a new
// dependency that interrupts the contract being saved to disk after being
// removed from static contracts
func NewDependencyInterruptContractSaveToDiskAfterDeletion() *DependencyInterruptOnceOnKeyword {
	return newDependencyInterruptOnceOnKeyword("InterruptContractSaveToDiskAfterDeletion")
}

// NewDependencyInterruptDownloadBeforeSendingRevision creates a new dependency
// that interrupts the download on the renter side before sending the signed
// revision to the host.
func NewDependencyInterruptDownloadBeforeSendingRevision() *DependencyInterruptOnceOnKeyword {
	return newDependencyInterruptOnceOnKeyword("InterruptDownloadBeforeSendingRevision")
}

// NewDependencyInterruptDownloadAfterSendingRevision creates a new dependency
// that interrupts the download on the renter side right after receiving the
// signed revision from the host.
func NewDependencyInterruptDownloadAfterSendingRevision() *DependencyInterruptOnceOnKeyword {
	return newDependencyInterruptOnceOnKeyword("InterruptDownloadAfterSendingRevision")
}

// NewDependencyInterruptNewStreamTimeout a dependency that interrupts
// interaction with a stream by timing out on trying to create a new stream with
// the host.
func NewDependencyInterruptNewStreamTimeout() *DependencyWithDisableAndEnable {
	return newDependencywithDisableAndEnable("InterruptNewStreamTimeout")
}

// NewDependencyInterruptHostScan is a dependency that interrupts the host scan
// and ensures hosts will be considered offline if the dependency is enabled.
func NewDependencyInterruptHostScan() *DependencyWithDisableAndEnable {
	return newDependencywithDisableAndEnable("InterruptHostScan")
}

// NewDependencyInterruptUploadBeforeSendingRevision creates a new dependency
// that interrupts the upload on the renter side before sending the signed
// revision to the host.
func NewDependencyInterruptUploadBeforeSendingRevision() *DependencyInterruptOnceOnKeyword {
	return newDependencyInterruptOnceOnKeyword("InterruptUploadBeforeSendingRevision")
}

// NewDependencyInterruptUploadAfterSendingRevision creates a new dependency
// that interrupts the upload on the renter side right after receiving the
// signed revision from the host.
func NewDependencyInterruptUploadAfterSendingRevision() *DependencyInterruptOnceOnKeyword {
	return newDependencyInterruptOnceOnKeyword("InterruptUploadAfterSendingRevision")
}

// newDependencyInterruptOnceOnKeyword creates a new
// DependencyInterruptOnceOnKeyword from a given disrupt key.
func newDependencyInterruptOnceOnKeyword(str string) *DependencyInterruptOnceOnKeyword {
	return &DependencyInterruptOnceOnKeyword{
		str: str,
	}
}

// newDependencyInterruptAfterNCalls creates a new
// DependencyInterruptAfterNCalls from a given disrupt key and n.
func newDependencyInterruptAfterNCalls(str string, n int) *DependencyInterruptAfterNCalls {
	return &DependencyInterruptAfterNCalls{
		DependencyInterruptOnceOnKeyword: DependencyInterruptOnceOnKeyword{
			str: str,
		},
		n: n,
	}
}

// NewDependencyHostBlockRPC creates a new dependency that can be used to
// simulate an unresponsive host.
func NewDependencyHostBlockRPC() *DependencyWithDisableAndEnable {
	return newDependencywithDisableAndEnable("HostBlockRPC")
}

// NewDependencyHostLosePriceTable creates a dependency, that causes
// the host to act is if it can not find a price table for given UID.
func NewDependencyHostLosePriceTable() *DependencyWithDisableAndEnable {
	return newDependencywithDisableAndEnable("HostLosePriceTable")
}

// NewDependencyReadRegistryNoEntry creates a new dependency that can be used to
// simulate a host losing a registry entry.
func NewDependencyReadRegistryNoEntry() *DependencyWithDisableAndEnable {
	return newDependencywithDisableAndEnable("ReadRegistryNoEntry")
}

// NewDependencyRegistryUpdateNoOp creates a dependency, that causes
// RegistryUpdate to be a no-op.
func NewDependencyRegistryUpdateNoOp() *DependencyWithDisableAndEnable {
	return newDependencywithDisableAndEnable("RegistryUpdateNoOp")
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyRegistryUpdateLyingHost) Disrupt(s string) bool {
	return s == "RegistryUpdateLyingHost"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyInvalidateStatsCache) Disrupt(s string) bool {
	return s == "DisableInvalidateStatsCache"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyRenewFail) Disrupt(s string) bool {
	return s == "RenewFail"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyDisableWorker) Disrupt(s string) bool {
	if s == "DisableWorkerLoop" {
		return true
	}
	if s == "DisableRepairAndHealthLoops" {
		return true
	}
	if s == "DisableSnapshotSync" {
		return true
	}
	if s == "DisableSubscriptionLoop" {
		return true
	}
	return false
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyReadRegistryBlocking) Disrupt(s string) bool {
	return s == "ReadRegistryBlocking"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyForceLegacyRepairDownload) Disrupt(s string) bool {
	return s == "ForceLegacyRepairDownload"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyFailLegacyRepairDownload) Disrupt(s string) bool {
	return s == "FailLegacyRepairDownload"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyDelayChunkDistribution) Disrupt(s string) bool {
	return s == "DelayChunkDistribution"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyAcceptHostRevision) Disrupt(s string) bool {
	return s == "AcceptHostRevision"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyUnstableTUSUpload) Disrupt(s string) bool {
	return s == "TUSUnstable" || s == "TUSNoPrune"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyLegacyRenew) Disrupt(s string) bool {
	return s == "LegacyRenew"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyNoSnapshotSyncInterruptAccountSaveOnShutdown) Disrupt(s string) bool {
	if s == "InterruptAccountSaveOnShutdown" {
		return true
	}
	if s == "DisableSnapshotSync" {
		return true
	}
	return false
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyStandardUploadRedundancy) Disrupt(s string) bool {
	return s == "StandardUploadRedundancy"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyNoSnapshotSync) Disrupt(s string) bool {
	return s == "DisableSnapshotSync"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyStorageObligationNotFound) Disrupt(s string) bool {
	return s == "StorageObligationNotFound"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyPreventEARefill) Disrupt(s string) bool {
	return s == "DisableFunding"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyBlockResumeJobDownloadUntilTimeout) Disrupt(s string) bool {
	if s == "BlockUntilTimeout" {
		<-d.c
		return true
	} else if s == "ResumeOnTimeout" {
		close(d.c)
		return true
	}
	return false
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyDisableCriticalOnMaxBalance) Disrupt(s string) bool {
	return s == "DisableCriticalOnMaxBalance"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyDisableAsyncStartup) Disrupt(s string) bool {
	return s == "BlockAsyncStartup"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyDisableHostSiamux) Disrupt(s string) bool {
	return s == "DisableHostSiamux"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyDisableStreamClose) Disrupt(s string) bool {
	return s == "DisableStreamClose"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencySkipDeleteContractAfterRenewal) Disrupt(s string) bool {
	return s == "SkipContractDeleteAfterRenew" || s == "DisableContractRecovery"
}

// Disrupt causes contract formation to fail due to low allowance funds.
func (d *DependencyLowFundsFormationFail) Disrupt(s string) bool {
	return s == "LowFundsFormation"
}

// Disrupt causes contract renewal to fail due to low allowance funds.
func (d *DependencyLowFundsRenewalFail) Disrupt(s string) bool {
	return s == "LowFundsRenewal"
}

// Disrupt causes contract renewal to fail due to low allowance funds.
func (d *DependencyLowFundsRefreshFail) Disrupt(s string) bool {
	return s == "LowFundsRefresh"
}

// Disrupt causes contract renewal to not clear the contents of a contract.
func (d *DependencyInterruptAccountSaveOnShutdown) Disrupt(s string) bool {
	return s == "InterruptAccountSaveOnShutdown"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyDisableRotateFingerprintBuckets) Disrupt(s string) bool {
	return s == "DisableRotateFingerprintBuckets"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyTimeoutOnHostGET) Disrupt(s string) bool {
	return s == "TimeoutOnHostGET"
}

// Disrupt returns true if the correct string is provided and if the flag was
// set to true by calling fail on the dependency beforehand. After simulating a
// crash the flag will be set to false and fail has to be called again for
// another disruption.
func (d *DependencyInterruptOnceOnKeyword) Disrupt(s string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f && s == d.str {
		d.f = false
		return true
	}
	return false
}

// Disrupt returns true if the correct string is provided, if the flag was set
// to true by calling fail on the dependency and if Disrupt has been called n
// times since fail was called.
func (d *DependencyInterruptAfterNCalls) Disrupt(s string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f && s == d.str && d.cntr == d.n {
		d.f = false
		d.cntr = 0
		return true
	} else if d.f && s == d.str && d.cntr < d.n {
		d.cntr++
	}
	return false
}

// Fail causes the next call to Disrupt to return true if the correct string is
// provided.
func (d *DependencyInterruptOnceOnKeyword) Fail() {
	d.mu.Lock()
	d.f = true
	d.mu.Unlock()
}

// Disable sets the flag to false to make sure that the dependency won't fail.
func (d *DependencyInterruptOnceOnKeyword) Disable() {
	d.mu.Lock()
	d.f = false
	d.mu.Unlock()
}

// Disrupt prevents contracts from being recovered in
// threadedContractMaintenance.
func (d *DependencyDisableContractRecovery) Disrupt(s string) bool {
	return s == "DisableContractRecovery"
}

// Disrupt will prevent the fields scanInProgress and atomicRecoveryScanHeight
// from being reset after the scan is done and also prevent automatic contract
// recovery scans from being triggered.
func (d *DependencyDisableRecoveryStatusReset) Disrupt(s string) bool {
	return s == "disableRecoveryStatusReset" || s == "disableAutomaticContractRecoveryScan"
}

// Disrupt will prevent contracts from being renewed.
func (d *DependencyDisableRenewal) Disrupt(s string) bool {
	return s == "disableRenew"
}

// Disrupt returns true if the correct string is provided.
func (d *DependencyPostponeWritePiecesRecovery) Disrupt(s string) bool {
	return s == "PostponeWritePiecesRecovery"
}

type (
	// customResolver is a testing resolver which can be created from any
	// lookupIP method.
	customResolver struct {
		lookupIP func(string) ([]net.IP, error)
	}
	// dependencyCustomResolver is a dependency which overrides the Resolver
	// method to return a custom resolver with a specific lookupIP method.
	dependencyCustomResolver struct {
		skymodules.SkynetDependencies
		lookupIP func(string) ([]net.IP, error)
	}
)

// LookupIP implements the modules.Resolver interface.
func (cr customResolver) LookupIP(host string) ([]net.IP, error) {
	return cr.lookupIP(host)
}

// Disrupt makes sure that hosts which resolve to addresses we can't connect to
// due to the customResolver will be online in the hostdb.
func (d *dependencyCustomResolver) Disrupt(s string) bool {
	return s == "customResolver"
}

// Resolver creates a new custom resolver.
func (d *dependencyCustomResolver) Resolver() modules.Resolver {
	return customResolver{d.lookupIP}
}

// DependencyAddLatency will introduce a latency by sleeping for the
// specified duration if the argument passed to Distrupt equals str.
type DependencyAddLatency struct {
	str      string
	duration time.Duration
	skymodules.SkynetDependencies
}

// newDependencyAddLatency creates a new DependencyAddLatency from a given
// disrupt string and duration
func newDependencyAddLatency(str string, d time.Duration) *DependencyAddLatency {
	return &DependencyAddLatency{
		str:      str,
		duration: d,
	}
}

// Disrupt will sleep for the specified duration if the correct string is
// provided.
func (d *DependencyAddLatency) Disrupt(s string) bool {
	if s == d.str {
		time.Sleep(d.duration)
		return true
	}
	return false
}

// Disrupt causes the contractor to use default host settings
// when renewing a contract.
func (d *DependencyDefaultRenewSettings) Disrupt(s string) bool {
	d.mu.Lock()
	enabled := d.enabled
	d.mu.Unlock()
	return enabled && s == "DefaultRenewSettings"
}

// Enable enables the dependency.
func (d *DependencyDefaultRenewSettings) Enable() {
	d.mu.Lock()
	d.enabled = true
	d.mu.Unlock()
}

// Disable disables the dependency.
func (d *DependencyDefaultRenewSettings) Disable() {
	d.mu.Lock()
	d.enabled = false
	d.mu.Unlock()
}

// Disrupt causes skylink data to be loaded from fixtures instead of downloaded.
func (d *DependencyResolveSkylinkToFixture) Disrupt(s string) bool {
	return s == "resolveSkylinkToFixture"
}

// DependencyWithDisableAndEnable adds the ability to disable the dependency
type DependencyWithDisableAndEnable struct {
	disabled bool
	skymodules.SkynetDependencies
	mu  sync.Mutex
	str string
}

// newDependencywithDisableAndEnable creates a new
// DependencyWithDisableAndEnable from a given disrupt key.
func newDependencywithDisableAndEnable(str string) *DependencyWithDisableAndEnable {
	return &DependencyWithDisableAndEnable{
		str: str,
	}
}

// Disrupt returns true if the correct string is provided and the dependency has
// not been disabled.
func (d *DependencyWithDisableAndEnable) Disrupt(s string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.disabled && s == d.str
}

// Disable sets the flag to true to make sure that the dependency will fail.
func (d *DependencyWithDisableAndEnable) Disable() {
	d.mu.Lock()
	d.disabled = true
	d.mu.Unlock()
}

// Enable sets the flag to false to make sure that the dependency won't fail.
func (d *DependencyWithDisableAndEnable) Enable() {
	d.mu.Lock()
	d.disabled = false
	d.mu.Unlock()
}

// newDependencyMultiDisrupt creates a new DependencyMultiDistrupt from a given
// disrupt key set.
func newDependencyMultiDisrupt(strs []string) *DependencyMultiDisrupt {
	return &DependencyMultiDisrupt{
		strs: strs,
	}
}

// Disrupt returns true if the provided string is one of the disrupt strings.
func (d *DependencyMultiDisrupt) Disrupt(s string) bool {
	for _, str := range d.strs {
		if str == s {
			return true
		}
	}
	return false
}
