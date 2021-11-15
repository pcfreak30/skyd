package renter

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"

	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

var (
	// errNoStuckFiles is a helper to indicate that there are no stuck files in
	// the renter's directory
	errNoStuckFiles = errors.New("no stuck files")

	// errNoStuckChunks is a helper to indicate that there are no stuck chunks
	// in a siafile
	errNoStuckChunks = errors.New("no stuck chunks")
)

// managedAddRandomStuckChunks will try and add up to
// maxRandomStuckChunksAddToHeap random stuck chunks to the upload heap
func (r *Renter) managedAddRandomStuckChunks(hosts map[string]struct{}) ([]skymodules.SiaPath, error) {
	var dirSiaPaths []skymodules.SiaPath
	// Remember number of stuck chunks we are starting with
	prevNumStuckChunks, prevNumRandomStuckChunks := r.staticUploadHeap.managedNumStuckChunks()
	// Check if there is space in the heap. There is space if the number of
	// random stuck chunks has not exceeded maxRandomStuckChunksInHeap and the
	// total number of stuck chunks as not exceeded maxStuckChunksInHeap
	spaceInHeap := prevNumRandomStuckChunks < maxRandomStuckChunksInHeap && prevNumStuckChunks < maxStuckChunksInHeap
	for i := 0; i < maxRandomStuckChunksAddToHeap && spaceInHeap; i++ {
		// Randomly get directory with stuck files
		dirSiaPath, err := r.managedStuckDirectory()
		if err != nil {
			return dirSiaPaths, errors.AddContext(err, "unable to get random stuck directory")
		}

		// Get Random stuck file from directory
		siaPath, err := r.managedStuckFile(dirSiaPath)
		if err != nil {
			return dirSiaPaths, errors.AddContext(err, "unable to get random stuck file in dir "+dirSiaPath.String())
		}

		// Add stuck chunk to upload heap and signal repair needed
		err = r.managedBuildAndPushRandomChunk(siaPath, hosts, targetStuckChunks, r.staticRepairMemoryManager)
		if err != nil {
			return dirSiaPaths, errors.AddContext(err, "unable to push random stuck chunk from '"+siaPath.String()+"' of '"+dirSiaPath.String()+"'")
		}

		// Sanity check that stuck chunks were added
		currentNumStuckChunks, currentNumRandomStuckChunks := r.staticUploadHeap.managedNumStuckChunks()
		if currentNumRandomStuckChunks <= prevNumRandomStuckChunks {
			// If the number of stuck chunks in the heap is not increasing
			// then break out of this loop in order to prevent getting stuck
			// in an infinite loop
			break
		}

		// Remember the directory so bubble can be called on it at the end of
		// the iteration
		dirSiaPaths = append(dirSiaPaths, dirSiaPath)
		r.staticRepairLog.Printf("Added %v stuck chunks from %s", currentNumRandomStuckChunks-prevNumRandomStuckChunks, dirSiaPath.String())
		prevNumStuckChunks = currentNumStuckChunks
		prevNumRandomStuckChunks = currentNumRandomStuckChunks
		spaceInHeap = prevNumRandomStuckChunks < maxRandomStuckChunksInHeap && prevNumStuckChunks < maxStuckChunksInHeap
	}
	return dirSiaPaths, nil
}

// managedAddStuckChunksFromStuckStack will try and add up to
// maxStuckChunksInHeap stuck chunks to the upload heap from the files in the
// stuck stack.
func (r *Renter) managedAddStuckChunksFromStuckStack(hosts map[string]struct{}) ([]skymodules.SiaPath, error) {
	var dirSiaPaths []skymodules.SiaPath
	offline, goodForRenew, _, _ := r.callRenterContractsAndUtilities()
	numStuckChunks, _ := r.staticUploadHeap.managedNumStuckChunks()
	for r.staticStuckStack.managedLen() > 0 && numStuckChunks < maxStuckChunksInHeap {
		// Pop the first file SiaPath
		siaPath := r.staticStuckStack.managedPop()

		// Add stuck chunks to uploadHeap
		err := r.managedAddStuckChunksToHeap(siaPath, hosts, offline, goodForRenew)
		if err != nil && !errors.Contains(err, errNoStuckChunks) {
			return dirSiaPaths, errors.AddContext(err, "unable to add stuck chunks to heap")
		}

		// Since we either added stuck chunks to the heap from this file,
		// there are no stuck chunks left in the file, or all the stuck
		// chunks for the file are already being worked on, remember the
		// directory so we can call bubble on it at the end of this
		// iteration of the stuck loop to update the filesystem
		dirSiaPath, err := siaPath.Dir()
		if err != nil {
			return dirSiaPaths, errors.AddContext(err, "unable to get directory siapath")
		}
		dirSiaPaths = append(dirSiaPaths, dirSiaPath)
		numStuckChunks, _ = r.staticUploadHeap.managedNumStuckChunks()
	}
	return dirSiaPaths, nil
}

// managedAddStuckChunksToHeap tries to add as many stuck chunks from a siafile
// to the upload heap as possible
func (r *Renter) managedAddStuckChunksToHeap(siaPath skymodules.SiaPath, hosts map[string]struct{}, offline, goodForRenew map[string]bool) (err error) {
	// Open File
	sf, err := r.staticFileSystem.OpenSiaFile(siaPath)
	if err != nil {
		return fmt.Errorf("unable to open siafile %v, error: %v", siaPath, err)
	}
	defer func() {
		err = errors.Compose(err, sf.Close())
	}()

	// Check if there are still stuck chunks to repair
	if sf.NumStuckChunks() == 0 {
		return errNoStuckChunks
	}

	// Build unfinished stuck chunks
	var allErrors error
	unfinishedStuckChunks := r.managedBuildUnfinishedChunks(sf, hosts, targetStuckChunks, offline, goodForRenew, r.staticRepairMemoryManager)
	defer func() {
		// Close out remaining file entries
		for _, chunk := range unfinishedStuckChunks {
			allErrors = errors.Compose(allErrors, chunk.Close())
		}
	}()

	// Add up to maxStuckChunksInHeap stuck chunks to the upload heap
	var chunk *unfinishedUploadChunk
	stuckChunksAdded := 0
	for len(unfinishedStuckChunks) > 0 && stuckChunksAdded < maxStuckChunksInHeap {
		chunk = unfinishedStuckChunks[0]
		unfinishedStuckChunks = unfinishedStuckChunks[1:]
		chunk.stuckRepair = true
		chunk.fileRecentlySuccessful = true
		_, pushed, err := r.managedPushChunkForRepair(chunk, chunkTypeLocalChunk)
		if err != nil {
			return errors.Compose(allErrors, err, chunk.Close())
		}
		if !pushed {
			// Stuck chunk unable to be added. Close the file entry of that
			// chunk
			allErrors = errors.Compose(allErrors, chunk.Close())
			continue
		}
		stuckChunksAdded++
	}
	if stuckChunksAdded > 0 {
		r.staticRepairLog.Printf("Added %v stuck chunks from %s to the repair heap", stuckChunksAdded, siaPath.String())
	}

	// check if there are more stuck chunks in the file
	if len(unfinishedStuckChunks) > 0 {
		r.staticStuckStack.managedPush(siaPath)
	}
	return allErrors
}

// managedStuckDirectory randomly finds a directory that contains stuck chunks
func (r *Renter) managedStuckDirectory() (skymodules.SiaPath, error) {
	// Iterating of the renter directory until randomly ending up in a
	// directory, break and return that directory
	siaPath := skymodules.RootSiaPath()
	for {
		select {
		// Check to make sure renter hasn't been shutdown
		case <-r.tg.StopChan():
			return skymodules.SiaPath{}, nil
		default:
		}

		directories, err := r.managedDirList(siaPath)
		if err != nil {
			return skymodules.SiaPath{}, err
		}
		// Sanity check that there is at least the current directory
		if len(directories) == 0 {
			build.Critical("No directories returned from DirList", siaPath.String())
		}

		// Check if we are in an empty Directory. This will be the case before
		// any files have been uploaded so the root directory is empty. Also it
		// could happen if the only file in a directory was stuck and was very
		// recently deleted so the health of the directory has not yet been
		// updated.
		emptyDir := len(directories) == 1 && directories[0].NumFiles == 0
		if emptyDir {
			return siaPath, errNoStuckFiles
		}
		// Check if there are stuck chunks in this directory
		if directories[0].AggregateNumStuckChunks == 0 {
			// Log error if we are not at the root directory
			if !siaPath.IsRoot() {
				r.staticLog.Println("WARN: ended up in directory with no stuck chunks that is not root directory:", siaPath)
			}
			return siaPath, errNoStuckFiles
		}
		// Check if we have reached a directory with only files
		if len(directories) == 1 {
			return siaPath, nil
		}

		// Get random int
		rand := fastrand.Intn(int(directories[0].AggregateNumStuckChunks))
		// Use rand to decide which directory to go into. Work backwards over
		// the slice of directories. Since the first element is the current
		// directory that means that it is the sum of all the files and
		// directories.  We can chose a directory by subtracting the number of
		// stuck chunks a directory has from rand and if rand gets to 0 or less
		// we choose that directory
		for i := len(directories) - 1; i >= 0; i-- {
			// If we are on the last iteration and the directory does have files
			// then return the current directory
			if i == 0 {
				siaPath = directories[0].SiaPath
				return siaPath, nil
			}

			// Skip directories with no stuck chunks
			if directories[i].AggregateNumStuckChunks == uint64(0) {
				continue
			}

			rand = rand - int(directories[i].AggregateNumStuckChunks)
			siaPath = directories[i].SiaPath
			// If rand is less than 0 break out of the loop and continue into
			// that directory
			if rand < 0 {
				break
			}
		}
	}
}

// managedStuckFile finds a weighted random stuck file from a directory based on
// the number of stuck chunks in the stuck files of the directory
func (r *Renter) managedStuckFile(dirSiaPath skymodules.SiaPath) (siapath skymodules.SiaPath, err error) {
	// Grab Aggregate number of stuck chunks from the directory
	//
	// NOTE: using the aggregate number of stuck chunks assumes that the
	// directory and the files within the directory are in sync. This is ok to
	// do as the risks associated with being out of sync are low.
	siaDir, err := r.staticFileSystem.OpenSiaDir(dirSiaPath)
	if err != nil {
		return skymodules.SiaPath{}, errors.AddContext(err, "unable to open siaDir "+dirSiaPath.String())
	}
	defer func() {
		err = errors.Compose(err, siaDir.Close())
	}()
	metadata, err := siaDir.Metadata()
	if err != nil {
		return skymodules.SiaPath{}, err
	}
	aggregateNumStuckChunks := metadata.AggregateNumStuckChunks
	numStuckChunks := metadata.NumStuckChunks
	numFiles := metadata.NumFiles
	if aggregateNumStuckChunks == 0 || numStuckChunks == 0 || numFiles == 0 {
		// If the number of stuck chunks or number of files is zero then this
		// directory should not have been used to find a stuck file. Queue an
		// update on the directories metadata to prevent this from happening
		// again.
		r.staticDirUpdateBatcher.callQueueDirUpdate(dirSiaPath)
		err = fmt.Errorf("managedStuckFile should not have been called on %v, AggregateNumStuckChunks: %v, NumStuckChunks: %v, NumFiles: %v", dirSiaPath.String(), aggregateNumStuckChunks, numStuckChunks, numFiles)
		return skymodules.SiaPath{}, err
	}

	// Use rand to decide which file to select. We can chose a file by
	// subtracting the number of stuck chunks a file has from rand and if rand
	// gets to 0 or less we choose that file
	rand := fastrand.Intn(int(aggregateNumStuckChunks))

	// Read the directory, using ReadDir so we don't read all the siafiles
	// unless we need to
	fileinfos, err := r.staticFileSystem.ReadDir(dirSiaPath)
	if err != nil {
		return skymodules.SiaPath{}, errors.AddContext(err, "unable to open siadir: "+dirSiaPath.String())
	}
	// Iterate over the fileinfos
	for _, fi := range fileinfos {
		// Check for SiaFile
		if fi.IsDir() || filepath.Ext(fi.Name()) != skymodules.SiaFileExtension {
			continue
		}

		// Get SiaPath
		sp, err := dirSiaPath.Join(strings.TrimSuffix(fi.Name(), skymodules.SiaFileExtension))
		if err != nil {
			return skymodules.SiaPath{}, errors.AddContext(err, "unable to join the siapath with the file: "+fi.Name())
		}

		// Open SiaFile, grab the number of stuck chunks and close the file
		f, err := r.staticFileSystem.OpenSiaFile(sp)
		if err != nil {
			return skymodules.SiaPath{}, errors.AddContext(err, "could not open siafileset for "+sp.String())
		}
		numStuckChunks := int(f.NumStuckChunks())
		if err := f.Close(); err != nil {
			return skymodules.SiaPath{}, errors.AddContext(err, "failed to close filenode "+sp.String())
		}

		// Check if stuck
		if numStuckChunks == 0 {
			fmt.Println("no stuck")
			continue
		}
		fmt.Println("stuck")

		// Decrement rand and check if we have decremented fully
		rand = rand - numStuckChunks
		siapath = sp
		if rand < 0 {
			break
		}
	}
	if siapath.IsEmpty() {
		// If no files were selected from the directory than there is a mismatch
		// between the file metadata and the directory metadata. Queue an update
		// on the directory's metadata so this doesn't happen again.
		r.staticDirUpdateBatcher.callQueueDirUpdate(dirSiaPath)
		r.staticDirUpdateBatcher.callFlush() // wait to avoid spinning

		return skymodules.SiaPath{}, errors.New("no files selected from directory " + dirSiaPath.String())
	}
	return siapath, nil
}

// managedSubDirectories reads a directory and returns a slice of all the sub
// directory SiaPaths
func (r *Renter) managedSubDirectories(siaPath skymodules.SiaPath) ([]skymodules.SiaPath, error) {
	// Read directory
	fileinfos, err := r.staticFileSystem.ReadDir(siaPath)
	if err != nil {
		return nil, err
	}
	// Find all sub directory SiaPaths
	folders := make([]skymodules.SiaPath, 0, len(fileinfos))
	for _, fi := range fileinfos {
		if fi.IsDir() {
			subDir, err := siaPath.Join(fi.Name())
			if err != nil {
				return nil, err
			}
			folders = append(folders, subDir)
		}
	}
	return folders, nil
}

// threadedStuckFileLoop works through the renter directory and finds the stuck
// chunks and tries to repair them
func (r *Renter) threadedStuckFileLoop() {
	err := r.tg.Add()
	if err != nil {
		return
	}
	defer r.tg.Done()

	// Loop until the renter has shutdown or until there are no stuck chunks
	for {
		// Return if the renter has shut down.
		select {
		case <-r.tg.StopChan():
			return
		default:
		}

		// Wait until the renter is online to proceed.
		if !r.managedBlockUntilOnline() {
			// The renter shut down before the internet connection was restored.
			r.staticLog.Println("renter shutdown before internet connection")
			return
		}

		// As we add stuck chunks to the upload heap we want to remember the
		// directories they came from so we can call bubble to update the
		// filesystem
		var dirSiaPaths []skymodules.SiaPath

		// Refresh the hosts and workers before adding stuck chunks to the
		// upload heap
		hosts := r.managedRefreshHostsAndWorkers()

		// Try and add stuck chunks from the stuck stack. We try and add these
		// first as they will be from files that previously had a successful
		// stuck chunk repair. The previous success gives us more confidence
		// that it is more likely additional stuck chunks from these files will
		// be successful compared to a random stuck chunk from the renter's
		// directory.
		stuckStackDirSiaPaths, err := r.managedAddStuckChunksFromStuckStack(hosts)
		if err != nil {
			r.staticRepairLog.Println("WARN: error adding stuck chunks to repair heap from files with previously successful stuck repair jobs:", err)
		}
		dirSiaPaths = append(dirSiaPaths, stuckStackDirSiaPaths...)

		// Try add random stuck chunks to upload heap
		randomDirSiaPaths, err := r.managedAddRandomStuckChunks(hosts)
		if err != nil {
			r.staticRepairLog.Println("WARN: error adding random stuck chunks to upload heap:", err)
		}
		dirSiaPaths = append(dirSiaPaths, randomDirSiaPaths...)

		// Check if any stuck chunks were added to the upload heap
		numStuckChunks, _ := r.staticUploadHeap.managedNumStuckChunks()
		if numStuckChunks == 0 {
			// Block until new work is required.
			select {
			case <-r.tg.StopChan():
				// The renter has shut down.
				return
			case <-r.staticUploadHeap.stuckChunkFound:
				// Health Loop found stuck chunk
			case <-r.staticUploadHeap.stuckChunkSuccess:
				// Stuck chunk was successfully repaired.
			}
			continue
		}

		// Signal that a repair is needed because stuck chunks were added to the
		// upload heap
		select {
		case r.staticUploadHeap.repairNeeded <- struct{}{}:
			fmt.Println("stuck chunks")
		default:
		}

		// Sleep until it is time to try and repair another stuck chunk
		rebuildStuckHeapSignal := time.After(repairStuckChunkInterval)
		select {
		case <-r.tg.StopChan():
			// Return if the return has been shutdown
			return
		case <-rebuildStuckHeapSignal:
			// Time to find another random chunk
		case <-r.staticUploadHeap.stuckChunkSuccess:
			// Stuck chunk was successfully repaired.
		}

		// Queue an update to all of the dirs that were visited and then block
		// until all of the updates have completed and have their stats
		// represented in the root aggregate metadata.
		for _, dirSiaPath := range dirSiaPaths {
			r.staticDirUpdateBatcher.callQueueDirUpdate(dirSiaPath)
		}
		r.staticDirUpdateBatcher.callFlush()
	}
}
