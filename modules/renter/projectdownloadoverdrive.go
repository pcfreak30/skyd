package renter

// TODO: The pricing mechanism for these overdrive workers is not optimal
// because the pricing mechnism right now assumes there is only one overdrive
// worker and that the overdrive worker definitely is the slowest/latest worker
// out of everyone that has already launched. For the most part, these
// assumptions are going to be true in 99% of cases, so this doesn't need to be
// addressed immediately.

// bestOverdriveUnresolvedWorker will scan through a proveded list of unresolved
// workers and find the best one to use as an overdrive worker.
//
// Three values are returned. The first signifies whether the best worker is
// late. If so, any worker that is resolved should be preferred over any worker
// that is unresolved.
//
// The second return value is the unresolved duration. This is a modified
// duration based on the combination of the amount of time until the worker has
// completed its task plus the amount of time penalty the worker incurs for
// being expensive.
//
// The final return value is a wait duration, which indicates how much time
// needs to elapse before the best unresolved worker flips over into being a
// late worker.
func (pdc *projectDownloadChunk) bestOverdriveUnresolvedWorker(puws []*pcwsUnresolvedWorker) (exists, late bool, duration, waitDuration time.Duration) {
	// Set the duration and late status to the most pessimistic value.
	exists = false
	late = true
	duration = time.Duration(math.MaxInt64)
	waitDuration = time.Duration(math.MaxInt64)

	// Loop through the unresovled workers and find the best unresovled worker.
	for _, uw := range puws {
		// Figure how much time is expected to remain until the worker is
		// avaialble. Note that no price penatly is attached to the HasSector
		// call, because that call is being made regardless of the cost.
		hasSectorTime := time.Until(uw.staticExpectedCompletionTime)
		if hasSectorTime < 0 {
			hasSectorTime = 0
		}
		// Skip this worker if the best is not late but this worker is late.
		late := hasSectorTime <= 0
		if late && !bestWorkerLate {
			continue
		}

		// Figure out how much time is expected until the worker completes the
		// download job.
		//
		// TODO: Break into helper function and unit test.
		readTime := uw.staticWorker.staticJobReadQueue.callExpectedJobTime(pdc.pieceLength)
		if readTime < 0 {
			readTime = 0
		}
		// Add a penalty to performance based on the cost of the job. Need to be
		// careful with the underflow cases.
		//
		// TODO: Break into helper function and unit test.
		if pdc.pricePerMS.IsZero() {
			pdc.pricePerMS = types.NewCurrency64(1)
		}
		expectedRSCompletionCost := uw.staticWorker.staticJobReadQueue.callExpectedJobCost(pdc.pieceLength)
		rsPricePenalty, err := expectedRSCompletionCost.Div(pdc.pricePerMS).Uint64()
		if err != nil || rsPricePenalty > math.MaxInt64 {
			readTime = time.Duration(math.MaxInt64)
		} else if reduced := math.MaxInt64 - int64(rsPricePenalty); int64(readTime) > reduced {
			readTime = time.Duration(math.MaxInt64)
		} else {
			readTime += time.Duration(rsPricePenalty)
		}
		adjustedTotalDuration := hasSectorTime + readTime

		// Compare the total time (including price preference) to the current
		// best time. Workers that are not late get preference over workers that
		// are late.
		betterLateStatus := bestWorkerLate && !late
		betterDuration := adjustedTotalDuration < bestUnresolvedDuration
		if betterLateStatus || betterDuration {
			bestWorkerExists = true
			bestUnresolvedDuration = adjustedTotalDuration
			if !late {
				bestUnresolvedWaitDuration = hasSectorTime
				bestWorkerLate = false
			}
		}
	}
	return bestWorkerLate, bestUnresolvedDuration, bestUnresolvedWaitDuration
}

// findBestOverdriveWorker will look at all of the workers that can help fetch a
// new piece. If a good worker is found, that worker is returned along with the
// index of the piece it should fetch. If no good worker is found but a good
// worker is in the queue, two channels will be returned, either of which can
// signal that a new worker may be available.
//
// An error will only be returned if there are not enough workers to complete
// the download. If there are enough workers to complete the download, but all
// potential workers have already been launched, all of the return values will
// be nil.
//
// If there are no workers at all that may be able to contribute, an error is
// returned, which means the download must either proceed with its current set
// of workers or must fail.
//
// NOTE: There is an edge case where all of the pieces are already active. In
// this case, a worker may be returned that overlaps with another worker. From
// an erasure coding perspective, this is inefficient, but can be useful if
// there is an expectation that lots of existing workers will fail.
//
// TODO: Remember the edge case where all unresolved workers have not returned
// yet and there are no other options. There is no timer in that case, only
// blocking on workersUpdatedChan.
func (pdc *projectDownloadChunk) findBestOverdriveWorker() (*worker, uint64, time.Duration, <-chan struct{}) {
	// Find the best unresolved worker. The return values include an 'adjusted
	// duration', which indicates how long the worker takes accounting for
	// pricing, and the 'wait duration', which is the max amount of time that we
	// should wait for this worker to return with a result from HasSector.
	//
	// If the best unresolved worker is late, the wait duration will be zero.
	// Technically these values are redundant but the code felt cleaner to
	// return them explicitly.
	//
	// buw = bestUnresolvedWorker
	unresolvedWorkers, updateChan := pdc.unresolvedWorkers()
	buwExists, buwLate, buwAdjustedDuration, buwWaitDuration := pdc.bestUnresolvedWorker(unresolvedWorkers)

	// Loop through the set of pieces to find the fastest worker that can be
	// launched. Because this function is only called for overdrive workers, we
	// can assume that any launched piece is already late.
	//
	// baw = bestAvailableWorker
	bawExists := false
	bawAdjustedDuration := time.Duration(math.MaxInt64)
	bawPieceIndex = 0
	var baw *worker
	for i, activePiece := range pdc.availablePieces {
		// This piece should be skipped if it is completed.
		complete := false
		for _, pieceDownload := range activePiece {
			// No need to check the rest of the pieces if this piece is
			// complete.
			if pieceDownload.completed {
				complete = true
				break
			}

			// Determine if this worker is better than any existing worker.
			workerAdjustedDuration := pdc.getAdjustedDuration(pieceDownload.worker)
			if workerAdjustedDuration < bawAdjustedDuration {
				bawExists = true
				bawAdjustedDuration = workerAdjustedDuration
				bawPieceIndex = i
				baw = pieceDownload.worker
			}
		}
		// Don't consider any workers from this piece if the piece is completed.
		if complete {
			continue
		}
	}

	// Return nil if there are no workers that can be launched.
	if !buwExists && !bawExists {
		// All 'nil' return values, meaning the download can succeed by waiting
		// for already launched workers to return, but cannot succeed by
		// launching new workers because no new workers are available.
		return nil, 0, 0, nil
	}

	// Return the buw values unconditionally if there is no baw. Also return the
	// buw if the buw is not late and has a better duration than the baw.
	buwNoBaw = buwExists && !bawExists
	buwBetter = !buwLate && buwAdjustedDuration < bawAdjustedDuration
	if buwNoBaw || buwBetter {
		return nil, 0, buwWaitDuration, updateChan
	}

	// Retrun the baw.
	return baw, bawPieceIndex, 0, nil
}

// launchOverdriveWorker will launch a worker and update the corresponding
// available piece.
func (pdc *projectDownloadChunk) launchOverdriveWorker(w *worker, pieceIndex uint64) {
	// Create the read sector job for the worker.
	//
	// TODO: The launch process should minimally have as input the ctx of
	// the pdc, that way if the pdc closes we know to garbage collect the
	// channel and not send down it. Ideally we can even cancel the job if
	// it is in-flight.
	jrs := &jobReadSector{
		jobRead: jobRead{
			staticResponseChan: pdc.workerResponseChan,
			staticLength:       pdc.pieceLength,

			staticSector: pdc.workerSet.staticPieceRoots[pieceIndex],

			jobGeneric: newJobGeneric(w.staticJobReadQueue, pdc.ctx.Done()),
		},
		staticOffset: pdc.pieceOffset,
	}
	// Submit the job.
	expectedCompletionTime, added := w.staticJobReadQueue.callAddWithEstimate(jrs)

	// Update the status of the piece that was launched. 'launched' should be
	// set to 'true'. If the launch failed, 'failed' should be set to 'true'. If
	// the launch succeeded, the expected completion time of the job should be
	// set.
	//
	// NOTE: We don't break out of the loop when we find a piece/worker
	// match. If all is going well, each worker should appear at most once
	// in this piece, but for the sake of defensive programming we check all
	// elements anyway.
	for _, pieceDownload := range pdc.availablePieces[pieceIndex] {
		if w.staticHostPubKeyStr == pieceDownload.worker.staticHostPubKeyStr {
			pieceDownload.launched = true
			if added {
				pieceDownload.expectedCompletionTime = expectedCompletionTime
			} else {
				pieceDownload.failed = true
			}
		}
	}
}

// overdriveStatus will return the number of overdrive workers that need to be
// launched, and the expected return time of the slowest worker that has already
// launched a download task.
func (pdc *projectDownloadChunk) overdriveStatus() (int, time.Time) {
	// Go through the pieces, determining how many pieces are launched without
	// fail, and what the latest return time is of all the workers that have
	// already been launched.
	numLWF := 0 // LWF = launchedWithoutFail
	var latestReturn time.Time
	for _, piece := range pdc.availablePieces {
		launchedWithoutFail := false
		for _, pieceDownload := range piece {
			if pieceDownload.launched && !pieceDownload.failed {
				launchedWithoutFail = true
				if !pieceDownload.completed && latestReturn.Before(pieceDownload.expectedCompletionTime) {
					latestReturn = pieceDownload.expectedCompletionTime
				}
			}
		}
		if launchedWithoutFail {
			numLWF++
		}
	}

	// If there are not enough LWF workers to complete the download, return the
	// number of workers that need to launch in order to complete the download.
	workersWanted := pdc.workerSet.staticErasureCoder.MinPieces()
	if numLWF < workersWanted {
		return workersWanted-numLWF, latestReturn
	}

	// If the latest worker should have already completed its job, return that
	// an overdrive worker should be launched.
	if time.Until(latestReturn) <= 0 {
		return 1, latestReturn
	}

	// If the latest worker is expected to return at some point in the future,
	// there is no immediate need to launch an overdrive worker.
	return 0, latestReturn
}

// tryLaunchOverdriveWorker will attempt to launch an overdrive worker. A worker
// may not be launched if the best worker is not yet available.
func (pdc *projectDownloadChunk) tryLaunchOverdriveWorker() (<-chan struct{}, <-chan time.Duration) {
	worker, pieceIndex, sleepTime, wakeChan := pdc.findBestOverdriveWorker()
	if worker == nil {
		return wakeChan, time.After(sleepTime)
	}

	// If there was a worker found, launch the worker.
	pdc.launchOverdriveWorker(worker, pieceIndex)
}

// tryOverdrive will determine whether an overdrive worker needs to be launched.
// If so, it will launch an overdrive worker asynchronously. It will return a
// channel which will be closed when tryOverdrive should be called again.
func (pdc *projectDownloadChunk) tryOverdrive() (<-chan struct{}, <-chan time.Duration) {
	// Fetch the number of overdrive workers that are needed, and the latest
	// return time of any active worker.
	neededOverdriveWorkers, latestReturn := pdc.overdriveStatus()

	// Launch all of the workers that are needed. If at any point a launch
	// fails, return the status channels to try again.
	for i := 0; i < neededOverdriveWorkers; i++ {
		wakeChan, workersLateChan, workerLaunched, noMoreWorkers, expectedReturnTime := pdc.tryLaunchOverdriveWorker()
		// Check if there are no more workers available to launch jobs. If there
		// are no more workers available, 'nil' channels are returned because
		// there will be no updates in the future which create new opportunities
		// to launch workers.
		if noMoreWorkers {
			return nil, nil
		}
		// If a worker failed to launch but there are more workers, that means
		// we are waiting for an unresolved worker to resolve. Return the two
		// channels related to waiting for an update.
		if !workerLaunched {
			return wakeChan, workersLateChan
		}
		// Update the latest return to account for the amount of time this
		// worker is expected to take to return.
		if latestReturn.Before(expectedReturnTime) {
			latestReturn = expectedReturnTime
		}
	}

	// All needed overdrive workers have been launched. No need to try again
	// until the current set of workers are late.
	return nil, time.After(time.Until(latestReturn))
}
