package host

// TODO: seems like there would be problems with the negotiation protocols if
// the renter tried something like 'form' or 'renew' but then the connections
// dropped after the host completed the transaction but before the host was
// able to send the host signatures for the transaction.
//
// Especially on a renew, the host choosing to hold the renter signatures
// hostage could be a pretty significant problem, and would require the renter
// to attempt a double-spend to either force the transaction onto the
// blockchain or to make sure that the host cannot abscond with the funds
// without commitment.
//
// Incentive for the host to do such a thing is pretty low - they will still
// have to keep all the files following a renew in order to get the money.

import (
	"io"
	"net"
	"sync/atomic"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

// rpcSettingsDeprecated is a specifier for a deprecated settings request.
var rpcSettingsDeprecated = types.NewSpecifier("Settings")

// threadedUpdateHostname periodically runs 'managedLearnHostname', which
// checks if the host's hostname has changed, and makes an updated host
// announcement if so.
func (h *Host) threadedUpdateHostname(closeChan chan struct{}) {
	defer close(closeChan)
	for {
		h.managedLearnHostname()
		// Wait 30 minutes to check again. If the hostname is changing
		// regularly (more than once a week), we want the host to be able to be
		// seen as having 95% uptime. Every minute that the announcement is
		// pointing to the wrong address is a minute of perceived downtime to
		// the renters.
		select {
		case <-h.tg.StopChan():
			return
		case <-time.After(time.Minute * 30):
			continue
		}
	}
}

// threadedTrackWorkingStatus periodically checks if the host is working,
// where working is defined as having received 3 settings calls in the past 15
// minutes.
func (h *Host) threadedTrackWorkingStatus(closeChan chan struct{}) {
	defer close(closeChan)

	// Before entering the longer loop, try a greedy, faster attempt to verify
	// that the host is working.
	prevSettingsCalls := atomic.LoadUint64(&h.atomicSettingsCalls)
	select {
	case <-h.tg.StopChan():
		return
	case <-time.After(workingStatusFirstCheck):
	}
	settingsCalls := atomic.LoadUint64(&h.atomicSettingsCalls)

	// sanity check
	if prevSettingsCalls > settingsCalls {
		build.Severe("the host's settings calls decremented")
	}

	h.mu.Lock()
	if settingsCalls-prevSettingsCalls >= workingStatusThreshold {
		h.workingStatus = modules.HostWorkingStatusWorking
	}
	// First check is quick, don't set to 'not working' if host has not been
	// contacted enough times.
	h.mu.Unlock()

	for {
		prevSettingsCalls = atomic.LoadUint64(&h.atomicSettingsCalls)
		select {
		case <-h.tg.StopChan():
			return
		case <-time.After(workingStatusFrequency):
		}
		settingsCalls = atomic.LoadUint64(&h.atomicSettingsCalls)

		// sanity check
		if prevSettingsCalls > settingsCalls {
			build.Severe("the host's settings calls decremented")
			continue
		}

		h.mu.Lock()
		if settingsCalls-prevSettingsCalls >= workingStatusThreshold {
			h.workingStatus = modules.HostWorkingStatusWorking
		} else {
			h.workingStatus = modules.HostWorkingStatusNotWorking
		}
		h.mu.Unlock()
	}
}

// threadedTrackConnectabilityStatus periodically checks if the host is
// connectable at its netaddress.
func (h *Host) threadedTrackConnectabilityStatus(closeChan chan struct{}) {
	defer close(closeChan)

	// Wait briefly before checking the first time. This gives time for any port
	// forwarding to complete.
	select {
	case <-h.tg.StopChan():
		return
	case <-time.After(connectabilityCheckFirstWait):
	}

	for {
		h.mu.RLock()
		autoAddr := h.autoAddress
		userAddr := h.settings.NetAddress
		h.mu.RUnlock()

		activeAddr := autoAddr
		if userAddr != "" {
			activeAddr = userAddr
		}

		dialer := &net.Dialer{
			Cancel:  h.tg.StopChan(),
			Timeout: connectabilityCheckTimeout,
		}
		conn, err := dialer.Dial("tcp", string(activeAddr))

		var status modules.HostConnectabilityStatus
		if err != nil {
			status = modules.HostConnectabilityStatusNotConnectable
		} else {
			conn.Close()
			status = modules.HostConnectabilityStatusConnectable
		}
		h.mu.Lock()
		h.connectabilityStatus = status
		h.mu.Unlock()

		select {
		case <-h.tg.StopChan():
			return
		case <-time.After(connectabilityCheckFrequency):
		}
	}
}

// initNetworking performs actions like port forwarding, and gets the
// host established on the network.
func (h *Host) initNetworking(address string) (err error) {
	// Create the listener and setup the close procedures.
	h.listener, err = h.dependencies.Listen("tcp", address)
	if err != nil {
		return err
	}
	// Automatically close the listener when h.tg.Stop() is called.
	threadedListenerClosedChan := make(chan struct{})
	h.tg.OnStop(func() {
		err := h.listener.Close()
		if err != nil {
			h.log.Println("WARN: closing the listener failed:", err)
		}

		// Wait until the threadedListener has returned to continue shutdown.
		<-threadedListenerClosedChan
	})

	// Set the initial working state of the host
	h.workingStatus = modules.HostWorkingStatusChecking

	// Set the initial connectability state of the host
	h.connectabilityStatus = modules.HostConnectabilityStatusChecking

	// Set the port.
	_, port, err := net.SplitHostPort(h.listener.Addr().String())
	if err != nil {
		return err
	}
	h.port = port
	if build.Release == "testing" {
		// Set the autoAddress to localhost for testing builds only.
		h.autoAddress = modules.NetAddress(net.JoinHostPort("localhost", h.port))
	}

	// Non-blocking, perform port forwarding and create the hostname discovery
	// thread.
	go func() {
		// Add this function to the threadgroup, so that the logger will not
		// disappear before port closing can be registered to the threadgrourp
		// OnStop functions.
		err := h.tg.Add()
		if err != nil {
			// If this goroutine is not run before shutdown starts, this
			// codeblock is reachable.
			return
		}
		defer h.tg.Done()

		err = h.g.ForwardPort(port)
		if err != nil {
			h.log.Println("ERROR: failed to forward port:", err)
		}

		threadedUpdateHostnameClosedChan := make(chan struct{})
		go h.threadedUpdateHostname(threadedUpdateHostnameClosedChan)
		h.tg.OnStop(func() {
			<-threadedUpdateHostnameClosedChan
		})

		threadedTrackWorkingStatusClosedChan := make(chan struct{})
		go h.threadedTrackWorkingStatus(threadedTrackWorkingStatusClosedChan)
		h.tg.OnStop(func() {
			<-threadedTrackWorkingStatusClosedChan
		})

		threadedTrackConnectabilityStatusClosedChan := make(chan struct{})
		go h.threadedTrackConnectabilityStatus(threadedTrackConnectabilityStatusClosedChan)
		h.tg.OnStop(func() {
			<-threadedTrackConnectabilityStatusClosedChan
		})
	}()

	// Launch the listener.
	go h.threadedListen(threadedListenerClosedChan)
	return nil
}

// threadedHandleConn handles an incoming connection to the host, typically an
// RPC.
func (h *Host) threadedHandleConn(conn net.Conn) {
	err := h.tg.Add()
	if err != nil {
		return
	}
	defer h.tg.Done()

	// Close the conn on host.Close or when the method terminates, whichever comes
	// first.
	connCloseChan := make(chan struct{})
	defer close(connCloseChan)
	go func() {
		select {
		case <-h.tg.StopChan():
		case <-connCloseChan:
		}
		conn.Close()
	}()

	// Set an initial duration that is generous, but finite. RPCs can extend
	// this if desired.
	err = conn.SetDeadline(time.Now().Add(5 * time.Minute))
	if err != nil {
		h.log.Println("WARN: could not set deadline on connection:", err)
		return
	}

	// Read the first 16 bytes. If those bytes are RPCLoopEnter, then the
	// renter is attempting to use the new protocol; otherweise, assume the
	// renter is using the old protocol, and that the following 8 bytes
	// complete the renter's intended RPC ID.
	var id types.Specifier
	if err := encoding.NewDecoder(conn, encoding.DefaultAllocLimit).Decode(&id); err != nil {
		atomic.AddUint64(&h.atomicUnrecognizedCalls, 1)
		h.log.Debugf("WARN: incoming conn %v was malformed: %v", conn.RemoteAddr(), err)
		return
	}
	if id != modules.RPCLoopEnter {
		// first 8 bytes should be a length prefix of 16
		if lp := encoding.DecUint64(id[:8]); lp != 16 {
			atomic.AddUint64(&h.atomicUnrecognizedCalls, 1)
			h.log.Debugf("WARN: incoming conn %v was malformed: invalid length prefix %v", conn.RemoteAddr(), lp)
			return
		}
		// shift down 8 bytes, then read next 8
		copy(id[:8], id[8:])
		if _, err := io.ReadFull(conn, id[8:]); err != nil {
			atomic.AddUint64(&h.atomicUnrecognizedCalls, 1)
			h.log.Debugf("WARN: incoming conn %v was malformed: %v", conn.RemoteAddr(), err)
			return
		}
	}

	switch id {
	// new RPCs: enter an infinite request/response loop
	case modules.RPCLoopEnter:
		err = extendErr("incoming RPCLoopEnter failed: ", h.managedRPCLoop(conn))
	// old RPCs: handle a single request/response
	case modules.RPCDownload:
		atomic.AddUint64(&h.atomicDownloadCalls, 1)
		err = extendErr("incoming RPCDownload failed: ", h.managedRPCDownload(conn))
	case modules.RPCRenewContract:
		atomic.AddUint64(&h.atomicRenewCalls, 1)
		err = extendErr("incoming RPCRenewContract failed: ", h.managedRPCRenewContract(conn))
	case modules.RPCFormContract:
		atomic.AddUint64(&h.atomicFormContractCalls, 1)
		err = extendErr("incoming RPCFormContract failed: ", h.managedRPCFormContract(conn))
	case modules.RPCReviseContract:
		atomic.AddUint64(&h.atomicReviseCalls, 1)
		err = extendErr("incoming RPCReviseContract failed: ", h.managedRPCReviseContract(conn))
	case modules.RPCSettings:
		atomic.AddUint64(&h.atomicSettingsCalls, 1)
		err = extendErr("incoming RPCSettings failed: ", h.managedRPCSettings(conn))
	case rpcSettingsDeprecated:
		h.log.Debugln("Received deprecated settings call")
	default:
		h.log.Debugf("WARN: incoming conn %v requested unknown RPC \"%v\"", conn.RemoteAddr(), id)
		atomic.AddUint64(&h.atomicUnrecognizedCalls, 1)
	}
	if err != nil {
		atomic.AddUint64(&h.atomicErroredCalls, 1)
		err = extendErr("error with "+conn.RemoteAddr().String()+": ", err)
		h.managedLogError(err)
	}
}

// threadedHandleStream handles an incoming stream.
func (h *Host) threadedHandleStream(s *modules.Stream) {
	err := h.tg.Add()
	if err != nil {
		return
	}
	defer h.tg.Done()

	readIDFromStream := func(s *modules.Stream) (id types.Specifier, err error) {
		err = encoding.ReadObject(s, id, uint64(modules.RPCMinLen))
		return
	}

	// Close the stream on host.Close or when the method terminates, whichever
	// comes first.
	closeStreamChan := make(chan struct{})
	defer close(closeStreamChan)
	go func() {
		select {
		case <-h.tg.StopChan():
		case <-closeStreamChan:
		}
		s.Close()
	}()

	// The first RPC a renter sends over the stream should always be one to
	// request the host's prices.

	// TODO: unsure if this is a good requirement to have, but the first RPC the
	// renter sends should be one that updates the RPC price table. That, or the
	// prices should be communicated when the connection is set up.
	id, err := readIDFromStream(s)
	if err != nil {
		atomic.AddUint64(&h.atomicUnrecognizedCalls, 1)
		return
	}
	// TODO: possible to signal this to the renter through writing an error
	// response somehow? Right now this just closes the stream
	if id != modules.RPCUpdatePriceTable {
		return
	}
	var pt modules.RPCPriceTable
	if pt, err = h.managedRPCUpdatePriceTable(s, pt); err != nil {
		return
	}

	// The first call to update the price table will have established an initial
	// set of prices for this "session". We keep processing RPCs coming over the
	// stream until it either times out or gets closed.
	for {
		id, err := readIDFromStream(s)
		if err != nil {
			if !errors.Contains(modules.ErrStreamClosed, err) {
				atomic.AddUint64(&h.atomicUnrecognizedCalls, 1)
			}
			break
		}

		// TODO: verify if current blockheight does not exceed the RPC price
		// table's expiry. In which case we should not accept the RPC and
		// indicate to the renter he has outdated prices.

		switch id {
		case modules.RPCFundEphemeralAccount:
			err = errors.AddContext(h.managedRPCFundEphemeralAccount(s, pt), "Failed to handle FundEphemeralAccountRPC")
		case modules.RPCUpdatePriceTable:
			// Note this RPC call will update the price table, this way the host
			// has a copy of the same price table the renter has and can verify
			// if the payment was sufficient.
			pt, err = h.managedRPCUpdatePriceTable(s, pt)
			err = errors.AddContext(err, "Failed to handle UpdatePriceTableRPC")
		default:
			// TODO: add logging
			atomic.AddUint64(&h.atomicUnrecognizedCalls, 1)
		}

		if errors.Contains(modules.ErrStreamTimeout, err) ||
			errors.Contains(modules.ErrStreamClosed, err) {
			break
		}

		if err != nil {
			// TODO: add context to the error
			atomic.AddUint64(&h.atomicErroredCalls, 1)
			h.managedLogError(err)
		}
	}
}

// threadedListen listens for incoming RPCs and spawns an appropriate handler
// for each.
func (h *Host) threadedListen(closeChan chan struct{}) {
	defer close(closeChan)

	// Both incoming connections and streams will pass a handler over this
	// channel. This ensures the rpcRatelimit applies to both the RPCs coming in
	// over a stream as well as over a legacy connection.
	handlers := make(chan func())

	// Receive connections until an error is returned by the listener. When
	// an error is returned, there will be no more calls to receive.
	go h.compatV1421threadedAcceptsConnections(handlers)

	// Receive streams until an error is returned by the siamux. When an error
	// is returned, there will be no more calls to receive.
	go h.threadedAcceptsStreams(handlers)

	for {
		handler := <-handlers
		go handler()

		// Soft-sleep to ratelimit the number of incoming RPCs.
		select {
		case <-h.tg.StopChan():
		case <-time.After(rpcRatelimit):
		}
	}
}

// compatV1421threadedAcceptsConnections receive connections until an error is
// returned by the listener. When an error is returned, there will be no more
// calls to receive. When a connection gets received, we pass a handler for it
// to the given handlers channel.
//
// Note: This is considered legacy and is replaced by threadedAcceptsStreams.
// Note: We do not add ourselves to the threadgroup, instead the handler will do
// so, this to avoid a possible deadlock when the threadgroup gets flushed.
//
// TODO: verify version
func (h *Host) compatV1421threadedAcceptsConnections(handlers chan func()) {
	for {
		// Block until there is a connection to handle.
		conn, err := h.listener.Accept()
		if err != nil {
			return
		}
		handlers <- func() {
			h.threadedHandleConn(conn)
		}
	}
}

// threadedAcceptsStreams receive streams until an error is returned by the
// siamux. When an error is returned, there will be no more streams to accept.
// When a connection gets received, we pass a handler for it to the given
// handlers channel.
//
// Note: We do not add ourselves to the threadgroup, instead the handler will do
// so, this to avoid a possible deadlock when the threadgroup gets flushed.
func (h *Host) threadedAcceptsStreams(handlers chan func()) {
	for {
		// Block until there is a stream to handle.
		stream, err := h.staticMux.Accept()
		if err != nil {
			return
		}
		handlers <- func() {
			h.threadedHandleStream(stream.(*modules.Stream))
		}
	}
}

// NetAddress returns the address at which the host can be reached.
func (h *Host) NetAddress() modules.NetAddress {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.settings.NetAddress != "" {
		return h.settings.NetAddress
	}
	return h.autoAddress
}

// NetworkMetrics returns information about the types of rpc calls that have
// been made to the host.
func (h *Host) NetworkMetrics() modules.HostNetworkMetrics {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return modules.HostNetworkMetrics{
		DownloadCalls:     atomic.LoadUint64(&h.atomicDownloadCalls),
		ErrorCalls:        atomic.LoadUint64(&h.atomicErroredCalls),
		FormContractCalls: atomic.LoadUint64(&h.atomicFormContractCalls),
		RenewCalls:        atomic.LoadUint64(&h.atomicRenewCalls),
		ReviseCalls:       atomic.LoadUint64(&h.atomicReviseCalls),
		SettingsCalls:     atomic.LoadUint64(&h.atomicSettingsCalls),
		UnrecognizedCalls: atomic.LoadUint64(&h.atomicUnrecognizedCalls),
	}
}
