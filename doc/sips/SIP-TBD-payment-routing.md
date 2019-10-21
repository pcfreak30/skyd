# SIP-TBD Payment Routing

SIP-TBD is a description of a Payment Routing System between hosts in the Sia
Network. It outlines in detail the requirements for such a system and proposes a
series of logical [steps](#implementation-steps) that can be implemented through
separate MRs in order to successfully accomplish the goal outlined in the
description.

## Description

Payment routing is the act of routing a payment through potentially a series of
interconnected payment hubs. Routing payment effectively enables performing
actions on hosts with which you don't necessarily share an immediate payment
link. This opens up the door for hosts, renters, even non Sia users, to perform
actions on hosts.

## Motivation

Two of the most attractive use cases for Sia are filesharing and content
distribution. For both cases, a user will often be required to make a
micro payment to a host, where they don't have a preexisting payment channel
with that host. By implementing payment routing we enable anybody that has an
existing payment channel with a host, to make payment on behalf of somebody
else.

## Payment Hubs

A `PaymentHub` is a node that acts as a payment router in the network. Just as
hosts are incentivized to host their storage space, payment hubs are
incentivized to route payment in return for a fee.

In order to route payment these hubs will create file contracts that will serve
solely as a payment channel between two nodes. These file contracts won't keep
track of any data at all.

Becoming a payment hub will become possible through a setting in the host's
external settings. By adding it to the host's external settings we can piggy
back off of the existing host announcement infrastructure.

### Payment Hub Announcement

A `PaymentHubAnnouncement` is made by a host to indicate his willingness to
serve as a payment hub. Hosts will announce this through a setting. The current
code can easily be extended to support this new concept.

### Payment Hub Locator

The `PaymentHubLocator` is constantly monitoring consensus changes for these hub
announcements. Whenever a new host announces itself as payment hub, or an
existing host announces he is no longer willing to act as a hub, this will be
signaled to the routing subsystem. Most likely it will just be reflected in the
host database.

Here again, we will just extend the existing code that calls
'findHostAnnouncements' when a block is added to the consensus.

### Payment Destination List

The `PaymentDestinationList` is the list of nodes the payment hubs has an active
payment channel with. It comprises of a list of payment edges. Topology
builders, further described [here](#topology-builder), use this list to build up
the network topology.

This call will be an addition to the current RPC set.

## Payment Edge

A `PaymentEdge` describes a payment channel between two payment hubs. This
channel is ideally a two-way payment channel however this is not necessarily
required. This payment channel is nothing more than a FileContract which we will
use to move funds back and forth between the two parties in the contract.

These 'payment contracts' would essentially ignore the necessity to provide
proof of storage and simply have a merkle root of zero. The two participants in
the channel can move money around by signing off on contract revisions.

### Payment Edge Refresher

The `PaymentEdgeRefresher` is a background thread on a payment hub. If the
capacity of one of its payment edges gets below a certain pre configured
threshold on either side, it will refresh the contract by making an on-chain
transaction.

### Payment Edge Builder

The `PaymentEdgeBuilder` is responsible for constructing the payment channel
between two payment hubs. Currently, file contracts only allow one-way payments
to be made through a file contract. Code will need to be written in order to
support the behaviour of two-way payments through a file contract, as up until
now it only supported one-way payment from renter to the host.

### Payment Edge Pricer

Creating and maintaining these payment channels comes at a cost. This cost will
need to be fully recuperable through fees taken on the payment routing if the
payment network is to be successful and have proper incentivization structures.

The `PaymentEdgePricer` is responsible for pricing these payment edges, seeing
as the price is probably dependant on a couple of factors. In theory however it
should be possible to use fixed prices, although that might prove to be not very
viable as it'll be hard to be competitive with payment hubs that offer dynamic
pricing.

#### Payment Edge Fee

The fee structure is currently still undecided. Other networks apply varying fee
structures. Some factors come into play when designing this fee structure, such
as channel capacity, channel depletion, channel balance.

It should be possible for a host to manipulate his fee price. In that regard it
is good idea to have the notion of a base fee that is configurable through the
host settings. Just as hosts currently can set fixed prices for up - or
download bandwith.

Seeing as a depleted channel requires renewal or refreshing a contract, it might
make sense to charge more if you want to route a payment through a channel that
will get depleted as the result of it.

#### Negative Fees

In a network of payment channels it is possible for channels to become
unbalanced. Negative fees are a way to incentivize users to rebalance these
channels by routing payments through them. Payment hubs might incentivize users
this way to push their unbalanced channels from 95% to 75%, avoiding contract
renewal.

Structuring these negative fees might prove tricky. If a channel is oscillating
between these limits, say 75% - 95%, the hub must be making money. The
`PaymentEdgePricer` will have to mathematically ensure the hub always wins, also
when its only traffic is oscillating between these negative <-> positive fee
ranges.

Negative fees also bring with it the problem of negative weight cycles in
graphs. Those negative weight cycles pose a big issue to shortest path
algorithms, seeing as the shortest path becomes negative infinity as you can
keep relaxing the path. Bellman-Ford can detect these negative weight cycles but
is not capable of finding a shortest path when they are present in the network
graph. [This paper](#http://people.math.sfu.ca/~goddyn/Courses/800/Resources/GraphMisc/short_path.pdf)
gives a possible solution to this in Section #2.

## Payment Network Rebalancer

The `PaymentNetworkRebalancer` is a worker that can be run by anyone. It can be
run by a payment hub but it might as well be a thrid party that runs such a
service. It would do this because it is financially incentivized to do so.

The payment network rebalancer will scan the entire payment network and look for
negative fee routes. It will then route money through those routes and collect a
fee for doing so.

The payment network rebalancer offers a service to the network as a whole in the
sense that it makes the network healthier by rebalancing the capacity.

## Ephemeral Accounts

Ephemeral accounts are basically balances that are kept off-chain, by the
payment hubs. These accounts can be used as payment method to **all existing
payable RPC requests we already support**. It is important to note that these
accounts are fully entrusted with the host.

Instead of all payments being required to come from either a siacoin input or by
being paid through the file contract, we will add an option to spend from an
ephemeral account. In order to instead accept an outsourced payment from such an
ephemeral account the sender must supply a signature from the pubkey for that
account.

The host can at any point in time go offline and run away with the money you
have entrusted with it. We protect ourselves by using packetized payments, the
amounts of money we entrust with a host are very small. Usually not more than
the price of a partial download. In theory however these accounts could hold as
much as the funder is willing to put at stake.

Seeing as these ephemeral accounts can be used as a payment method, it will
become trivial to tell hosts to forward payments to other hosts. This forms the
basis of payment routing and enables pre-payment of actions in behalf of
somebody else.

I, Alice, could share my private key with Bob. I would then prefund money on
that account and forward it through to the appropriate host. Bob can then
contact said host and perform actions on it that require payment, seeing as he
is the owner of a pre-funded ephemeral account and can prove this ownership. Bob
could also just send me a public key to which he holds the private key.

## Routing

The most important part of a payment channel network is its routing algorithm.
Existing payment channel routing protocols have two main hurdles to overcome.
First, they attempt to route payment in an atomic fashion. Second, they fail to
keep payment channels balanced.

We plan to side-step these two main problems by having packetized payments, and
keep the payments very small w/regards to average channel capacity. Further more
we will not have atomic payments, in the sense that if one intermediary host
drops out, the money is "stuck" at the hop prior to that one.

At all times the money in transit is completely at stake. It is a trusted setup
in that sense, as every node along the route can take the money and run. We
trust hosts are incentivized not to do so, and they will get penalized for it if
they do.

### Routing Table

The routing table, maintained by the `TopologyBuilder`, can be built up by using
the information collected by the `PaymentHubLocator`. It is a set of medium-long
term static information about the network topology.

Besides keeping a list of every payment edge in the network it will also store
additional semi-static information about these payment channels. In particular
their capacity and fee, but it could also contain performance metrics such as
latency or keep scores of successful historic transactions.

That information is semi-static, that means that essentially it is dynamic and
variable to change, however we assume for now that it will remain static over a
period of hours or even days. This makes it possible to route payment with
accurate fee estimates, which is necessary if you want to be able to make
payments of an exact amount.

We expect that the size of this routing table will initially be rather small.
We estimate the amount of vertices to be around 100, and the amount of edges to
be around 10000. That means that algorithms that run in O(|V| . |E|) would
complete in a reasonable amount of time.

### Routing Algorithm

In order to route a payment to a certain endpoint one must first find a route to
that endpoint. This is done through a routing algorithm and is a topic that has
been well studied for decades. Please note though that we are dealing with a
distributed shortest path which brings a couple of difficulties with it.

That said, the current state of the art distributed routing algorithms like
VOUTE, SilentWhispers or SpeedyMurmurs all solve problems the Sia network does
not have. This is why at least in the MVP the routing algorithm of choice will
probably be a form of distributed Bellman Ford.

When talking about routing algorithms with edges of a certain capacity the term
'flow' comes to mind. There are algorithms that are designed to route packets
through such a network ensuring that the route along which you send it has
enough capacity. Ford-Fulkerson is an example of an algorithm that optimizes for
maximum flow.

Due to the fact that we have packetized payments, and that these payments are
usually orders of magnitude smaller than a channel's capacity, we are going to
ignore flow entirely. We can pretty much safely assume that if a route exist, it
will have enough capacity left to route our payment through. In the off chance
that is not the case our algorithm will fall back and will try to route the
payment through a secondary route and retry.

#### Clamshell Routing

Clamshell routing means that the sender of the payment is orchestrating the
forward along the entire route. This means that if it is going to route a
payment along a route with 2 landmark routers, it will talk to them directly and
request to forward payment. It will instruct each intermediary to forward the
payment onto the next hop.

This is necessary because otherwise we can not tell which host betrayed us by
not forwarding the payment. We would have no way of knowing which host was
malicious.

Another benefit is that it becomes trivial to swap out the routing algorithm for
another. Because the only thing that needs to change is how the router
structures the clam shell. The primitives for moving money around will remain
the same.

#### Accountability Manager

The `AccountabilityMananger` will properly handle a failed routing request. It
can try and reroute the payment and possibly penalize the node that failed to
properly complete the request.

## RPC

This section will provide an overview with the required changes to the RPC set.
Both changes to the already existing RPCs as well as new additions are
discussed.

Payable RPCs, such as `FormNewContract` or `Read` (which is essentially
download), currently implement a form of direct payment. This direct payment is
done by exchanging money through updating and signing a contract revision. All
the RPCs which currently support this "direct payment method" could
eventually also support the new "ephemeral payment method".

Note that an RPC with an ephemeral account as payment method which is not
pre-funded will block until the account holds enough funds to successfully
complete the RPC. This blocked request will expire and resolve in failure after
a certain timeout.

- `listPaymentDestinations`  
  This RPC will return all the possible payment destinations a payment hub
  offers. The result of this request is aggregated across multiple payment hubs
  and thus forms network topology. The `TopologyBuilder` does this.

- `fundEphemeralAccount`  
  This RPC funds an ephemeral account on a host. It requires an amount of SC
  that needs to be pre-funded as well as a contract id. This RPC requires payment
  as it effectively credits money to an account.

- `forwardEphemeralPayment`  
  This RPC instructs to fund an ephemeral account on another host. It requires
  an IP and the necessary payment details such as amount, contract id and
  payment code or account number.

### Payment Routing Example

The following is a graphical overview of the requests necessary to forward a
payment in a multi-hop payment route. In the diagram below we assume the renter
is trying to download from H3 however he only has an active contract with H1.

Please note that although the steps listed are represented sequentially, the
underlying system will be built in such a way that the renter does not need to
perform these calls in any particular order. Instead, the renter will fire off
all of these requests concurrently.

We assume the renter has a background thread running a `TopologyBuilder` and
basically maintaining an up-to-date view of the entire network topology. That
process has presented us with the most ideal route to H3 which goes over H1 to
H2 eventually ending up at H3.

H1 and H2 are essentially shown as hosts but could very well be payment hubs and
nothing else. H3 however has to be a host seeing as he has the data we want to
download off of it.

1. Renter sends a `download` RPC request to H3 **specifying an ephemeral account
   ID as payment method**. (Please note that 'download RPC' is a simplification
   of the download process as no such RPC actually exists but rather is
   comprised of multiple reads.)

   If the given ephemeral account was pre-funded the RPC requests will not be
   blocking at all but instead immediately resolve. Much quicker than currently
   the case because the pre-payment will have eliminated the need for a contract
   update which entails locking and fsync'ing.

2. Renter sends a `forwardEphemeralPayment` RPC request to H2, again it
   specifies an ephemeral account ID as payment method. This can go either way,
   if the host in question knows about the account and the account balance is
   sufficient to forward the request amount of money it will forward that money
   by means of a `fundEphemeralAccount` request immediately to the destination
   node. Otherwise this call will block until the account is created and/or
   sufficient funds are credited into the account

3. Renter sends a `forwardEphemeralPayment` RPC request to H1, here however it
   specifies direct pay as payment method. It can do this because it knows H1
   and is able to pay it through an active contract they share.

   This call is non blocking and immediately returns. It requires the renter to
   supply a revised contract that will get verified and ultimately accepted by
   H1.

4. H1 will forward payment by calling `fundEphemeralAccount` on H2

5. H2 will forward payment by calling `fundEphemeralAccount` on H3. This action
   will effectively unlock the first download RPC request which was blocking.
   The ephemeral account should now hold enough balance to fulfill the download
   request.

![Screenshot](../assets/paymentrouting.png)

## Privacy Goals

## Implementation Steps

### MR1: PaymentHub Announcements

Description:

Add the notion of a payment hub. Hosts can signal they are willing to act as a
payment hub through their host settings. Payment hubs can be used to route
payment through. They will advertise all hosts they have payment channels with.

Tasks:

- Extend HostSettings w/payment hub option
- Extend HostDBEntry w/payment hub option (probably implied through hES)

Dependencies:

- /

### MR2: Form Payment Contract (PaymentEdgeBuilder)

Description:

The payment edge builder will construct the payment channel between two payment
hubs. The payment channel is constructed by forming a file contract between two
hosts without any data in it.

This MR will only add code that deals with forming these new type of contracts.
Other functinalities like pricing, advertising, listing will be tackled later.

Tasks:

- Add code to form payment contract w/another host
  (`managedRPCFormPaymentContract`)

Dependencies:

- Notion of payment hub, forming payment contracts is only allowed with a host
  that has this setting enabled

Notes:

- The concept of PaymentEdgeBuilder might be a bit much, if it's exposed through
  an RPC we can drop the term entirely

### MR3: PaymentEdgePricer

Description:

The PaymentEdgePricer determines the cost to use the payment channel. The price
is dependent on a couple of factors, which may or may not include:

- base fee (?)
- liquidity fee (?)
- channel capacity
- channel latency
- channel imbalance (inbound/outbound capacity)
- contract refresh/formatino costs

The eventually determined price to route payment through a payment channel will
always consist out of two parts, the inbound and outbound cost.

Consider the following setup  
`R1 -> PH1 <-> PH2 -> H1`

If R1 wants to route payment to H1, he can use PH1 and PH2 to route payment
through. To traverse the channel between PH1 and PH2, the fee consists of the
outbound fee set by PH1 plus the inbound fee set by PH2.

Tasks:

- Add payment edge pricing logic

Dependencies:

- Payment Contract formation (see MR2)

Design (WIP):

```Go
func determinePaymentEdgePrice(
    costToOpenChannel,
    costToRefreshChannel,
    remainingInboundCapacity,
    remainingOutboundCapacity types.Currency
) (costOfInboundPayment, costOfOutboundPayment types.Currency)
```

### MR4: PaymentEdgeDestinationList

Description:

In this MR we will add the list route which advertises the payment edge
destinations of a payment hub. This will be used by the `RouteBuilder` to form a
topology of the overall network, to decide how to route the payment.

Tasks:

- Expose function that lists all payment edges

Questions:

- Do we build the list on demand or periodically?
- Do we reprice based off of triggers or on request?

Design (WIP):

```Go
// PaymentEdge defines a link between two nodes through which a payment can be
// routed. It contains the identifier of the destination node and edge pricing
// details
type PaymentEdge struct {
    destination         types.SiaPublicKey
    inboundCost         types.Currency
    outboundCost        types.Currency
    capacity            types.Currency
    latency             time.Duration
}

Host interface {
    PaymentEdgeDestinations() []types.PaymentEdge
}
```

### MR5: Add Ephemeral Accounts

Description:

Ephemeral accounts are a new form of payment method. They are kept off-chain and
are managed by the hosts. They keep track of a balance. See the section on
[ephemeral accounts](#ephemeral-accounts) for more info.

We will extend all payable RPCs to be payable by ephemeral account

Tasks:

- Add notion of ephemeral account
- Add an ephemeral account manager
- Ensure accounts are persisted
- Enable account funding through direct payment (as initial step)

Dependencies:

- /

Questions:

- Do we add ephemeral accounts to the host's persistence object?

Design (WIP):

```Go
    // EphemeralAccount represents an account which is kept off-chain, and which
    // can be used as a form of payment method. Every payable RPC can be paid
    // through direct payment or through such an ephemeral account payment
    EphemeralAccount struct {
        Balance types.Currency
        PubKey  types.SiaPublicKey
    }

    // EphemeralAccountManager is responsible for managing all of the ephemeral
    // accounts that might be present on a host. In order to fund such an ac
    EphemeralAccountManager struct {
        accounts    map[string]EphemeralAccount
        mu          sync.Mutex
    }

    // RPC Request and method on the host
    func (h *Host) managedRPCLoopFundEphemeralAccount(s *rpcSession) error

    type LoopFundEphemeralAccountRequest struct {
        // Payment details
        ContractID  types.FileContractID
        account     types.SiaPublicKey
        amount      types.Currency

        // Contract details
        NewRevisionNumber    uint64
        NewValidProofValues  []types.Currency
        NewMissedProofValues []types.Currency

        // Contains challenge signed by the ephemeral account owner's public key
        Signature []byte
    }

```

### MR6: Add Ephemeral Account Payment Method

Description:

Every RPC that requires some form of payment needs to be altered/extended to be
able to pay through form of an ephemeral account. This payment method might be
blocking if the account is not funded enough to perform the RPC. This blocking
action is protected by a timeout after which the RPC fails.

Tasks:

- /

Dependencies:

- Ephmeral Accounts

### MR6: Add Forward Ephemeral Payment

Description:

Extend the current RPC set with a forward payment method. This RPC can be either
paid directly or funded by an ephemeral account.

Tasks:

- Add `forwardEphemeralPayment` RPC

Dependencies:

- Ephmeral Accounts
