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
before modules were being broken down into subsystems. The core subsystem should
be broken down and separated out into new subsystems.

### New Peer Share
**Key Files**
 - [newpeershare.go](./newpeershare.go)
 - [newpeershareconsts.go](./newpeershareconsts.go)

The new peer share subsystem is responsible for tracking which peers siad is
connected to and sharing the transactions in the transaction pool with that
peer.

The subsystem has an independent thread which monitors the peers in the gateway
and spins up a new thread for each new peer that appears. That thread will take
a snapshot of all of the transaction objects currently in the tpool and queue
them up to be sent to the peer.

The transaction objects are tracked independently, however they are sent as
sets. An object is selected, and then the transaction set which contains that
object is sent to the peer. All of the objects in that transaction set will be
removed from the queue for the peer to receive. Objects are tracked instead of
transaction sets because the transaction sets in the pool are constantly
changing as blocks are found and as new transactions arrive. Tracking by
transaction object ensures that the peer receives all data, and that the peer is
also receiving up to date data. Transaction objects are used instead of
transactions or transaction IDs because the transaction pool has a direct
mapping from object ID to transaction, but does not have a mapping from
transaction to transaction set.

The new peer share subsystem will call `callBlockForShareTSet` before sharing a
transaction set with a new peer. This will allow the peer share limiter
subsystem to block for conditions such as waiting for the transaction pool to be
synced, and also will allow the peer share limiter to enforce a ratelimit on
sending transactions to new peers that prioritizes older peers over newer peers.

##### Outbound Complexities
 - `callRemainingObjectsList` from the [Core](#core) is used to fetch a snapshot
   of objects in the transaction pool after a new peer connects to the
   transaction pool. This list of objects is used to determine what pre-existing
   transaction sets the peer is potentially missing.
 - `callTSetByObjectID` from the [Core](#core) is used to fetch the transaction
   set associated with a particular object id while looking for pre-existing
   transaction sets to send to a new peer.
 - `callBlockForShareTSet` from the [Peer Share Limiter](#peer-share-limiter)
   will be used to ensure that a transaction set is not sent until the limiter
   believes that it is okay to send that transaction set.
 - `callRelayTransactionSet` from the [Repeat Broadcast
   Filter](#repeat-broadcast-filter) subsystem will be used to send transaction
   sets to new peers.

##### TODOs
 - TODO: The subsystem should not start sending transactions to a new peer until
   it knows that the peer has the most recent blocks on the network. This is to
   avoid sending the peer outdated transactions if the transaction pool is still
   catching up to the most recent block.
 - TODO: The subsystem ideally sends transaction sets to peers roughly in order
   of fee rate. This ensures that new peers get the most valuable transactions
   first and have the best idea for what sorts of fees are required to get into
   blocks. The peer share subsystem also ideally sends transactions in a
   semi-random order so that a new peer to the network is receiving different
   transactions from all of its peers instead of the same information over and
   over from each peer. Some amount of randomness is still desirable, so that
   redundant shares from peers will initially likely cover different
   transactions.
 - TODO: The peer share subsystem should have lowest priority when bumping up
   against the ratelimits on the gateway, other bandwidth such as new
   transaction broadcasting is more important. Also important is host and renter
   bandwidth. Once a good QoS strategy is implemented to prioritize different
   types of bandwidth, the ratelimit that is applied on sending new transactions
   to peers can be removed.

### Peer Share Limiter
**Key Files**
 - [peersharelimiter.go](./peersharelimiter.go)

The peer share limiter is responsible for limiting when the new peer share
subsystem is allowed to send transaction sets to peers. The limiter will gate
for factors such as the transaction pool being online and synced, and the
limiter will also enforce a ratelimit on sharing new transactions with peers.

The new peer share subsystem should call `callBlockForShareTSet`, which takes as
input the timestamp when the peer was discovered, and returns a channel that
must be used to report the size of the transaction set that will be sent.

The peer share limiter attempts to enforce a global, long term and burst
agnostic ratelimit on the new peer share subsystem. Ideally, transaction sets
can be shared with new peers at full speed, but then an amount of time is
allowed to pass between sending transaction sets to ensure that the long term
average bandwidth consumption is below the peer share ratelimit. If some peers
are particularly slow in receiving transactions, the peer share limiter would
like to unblock threads that are trying to send to other peers, so that slow
peers cannot suffocate other peers. Using a heap to select which peers to
unblock achieves these goals.

##### Inbound Complexities
 - `callBlockForShareTSet` is used by the [New Peer Share](#new-peer-share)
   subsystem to limit transactions being shared with new peers.

##### Outbound Complexities
 - `callTpoolSynced` is called from the [Core](#core) subsystem to determine
   whether or not the transaction pool is currently synced.

##### Other Complexities
 - The core subsystem has a dynamic set of transactions which changes as new
   blocks are found and new transactions are added. When the new peer share
   subsystem blocks before sending a transaction set to a peer, the ideal
   transaction set to send can change during the block. For that reason, the
   transaction set to send is not chosen until after the blocking is complete.
   The peer share limiter needs to know the size of the object that gets sent,
   this therefore needs to be communicated after the blocking is complete. This
   communication is performed using channels.

##### TODOs
 - TODO: Currently the ratelimit that the peer share limiter uses is a const
   that cannot be changed at runtime. An export should be created that allows
   the user to configure this value through the transaction pool API.
 - TODO: Instead of polling the gateway to see when peers are coming and going,
   the transaction pool should be getting some sort of subscription type
   notification from the gateway each time the peer set changes. This would
   allow the central goroutine to be removed and also allow transaction
   propagation to begin more quickly.

### Repeat Broadcast Filter
**Key Files**
 - [repeatbroadcastfilter.go](./repeatbroadcastfilter.go)

The repeat broadcast filter will track each transaction in the transaction pool,
remembering which peers have received that transaction before. This will prevent
the transaction pool from sending redundant information around the network. All
transactions broadcast from the tpool will first pass through the repeat
broadcast filter.

The main mechanism of action is to have a map that links from transaction id to
a map of peers. For each transaction, there is a map of which peers have
received that transaction already. When a new transaction is entered into the
transaction pool, a corresponding map is created for that transaction. And when
a transaction is removed from the transaction pool, the corresponding map is
deleted.

When choosing to broadcast a transaction set to a peer, the subsystem will check
what transactions of that set have already been sent to that peer, and exclude
any transactions that the peer already knows about, sending them only the new
information.

There's a special case for inserting transactions that are added to the pool
from reverted blocks. If a block is reverted, the subsystem will assume that all
of its peers already have that transaction since it was in a block that was
propagated, so it'll add those transactions assuming that all peers already have
the transaction.

##### Inbound Complexities
 - `callBroadcastTransactionSet` can be used to send transactions to peers.
   - The [Core](#core) subsystem will use `callBroadcastTransactionSet` for
	 general purpose transaction broadcasting and relaying
   - The [New Peer Share](#peer-share) subsystem will use
	 `callBroadcastTransactionSet` to send new peers transactions that they may
	 be missing.

##### TODOs
 - TODO: Eventually the subsystem will be able to catalog incoming transactions.
   If a peer tells us about a transaction, they obviously have that transaction
   and it doesn't need to be broadcast to them again.
 - TODO: The repeat broadcast filter suffers from shortcomings if peers are
   evicting transactions or rejecting transactions for having low fee rates,
   because they may evict what will eventually become ancestors, and then no
   longer be able to receive updates on that set. This could be problematic for
   child-pays-for-parent transactions. We need some way for a peer to realize
   that there are ancestors it can request without also opening up a DoS. I
   believe the solution would be to send peers the fee rate and full transaction
   size for both the new transactions and the set as a whole, which will allow
   the peer to evaluate whether the transaction set is worth requesting in full.
