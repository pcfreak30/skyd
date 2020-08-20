package host

import (
	"net"

	"gitlab.com/NebulousLabs/Sia/modules"
)

type (
	// dependencyCustomLookupIP is a dependency that enables mocking of the
	// net.LookupIP method for testing.
	dependencyCustomLookupIP struct {
		modules.ProductionDependencies
		lookupIP func(string) ([]net.IP, error)
	}
)

// Resolver returns the dependency itself, which implements the LookupIP method.
func (d *dependencyCustomLookupIP) Resolver() modules.Resolver {
	return d
}

// LookupIP calls the custom lookupIP method the struct was created with.
func (d *dependencyCustomLookupIP) LookupIP(host string) ([]net.IP, error) {
	return d.lookupIP(host)
}

// NewDependencyCustomLookupIP creates a dependencyCustomLookupIP from the
// provided lookupIP method.
func NewDependencyCustomLookupIP(lookupIP func(string) ([]net.IP, error)) *dependencyCustomLookupIP {
	return &dependencyCustomLookupIP{
		lookupIP: lookupIP,
	}
}
