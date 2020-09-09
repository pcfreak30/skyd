package renter

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/renter/contractor"
	"gitlab.com/NebulousLabs/Sia/modules/renter/proto"
	"gitlab.com/NebulousLabs/encoding"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
)

type (
	// jobUploadSnapshot is a job for the worker to upload a snapshot to its
	// respective host.
	jobUploadSnapshot struct {
		staticMetadata    modules.UploadedBackup
		staticSiaFileData []byte

		staticResponseChan chan *jobUploadSnapshotResponse

		*jobGeneric
	}

	// jobUploadSnapshotQueue contains the set of snapshots that need to be
	// uploaded.
	jobUploadSnapshotQueue struct {
		*jobGenericQueue
	}

	// jobUploadSnapshotResponse contains the response to an upload snapshot
	// job.
	jobUploadSnapshotResponse struct {
		staticErr error
	}
)

// callDiscard will discard this job, sending an error down the response
// channel.
func (j *jobUploadSnapshot) callDiscard(err error) {
	resp := &jobUploadSnapshotResponse{
		staticErr: errors.Extend(err, ErrJobDiscarded),
	}
	w := j.staticQueue.staticWorker()
	w.renter.tg.Launch(func() {
		select {
		case j.staticResponseChan <- resp:
		case <-j.staticCtx.Done():
		case <-w.renter.tg.StopChan():
		}
	})
}

// callExecute will perform an upload snapshot job for the worker.
func (j *jobUploadSnapshot) callExecute() {
	w := j.staticQueue.staticWorker()

	// Defer a function to send the result down a channel.
	var err error
	defer func() {
		// Return the error to the caller, error may be nil.
		resp := &jobUploadSnapshotResponse{
			staticErr: err,
		}
		w.renter.tg.Launch(func() {
			select {
			case j.staticResponseChan <- resp:
			case <-j.staticCtx.Done():
			case <-w.renter.tg.StopChan():
			}
		})

		// Report a failure to the queue if this job had an error.
		if err != nil {
			j.staticQueue.callReportFailure(err)
		} else {
			j.staticQueue.callReportSuccess()
		}
	}()

	// Check that the worker is good for upload.
	if !w.staticCache().staticContractUtility.GoodForUpload {
		err = errors.New("snapshot was not uploaded because the worker is not good for upload")
		return
	}

	// Perform the actual upload.
	var sess contractor.Session
	sess, err = w.renter.hostContractor.Session(w.staticHostPubKey, w.renter.tg.StopChan())
	if err != nil {
		w.renter.log.Debugln("unable to grab a session to perform an upload snapshot job:", err)
		err = errors.AddContext(err, "unable to get host session")
		return
	}
	defer func() {
		closeErr := sess.Close()
		if closeErr != nil {
			w.renter.log.Println("error while closing session:", closeErr)
		}
		err = errors.Compose(err, closeErr)
	}()

	allowance := w.renter.hostContractor.Allowance()
	gc := modules.CheckHostSettingsGouging(allowance, sess.HostSettings())
	if gc.UploadSnapshot.IsGouging {
		err = fmt.Errorf("snapshot upload blocked because potential price gouging was detected, %s", gc.UploadSnapshot.Reason)
		return
	}

	// Upload the snapshot to the host. The session is created by passing in a
	// thread group, so this call should be responsive to fast shutdown.
	err = w.renter.managedUploadSnapshotHost(j.staticMetadata, j.staticSiaFileData, sess)
	if err != nil {
		w.renter.log.Debugln("uploading a snapshot to a host failed:", err)
		err = errors.AddContext(err, "uploading a snapshot to a host failed")
		return
	}
}

// callExpectedBandwidth returns the amount of bandwidth this job is expected to
// consume.
func (j *jobUploadSnapshot) callExpectedBandwidth() (ul, dl uint64) {
	// Estimate 50kb in overhead for upload and download, and then 4 MiB
	// necessary to send the actual full sector payload.
	return 50e3 + 1<<22, 50e3
}

// initJobUploadSnapshotQueue will initialize the upload snapshot job queue for
// the worker.
func (w *worker) initJobUploadSnapshotQueue() {
	if w.staticJobUploadSnapshotQueue != nil {
		w.renter.log.Critical("should not be double initializng the upload snapshot queue")
		return
	}

	w.staticJobUploadSnapshotQueue = &jobUploadSnapshotQueue{
		jobGenericQueue: newJobGenericQueue(w),
	}
}

// managedUploadSnapshotHost uploads a snapshot to a single host.
func (r *Renter) managedUploadSnapshotHost(meta modules.UploadedBackup, dotSia []byte, host contractor.Session) error {
	// Get the wallet seed.
	ws, _, err := r.w.PrimarySeed()
	if err != nil {
		return errors.AddContext(err, "failed to get wallet's primary seed")
	}
	// Derive the renter seed and wipe the memory once we are done using it.
	rs := proto.DeriveRenterSeed(ws)
	defer fastrand.Read(rs[:])
	// Derive the secret and wipe it afterwards.
	secret := crypto.HashAll(rs, snapshotKeySpecifier)
	defer fastrand.Read(secret[:])

	// split the snapshot .sia file into sectors
	var sectors [][]byte
	for buf := bytes.NewBuffer(dotSia); buf.Len() > 0; {
		sector := make([]byte, modules.SectorSize)
		copy(sector, buf.Next(len(sector)))
		sectors = append(sectors, sector)
	}
	if len(sectors) > 4 {
		return errors.New("snapshot is too large")
	}

	// upload the siafile, creating a snapshotEntry
	var name [96]byte
	copy(name[:], meta.Name)
	entry := snapshotEntry{
		Name:         name,
		UID:          meta.UID,
		CreationDate: meta.CreationDate,
		Size:         meta.Size,
	}
	for j, piece := range sectors {
		root, err := host.Upload(piece)
		if err != nil {
			return errors.AddContext(err, "could not perform host upload")
		}
		entry.DataSectors[j] = root
	}

	// TODO: this should happen before uploading the pieces. Unfortunately RHP2
	// won't let us easily do that because host.Upload is going to fail if
	// called after managedDownloadSnapshotTableRHP2.
	// download the current entry table
	entryTable, err := r.managedDownloadSnapshotTableRHP2(host)
	if err != nil {
		return errors.AddContext(err, "could not download the snapshot table")
	}

	// check if the table already contains the entry.
	for _, existingEntry := range entryTable {
		if existingEntry.UID == meta.UID {
			return nil // host already contains entry
		}
	}

	shouldOverwrite := len(entryTable) != 0 // only overwrite if the sector already contained an entryTable
	entryTable = append(entryTable, entry)

	// if entryTable is too large to fit in a sector, repeatedly remove the
	// oldest entry until it fits
	id := r.mu.Lock()
	sort.Slice(r.persist.UploadedBackups, func(i, j int) bool {
		return r.persist.UploadedBackups[i].CreationDate > r.persist.UploadedBackups[j].CreationDate
	})
	r.mu.Unlock(id)
	c, _ := crypto.NewSiaKey(crypto.TypeThreefish, secret[:])
	for len(encoding.Marshal(entryTable)) > int(modules.SectorSize) {
		entryTable = entryTable[:len(entryTable)-1]
	}

	// encode and encrypt the table
	newTable := make([]byte, modules.SectorSize)
	copy(newTable[:16], snapshotTableSpecifier[:])
	copy(newTable[16:], encoding.Marshal(entryTable))
	tableSector := c.EncryptBytes(newTable)

	// swap the new entry table into index 0 and delete the old one
	// (unless it wasn't an entry table)
	if _, err := host.Replace(tableSector, 0, shouldOverwrite); err != nil {
		// Sometimes during the siatests, this will fail with 'write to host
		// failed; connection reset by peer. This error is very consistent in
		// TestRemoteBackup, but occurs after everything else has succeeded so
		// the test doesn't fail.
		return errors.AddContext(err, "could not perform sector replace for the snapshot")
	}
	return nil
}

// UploadSnapshot is a helper method to run a UploadSnapshot job on a worker.
func (w *worker) UploadSnapshot(ctx context.Context, meta modules.UploadedBackup, dotSia []byte) error {
	uploadSnapshotRespChan := make(chan *jobUploadSnapshotResponse)
	jus := &jobUploadSnapshot{
		staticMetadata:     meta,
		staticSiaFileData:  dotSia,
		staticResponseChan: uploadSnapshotRespChan,

		jobGeneric: newJobGeneric(ctx, w.staticJobUploadSnapshotQueue),
	}

	// Add the job to the queue.
	if !w.staticJobUploadSnapshotQueue.callAdd(jus) {
		return errors.New("worker unavailable")
	}

	// Wait for the response.
	var resp *jobUploadSnapshotResponse
	select {
	case <-ctx.Done():
		return errors.New("UploadSnapshot interrupted")
	case resp = <-uploadSnapshotRespChan:
	}
	return resp.staticErr
}
