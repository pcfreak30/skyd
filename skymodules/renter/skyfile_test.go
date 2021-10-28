package renter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
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

	// Wait for them to show up as workers.
	err = build.Retry(600, 100*time.Millisecond, func() error {
		_, err := wt.rt.miner.AddBlock()
		if err != nil {
			return err
		}
		r.staticWorkerPool.callUpdate()
		workers := r.staticWorkerPool.callWorkers()
		if len(workers) < 3 {
			return fmt.Errorf("expected %v workers but got %v", 3, len(workers))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

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

	// Init the upload.
	fileNode, err := r.managedInitUploadStream(skymodules.FileUploadParams{
		CipherType:  crypto.TypePlain,
		ErasureCode: ec,
		SiaPath:     skymodules.RandomSiaPath(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Upload the fanout.
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
	bs, fetchSize, _ := skymodules.BuildBaseSector(sl.Encode(), fanout, metadataBytes, nil)
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

	// Download the file. This should fail due to the short fanout.
	_, _, err = r.DownloadSkylink(skylink, time.Hour, skymodules.DefaultSkynetPricePerMS)
	if err == nil || !strings.Contains(err.Error(), skymodules.ErrMalformedBaseSector.Error()) {
		t.Fatal(err)
	}
}

// TestParseSkyfileMetadata tests parsing a recursive skyfile metadata.
func TestParseSkyfileMetadataRecursive(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}

	wt, err := newWorkerTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := wt.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	r := wt.rt.renter

	// Prepare a metadata for a basic file.
	fileSize := 3 * modules.SectorSize * modules.SectorSize
	md := skymodules.SkyfileMetadata{
		Filename: "test",
		Length:   fileSize,
	}
	metadataBytes, err := skymodules.SkyfileMetadataBytes(md)
	if err != nil {
		t.Fatal(err)
	}

	// Upload a huge amount of data to force a base sector recursion of
	// depth 1.
	data := fastrand.Bytes(int(fileSize))
	ec, err := skymodules.NewRSSubCode(2, 1, crypto.SegmentSize)
	if err != nil {
		t.Fatal(err)
	}

	// Upload the fanout. We don't really upload it here. Instead we just
	// use a chunkreader to figure out what the fanout would be for that
	// file.
	fileNode, err := r.managedInitUploadStream(skymodules.FileUploadParams{
		CipherType:  crypto.TypePlain,
		ErasureCode: ec,
		SiaPath:     skymodules.RandomSiaPath(),
	})
	if err != nil {
		t.Fatal(err)
	}
	chunkReader := NewFanoutChunkReader(bytes.NewReader(data), fileNode.ErasureCode(), false, fileNode.MasterKey())
	if err := fileNode.Close(); err != nil {
		t.Fatal(err)
	}

	// Read all chunks.
	for {
		_, _, err := chunkReader.ReadChunk()
		if errors.Contains(err, io.EOF) {
			break // done
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	fanout := chunkReader.Fanout()

	// Prepare a valid layout.
	sl := skymodules.NewSkyfileLayout(fileSize, uint64(len(metadataBytes)), uint64(len(fanout)), ec, crypto.TypePlain)

	// Prepare a base sector with fanout but also with the file data.
	bs, fetchSize, extension := skymodules.BuildBaseSector(sl.Encode(), fanout, metadataBytes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(extension) != 2 {
		t.Fatal("the depth of the returned extension should be 2", len(extension))
	}
	if len(bs) != int(modules.SectorSize) {
		t.Fatal("basesector should be exactly a sectorsize large at this point", len(bs))
	}

	skylink, err := skymodules.NewSkylinkV1(crypto.MerkleRoot(bs), 0, fetchSize)
	if err != nil {
		t.Fatal(err)
	}
	for _, ext := range extension {
		bs = append(bs, ext...)
	}

	// Upload the base sector.
	err = r.managedUploadBaseSector(context.Background(), skymodules.SkyfileUploadParameters{
		SiaPath: skymodules.RandomSkynetFilePath(),
	}, bs, skylink)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("BS uploaded")

	// Download the base sector using the skylink and parse it.
	offset, fetchSize, err := skylink.OffsetAndFetchSize()
	if err != nil {
		t.Fatal(err)
	}
	var bs2 []byte
	err = build.Retry(600, 100*time.Millisecond, func() error {
		t.Log("BS downloading...")
		bs2, _, err = r.managedDownloadByRoot(context.Background(), skylink.MerkleRoot(), offset, fetchSize, skymodules.DefaultSkynetPricePerMS)
		if err != nil {
			t.Log("BS download err", err)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("BS downloaded")

	// Compare base sectors.
	if !bytes.Equal(bs[:modules.SectorSize], bs2) {
		t.Fatal("base sectors don't match")
	}

	var sl2 skymodules.SkyfileLayout
	var fanout2, rawSM []byte
	var wps skymodules.WorkerPoolStatus
	var wpsErr error
	err = build.Retry(600, 100*time.Millisecond, func() error {
		t.Log("MD parsing...")
		sl2, fanout2, _, rawSM, _, _, err = r.ParseSkyfileMetadata(bs2)
		if err != nil {
			t.Log("MD parse err", err)
			wps, wpsErr = r.WorkerPoolStatus()
			if wpsErr != nil {
				err = errors.Compose(err, wpsErr)
			}
			return err
		}
		return nil
	})
	if err != nil {
		t.Log("num workers", wps.NumWorkers)
		t.Log("total DL cooldown", wps.TotalDownloadCoolDown)
		t.Log("total MAINT cooldown", wps.TotalMaintenanceCoolDown)
		t.Fatal(err)
	}
	t.Log("MD parsed")

	// Compare fanouts.
	if !bytes.Equal(sl.Encode(), sl2.Encode()) {
		t.Fatal("fanout mismatch")
	}
	if !bytes.Equal(metadataBytes, rawSM) {
		t.Fatal("md mismatch")
	}
	if !bytes.Equal(fanout, fanout2) {
		t.Fatal("fanout mismatch")
	}
}
