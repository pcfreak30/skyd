# SIP-0003: RPC Payment Method

## Description

The host offers a list of RPC methods to transact with renters. Many of those
RPCs require payment as they involve forming or renewing new contracts, but also
downloading and uploading.

Payment can be made through file contracts, where the renter pays the host
essentially by moving money towards the host through a contract revision. This
form of payment method will be called 'PayByContract' from here on out.

Ephemeral accounts offer a new form of payment method. Ephemeral accounts are a
service offered by hosts that allow users to connect a balance to a pubkey.
Users can deposit funds into an ephemeral account with a host and then later use
the funds to transact with the host.

This proposal contains a library function which isolates all logic dealing with
extracting payment from an RPC. This function can get called by every bespoke
RPC and will process payment. It returns whether or not it was accepted and how
much money was paid.

## Motivation

Ephemeral accounts introduce an alternative method of payment. Alongside
PayByContract, hosts which offer ephemeral accounts as a service allow to pay
through an ephemeral account, which will be called 'PayByEphemeralAccount' from
here on out.

The payment process, which involves all request-response communication between a
host and a renter, will be identical across all RPCs that require payment. By
isolating it into a separate library function.

## Extract Payment For RPC

### Payment Methods

#### PayByContract

PayByContract is a form of payment method where the renter can pay the host by
using a file contract the renter and host share. To spend from the contract, the
renter has to supply an updated revision and his signature. The host will lock
the contract and verify the updated revision. The amount to be spent will be
implied by the revision. If the payment was successful, meaning the revision was
accepted by the host, the RPC will continue. If the host denies the revision the
RPC will abort.

Note that the contract id will be implied from the session. When forming a
session between a renter and a host, the contract id is specified. If the id is
blank or bad, we will return an error and the RPC will fail to complete.

#### PayByEphemeralAccount

PayByEphemeralAccount is a form of payment method where the renter can pay the
host by using an ephemeral account. This ephemeral account is located on the
host's side and ties the renter's pubkey to his balance. The renter can fund
this account and spend from it at a later point in time. In order to spend from
his ephemeral account, the renter makes up a withdrawal message containing all
necessary details. He then supplies the host with this message and a signature.

Withdrawal messages contain an expiry. This is the blockheight at which the
withdrawal message is no longer valid. This expiry can not be in the past, nor
can it be too far into the future. If a withdrawal message expires, the renter
can just make a new one.

Aside from an expiry, the message should also contain an account id, amount to
spend and a nonce. The nonce is an arbitrary number making the message unique.

To ensure a withdrawal message is only spent once, the host will keep track of
its fingerprint. The fingerprint is the hash of the withdrawal message. The host
holds on to these fingerprints until they expire. By doing so, the host prevents
replay attacks as the account owner won't be able to spend the same withdrawal
twice.

### RPC Price Table

The RPC price table is a list of prices, set by the host, that are kept on the
session between a renter and a host. This table contains a price for every RPC
the host offers. When a session is formed, the host will calculate his prices
and communicate those to the renter. These prices are only valid for a certain
amount of time, in practice the host will provide an expiry blockheight up until
which the offered prices remain valid.

Before the price table TTL expires, the renter must update the host's prices. It
does so by a separate RPC call. It has to do so because any RPC call made to the
host will fail if the host's price table is expired. In practice the renter will
do this frequently to avoid ever having to request a price update mid-action,
for example mid-download.

### Design

The code to extract payment for the RPC will live as a library function that can
be called by all bespoke RPCs that require a payment. The function is called
`extractPaymentForRPC` and returns whether or not the call got accepted, how
much was paid and an error in case it was not able to extract the payment. When
we were to extract payment the RPC will simply continue, if not it will abort.

The communication will happen through a set of request-response objects for both
PayByEphemeralAccount and PayByContract.

Note that the file contract id is not specified in these request-response
objects. This is because the contract id will be implied through the session.
When the renter forms a session with the host, the contract id will be set on
the session.

Note that the contract id specified on the session does not mean that contract
is locked. The RPCs that require interaction to that file contract will acquire
a lock over the duration of the RPC.

```Go
func extractPaymentForRPC(conn net.Conn) (accepted bool, amountPaid 
types.Currency, err error)

type WithdrawalMessage {
    Id      types.SiaPublicKey
    Expiry  types.BlockHeight
    Amount  types.Currency
    Nonce   int
}

type PayByEphemeralAccountRequest {
    Message     WithdrawalMessage
    Signature   crypto.Signature
}

type PayByEphemeralAccountResponse {
    Amount                 types.Currency
    AcceptRejectMessage    string
    AccountManagerResponse error
}

type PayByContractRequest { 
    Revision  types.FileContractRevision
    Signature crypto.Signature
}

type PayByContractResponse {
    Amount              types.Currency
    AcceptRejectMessage string
    Signature           crypto.Signature
}
```
