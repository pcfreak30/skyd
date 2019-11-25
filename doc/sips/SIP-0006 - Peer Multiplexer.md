# SIP-0006 - Peer Multiplexer

TODO: Haven't accounted for any data overhead that's caused by using the
encryption. I am assuming that there is probably some, because I believe all of
the messages are authenticated as well as encrypted. That's going to change the
packet math for determining how large the packets should be.

TODO: Not sure if the encryption scheme we currently use provides forward
secrecy or not. Not sure if that's something which should be in the threat model
or not.

TODO: The peermux should have some sort of tracking about how much data has been
sent or received by each peer so that some lower level system can handle
pricing. And there should be some API to allow the pricing module to tell
peermux to blacklist various peers for failing to pay the appropriate prices.
Maybe it's too early for that.

TODO: Need to update the interface so that an expected public key can be
provided.

Status: Proposal

SIP-0006 is a description of a multiplexer for a TCP connection - the Peer
Multiplexer or peermux for short. The peermux will handle details such as
encryption and packet management and allow the underlying caller to open
multiple concurrent priority sorted connections to a remote peer.

There is an intention to eventually support not just the renter-host protocol
over the peermux, but also to support the gateway protocols over the peermux,
with the renter-host and gateway protocols using the same port and same
underlying TCP connection when the counterparty is the same.

## Motivation and Rationale

The primary goal of the peermux is to improve the latency and throughput of
uploads and downloads. Currently, only one job can be issued at a time, and
there is downtime between jobs from the moment that the host sends the final
packet of a job to the moment that the renter receives that packet from the job.

Multiplexing the connections will allow uploading and downloading to happen
simultaneously, and will also enable pipelining in a way that eliminates the
downtime between jobs. Multiplexing also enables aggressive pre-emption of
existing jobs, which is important for low latency operations like download
streaming and seeking.

A secondary goal is to create an automatically secure connection on which to
build more complex protocols. The entire peermux connection is wrapped with a
secure encryption scheme, and the protocol is designed to have a homogenous
packet structure. The homogenous structure provides some obfuscation against
adversaries intending to learn information by spying on the connection.

## Peermux Specification

A peermux connection is a single TCP connection between the user and a single
remote party. The peermux will use this TPC connection to multiplex several
independent streams over the single connection. The core TCP connection will
handle encryption, padding, and other techniques for improving performance and
privacy. These peermux connections are intended to be long lived, potentially
many months for high uptime services, and therefore also perform tasks such as
keepalives.

Underlying modules can register a listener with the peermux connection to
receive new incoming streams. The listener will need to be registered with a
name like "renter-host" so that the peermux knows where to forward incoming
streams when the same connection is using multiple different protocols.

Outbound streams will similarly need to provide a name, this name will be used
to connect to the correct listener when opening a new stream with a peer.

Once a stream has been established, the underlying handlers will receive a
Stream that operates similarly to a net.Conn. The frame protocol, the
encryption, the padding, and the other optimizations of the peermux will be
hidden from the caller, the caller will only see a stream that acts much like a
normal net.Conn.

Streams are allowed to be either short lived or long lived. Streams can set a
timeout, this timeout will be reset every time a new message is sent or
received.

### Persistent Peers

Connections are maintained between peers at the peermux level. Connections will
be persistent between restarts of siad. Any subscriber to the peermux can
request a connection to a remote peer, and when that connection is requested a
lifetime must be provided. The peermux will maintain a continuous open
connection to this peer for the duration of the lifetime, including through
shutdowns. After a reboot, the peermux will immediately connect to this peer
again for communication. Ephemeral connections can be established by presenting
a short lifetime.

Information about the peer is persisted to maintain the relationship between
reboots. The information that gets persisted includes the remaining lifetime of
the peer and any shared encryption keys to the peer. Other information may be
persisted as well.

If a persistent peer is not connectable, the peermux will continue to retry to
connect to that peer with some interval. If a stream is requested to that
persistent peer, the peermux will try again at the moment the stream is
requested, even if the peermux has already tried recently. Even if a persistent
peer is chronically unconnectalbe, the peermux will continue to attempt to
connect to that peer so long as one subscriber has requested that peer as a
persistent peerr.

The peermux will allow any subscriber to view the list of persistent peers and
which subscribers have requested each. The subscribers can modify the set of
peers that they have requested at any time, adding or removing peers.

TODO: When starting up, the listeners may not be ready yet. How do we ensure
that a peermux isn't listening for new frames until all of the listeners have
loaded?

```go
// TODO: func ListPeers

// TODO: func SetPeers
```

### Peermux Streams

Peermux streams are similar to the net.Conn interface. They can read and write
concurrently. Peermux streams also have a priority, which can be updated at any
time.

```go
type Stream interface {
	// Send data down a stream.
	Read([]byte) (int, error)

	// Receive data from a stream.
	Write([]byte) (int, error)

	// Set the priority of a stream.
	SetPriority(int) error

	// SetTimeout establishes a timeout for the stream. This is different from a
	// deadline in a net.Conn in that the timeout resets every time data is
	// successfully read from or written to the stream.
	SetTimeout(time.Time) error
}

// NewStream will open a new stream with the provided destination. If no
// connection exists with that peer yet, a new connection will be created. If
// the lifetime of the connection to the peere is shorter than the provided
// lifetime, the lifetime for that peer will be updated.
func (pm *Peermux) NewStream(subscriberName, destination string, lifetime time.Time) (Stream, error) {
	// ...
}
```

When multiple streams are trying to Read or Write simultaneously to the same
Peermux connection, streams will be selected based on their priority. A
starvation control mechanism ensures that every read and write will eventually
be executed, however particular streams will be heavily favored for bandwidth if
their priority is higher. More bandwidth will be allocated if the gap in
priorities is large.

If the user's bandwidth is fully saturated, prioritization will happen across
peermux connections in addition to happening within a single connection. Streams
with higher priority that are pointed to other peers will get preferential
access to bandwidth even if the stream's peer has not received any data in a
while.

### Creating Listeners

Any subscriber can create a listener with the peermux. The subscriber will need
to supply a name for the listener, and a handler that can be called any time a
stream is opened which talks to that listener. The name of the listener must be
16 bytes or less. Listeners are global to the peermux - any peer can request any
listener which has registered with the peermux.

There is also a CloseListener function which allows a subscriber to declare that
the listener has been closed, so that the peermux no longer routes connections
to that listener.

```go
var Handler func(*context.Context, peermux.Stream) error

func (pm *Peermux) NewListener(name string, handler Handler) error {
	// ...
}

func (pm *Peerrmux) CloseListener(name string) error {
	// ...
}
```

### Frames

The peermux TCP connection gets broken into frames. Each frame has some metadata
to identify which stream the frame belongs to, to enable performance
optimizations, and to keep each side of the peermux session synchronized.

Every frame is padded to an exact number of packets. The packet size is
negotiated in the setup handshake.

```go
// frame defines a single frame for sending data through the peermux session.
// Each frame has 10 bytes of overhead. Typically, this will result in
// substantially less than 1% overhead for high performance transfers.
type frame struct {
	// The id of the frame indicates what stream the frame is contributing to. A
	// set of reserved IDs are used for peermux communications.
	id uint32

	// length indicates the number of bytes in the payload of the frame. All
	// frames are padded so that they consume an exact number of packets.
	length uint32

	// The Frame contains 16 flags that can be set to indicate information about
	// the frame and optimizations within the frame.
	flags uint16

	// The payload that is intended to be forwarded to the underlying stream.
	payload []byte
}
```

```go
// Flag bits and what they mean.
const (
	// Indicates whether or not this is the final frame for this stream. If set,
	// the stream is expected to be closed with no response.
	frameBitFinalFrame = 1 << 0

	// Indicates whether the stream is being closed becuase of a peermux error.
	// If set, the entire payload is an error string. Bit 0 must be set if bit 1
	// is set.
	frameBitErrorFrame = 1 << 1

	// frameBitPaddingContainsAnotherFrame indicates that another frame exists
	// in the padding of the current frame. This is an optimization technique to
	// increase the frame density when every frame needs to be padded to an
	// exact number of packets.
	frameBitPaddingContainsAnotherFrame = 1 << 2
)
```

The first 256 IDs for frames are reserved for protocol messages. Only the first
few are definied by this SIP, the rest are set aside for future use.

```go
// Reserved frame IDs and their meanings.
const (
	// frameIDErrorBadInit indicates that the frame ID was initialized
	// improperly, using the empty value instead of setting the frame to a
	// correct value. Any time the peermux receives a frameErrorBadInit, an
	// error frame will be returned to the peer indicating that a bad frame was
	// sent.
	frameIDErrorBadInit = iota

	// frameIDEstablishEncryption is used to indicate that a frame contains
	// setup information to establish a connection between two peermux peers. If
	// setup has not yet been completed, this is the only frame ID that is
	// allowed.
	frameIDEstablishEncryption

	// frameIDUpdateSettings is used to indicate that a peer wants to update
	// their connection settings. This is the first frame that is sent after
	// establishing an encrypted connection.
	frameIDUpdateSettings

	// frameIDKeepalive indicates that the peer is sending this frame to reset
	// the timeout on the peermux connection. Keepalive frames only need to be
	// sent if there has been no other recent activity - all frames will reset
	// the keepalive.
	frameIDKeepalive

	// frameIDNewStream announces the creation of a new stream from the other
	// party. This frame is used to create new multiplexed connections across
	// the peermux, and will contain not only the announcement of a new stream
	// but also the first bits of data in the stream.
	frameIDNewStream

	// ...

	frameReserved255
)
```

#### Connection Setup

Connection setup begins with establishing encryption, and then is followed by
sending over a set of settings via the update settings frame. The settings need
to be sent in a separate frame from the frame that is used to establish the
encryption, and cannot be sent until all required information has been received
from the other party. This is to ensure that an external observer cannot see the
settings of the connection.

If a shared key with that peer has already been persisted, then the step of
establishing encryption can be skipped. The settings frame can be sent, and
additional frames can be packed into the padding of the settings frame or
otherwise sent in separate packets. This allows the connection to get rolling
much more rapidly.

There is a chance that the other peer does not know the shared key, in which
case all of the frames sent before hearing any response from the other peer will
be seen as corrupted and lost. This creates an edge case for both the sender and
the receiver. The sender needs to remember all frames that were sent prior to
discovering that the receiver did not have the shared key, and the receiver
needs to realize that a large number of corrupted frames may be sent before the
sender sends an unencrypted frame to perform the Diffie-Helman exchange.

After encryption has been established, each peer will persist the shared key to
enable a faster connection to get started in the future.

```go
type establishEncryption struct {
	// TODO: Don't know the specifics here. If all desired properties are
	// achieved by X25519 (speed, forward secrecy, persistable shared key, etc)
	// then we should go with that scheme.
}

type connectionSettings struct {
	// Different connections are going to have different optimal packet sizes.
	// Typically, for IPv4-TCP connections, the optimal packet size is going to
	// be 1460 bytes, and for IPv6-TPC connections, the optimal packet size is
	// going to be 1440 bytes.
	//
	// These numbers are derived from the fact that Ethernetv2 packets generally
	// have an MTU of 1500 bytes. 20 bytes goes to IPv4 headers, 40 bytes
	// goes to IPv6 headers, and 20 bytes goes to TCP headerrs.
	//
	// The requestedPacketSize is not allowed to be smaller than 1220 bytes.
	// This is derived from the IPv6 required link MTU of 1280. 40 bytes of that
	// will be lost to IPv6, and 20 more will be lost to TPC.
	//
	// TODO: This does not account for the overhead of the encryption scheme
	// that we are using, need to account for that.
	requestedPacketSize uint16

	// maxFrameSize establishes the maximum size in bytes that the peermux will
	// accept for a frame. If a larger frame is sent, the peermux connection
	// will be closed.
	//
	// A typical maximum frame size is 64 packets. Generally frame sizes are
	// going to be small so that streams can be intertwined, and so that it is
	// easy to interrupt a low priority stream with a sudden high priority
	// stream. This value must be at least 16 packets.
	maxFrameSize uint32

	// maxPeermuxTimeout defines the maximum timeout that the peermux peer is
	// willing to accept for the connection. Keepalives need to be sent at least
	// this often.
	maxPeermuxTimeout uint16

	// maxStreamTimeout specifies how frequently a stream needs to send or
	// receive a frame in order to stay alive. If a stream reaches the timeout,
	// the stream will be closed without sending a message to the other peermux
	// that the stream is dead. The units are specified in seconds.
	maxStreamTimeout uint16
}
```

The settings can be updated at any time using an update settings frame.

#### Keepalive

A keepalive timeout is specified by the settings. The peermux connection will be
closed unless a frame is received from the peer before the timeout. Any frame
received is sufficient to reset the timeout, it does not need to be explicitly a
keepalive frame.

If there is no reason to send other frames, a peer can keep a connection alive
by sending a keepalive frame. The keepalive frame will typically be a single
packet.

Keepalives are sent at random intervals between 1/4 of the way through the
timeout and 1/2 of the way through the timeout. The randomness is an obfuscation
technique so that an external observer cannot tell whether a keepalive was sent
or some other message.

#### Error Handling for Reserved Frames

If at any point in time an error needs to be sent to the peer because of an
issue related to a reserved frame, an error can be sent using the same frame ID
as the type of reserved frame that experienced the issue. This error will be
followed up by closing the peermux connection.

Following an error during setup, the related peer will be put on a blacklist for
10 minutes, no connections will be accepted while the ban is in effect. The
purpose of the ban is to prevent a single misconfigured peer from locking into a
perpetual state of trying to form connections that aren't going to work.

### Stream Protocol

A new stream can be opened by sending a frame with the `frameNewStream` ID. That
frame will decode into a struct which contains a listener name, and a requested
ID for the stream. The listener name tells the peermux who to send the first
message, and the requested ID is an ID that the peer is requesting to idenfity
this specific stream when sending future frames. If all is well, the requested
ID should be an ID that is not currently being used for any other stream.

If there is no listener subscribed to the peermux with the provided name, or if
the requested ID is already taken or otherwise unable to be assigned, an error
frame will be returned. The error frame will have the same ID as the frame which
requested a new stream, and the error bit will be set in the frame flags. An
error payload can be sent for any other error that occurs related to a
particular stream at the peermux level.

```go
type NewStreamPayload struct {
	ListenerName [16]byte
	RequestedID  uint16

	FirstMessage []byte
}

type ErrorPayload struct {
	// ErrorResponse only has 256 bytes to leave room for other frames to be
	// sent in the padding. In practice, error messages are rarely longer than
	// this.
	ErrorResponse [256]byte
}
```

When a stream is closed, the final frame for that stream will have the closed
bit set in the flag streams. This means that there is no more room for
communication with this stream, and no more packets are expected.

The peer that closes a stream will need to be aware that additional frames for
that stream may have been sent by the other peer before the closed stream frame
reached the peer. If additional frames are recovered after a stream is closed,
these frames can be ignored and can be assumed to be a product of async
communication.

#### Stream Prioritization

Every outgoing write is put into two priority heaps. The first heap is built
based on on priority, and the second heap is based on queue time. When creating
frames, the selector will alternate between selecting from the priority heap and
selecting from the time heap. If less than one quarter of the keepalive time has
passed since the message in the time heap has been queued, a message from the
priority queue will be sent instead.

When selecting from the priority heap, a maximum size frame will be sent. When
selecting from the time heap, a frame that is one quarter of the maximum size
will be sent. In both cases, if there is any remaining data to be sent down that
stream, the timestamp will be reset and the stream will be re-inserted into the
heaps.

A second layer of queues is used at the peermux level. Every connection will use
the above strategy to create a frame that they wish to send next, and then that
frame will be presented to the global connection selector. The global connection
selector will know which Write calls to various have returned and which ones are
still blocking. Of the ones that are not blocking, the global connection
selector will choose the connection with the highest priority, unless one of
them has a queue time that is more than one fourth of the timeout in the past,
in which case it will select the message with the oldest queue time.

#### Covert Streams

A covert stream is a stream which hides in the padding of frames that are
padded. The intention of a covert stream is to hide from an external observer,
leaving no trace in the packet structure which indicates that a covert stream
may exist. For safety, a covert stream must always be the lowest priority, if
there is any non-covert stream which can fit a frame into the padding of another
frame, that non-covert stream must be given priority.

To make a stream a covert stream, the priority should be set to zero.
