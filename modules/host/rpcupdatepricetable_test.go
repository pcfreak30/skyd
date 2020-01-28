package host

import (
	"encoding/json"
	"io/ioutil"
	"sync"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/siamux"
	"gitlab.com/NebulousLabs/siamux/mux"
)

// TestMarshalUnmarshalRPCPriceTable tests the MarshalJSON and UnmarshalJSON
// function of the RPC price table
func TestMarshalUnmarshalJSONRPCPriceTable(t *testing.T) {
	pt := modules.NewRPCPriceTable(time.Now().Add(1).Unix())
	pt.Costs[types.NewSpecifier("RPC1")] = types.NewCurrency64(1)
	pt.Costs[types.NewSpecifier("RPC2")] = types.NewCurrency64(2)

	bytes, err := json.Marshal(pt)
	if err != nil {
		t.Fatal("Failed to marshal RPC price table", err)
	}

	var ptUmar modules.RPCPriceTable
	err = json.Unmarshal(bytes, &ptUmar)
	if err != nil {
		t.Fatal("Failed to unmarshal RPC price table", err)
	}

	if pt.Expiry != ptUmar.Expiry {
		t.Log("expected:", pt.Expiry)
		t.Log("actual:", ptUmar.Expiry)
		t.Fatal("Unexpected Expiry after marshal unmarshal")
	}

	if len(pt.Costs) != len(ptUmar.Costs) {
		t.Log("expected:", len(pt.Costs))
		t.Log("actual:", len(ptUmar.Costs))
		t.Fatal("Unexpected # of Costs after marshal unmarshal")
	}

	for r, c := range pt.Costs {
		actual, exists := ptUmar.Costs[r]
		if !exists {
			t.Log(r)
			t.Fatal("Failed to find cost of RPC after marshal unmarshal")
		}
		if !c.Equals(actual) {
			t.Log("expected:", c)
			t.Log("actual:", actual)
			t.Fatal("Unexpected cost of RPC after marshal unmarshal")
		}
	}
}

// TestUpdatePriceTableRPC verifies the update price table RPC, it does this by
// manually calling the RPC handler.
func TestUpdatePriceTableRPC(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// create a blank host tester
	ht, err := blankHostTester(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer ht.Close()

	// call the update price table RPC directly
	cc, sc := createTestingConns()
	err = ht.host.managedRPCUpdatePriceTable(sc)
	if err != nil {
		t.Fatal("Failed to update the RPC price table", err)
	}

	// read the updated RPC price table
	var update modules.RPCUpdatePriceTableResponse
	if err = encoding.ReadObject(cc, &update, modules.RPCMinLen); err != nil {
		t.Fatal("Failed to read updated price table from the stream", err)
	}

	// unmarshal the JSON into a price table
	var pt modules.RPCPriceTable
	if err = json.Unmarshal(update.PriceTableJSON, &pt); err != nil {
		t.Fatal("Failed to unmarshal the JSON encoded RPC price table")
	}

	_, exists := pt.Costs[modules.RPCUpdatePriceTable.DontLookAtMeHarryImHideous()]
	if !exists {
		t.Log(pt)
		t.Fatal("Expected the cost of the updatePriceTableRPC to be defined")
	}
}

// createTestingMuxs creates a connected pair of type Mux which has already
// completed the encryption handshake and is ready to go.
func createTestingConns() (clientStream, serverStream siamux.Stream) {
	// Prepare tcp connections.
	clientConn, serverConn := createTestingConns()
	// Generate server keypair.
	serverPrivKey, serverPubKey := mux.GenerateED25519KeyPair()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var err error
		clientMux, err := mux.NewClientMux(clientConn, serverPubKey, persist.NewLogger(ioutil.Discard))
		if err != nil {
			panic(err)
		}
		clientStream, err = clientMux.NewStream()
		if err != nil {
			panic(err)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		var err error
		serverMux, err := mux.NewServerMux(serverConn, serverPubKey, serverPrivKey, persist.NewLogger(ioutil.Discard))
		if err != nil {
			panic(err)
		}
		serverStream, err = serverMux.AcceptStream()
		if err != nil {
			panic(err)
		}
	}()
	wg.Wait()
	return
}
