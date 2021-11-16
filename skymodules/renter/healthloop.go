package renter

// healthloop.go houses the code that runs the health loop. The health loop is
// called the health loop because its main purpose is to check the churn levels
// on all of the files. As hosts enter and leave the Sia network, we need to
// make sure that files are being repaired.
//
// The health loop does its job by just generally updating the metadata of all
// directories, so it could just as well be called the 'metadata loop'. But if
// there wasn't host churn on the network, the health loop probably wouldn't
// exist, and aggregate metadata would just be updated as the files are updated.
//
// NOTE: The stateful variable of the health loop is not exposed to the renter
// in any way. For the most part, the renter can learn everything useful about
// the health loop by checking the numbers in the root siadir.

// TODO: Once tagged logging is in place, everywhere that a log has the prefix
// 'HEALTH LOOP', we can swap out that log.Println for a
// log.Tagline("health-loop", $msg), meaning those lines will always print if
// someone activates the tag "health-loop" in the logger. "HEALTH LOOP VERBOSE"
// logs can be given the tag "health-loop-verbose".

import (
	"fmt"
	"sync/atomic"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

const (
	// The system scan time estimator decay values determine how much decay
	// should be applied to the estimator. A decay that is closer to 1 will take
	// into account more historical data, and a decay that is closer to 0 will
	// be more responsive to changes in the environment.
	systemScanTimeEstimatorDecay = 0.9
)

var (
	// emptyFilesystemSleepDuration determines how long the health loop will
	// sleep if there are files in the filesystem.
	emptyFilesystemSleepDuration = build.Select(build.Var{
		Dev:      5 * time.Second,
		Standard: 5 * time.Minute,
		Testing:  2 * time.Second,
	}).(time.Duration)

	// healthLoopErrorSleepDuration indicates how long the health loop should
	// sleep before retrying if there is an error preventing progress.
	healthLoopErrorSleepDuration = build.Select(build.Var{
		Dev:      9 * time.Second,
		Standard: 5 * time.Minute,
		Testing:  time.Second,
	}).(time.Duration)

	// healthLoopResetInterval defines how frequently the health loop resets,
	// cleaning out its cache and restarting from root.
	healthLoopResetInterval = build.Select(build.Var{
		Dev:      30 * time.Second,
		Standard: 15 * time.Minute,
		Testing:  2 * time.Second,
	}).(time.Duration)

	// TargetHealthCheckFrequency defines how frequently we want to update the
	// health of the filesystem when everything is running smoothly. The goal of
	// the health check system is to spread the health checks of files over this
	// interval, so that the load of performing health checks is as light as
	// possible when the system is healthy.
	//
	// For standard builds, we're targeting 24 hours as a sign of a filesystem
	// in good health. This value is picked based on the rate at which hosts
	// churn through Skynet - in the course of 24 hours, we should never have
	// enough churn to have built up a concerning amount of repair burden.
	TargetHealthCheckFrequency = build.Select(build.Var{
		Dev:      3 * time.Minute,
		Standard: 24 * time.Hour,
		Testing:  5 * time.Second,
	}).(time.Duration)

	// urgentHealthCheckFrequency is the time at which we feel the health of the
	// system has reached an urgent state, we haven't checked the health of
	// certain files in so long that the system should be running at full speed
	// performing only health checks.
	//
	// As the health time of the files in the filesystem grows from the target
	// health check frequency to the urgent health check frequency, the
	// percentage of resources that are devoted to the health checks will
	// linearly increase. If the recent health time of current files is nearly
	// at the urgent frequency, the health loop will be running nearly at full
	// speed. If the recent health time of the current files is only halfway
	// there, the health loop will run halfway between proprtional speed and
	// full speed.
	urgentHealthCheckFrequency = build.Select(build.Var{
		Dev:      9 * time.Minute,
		Standard: 72 * time.Hour,
		Testing:  10 * time.Second,
	}).(time.Duration)
)

// healthLoopDirFinder is a helper structure which keeps track of which
// directories the health loop has visited and which directories still need to
// be visited
//
// NOTE: this struct is not thread safe, it is only intended to be used in
// single-threaded situations.
type healthLoopDirFinder struct {
	// If the user has triggered a manual check, the health loop should run at
	// full speed until the check is complete. We track whether it's complete by
	// looking at whether the latest aggregate health time is later than the
	// moment the check was triggered.
	manualCheckTime time.Time

	nextDir          skymodules.SiaPath // The next dir to scan and update.
	filesInNextDir   uint64             // An approximation of the number of files in the next dir we will be scanning.
	leastRecentCheck time.Time          // The time of the least recently checked dir in the filesystem.
	totalFiles       uint64             // An approximation of the total number of files in the filesystem.

	// These variables are used to estimate how long it takes to scan the
	// filesystem when you exclude the sleeps. The weighted values are used to
	// compute an exponential moving average to get a more accurate estimate
	// based on historic data. The window variables count up the stats in the
	// most recent window of time.
	estimatedSystemScanDuration time.Duration
	systemScanDurationAvg       *expMovingAvg
	windowFilesProcessed        uint64
	windowSleepTime             time.Duration
	windowStartTime             time.Time

	renter *Renter
}

// computeUpdatedEstimatedSystemScanDuration computes the estimated system scan
// duration of the dirFinder. It uses an exponential moving average, compressing
// historic values and then adding the new values from the recent window.
// Finally, it resets the new values from the recent window so that the EMA is
// not corrupted if called multiple times.
func (dirFinder *healthLoopDirFinder) updateEstimatedSystemScanDuration() {
	if dirFinder.windowFilesProcessed > 0 {
		processingTime := time.Since(dirFinder.windowStartTime) - dirFinder.windowSleepTime
		estimatedScanDuration := float64(processingTime) * float64(dirFinder.totalFiles) / float64(dirFinder.windowFilesProcessed)
		dirFinder.systemScanDurationAvg.addDataPoint(estimatedScanDuration)
		dirFinder.estimatedSystemScanDuration = time.Duration(dirFinder.systemScanDurationAvg.average())

		// Set the renter's estimated system scan duration as well.
		atomic.StoreUint64(&dirFinder.renter.atomicSystemHealthScanDuration, uint64(dirFinder.estimatedSystemScanDuration))
	}

	// Reset the window variables.
	dirFinder.windowFilesProcessed = 0
	dirFinder.windowSleepTime = 0
	dirFinder.windowStartTime = time.Now()
}

// reset will reset the dirFinder and start the dirFinder back at the root
// level.
//
// TODO: When tiered caching is added, reset the tiered caching here.
//
// TODO: If we aren't doing everything from root, then upon reset we need to
// commit the directory metadata changes in every part of our cacheing layer, so
// the changes exist on disk.
func (dirFinder *healthLoopDirFinder) reset() {
	filesProcessed := dirFinder.windowFilesProcessed
	timeTaken := time.Since(dirFinder.windowStartTime) - dirFinder.windowSleepTime
	dirFinder.updateEstimatedSystemScanDuration()
	dirFinder.renter.staticLog.Printf("HEALTH LOOP: scanned %v files in %v, resulting in a new estimated full scan duration of %v", filesProcessed, timeTaken, dirFinder.estimatedSystemScanDuration)
}

// loadNextDir will find the next directory with the worst health and load
// it.
//
// TODO: This function can be significantly optimized by remembering/cacheing
// the healths of the levels above us, it's still roughly log(n) space but
// allows us to cut down on the reads and even attempt to linearize.
//
// TODO: We can attempt to linearize by refusing to retreat back up a level if
// the other directories at our current level are reasonably within the timeout
// range, preferring to go deeper here and making the structure more linear in
// the future.
//
// TODO: There's an idea of regionalized dfs, where we attempt to explore all
// the potential directories in order of health, but while we are in a given
// region we potentially add extra directories since it is cheaper to do them
// now while in the region than to come back to them later. The full algorithm
// is a bit involved, consult David before attempting.
func (dirFinder *healthLoopDirFinder) loadNextDir() error {
	// Check if we need to reset the dirFinder.
	if time.Since(dirFinder.windowStartTime) > healthLoopResetInterval {
		dirFinder.reset()
	}

	// Check the siadir metadata for the root files directory.
	siaPath := skymodules.RootSiaPath()
	metadata, err := dirFinder.renter.managedDirectoryMetadata(siaPath)
	if err != nil {
		return errors.AddContext(err, "unable to load root metadata")
	}
	dirFinder.totalFiles = metadata.AggregateNumFiles
	dirFinder.leastRecentCheck = metadata.AggregateLastHealthCheckTime

	// Run a loop that will continually descend into child directories until it
	// discovers the directory with the least recent health check time.
	for {
		// Load any subdirectories.
		subDirSiaPaths, err := dirFinder.renter.managedSubDirectories(siaPath)
		if err != nil {
			errStr := fmt.Sprintf("error when fetching the sub directories of %s", siaPath)
			return errors.AddContext(err, errStr)
		}

		// Find the oldest LastHealthCheckTime of the sub directories
		betterSubdirFound := false
		for _, subDirPath := range subDirSiaPaths {
			// Load the metadata of this subdir.
			subMetadata, err := dirFinder.renter.managedDirectoryMetadata(subDirPath)
			if err != nil {
				errStr := fmt.Sprintf("unable to load the metadata of subdirectory %s", subDirPath)
				return errors.AddContext(err, errStr)
			}

			// Check whether this subdir is better.
			if !subMetadata.AggregateLastHealthCheckTime.After(metadata.AggregateLastHealthCheckTime) {
				betterSubdirFound = true
				siaPath = subDirPath
				metadata = subMetadata
			}
		}
		// If a better subdir was not discovered, this is the winning subdir.
		if !betterSubdirFound {
			break
		}
	}

	dirFinder.filesInNextDir = metadata.NumFiles
	dirFinder.nextDir = siaPath
	return nil
}

// sleepDurationBeforeNextDir will determine how long the health loop should
// sleep before processing the next directory.
//
// NOTE: The dir finder tries to estimate the amount of time that it takes to
// process the entire filesystem if there was no sleeping. It does this by
// remembering how long it has told callers to sleep, which means that in order
// for the estimate to be correct, the callers *must* sleep after making this
// call.
func (dirFinder *healthLoopDirFinder) sleepDurationBeforeNextDir() time.Duration {
	// If there are no files, return a standard time for sleeping.
	//
	// NOTE: Without this check, you get a divide by zero.
	if dirFinder.totalFiles == 0 {
		dirFinder.renter.staticLog.Println("HEALTH LOOP: sleeping because the total files in the filesystem is zero")
		return emptyFilesystemSleepDuration
	}

	// Sleep before processing any directories. The amount of sleep will be
	// determined by the recent health time of the provided directory compared
	// against the target health time. If the health time is more recent, we
	// will sleep a proportionate amount of time so that we average scanning the
	// entire filesystem once per target interval, but evenly spaced throughout
	// that interval.
	//
	// If the recent check time is later than the target interval, the amount of
	// sleep is reduced proportionally to the distance from the urgent time.
	// This proportional reduction still has a bit of a spreading effect, to
	// keep the load distributed over a large range of time rather than
	// clustered.
	//
	// If the recent check is later than the urgent interval, there is no sleep
	// at all because we need to get the updated health status on the files.
	lrc := dirFinder.leastRecentCheck
	timeSinceLRC := time.Since(lrc)
	urgent := timeSinceLRC > urgentHealthCheckFrequency
	slowScanTime := dirFinder.estimatedSystemScanDuration >= TargetHealthCheckFrequency
	manualCheckActive := dirFinder.manualCheckTime.After(lrc)
	// If a manual check is currently active, or if the condition of the
	// file health is urgent, or if the amount of time it takes to scan the
	// filesystem is longer than the target health interval, do not try to
	// sleep.
	if urgent || manualCheckActive || slowScanTime {
		dirFinder.renter.staticLog.Println("HEALTH LOOP VERBOSE: skipping a sleep", urgent, manualCheckActive, slowScanTime)
		return 0
	}

	// Compute the sleepTime. We want to sleep such that we check files
	// at a rate that is perfectly evenly spread over the target health
	// check interval. To compute that, you divide the target health
	// check interval by the total number of files.
	//
	// We update an entire directory at once though, so we need to
	// multiply the sleep time by the total number of files in the
	// directory.
	//
	// Implemented naively, the average amount of time we sleep per
	// cycle is exactly equal to the target health check interval, which
	// gives us zero computational time to do the health check itself.
	// To compensate for that, we track how much time we spend in system
	// scan per cylce and subtract that from the numerator of the above
	// described equation.
	desiredSleepPerScan := TargetHealthCheckFrequency - dirFinder.estimatedSystemScanDuration
	sleepTime := desiredSleepPerScan * time.Duration(dirFinder.filesInNextDir) / time.Duration(dirFinder.totalFiles)
	// If we are behind schedule, we compress the sleep time
	// proportionally to how far behind schedule we are.
	if timeSinceLRC > TargetHealthCheckFrequency {
		// We are behind schedule, compute the percentage progress
		// towards urgency that we are. For example, if we are 1 minute
		// later than the target health check frequency, and the urgent
		// frequency is 100 minutes later than the target frequency,
		// reduce the amount of sleep by 1%. If 2 minutes later than
		// target, reduce by 2%, etc.
		//
		// NOTE: This is safe from divide by zero errors because we check
		// earlier in the program that the urgent time is strictly greater than
		// the target time.
		compressionNum := float64(timeSinceLRC - TargetHealthCheckFrequency)
		compressionDenom := float64(urgentHealthCheckFrequency - TargetHealthCheckFrequency)
		compression := 1 - (compressionNum / compressionDenom)
		sleepTime = time.Duration(float64(sleepTime) * compression)
	}
	dirFinder.windowSleepTime += sleepTime
	return sleepTime
}

// processNextDir performs the actual health check and update on the directory
// that was discovered in loadNextDir.
func (dirFinder *healthLoopDirFinder) processNextDir() error {
	// Scan and update the healths of all the files in the directory, and update
	// the corresponding directory metadata.
	nextDir := dirFinder.nextDir
	err := dirFinder.renter.managedUpdateFilesInDir(nextDir)
	if err != nil {
		errStr := fmt.Sprintf("unable to process directory %s from within the health loop", nextDir)
		return errors.AddContext(err, errStr)
	}
	dirFinder.windowFilesProcessed += dirFinder.filesInNextDir
	err = dirFinder.renter.managedUpdateDirMetadata(nextDir)
	if err != nil {
		errStr := fmt.Sprintf("unable to update the metadata of directory %s", nextDir)
		return errors.AddContext(err, errStr)
	}

	// Update the metadatas of all the underlying directories up to root. This
	// won't scan and update all of the inner files, it'll just use the
	// metadatas that the inner files already have. Most skynet portals only
	// have files at the leaf directories, so it shouldn't make a big difference
	// either way.
	for !nextDir.IsRoot() {
		parent, err := nextDir.Dir()
		if err != nil {
			str := fmt.Sprint("unable to get the parent of the non-root siapath:", nextDir)
			err = errors.AddContext(err, str)
			build.Critical(err)
			return err
		}
		nextDir = parent
		err = dirFinder.renter.managedUpdateDirMetadata(nextDir)
		if err != nil {
			errStr := fmt.Sprintf("unable to update the metadata of directory %s", nextDir)
			return errors.AddContext(err, errStr)
		}
	}
	return nil
}

// newHealthLoopDirFinder creates a new dir finder that is ready to perform
// health checks.
func (r *Renter) newHealthLoopDirFinder() *healthLoopDirFinder {
	return &healthLoopDirFinder{
		windowStartTime:       time.Now(),
		systemScanDurationAvg: newExpMovingAvg(systemScanTimeEstimatorDecay),

		renter: r,
	}
}

// threadedHealthLoop is a permanent background loop in the renter that keeps
// the health of the files up to date.
//
// NOTE: The entire health loop is single threaded. If the system is under load
// such that the health loop could benefit from being multi-threaded, the CPU
// and disk IO cost of doing the health checks would probably causing
// significant disruption to other services on the Skynet portal. The health
// checks really should never be consuming more than a fraction of the total
// system resources, it's a sign that you need more servers rather than more
// threads if your health loop is not keeping up on a single thread.
func (r *Renter) threadedHealthLoop() {
	err := r.tg.Add()
	if err != nil {
		return
	}
	defer r.tg.Done()

	// Perform a check that the constants are configured correctly.
	//
	// NOTE: If this invariant is broken, it could cause divide by zero errors.
	if urgentHealthCheckFrequency <= TargetHealthCheckFrequency {
		panic("constants are set incorrectly, TargetHealthCheckFrequenecy needs to be smaller than urgentHealthCheckFrequency")
	}

	// Launch the background loop to perform health checks on the filesystem.
	dirFinder := r.newHealthLoopDirFinder()
	for {
		// Load the next directory. In the event of an error, reset and try again.
		err := dirFinder.loadNextDir()
		for err != nil {
			// Log the error and then sleep.
			r.staticLog.Println("Error loading next directory:", err)
			select {
			case <-time.After(healthLoopErrorSleepDuration):
			case <-r.tg.StopChan():
				return
			}

			// Try again to load the next directory. The logic inside the
			// function handles any resets that are required. Normally the reset
			// would be handled out here, but that made the error handling and
			// logging incredibly verbose.
			err = dirFinder.loadNextDir()
		}

		// Sleep before processing the next directory. This also serves as the
		// exit condition for the loop.
		//
		// NOTE: The dirFinder tries to measure a throughput to estimate how
		// long it would take to scan the entire filesystem, this estimate
		// ignores the sleep time. In order for the estimate to be correct, this
		// loop *must* sleep every time that it calls
		// sleepDurationBeforeNextDir().
		//
		// NOTE: Need to make sure this is called after 'loadNextDir' so that
		// the right amount of sleep time is chosen, as the sleep duration will
		// depend on which directory is up next.
		sleepTime := dirFinder.sleepDurationBeforeNextDir()
		r.staticLog.Println("HEALTH LOOP VERBOSE: sleeping before next directory", sleepTime)
		select {
		case <-time.After(sleepTime):
		case <-r.tg.StopChan():
			return
		}

		// Process the next directory. We don't retry on error, we just move on
		// to the next directory.
		err = dirFinder.processNextDir()
		if err != nil {
			r.staticLog.Println("Error processing a directory in the health loop:", err)
		}
	}
}
