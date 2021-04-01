package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/skynetlabs/skyd/build"
	"gitlab.com/skynetlabs/skyd/node/api"
	"gitlab.com/skynetlabs/skyd/skymodules"
	"gitlab.com/skynetlabs/skyd/skymodules/renter/filesystem"
)

var (
	// errIncorrectNumArgs is the error return if there is an incorrect number of
	// arguments
	errIncorrectNumArgs = errors.New("incorrect number of arguments")
)

// byDirectoryInfo implements sort.Interface for []directoryInfo based on the
// SiaPath field.
type byDirectoryInfo []directoryInfo

func (s byDirectoryInfo) Len() int      { return len(s) }
func (s byDirectoryInfo) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s byDirectoryInfo) Less(i, j int) bool {
	return s[i].dir.SiaPath.String() < s[j].dir.SiaPath.String()
}

// bySiaPathFile implements sort.Interface for [] skymodules.FileInfo based on the
// SiaPath field.
type bySiaPathFile []skymodules.FileInfo

func (s bySiaPathFile) Len() int           { return len(s) }
func (s bySiaPathFile) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s bySiaPathFile) Less(i, j int) bool { return s[i].SiaPath.String() < s[j].SiaPath.String() }

// bySiaPathDir implements sort.Interface for [] skymodules.DirectoryInfo based on the
// SiaPath field.
type bySiaPathDir []skymodules.DirectoryInfo

func (s bySiaPathDir) Len() int           { return len(s) }
func (s bySiaPathDir) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s bySiaPathDir) Less(i, j int) bool { return s[i].SiaPath.String() < s[j].SiaPath.String() }

// byValue sorts contracts by their value in siacoins, high to low. If two
// contracts have the same value, they are sorted by their host's address.
type byValue []api.RenterContract

func (s byValue) Len() int      { return len(s) }
func (s byValue) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s byValue) Less(i, j int) bool {
	cmp := s[i].RenterFunds.Cmp(s[j].RenterFunds)
	if cmp == 0 {
		return s[i].NetAddress < s[j].NetAddress
	}
	return cmp > 0
}

// directoryInfo is a helper struct that contains the skymodules.DirectoryInfo for
// a directory, the skymodules.FileInfo for all the directory's files, and the
// skymodules.DirectoryInfo for all the subdirs.
type directoryInfo struct {
	dir     skymodules.DirectoryInfo
	files   []skymodules.FileInfo
	subDirs []skymodules.DirectoryInfo
}

// progressMeasurement is a helper type used for measuring the progress of
// a download.
type progressMeasurement struct {
	progress uint64
	time     time.Time
}

// trackedFile is a helper struct for tracking files related to downloads
type trackedFile struct {
	siaPath skymodules.SiaPath
	dst     string
}

// contractStats is a helper function to pull information out of the renter
// contracts to be displayed
func contractStats(contracts []api.RenterContract) (size uint64, spent, remaining, fees types.Currency) {
	for _, c := range contracts {
		size += c.Size
		remaining = remaining.Add(c.RenterFunds)
		fees = fees.Add(c.Fees)
		// Negative Currency Check
		var contractTotalSpent types.Currency
		if c.TotalCost.Cmp(c.RenterFunds.Add(c.Fees)) < 0 {
			contractTotalSpent = c.RenterFunds.Add(c.Fees)
		} else {
			contractTotalSpent = c.TotalCost.Sub(c.RenterFunds).Sub(c.Fees)
		}
		spent = spent.Add(contractTotalSpent)
	}
	return
}

// downloadDir downloads the dir at the specified siaPath to the specified
// location. It returns all the files for which a download was initialized as
// tracked files and the ones which were ignored as skipped. Errors are composed
// into a single error.
func downloadDir(siaPath skymodules.SiaPath, destination string) (tfs []trackedFile, skipped []string, totalSize uint64, err error) {
	// Get dir info.
	rd, err := httpClient.RenterDirRootGet(siaPath)
	if err != nil {
		err = errors.AddContext(err, "failed to get dir info")
		return
	}
	// Create destination on disk.
	if err = os.MkdirAll(destination, 0750); err != nil {
		err = errors.AddContext(err, "failed to create destination dir")
		return
	}
	// Download files.
	for _, file := range rd.Files {
		// Skip files that already exist.
		dst := filepath.Join(destination, file.SiaPath.Name())
		if _, err = os.Stat(dst); err == nil {
			skipped = append(skipped, dst)
			continue
		} else if !os.IsNotExist(err) {
			err = errors.AddContext(err, "failed to get file stats")
			return
		}
		// Download file.
		totalSize += file.Filesize
		_, err = httpClient.RenterDownloadFullGet(file.SiaPath, dst, true, true)
		if err != nil {
			err = errors.AddContext(err, "Failed to start download")
			return
		}
		// Append file to tracked files.
		tfs = append(tfs, trackedFile{
			siaPath: file.SiaPath,
			dst:     dst,
		})
	}
	// If the download isn't recursive we are done.
	if !renterDownloadRecursive {
		return
	}
	// Call downloadDir on all subdirs.
	for i := 1; i < len(rd.Directories); i++ {
		subDir := rd.Directories[i]
		rtfs, rskipped, totalSubSize, rerr := downloadDir(subDir.SiaPath, filepath.Join(destination, subDir.SiaPath.Name()))
		tfs = append(tfs, rtfs...)
		skipped = append(skipped, rskipped...)
		totalSize += totalSubSize
		err = errors.Compose(err, rerr)
	}
	return
}

// downloadProgress will display the progress of the provided files and return a
// slice of DownloadInfos for failed downloads.
func downloadProgress(tfs []trackedFile) []api.DownloadInfo {
	// Nothing to do if no files are tracked.
	if len(tfs) == 0 {
		return nil
	}
	start := time.Now()

	// Create a map of all tracked files for faster lookups and also a measurement
	// map which is initialized with 0 progress for all tracked files.
	tfsMap := make(map[skymodules.SiaPath]trackedFile)
	measurements := make(map[skymodules.SiaPath][]progressMeasurement)
	for _, tf := range tfs {
		tfsMap[tf.siaPath] = tf
		measurements[tf.siaPath] = []progressMeasurement{{
			progress: 0,
			time:     time.Now(),
		}}
	}
	// Periodically print measurements until download is done.
	completed := make(map[string]struct{})
	errMap := make(map[string]api.DownloadInfo)
	failedDownloads := func() (fd []api.DownloadInfo) {
		for _, di := range errMap {
			fd = append(fd, di)
		}
		return
	}
	for range time.Tick(OutputRefreshRate) {
		// Get the list of downloads.
		rdg, err := httpClient.RenterDownloadsRootGet()
		if err != nil {
			continue // benign
		}
		// Create a map of downloads for faster lookups. To get unique keys we use
		// siaPath + destination as the key.
		queue := make(map[string]api.DownloadInfo)
		for _, d := range rdg.Downloads {
			key := d.SiaPath.String() + d.Destination
			if _, exists := queue[key]; !exists {
				queue[key] = d
			}
		}
		// Clear terminal.
		clearStr := fmt.Sprint("\033[H\033[2J")
		// Take new measurements for each tracked file.
		progressStr := clearStr
		for tfIdx, tf := range tfs {
			// Search for the download in the list of downloads.
			mapKey := tf.siaPath.String() + tf.dst
			d, found := queue[mapKey]
			m, exists := measurements[tf.siaPath]
			if !exists {
				die("Measurement missing for tracked file. This should never happen.")
			}
			// If the download has not appeared in the queue yet, either continue or
			// give up.
			if !found {
				if time.Since(start) > RenterDownloadTimeout {
					die("Unable to find download in queue. This should never happen.")
				}
				continue
			}
			// Check whether the file has completed or otherwise errored out.
			if d.Error != "" {
				errMap[mapKey] = d
			}
			if d.Completed {
				completed[mapKey] = struct{}{}
				// Check if all downloads are done.
				if len(completed) == len(tfs) {
					return failedDownloads()
				}
				continue
			}
			// Add the current progress to the measurements.
			m = append(m, progressMeasurement{
				progress: d.Received,
				time:     time.Now(),
			})
			// Shrink the measurements to only contain measurements from within the
			// SpeedEstimationWindow.
			for len(m) > 2 && m[len(m)-1].time.Sub(m[0].time) > SpeedEstimationWindow {
				m = m[1:]
			}
			// Update measurements in the map.
			measurements[tf.siaPath] = m
			// Compute the progress and timespan between the first and last
			// measurement to get the speed.
			received := float64(m[len(m)-1].progress - m[0].progress)
			timespan := m[len(m)-1].time.Sub(m[0].time)
			speed := bandwidthUnit(uint64((received * 8) / timespan.Seconds()))

			// Compuate the percentage of completion and time elapsed since the
			// start of the download.
			pct := 100 * float64(d.Received) / float64(d.Filesize)
			elapsed := time.Since(d.StartTime)
			elapsed -= elapsed % time.Second // round to nearest second

			progressLine := fmt.Sprintf("Downloading %v... %5.1f%% of %v, %v elapsed, %s    ", tf.siaPath.String(), pct, modules.FilesizeUnits(d.Filesize), elapsed, speed)
			if tfIdx < len(tfs)-1 {
				progressStr += fmt.Sprintln(progressLine)
			} else {
				progressStr += fmt.Sprint(progressLine)
			}
		}
		fmt.Print(progressStr)
		progressStr = clearStr
	}
	// This code is unreachable, but the compiler requires this to be here.
	return nil
}

// fileHealthBreakdown returns a percentage breakdown of the renter's files'
// healths and the number of stuck files
func fileHealthBreakdown(dirs []directoryInfo, printLostFiles bool) ([]float64, int, error) {
	// Check for nil input
	if len(dirs) == 0 {
		return nil, 0, errors.New("No Directories Found")
	}

	// Note: we are manually counting the number of files here since the
	// aggregate fields in the directory could be incorrect due to delays in the
	// health loop. This is OK since we have to iterate over all the files
	// anyways.
	var total, fullHealth, greater75, greater50, greater25, greater0, unrecoverable float64
	var numStuck int
	for _, dir := range dirs {
		for _, file := range dir.files {
			total++
			if file.Stuck {
				numStuck++
			}
			switch {
			case file.MaxHealthPercent == 100:
				fullHealth++
			case file.MaxHealthPercent > 75:
				greater75++
			case file.MaxHealthPercent > 50:
				greater50++
			case file.MaxHealthPercent > 25:
				greater25++
			case file.MaxHealthPercent > 0 || file.OnDisk:
				greater0++
			default:
				unrecoverable++
				if printLostFiles {
					fmt.Println(file.SiaPath)
				}
			}
		}
	}

	// Print out total lost files
	if printLostFiles {
		fmt.Println()
		fmt.Println(unrecoverable, "lost files found.")
	}

	// Check for no files uploaded
	if total == 0 {
		return nil, 0, errors.New("No Files Uploaded")
	}

	fullHealth = 100 * fullHealth / total
	greater75 = 100 * greater75 / total
	greater50 = 100 * greater50 / total
	greater25 = 100 * greater25 / total
	greater0 = 100 * greater0 / total
	unrecoverable = 100 * unrecoverable / total

	return []float64{fullHealth, greater75, greater50, greater25, greater0, unrecoverable}, numStuck, nil
}

// getDir returns the directory info for the directory at siaPath and its
// subdirs, querying the root directory.
func getDir(siaPath skymodules.SiaPath, root, recursive bool) (dirs []directoryInfo) {
	var rd api.RenterDirectory
	var err error
	if root {
		rd, err = httpClient.RenterDirRootGet(siaPath)
	} else {
		rd, err = httpClient.RenterDirGet(siaPath)
	}
	if err != nil {
		die("failed to get dir info:", err)
	}
	dir := rd.Directories[0]
	subDirs := rd.Directories[1:]

	// Append directory to dirs.
	dirs = append(dirs, directoryInfo{
		dir:     dir,
		files:   rd.Files,
		subDirs: subDirs,
	})

	// If -R isn't set we are done.
	if !recursive {
		return
	}
	// Call getDir on subdirs.
	for _, subDir := range subDirs {
		rdirs := getDir(subDir.SiaPath, root, recursive)
		dirs = append(dirs, rdirs...)
	}
	return
}

// getDirSorted calls getDir and then sorts the response by siapath
func getDirSorted(siaPath skymodules.SiaPath, root, recursive bool) []directoryInfo {
	// Get Dirs
	dirs := getDir(siaPath, root, recursive)

	// Sort the directories and the files.
	sort.Sort(byDirectoryInfo(dirs))
	for i := 0; i < len(dirs); i++ {
		sort.Sort(bySiaPathDir(dirs[i].subDirs))
		sort.Sort(bySiaPathFile(dirs[i].files))
	}
	return dirs
}

// parseLSArgs is a helper that parses the arguments for renter ls and skynet ls
// and returns the siapath.
func parseLSArgs(args []string) (skymodules.SiaPath, error) {
	var path string
	switch len(args) {
	case 0:
		path = "."
	case 1:
		path = args[0]
	default:
		return skymodules.SiaPath{}, errIncorrectNumArgs
	}
	// Parse the input siapath.
	var sp skymodules.SiaPath
	var err error
	if path == "." || path == "" || path == "/" {
		sp = skymodules.RootSiaPath()
	} else {
		sp, err = skymodules.NewSiaPath(path)
		if err != nil {
			return skymodules.SiaPath{}, errors.AddContext(err, "could not parse siaPath")
		}
	}
	return sp, nil
}

// printContractInfo is a helper function for printing the information about a
// specific contract
func printContractInfo(cid string, contracts []api.RenterContract) error {
	for _, rc := range contracts {
		if rc.ID.String() == cid {
			var fundsAllocated types.Currency
			if rc.TotalCost.Cmp(rc.Fees) > 0 {
				fundsAllocated = rc.TotalCost.Sub(rc.Fees)
			}
			hostInfo, err := httpClient.HostDbHostsGet(rc.HostPublicKey)
			if err != nil {
				return fmt.Errorf("Could not fetch details of host: %v", err)
			}
			fmt.Printf(`
Contract %v
	Host: %v (Public Key: %v)
	Host Version: %v

  Start Height: %v
  End Height:   %v

  Total cost:           %v (Fees: %v)
  Funds Allocated:      %v
  Upload Spending:      %v
  Storage Spending:     %v
  Download Spending:    %v
  FundAccount Spending: %v
  Maintenance Spending: %v
  Remaining Funds:      %v

  File Size: %v
`, rc.ID, rc.NetAddress, rc.HostPublicKey.String(), rc.HostVersion, rc.StartHeight, rc.EndHeight,
				currencyUnits(rc.TotalCost), currencyUnits(rc.Fees),
				currencyUnits(fundsAllocated),
				currencyUnits(rc.UploadSpending),
				currencyUnits(rc.StorageSpending),
				currencyUnits(rc.DownloadSpending),
				currencyUnits(rc.FundAccountSpending),
				currencyUnits(rc.MaintenanceSpending.Sum()),
				currencyUnits(rc.RenterFunds),
				modules.FilesizeUnits(rc.Size))

			printScoreBreakdown(&hostInfo)
			return nil
		}
	}

	fmt.Println("Contract not found")
	return nil
}

// printDirs is a helper for printing directoryInfos
func printDirs(dirs []directoryInfo) error {
	for _, dir := range dirs {
		// Initialize a tab writer for the diretory
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

		// Print the Directory SiaPath
		fmt.Fprintf(w, "%v/\n", dir.dir.SiaPath)

		// Print SubDirs
		for _, subDir := range dir.subDirs {
			name := subDir.SiaPath.Name() + "/"
			size := modules.FilesizeUnits(subDir.AggregateSize)
			fmt.Fprintf(w, "  %v\t%9v\n", name, size)
		}

		// Print files
		for _, file := range dir.files {
			name := file.SiaPath.Name()
			size := modules.FilesizeUnits(file.Filesize)
			fmt.Fprintf(w, "  %v\t%9v\n", name, size)
		}
		fmt.Fprintln(w)

		// Flush the writer
		if err := w.Flush(); err != nil {
			return errors.AddContext(err, "failed to flush writer")
		}
	}
	return nil
}

// printDirsVerbose is a helper for verbose printing of directoryInfos
func printDirsVerbose(dirs []directoryInfo) error {
	for _, dir := range dirs {
		// Create a tab writer for the directory
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

		// Print the Directory SiaPath
		fmt.Fprintf(w, "%v/\n", dir.dir.SiaPath)

		// Print SubDirs
		fmt.Fprintf(w, "  Name\tFile size\tAvailable\t Uploaded\tProgress\tRedundancy\tHealth\tStuck Health\tStuck\tRenewing\tOn Disk\tRecoverable\n")
		for _, subDir := range dir.subDirs {
			name := subDir.SiaPath.Name() + "/"
			size := modules.FilesizeUnits(subDir.AggregateSize)
			redundancyStr := fmt.Sprintf("%.2f", subDir.AggregateMinRedundancy)
			if subDir.AggregateMinRedundancy == -1 {
				redundancyStr = "-"
			}
			healthStr := fmt.Sprintf("%.2f%%", skymodules.HealthPercentage(subDir.AggregateHealth))
			stuckHealthStr := fmt.Sprintf("%.2f%%", skymodules.HealthPercentage(subDir.AggregateStuckHealth))
			stuckStr := yesNo(subDir.AggregateNumStuckChunks > 0)
			fmt.Fprintf(w, "  %v\t%9v\t%9s\t%9s\t%8s\t%10s\t%7s\t%7s\t%5s\t%8s\t%7s\t%11s\n", name, size, "-", "-", "-", redundancyStr, healthStr, stuckHealthStr, stuckStr, "-", "-", "-")
		}

		// Print files
		for _, file := range dir.files {
			name := file.SiaPath.Name()
			size := modules.FilesizeUnits(file.Filesize)
			availStr := yesNo(file.Available)
			bytesUploaded := modules.FilesizeUnits(file.UploadedBytes)
			uploadStr := fmt.Sprintf("%.2f%%", file.UploadProgress)
			if file.UploadProgress == -1 {
				uploadStr = "-"
			}
			redundancyStr := fmt.Sprintf("%.2f", file.Redundancy)
			if file.Redundancy == -1 {
				redundancyStr = "-"
			}

			healthStr := fmt.Sprintf("%.2f%%", skymodules.HealthPercentage(file.Health))
			stuckHealthStr := fmt.Sprintf("%.2f%%", skymodules.HealthPercentage(file.StuckHealth))
			stuckStr := yesNo(file.Stuck)
			renewStr := yesNo(file.Renewing)
			onDiskStr := yesNo(file.OnDisk)
			recoverStr := yesNo(file.Recoverable)
			fmt.Fprintf(w, "  %v\t%9v\t%9s\t%9s\t%8s\t%10s\t%7s\t%7s\t%5s\t%8s\t%7s\t%11s\n", name, size, availStr, bytesUploaded, uploadStr, redundancyStr, healthStr, stuckHealthStr, stuckStr, renewStr, onDiskStr, recoverStr)
		}
		fmt.Fprintln(w)

		// Flush the writer
		if err := w.Flush(); err != nil {
			return errors.AddContext(err, "failed to flush writer")
		}
	}
	return nil
}

// printSingleFile is a helper for printing information about a single file
func printSingleFile(sp skymodules.SiaPath, root, skylinkCheck bool) (tryDir bool, err error) {
	var rf api.RenterFile
	if root {
		rf, err = httpClient.RenterFileRootGet(sp)
	} else {
		rf, err = httpClient.RenterFileGet(sp)
	}
	if err == nil {
		if skylinkCheck && len(rf.File.Skylinks) == 0 {
			err = errors.New("File is not pinning any skylinks")
			return
		}
		var data []byte
		data, err = json.MarshalIndent(rf.File, "", "  ")
		if err != nil {
			return
		}

		fmt.Println()
		fmt.Println(string(data))
		fmt.Println()
		return
	} else if !strings.Contains(err.Error(), filesystem.ErrNotExist.Error()) {
		err = fmt.Errorf("Error getting file %v: %v", sp.Name(), err)
		return
	}
	tryDir = true
	return
}

// renterallowancespending prints info about the current period spending
// this also get called by 'skyc renter -v' which is why it's in its own
// function
func renterallowancespending(rg api.RenterGET) {
	// Show spending detail
	totalSpent, unspentAllocated, unspentUnallocated := rg.FinancialMetrics.SpendingBreakdown()

	rate, err := types.ParseExchangeRate(build.ExchangeRate())
	if err != nil {
		fmt.Printf("Warning: ignoring exchange rate - %s\n", err)
	}

	fm := rg.FinancialMetrics
	fmt.Printf(`
Spending:
  Current Period Spending:`)

	if rg.Settings.Allowance.Funds.IsZero() {
		fmt.Printf("\n    No current period spending.\n")
	} else {
		fmt.Printf(`
    Spent Funds:              %v
      Storage:                %v
      Upload:                 %v
      Download:               %v
      FundAccount:            %v
      Maintenance:            %v
        AccountBalanceCost:   %v
        FundAccountCost:      %v
        UpdatePriceTableCost: %v
      Fees:                   %v
    Unspent Funds:            %v
      Allocated:              %v
      Unallocated:            %v
`, currencyUnitsWithExchangeRate(totalSpent, rate),
			currencyUnitsWithExchangeRate(fm.StorageSpending, rate),
			currencyUnitsWithExchangeRate(fm.UploadSpending, rate),
			currencyUnitsWithExchangeRate(fm.DownloadSpending, rate),
			currencyUnitsWithExchangeRate(fm.FundAccountSpending, rate),
			currencyUnitsWithExchangeRate(fm.MaintenanceSpending.Sum(), rate),
			currencyUnitsWithExchangeRate(fm.MaintenanceSpending.AccountBalanceCost, rate),
			currencyUnitsWithExchangeRate(fm.MaintenanceSpending.FundAccountCost, rate),
			currencyUnitsWithExchangeRate(fm.MaintenanceSpending.UpdatePriceTableCost, rate),
			currencyUnitsWithExchangeRate(fm.ContractFees, rate),
			currencyUnitsWithExchangeRate(fm.Unspent, rate),
			currencyUnitsWithExchangeRate(unspentAllocated, rate),
			currencyUnitsWithExchangeRate(unspentUnallocated, rate))
	}
}

// renterFilesAndContractSummary prints out a summary of what the renter is
// storing
func renterFilesAndContractSummary() error {
	rf, err := httpClient.RenterDirRootGet(skymodules.RootSiaPath())
	if errors.Contains(err, api.ErrAPICallNotRecognized) {
		// Assume module is not loaded if status command is not recognized.
		fmt.Printf("\n  Status: %s\n\n", moduleNotReadyStatus)
		return nil
	} else if err != nil {
		return errors.AddContext(err, "unable to get root dir with RenterDirRootGet")
	}

	rc, err := httpClient.RenterDisabledContractsGet()
	if err != nil {
		return err
	}
	redundancyStr := fmt.Sprintf("%.2f", rf.Directories[0].AggregateMinRedundancy)
	if rf.Directories[0].AggregateMinRedundancy == -1 {
		redundancyStr = "-"
	}
	// Active Contracts are all good data
	activeSize, _, _, _ := contractStats(rc.ActiveContracts)
	// Passive Contracts are all good data
	passiveSize, _, _, _ := contractStats(rc.PassiveContracts)

	w := tabwriter.NewWriter(os.Stdout, 2, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  Files:\t%v\n", rf.Directories[0].AggregateNumFiles)
	fmt.Fprintf(w, "  Total Stored:\t%v\n", modules.FilesizeUnits(rf.Directories[0].AggregateSize))
	fmt.Fprintf(w, "  Total Renewing Data:\t%v\n", modules.FilesizeUnits(activeSize+passiveSize))
	fmt.Fprintf(w, "  Repair Data Remaining:\t%v\n", modules.FilesizeUnits(rf.Directories[0].AggregateRepairSize))
	fmt.Fprintf(w, "  Stuck Repair Remaining:\t%v\n", modules.FilesizeUnits(rf.Directories[0].AggregateStuckSize))
	fmt.Fprintf(w, "  Min Redundancy:\t%v\n", redundancyStr)
	fmt.Fprintf(w, "  Active Contracts:\t%v\n", len(rc.ActiveContracts))
	fmt.Fprintf(w, "  Passive Contracts:\t%v\n", len(rc.PassiveContracts))
	fmt.Fprintf(w, "  Disabled Contracts:\t%v\n", len(rc.DisabledContracts))
	return w.Flush()
}

// renterFilesDownload downloads the file at the specified path from the Sia
// network to the local specified destination.
func renterFilesDownload(path, destination string) {
	destination = abs(destination)
	// Parse SiaPath.
	siaPath, err := skymodules.NewSiaPath(path)
	if err != nil {
		die("Couldn't parse SiaPath:", err)
	}
	// If root is not set we need to rebase.
	if !renterDownloadRoot {
		siaPath, err = siaPath.Rebase(skymodules.RootSiaPath(), skymodules.UserFolder)
		if err != nil {
			die("Couldn't rebase SiaPath:", err)
		}
	}
	// If the destination is a folder, download the file to that folder.
	fi, err := os.Stat(destination)
	if err == nil && fi.IsDir() {
		destination = filepath.Join(destination, siaPath.Name())
	}

	// Queue the download. An error will be returned if the queueing failed, but
	// the call will return before the download has completed. The call is made
	// as an async call.
	start := time.Now()
	cancelID, err := httpClient.RenterDownloadFullGet(siaPath, destination, true, true)
	if err != nil {
		die("Download could not be started:", err)
	}

	// If the download is async, report success.
	if renterDownloadAsync {
		fmt.Printf("Queued Download '%s' to %s.\n", siaPath.String(), abs(destination))
		fmt.Printf("ID to cancel download: '%v'\n", cancelID)
		return
	}

	// If the download is blocking, display progress as the file downloads.
	var file api.RenterFile
	file, err = httpClient.RenterFileRootGet(siaPath)
	if err != nil {
		die("Error getting file after download has started:", err)
	}

	failedDownloads := downloadProgress([]trackedFile{{siaPath: siaPath, dst: destination}})
	if len(failedDownloads) > 0 {
		die("\nDownload could not be completed:", failedDownloads[0].Error)
	}
	fmt.Printf("\nDownloaded '%s' to '%s - %v in %v'.\n", path, abs(destination), modules.FilesizeUnits(file.File.Filesize), time.Since(start).Round(time.Millisecond))
}

// renterFileHealthSummary prints out a summary of the status of all the files
// in the renter to track the progress of the files
func renterFileHealthSummary(dirs []directoryInfo) {
	percentages, numStuck, err := fileHealthBreakdown(dirs, false)
	if err != nil {
		die(err)
	}

	percentages = parsePercentages(percentages)

	fmt.Println("File Health Summary")
	w := tabwriter.NewWriter(os.Stdout, 2, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  %% At 100%%\t%v%%\n", percentages[0])
	fmt.Fprintf(w, "  %% Between 75%% - 100%%\t%v%%\n", percentages[1])
	fmt.Fprintf(w, "  %% Between 50%% - 75%%\t%v%%\n", percentages[2])
	fmt.Fprintf(w, "  %% Between 25%% - 50%%\t%v%%\n", percentages[3])
	fmt.Fprintf(w, "  %% Between 0%% - 25%%\t%v%%\n", percentages[4])
	fmt.Fprintf(w, "  %% Unrecoverable\t%v%%\n", percentages[5])
	fmt.Fprintf(w, "  Number of Stuck Files\t%v\n", numStuck)
	if err := w.Flush(); err != nil {
		die("failed to flush writer:", err)
	}
}

// writeContracts is a helper function to display contracts
func writeContracts(contracts []api.RenterContract) {
	fmt.Println("  Number of Contracts:", len(contracts))
	sort.Sort(byValue(contracts))
	w := tabwriter.NewWriter(os.Stdout, 2, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  \nHost\tHost PubKey\tHost Version\tRemaining Funds\tSpent Funds\tSpent Fees\tData\tEnd Height\tContract ID\tGoodForUpload\tGoodForRenew\tBadContract")
	for _, c := range contracts {
		address := c.NetAddress
		hostVersion := c.HostVersion
		if address == "" {
			address = "Host Removed"
			hostVersion = ""
		}
		// Negative Currency Check
		var contractTotalSpent types.Currency
		if c.TotalCost.Cmp(c.RenterFunds.Add(c.Fees)) < 0 {
			contractTotalSpent = c.RenterFunds.Add(c.Fees)
		} else {
			contractTotalSpent = c.TotalCost.Sub(c.RenterFunds).Sub(c.Fees)
		}
		fmt.Fprintf(w, "  %v\t%v\t%v\t%8s\t%8s\t%8s\t%v\t%v\t%v\t%v\t%v\t%v\n",
			address,
			c.HostPublicKey.String(),
			hostVersion,
			currencyUnits(c.RenterFunds),
			currencyUnits(contractTotalSpent),
			currencyUnits(c.Fees),
			modules.FilesizeUnits(c.Size),
			c.EndHeight,
			c.ID,
			c.GoodForUpload,
			c.GoodForRenew,
			c.BadContract)
	}
	if err := w.Flush(); err != nil {
		die("failed to flush writer:", err)
	}
}

// writeWorkerDownloadUploadInfo is a helper function for writing the download
// or upload information to the tabwriter.
func writeWorkerDownloadUploadInfo(download bool, w *tabwriter.Writer, rw skymodules.WorkerPoolStatus) {
	// print summary
	fmt.Fprintf(w, "Worker Pool Summary \n")
	fmt.Fprintf(w, "  Total Workers: \t%v\n", rw.NumWorkers)
	if download {
		fmt.Fprintf(w, "  Workers On Download Cooldown:\t%v\n", rw.TotalDownloadCoolDown)
	} else {
		fmt.Fprintf(w, "  Workers On Upload Cooldown:\t%v\n", rw.TotalUploadCoolDown)
	}

	// print header
	hostInfo := "Host PubKey"
	info := "\tOn Cooldown\tCooldown Time\tLast Error\tQueue\tTerminated"
	header := hostInfo + info
	if download {
		fmt.Fprintln(w, "\nWorker Downloads Detail  \n\n"+header)
	} else {
		fmt.Fprintln(w, "\nWorker Uploads Detail  \n\n"+header)
	}

	// print rows
	for _, worker := range rw.Workers {
		// Host Info
		fmt.Fprintf(w, "%v", worker.HostPubKey.String())

		// Download Info
		if download {
			fmt.Fprintf(w, "\t%v\t%v\t%v\t%v\t%v\n",
				worker.DownloadOnCoolDown,
				absDuration(worker.DownloadCoolDownTime),
				sanitizeErr(worker.DownloadCoolDownError),
				worker.DownloadQueueSize,
				worker.DownloadTerminated)
			continue
		}
		// Upload Info
		fmt.Fprintf(w, "\t%v\t%v\t%v\t%v\t%v\n",
			worker.UploadOnCoolDown,
			absDuration(worker.UploadCoolDownTime),
			sanitizeErr(worker.UploadCoolDownError),
			worker.UploadQueueSize,
			worker.UploadTerminated)
	}
}

// writeWorkerReadUpdateRegistryInfo is a helper function for writing the read registry
// or update registry information to the tabwriter.
func writeWorkerReadUpdateRegistryInfo(read bool, w *tabwriter.Writer, rw skymodules.WorkerPoolStatus) {
	// print summary
	fmt.Fprintf(w, "Worker Pool Summary \n")
	fmt.Fprintf(w, "  Total Workers: \t%v\n", rw.NumWorkers)
	if read {
		fmt.Fprintf(w, "  Workers On ReadRegistry Cooldown:\t%v\n", rw.TotalDownloadCoolDown)
	} else {
		fmt.Fprintf(w, "  Workers On UpdateRegistry Cooldown:\t%v\n", rw.TotalUploadCoolDown)
	}

	// print header
	hostInfo := "Host PubKey"
	info := "\tOn Cooldown\tCooldown Time\tLast Error\tLast Error Time\tQueue"
	header := hostInfo + info
	if read {
		fmt.Fprintln(w, "\nWorker ReadRegistry Detail  \n\n"+header)
	} else {
		fmt.Fprintln(w, "\nWorker UpdateRegistry Detail  \n\n"+header)
	}

	// print rows
	for _, worker := range rw.Workers {
		// Host Info
		fmt.Fprintf(w, "%v", worker.HostPubKey.String())

		// Qeue Info
		if read {
			status := worker.ReadRegistryJobsStatus
			fmt.Fprintf(w, "\t%v\t%v\t%v\t%v\t%v\n",
				status.OnCooldown,
				absDuration(time.Until(status.OnCooldownUntil)),
				sanitizeErr(status.RecentErr),
				status.RecentErrTime,
				status.JobQueueSize)
		} else {
			status := worker.UpdateRegistryJobsStatus
			fmt.Fprintf(w, "\t%v\t%v\t%v\t%v\t%v\n",
				status.OnCooldown,
				absDuration(time.Until(status.OnCooldownUntil)),
				sanitizeErr(status.RecentErr),
				status.RecentErrTime,
				status.JobQueueSize)
		}
	}
}
