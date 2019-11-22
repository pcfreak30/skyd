# SIP-0006 - Peer Multiplexer

Status: Proposal

SIP-0006 is a description of a multiplexer for a TCP connection - the Peer
Multiplexer or peermux for short. The peermux will handle details such as
encryption and packet management and allow the underlying caller to open
multiple concurrent priority sorted connections to a remote host.

One of the goals of the peerrmux is that every connection looks identical, and
that an external observer cannot determine any information about what type of
actions are being performed over the peermux. The peermux aims to accomplish
this without sacrificing more than 1% overall performance in terms of throughput
and latency when compared to a typical TCP connection.

There is an intention to eventually support not just the renter-host protocol
over the peermux, but also to support the gateway protocols over the peermux,
with the renter-host and gateway protocols using the same port and same
underlying TCP connection when the counterparty is the same.

## Motivation and Rationale

The renter performs many actions on a host regularly. These actions frequently
consume different resources from the host, and could experience considerable
speedup by being performed concurrently. A strong example of this is uploading
and downloading. Before the peer multiplexer, an upload or a download is
essentially required to monopolize a connection, meaning that only one of those
operations can be performed with a host at a time, even though each operation
could run at approximately full speed if running in parallel.

Another key limitation that Sia must tolerate without a multiplexer is the
inability to queue multiple jobs on the host in parallel. Currently, the renter
must wait for an existing download job on a host to complete before scheduling
another download job on a host. This means that there is inherent downtime when
fetch data from a host between the sending of an original job and the sending of
a following job. This downtime happens between the time that the host sends the
final packet of the last job and the time that the renter receives the final
packet of the last job. A multiplexed connection allows jobs to be pipelined,
and gives the renter much greater flexibility in supporting actions like
multiple simultaneous file streams.

By having the peermux handle encryption, the complexity of encryption can be
removed from the renter-host protocol and instead the RPCs can directly use a
wrapped connection with the guarantee that security and privacy is being applied
automatically. The peerrmux can also handle issues such as optimal packet size
and padding.

Having a single external peermux also allows the peermux to evolve and be
extended to increasingly support better privacy and improved speeds
independently of the evolution of any RPCs that leverage the peermux.

Finally, Sia has been dependent on underspecified external libraries for
multiplexing since first becoming a production storage platform. The peermux can
be a fully specified multiplexing protocol that is up to the standards of the
Sia team and easily re-implemented by third party developers.

## Peermux Specification
