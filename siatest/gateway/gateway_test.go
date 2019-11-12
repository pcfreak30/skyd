package gateway

import (
	"errors"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/modules/gateway"
	"gitlab.com/NebulousLabs/Sia/node"
	"gitlab.com/NebulousLabs/Sia/siatest"
	"gitlab.com/NebulousLabs/Sia/siatest/dependencies"
)

// TestGatewayRatelimit makes sure that we can set the gateway's ratelimits
// using the API and that they are persisted correctly.
func TestGatewayRatelimit(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	testDir := gatewayTestDir(t.Name())

	// Create a new server
	testNode, err := siatest.NewCleanNode(node.Gateway(testDir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := testNode.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	// Get the current ratelimits.
	gg, err := testNode.GatewayGet()
	if err != nil {
		t.Fatal(err)
	}
	// Speeds should be 0 which means it's not being rate limited.
	if gg.MaxDownloadSpeed != 0 || gg.MaxUploadSpeed != 0 {
		t.Fatalf("Limits should be 0 but were %v and %v", gg.MaxDownloadSpeed, gg.MaxUploadSpeed)
	}
	// Change the limits.
	ds := int64(100)
	us := int64(200)
	if err := testNode.GatewayRateLimitPost(ds, us); err != nil {
		t.Fatal(err)
	}
	// Get the ratelimit again.
	gg, err = testNode.GatewayGet()
	if err != nil {
		t.Fatal(err)
	}
	// Limit should be set correctly.
	if gg.MaxDownloadSpeed != ds || gg.MaxUploadSpeed != us {
		t.Fatalf("Limits should be %v/%v but are %v/%v",
			ds, us, gg.MaxDownloadSpeed, gg.MaxUploadSpeed)
	}
	// Restart the node.
	if err := testNode.RestartNode(); err != nil {
		t.Fatal(err)
	}
	// Get the ratelimit again.
	gg, err = testNode.GatewayGet()
	if err != nil {
		t.Fatal(err)
	}
	// Limit should've been persisted correctly.
	if gg.MaxDownloadSpeed != ds || gg.MaxUploadSpeed != us {
		t.Fatalf("Limits should be %v/%v but are %v/%v",
			ds, us, gg.MaxDownloadSpeed, gg.MaxUploadSpeed)
	}
}

// TestGatewayBlacklist probes the gateway blacklist endpoints
func TestGatewayBlacklist(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create Gateway
	testDir := gatewayTestDir(t.Name())
	gateway, err := siatest.NewCleanNode(node.Gateway(testDir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := gateway.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Get current blacklist, should be empty
	blacklist, err := gateway.GatewayBlacklistGet()
	if err != nil {
		t.Fatal(err)
	}
	if len(blacklist.Blacklist) != 0 {
		t.Fatalf("Expected blacklist to be empty, got %v", blacklist)
	}

	hostnameNoPort := modules.NetAddress("host.name.No.Port")
	badHostname := modules.NetAddress("badHostname")
	noPortAddr := modules.NetAddress("123.123.123.123")
	hostnameWithPort := modules.NetAddress("host.name.With.Port:1")
	addr1 := modules.NetAddress("123.123.123.123:1")
	addr2 := modules.NetAddress("456.456.456.456:1")
	var blacklistTests = []struct {
		addresses    []modules.NetAddress
		action       string
		errExpected  bool
		lenBlacklist int
	}{
		{[]modules.NetAddress{hostnameNoPort}, "set", true, 0},    // check hostname with no port fails
		{[]modules.NetAddress{badHostname}, "set", true, 0},       // check bad hostname fails
		{[]modules.NetAddress{noPortAddr}, "set", true, 0},        // check address with no port fails
		{[]modules.NetAddress{hostnameWithPort}, "set", false, 1}, // check hostname with port succeeds
		{[]modules.NetAddress{addr1, addr2}, "append", false, 3},  // check appending addresses
		{[]modules.NetAddress{addr1}, "remove", false, 2},         // check removing addresses
		{[]modules.NetAddress{}, "remove", true, 2},               // check removing empty list results in an error
		{[]modules.NetAddress{}, "set", false, 0},                 // check clearing the blacklist
	}

	for _, test := range blacklistTests {
		// Perform blacklist action
		switch test.action {
		case "set":
			err = gateway.GatewaySetBlacklistPost(test.addresses)
		case "append":
			err = gateway.GatewayAppendBlacklistPost(test.addresses)
		case "remove":
			err = gateway.GatewayRemoveBlacklistPost(test.addresses)
		default:
			t.Fatal("test action not recognized:", test.action)
		}
		// Check action error
		if err == nil && test.errExpected {
			t.Fatal("Expected error with test:", test)
		}
		if err != nil && !test.errExpected {
			t.Fatalf("Expected no error with tests %v but got %v", test, err)
		}

		// Check the Blacklist Length
		blacklist, err = gateway.GatewayBlacklistGet()
		if err != nil {
			t.Fatal(err)
		}
		if len(blacklist.Blacklist) != test.lenBlacklist {
			t.Fatalf("Expected blacklist to be of length %v but got %v", test.lenBlacklist, blacklist)
		}
		if test.lenBlacklist == 0 {
			continue
		}

		// Verify Blacklisted addresses
		blacklistMap := make(map[string]struct{})
		for _, addr := range blacklist.Blacklist {
			blacklistMap[addr] = struct{}{}
		}
		for _, addr := range test.addresses {
			_, ok := blacklistMap[addr.Host()]
			if !ok && test.action != "remove" {
				t.Fatalf("Did not find %v in the blacklist", addr.Host())
			}
			if ok && test.action == "remove" {
				t.Fatal("Address should have been removing from the blacklist:", addr)
			}
		}
	}
}

// TestGatewayOfflineAlert tests if a gateway correctly registers the
// appropriate alert when it is online.
func TestGatewayOfflineAlert(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	testDir := gatewayTestDir(t.Name())

	// Create a new server
	params := node.Gateway(testDir)
	params.GatewayDeps = &dependencies.DependencyDisableAutoOnline{}
	testNode, err := siatest.NewCleanNode(params)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := testNode.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	// Create a second server to connect to.
	testNode2, err := siatest.NewCleanNodeAsync(node.Gateway(testDir + "2"))
	if err != nil {
		t.Fatal(err)
	}
	// Test that gateway registered alert.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		_, err := testNode.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		dag, err := testNode.DaemonAlertsGet()
		if err != nil {
			t.Fatal(err)
		}
		for _, alert := range dag.Alerts {
			if alert.Module == "gateway" && alert.Cause == "" &&
				alert.Msg == gateway.AlertMSGGatewayOffline && alert.Severity == modules.SeverityWarning {
				return nil
			}
		}
		return errors.New("couldn't find correct alert")
	})
	if err != nil {
		t.Fatal(err)
	}

	// Connect nodes.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		return testNode.GatewayConnectPost(testNode2.GatewayAddress())
	})
	if err != nil {
		t.Fatal(err)
	}

	// Test that gateway unregistered alert.
	err = build.Retry(100, 100*time.Millisecond, func() error {
		_, err := testNode.GatewayGet()
		if err != nil {
			t.Fatal(err)
		}
		dag, err := testNode.DaemonAlertsGet()
		if err != nil {
			t.Fatal(err)
		}
		for _, alert := range dag.Alerts {
			if alert.Module == "gateway" && alert.Cause == "" &&
				alert.Msg == gateway.AlertMSGGatewayOffline && alert.Severity == modules.SeverityWarning {
				return errors.New("alert is still registered")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
