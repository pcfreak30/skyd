// +build !linux,!darwin

package renter

import (
	"gitlab.com/NebulousLabs/errors"

	"gitlab.com/skynetlabs/skyd/skymodules"
)

var errNoFuseSupportOnSystem = errors.New("Fuse library is incompatible with this operating system.")

// dummyFuseManager implements the renterFuseManager interface.
type dummyFuseManager struct {
}

// Mount always returns an error since mounting a FUSE filesystem is not
// possible.
func (dm dummyFuseManager) Mount(mountPoint string, sp skymodules.SiaPath, opts skymodules.MountOptions) (err error) {
	return errNoFuseSupportOnSystem
}

// MountInfo returns the list of currently mounted fuse filesystems which is
// always empty on systems without FUSE support.
func (dm dummyFuseManager) MountInfo() []skymodules.MountInfo { return nil }

// Unmount always returns an error since mounting is not possible.
func (dm dummyFuseManager) Unmount(mountPoint string) error { return errNoFuseSupportOnSystem }

// newFuseManager return a dummyFuseManager.
func newFuseManager(r *Renter) renterFuseManager {
	return dummyFuseManager{}
}
