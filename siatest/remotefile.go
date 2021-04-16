package siatest

import (
	"sync"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

type (
	// RemoteFile is a helper struct that represents a file uploaded to the Sia
	// network.
	RemoteFile struct {
		checksum crypto.Hash
		siaPath  skymodules.SiaPath
		root     bool
		mu       sync.Mutex
	}
)

// Checksum returns the checksum of a remote file.
func (rf *RemoteFile) Checksum() crypto.Hash {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.checksum
}

// Root returns whether the siapath needs to be treated as an absolute path.
func (rf *RemoteFile) Root() bool {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.root
}

// SiaPath returns the siaPath of a remote file.
func (rf *RemoteFile) SiaPath() skymodules.SiaPath {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.siaPath
}
