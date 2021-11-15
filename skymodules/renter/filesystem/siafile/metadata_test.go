package siafile

import (
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"go.sia.tech/siad/types"
)

// TestBackupRestoreMetadata tests that restoring a metadata from its backup
// works as expected. Especially using it as a deferred statement like we would
// use it in production code.
func TestBackupRestoreMetadata(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}

	sf := newTestFile()

	// Test both nil slice and regular slice.
	if fastrand.Intn(2) == 0 {
		sf.staticMetadata.Skylinks = []string{}
	} else {
		sf.staticMetadata.Skylinks = nil
	}

	// Clone the metadata before modifying it.
	mdBefore := sf.staticMetadata.backup()

	// Make sure it's not the same address. Otherwise the test would later just
	// compare the pointer to itself.
	if &mdBefore == &sf.staticMetadata {
		t.Fatal("backup only copied pointer")
	}
	// To be 100% sure this works we call it like we would in the remaining
	// codebase. Deferred with a named retval.
	func() (err error) {
		// Adding this should restore the metadata later.
		defer func(backup Metadata) {
			if err != nil {
				sf.staticMetadata.restore(backup)
			}
		}(sf.staticMetadata.backup()) // NOTE: this needs to be passed in like that to work

		// Change all fields that are not static.
		sf.staticMetadata.UniqueID = SiafileUID(fmt.Sprint(fastrand.Intn(100)))
		sf.staticMetadata.FileSize = int64(fastrand.Intn(100))
		sf.staticMetadata.LocalPath = string(fastrand.Bytes(100))
		sf.staticMetadata.ModTime = time.Now()
		sf.staticMetadata.ChangeTime = time.Now()
		sf.staticMetadata.AccessTime = time.Now()
		sf.staticMetadata.CreateTime = time.Now()
		sf.staticMetadata.CachedRedundancy = float64(fastrand.Intn(10))
		sf.staticMetadata.CachedUserRedundancy = float64(fastrand.Intn(10))
		sf.staticMetadata.CachedHealth = float64(fastrand.Intn(10))
		sf.staticMetadata.CachedRepairBytes = fastrand.Uint64n(100)
		sf.staticMetadata.CachedStuckBytes = fastrand.Uint64n(100)
		sf.staticMetadata.CachedStuckHealth = float64(fastrand.Intn(10))
		sf.staticMetadata.CachedExpiration = types.BlockHeight(fastrand.Intn(10))
		sf.staticMetadata.CachedUploadedBytes = uint64(fastrand.Intn(1000))
		sf.staticMetadata.CachedUploadProgress = float64(fastrand.Intn(100))
		sf.staticMetadata.Finished = fastrand.Intn(2) == 0
		sf.staticMetadata.LastHealthCheckTime = time.Now()
		sf.staticMetadata.NumStuckChunks = fastrand.Uint64n(100)
		sf.staticMetadata.Redundancy = float64(fastrand.Intn(10))
		sf.staticMetadata.StuckBytes = fastrand.Uint64n(100)
		sf.staticMetadata.StuckHealth = float64(fastrand.Intn(100))
		sf.staticMetadata.Mode = os.FileMode(fastrand.Intn(100))
		sf.staticMetadata.UserID = int32(fastrand.Intn(100))
		sf.staticMetadata.GroupID = int32(fastrand.Intn(100))
		sf.staticMetadata.ChunkOffset = int64(fastrand.Uint64n(100))
		sf.staticMetadata.PubKeyTableOffset = int64(fastrand.Uint64n(100))
		sf.staticMetadata.Skylinks = nil
		if fastrand.Intn(2) == 0 { // 50% chance to be not nil
			sf.staticMetadata.Skylinks = make([]string, fastrand.Intn(10))
		}

		// Error occurred after changing the fields.
		return errors.New("")
	}()
	// Fields should be the same as before.
	if !reflect.DeepEqual(mdBefore, sf.staticMetadata) {
		t.Fatalf("metadata wasn't restored successfully %v %v", mdBefore, sf.staticMetadata)
	}
}
