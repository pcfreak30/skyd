package renter

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
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
	slV1, entries, err := wt.rt.renter.managedTryResolveSkylinkV2(context.Background(), skylinkV2, true)
	if err != nil {
		t.Fatal(err)
	}

	// Skylinks should match.
	if !reflect.DeepEqual(skylinkV1, slV1) {
		t.Fatal("skylinks don't match")
	}

	// Should only be one entry
	if len(entries) != 1 {
		t.Fatal("Expected only 1 entry, got", len(entries))
	}
	entry := entries[0]

	// Entry shouldn't be nil.
	expectedSRV := skymodules.NewRegistryEntry(spk, srv)
	if !reflect.DeepEqual(entry, expectedSRV) {
		t.Log(entry)
		t.Log(expectedSRV)
		t.Fatal("entry mismatch")
	}

	// Try resolving the v1 skylink. Should be a no-op.
	slV1, entries, err = wt.rt.renter.managedTryResolveSkylinkV2(context.Background(), skylinkV1, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(skylinkV1, slV1) {
		t.Fatal("skylinks don't match")
	}
	if entries != nil {
		t.Fatal("entries should be nil")
	}
}

// TestShortFanoutPanic is a regression test for when the size of a fanout
// doesn't match the filesize.
func TestShortFanoutPanic(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	wt, err := newWorkerTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}

	// Add 2 more hosts.
	if _, err = wt.rt.addHost(t.Name() + "1"); err != nil {
		t.Fatal(err)
	}
	if _, err = wt.rt.addHost(t.Name() + "2"); err != nil {
		t.Fatal(err)
	}

	r := wt.rt.renter

	// Prepare a metadata for a basic file.
	fileSize := modules.SectorSize * 3
	md := skymodules.SkyfileMetadata{
		Filename: "test",
		Length:   fileSize,
	}
	metadataBytes, err := skymodules.SkyfileMetadataBytes(md)
	if err != nil {
		t.Fatal(err)
	}

	// Get some random data and fanout.
	data := fastrand.Bytes(int(fileSize))
	ec, err := skymodules.NewRSSubCode(2, 1, crypto.SegmentSize)
	if err != nil {
		t.Fatal(err)
	}

	// Upload the fanout.
	fileNode, err := r.managedInitUploadStream(skymodules.FileUploadParams{
		CipherType:  crypto.TypePlain,
		ErasureCode: ec,
		SiaPath:     skymodules.RandomSiaPath(),
	})
	if err != nil {
		t.Fatal(err)
	}
	chunkReader := NewFanoutChunkReader(bytes.NewReader(data), fileNode.ErasureCode(), false, fileNode.MasterKey())
	_, err = r.callUploadStreamFromReaderWithFileNode(context.Background(), fileNode, chunkReader, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := fileNode.Close(); err != nil {
		t.Fatal(err)
	}
	fanout := chunkReader.Fanout()

	// Shrink the fanout to 1 chunk.
	fanout = fanout[:ec.NumPieces()*crypto.HashSize]

	// Prepare a layout.
	sl := skymodules.SkyfileLayout{
		Version:            skymodules.SkyfileVersion,
		Filesize:           fileSize,
		MetadataSize:       uint64(len(metadataBytes)),
		FanoutSize:         uint64(len(fanout)),
		FanoutDataPieces:   uint8(ec.MinPieces()),
		FanoutParityPieces: uint8(ec.NumPieces() - ec.MinPieces()),
		CipherType:         crypto.TypePlain,
	}

	// Prepare a base sector with fanout but also with the file data.
	bs, fetchSize := skymodules.BuildBaseSector(sl.Encode(), fanout, metadataBytes, nil)
	if err != nil {
		t.Fatal(err)
	}
	skylink, err := skymodules.NewSkylinkV1(crypto.MerkleRoot(bs), 0, fetchSize)
	if err != nil {
		t.Fatal(err)
	}

	// Upload the base sector.
	err = r.managedUploadBaseSector(context.Background(), skymodules.SkyfileUploadParameters{
		SiaPath: skymodules.RandomSiaPath(),
	}, bs, skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Download the file. This should fail due to the malformed fanout.
	_, _, err = r.DownloadSkylink(skylink, time.Hour, types.SiacoinPrecision.MulFloat(1e-7))
	if err == nil || !strings.Contains(err.Error(), skymodules.ErrMalformedBaseSector.Error()) {
		t.Fatal(err)
	}
}
