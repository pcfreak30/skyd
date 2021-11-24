package filesystem

import (
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem/siadir"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem/siafile"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
)

type (
	// DirNode is a node which references a SiaDir.
	DirNode struct {
		node

		directories map[string]*DirNode
		files       map[string]*FileNode

		// lazySiaDir is the SiaDir of the DirNode. 'lazy' means that it will
		// only be loaded on demand and destroyed as soon as the length of
		// 'threads' reaches 0.
		lazySiaDir **siadir.SiaDir
	}
)

// Close calls close on the DirNode and also removes the dNode from its parent
// if it's no longer being used and if it doesn't have any children which are
// currently in use. This happens iteratively for all parent as long as
// removing a child resulted in them not having any children left.
func (n *DirNode) Close() error {
	// If a parent exists, we need to lock it while closing a child.
	parent := n.node.managedLockWithParent()

	// call private close method.
	n.closeDirNode()

	// Remove node from parent if there are no more children after this close.
	removeDir := len(n.threads) == 0 && len(n.directories) == 0 && len(n.files) == 0
	if parent != nil && removeDir {
		parent.removeDir(n)
	}

	// Unlock child and parent.
	n.mu.Unlock()
	if parent != nil {
		parent.mu.Unlock()
		// Check if the parent needs to be removed from its parent too.
		parent.managedTryRemoveFromParentsIteratively()
	}

	return nil
}

// Delete is a wrapper for SiaDir.Delete.
func (n *DirNode) Delete() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	sd, err := n.siaDir()
	if err != nil {
		return err
	}
	return sd.Delete()
}

// Deleted is a wrapper for SiaDir.Deleted.
func (n *DirNode) Deleted() (bool, error) {
	n.mu.Lock()
	sd, err := n.siaDir()
	n.mu.Unlock()
	if err != nil {
		return false, err
	}
	return sd.Deleted(), nil
}

// Dir will return a child dir of this directory if it exists.
func (n *DirNode) Dir(name string) (*DirNode, error) {
	n.mu.Lock()
	node, err := n.openDir(name)
	n.mu.Unlock()
	return node, errors.AddContext(err, "unable to open child directory")
}

// DirReader is a wrapper for SiaDir.DirReader.
func (n *DirNode) DirReader() (*siadir.DirReader, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	sd, err := n.siaDir()
	if err != nil {
		return nil, err
	}
	return sd.DirReader()
}

// File will return a child file of this directory if it exists.
func (n *DirNode) File(name string) (*FileNode, error) {
	n.mu.Lock()
	node, err := n.openFile(name)
	n.mu.Unlock()
	return node, errors.AddContext(err, "unable to open child file")
}

// Metadata is a wrapper for SiaDir.Metadata.
func (n *DirNode) Metadata() (siadir.Metadata, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	sd, err := n.siaDir()
	if os.IsNotExist(err) {
		return siadir.Metadata{}, ErrNotExist
	}
	if err != nil {
		return siadir.Metadata{}, err
	}
	return sd.Metadata(), nil
}

// Path is a wrapper for SiaDir.Path.
func (n *DirNode) Path() (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	sd, err := n.siaDir()
	if err != nil {
		return "", err
	}
	return sd.Path(), nil
}

// UpdateLastHealthCheckTime is a wrapper for SiaDir.UpdateLastHealthCheckTime.
func (n *DirNode) UpdateLastHealthCheckTime(aggregateLastHealthCheckTime, lastHealthCheckTime time.Time) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	sd, err := n.siaDir()
	if err != nil {
		return err
	}
	return sd.UpdateLastHealthCheckTime(aggregateLastHealthCheckTime, lastHealthCheckTime)
}

// UpdateMetadata is a wrapper for SiaDir.UpdateMetadata.
func (n *DirNode) UpdateMetadata(md siadir.Metadata) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	sd, err := n.siaDir()
	if err != nil {
		return err
	}
	return sd.UpdateMetadata(md)
}

// managedList returns the files and dirs within the SiaDir specified by siaPath.
// offlineMap, goodForRenewMap and contractMap don't need to be provided if
// 'cached' is set to 'true'.
func (n *DirNode) managedList(fsRoot string, recursive, cached bool, offlineMap map[string]bool, goodForRenewMap map[string]bool, contractsMap map[string]skymodules.RenterContract, flf skymodules.FileListFunc, dlf skymodules.DirListFunc) error {
	// Prepare a pool of workers.
	numThreads := 40
	dirLoadChan := make(chan *DirNode, numThreads)
	fileLoadChan := make(chan func() (*FileNode, error), numThreads)
	dirWorker := func() {
		for sd := range dirLoadChan {
			var di skymodules.DirectoryInfo
			var err error
			if sd.managedAbsPath() == fsRoot {
				di, err = sd.managedInfo(skymodules.RootSiaPath())
			} else {
				di, err = sd.managedInfo(nodeSiaPath(fsRoot, &sd.node))
			}
			sd.Close()
			if errors.Contains(err, ErrNotExist) {
				continue
			}
			if err != nil {
				n.staticLog.Debugf("Failed to get DirectoryInfo of '%v': %v", sd.managedAbsPath(), err)
				continue
			}
			dlf(di)
		}
	}
	fileWorker := func() {
		for load := range fileLoadChan {
			sf, err := load()
			if err != nil {
				n.staticLog.Debugf("Failed to load file: %v", err)
				continue
			}
			var fi skymodules.FileInfo
			if cached {
				fi, err = sf.staticCachedInfo(nodeSiaPath(fsRoot, &sf.node))
			} else {
				fi, err = sf.managedFileInfo(nodeSiaPath(fsRoot, &sf.node), offlineMap, goodForRenewMap, contractsMap)
			}
			if errors.Contains(err, ErrNotExist) {
				continue
			}
			if err != nil {
				n.staticLog.Debugf("Failed to get FileInfo of '%v': %v", sf.managedAbsPath(), err)
				continue
			}
			flf(fi)
		}
	}
	// Spin the workers up.
	var wg sync.WaitGroup
	for i := 0; i < numThreads/2; i++ {
		wg.Add(1)
		go func() {
			dirWorker()
			wg.Done()
		}()
		wg.Add(1)
		go func() {
			fileWorker()
			wg.Done()
		}()
	}
	err := n.managedRecursiveList(recursive, cached, fileLoadChan, dirLoadChan)
	// Signal the workers that we are done adding work and wait for them to
	// finish any pending work.
	close(dirLoadChan)
	close(fileLoadChan)
	wg.Wait()
	return err
}

// managedRecursiveList returns the files and dirs within the SiaDir.
func (n *DirNode) managedRecursiveList(recursive, cached bool, fileLoadChan chan func() (*FileNode, error), dirLoadChan chan *DirNode) error {
	// Get DirectoryInfo of dir itself.
	dirLoadChan <- n.managedCopy()
	// Read dir.
	fis, err := ioutil.ReadDir(n.managedAbsPath())
	if err != nil {
		return err
	}
	// Separate dirs and files.
	var dirNames, fileNames []string
	for _, info := range fis {
		// Skip non-siafiles and non-dirs.
		if !info.IsDir() && filepath.Ext(info.Name()) != skymodules.SiaFileExtension {
			continue
		}
		if info.IsDir() {
			dirNames = append(dirNames, info.Name())
			continue
		}
		fileNames = append(fileNames, strings.TrimSuffix(info.Name(), skymodules.SiaFileExtension))
	}
	// Handle dirs first.
	for _, dirName := range dirNames {
		// Open the dir.
		n.mu.Lock()
		dir, err := n.openDir(dirName)
		n.mu.Unlock()
		if errors.Contains(err, ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if recursive {
			// Call managedList on the child if 'recursive' was specified.
			err = dir.managedRecursiveList(recursive, cached, fileLoadChan, dirLoadChan)
		} else {
			// If not recursive, hand a copy to the worker. It will handle closing it.
			dirLoadChan <- dir.managedCopy()
		}
		if err != nil {
			return err
		}
		err = dir.Close()
		if err != nil {
			return err
		}
		continue
	}
	// Check if there are any files to handle.
	if len(fileNames) == 0 {
		return nil
	}
	// Handle files by sending a method to load them to the workers. Wee add all
	// of them to the waitgroup to be able to tell when to release the mutex.
	n.mu.Lock()
	defer n.mu.Unlock()
	var wg sync.WaitGroup
	wg.Add(len(fileNames))
	for i := range fileNames {
		fileName := fileNames[i]
		f := func() (*FileNode, error) {
			file, err := n.readonlyOpenFile(fileName)
			wg.Done()
			if err != nil {
				return nil, err
			}
			return file, nil // no need to close it since it was created using readonlyOpenFile.
		}
		fileLoadChan <- f
	}
	// Wait for the workers to finish all calls to `openFile` before releasing the
	// lock.
	wg.Wait()
	return nil
}

// close calls the common close method.
func (n *DirNode) closeDirNode() {
	n.node.closeNode()
	// If no more threads use the directory we delete the SiaDir to invalidate
	// the cache.
	if len(n.threads) == 0 {
		*n.lazySiaDir = nil
	}
}

// managedNewSiaFileFromReader will read a siafile and its chunks from the given
// reader and add it to the directory. This will always load the file from the
// given reader.
func (n *DirNode) managedNewSiaFileFromExisting(sf *siafile.SiaFile, chunks siafile.Chunks) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	// Get the initial path of the siafile.
	path := sf.SiaFilePath()
	// Check if the path is taken.
	currentPath, exists := n.uniquePrefix(path, sf.UID())
	if exists {
		return nil // file already exists
	}
	// Either the file doesn't exist yet or we found a filename that doesn't
	// exist. Update the UID for safety and set the correct siafilepath.
	sf.UpdateUniqueID()
	sf.SetSiaFilePath(currentPath)
	// Save the file to disk.
	if err := sf.SaveWithChunks(chunks); err != nil {
		return err
	}
	// Add the node to the dir.
	fileName := strings.TrimSuffix(filepath.Base(currentPath), skymodules.SiaFileExtension)
	fn := &FileNode{
		node:    newNode(n, currentPath, fileName, 0, n.staticWal, n.staticLog),
		SiaFile: sf,
	}
	n.files[fileName] = fn
	return nil
}

// managedNewSiaFileFromLegacyData adds an existing SiaFile to the filesystem
// using the provided siafile.FileData object.
func (n *DirNode) managedNewSiaFileFromLegacyData(fileName string, fd siafile.FileData) (*FileNode, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	// Check if the path is taken.
	path := filepath.Join(n.absPath(), fileName+skymodules.SiaFileExtension)
	if _, err := os.Stat(filepath.Join(path)); !os.IsNotExist(err) {
		return nil, ErrExists
	}
	// Check if the file or folder exists already.
	key := strings.TrimSuffix(fileName, skymodules.SiaFileExtension)
	if exists := n.childExists(key); exists {
		return nil, ErrExists
	}
	// Otherwise create the file.
	sf, err := siafile.NewFromLegacyData(fd, path, n.staticWal)
	if err != nil {
		return nil, err
	}
	// Add it to the node.
	fn := &FileNode{
		node:    newNode(n, path, key, 0, n.staticWal, n.staticLog),
		SiaFile: sf,
	}
	n.files[key] = fn
	return fn.managedCopy(), nil
}

// uniquePrefix returns a new path for the siafile with the given path
// and uid by adding a suffix to the current path and incrementing it as long as
// the resulting path is already taken.
func (n *DirNode) uniquePrefix(path string, uid siafile.SiafileUID) (string, bool) {
	suffix := 0
	currentPath := path
	for {
		fileName := strings.TrimSuffix(filepath.Base(currentPath), skymodules.SiaFileExtension)
		oldFile, err := n.openFile(fileName)
		exists := err == nil
		if exists && oldFile.UID() == uid {
			oldFile.managedClose()
			return "", true // skip file since it already exists
		} else if exists {
			// Exists: update currentPath and fileName
			suffix++
			currentPath = strings.TrimSuffix(path, skymodules.SiaFileExtension)
			currentPath = fmt.Sprintf("%v_%v%v", currentPath, suffix, skymodules.SiaFileExtension)
			fileName = filepath.Base(currentPath)
			oldFile.managedClose()
			continue
		}
		break
	}
	return currentPath, false
}

// siaDir is a wrapper for the lazySiaDir field.
func (n *DirNode) siaDir() (*siadir.SiaDir, error) {
	if *n.lazySiaDir != nil {
		return *n.lazySiaDir, nil
	}
	sd, err := siadir.LoadSiaDir(n.absPath(), modules.ProdDependencies)
	if os.IsNotExist(err) {
		return nil, ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	*n.lazySiaDir = sd
	return sd, nil
}

// managedTryRemoveFromParentsIteratively will remove the DirNode from its
// parent if it doesn't have any more files or directories as children. It will
// do so iteratively until it reaches an acestor with children.
func (n *DirNode) managedTryRemoveFromParentsIteratively() {
	n.mu.Lock()
	child := n
	parent := n.parent
	n.mu.Unlock()

	// Iteratively try to remove from parents as long as children got
	// removed.
	removeDir := true
	for removeDir && parent != nil {
		parent.mu.Lock()
		child.mu.Lock()
		removeDir = len(child.threads)+len(child.directories)+len(child.files) == 0
		if removeDir {
			parent.removeDir(child)
		}
		child.mu.Unlock()
		child, parent = parent, parent.parent
		child.mu.Unlock() // parent became child
	}
}

// managedDelete recursively deletes a dNode from disk.
func (n *DirNode) managedDelete() error {
	// If there is a parent lock it.
	parent := n.managedLockWithParent()
	if parent != nil {
		defer parent.mu.Unlock()
	}
	defer n.mu.Unlock()
	// Get contents of dir.
	dirsToLock := n.childDirs()
	var filesToDelete []*FileNode
	var lockedNodes []*node
	for _, file := range n.childFiles() {
		file.node.mu.Lock()
		file.SiaFile.Lock()
		lockedNodes = append(lockedNodes, &file.node)
		filesToDelete = append(filesToDelete, file)
	}
	// Unlock all locked nodes regardless of errors.
	defer func() {
		for _, file := range filesToDelete {
			file.Unlock()
		}
		for _, node := range lockedNodes {
			node.mu.Unlock()
		}
	}()
	// Lock dir and all open children. Remember in which order we acquired the
	// locks.
	for len(dirsToLock) > 0 {
		// Get next dir.
		d := dirsToLock[0]
		dirsToLock = dirsToLock[1:]
		// Lock the dir.
		d.mu.Lock()
		lockedNodes = append(lockedNodes, &d.node)
		// Remember the open files.
		for _, file := range d.files {
			file.node.mu.Lock()
			file.SiaFile.Lock()
			lockedNodes = append(lockedNodes, &file.node)
			filesToDelete = append(filesToDelete, file)
		}
		// Add the open dirs to dirsToLock.
		dirsToLock = append(dirsToLock, d.childDirs()...)
	}
	// Delete the dir.
	dir, err := n.siaDir()
	if err != nil {
		return err
	}
	err = dir.Delete()
	if err != nil {
		return err
	}
	// Remove the dir from the parent if it exists.
	if n.parent != nil {
		n.parent.removeDir(n)
	}
	// Delete all the open files in memory.
	for _, file := range filesToDelete {
		file.UnmanagedSetDeleted(true)
	}
	return nil
}

// managedDeleteFile deletes the file with the given name from the directory.
func (n *DirNode) managedDeleteFile(fileName string) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Check if the file is open in memory. If it is delete it.
	sf, exists := n.files[fileName]
	if exists {
		err := sf.managedDelete()
		if err != nil {
			return err
		}
		n.removeFile(sf)
		return nil
	}

	// Check whether the file is actually a directory.
	_, exists = n.directories[fileName]
	if exists {
		return ErrDeleteFileIsDir
	}

	// Check if the on-disk version is a file. This check is needed because
	// os.Remove will delete an empty directory without returning any error, if
	// the user has a directory name 'dir.sia' it could cause an edge case.
	//
	// There is a test for this edge case in the integration test called
	// 'TestUploadAfterDelete'.
	sysPath := filepath.Join(n.absPath(), fileName+skymodules.SiaFileExtension)
	info, err := os.Stat(sysPath)
	if os.IsNotExist(err) {
		return errors.Extend(err, ErrNotExist)
	} else if err != nil {
		return errors.AddContext(err, "unable to find file")
	}
	if info.IsDir() {
		return ErrDeleteFileIsDir
	}

	// Otherwise simply delete the file.
	err = os.Remove(sysPath)
	return errors.AddContext(err, "unable to delete file")
}

// managedInfo builds and returns the DirectoryInfo of a SiaDir.
func (n *DirNode) managedInfo(siaPath skymodules.SiaPath) (skymodules.DirectoryInfo, error) {
	// Grab the siadir metadata
	metadata, err := n.Metadata()
	if err != nil {
		return skymodules.DirectoryInfo{}, err
	}
	aggregateMaxHealth := math.Max(metadata.AggregateHealth, metadata.AggregateStuckHealth)
	maxHealth := math.Max(metadata.Health, metadata.StuckHealth)
	return skymodules.DirectoryInfo{
		// Aggregate Fields
		AggregateHealth:              metadata.AggregateHealth,
		AggregateLastHealthCheckTime: metadata.AggregateLastHealthCheckTime,
		AggregateMaxHealth:           aggregateMaxHealth,
		AggregateMaxHealthPercentage: skymodules.HealthPercentage(aggregateMaxHealth),
		AggregateMinRedundancy:       metadata.AggregateMinRedundancy,
		AggregateMostRecentModTime:   metadata.AggregateModTime,
		AggregateNumFiles:            metadata.AggregateNumFiles,
		AggregateNumLostFiles:        metadata.AggregateNumLostFiles,
		AggregateNumStuckChunks:      metadata.AggregateNumStuckChunks,
		AggregateNumSubDirs:          metadata.AggregateNumSubDirs,
		AggregateNumUnfinishedFiles:  metadata.AggregateNumUnfinishedFiles,
		AggregateRepairSize:          metadata.AggregateRepairSize,
		AggregateSize:                metadata.AggregateSize,
		AggregateStuckHealth:         metadata.AggregateStuckHealth,
		AggregateStuckSize:           metadata.AggregateStuckSize,

		// Skynet Fields
		AggregateSkynetFiles: metadata.AggregateSkynetFiles,
		AggregateSkynetSize:  metadata.AggregateSkynetSize,

		// SiaDir Fields
		Health:              metadata.Health,
		LastHealthCheckTime: metadata.LastHealthCheckTime,
		MaxHealth:           maxHealth,
		MaxHealthPercentage: skymodules.HealthPercentage(maxHealth),
		MinRedundancy:       metadata.MinRedundancy,
		DirMode:             metadata.Mode,
		MostRecentModTime:   metadata.ModTime,
		NumFiles:            metadata.NumFiles,
		NumLostFiles:        metadata.NumLostFiles,
		NumStuckChunks:      metadata.NumStuckChunks,
		NumSubDirs:          metadata.NumSubDirs,
		NumUnfinishedFiles:  metadata.NumUnfinishedFiles,
		RepairSize:          metadata.RepairSize,
		DirSize:             metadata.Size,
		StuckHealth:         metadata.StuckHealth,
		StuckSize:           metadata.StuckSize,
		SiaPath:             siaPath,
		UID:                 n.staticUID,

		// Skynet Fields
		SkynetFiles: metadata.SkynetFiles,
		SkynetSize:  metadata.SkynetSize,
	}, nil
}

// childDirs is a convenience method to return the directories field of a DNode
// as a slice.
func (n *DirNode) childDirs() []*DirNode {
	dirs := make([]*DirNode, 0, len(n.directories))
	for _, dir := range n.directories {
		dirs = append(dirs, dir)
	}
	return dirs
}

// managedExists returns 'true' if a file or folder with the given name already
// exists within the dir.
func (n *DirNode) childExists(name string) bool {
	// Check the ones in memory first.
	if _, exists := n.files[name]; exists {
		return true
	}
	if _, exists := n.directories[name]; exists {
		return true
	}
	// Check that no dir or file exists on disk.
	_, errFile := os.Stat(filepath.Join(n.absPath(), name))
	_, errDir := os.Stat(filepath.Join(n.absPath(), name+skymodules.SiaFileExtension))
	return !os.IsNotExist(errFile) || !os.IsNotExist(errDir)
}

// childFiles is a convenience method to return the files field of a DNode as a
// slice.
func (n *DirNode) childFiles() []*FileNode {
	files := make([]*FileNode, 0, len(n.files))
	for _, file := range n.files {
		files = append(files, file)
	}
	return files
}

// managedNewSiaFile creates a new SiaFile in the directory.
func (n *DirNode) managedNewSiaFile(fileName string, source string, ec skymodules.ErasureCoder, mk crypto.CipherKey, fileSize uint64, fileMode os.FileMode) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	// Make sure we don't have a file or folder with that name already.
	if exists := n.childExists(fileName); exists {
		return ErrExists
	}
	_, err := siafile.New(filepath.Join(n.absPath(), fileName+skymodules.SiaFileExtension), source, n.staticWal, ec, mk, fileSize, fileMode)
	return errors.AddContext(err, "NewSiaFile: failed to create file")
}

// managedNewSiaDir creates the SiaDir with the given dirName as its child. We
// try to create the SiaDir if it exists in memory but not on disk, as it may
// have just been deleted. We also do not return an error if the SiaDir exists
// in memory and on disk already, which may be due to a race.
func (n *DirNode) managedNewSiaDir(dirName string, rootPath string, mode os.FileMode) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	// Check if a file already exists with that name.
	if _, exists := n.files[dirName]; exists {
		return ErrExists
	}
	// Check that no dir or file exists on disk.
	_, err := os.Stat(filepath.Join(n.absPath(), dirName+skymodules.SiaFileExtension))
	if !os.IsNotExist(err) {
		return ErrExists
	}
	_, err = siadir.New(filepath.Join(n.absPath(), dirName), rootPath, mode)
	if errors.Contains(err, os.ErrExist) {
		return nil
	}
	return err
}

// managedOpenFile opens a SiaFile and adds it and all of its parents to the
// filesystem tree.
func (n *DirNode) managedOpenFile(fileName string) (*FileNode, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.openFile(fileName)
}

// openFile is like readonlyOpenFile but adds the file to the parent.
func (n *DirNode) openFile(fileName string) (*FileNode, error) {
	fn, err := n.readonlyOpenFile(fileName)
	if err != nil {
		return nil, err
	}
	n.files[fileName] = fn
	return fn.managedCopy(), nil
}

// readonlyOpenFile opens a SiaFile but doesn't add it to the parent. That's
// useful for opening nodes which are short-lived and don't need to be closed.
func (n *DirNode) readonlyOpenFile(fileName string) (*FileNode, error) {
	fn, exists := n.files[fileName]
	if exists && fn.Deleted() {
		return nil, ErrNotExist // don't return a deleted file
	}
	if exists {
		return fn, nil
	}
	// Load file from disk.
	filePath := filepath.Join(n.absPath(), fileName+skymodules.SiaFileExtension)
	sf, err := siafile.LoadSiaFile(filePath, n.staticWal)
	if errors.Contains(err, siafile.ErrUnknownPath) || os.IsNotExist(err) {
		return nil, ErrNotExist
	}
	if err != nil {
		return nil, errors.AddContext(err, fmt.Sprintf("failed to load SiaFile '%v' from disk", filePath))
	}
	fn = &FileNode{
		node:    newNode(n, filePath, fileName, 0, n.staticWal, n.staticLog),
		SiaFile: sf,
	}
	// Clone the node, give it a new UID and return it.
	return fn, nil
}

// openDir opens the dir with the specified name within the current dir.
func (n *DirNode) openDir(dirName string) (*DirNode, error) {
	// Check if dir was already loaded. Then just copy it.
	dir, exists := n.directories[dirName]
	if exists {
		return dir.managedCopy(), nil
	}
	// Load the dir.
	dirPath := filepath.Join(n.absPath(), dirName)
	_, err := os.Stat(dirPath)
	if os.IsNotExist(err) {
		return nil, ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	// Make sure the metadata exists too.
	dirMDPath := filepath.Join(dirPath, skymodules.SiaDirExtension)
	_, err = os.Stat(dirMDPath)
	if os.IsNotExist(err) {
		return nil, ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	// Add the dir to the opened dirs.
	dir = &DirNode{
		node:        newNode(n, dirPath, dirName, 0, n.staticWal, n.staticLog),
		directories: make(map[string]*DirNode),
		files:       make(map[string]*FileNode),
		lazySiaDir:  new(*siadir.SiaDir),
	}
	n.directories[*dir.name] = dir
	return dir.managedCopy(), nil
}

// copyDirNode copies the node, adds a new thread to the threads map and returns
// the new instance.
func (n *DirNode) copyDirNode() *DirNode {
	// Copy the dNode and change the uid to a unique one.
	newNode := *n
	newNode.threadUID = newThreadUID()
	newNode.threads[newNode.threadUID] = struct{}{}
	return &newNode
}

// managedCopy copies the node, adds a new thread to the threads map and returns the
// new instance.
func (n *DirNode) managedCopy() *DirNode {
	// Copy the dNode and change the uid to a unique one.
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.copyDirNode()
}

// managedOpenDir opens a SiaDir.
func (n *DirNode) managedOpenDir(path string) (_ *DirNode, err error) {
	// Get the name of the next sub directory.
	pathList := strings.Split(path, string(filepath.Separator))
	n.mu.Lock()
	subNode, err := n.openDir(pathList[0])
	n.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// If path is empty we are done.
	pathList = pathList[1:]
	if len(pathList) == 0 {
		return subNode, nil
	}
	// Otherwise open the next dir.
	defer func() {
		err = errors.Compose(err, subNode.Close())
	}()
	return subNode.managedOpenDir(filepath.Join(pathList...))
}

// managedRemoveDir removes a dir from a dNode.
// NOTE: child.mu needs to be locked
func (n *DirNode) removeDir(child *DirNode) {
	// Remove the child node.
	currentChild, exists := n.directories[*child.name]
	if !exists || child.staticUID != currentChild.staticUID {
		return // nothing to do
	}
	delete(n.directories, *child.name)
}

// removeFile removes a child from a dNode.
// NOTE: child.mu needs to be locked
func (n *DirNode) removeFile(child *FileNode) {
	// Remove the child node.
	currentChild, exists := n.files[*child.name]
	if !exists || child.SiaFile != currentChild.SiaFile {
		return // Nothing to do
	}
	delete(n.files, *child.name)
}

// managedRename renames the fNode's underlying file.
func (n *DirNode) managedRename(newName string, oldParent, newParent *DirNode) error {
	// Iteratively remove oldParent after Rename is done.
	defer oldParent.managedTryRemoveFromParentsIteratively()

	// Lock the parents. If they are the same, only lock one.
	oldParent.mu.Lock()
	defer oldParent.mu.Unlock()
	if oldParent.staticUID != newParent.staticUID {
		newParent.mu.Lock()
		defer newParent.mu.Unlock()
	}
	// Check that newParent doesn't have a dir or file with that name already.
	if exists := newParent.childExists(newName); exists {
		return ErrExists
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	dirsToLock := n.childDirs()
	var dirsToRename []*DirNode
	var filesToRename []*FileNode
	var lockedNodes []*node
	for _, file := range n.childFiles() {
		file.node.mu.Lock()
		file.SiaFile.Lock()
		lockedNodes = append(lockedNodes, &file.node)
		filesToRename = append(filesToRename, file)
	}
	// Unlock all locked nodes regardless of errors.
	defer func() {
		for _, file := range filesToRename {
			file.Unlock()
		}
		for _, node := range lockedNodes {
			node.mu.Unlock()
		}
	}()
	// Lock dir and all open children. Remember in which order we acquired the
	// locks.
	for len(dirsToLock) > 0 {
		// Get next dir.
		d := dirsToLock[0]
		dirsToLock = dirsToLock[1:]
		// Lock the dir.
		d.mu.Lock()
		lockedNodes = append(lockedNodes, &d.node)
		dirsToRename = append(dirsToRename, d)
		// Lock the open files.
		for _, file := range d.files {
			file.node.mu.Lock()
			file.SiaFile.Lock()
			lockedNodes = append(lockedNodes, &file.node)
			filesToRename = append(filesToRename, file)
		}
		// Add the open dirs to dirsToLock.
		dirsToLock = append(dirsToLock, d.childDirs()...)
	}
	newBase := filepath.Join(newParent.absPath(), newName)
	// Rename the dir.
	dir, err := n.siaDir()
	if err != nil {
		return err
	}
	err = dir.Rename(newBase)
	if os.IsExist(err) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	// Remove dir from old parent and add it to new parent.
	oldParent.removeDir(n)
	// Update parent and name.
	n.parent = newParent
	*n.name = newName
	*n.path = newBase
	// Add dir to new parent.
	n.parent.directories[*n.name] = n
	// Rename all locked nodes in memory.
	for _, node := range lockedNodes {
		*node.path = filepath.Join(*node.parent.path, *node.name)
	}
	// Rename all files in memory.
	for _, file := range filesToRename {
		*file.path = *file.path + skymodules.SiaFileExtension
		file.UnmanagedSetSiaFilePath(*file.path)
	}
	// Rename all dirs in memory.
	for _, dir := range dirsToRename {
		if *dir.lazySiaDir == nil {
			continue // dir isn't loaded
		}
		err = (*dir.lazySiaDir).SetPath(*dir.path)
		if err != nil {
			return errors.AddContext(err, fmt.Sprintf("unable to set path for %v", *dir.path))
		}
	}
	return err
}
