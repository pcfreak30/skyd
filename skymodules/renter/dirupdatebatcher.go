package renter

// dirupdatebatcher.go contains the logic for the dirupdatebatcher, which is a
// batching tool that improves the performance of updating a large numer of
// directories in the same time period by removing redundant calls to the same
// directory, and by removing redundant update calls that would happen on shared
// parent directories.
//
// NOTE: The dir update batcher is already fairly optimized. There are two known
// places to improve performance, but both contain a fair amount of programming
// overhead and could potentially make performance worse if implemented
// incorrectly. The first is that batches do not deduplicate between eachother.
// If flush() is called on one batch before the previous batch is finished, the
// two batches may perform redundant work. This can be deduplicated if the
// batches have pointers to eachother, however for garbage collection purposes
// you need to make sure to clean up the pointers later. The second thing is
// that the update calls are all made together in rapid succession, which could
// hog the CPU and consume a ton of disk IOPs all at once. We try to manage this
// by only batching together 30 seconds at a time. You could try to slow down
// the update calls so that the CPU is under less stress, but this may block
// parts of the repair loop, and may also block user calls. It is unlikely that
// either of these optimizations need to be pursued, but is something to keep in
// mind if the batcher seems to be causing issues in production.

import (
	"container/list"
	"fmt"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

var (
	// maxTimeBetweenBatchExectutions defines the amount of time that a batch
	// will wait before executing the queue of directories to batch. The testing
	// value is really low at 50ms to maximize the opportunity that threads
	// queue things across multiple batches (which should be safe, but
	// potentially has edge cases).
	//
	// The production value is also relatively low at 30 seconds was set a lot
	// higher (15 minutes), but we saw in production that this would result in
	// large amounts of files being batched together all at once, causing the
	// flush to take over a minute.
	maxTimeBetweenBatchExecutions = build.Select(build.Var{
		Dev:      10 * time.Second,
		Standard: 30 * time.Second,
		Testing:  50 * time.Millisecond,
	}).(time.Duration)
)

type (
	// dirUpdateBatch defines a batch of updates that should be run at the
	// same time. Performing an update on a file requires doing an update on its
	// directory and all parent directories up to the root directory. By doing
	// the updates as a batch, we can reduce the total amount of work required
	// to complete the update.
	//
	// NOTE: the health update batch depends on the mutex of the
	// dirUpdateBatcher for thread safety.
	dirUpdateBatch struct {
		// batchSet is an array of maps which contain the directories that need
		// to be updated. Each element of the array corresponds to a directory
		// of a different depth. The first element of the array just contains
		// the root directory. The second element is a map that contains only
		// direct subdirs of the root. The third element is a map that contains
		// directories which live directly in subdirs of the root, and so on.
		//
		// When performing the update on the set, the lowest level dirs are all
		// executed at once, and then their parents are added to the batchSet,
		// then the next level of dirs are executed all together, and so on.
		// This ensures that each directory is only updated a single time per
		// batch, even if it appears as a parent in dozens of directories in the
		// batchSet.
		batchSet []map[skymodules.SiaPath]struct{}

		// completeChan is a channel that gets closed when the whole batch has
		// successfully executed. It will not be closed until priorCompleteChan
		// has been closed. priorCompleteChan is the channel owned by the
		// previous batch. This ensures that when the channel is closed, all
		// updates are certain to have completed, even if those updates were
		// submitted to previous batches.
		completeChan      chan struct{}
		priorCompleteChan <-chan struct{}

		// Contains a renter, and also has some dependency injection logic.
		dirUpdateBatchDeps
	}

	// dirUpdateBatcher receives requests to update the health of a file or
	// directory and adds them to a batch. This struct manages concurrency and
	// safety between different batches.
	dirUpdateBatcher struct {
		// nextBatch defines the next batch that will perform a health update.
		nextBatch *dirUpdateBatch

		// Utilities
		closed          bool // callQueueDirUpdate is a no-op after shutdown
		staticFlushChan chan struct{}
		mu              sync.Mutex
		staticRenter    *Renter
	}
)

// managedExecute will execute a batch of updates.
func (batch *dirUpdateBatch) managedExecute() {
	renter := batch.dirUpdateBatchDeps.renter
	start := time.Now()
	dirs := 0
	defer func() {
		if dirs > 0 {
			str := fmt.Sprintf("dirupdatebatch completed %v dirs in %v", dirs, time.Since(start))
			renter.staticLog.Println(str, "dirupdatebatcher")
		}
	}()

	// iterate through the batchSet backwards.
	for i := len(batch.batchSet) - 1; i >= 0; i-- {
		for dirPath := range batch.batchSet[i] {
			// Update the directory metadata. Note: we don't do any updates on
			// the file healths themselves, we just use the file metadata.
			err := batch.managedUpdateDirMetadata(dirPath) // passes through to the renter except during testing
			if err != nil {
				str := fmt.Sprintf("error updating directory %v in dirUpdateBatch.execute: %v", dirPath, err)
				renter.staticLog.Println(str, "health-verbose", "dirupdatebatcher", "error")
				continue
			}
			dirs++ // Increment after the error.

			// Add the parent.
			if !dirPath.IsRoot() {
				parent, err := dirPath.Dir()
				if err != nil {
					renter.staticLog.Critical("should not be getting an error when grabbing the dir of a non-root siadir:", dirPath, err)
				}
				batch.batchSet[i-1][parent] = struct{}{}
			}
		}
	}

	// Wait until the previous batch is complete. If we are shutting down, go
	// ahead and front-run the previous batch and just signal a close
	// immediately.
	select {
	case <-batch.priorCompleteChan:
	case <-batch.renter.tg.StopChan():
	}
	close(batch.completeChan)
}

// callQueueUpdate will add an update to the current batch. The input needs to
// be a dir.
func (dub *dirUpdateBatcher) callQueueDirUpdate(dirPath skymodules.SiaPath) {
	dub.mu.Lock()
	defer dub.mu.Unlock()
	if dub.closed {
		return
	}

	// Make sure maps at each depth exist.
	depth := dirPath.Depth()
	for i := len(dub.nextBatch.batchSet); i <= depth; i++ {
		dub.nextBatch.batchSet = append(dub.nextBatch.batchSet, make(map[skymodules.SiaPath]struct{}))
	}
	// Add the input dirPath to the final level.
	dub.nextBatch.batchSet[depth][dirPath] = struct{}{}
}

// callFlush will trigger the current batch of updates to execute, and will not
// return until all updates have completed and are represented in the root
// directory. It will also not return until all prior batches have completed as
// well - if you have added a directory to a batch and call flush, you can be
// certain that the directory update will have executed by the time the flush
// call returns, regardless of which batch that directory was added to.
func (dub *dirUpdateBatcher) callFlush() {
	// Grab the complete chan for the current batch.
	dub.mu.Lock()
	completeChan := dub.nextBatch.completeChan
	dub.mu.Unlock()

	// Signal that the current batch should be flushed.
	select {
	case dub.staticFlushChan <- struct{}{}:
	default:
	}

	// Wait until the batch has completed before returning. No need to wait if
	// the renter has closed, just exit immediately.
	select {
	case <-completeChan:
	case <-dub.staticRenter.tg.StopChan():
	}
}

// newBatch returns a new dirUpdateBatch ready for use.
func (dub *dirUpdateBatcher) newBatch(priorCompleteChan <-chan struct{}) *dirUpdateBatch {
	return &dirUpdateBatch{
		completeChan:      make(chan struct{}),
		priorCompleteChan: priorCompleteChan,

		dirUpdateBatchDeps: dirUpdateBatchDeps{
			renter: dub.staticRenter,
		},
	}
}

// threadedExecuteBatchUpdates is a permanent background thread which will
// execute batched updates in the background.
func (dub *dirUpdateBatcher) threadedExecuteBatchUpdates() {
	for {
		select {
		case <-dub.staticRenter.tg.StopChan():
			dub.mu.Lock()
			dub.closed = true
			dub.mu.Unlock()
			dub.nextBatch.managedExecute()
			return
		case <-dub.staticFlushChan:
		case <-time.After(maxTimeBetweenBatchExecutions):
		}

		// Rotate the current batch out for a new batch. This will block any
		// thread trying to add new updates to the batch, so make sure it
		// happens quickly.
		dub.mu.Lock()
		batch := dub.nextBatch
		dub.nextBatch = dub.newBatch(batch.priorCompleteChan)
		dub.mu.Unlock()
		// Execute the batch now that we aren't blocking anymore.
		batch.managedExecute()
	}
}

// newDirUpdateBatcher returns a health update batcher that is ready for use.
func (r *Renter) newDirUpdateBatcher() (*dirUpdateBatcher, error) {
	dub := &dirUpdateBatcher{
		staticFlushChan: make(chan struct{}, 1),
		staticRenter:    r,
	}

	// The next batch needs a channel which will be closed when the previous
	// batch completes. Since there is no previous batch, we provide a channel
	// that is already closed.
	initialChan := make(chan struct{})
	close(initialChan)

	dub.nextBatch = dub.newBatch(initialChan)
	err := r.tg.Launch(dub.threadedExecuteBatchUpdates)
	if err != nil {
		return nil, errors.AddContext(err, "unable to launch the batch updates backghround thread")
	}
	return dub, nil
}

// UpdateMetadata will explicitly update the metadata of the provided directory,
// returning once the directory has been updated and the changes are reflected
// in the aggregate metadata of the root directory. If the recursive flag is
// set, it will do a check on all subdirs as well.
//
// NOTE: This call is not very efficient, and generally isn't intended to be
// used on large directories with lots of subdirectories.
func (r *Renter) UpdateMetadata(siaPath skymodules.SiaPath, recursive bool) error {
	err := r.tg.Add()
	if err != nil {
		return err
	}
	defer r.tg.Done()

	// Use a list to track all of the siapaths we want.
	dirPaths := list.New()
	dirPaths.PushBack(siaPath)
	for dirPaths.Front() != nil {
		e := dirPaths.Front()
		dirPaths.Remove(e)
		siaPath := e.Value.(skymodules.SiaPath)
		err := r.managedUpdateFilesInDir(siaPath)
		if err != nil {
			context := fmt.Sprintf("unable to update the metadata of the files in dir %v", siaPath)
			return errors.AddContext(err, context)
		}
		r.staticDirUpdateBatcher.callQueueDirUpdate(siaPath)
		if !recursive {
			// If the recursive flag isn't set, this should trigger immediately
			// and result in only one directory being processed.
			continue
		}

		// The recursive flag is set, so load the full list of subdirectories
		// and ensure the loop will scan all of those directories as well.
		subDirPaths, err := r.managedSubDirectories(siaPath)
		if err != nil {
			context := fmt.Sprintf("unable to load list of subdirs for %v", siaPath)
			return errors.AddContext(err, context)
		}
		for _, subDir := range subDirPaths {
			dirPaths.PushBack(subDir)
		}
	}

	// Block until all updates are represented in the root aggregate metadata.
	r.staticDirUpdateBatcher.callFlush()
	return nil
}
