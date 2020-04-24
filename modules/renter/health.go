package renter

// threadedUpdateRenterHealth reads all the siafiles in the renter, calculates
// the health of each file and updates the folder metadata
func (r *Renter) threadedUpdateRenterHealth() {
	err := r.tg.Add()
	if err != nil {
		return
	}
	defer r.tg.Done()

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
		r.log.Debugln("Checking for oldest health check time")
		siaPath, lastHealthCheckTime, err := r.managedOldestHealthCheckTime()
		if err != nil {
			// If there is an error getting the lastHealthCheckTime sleep for a
			// little bit before continuing
			r.log.Debug("WARN: Could not find oldest health check time:", err)
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
			}
		}
		r.log.Debug("Health Loop calling bubble on '", siaPath.String(), "'")
		err = r.managedBubbleMetadata(siaPath)
		if err != nil {
			r.log.Println("Error calling managedBubbleMetadata on `", siaPath.String(), "`:", err)
			select {
			case <-time.After(healthLoopErrorSleepDuration):
			case <-r.tg.StopChan():
				return
			}
		}
	}
}
