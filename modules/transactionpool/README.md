# Transaction Pool
The transaction pool is responsible for tracking the set of unconfirmed
transactions on the network and for broadcasting new transactions out to the
network.

## Subsystems
The transaction pool has the following subsystems.
 - [Core](#core)
 - [New Peer Share](#new-peer-share)
 - [Repeat Broadcast Filter](#repeat-broadcast-filter)

### Core
**Key Files**
 - [accept.go](./accept.go)
 - [database.go](./database.go)
 - [persist.go](./persist.go)
 - [standard.go](./standard.go)
 - [subscribe.go](./subscribe.go)
 - [transactionpool.go](./transactionpool.go)
 - [update.go](./update.go)

The core subsystem contains all of the code that existed in the transaction pool
before we started breaking packages down into subsystems. The core subsystem
should be broken down and separated out into new subsystems.

### New Peer Share
**Key Files**
 - [newpeershare.go](./newpeershare.go)
 - [newpeershareconsts.go](./newpeershareconsts.go)

The new peer share subsystem is responsible for tracking which peers siad is
connected to and sharing the transactions in the transaction pool with that
peer.

The subsystem has an independent thread which monitors the peers in the gateway
and adds work to a queue. The work is a map of transactions that the tpool knows
the peer hasn't seen yet. The transactions will be listed one by one, but will
be broadcast one set at a time. When a full set is broadcast, all transactions
in that set will be removed from the work map. We track by transaction instead
of by transaction set because the transaction sets are constantly changing as
new transactions are broadcast and as new blocks are found. Storing by
transaction enables the new peer share submodule to handle the constantly
changing transaction sets without much internal complexity and without needing
to interact with other subsystems. It also allows for significant changes to the
structure of the core submodule without breaking the new peer share subsystem.

The subsystem will self-ratelimit when sending new transactions to peers. It
will send each transaction set at full speed, but then sleep for some time
between the sending of each set to ensure that the overall data rate for that
peer is reasonable. There is a concern that a node could be DoS'd by a high
bandwidth peer who is continuously connecting, disconnecting, and re-connecting,
causing the host peer to continually send that peer the whole transaction pool.
This DoS isn't so bad because the bandwidth ratio for an attacker to the node is
1:1.

##### Outbound Complexities
 - `callBroadcastTransactionSet` from the [Repeat Broadcast
   Filter](#repeat-broadcast-filter) subsystem will be used to send transaction
   sets to new peers.

##### State Complexities
The new peer share subsystem depends on the `synced` value of the transaction
pool, which needs to be updated every time a new `ConsensusChange` is received
by `ProcessConsensusChange` in [update.go](./update.go).

##### TODOs
 - TODO: Instead of polling the gateway to see when peers are coming and going,
   the transaction pool should be getting some sort of subscription type
   notification from the gateway each time the peer set changes. This would
   allow the central goroutine to be removed and also allow transaction
   propagation to begin more quickly.
 - TODO: The subsystem will not start sending transactions to a new peer until
   it knows that it has the most recent blocks on the network. This is to avoid
   sending the peer outdated transactions if the transaction pool is still
   catching up to the most recent block.
 - TODO: To help mitigate the DoS vector where an attacker is having the peer
   continually resync the attacker, we can remember which IPs we've synced even
   after the node has disconnected, and refuse to re-send a transaction set to
   the same IP or IP range multiple times. This set will be cleared out after a
   peer from an IP range has been gone for a sufficient amount of time (likely
   several hours).
 - TODO: The new peer share subsystem ideally sends transaction sets to peers
   roughly in order of fee rate. This ensures that new peers get the most
   valuable transactions first and have the best idea for what sorts of fees are
   required to get into blocks. The new peer share subsystem also ideally sends
   transactions in a semi-random order so that a new peer to the network is
   receiving different transactions from all of its peers instead of the same
   information over and over from each peer.
 - TODO: The new peer share subsystem should have lowest priority when bumping
   up against the ratelimits on the gateway, other bandwidth such as new
   transaction broadcasting is more important.

### Repeat Broadcast Filter
**Key Files**
 - [repeatbroadcastfilter.go](./repeatbroadcastfilter.go)

The repeat broadcast filter will track each transaction in the transaction pool,
remembering which peers have received that transaction before. This will prevent
the transaction pool from sending redundant information around the network.

The main mechanism of action is to have a map that links from transaction id to
a map of peers. So for each transaction, we have a map of which peers have
received that transaction already. When a new transaction is entered into the
transaction pool, a corresponding map is created for that transaction. And when
a transaction is removed from the transaction pool, the corresponding map is
deleted.

`map[types.TransactionID]map[modules.NetAddress]struct{}`

When choosing to broadcast a transaction set to a peer, we will check what
transactions of that set we have already sent to that peer

There's a special case for inserting transactions that are added to the pool
from reverted blocks. If a block is reverted, we assume that all of our peers
already have that transaction since it was in a block that was propagated, so
we'll add those transactions assuming that all peers already have the
transaction.

##### Inbound Complexities
 - `callBroadcastTransactionSet` can be used to send transactions to peers.
   - The [Core](#core) subsystem will use `callBroadcastTransactionSet` for
	 general purpose transaction broadcasting and relaying
   - The [New Peer Share](#new-peer-share) subsystem will use
	 `callBroadcastTransactionSet` to send new peers transactions that they may
	 be missing.

##### TODOs
 - TODO: Eventually we will be able to catalog incoming transactions. If a peer
   tells us about a transaction, they obviously have that transaction and we do
   not need to receive it again.
 - TODO: Splitting off this filter into its own subsystem makes it easy to split
   broadcasting strategy when we upgrade to broadcasting txids only instead of
   full transactions. The new broadcast strategy will send peers a list of txids
   and then the peer can determine whether or not to request the full
   transactions, saving network bandwidth. Or we can jump straight to erlay
   style set reconciliation.
