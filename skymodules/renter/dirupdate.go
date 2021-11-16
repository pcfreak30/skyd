package renter

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/types"
)

// managedUpdateFilesInDir will update all of the files in the directory to have
// recent health information, then it will update the metadata of the directory
// as well.
func (r *Renter) managedUpdateFilesInDir(siaPath skymodules.SiaPath) error {
	// Update the File metadatas in the directory.
	//
	// NOTE: the result of callRenterContractsAndUtilites already has a caching
	// layer, no need to cache the results here.
	//
	// TODO: Combine this with the next call to save a call to ReadDir
	offlineMap, goodForRenewMap, contracts, used := r.callRenterContractsAndUtilities()
	err := r.managedUpdateFileMetadatasParams(siaPath, offlineMap, goodForRenewMap, contracts, used)
	if err != nil {
		e := fmt.Sprintf("unable to update the file metadatas for directory '%v'", siaPath.String())
		return errors.AddContext(err, e)
	}
	return r.managedUpdateDirMetadata(siaPath)
}

// managedUpdateDirMetadata will update the metadata for just this directory,
// without computing all the healths of the files.
func (r *Renter) managedUpdateDirMetadata(siaPath skymodules.SiaPath) error {
	// Calculate the new metadata values of the directory
	//
	// TODO: Either pass in the stuff from the previous call or combine the
	// calls.
	metadata, err := r.callCalculateDirectoryMetadata(siaPath)
	if err != nil {
		e := fmt.Sprintf("could not calculate the metadata of directory '%v'", siaPath.String())
		return errors.AddContext(err, e)
	}

	// Update directory metadata with the health information.
	siaDir, err := r.staticFileSystem.OpenSiaDir(siaPath)
	if err != nil {
		e := fmt.Sprintf("could not open directory %v", siaPath.String())
		return errors.AddContext(err, e)
	}
	err = siaDir.UpdateMetadata(metadata)
	if err != nil {
		e := fmt.Sprintf("could not update the metadata of the directory %v", siaPath.String())
		err = errors.AddContext(err, e)
	}
	err = errors.Compose(err, siaDir.Close())
	if err != nil {
		return err
	}

	// If we are at the root directory then check if any files were found in
	// need of repair or and stuck chunks and trigger the appropriate repair
	// loop. This is only done at the root directory as the repair and stuck
	// loops start at the root directory so there is no point triggering them
	// until the root directory is updated
	if siaPath.IsRoot() {
		if skymodules.NeedsRepair(metadata.AggregateHealth) {
			//	err = r.managedPushUnexploredDirectory(siaPath)
			//	if err != nil {
			//		return errors.AddContext(err, "failed to push unexplored directory")
			//	}
			fmt.Println("trigger", siaPath)
			select {
			case r.staticUploadHeap.repairNeeded <- struct{}{}:
				fmt.Println("managedUpdateDirMetadata")
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
	return nil
}

// managedUpdateFileMetadatasParams updates the metadata of all siafiles within
// a dir with the provided parameters.  This can be very expensive for large
// directories and should therefore only happen sparingly.
func (r *Renter) managedUpdateFileMetadatasParams(dirSiaPath skymodules.SiaPath, offlineMap map[string]bool, goodForRenewMap map[string]bool, contracts map[string]skymodules.RenterContract, used []types.SiaPublicKey) error {
	// Read the fileinfos from the directory
	fis, err := r.staticFileSystem.ReadDir(dirSiaPath)
	if err != nil {
		return errors.AddContext(err, "managedUpdateFileMetadatas: failed to read dir")
	}

	// Define common variables
	var errs error
	var errMU sync.Mutex
	fileSiaPathChan := make(chan skymodules.SiaPath, numBubbleWorkerThreads)

	// Define the fileWorker
	fileWorker := func() {
		for fileSiaPath := range fileSiaPathChan {
			err := func() error {
				sf, err := r.staticFileSystem.OpenSiaFile(fileSiaPath)
				if err != nil {
					return err
				}
				err = sf.UpdateMetadata(offlineMap, goodForRenewMap, contracts, used)
				return errors.Compose(err, sf.Close())
			}()
			errMU.Lock()
			errs = errors.Compose(errs, err)
			errMU.Unlock()
		}
	}

	// Launch file workers
	var wg sync.WaitGroup
	for i := 0; i < numBubbleWorkerThreads; i++ {
		wg.Add(1)
		go func() {
			fileWorker()
			wg.Done()
		}()
	}

	// Update the file metadatas
	for _, fi := range fis {
		ext := filepath.Ext(fi.Name())
		if ext != skymodules.SiaFileExtension {
			continue
		}
		fName := strings.TrimSuffix(fi.Name(), skymodules.SiaFileExtension)
		fileSiaPath, err := dirSiaPath.Join(fName)
		if err != nil {
			r.staticLog.Println("managedUpdateFileMetadatas: unable to join siapath with dirpath", err)
			continue
		}
		// Send fileSiaPath to the file workers
		select {
		case fileSiaPathChan <- fileSiaPath:
		case <-r.tg.StopChan():
			close(fileSiaPathChan)
			wg.Wait()
			return errors.AddContext(errs, "renter shutdown")
		}
	}

	// Close the chan and wait for the workers to finish
	close(fileSiaPathChan)
	wg.Wait()
	return errs
}
