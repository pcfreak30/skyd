package mdm

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/fastrand"
)

// TestInstructionReadSector tests executing a program with a single
// ReadSectorInstruction.
func TestInstructionReadSector(t *testing.T) {
	host := newTestHost()
	mdm := New(host)
	defer mdm.Stop()

	// Create a program to read a full sector from the host.
	instructions, programData := NewReadSectorProgram(modules.SectorSize, 0, crypto.Hash{}, false) // TODO: use merkle
	dataLen := uint64(len(programData))
	// Execute it.
	ics := uint64(0) // initial contract size is 0 sectors.
	imr := crypto.Hash{}
	fastrand.Read(imr[:]) // random initial merkle root
	finalize, outputs, err := mdm.ExecuteProgram(context.Background(), instructions, modules.InitCost(dataLen).Add(modules.ReadSectorCost()), newTestStorageObligation(true, ics, imr), dataLen, bytes.NewReader(programData))
	if err != nil {
		t.Fatal(err)
	}
	// There should be one output since there was one instruction.
	numOutputs := 0
	var sectorData []byte
	for output := range outputs {
		if err := output.Error; err != nil {
			t.Fatal(err)
		}
		if output.NewSize != ics {
			t.Fatalf("expected contract size to stay the same: %v != %v", ics, output.NewSize)
		}
		if output.NewMerkleRoot != imr {
			t.Fatalf("expected merkle root to stay the same: %v != %v", imr, output.NewMerkleRoot)
		}
		if len(output.Proof) != 0 {
			t.Fatalf("expected proof length to be %v but was %v", 0, len(output.Proof))
		}
		if uint64(len(output.Output)) != modules.SectorSize {
			t.Fatalf("expected returned data to have length %v but was %v", modules.SectorSize, len(output.Output))
		}
		sectorData = output.Output
		numOutputs++
	}
	if numOutputs != 1 {
		t.Fatalf("numOutputs was %v but should be %v", numOutputs, 1)
	}
	// No need to finalize the program since this program is readonly.
	if finalize != nil {
		t.Fatal("finalize callback should be nil for readonly program")
	}
	// Create a program to read half a sector from the host.
	offset := modules.SectorSize / 2
	length := offset
	instructions, programData = NewReadSectorProgram(length, offset, crypto.Hash{}, true)
	dataLen = uint64(len(programData))
	// Execute it.
	finalize, outputs, err = mdm.ExecuteProgram(context.Background(), instructions, modules.InitCost(dataLen).Add(modules.ReadSectorCost()), newTestStorageObligation(true, ics, imr), dataLen, bytes.NewReader(programData))
	if err != nil {
		t.Fatal(err)
	}
	// There should be one output since there was one instructions.
	numOutputs = 0
	for output := range outputs {
		if err := output.Error; err != nil {
			t.Fatal(err)
		}
		if output.NewSize != ics {
			t.Fatalf("expected contract size to stay the same: %v != %v", ics, output.NewSize)
		}
		if output.NewMerkleRoot != imr {
			t.Fatalf("expected merkle root to stay the same: %v != %v", imr, output.NewMerkleRoot)
		}
		proofStart := int(offset) / crypto.SegmentSize
		proofEnd := int(offset+length) / crypto.SegmentSize
		proof := crypto.MerkleRangeProof(sectorData, proofStart, proofEnd)
		if !reflect.DeepEqual(proof, output.Proof) {
			t.Fatal("proof doesn't match expected proof")
		}
		if !bytes.Equal(output.Output, sectorData[modules.SectorSize/2:]) {
			t.Fatal("output should match the second half of the sector data")
		}
		numOutputs++
	}
	if numOutputs != 1 {
		t.Fatalf("numOutputs was %v but should be %v", numOutputs, 1)
	}
	// No need to finalize the program since an this program is readonly.
	if finalize != nil {
		t.Fatal("finalize callback should be nil for readonly program")
	}
}
