package renter

import (
	"encoding/binary"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/build"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// TestRegistryCache tests the in-memory registry type.
func TestRegistryCache(t *testing.T) {
	numEntries := uint64(100)
	cacheSize := numEntries * cachedEntryEstimatedSize

	// Create the cache and check its maxEntries field.
	cache := newRegistryCache(cacheSize, types.SiaPublicKey{})
	if cache.maxEntries != numEntries {
		t.Fatalf("maxEntries %v != %v", cache.maxEntries, numEntries)
	}

	// Get a public key.
	pk := types.SiaPublicKey{
		Algorithm: types.SignatureEd25519,
		Key:       fastrand.Bytes(crypto.PublicKeySize),
	}

	// Declare a helper to create registry values.
	registryValue := func(tweak, revNum uint64) modules.SignedRegistryValue {
		var t crypto.Hash
		binary.LittleEndian.PutUint64(t[:], tweak)
		return modules.NewSignedRegistryValue(t, []byte{}, revNum, crypto.Signature{}, modules.RegistryTypeWithoutPubkey)
	}

	// Set an entry.
	rv := registryValue(0, 0)
	cache.Set(modules.DeriveRegistryEntryID(pk, rv.Tweak), rv, false)
	if len(cache.entryMap) != 1 || len(cache.entryList) != 1 {
		t.Fatal("map and list should both have 1 element")
	}

	// Set it again with a higher revision.
	rv = registryValue(0, 1)
	cache.Set(modules.DeriveRegistryEntryID(pk, rv.Tweak), rv, false)
	if len(cache.entryMap) != 1 || len(cache.entryList) != 1 {
		t.Fatal("map and list should both have 1 element")
	}

	// Set it back with force = false. This should be a no-op.
	rv2 := registryValue(0, 0)
	cache.Set(modules.DeriveRegistryEntryID(pk, rv2.Tweak), rv2, false)
	if len(cache.entryMap) != 1 || len(cache.entryList) != 1 {
		t.Fatal("map and list should both have 1 element")
	}

	// Make sure the value can be retrieved.
	readRev, exists := cache.Get(modules.DeriveRegistryEntryID(pk, rv.Tweak))
	if !exists || readRev.Revision != 1 {
		t.Fatal("get returned wrong value", exists, readRev)
	}

	// Fill up the cache with numEntries-1 more entries. All have revision
	// number 1.
	for i := uint64(1); i < numEntries; i++ {
		tmpRV := registryValue(i, 1)
		cache.Set(modules.DeriveRegistryEntryID(pk, tmpRV.Tweak), tmpRV, false)
	}
	if uint64(len(cache.entryMap)) != numEntries || uint64(len(cache.entryList)) != numEntries {
		t.Fatal("map and list should both have numEntries element")
	}

	// Add one more element. This time with revision number 2.
	rv = registryValue(numEntries, 2)

	// The following code happens in a retry since an element that is added
	// might get evicted right away.
	err := build.Retry(1000, time.Millisecond, func() error {
		cache.Set(modules.DeriveRegistryEntryID(pk, rv.Tweak), rv, false)

		// The datastructures should still have the same length since a random
		// element was evicted.
		if uint64(len(cache.entryMap)) != numEntries || uint64(len(cache.entryList)) != numEntries {
			t.Fatal("map and list should both have numEntries element")
		}

		// Both datastructures should contain an entry with number 2.
		found := false
		for _, v := range cache.entryMap {
			if v.rv.Revision == 2 {
				found = true
				break
			}
		}
		if !found {
			return errors.New("new entry wasn't found")
		}
		found = false
		for _, v := range cache.entryList {
			if v.rv.Revision == 2 {
				found = true
				break
			}
		}
		if !found {
			return errors.New("new entry wasn't found")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Get should return the same entry.
	readRev, exists = cache.Get(modules.DeriveRegistryEntryID(pk, rv.Tweak))
	if !exists || readRev.Revision != 2 {
		t.Fatal("get returned wrong value", exists, readRev)
	}
}
