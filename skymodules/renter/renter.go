// Package renter is responsible for uploading and downloading files on the sia
// network.
package renter

// TODO: Allow the 'baseMemory' to be set by the user.
//
// TODO: The repair loop currently receives new upload jobs through a channel.
// The download loop has a better model, a heap that can be pushed to and popped
// from concurrently without needing complex channel communication. Migrating
// the renter to this model should clean up some of the places where uploading
// bottlenecks, and reduce the amount of channel-ninjitsu required to make the
// uploading function.
//
// TODO: Allow user to configure the packet size when ratelimiting the renter.
// Currently the default is set to 16kb. That's going to require updating the
// API and extending the settings object, and then tweaking the
// setBandwidthLimits function.
//
// TODO: Currently 'callUpdate()' is used after setting the allowance, though
// this doesn't guarantee that anything interesting will happen because the
// contractor's 'threadedContractMaintenance' will run in the background and
// choose to update the hosts and contracts. Really, we should have the
// contractor notify the renter whenever there has been a change in the contract
// set so that 'callUpdate()' can be used. Implementation in renter.SetSettings.

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/ratelimit"
	"gitlab.com/NebulousLabs/siamux"
	"gitlab.com/NebulousLabs/threadgroup"
	"gitlab.com/NebulousLabs/writeaheadlog"

	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skykey"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/contractor"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/hostdb"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/skynetblocklist"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/skynetportals"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/persist"
	siasync "go.sia.tech/siad/sync"
	"go.sia.tech/siad/types"
)

var (
	// skynetFeePayoutMultiplier is a factor that we multiply the fee estimation
	// with to determine the skynet fee payout threshold.
	skynetFeePayoutMultiplier = build.Select(build.Var{
		Dev:      uint64(100 * 1024), // 100 * 1kib txn
		Standard: uint64(100 * 1024), // 100 * 1kib txn
		Testing:  uint64(1),          // threshold == fee estimate
	}).(uint64)
)
var (
	errNilContractor = errors.New("cannot create renter with nil contractor")
	errNilCS         = errors.New("cannot create renter with nil consensus set")
	errNilGateway    = errors.New("cannot create hostdb with nil gateway")
	errNilHdb        = errors.New("cannot create renter with nil hostdb")
	errNilTpool      = errors.New("cannot create renter with nil transaction pool")
	errNilWallet     = errors.New("cannot create renter with nil wallet")
)

// A hostContractor negotiates, revises, renews, and provides access to file
// contracts.
type hostContractor interface {
	modules.Alerter

	// SetAllowance sets the amount of money the contractor is allowed to
	// spend on contracts over a given time period, divided among the number
	// of hosts specified. Note that contractor can start forming contracts as
	// soon as SetAllowance is called; that is, it may block.
	SetAllowance(skymodules.Allowance) error

	// Allowance returns the current allowance
	Allowance() skymodules.Allowance

	// Close closes the hostContractor.
	Close() error

	// CancelContract cancels the Renter's contract
	CancelContract(id types.FileContractID) error

	// Contracts returns the staticContracts of the renter's hostContractor.
	Contracts() []skymodules.RenterContract

	// ContractByPublicKey returns the contract associated with the host key.
	ContractByPublicKey(types.SiaPublicKey) (skymodules.RenterContract, bool)

	// ContractPublicKey returns the public key capable of verifying the renter's
	// signature on a contract.
	ContractPublicKey(pk types.SiaPublicKey) (crypto.PublicKey, bool)

	// ChurnStatus returns contract churn stats for the current period.
	ChurnStatus() skymodules.ContractorChurnStatus

	// ContractUtility returns the utility field for a given contract, along
	// with a bool indicating if it exists.
	ContractUtility(types.SiaPublicKey) (skymodules.ContractUtility, bool)

	// ContractStatus returns the status of the given contract within the
	// watchdog.
	ContractStatus(fcID types.FileContractID) (skymodules.ContractWatchStatus, bool)

	// CurrentPeriod returns the height at which the current allowance period
	// began.
	CurrentPeriod() types.BlockHeight

	// InitRecoveryScan starts scanning the whole blockchain for recoverable
	// contracts within a separate thread.
	InitRecoveryScan() error

	// PeriodSpending returns the amount spent on contracts during the current
	// billing period.
	PeriodSpending() (skymodules.ContractorSpending, error)

	// ProvidePayment takes a stream and a set of payment details and handles
	// the payment for an RPC by sending and processing payment request and
	// response objects to the host. It returns an error in case of failure.
	ProvidePayment(stream io.ReadWriter, pt *modules.RPCPriceTable, details contractor.PaymentDetails) error

	// OldContracts returns the oldContracts of the renter's hostContractor.
	OldContracts() []skymodules.RenterContract

	// Editor creates an Editor from the specified contract ID, allowing the
	// insertion, deletion, and modification of sectors.
	Editor(types.SiaPublicKey, <-chan struct{}) (contractor.Editor, error)

	// IsOffline reports whether the specified host is considered offline.
	IsOffline(types.SiaPublicKey) bool

	// Downloader creates a Downloader from the specified contract ID,
	// allowing the retrieval of sectors.
	Downloader(types.SiaPublicKey, <-chan struct{}) (contractor.Downloader, error)

	// Session creates a Session from the specified contract ID.
	Session(types.SiaPublicKey, <-chan struct{}) (contractor.Session, error)

	// RecoverableContracts returns the contracts that the contractor deems
	// recoverable. That means they are not expired yet and also not part of the
	// active contracts. Usually this should return an empty slice unless the host
	// isn't available for recovery or something went wrong.
	RecoverableContracts() []skymodules.RecoverableContract

	// RecoveryScanStatus returns a bool indicating if a scan for recoverable
	// contracts is in progress and if it is, the current progress of the scan.
	RecoveryScanStatus() (bool, types.BlockHeight)

	// RefreshedContract checks if the contract was previously refreshed
	RefreshedContract(fcid types.FileContractID) bool

	// RenewContract takes an established connection to a host and renews the
	// given contract with that host.
	RenewContract(conn net.Conn, fcid types.FileContractID, params skymodules.ContractParams, txnBuilder modules.TransactionBuilder, tpool modules.TransactionPool, hdb skymodules.HostDB, pt *modules.RPCPriceTable) (skymodules.RenterContract, []types.Transaction, error)

	// Synced returns a channel that is closed when the contractor is fully
	// synced with the peer-to-peer network.
	Synced() <-chan struct{}

	// UpdateWorkerPool updates the workerpool currently in use by the contractor.
	UpdateWorkerPool(skymodules.WorkerPool)
}

type renterFuseManager interface {
	// Mount mounts the files under the specified siapath under the 'mountPoint' folder on
	// the local filesystem.
	Mount(mountPoint string, sp skymodules.SiaPath, opts skymodules.MountOptions) (err error)

	// MountInfo returns the list of currently mounted fuse filesystems.
	MountInfo() []skymodules.MountInfo

	// Unmount unmounts the fuse filesystem currently mounted at mountPoint.
	Unmount(mountPoint string) error
}

// A siacoinSender is an object capable of sending siacoins to an address.
type siacoinSender interface {
	// SendSiacoins sends the specified amount of siacoins to the provided
	// address.
	SendSiacoins(types.Currency, types.UnlockHash) ([]types.Transaction, error)
}

// cachedUtilities contains the cached utilities used when bubbling file and
// folder metadata.
type cachedUtilities struct {
	offline      map[string]bool
	goodForRenew map[string]bool
	contracts    map[string]skymodules.RenterContract
	used         []types.SiaPublicKey
}

// A Renter is responsible for tracking all of the files that a user has
// uploaded to Sia, as well as the locations and health of these files.
type Renter struct {
	// An atomic variable to export the estimated system scan duration from the
	// health loop code to the renter. We use a uint64 because that's what's
	// friendly to the atomic package, but actually it's a time.Duration.
	atomicSystemHealthScanDuration uint64

	// Skynet Management
	staticSkylinkManager    *skylinkManager
	staticSkynetBlocklist   *skynetblocklist.SkynetBlocklist
	staticSkynetPortals     *skynetportals.SkynetPortals
	staticSpendingHistory   *spendingHistory
	staticSkynetTUSUploader *skynetTUSUploader

	// Download management.
	staticDownloadHeap *downloadHeap
	newDownloads       chan struct{} // Used to notify download loop that new downloads are available.

	// Download history.
	//
	// TODO: Currently the download history doesn't include repair-initiated
	// downloads, and instead only contains user-initiated downloads.
	staticDownloadHistory *downloadHistory

	// Upload and repair management.
	staticDirectoryHeap directoryHeap
	staticStuckStack    stuckStack
	staticUploadHeap    uploadHeap

	// Registry repair related fields.
	ongoingRegistryRepairs   map[modules.RegistryEntryID]struct{}
	ongoingRegistryRepairsMu sync.Mutex

	// Cache the hosts from the last price estimation result.
	lastEstimationHosts []skymodules.HostDBEntry

	// cachedUtilities contain contract information used when calculating
	// metadata information about the filesystem, such as health. This
	// information is used in various functions such as listing filesystem
	// information and updating directory metadata.  These values are cached to
	// prevent recomputing them too often.
	cachedUtilities cachedUtilities

	repairingChunksMu sync.Mutex
	repairingChunks   map[uploadChunkID]*unfinishedUploadChunk

	// staticSubscriptionManager is the global manager of registry
	// subscriptions.
	staticSubscriptionManager *registrySubscriptionManager

	// The renter's bandwidth ratelimit.
	staticRL *ratelimit.RateLimit

	// stats cache related fields.
	statsChan chan struct{}
	statsMu   sync.Mutex

	// various performance stats
	staticBaseSectorDownloadStats   *skymodules.DownloadOverdriveStats
	staticBaseSectorUploadStats     *skymodules.DistributionTracker
	staticChunkUploadStats          *skymodules.DistributionTracker
	staticFanoutSectorDownloadStats *skymodules.DownloadOverdriveStats
	staticRegistryReadStats         *skymodules.DistributionTracker
	staticRegWriteStats             *skymodules.DistributionTracker
	staticStreamBufferStats         *skymodules.DistributionTracker

	// Memory management
	//
	// staticRegistryMemoryManager is used for updating registry entries and reading
	// them.
	//
	// staticUserUploadManager is used for user-initiated uploads
	//
	// staticUserDownloadMemoryManager is used for user-initiated downloads
	//
	// staticRepairMemoryManager is used for repair work scheduled by siad
	//
	staticMemoryManager             *memoryManager
	staticRegistryMemoryManager     *memoryManager
	staticRepairMemoryManager       *memoryManager
	staticUserDownloadMemoryManager *memoryManager
	staticUserUploadMemoryManager   *memoryManager

	// Modules and subsystems
	staticAccountManager               *accountManager
	staticAlerter                      *modules.GenericAlerter
	staticConsensusSet                 modules.ConsensusSet
	staticDirUpdateBatcher             *dirUpdateBatcher
	staticFileSystem                   *filesystem.FileSystem
	staticFuseManager                  renterFuseManager
	staticGateway                      modules.Gateway
	staticHostContractor               hostContractor
	staticHostDB                       skymodules.HostDB
	staticSkykeyManager                *skykey.SkykeyManager
	staticStreamBufferSet              *streamBufferSet
	staticTPool                        modules.TransactionPool
	staticUploadChunkDistributionQueue *uploadChunkDistributionQueue
	staticWallet                       modules.Wallet
	staticWorkerPool                   *workerPool

	// Utilities
	persist         persistence
	persistDir      string
	mu              *siasync.RWMutex
	staticDeps      skymodules.SkydDependencies
	staticLog       *persist.Logger
	staticMux       *siamux.SiaMux
	staticRepairLog *persist.Logger
	staticWAL       *writeaheadlog.WAL
	tg              threadgroup.ThreadGroup
}

// Close closes the Renter and its dependencies
func (r *Renter) Close() error {
	// TODO: Is this check needed?
	if r == nil {
		return nil
	}

	return errors.Compose(r.tg.Stop(), r.staticHostDB.Close(), r.staticHostContractor.Close(), r.staticSkynetBlocklist.Close(), r.staticSkynetPortals.Close())
}

// MemoryStatus returns the current status of the memory manager
func (r *Renter) MemoryStatus() (skymodules.MemoryStatus, error) {
	if err := r.tg.Add(); err != nil {
		return skymodules.MemoryStatus{}, err
	}
	defer r.tg.Done()

	repairStatus := r.staticRepairMemoryManager.callStatus()
	userDownloadStatus := r.staticUserDownloadMemoryManager.callStatus()
	userUploadStatus := r.staticUserUploadMemoryManager.callStatus()
	registryStatus := r.staticRegistryMemoryManager.callStatus()
	total := repairStatus.Add(userDownloadStatus).Add(userUploadStatus).Add(registryStatus)
	return skymodules.MemoryStatus{
		MemoryManagerStatus: total,

		Registry:     registryStatus,
		System:       repairStatus,
		UserDownload: userDownloadStatus,
		UserUpload:   userUploadStatus,
	}, nil
}

// PriceEstimation estimates the cost in siacoins of performing various storage
// and data operations.  The estimation will be done using the provided
// allowance, if an empty allowance is provided then the renter's current
// allowance will be used if one is set.  The final allowance used will be
// returned.
func (r *Renter) PriceEstimation(allowance skymodules.Allowance) (skymodules.RenterPriceEstimation, skymodules.Allowance, error) {
	if err := r.tg.Add(); err != nil {
		return skymodules.RenterPriceEstimation{}, skymodules.Allowance{}, err
	}
	defer r.tg.Done()
	// Use provide allowance. If no allowance provided use the existing
	// allowance. If no allowance exists, use a sane default allowance.
	if reflect.DeepEqual(allowance, skymodules.Allowance{}) {
		rs, err := r.Settings()
		if err != nil {
			return skymodules.RenterPriceEstimation{}, skymodules.Allowance{}, errors.AddContext(err, "error getting renter settings:")
		}
		allowance = rs.Allowance
		if reflect.DeepEqual(allowance, skymodules.Allowance{}) {
			allowance = skymodules.DefaultAllowance
		}
	}

	// Get hosts for estimate
	var hosts []skymodules.HostDBEntry
	hostmap := make(map[string]struct{})

	// Start by grabbing hosts from contracts
	// Get host pubkeys from contracts
	contracts := r.Contracts()
	var pks []types.SiaPublicKey
	for _, c := range contracts {
		u, ok := r.ContractUtility(c.HostPublicKey)
		if !ok {
			continue
		}
		// Check for active contracts only
		if !u.GoodForRenew {
			continue
		}
		pks = append(pks, c.HostPublicKey)
	}
	// Get hosts from pubkeys
	for _, pk := range pks {
		host, ok, err := r.staticHostDB.Host(pk)
		if !ok || host.Filtered || err != nil {
			continue
		}
		// confirm host wasn't already added
		if _, ok := hostmap[host.PublicKey.String()]; ok {
			continue
		}
		hosts = append(hosts, host)
		hostmap[host.PublicKey.String()] = struct{}{}
	}
	// Add hosts from previous estimate cache if needed
	if len(hosts) < int(allowance.Hosts) {
		id := r.mu.Lock()
		cachedHosts := r.lastEstimationHosts
		r.mu.Unlock(id)
		for _, host := range cachedHosts {
			// confirm host wasn't already added
			if _, ok := hostmap[host.PublicKey.String()]; ok {
				continue
			}
			hosts = append(hosts, host)
			hostmap[host.PublicKey.String()] = struct{}{}
		}
	}
	// Add random hosts if needed
	if len(hosts) < int(allowance.Hosts) {
		// Re-initialize the list with SiaPublicKeys to hold the public keys from the current
		// set of hosts. This list will be used as address filter when requesting random hosts.
		var pks []types.SiaPublicKey
		for _, host := range hosts {
			pks = append(pks, host.PublicKey)
		}
		// Grab hosts to perform the estimation.
		var err error
		randHosts, err := r.staticHostDB.RandomHostsWithAllowance(int(allowance.Hosts)-len(hosts), pks, pks, allowance)
		if err != nil {
			return skymodules.RenterPriceEstimation{}, allowance, errors.AddContext(err, "could not generate estimate, could not get random hosts")
		}
		// As the returned random hosts are checked for IP violations and double entries against the current
		// slice of hosts, the returned hosts can be safely added to the current slice.
		hosts = append(hosts, randHosts...)
	}
	// Check if there are zero hosts, which means no estimation can be made.
	if len(hosts) == 0 {
		return skymodules.RenterPriceEstimation{}, allowance, errors.New("estimate cannot be made, there are no hosts")
	}

	// Add up the costs for each host.
	var totalContractCost types.Currency
	var totalDownloadCost types.Currency
	var totalStorageCost types.Currency
	var totalUploadCost types.Currency
	for _, host := range hosts {
		totalContractCost = totalContractCost.Add(host.ContractPrice)
		totalDownloadCost = totalDownloadCost.Add(host.DownloadBandwidthPrice)
		totalStorageCost = totalStorageCost.Add(host.StoragePrice)
		totalUploadCost = totalUploadCost.Add(host.UploadBandwidthPrice)
	}

	// Convert values to being human-scale.
	totalDownloadCost = totalDownloadCost.Mul(modules.BytesPerTerabyte)
	totalStorageCost = totalStorageCost.Mul(modules.BlockBytesPerMonthTerabyte)
	totalUploadCost = totalUploadCost.Mul(modules.BytesPerTerabyte)

	// Factor in redundancy.
	totalStorageCost = totalStorageCost.Mul64(3) // TODO: follow file settings?
	totalUploadCost = totalUploadCost.Mul64(3)   // TODO: follow file settings?

	// Perform averages.
	totalContractCost = totalContractCost.Div64(uint64(len(hosts)))
	totalDownloadCost = totalDownloadCost.Div64(uint64(len(hosts)))
	totalStorageCost = totalStorageCost.Div64(uint64(len(hosts)))
	totalUploadCost = totalUploadCost.Div64(uint64(len(hosts)))

	// Take the average of the host set to estimate the overall cost of the
	// contract forming. This is to protect against the case where less hosts
	// were gathered for the estimate that the allowance requires
	totalContractCost = totalContractCost.Mul64(allowance.Hosts)

	// Add the cost of paying the transaction fees and then double the contract
	// costs to account for renewing a full set of contracts.
	_, feePerByte := r.staticTPool.FeeEstimation()
	txnsFees := feePerByte.Mul64(skymodules.EstimatedFileContractTransactionSetSize).Mul64(uint64(allowance.Hosts))
	totalContractCost = totalContractCost.Add(txnsFees)
	totalContractCost = totalContractCost.Mul64(2)

	// Determine host collateral to be added to siafund fee
	var hostCollateral types.Currency
	contractCostPerHost := totalContractCost.Div64(allowance.Hosts)
	fundingPerHost := allowance.Funds.Div64(allowance.Hosts)
	numHosts := uint64(0)
	for _, host := range hosts {
		// Assume that the ContractPrice equals contractCostPerHost and that
		// the txnFee was zero. It doesn't matter since RenterPayoutsPreTax
		// simply subtracts both values from the funding.
		host.ContractPrice = contractCostPerHost
		expectedStorage := allowance.ExpectedStorage / uint64(len(hosts))
		_, _, collateral, err := skymodules.RenterPayoutsPreTax(host, fundingPerHost, types.ZeroCurrency, types.ZeroCurrency, types.ZeroCurrency, allowance.Period, expectedStorage)
		if err != nil {
			continue
		}
		hostCollateral = hostCollateral.Add(collateral)
		numHosts++
	}

	// Divide by zero check. The only way to get 0 numHosts is if
	// RenterPayoutsPreTax errors for every host. This would happen if the
	// funding of the allowance is not enough as that would cause the
	// fundingPerHost to be less than the contract price
	if numHosts == 0 {
		return skymodules.RenterPriceEstimation{}, allowance, errors.New("funding insufficient for number of hosts")
	}
	// Calculate average collateral and determine collateral for allowance
	hostCollateral = hostCollateral.Div64(numHosts)
	hostCollateral = hostCollateral.Mul64(allowance.Hosts)

	// Add in siafund fee. which should be around 10%. The 10% siafund fee
	// accounts for paying 3.9% siafund on transactions and host collateral. We
	// estimate the renter to spend all of it's allowance so the siafund fee
	// will be calculated on the sum of the allowance and the hosts collateral
	totalPayout := allowance.Funds.Add(hostCollateral)
	siafundFee := types.Tax(r.staticConsensusSet.Height(), totalPayout)
	totalContractCost = totalContractCost.Add(siafundFee)

	// Increase estimates by a factor of safety to account for host churn and
	// any potential missed additions
	totalContractCost = totalContractCost.MulFloat(PriceEstimationSafetyFactor)
	totalDownloadCost = totalDownloadCost.MulFloat(PriceEstimationSafetyFactor)
	totalStorageCost = totalStorageCost.MulFloat(PriceEstimationSafetyFactor)
	totalUploadCost = totalUploadCost.MulFloat(PriceEstimationSafetyFactor)

	est := skymodules.RenterPriceEstimation{
		FormContracts:        totalContractCost,
		DownloadTerabyte:     totalDownloadCost,
		StorageTerabyteMonth: totalStorageCost,
		UploadTerabyte:       totalUploadCost,
	}

	id := r.mu.Lock()
	r.lastEstimationHosts = hosts
	r.mu.Unlock(id)

	return est, allowance, nil
}

// callRenterContractsAndUtilities returns the cached contracts and utilities
// from the renter. They can be updated by calling
// managedUpdateRenterContractsAndUtilities.
func (r *Renter) callRenterContractsAndUtilities() (offline map[string]bool, goodForRenew map[string]bool, contracts map[string]skymodules.RenterContract, used []types.SiaPublicKey) {
	id := r.mu.Lock()
	defer r.mu.Unlock(id)
	cu := r.cachedUtilities
	return cu.offline, cu.goodForRenew, cu.contracts, cu.used
}

// managedUpdateRenterContractsAndUtilities grabs the pubkeys of the hosts that
// the file(s) have been uploaded to and then generates maps of the contract's
// utilities showing which hosts are GoodForRenew and which hosts are Offline.
// Additionally a map of host pubkeys to renter contract is created. The offline
// and goodforrenew maps are needed for calculating redundancy and other file
// metrics. All of that information is cached within the renter.
func (r *Renter) managedUpdateRenterContractsAndUtilities() {
	var used []types.SiaPublicKey
	goodForRenew := make(map[string]bool)
	offline := make(map[string]bool)
	allContracts := r.staticHostContractor.Contracts()
	contracts := make(map[string]skymodules.RenterContract)
	for _, contract := range allContracts {
		pk := contract.HostPublicKey
		cu := contract.Utility
		goodForRenew[pk.String()] = cu.GoodForRenew
		offline[pk.String()] = r.staticHostContractor.IsOffline(pk)
		contracts[pk.String()] = contract
		if cu.GoodForRenew {
			used = append(used, pk)
		}
	}

	// Update cache.
	id := r.mu.Lock()
	r.cachedUtilities = cachedUtilities{
		offline:      offline,
		goodForRenew: goodForRenew,
		contracts:    contracts,
		used:         used,
	}
	r.mu.Unlock(id)
}

// staticSetBandwidthLimits will change the bandwidth limits of the renter based
// on the persist values for the bandwidth.
func (r *Renter) staticSetBandwidthLimits(downloadSpeed int64, uploadSpeed int64) error {
	// Input validation.
	if downloadSpeed < 0 || uploadSpeed < 0 {
		return errors.New("download/upload rate limit can't be below 0")
	}

	// Check for sentinel "no limits" value.
	if downloadSpeed == 0 && uploadSpeed == 0 {
		r.staticRL.SetLimits(0, 0, 0)
	} else {
		// Set the rate limits according to the provided values.
		r.staticRL.SetLimits(downloadSpeed, uploadSpeed, 4*4096)
	}
	return nil
}

// SetSettings will update the settings for the renter.
//
// NOTE: This function can't be atomic. Typically we try to have user requests
// be atomic, so that either everything changes or nothing changes, but since
// these changes happen progressively, it's possible for some of the settings
// (like the allowance) to succeed, but then if the bandwidth limits for example
// are bad, then the allowance will update but the bandwidth will not update.
func (r *Renter) SetSettings(s skymodules.RenterSettings) error {
	if err := r.tg.Add(); err != nil {
		return err
	}
	defer r.tg.Done()
	// Early input validation.
	if s.MaxDownloadSpeed < 0 || s.MaxUploadSpeed < 0 {
		return errors.New("bandwidth limits cannot be negative")
	}

	// Set allowance.
	err := r.staticHostContractor.SetAllowance(s.Allowance)
	if err != nil {
		return err
	}

	// Set IPViolationsCheck
	r.staticHostDB.SetIPViolationCheck(s.IPViolationCheck)

	// Set the bandwidth limits.
	err = r.staticSetBandwidthLimits(s.MaxDownloadSpeed, s.MaxUploadSpeed)
	if err != nil {
		return err
	}

	// Save the changes.
	id := r.mu.Lock()
	r.persist.MaxDownloadSpeed = s.MaxDownloadSpeed
	r.persist.MaxUploadSpeed = s.MaxUploadSpeed
	err = r.saveSync()
	r.mu.Unlock(id)
	if err != nil {
		return err
	}

	// Update the worker pool so that the changes are immediately apparent to
	// users.
	r.staticWorkerPool.callUpdate()
	return nil
}

// SetFileTrackingPath sets the on-disk location of an uploaded file to a new
// value. Useful if files need to be moved on disk. SetFileTrackingPath will
// check that a file exists at the new location and it ensures that it has the
// right size, but it can't check that the content is the same. Therefore the
// caller is responsible for not accidentally corrupting the uploaded file by
// providing a different file with the same size.
func (r *Renter) SetFileTrackingPath(siaPath skymodules.SiaPath, newPath string) (err error) {
	if err := r.tg.Add(); err != nil {
		return err
	}
	defer r.tg.Done()
	// Check if file exists and is being tracked.
	entry, err := r.staticFileSystem.OpenSiaFile(siaPath)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Compose(err, entry.Close())
	}()

	// Sanity check that a file with the correct size exists at the new
	// location.
	fi, err := os.Stat(newPath)
	if err != nil {
		return errors.AddContext(err, "failed to get fileinfo of the file")
	}
	if uint64(fi.Size()) != entry.Size() {
		return fmt.Errorf("file sizes don't match - want %v but got %v", entry.Size(), fi.Size())
	}

	// Set the new path on disk.
	return entry.SetLocalPath(newPath)
}

// ActiveHosts returns an array of hostDB's active hosts
func (r *Renter) ActiveHosts() ([]skymodules.HostDBEntry, error) { return r.staticHostDB.ActiveHosts() }

// AllHosts returns an array of all hosts
func (r *Renter) AllHosts() ([]skymodules.HostDBEntry, error) { return r.staticHostDB.AllHosts() }

// Filter returns the renter's hostdb's filterMode and filteredHosts
func (r *Renter) Filter() (skymodules.FilterMode, map[string]types.SiaPublicKey, error) {
	var fm skymodules.FilterMode
	hosts := make(map[string]types.SiaPublicKey)
	if err := r.tg.Add(); err != nil {
		return fm, hosts, err
	}
	defer r.tg.Done()
	fm, hosts, err := r.staticHostDB.Filter()
	if err != nil {
		return fm, hosts, errors.AddContext(err, "error getting hostdb filter:")
	}
	return fm, hosts, nil
}

// SetFilterMode sets the renter's hostdb filter mode
func (r *Renter) SetFilterMode(lm skymodules.FilterMode, hosts []types.SiaPublicKey) error {
	if err := r.tg.Add(); err != nil {
		return err
	}
	defer r.tg.Done()
	// Check to see how many hosts are needed for the allowance
	settings, err := r.Settings()
	if err != nil {
		return errors.AddContext(err, "error getting renter settings:")
	}
	minHosts := settings.Allowance.Hosts
	if len(hosts) < int(minHosts) && lm == skymodules.HostDBActiveWhitelist {
		r.staticLog.Printf("WARN: There are fewer whitelisted hosts than the allowance requires.  Have %v whitelisted hosts, need %v to support allowance\n", len(hosts), minHosts)
	}

	// Set list mode filter for the hostdb
	if err := r.staticHostDB.SetFilterMode(lm, hosts); err != nil {
		return err
	}

	return nil
}

// Host returns the host associated with the given public key
func (r *Renter) Host(spk types.SiaPublicKey) (skymodules.HostDBEntry, bool, error) {
	return r.staticHostDB.Host(spk)
}

// InitialScanComplete returns a boolean indicating if the initial scan of the
// hostdb is completed.
func (r *Renter) InitialScanComplete() (bool, error) { return r.staticHostDB.InitialScanComplete() }

// ScoreBreakdown returns the score breakdown
func (r *Renter) ScoreBreakdown(e skymodules.HostDBEntry) (skymodules.HostScoreBreakdown, error) {
	return r.staticHostDB.ScoreBreakdown(e)
}

// EstimateHostScore returns the estimated host score
func (r *Renter) EstimateHostScore(e skymodules.HostDBEntry, a skymodules.Allowance) (skymodules.HostScoreBreakdown, error) {
	if reflect.DeepEqual(a, skymodules.Allowance{}) {
		settings, err := r.Settings()
		if err != nil {
			return skymodules.HostScoreBreakdown{}, errors.AddContext(err, "error getting renter settings:")
		}
		a = settings.Allowance
	}
	if reflect.DeepEqual(a, skymodules.Allowance{}) {
		a = skymodules.DefaultAllowance
	}
	return r.staticHostDB.EstimateHostScore(e, a)
}

// CancelContract cancels a renter's contract by ID by setting goodForRenew and goodForUpload to false
func (r *Renter) CancelContract(id types.FileContractID) error {
	return r.staticHostContractor.CancelContract(id)
}

// Contracts returns an array of host contractor's staticContracts
func (r *Renter) Contracts() []skymodules.RenterContract { return r.staticHostContractor.Contracts() }

// CurrentPeriod returns the host contractor's current period
func (r *Renter) CurrentPeriod() types.BlockHeight { return r.staticHostContractor.CurrentPeriod() }

// ContractUtility returns the utility field for a given contract, along
// with a bool indicating if it exists.
func (r *Renter) ContractUtility(pk types.SiaPublicKey) (skymodules.ContractUtility, bool) {
	return r.staticHostContractor.ContractUtility(pk)
}

// ContractStatus returns the status of the given contract within the watchdog,
// and a bool indicating whether or not it is being monitored.
func (r *Renter) ContractStatus(fcID types.FileContractID) (skymodules.ContractWatchStatus, bool) {
	return r.staticHostContractor.ContractStatus(fcID)
}

// ContractorChurnStatus returns contract churn stats for the current period.
func (r *Renter) ContractorChurnStatus() skymodules.ContractorChurnStatus {
	return r.staticHostContractor.ChurnStatus()
}

// InitRecoveryScan starts scanning the whole blockchain for recoverable
// contracts within a separate thread.
func (r *Renter) InitRecoveryScan() error {
	return r.staticHostContractor.InitRecoveryScan()
}

// RecoveryScanStatus returns a bool indicating if a scan for recoverable
// contracts is in progress and if it is, the current progress of the scan.
func (r *Renter) RecoveryScanStatus() (bool, types.BlockHeight) {
	return r.staticHostContractor.RecoveryScanStatus()
}

// OldContracts returns an array of host contractor's oldContracts
func (r *Renter) OldContracts() []skymodules.RenterContract {
	return r.staticHostContractor.OldContracts()
}

// Performance is a function call that returns all of the performance
// information about the renter.
func (r *Renter) Performance() (skymodules.RenterPerformance, error) {
	healthDuration := time.Duration(atomic.LoadUint64(&r.atomicSystemHealthScanDuration))
	return skymodules.RenterPerformance{
		SystemHealthScanDuration: healthDuration,

		BaseSectorDownloadOverdriveStats:   r.staticBaseSectorDownloadStats,
		BaseSectorUploadStats:              r.staticBaseSectorUploadStats.Stats(),
		ChunkUploadStats:                   r.staticChunkUploadStats.Stats(),
		FanoutSectorDownloadOverdriveStats: r.staticFanoutSectorDownloadStats,
		RegistryReadStats:                  r.staticRegistryReadStats.Stats(),
		RegistryWriteStats:                 r.staticRegWriteStats.Stats(),
		StreamBufferReadStats:              r.staticStreamBufferStats.Stats(),
	}, nil
}

// PeriodSpending returns the host contractor's period spending
func (r *Renter) PeriodSpending() (skymodules.ContractorSpending, error) {
	return r.staticHostContractor.PeriodSpending()
}

// RecoverableContracts returns the host contractor's recoverable contracts.
func (r *Renter) RecoverableContracts() []skymodules.RecoverableContract {
	return r.staticHostContractor.RecoverableContracts()
}

// RefreshedContract returns a bool indicating if the contract was previously
// refreshed
func (r *Renter) RefreshedContract(fcid types.FileContractID) bool {
	return r.staticHostContractor.RefreshedContract(fcid)
}

// ResetStats resets the renter stats.
func (r *Renter) ResetStats(statsType types.Specifier) error {
	switch statsType {
	case skymodules.OverdriveStats:
		r.staticBaseSectorDownloadStats.Reset()
		r.staticFanoutSectorDownloadStats.Reset()
		return nil
	default:
		return fmt.Errorf("invalid stats type '%v', the only supported type is '%v'", statsType, skymodules.OverdriveStats)
	}
}

// Settings returns the Renter's current settings.
func (r *Renter) Settings() (skymodules.RenterSettings, error) {
	if err := r.tg.Add(); err != nil {
		return skymodules.RenterSettings{}, err
	}
	defer r.tg.Done()
	download, upload, _ := r.staticRL.Limits()
	enabled, err := r.staticHostDB.IPViolationsCheck()
	if err != nil {
		return skymodules.RenterSettings{}, errors.AddContext(err, "error getting IPViolationsCheck:")
	}
	paused, endTime := r.staticUploadHeap.managedPauseStatus()
	return skymodules.RenterSettings{
		Allowance:        r.staticHostContractor.Allowance(),
		IPViolationCheck: enabled,
		MaxDownloadSpeed: download,
		MaxUploadSpeed:   upload,
		UploadsStatus: skymodules.UploadsStatus{
			Paused:       paused,
			PauseEndTime: endTime,
		},
	}, nil
}

// ProcessConsensusChange returns the process consensus change
func (r *Renter) ProcessConsensusChange(cc modules.ConsensusChange) {
	id := r.mu.Lock()
	r.lastEstimationHosts = []skymodules.HostDBEntry{}
	r.mu.Unlock(id)
	if cc.Synced {
		_ = r.tg.Launch(r.staticWorkerPool.callUpdate)
	}
}

// threadedPaySkynetFee pays the accumulated skynet fee every 24 hours.
func (r *Renter) threadedPaySkynetFee() {
	// Pay periodically.
	ticker := time.NewTicker(skymodules.SkynetFeePayoutCheckInterval)
	for {
		na := r.staticDeps.SkynetAddress()

		// Compute the threshold.
		_, max := r.staticTPool.FeeEstimation()
		threshold := max.Mul64(skynetFeePayoutMultiplier)

		err := paySkynetFee(r.staticSpendingHistory, r.staticWallet, append(r.Contracts(), r.OldContracts()...), na, threshold, r.staticLog)
		if err != nil {
			r.staticLog.Print(err)
		}
		select {
		case <-r.tg.StopChan():
			return // shutdown
		case <-ticker.C:
		}
	}
}

// paySkynetFee pays the accumulated skynet fee every 24 hours.
// TODO: once we pay for monetized content, that also needs to be part of the
// total spending.
func paySkynetFee(sh *spendingHistory, w siacoinSender, contracts []skymodules.RenterContract, addr types.UnlockHash, threshold types.Currency, log *persist.Logger) error {
	// Get the last spending.
	lastSpending, lastSpendingHeight := sh.LastSpending()

	// Only pay fees once per day.
	if time.Since(lastSpendingHeight) < skymodules.SkynetFeePayoutInterval {
		return nil
	}

	// Compute the total spending at this point in time.
	var totalSpending types.Currency
	for _, contract := range contracts {
		totalSpending = totalSpending.Add(contract.SkynetSpending())
	}

	// Check by how much it increased since the last time.
	if totalSpending.Cmp(lastSpending) <= 0 {
		return nil // Spending didn't increase
	}

	// Compute the fee.
	fee := totalSpending.Sub(lastSpending).Div64(skymodules.SkynetFeeDivider)

	// Check if we are above a payout threshold.
	if fee.Cmp(threshold) < 0 {
		log.Printf("Not paying fee of %v since it's below the threshold of %v", fee, threshold)
		return nil // Don't pay if we are below the threshold.
	}

	// Log that we are about to pay the fee.
	log.Printf("Paying fee of %v to %v after spending increased from %v to %v", fee, addr, lastSpending, totalSpending)

	// Send the fee.
	txn, err := w.SendSiacoins(fee, addr)
	if err != nil {
		return errors.AddContext(err, "Failed to send siacoins for skynet fee. Will retry again in an hour")
	}

	// Mark the totalSpending in the history.
	err = sh.AddSpending(totalSpending, txn, time.Now())
	if err != nil {
		err = errors.AddContext(err, "failed to persist paid skynet fees")
		build.Critical(err)
		return err
	}
	return nil
}

// SetIPViolationCheck is a passthrough method to the hostdb's method of the
// same name.
func (r *Renter) SetIPViolationCheck(enabled bool) {
	r.staticHostDB.SetIPViolationCheck(enabled)
}

// MountInfo returns the list of currently mounted fusefilesystems.
func (r *Renter) MountInfo() []skymodules.MountInfo {
	return r.staticFuseManager.MountInfo()
}

// Mount mounts the files under the specified siapath under the 'mountPoint' folder on
// the local filesystem.
func (r *Renter) Mount(mountPoint string, sp skymodules.SiaPath, opts skymodules.MountOptions) error {
	return r.staticFuseManager.Mount(mountPoint, sp, opts)
}

// Unmount unmounts the fuse filesystem currently mounted at mountPoint.
func (r *Renter) Unmount(mountPoint string) error {
	return r.staticFuseManager.Unmount(mountPoint)
}

// AddSkykey adds the skykey with the given name, cipher type, and entropy to
// the renter's skykey manager.
func (r *Renter) AddSkykey(sk skykey.Skykey) error {
	if err := r.tg.Add(); err != nil {
		return err
	}
	defer r.tg.Done()
	return r.staticSkykeyManager.AddKey(sk)
}

// DeleteSkykeyByID deletes the Skykey with the given ID from the renter's skykey
// manager if it exists.
func (r *Renter) DeleteSkykeyByID(id skykey.SkykeyID) error {
	if err := r.tg.Add(); err != nil {
		return err
	}
	defer r.tg.Done()
	return r.staticSkykeyManager.DeleteKeyByID(id)
}

// DeleteSkykeyByName deletes the Skykey with the given name from the renter's skykey
// manager if it exists.
func (r *Renter) DeleteSkykeyByName(name string) error {
	if err := r.tg.Add(); err != nil {
		return err
	}
	defer r.tg.Done()
	return r.staticSkykeyManager.DeleteKeyByName(name)
}

// SkykeyByName gets the Skykey with the given name from the renter's skykey
// manager if it exists.
func (r *Renter) SkykeyByName(name string) (skykey.Skykey, error) {
	if err := r.tg.Add(); err != nil {
		return skykey.Skykey{}, err
	}
	defer r.tg.Done()
	return r.staticSkykeyManager.KeyByName(name)
}

// CreateSkykey creates a new Skykey with the given name and ciphertype.
func (r *Renter) CreateSkykey(name string, skType skykey.SkykeyType) (skykey.Skykey, error) {
	if err := r.tg.Add(); err != nil {
		return skykey.Skykey{}, err
	}
	defer r.tg.Done()
	return r.staticSkykeyManager.CreateKey(name, skType)
}

// NewRegistrySubscriber creates a new registry subscriber.
func (r *Renter) NewRegistrySubscriber(notifyFunc func(entry skymodules.RegistryEntry) error) (skymodules.RegistrySubscriber, error) {
	if err := r.tg.Add(); err != nil {
		return nil, err
	}
	defer r.tg.Done()
	return r.staticSubscriptionManager.NewSubscriber(notifyFunc), nil
}

// SkykeyByID gets the Skykey with the given ID from the renter's skykey
// manager if it exists.
func (r *Renter) SkykeyByID(id skykey.SkykeyID) (skykey.Skykey, error) {
	if err := r.tg.Add(); err != nil {
		return skykey.Skykey{}, err
	}
	defer r.tg.Done()
	return r.staticSkykeyManager.KeyByID(id)
}

// SkykeyIDByName gets the SkykeyID of the key with the given name if it
// exists.
func (r *Renter) SkykeyIDByName(name string) (skykey.SkykeyID, error) {
	if err := r.tg.Add(); err != nil {
		return skykey.SkykeyID{}, err
	}
	defer r.tg.Done()
	return r.staticSkykeyManager.IDByName(name)
}

// Skykeys returns a slice containing each Skykey being stored by the renter.
func (r *Renter) Skykeys() ([]skykey.Skykey, error) {
	if err := r.tg.Add(); err != nil {
		return nil, err
	}
	defer r.tg.Done()

	return r.staticSkykeyManager.Skykeys(), nil
}

// Enforce that Renter satisfies the skymodules.Renter interface.
var _ skymodules.Renter = (*Renter)(nil)

// renterBlockingStartup handles the blocking portion of NewCustomRenter.
func renterBlockingStartup(g modules.Gateway, cs modules.ConsensusSet, tpool modules.TransactionPool, hdb skymodules.HostDB, w modules.Wallet, hc hostContractor, mux *siamux.SiaMux, tus skymodules.SkynetTUSUploadStore, persistDir string, rl *ratelimit.RateLimit, deps skymodules.SkydDependencies) (*Renter, error) {
	if g == nil {
		return nil, errNilGateway
	}
	if cs == nil {
		return nil, errNilCS
	}
	if tpool == nil {
		return nil, errNilTpool
	}
	if hc == nil {
		return nil, errNilContractor
	}
	if hdb == nil && build.Release != "testing" {
		return nil, errNilHdb
	}
	if w == nil {
		return nil, errNilWallet
	}

	r := &Renter{
		// Initiate skynet resources
		staticSkylinkManager: newSkylinkManager(),

		repairingChunks: make(map[uploadChunkID]*unfinishedUploadChunk),

		// Making newDownloads a buffered channel means that most of the time, a
		// new download will trigger an unnecessary extra iteration of the
		// download heap loop, searching for a chunk that's not there. This is
		// preferable to the alternative, where in rare cases the download heap
		// will miss work altogether.
		newDownloads:       make(chan struct{}, 1),
		staticDownloadHeap: &downloadHeap{},

		staticBaseSectorDownloadStats:   skymodules.NewSectorDownloadStats(),
		staticFanoutSectorDownloadStats: skymodules.NewSectorDownloadStats(),

		staticUploadHeap: uploadHeap{
			stuckHeapChunks:   make(map[uploadChunkID]*unfinishedUploadChunk),
			unstuckHeapChunks: make(map[uploadChunkID]*unfinishedUploadChunk),

			newUploads:        make(chan struct{}, 1),
			repairNeeded:      make(chan struct{}, 1),
			stuckChunkFound:   make(chan struct{}, 1),
			stuckChunkSuccess: make(chan struct{}, 1),

			pauseChan: make(chan struct{}),
		},
		staticDirectoryHeap: directoryHeap{
			heapDirectories: make(map[skymodules.SiaPath]*directory),
		},

		staticDownloadHistory: newDownloadHistory(),

		ongoingRegistryRepairs: make(map[modules.RegistryEntryID]struct{}),

		staticConsensusSet:   cs,
		staticDeps:           deps,
		staticGateway:        g,
		staticWallet:         w,
		staticHostDB:         hdb,
		staticHostContractor: hc,
		persistDir:           persistDir,
		staticRL:             rl,
		staticAlerter:        modules.NewAlerter("renter"),
		staticMux:            mux,
		mu:                   siasync.New(modules.SafeMutexDelay, 1),
		staticTPool:          tpool,
	}

	r.staticSkynetTUSUploader = newSkynetTUSUploader(r, tus)
	if err := r.tg.AfterStop(r.staticSkynetTUSUploader.Close); err != nil {
		return nil, err
	}
	r.staticUploadChunkDistributionQueue = newUploadChunkDistributionQueue(r)
	close(r.staticUploadHeap.pauseChan)

	// Init the spending history.
	sh, err := NewSpendingHistory(r.persistDir, skymodules.SkynetSpendingHistoryFilename)
	if err != nil {
		return nil, err
	}
	r.staticSpendingHistory = sh

	// Init the statsChan and close it right away to signal that no scan is
	// going on.
	r.statsChan = make(chan struct{})
	close(r.statsChan)

	// Initialize the loggers so that they are available for the components as
	// the components start up.
	r.staticLog, err = persist.NewFileLogger(filepath.Join(r.persistDir, logFile))
	if err != nil {
		return nil, err
	}
	if err := r.tg.AfterStop(r.staticLog.Close); err != nil {
		return nil, err
	}
	r.staticRepairLog, err = persist.NewFileLogger(filepath.Join(r.persistDir, repairLogFile))
	if err != nil {
		return nil, err
	}
	if err := r.tg.AfterStop(r.staticRepairLog.Close); err != nil {
		return nil, err
	}

	// Initialize the dirUpdateBatcher.
	r.staticDirUpdateBatcher, err = r.newDirUpdateBatcher()
	if err != nil {
		return nil, errors.AddContext(err, "unable to create new health update batcher")
	}

	// Initialize some of the components.
	err = r.newAccountManager()
	if err != nil {
		return nil, errors.AddContext(err, "unable to create account manager")
	}

	r.staticRegistryMemoryManager = newMemoryManager(registryMemoryDefault, registryMemoryPriorityDefault, r.tg.StopChan())
	r.staticUserUploadMemoryManager = newMemoryManager(userUploadMemoryDefault, userUploadMemoryPriorityDefault, r.tg.StopChan())
	r.staticUserDownloadMemoryManager = newMemoryManager(userDownloadMemoryDefault, userDownloadMemoryPriorityDefault, r.tg.StopChan())
	r.staticRepairMemoryManager = newMemoryManager(repairMemoryDefault, repairMemoryPriorityDefault, r.tg.StopChan())

	r.staticFuseManager = newFuseManager(r)
	r.staticStuckStack = callNewStuckStack()

	// Add SkynetBlocklist
	sb, err := skynetblocklist.New(r.persistDir)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create new skynet blocklist")
	}
	r.staticSkynetBlocklist = sb

	// Add SkynetPortals
	sp, err := skynetportals.New(r.persistDir)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create new skynet portal list")
	}
	r.staticSkynetPortals = sp

	// Load all saved data.
	err = r.managedInitPersist()
	if err != nil {
		return nil, err
	}

	// Init stream buffer now that the stats are initialised.
	r.staticStreamBufferSet = newStreamBufferSet(r.staticStreamBufferStats, &r.tg)

	// After persist is initialized, create the worker pool.
	r.staticWorkerPool = r.newWorkerPool()

	// Set the worker pool on the contractor.
	r.staticHostContractor.UpdateWorkerPool(r.staticWorkerPool)

	// Create the skykey manager.
	// In testing, keep the skykeys with the rest of the renter data.
	skykeyManDir := build.SkynetDir()
	if build.Release == "testing" {
		skykeyManDir = persistDir
	}
	r.staticSkykeyManager, err = skykey.NewSkykeyManager(skykeyManDir)
	if err != nil {
		return nil, err
	}

	// Calculate the initial cached utilities and kick off a thread that updates
	// the utilities regularly.
	r.managedUpdateRenterContractsAndUtilities()
	go r.threadedUpdateRenterContractsAndUtilities()

	// Launch the stat persisting thread.
	go r.threadedStatsPersister()

	// Spin up background threads which are not depending on the renter being
	// up-to-date with consensus.
	if !r.staticDeps.Disrupt("DisableRepairAndHealthLoops") {
		// Push the root directory onto the directory heap for the repair process.
		err = r.managedPushUnexploredDirectory(skymodules.RootSiaPath())
		if err != nil {
			return nil, err
		}
		go r.threadedHealthLoop()
	}

	// If the spending history didn't exist before, manually init it with the
	// current spending. We don't want portals to pay a huge fee right after
	// upgrading for pre-skynet license spendings.
	_, lastSpendingTime := sh.LastSpending()
	if lastSpendingTime.IsZero() {
		var totalSpending types.Currency
		for _, c := range append(r.Contracts(), r.OldContracts()...) {
			totalSpending = totalSpending.Add(c.SkynetSpending())
		}
		err = sh.AddSpending(totalSpending, []types.Transaction{}, time.Now())
		if err != nil {
			return nil, errors.AddContext(err, "failed to add initial spending")
		}
	}

	// Create the subscription manager and launch the thread that updates the
	// workers.
	r.staticSubscriptionManager = newSubscriptionManager(r)
	err = r.tg.Launch(r.staticSubscriptionManager.threadedUpdateWorkers)
	if err != nil {
		return nil, err
	}

	// Spin up the skynet fee paying goroutine.
	if err := r.tg.Launch(r.threadedPaySkynetFee); err != nil {
		return nil, err
	}

	// Spin up the tus pruning goroutine.
	if err := r.tg.Launch(r.threadedPruneTUSUploads); err != nil {
		return nil, err
	}

	// Unsubscribe on shutdown.
	err = r.tg.OnStop(func() error {
		cs.Unsubscribe(r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// renterAsyncStartup handles the non-blocking portion of NewCustomRenter.
func renterAsyncStartup(r *Renter, cs modules.ConsensusSet) error {
	if r.staticDeps.Disrupt("BlockAsyncStartup") {
		return nil
	}
	// Subscribe to the consensus set in a separate goroutine.
	done := make(chan struct{})
	defer close(done)
	err := cs.ConsensusSetSubscribe(r, modules.ConsensusChangeRecent, r.tg.StopChan())
	if err != nil && strings.Contains(err.Error(), threadgroup.ErrStopped.Error()) {
		return err
	}
	if err != nil {
		return err
	}
	// Spin up the remaining background threads once we are caught up with the
	// consensus set.
	// Spin up the workers for the work pool.
	go r.threadedDownloadLoop()
	if !r.staticDeps.Disrupt("DisableRepairAndHealthLoops") {
		go r.threadedUploadAndRepair()
		go r.threadedStuckFileLoop()
	}
	// Spin up the snapshot synchronization thread.
	if !r.staticDeps.Disrupt("DisableSnapshotSync") {
		go r.threadedSynchronizeSnapshots()
	}
	return nil
}

// threadedUpdateRenterContractsAndUtilities periodically calls
// managedUpdateRenterContractsAndUtilities.
func (r *Renter) threadedUpdateRenterContractsAndUtilities() {
	err := r.tg.Add()
	if err != nil {
		return
	}
	defer r.tg.Done()
	for {
		select {
		case <-r.tg.StopChan():
			return
		case <-time.After(cachedUtilitiesUpdateInterval):
		}
		r.managedUpdateRenterContractsAndUtilities()
	}
}

// NewCustomRenter initializes a renter and returns it.
func NewCustomRenter(g modules.Gateway, cs modules.ConsensusSet, tpool modules.TransactionPool, hdb skymodules.HostDB, w modules.Wallet, hc hostContractor, mux *siamux.SiaMux, tus skymodules.SkynetTUSUploadStore, persistDir string, rl *ratelimit.RateLimit, deps skymodules.SkydDependencies) (*Renter, <-chan error) {
	errChan := make(chan error, 1)

	// Blocking startup.
	r, err := renterBlockingStartup(g, cs, tpool, hdb, w, hc, mux, tus, persistDir, rl, deps)
	if err != nil {
		errChan <- err
		return nil, errChan
	}

	// non-blocking startup
	go func() {
		defer close(errChan)
		if err := r.tg.Add(); err != nil {
			errChan <- err
			return
		}
		defer r.tg.Done()
		err := renterAsyncStartup(r, cs)
		if err != nil {
			errChan <- err
		}
	}()
	return r, errChan
}

// New returns an initialized renter.
func New(g modules.Gateway, cs modules.ConsensusSet, wallet modules.Wallet, tpool modules.TransactionPool, mux *siamux.SiaMux, tus skymodules.SkynetTUSUploadStore, rl *ratelimit.RateLimit, persistDir string) (*Renter, <-chan error) {
	errChan := make(chan error, 1)
	hdb, errChanHDB := hostdb.New(g, cs, tpool, mux, persistDir)
	if err := modules.PeekErr(errChanHDB); err != nil {
		errChan <- err
		return nil, errChan
	}
	hc, errChanContractor := contractor.New(cs, wallet, tpool, hdb, rl, persistDir)
	if err := modules.PeekErr(errChanContractor); err != nil {
		errChan <- err
		return nil, errChan
	}
	renter, errChanRenter := NewCustomRenter(g, cs, tpool, hdb, wallet, hc, mux, tus, persistDir, rl, skymodules.SkydProdDependencies)
	if err := modules.PeekErr(errChanRenter); err != nil {
		errChan <- err
		return nil, errChan
	}
	go func() {
		errChan <- errors.Compose(<-errChanHDB, <-errChanContractor, <-errChanRenter)
		close(errChan)
	}()
	return renter, errChan
}

// HostsForRegistryUpdate returns a list of hosts that the renter would be using
// for updating the registry.
func (r *Renter) HostsForRegistryUpdate() ([]skymodules.HostForRegistryUpdate, error) {
	if err := r.tg.Add(); err != nil {
		return nil, err
	}
	defer r.tg.Done()

	var hosts []skymodules.HostForRegistryUpdate
	for _, w := range r.staticWorkerPool.callWorkers() {
		if !isWorkerGoodForRegistryUpdate(w) {
			continue
		}
		hosts = append(hosts, skymodules.HostForRegistryUpdate{
			Pubkey: w.staticHostPubKey,
		})
	}
	return hosts, nil
}
