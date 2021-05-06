package renter

import (
	"context"
	"reflect"
	"testing"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

// TestTryResolveSkylinkV2 is a unit test for managedTryResolveSkylinkV2.
func TestTryResolveSkylinkV2(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	wt, err := newWorkerTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}

	var mr crypto.Hash
	fastrand.Read(mr[:])
	skylinkV1, err := skymodules.NewSkylinkV1(mr, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Set skylink on host.
	srv, spk, sk := randomRegistryValue()
	srv.Data = skylinkV1.Bytes()
	srv.Revision++
	srv = srv.Sign(sk)
	err = wt.UpdateRegistry(context.Background(), spk, srv)
	if err != nil {
		t.Fatal(err)
	}

	// Get the v2 skylink.
	skylinkV2 := skymodules.NewSkylinkV2(spk, srv.Tweak)

	// Resolve it.
	slV1, err := wt.rt.renter.managedTryResolveSkylinkV2(context.Background(), skylinkV2)
	if err != nil {
		t.Fatal(err)
	}

	// Skylinks should match.
	if !reflect.DeepEqual(skylinkV1, slV1) {
		t.Fatal("skylinks don't match")
	}

	// Try resolving the v1 skylink. Should be a no-op.
	slV1, err = wt.rt.renter.managedTryResolveSkylinkV2(context.Background(), skylinkV1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(skylinkV1, slV1) {
		t.Fatal("skylinks don't match")
	}
}
