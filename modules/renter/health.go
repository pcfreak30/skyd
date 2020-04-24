package renter

import (
	"time"

	"gitlab.com/NebulousLabs/errors"

	"gitlab.com/NebulousLabs/Sia/modules"
)

// managedOldestHealthCheckTime finds the lowest level directory with the oldest
// LastHealthCheckTime
func (r *Renter) managedOldestHealthCheckTime() (modules.SiaPath, time.Time, error) {
	// Check the siadir metadata for the root files directory
	siaPath := modules.RootSiaPath()
	metadata, err := r.managedDirectoryMetadata(siaPath)
	if err != nil {
		return modules.SiaPath{}, time.Time{}, err
	}

	// Follow the path of oldest LastHealthCheckTime to the lowest level
	// directory
	for metadata.NumSubDirs > 0 {
		// Check to make sure renter hasn't been shutdown
		select {
		case <-r.tg.StopChan():
			return modules.SiaPath{}, time.Time{}, errors.New("Renter shutdown before oldestHealthCheckTime could be found")
		default:
		}

		// Check for sub directories
		subDirSiaPaths, err := r.managedSubDirectories(siaPath)
		if err != nil {
			return modules.SiaPath{}, time.Time{}, err
		}

		// Find the oldest LastHealthCheckTime of the sub directories
		updated := false
		for _, subDirPath := range subDirSiaPaths {
			// Check to make sure renter hasn't been shutdown
			select {
			case <-r.tg.StopChan():
				return modules.SiaPath{}, time.Time{}, errors.New("Renter shutdown before oldestHealthCheckTime could be found")
			default:
			}

			// Check lastHealthCheckTime of sub directory
			subMetadata, err := r.managedDirectoryMetadata(subDirPath)
			if err != nil {
				return modules.SiaPath{}, time.Time{}, err
			}

			// If the LastHealthCheckTime is after current LastHealthCheckTime
			// continue since we are already in a directory with an older
			// timestamp
			if subMetadata.AggregateLastHealthCheckTime.After(metadata.AggregateLastHealthCheckTime) {
				continue
			}

			// Update LastHealthCheckTime and follow older path
			updated = true
			metadata = subMetadata
			siaPath = subDirPath
		}

		// If the values were never updated with any of the sub directory values
		// then return as we are in the directory we are looking for
		if !updated {
			return siaPath, metadata.AggregateLastHealthCheckTime, nil
		}
	}

	return siaPath, metadata.AggregateLastHealthCheckTime, nil
}

// threadedUpdateRenterHealth reads all the siafiles in the renter, calculates
// the health of each file and updates the folder metadata
func (r *Renter) threadedUpdateRenterHealth() {
	// Loop until the renter has shutdown or until the renter's top level files
	// directory has a LasHealthCheckTime within the healthCheckInterval
	for {
		select {
		// Check to make sure renter hasn't been shutdown
		case <-r.tg.StopChan():
			return
		default:
		}

		// Follow path of oldest time, return directory and timestamp
		siaPath, lastHealthCheckTime, err := r.managedOldestHealthCheckTime()
		if err != nil {
			// If there is an error getting the lastHealthCheckTime sleep for a
			// little bit before continuing
			r.log.Println("Health loop failed to find a file that needs updating:", err)
			select {
			case <-time.After(healthLoopErrorSleepDuration):
			case <-r.tg.StopChan():
				return
			}
			continue
		}

		// Check if the time since the last check on the least recently checked
		// folder is inside the health check interval. If so, the whole
		// filesystem has been checked recently, and we can sleep until the
		// least recent check is outside the check interval.
		timeSinceLastCheck := time.Since(lastHealthCheckTime)
		if timeSinceLastCheck < healthCheckInterval {
			// Sleep until the least recent check is outside the check interval.
			sleepDuration := healthCheckInterval - timeSinceLastCheck
			r.log.Debugln("Health loop sleeping for", sleepDuration)
			wakeSignal := time.After(sleepDuration)
			select {
			case <-r.tg.StopChan():
				return
			case <-wakeSignal:
				continue
			}
		}

		r.log.Debugln("Updating health for '", siaPath.String(), "'")
		err = r.managedBubbleMetadata(siaPath)
		if err != nil {
			r.log.Println("Error updating health of '", siaPath.String(), "':", err)
			select {
			case <-time.After(healthLoopErrorSleepDuration):
			case <-r.tg.StopChan():
				return
			}
		}
	}
}
