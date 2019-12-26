# SIP-0002 Ephemeral Accounts

## Description

Ephemeral accounts are a service offered by hosts that allow users to connect a
balance to a pubkey. Users can deposit funds into an ephemeral account with a
host and then later use the funds to transact with the host. The most common
transactions will be uploading and downloading data, however any RPC that
requires payment will support receiving payment from an ephemeral account.

Each host has a separate set of ephemeral accounts, and each host is fully
trusted to honestly track the balances of the accounts. Users who keep ephemeral
accounts with a host will be able to verify themselves that a host is honestly
reporting the balance of an ephemeral account, however users have no recourse if
a host chooses to steal all of the money in an ephemeral account or otherwise
deny that the account exists. For this reason, users should only keep tiny
balances in ephemeral accounts and users should refill the ephemeral accounts
frequently, even on the order of multiple times per minute.

## Motivation

The main purpose of ephemeral accounts is to eliminate a key bottleneck in
download latency. The advantage it offers is that we can front-load our latency.
If it takes 6 hops and 2.5 seconds to pay host X, we can perform that payment in
advance of needing to download anything from host X by opening an ephemeral
account. That way, when we actually want to download data from the host, we can
enjoy downloading from the host in a single round trip that only needs to
communicate with the host, and we save ourselves from needing to do the 6 hop
routing in real time.

## Ephemeral Account

An ephemeral account is a balance linked to a pubkey. These accounts reside on
the host. The account owner fully entrusts the money with the host, he has no
recourse at all if the host decides to steal the funds.

The name 'ephemeral account' stems from the fact that hosts maintain a relaxed
consistency and durability of these accounts. This effectively enables us to do
perform the I/O asynchronously to the transaction, instead of needing to commit
a change before completing the transaction. This eliminates a key bottleneck in
download latency, and also enables much greater parallelism on downloads,
allowing a speed and flexibility that cannot be achieved with traditional
payment channels.

### Ephemeral Account Expiry

If an account has been inactive for a longer period of time, the host will prune
it from the accounts list. This will effectively expire the account, along with
all the money that was associated to it. This period is configurable through the
`EphemeralAccountExpiry` setting on the host and defaults to 7 days.

Tracking the account consumes disk and cpu resources on the host. By expiring
accounts after a period of inactivity, the host has more control over how many
resources are consumed by ephemeral accounts.

### Ephemeral Account Maximum Balance

Accounts have a maximum balance. This is a safety measure that protects the
host. If the host were to go offline, it would lose part of its state. Renters
could pick up on this and attempt to double spend. By limiting the maximum
account balance we protect the total amount of money the host is on the hook
for.

The maximum ephemeral account balance is configurable through the internal host
setting called `MaxEphemeralAccountBalance`. It defaults to the siacoin
precision, which is how many base units fit into a single siacoin, which is
10^24.

### Ephemeral Account Withdrawal

In order to spend from the ephemeral account, the account owner will construct a
withdrawal message together with a signature. This way the host can verify the
sender of the message is the owner of the ephemeral account he wishes to spend
from.

```Go
type WithdrawalMessage {
    AccountID string
    Expiry    types.BlockHeight
    Amount    types.Currency
    Nonce     uint64
}
```

**AccountID** string  
The AccountID is the string representation of the ephemeral account's pubkey.

**Expiry** types.BlockHeight  
The expiry is the block height at which the withdrawal message is no longer
valid. This expiry block height can not be in the past, nor can it be too far
into the future. If a withdrawal message expires, the renter can just make a new
one.

**Amount** types.Currency  
The amount to be withdrawn from the ephemeral account.

**Nonce** uint64  
The nonce is an arbitrary number which makes the withdrawal message unique.

#### Fingerprint

To ensure a withdrawal message is only spent once, the host will keep track of
its fingerprint. The fingerprint is the hash of the withdrawal message. The host
holds on to these fingerprints until it is certain they have expired. By doing
so, the host prevents replay attacks as the account owner won't be able to spend
the same withdrawal twice. Note that if the renter wishes to make the same spend
multiple times, he can do so by using a different nonce for each withdrawal
message, but leaving the other fields the same. This will alter the hash of the
message, making it unique.

#### Blocking

Withdrawing from an ephemeral account is a potentially blocking action. It will
block if the account balance is insufficient to perform the withdrawal. This
block either expires after a timeout, or is lifted by a deposit that is large
enough to cover the blocked withdrawal. This is a latency optimisation and
allows to easily orchestrate calls that involve withdrawing from multiple hosts.
This will prove useful for forwarding payment.

The blocked withdrawals will be added to a queue. They are unblocked in the
order which is based on the priority that was specified when the caller
requested the withdrawal. The renter could for example pass in the current
timestamp as priority, when making the withdrawal. This would end up processing
the blocked withdrawals in a FIFO fashion.

## Persistence Model

The ephemeral account data is comprised of two main parts. On the one side you
have the account's pubkey and the balance, on the other we have a list of
fingerprints that were generated by withdrawals that happened it the past. Due
to their expiring nature, fingerprints should be kept separate from the account
data. This will have some performance benefits, as this means that fingerprints
can be saved using append-only file persistence.

All account balances are kept in a single accounts persist file. Because account
balances are so frequenty updated, we want to avoid having to update the entire
accounts file every time one of them is updated. For this reason, accounts are
fixed in size and get persisted to disk at a fixed location in the accounts
file. This allows to update an account's data without having to hold a lock over
the entire file.

The fingerprints are persisted to two separate files on disk. We will call these
buckets from now on, the 'current' and 'next' bucket. Because withdrawal message
have an expiry block height, fingerprints also expire and thus do not need to be
tracked after their expiry block height is reached. By keeping them in two
separate files on disk, we can easily purge an entire bucket of fingerprints in
constant time when the block height reaches the threshold blockheight of the
next bucket. See an example of this rotation [here](#withdrawal-message-expiry).

The current bucket contains all fingerprints which expire within the current
block period. This current block period is calculated by dividing the current
block height by the block range of the bucket, and rounding upward. The next
bucket will contain all fingerprints which expire with the next block period.
Anything that is outside of those two buckets is considered either too far into
the future or in the past.

Note that the list of fingerprints does not build up indefinitely because the
fingerprints are kept in two separate structures that rotate internally when the
blockchain reached a certain block height.

### Asynchronous Persist Of Withdrawals

To increase performance, the host will allow a user to withdraw from an
ephemeral without requiring the user to wait until it has persisted the
withdrawal to disk. This asynchronous form of persisting allows the user to
perform actions such as downloading with significantly less latency. This does
mean however that the host is at risk for all of the money it has yet to persist
to disk. If the host loses power at that exact moment, the host will forget that
the user has spent money and the user will be able to spend that money again.
The host can configure the amount of money he is willing to risk due to this
asynchronous persist model through the `MaxEphemeralAccountRisk` setting.

### Withdrawal Message Expiry

Whether a withdrawal message is considered expired or too far into the future
comes from how we persist them. If the expiry does not fall into either the
current or next bucket, we consider it to be invalid. The current block height
is used to validate if it is in the past, so if the current blockheight is 22,
any incoming withdrawal with an expiry less than 22 is considered invalid.

Example:  
If the buckets have a block range of 10 blocks, and current blockheight is 22,
the current block period is `[20-30)` and the next block period is `[30-40)`.
Withdrawal messages that expiry at block 40 or higher will be considered invalid
as it expires too far into the future.

If the current block height reaches the threshold block height of the next
bucket. The current bucket is pruned by replacing it with the contents of the
next bucket. The next bucket will be recreated.

```
B: 20 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40
         C
   [ - - - - current - - - - - - )[- - - - - next - - - - - - -)

B: 27 28 29 30 31 32 33 34 35 36 37 38 39 40 41 42 43 44 45 46 47
            C
 - current -)[- - - - - next - - - - - - -)
 - deleted -)[- - - - - current- - - - - -)[- - - - new next - -
```
