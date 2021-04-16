package renter

import (
	"io/ioutil"
	"os"
	"testing"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

// TestRenterUploadDirectory verifies that the renter returns an error if a
// directory is provided as the source of an upload.
func TestRenterUploadDirectory(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	rt, err := newRenterTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	testUploadPath, err := ioutil.TempDir("", t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(testUploadPath); err != nil {
			t.Fatal(err)
		}
	}()

	params := skymodules.FileUploadParams{
		Source:      testUploadPath,
		SiaPath:     skymodules.RandomSiaPath(),
		ErasureCode: skymodules.NewRSCodeDefault(),
	}
	err = rt.renter.Upload(params)
	if err == nil {
		t.Fatal("expected Upload to fail with empty directory as source")
	}
	if !errors.Contains(err, ErrUploadDirectory) {
		t.Fatal("expected ErrUploadDirectory, got", err)
	}
}
