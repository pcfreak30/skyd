package skymodules

import (
	"io/ioutil"
	"net"
	"os"
	"time"

	"gitlab.com/NebulousLabs/fastrand"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/persist"
	"go.sia.tech/siad/types"
)

var (
	// skynetAddress is an address owned by Skynet Labs
	skynetAddress = [32]byte{83, 122, 255, 169, 118, 147, 164, 142, 35, 145, 142, 89, 206, 40, 18, 67, 242, 215, 72, 187, 139, 253, 173, 164, 149, 53, 197, 250, 173, 62, 250, 194}

	// SkydProdDependencies act as a global instance of the skynet dependencies
	// to avoid having to instantiate new dependencies every time we want to
	// pass skynet dependencies.
	SkydProdDependencies = new(SkynetDependencies)
)

type (
	// SkydDependencies defines dependencies used by all of Skyd's modules.
	// Custom dependencies can be created to inject certain behavior during
	// testing.
	SkydDependencies interface {
		modules.Dependencies

		// SkynetAddress returns an address to be used to send SC to Skynet Labs
		SkynetAddress() types.UnlockHash
	}

	// SkynetDependencies are the dependencies used in a Release or Debug
	// production build.
	SkynetDependencies struct {
	}
)

// Satisfy the Skynet specific interface

// SkynetAddress returns an address owned by Skynet Labs
func (*SkynetDependencies) SkynetAddress() types.UnlockHash {
	return skynetAddress
}

// Satisfy the modules.Dependencies interface

// AtLeastOne will return a value that is equal to 1 if debugging is disabled.
// If debugging is enabled, a higher value may be returned.
func (*SkynetDependencies) AtLeastOne() uint64 {
	return modules.ProdDependencies.AtLeastOne()
}

// CreateFile gives the host the ability to create files on the operating
// system.
func (sd *SkynetDependencies) CreateFile(s string) (modules.File, error) {
	return modules.ProdDependencies.CreateFile(s)
}

// Destruct checks that all resources have been cleaned up correctly.
func (sd *SkynetDependencies) Destruct() {
	modules.ProdDependencies.Destruct()
}

// DialTimeout creates a tcp connection to a certain address with the specified
// timeout.
func (*SkynetDependencies) DialTimeout(addr modules.NetAddress, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", string(addr), timeout)
}

// Disrupt can be used to inject specific behavior into a module by overwriting
// it using a custom dependency.
func (*SkynetDependencies) Disrupt(string) bool {
	return false
}

// Listen gives the host the ability to receive incoming connections.
func (*SkynetDependencies) Listen(s1, s2 string) (net.Listener, error) {
	return net.Listen(s1, s2)
}

// LoadFile loads JSON encoded data from a file.
func (*SkynetDependencies) LoadFile(meta persist.Metadata, data interface{}, filename string) error {
	return persist.LoadJSON(meta, data, filename)
}

// LookupIP resolves a hostname to a number of IP addresses. If an IP address
// is provided as an argument it will just return that IP.
func (*SkynetDependencies) LookupIP(host string) ([]net.IP, error) {
	return (modules.ProductionResolver{}).LookupIP(host)
}

// SaveFileSync writes JSON encoded data to a file and syncs the file to disk
// afterwards.
func (*SkynetDependencies) SaveFileSync(meta persist.Metadata, data interface{}, filename string) error {
	return persist.SaveJSON(meta, data, filename)
}

// MkdirAll gives the host the ability to create chains of folders within the
// filesystem.
func (*SkynetDependencies) MkdirAll(s string, fm os.FileMode) error {
	return os.MkdirAll(s, fm)
}

// NewLogger creates a logger that the host can use to log messages and write
// critical statements.
func (*SkynetDependencies) NewLogger(s string) (*persist.Logger, error) {
	return persist.NewFileLogger(s)
}

// OpenDatabase creates a database that the host can use to interact with large
// volumes of persistent data.
func (*SkynetDependencies) OpenDatabase(m persist.Metadata, s string) (*persist.BoltDatabase, error) {
	return persist.OpenDatabase(m, s)
}

// Open opens a file readonly.
func (sd *SkynetDependencies) Open(s string) (modules.File, error) {
	return sd.OpenFile(s, os.O_RDONLY, 0)
}

// OpenFile opens a file with the specified mode and permissions.
func (sd *SkynetDependencies) OpenFile(s string, i int, fm os.FileMode) (modules.File, error) {
	return modules.ProdDependencies.OpenFile(s, i, fm)
}

// RandRead fills the input bytes with random data.
func (*SkynetDependencies) RandRead(b []byte) (int, error) {
	return fastrand.Reader.Read(b)
}

// ReadFile reads a file from the filesystem.
func (*SkynetDependencies) ReadFile(s string) ([]byte, error) {
	return ioutil.ReadFile(s)
}

// RemoveFile will remove a file from disk.
func (sd *SkynetDependencies) RemoveFile(s string) error {
	return modules.ProdDependencies.RemoveFile(s)
}

// RenameFile renames a file on disk.
func (sd *SkynetDependencies) RenameFile(s1 string, s2 string) error {
	return modules.ProdDependencies.RenameFile(s1, s2)
}

// Sleep blocks the calling thread for a certain duration.
func (*SkynetDependencies) Sleep(d time.Duration) {
	time.Sleep(d)
}

// Symlink creates a symlink between a source and a destination file.
func (*SkynetDependencies) Symlink(s1, s2 string) error {
	return os.Symlink(s1, s2)
}

// WriteFile writes a file to the filesystem.
func (*SkynetDependencies) WriteFile(s string, b []byte, fm os.FileMode) error {
	return ioutil.WriteFile(s, b, fm)
}

// Resolver returns the ProductionResolver.
func (*SkynetDependencies) Resolver() modules.Resolver {
	return modules.ProductionResolver{}
}
