package main

import (
	"testing"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetHQ/skyd/skymodules"
)

// TestParseLSArgs probes the parseLSArgs function
func TestParseLSArgs(t *testing.T) {
	t.Parallel()

	// Define Tests
	var tests = []struct {
		args []string
		sp   skymodules.SiaPath
		err  error
	}{
		// Valid Cases
		{nil, skymodules.RootSiaPath(), nil},
		{[]string{}, skymodules.RootSiaPath(), nil},
		{[]string{""}, skymodules.RootSiaPath(), nil},
		{[]string{"."}, skymodules.RootSiaPath(), nil},
		{[]string{"/"}, skymodules.RootSiaPath(), nil},
		{[]string{"path"}, skymodules.SiaPath{Path: "path"}, nil},

		// Invalid Cases
		{[]string{"path", "extra"}, skymodules.SiaPath{}, errIncorrectNumArgs},
		{[]string{"path", "extra", "extra"}, skymodules.SiaPath{}, errIncorrectNumArgs},
		{[]string{"...//////badd....////path"}, skymodules.SiaPath{}, skymodules.ErrInvalidSiaPath},
	}
	// Execute Tests
	for _, test := range tests {
		sp, err := parseLSArgs(test.args)
		if !sp.Equals(test.sp) {
			t.Log("Expected:", test.sp)
			t.Log("Actual:", sp)
			t.Error("unexpected siapath")
		}
		if !errors.Contains(err, test.err) && err != test.err {
			t.Log("Expected:", test.err)
			t.Log("Actual:", err)
			t.Error("unexpected error")
		}
	}
}
