package renter

// processNextDirectory performs the actual health check and update on the
// directory that was discovered in loadNextDirectory.
func (dirFinder *healthLoopDirFinder) processNextDirectory() error {
	// Scan and update the healths of all the files in the directory, and update
	// the corresponding directory metadata.
	nextDir := dirFinder.nextDir
	err := dirFinder.renter.managedUpdateFilesInDirectory(nextDir)
	if err != nil {
		errStr := fmt.Sprintf("unable to process directory %s from within the health loop", nextDir)
		return errors.AddContext(err, errStr)
	}
	dirFinder.cumulativeFilesProcessed += dirFinder.filesInNextDir

	// Update the metadatas of all the underlying directories up to root. This
	// won't scan and update all of the inner files, it'll just use the
	// metadatas that the inner files already have. Most skynet portals only
	// have files at the leaf directories, so it shouldn't make a big difference
	// either way.
	for !nextDir.IsRoot() {
		parent, err = nextDir.Dir()
		if err != nil {
			errStr := fmt.Sprintf("unable to get the parent directory of %s", nextDir)
			return errors.AddContext(err, errStr)
		}
		nextDir = parent
		err = nextDir.managedUpdateDirectoryMetadata()
		if err != nil {
			errStr := fmt.Sprintf("unable to update the metadata of directory %s", nextDir)
			return errors.AddContext(err, errStr)
		}
	}
	return nil
}

// managedUpdateFilesInDirectory will update all of the files in the directory
// to have recent health information, then it will update the metadata of the
// directory as well.
func (r *Renter) managedUpdateFilesInDirectory(siaPath skymodules.SiaPath) (siadir.Metadata, error) {
	// Update the File metadatas in the directory.
	//
	// NOTE: the result of callRenterContractsAndUtilites already has a caching
	// layer, no need to cache the results here.
	//
	// TODO: Combine this with the next call to save a call to ReadDir
	offlineMap, goodForRenewMap, contracts, used := r.callRenterContractsAndUtilities()
	err = r.managedUpdateFileMetadatasParams(siaPath, offlineMap, goodForRenewMap, contracts, used)
	if err != nil {
		e := fmt.Sprintf("unable to update the file metadatas for directory '%v'", siaPath.String())
		return siadir.Metadata{}, errors.AddContext(err, e)
	}
	return r.managedUpdateDirectoryMetadata(siaPath)
}

// managedUpdateDirectoryMetadata will update the metadata for just this
// directory, without computing all the healths of the files.
func (r *Renter) managedUpdateDirectoryMetadata(siaPath skymodules.SiaPath) error {
	// Calculate the new metadata values of the directory
	//
	// TODO: Either pass in the stuff from the previous call or combine the
	// calls.
	metadata, err := r.callCalculateDirectoryMetadata(siaPath)
	if err != nil {
		e := fmt.Sprintf("could not calculate the metadata of directory '%v'", siaPath.String())
		return siadir.Metadata{}, errors.AddContext(err, e)
	}

	// Update directory metadata with the health information. Don't return here
	// to avoid skipping the repairNeeded and stuckChunkFound signals.
	siaDir, err := r.staticFileSystem.OpenSiaDir(siaPath)
	if err != nil {
		e := fmt.Sprintf("could not open directory %v", siaPath.String())
		err = errors.AddContext(err, e)
	} else {
		defer func() {
			err = errors.Compose(err, siaDir.Close())
		}()
		err = siaDir.UpdateBubbledMetadata(metadata)
		if err != nil {
			e := fmt.Sprintf("could not update the metadata of the directory %v", siaPath.String())
			err = errors.AddContext(err, e)
		}
	}

	// If we are at the root directory then check if any files were found in
	// need of repair or and stuck chunks and trigger the appropriate repair
	// loop. This is only done at the root directory as the repair and stuck
	// loops start at the root directory so there is no point triggering them
	// until the root directory is updated
	if siaPath.IsRoot() {
		if skymodules.NeedsRepair(metadata.AggregateHealth) {
			select {
			case r.staticUploadHeap.repairNeeded <- struct{}{}:
			default:
			}
		}
		if metadata.AggregateNumStuckChunks > 0 {
			select {
			case r.staticUploadHeap.stuckChunkFound <- struct{}{}:
			default:
			}
		}
		// Update the SkylinkManager's pruneTimeThreshold
		//
		// TODO: Probably this isn't the right way to be doing delete.
		r.staticSkylinkManager.callUpdatePruneTimeThreshold(metadata.AggregateLastHealthCheckTime)
	}
	return metadata, nil
}
