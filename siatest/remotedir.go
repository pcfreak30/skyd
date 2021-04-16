package siatest

import (
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

type (
	// RemoteDir is a helper struct that represents a directory on the Sia
	// network.
	RemoteDir struct {
		siapath skymodules.SiaPath
	}
)

// SiaPath returns the siapath of a remote directory.
func (rd *RemoteDir) SiaPath() skymodules.SiaPath {
	return rd.siapath
}
