---
title: Skyd API Documentation

language_tabs: # must be one of https://git.io/vQNgJ
  - go

toc_footers:
  - <a href='https://siasky.net'>The Official Skynet Website
  - <a href='https://gitlab.com/SkynetLabs/skyd'>Skyd on GitLab</a>

search: true
---

# Introduction

## Welcome to the Skyd API!
> Example GET curl call 

```go
curl -A "Sia-Agent" "localhost:9980/accounting?start=1234&end=5678"
```

> Example POST curl call with data

```go
curl -A "Sia-Agent" -u "":<apipassword> --data "period=12096&renewwindow=4032&funds=1000&hosts=50" "localhost:9980/renter"
```

> Example POST curl call without data or authentication

```go
curl -A "Sia-Agent" -X POST "localhost:9980/gateway/connect/123.456.789.0:9981"
```

Skyd uses semantic versioning and is backwards compatible to Sia version v1.0.0.
The Siad API can be found [here](https://sia.tech/docs).

API calls return either JSON or no content. Success is indicated by 2xx HTTP
status codes, while errors are indicated by 4xx and 5xx HTTP status codes. If an
endpoint does not specify its expected status code refer to [standard
responses](#Standard-Responses).

There may be functional API calls which are not documented. These are not
guaranteed to be supported beyond the current release, and should not be used in
production.

**Notes:**

- Requests must set their User-Agent string to contain the substring
  "Sia-Agent".
- By default, siad listens on "localhost:9980". This can be changed using the
  `--api-addr` flag when running siad.
- **Do not bind or expose the API to a non-loopback address unless you are aware
  of the possible dangers.**

## Documentation Standards

The following details the documentation standards for the API endpoints.

 - Endpoints should follow the structure of:
    - Parameters
    - Response
 - Each endpoint should have a corresponding curl example
   - For formatting there needs to be a newline between `> curl example` and the
     example
 - All non-standard responses should have a JSON Response example with units
   - For formatting there needs to be a newline between `> JSON Response
     Example` and the example
 - There should be detailed descriptions of all JSON response fields
 - There should be detailed descriptions of all query string parameters
 - Query String Parameters should be separated into **REQUIRED** and
   **OPTIONAL** sections
 - Detailed descriptions should be structured as "**field** | units"
   - For formatting there needs to be two spaces after the units so that the
     description is on a new line
 - All code blocks should specify `go` as the language for consistent formatting

Contributors should follow these standards when submitting updates to the API
documentation.  If you find API endpoints that do not adhere to these
documentation standards please let the Skyd team know by submitting an issue
[here](https://gitlab.com/SkynetLabs/skyd/issues)

# Standard Responses

## Success
The standard response indicating the request was successfully processed is HTTP
status code `204 No Content`. If the request was successfully processed and the
server responded with JSON the HTTP status code is `200 OK`. Specific endpoints
may specify other 2xx status codes on success.

## Error

```go
{
    "message": String

    // There may be additional fields depending on the specific error.
}
```

The standard error response indicating the request failed for any reason, is a
4xx or 5xx HTTP status code with an error JSON object describing the error.

### Module Not Loaded

A module that is not reachable due to not being loaded by siad will return
the custom status code `490 ModuleNotLoaded`. This is only returned during
startup. Once the startup is complete and the module is still not available,
ModuleDisabled will be returned.

### Module Disabled

A module that is not reachable due to being disabled, will return the custom
status code `491 ModuleDisabled`.

# Authentication
> Example POST curl call with Authentication

```go
curl -A "Sia-Agent" -u "":<apipassword> --data "period=12096&renewwindow=4032&funds=1000&hosts=50" "localhost:9980/renter"
```

API authentication is enabled by default, using a password stored in a flat
file. The location of this file is:

 - Linux:   `$HOME/.sia/apipassword`
 - MacOS:   `$HOME/Library/Application Support/Sia/apipassword`
 - Windows: `%LOCALAPPDATA%\Sia\apipassword`


Note that the file contains a trailing newline, which must be trimmed before
use.

Authentication is HTTP Basic Authentication as described in [RFC
2617](https://tools.ietf.org/html/rfc2617), however, the username is the empty
string. The flag does not enforce authentication on all API endpoints. Only
endpoints that expose sensitive information or modify state require
authentication.

For example, if the API password is "foobar" the request header should include

`Authorization: Basic OmZvb2Jhcg==`

And for a curl call the following would be included

`--user "":<apipassword>`

Authentication can be disabled by passing the `--authenticate-api=false` flag to
siad. You can change the password by modifying the password file, setting the
`SIA_API_PASSWORD` environment variable, or passing the `--temp-password` flag
to siad.

# Units

Unless otherwise noted, all parameters should be identified in their smallest
possible unit. For example, size should always be specified in bytes and
Siacoins should always be specified in hastings. JSON values returned by the API
will also use the smallest possible unit, unless otherwise noted.

If a number is returned as a string in JSON, it should be treated as an
arbitrary-precision number (bignum), and it should be parsed with your
language's corresponding bignum library. Currency values are the most common
example where this is necessary.

# Environment Variables
There are a number of environment variables supported by siad and siac.

 - `SIA_API_PASSWORD` is the environment variable that sets a custom API
   password if the default is not used
 - `SIA_DATA_DIR` is the environment variable that tells siad where to put the
   general sia data, e.g. api password, configuration, logs, etc.
 - `SIAD_DATA_DIR` is the environment variable that tells siad where to put the
   siad-specific data
 - `SIA_WALLET_PASSWORD` is the environment variable that can be set to enable
   auto unlocking the wallet
 - `SIA_EXCHANGE_RATE` is the environment variable that can be set (e.g. to
   "0.00018 mBTC") to extend the output of some siac subcommands when displaying
   currency amounts

# Accounting

The accounting endpoints provide some basic accounting information for the sia
node.

## /accounting [GET]
> curl example

```go
curl -A "Sia-Agent" "localhost:9980/accounting?start=1234&end=5678"
```

Returns basic accounting information for the modules that are running.

### Query String Parameters
### OPTIONAL
**start, end** | int64\
Unix timestamp of the range of accounting information to request. The default
value for `end` is `math.MaxInt64`. If neither param is provided, then the
entire history will be returned. 

### JSON Response
> JSON Response Example

```go
{
  [
    {
      "renter": {
        "unspentunallocated": "990000000000000000000000000", // hastings, big int
        "withheldfunds":      "0", // hastings, big int
      },
      "wallet": {
        "confirmedsiacoinbalance": "3365276858974358974358974358950" // hastings, big int
        "confirmedsiafundbalance": "0" // siafunds, big int
      },
      "timestamp": 0123456789 // unix timestamp
    }
  ]
}
```

**renter** | RenterAccounting\
Basic accounting information about the renter module.

**unspentunallocated** | hastings, big int\
The amount of funds currently tied up in the current period contracts that have
not been allocated for upload, download, or storage spending.

**withheldfunds** | hastings, big int\
The amount of funds currently tied up in expired contracts that have not been
released yet.

**wallet** | WalletAccounting\
Basic accounting information about the wallet module.

**confirmedsiacoinbalance** | hastings, big int\
Number of siacoins, in hastings, available to the wallet as of the most recent
block in the blockchain.

**confirmedsiafundbalance** | big int\
Number of siafunds available to the wallet as of the most recent block in the
blockchain.

**timestamp** | unix timestamp\
Unix timestamp of when the accounting information was recorded.

# Daemon

The daemon is responsible for starting and stopping the modules which make up
the rest of Sia.

## /daemon/alerts [GET]
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/daemon/alerts"
```

Returns all alerts of all severities of the Sia instance sorted by severity from highest to lowest in `alerts` and the alerts of the Sia instance sorted by category in `criticalalerts`, `erroralerts` and `warningalerts`.

### JSON Response
> JSON Response Example
 
```go
{
    "alerts": [
    {
      "cause": "wallet is locked",
      "msg": "user's contracts need to be renewed but a locked wallet prevents renewal",
      "module": "contractor",
      "severity": "warning",
    }
  ],
  "criticalalerts": [],
  "erroralerts": [],
  "warningalerts": [
    {
      "cause": "wallet is locked",
      "msg": "user's contracts need to be renewed but a locked wallet prevents renewal",
      "module": "contractor",
      "severity": "warning",
    }
  ]
}
```
**cause** | string  
Cause is the cause for the information contained in msg if known.

**msg** | string  
Msg contains information about an issue.

**module** | string  
Module is the module which caused the alert.

**severity** | string  
Severity is either "warning", "error" or "critical" where "error" might be a
lack of internet access and "critical" would be a lack of funds and contracts
that are about to expire due to that.

## /daemon/constants [GET]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/daemon/constants"
```

Returns the some of the constants that the Skynet daemon uses. 

### JSON Response
> JSON Response Example
 
```go
{
  "blockfrequency":600,           // blockheight
  "blocksizelimit":2000000,       // uint64
  "extremefuturethreshold":18000, // timestamp
  "futurethreshold":10800,        // timestamp
  "genesistimestamp":1433600000,  // timestamp
  "maturitydelay":144,            // blockheight
  "mediantimestampwindow":11,     // uint64
  "siafundcount":"10000",         // uint64
  "siafundportion":"39/1000",     // big.Rat
  "targetwindow":1000,            // blockheight
  
  "initialcoinbase":300000, // uint64
  "minimumcoinbase":30000,  // uint64
  
  "roottarget": // target
  [0,0,0,0,32,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0],
  "rootdepth":  // target
  [255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255,255],
  
  "defaultallowance":  // allowance
    {
      "funds":"250000000000000000000000000000",  // currency
      "hosts":50,                       // uint64
      "period":12096,                   // blockheight
      "renewwindow":4032,               // blockheight
      "expectedstorage":1000000000000,  // uint64
      "expectedupload":2,               // uint64
      "expecteddownload":1,             // uint64
      "expectedredundancy":3            // uint64
    },
  
  "maxtargetadjustmentup":"5/2",    // big.Rat
  "maxtargetadjustmentdown":"2/5",  // big.Rat
  
  "siacoinprecision":"1000000000000000000000000"  // currency
}
```
**blockfrequency** | blockheight  
BlockFrequency is the desired number of seconds that should elapse, on average,
between successive Blocks.

**blocksizelimit** | uint64  
BlockSizeLimit is the maximum size of a binary-encoded Block that is permitted
by the consensus rules.

**extremefuturethreshold** | timestamp  
ExtremeFutureThreshold is a temporal limit beyond which Blocks are discarded by
the consensus rules. When incoming Blocks are processed, their Timestamp is
allowed to exceed the processor's current time by a small amount. But if the
Timestamp is further into the future than ExtremeFutureThreshold, the Block is
immediately discarded.

**futurethreshold** | timestamp  
FutureThreshold is a temporal limit beyond which Blocks are discarded by the
consensus rules. When incoming Blocks are processed, their Timestamp is allowed
to exceed the processor's current time by no more than FutureThreshold. If the
excess duration is larger than FutureThreshold, but smaller than
ExtremeFutureThreshold, the Block may be held in memory until the Block's
Timestamp exceeds the current time by less than FutureThreshold.

**genesistimestamp** | timestamp  
GenesisBlock is the first block of the block chain

**maturitydelay** | blockheight  
MaturityDelay specifies the number of blocks that a maturity-required output is
required to be on hold before it can be spent on the blockchain. Outputs are
maturity-required if they are highly likely to be altered or invalidated in the
event of a small reorg. One example is the block reward, as a small reorg may
invalidate the block reward. Another example is a siafund payout, as a tiny
reorg may change the value of the payout, and thus invalidate any transactions
spending the payout. File contract payouts also are subject to a maturity delay.

**mediantimestampwindow** | uint64  
MedianTimestampWindow tells us how many blocks to look back when calculating the
median timestamp over the previous n blocks. The timestamp of a block is not
allowed to be less than or equal to the median timestamp of the previous n
blocks, where for Sia this number is typically 11.

**siafundcount** | currency  
SiafundCount is the total number of Siafunds in existence.

**siafundportion** | big.Rat  
SiafundPortion is the percentage of siacoins that is taxed from FileContracts.

**targetwindow** | blockheight  
TargetWindow is the number of blocks to look backwards when determining how much
time has passed vs. how many blocks have been created. It's only used in the
old, broken difficulty adjustment algorithm.

**initialcoinbase** | uint64  
InitialCoinbase is the coinbase reward of the Genesis block.

**minimumcoinbase** | uint64  
MinimumCoinbase is the minimum coinbase reward for a block. The coinbase
decreases in each block after the Genesis block, but it will not decrease past
MinimumCoinbase.

**roottarget** | target  
RootTarget is the target for the genesis block - basically how much work needs
to be done in order to mine the first block. The difficulty adjustment algorithm
takes over from there.

**rootdepth** | target  
RootDepth is the cumulative target of all blocks. The root depth is essentially
the maximum possible target, there have been no blocks yet, so there is no
cumulated difficulty yet.

**defaultallowance** | allowance  
DefaultAllowance is the set of default allowance settings that will be used when
allowances are not set or not fully set. See [/renter GET](#renter-get) for an
explanation of the fields.

**maxtargetadjustmentup** | big.Rat  
MaxTargetAdjustmentUp restrict how much the block difficulty is allowed to
change in a single step, which is important to limit the effect of difficulty
raising and lowering attacks.

**maxtargetadjustmentdown** | big.Rat  
MaxTargetAdjustmentDown restrict how much the block difficulty is allowed to
change in a single step, which is important to limit the effect of difficulty
raising and lowering attacks.

**siacoinprecision** | currency  
SiacoinPrecision is the number of base units in a siacoin. The Sia network has a
very large number of base units. We call 10^24 of these a siacoin.

## /daemon/settings [GET]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/daemon/settings"
```
Returns the settings for the daemon

### JSON Response
> JSON Response Example
 
```go
{
  "maxdownloadspeed": 0,  // bytes per second
  "maxuploadspeed":   0,  // bytes per second
  "modules": { 
    "consensus":       true,  // bool
    "explorer":        false, // bool
    "gateway":         true,  // bool
    "host":            true,  // bool
    "miner":           true,  // bool
    "renter":          true,  // bool
    "transactionpool": true,  // bool
    "wallet":          true   // bool

  } 
}
```

**maxdownloadspeed** | bytes per second  
Is the maximum download speed that the daemon can reach. 0 means there is no
limit set.

**maxuploadspeed** | bytes per second  
Is the maximum upload speed that the daemon can reach. 0 means there is no limit
set.

**modules** | struct  
Is a list of the siad modules with a bool indicating if the module was launched.

## /daemon/stack [GET]
**UNSTABLE**
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/daemon/stack"
```
Returns the daemon's current stack trace. The maximum buffer size that will be
returned is 64MB. If the stack trace is larger than 64MB the first 64MB are
returned.

### JSON Response
> JSON Response Example
 
```go
{
  "stack": [1,2,21,1,13,32,14,141,13,2,41,120], // []byte
}
```

**stack** | []byte  
Current stack trace. 

## /daemon/settings [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> --data "maxdownloadspeed=1000000&maxuploadspeed=20000" "localhost:9980/daemon/settings"
```

Modify settings that control the daemon's behavior.

### Query String Parameters
### OPTIONAL
**maxdownloadspeed** | bytes per second  
Max download speed permitted in bytes per second  

**maxuploadspeed** | bytes per second  
Max upload speed permitted in bytes per second  

### Response
standard success or error response. See [standard
responses](#standard-responses).

## /daemon/stop [GET]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/daemon/stop"
```

Cleanly shuts down the daemon. This may take a few seconds.

### Response
standard success or error response. See [standard
responses](#standard-responses).

## /daemon/update [GET]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/daemon/update"
```
Returns the the status of any updates available for the daemon

### JSON Response
> JSON Response Example
 
```go
{
  "available": false, // boolean
  "version": "1.4.0"  // string
}
```

**available** | boolean  
Available indicates whether or not there is an update available for the daemon.

**version** | string  
Version is the version of the latest release.

## /daemon/update [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/daemon/update"
```
Updates the daemon to the latest available version release.

### Response
standard success or error response. See [standard
responses](#standard-responses).

## /daemon/version [GET]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/daemon/version"
```

Returns the version of the Skynet daemon currently running. 

### JSON Response
> JSON Response Example
 
```go
{
"version": "1.3.7" // string
}
```
**version** | string  
This is the version number that is visible to its peers on the network.

# Host DB

The hostdb maintains a database of all hosts known to the network. The database
identifies hosts by their public key and keeps track of metrics such as price.

## /hostdb [GET]
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/hostdb"
```

Shows some general information about the state of the hostdb.

### JSON Response
> JSON Response Example
 
```go
{
    "initialscancomplete": false  // boolean
}
```
**initialscancomplete** | boolean  
indicates if all known hosts have been scanned at least once.

## /hostdb/active [GET]
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/hostdb/active"
```

lists all of the active hosts known to the renter, sorted by preference.

### Query String Parameters
### OPTIONAL
**numhosts** | int  
Number of hosts to return. The actual number of hosts returned may be less if
there are insufficient active hosts. Optional, the default is all active hosts.


### JSON Response
> JSON Response Example
 
```go
{
  "hosts": [
        {
      "acceptingcontracts":     true,                 // boolean
      "maxdownloadbatchsize":   17825792,             // bytes
      "maxduration":            25920,                // blocks
      "maxrevisebatchsize":     17825792,             // bytes
      "netaddress":             "123.456.789.0:9982"  // string 
      "remainingstorage":       35000000000,          // bytes
      "sectorsize":             4194304,              // bytes
      "totalstorage":           35000000000,          // bytes
      "unlockhash": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab", // hash
      "windowsize":             144,                            // blocks
      "collateral":             "20000000000"                   // hastings / byte / block
      "maxcollateral":          "1000000000000000000000000000"  // hastings
      "contractprice":          "1000000000000000000000000"     // hastings
      "downloadbandwidthprice": "35000000000000"                // hastings / byte
      "storageprice":           "14000000000"                   // hastings / byte / block
      "uploadbandwidthprice":   "3000000000000"                 // hastings / byte
      "revisionnumber":         12733798,                       // int
      "version":                "1.3.4"                         // string
      "firstseen":              160000,                         // blocks
      "historicdowntime":       0,                              // nanoseconds
      "historicuptime":         41634520900246576,              // nanoseconds
      "scanhistory": [
        {
          "success": true,  // boolean
          "timestamp": "2018-09-23T08:00:00.000000000+04:00"  // unix timestamp
        },
        {
          "success": true,  // boolean
          "timestamp": "2018-09-23T06:00:00.000000000+04:00"  // unix timestamp
        },
        {
          "success": true,  // boolean// boolean
          "timestamp": "2018-09-23T04:00:00.000000000+04:00"  // unix timestamp
        }
      ],
      "historicfailedinteractions":     0,      // int
      "historicsuccessfulinteractions": 5,      // int
      "recentfailedinteractions":       0,      // int
      "recentsuccessfulinteractions":   0,      // int
      "lasthistoricupdate":             174900, // blocks
      "ipnets": [
        "1.2.3.0",  // string
        "2.1.3.0"   // string
      ],
      "lastipnetchange": "2015-01-01T08:00:00.000000000+04:00", // unix timestamp
      "publickey": {
        "algorithm": "ed25519", // string
        "key":       "RW50cm9weSBpc24ndCB3aGF0IGl0IHVzZWQgdG8gYmU=" // string
      },
      "publickeystring": "ed25519:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",  // string
      "filtered": false, // boolean
    }
  ]
}
```

**hosts**  
**acceptingcontracts** | boolean  
true if the host is accepting new contracts.  

**maxdownloadbatchsize** | bytes  
Maximum number of bytes that the host will allow to be requested by a single
download request.  

**maxduration** | blocks  
Maximum duration in blocks that a host will allow for a file contract. The host
commits to keeping files for the full duration under the threat of facing a
large penalty for losing or dropping data before the duration is complete. The
storage proof window of an incoming file contract must end before the current
height + maxduration.  

There is a block approximately every 10 minutes. e.g. 1 day = 144 blocks  

**maxrevisebatchsize** | bytes  
Maximum size in bytes of a single batch of file contract revisions. Larger batch
sizes allow for higher throughput as there is significant communication overhead
associated with performing a batch upload.  

**netaddress** | string  
Remote address of the host. It can be an IPv4, IPv6, or hostname, along with the
port. IPv6 addresses are enclosed in square brackets.  

**remainingstorage** | bytes  
Unused storage capacity the host claims it has.  

**sectorsize** | bytes  
Smallest amount of data in bytes that can be uploaded or downloaded to or from
the host.  

**totalstorage** | bytes  
Total amount of storage capacity the host claims it has.  

**unlockhash** | hash  
Address at which the host can be paid when forming file contracts.  

**windowsize** | blocks  
A storage proof window is the number of blocks that the host has to get a
storage proof onto the blockchain. The window size is the minimum size of window
that the host will accept in a file contract.  

**collateral** | hastings / byte / block  
The maximum amount of money that the host will put up as collateral for storage
that is contracted by the renter.  

**maxcollateral** | hastings  
The maximum amount of collateral that the host will put into a single file
contract.  

**contractprice** | hastings  
The price that a renter has to pay to create a contract with the host. The
payment is intended to cover transaction fees for the file contract revision and
the storage proof that the host will be submitting to the blockchain.  

**downloadbandwidthprice** | hastings / byte  
The price that a renter has to pay when downloading data from the host.  

**storageprice** | hastings / byte / block  
The price that a renter has to pay to store files with the host.  

**uploadbandwidthprice** | hastings / byte  
The price that a renter has to pay when uploading data to the host.  

**revisionnumber** | int  
The revision number indicates to the renter what iteration of settings the host
is currently at. Settings are generally signed. If the renter has multiple
conflicting copies of settings from the host, the renter can expect the one with
the higher revision number to be more recent.  

**version** | string  
The version of the host.  

**firstseen** | blocks  
Firstseen is the last block height at which this host was announced.  

**historicdowntime** | nanoseconds  
Total amount of time the host has been offline.  

**historicuptime** | nanoseconds  
Total amount of time the host has been online.  

**scanhistory** Measurements that have been taken on the host. The most recent
measurements are kept in full detail.  

**historicfailedinteractions** | int  
Number of historic failed interactions with the host.  

**historicsuccessfulinteractions** | int Number of historic successful
interactions with the host.  

**recentfailedinteractions** | int  
Number of recent failed interactions with the host.  

**recentsuccessfulinteractions** | int  
Number of recent successful interactions with the host.  

**lasthistoricupdate** | blocks  
The last time that the interactions within scanhistory have been compressed into
the historic ones.  

**ipnets**  
List of IP subnet masks used by the host. For IPv4 the /24 and for IPv6 the /54
subnet mask is used. A host can have either one IPv4 or one IPv6 subnet or one
of each. E.g. these lists are valid: [ "IPv4" ], [ "IPv6" ] or [ "IPv4", "IPv6"
]. The following lists are invalid: [ "IPv4", "IPv4" ], [ "IPv4", "IPv6", "IPv6"
]. Hosts with an invalid list are ignored.  

**lastipnetchange** | date  
The last time the list of IP subnet masks was updated. When equal subnet masks
are found for different hosts, the host that occupies the subnet mask for a
longer time is preferred.  

**publickey** | SiaPublicKey  
Public key used to identify and verify hosts.  

**algorithm** | string  
Algorithm used for signing and verification. Typically "ed25519".  

**key** | hash  
Key used to verify signed host messages.  

**publickeystring** | string  
The string representation of the full public key, used when calling
/hostdb/hosts.  

**filtered** | boolean  
Indicates if the host is currently being filtered from the HostDB

## /hostdb/all [GET]
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/hostdb/all"
```

Lists all of the hosts known to the renter. Hosts are not guaranteed to be in
any particular order, and the order may change in subsequent calls.

### JSON Response 
Response is the same as [`/hostdb/active`](#hosts)

## /hostdb/hosts/:*pubkey* [GET]
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/hostdb/hosts/ed25519:8a95848bc71e9689e2f753c82c35dc47a1d62867f77c0113ebb6fa5b51723215"
```

fetches detailed information about a particular host, including metrics
regarding the score of the host within the database. It should be noted that
each renter uses different metrics for selecting hosts, and that a good score on
in one hostdb does not mean that the host will be successful on the network
overall.

### Path Parameters
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/hostdb/hosts/<pubkey>"
```
### REQUIRED
**pubkey**  
The public key of the host. Each public key identifies a single host.  

Example Pubkey:
ed25519:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef  

### JSON Response 
> JSON Response Example
 
```go
{
  "entry": {
    // same as hosts
  },
  "scorebreakdown": {
    "score":                      1,        // big int
    "acceptcontractadjustment":   1,        // float64
    "ageadjustment":              0.1234,   // float64
    "basepriceadjustment":        1,        // float64
    "burnadjustment":             0.1234,   // float64
    "collateraladjustment":       23.456,   // float64
    "conversionrate":             9.12345,  // float64
    "durationadjustment":         1,        // float64
    "interactionadjustment":      0.1234,   // float64
    "priceadjustment":            0.1234,   // float64
    "storageremainingadjustment": 0.1234,   // float64
    "uptimeadjustment":           0.1234,   // float64
    "versionadjustment":          0.1234,   // float64
  }
}
```
Response is the same as [`/hostdb/active`](#hosts) with the additional of the
**scorebreakdown**

**scorebreakdown**  
A set of scores as determined by the renter. Generally, the host's final score
is all of the values multiplied together. Modified renters may have additional
criteria that they use to judge a host, or may ignore certin criteia. In
general, these fields should only be used as a loose guide for the score of a
host, as every renter sees the world differently and uses different metrics to
evaluate hosts.  

**score** | big int  
The overall score for the host. Scores are entriely relative, and are consistent
only within the current hostdb. Between different machines, different
configurations, and different versions the absolute scores for a given host can
be off by many orders of magnitude. When displaying to a human, some form of
normalization with respect to the other hosts (for example, divide all scores by
the median score of the hosts) is recommended.  

**acceptcontractadjustment** | float64  
The multiplier that gets applied to the host based on whether its accepting contracts or not. Typically "1" if they do and "0" if they don't.

**ageadjustment** | float64  
The multiplier that gets applied to the host based on how long it has been a
host. Older hosts typically have a lower penalty.  

**basepriceadjustment** | float64  
The multiplier that gets applied to the host based on if the `BaseRPCPRice` and
the `SectorAccessPrice` are reasonable.  

**burnadjustment** | float64  
The multiplier that gets applied to the host based on how much proof-of-burn the
host has performed. More burn causes a linear increase in score.  

**collateraladjustment** | float64  
The multiplier that gets applied to a host based on how much collateral the host
is offering. More collateral is typically better, though above a point it can be
detrimental.  

**conversionrate** | float64  
conversionrate is the likelihood that the host will be selected by renters
forming contracts.  

**durationadjustment** | float64  
The multiplier that gets applied to a host based on the max duration it accepts
for file contracts. Typically '1' for hosts with an acceptable max duration, and
'0' for hosts that have a max duration which is not long enough.

**interactionadjustment** | float64  
The multiplier that gets applied to a host based on previous interactions
with the host. A high ratio of successful interactions will improve this
hosts score, and a high ratio of failed interactions will hurt this hosts
score. This adjustment helps account for hosts that are on unstable
connections, don't keep their wallets unlocked, ran out of funds, etc.  

**pricesmultiplier** | float64  
The multiplier that gets applied to a host based on the host's price. Lower
prices are almost always better. Below a certain, very low price, there is no
advantage.  

**storageremainingadjustment** | float64  
The multiplier that gets applied to a host based on how much storage is
remaining for the host. More storage remaining is better, to a point.  

**uptimeadjustment** | float64  
The multiplier that gets applied to a host based on the uptime percentage of the
host. The penalty increases extremely quickly as uptime drops below 90%.  

**versionadjustment** | float64  
The multiplier that gets applied to a host based on the version of Sia that they
are running. Versions get penalties if there are known bugs, scaling
limitations, performance limitations, etc. Generally, the most recent version is
always the one with the highest score.  

## /hostdb/filtermode [GET]
> curl example  

```go
curl -A "Sia-Agent" --user "":<apipassword> "localhost:9980/hostdb/filtermode"
```  
Returns the current filter mode of the hostDB and any filtered hosts.

### JSON Response 
> JSON Response Example
 
```go
{
  "filtermode": "blacklist",  // string
  "hosts":
    [
      "ed25519:122218260fb74b20a8be3000ad56a931f7461ea990a6dc5676c31bdf65fc668f"  // string
    ]
}

```
**filtermode** | string  
Can be either whitelist, blacklist, or disable.  

**hosts** | array of strings  
Comma separated pubkeys.  

## /hostdb/filtermode [POST]
> curl example  

```go
curl -A "Sia-Agent" --user "":<apipassword> --data '{"filtermode" : "whitelist","hosts" : ["ed25519:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef","ed25519:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef","ed25519:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"]}' "localhost:9980/hostdb/filtermode"
```  
```go
curl -A "Sia-Agent" --user "":<apipassword> --data '{"filtermode" : "disable"}' "localhost:9980/hostdb/filtermode"
```
Lets you enable and disable a filter mode for the hostdb. Currently the two
modes supported are `blacklist` mode and `whitelist` mode. In `blacklist` mode,
any hosts you identify as being on the `blacklist` will not be used to form
contracts. In `whitelist` mode, only the hosts identified as being on the
`whitelist` will be used to form contracts. In both modes, hosts that you are
blacklisted will be filtered from your hostdb. To enable either mode, set
`filtermode` to the desired mode and submit a list of host pubkeys as the
corresponding `blacklist` or `whitelist`. To disable either list, the `host`
field can be left blank (e.g. empty slice) and the `filtermode` should be set to
`disable`.  

**NOTE:** Enabling and disabling a filter mode can result in changes with your
current contracts with can result in an increase in contract fee spending. For
example, if `blacklist` mode is enabled, any hosts that you currently have
contracts with that are also on the provide list of `hosts` will have their
contracts replaced with non-blacklisted hosts. When `whitelist` mode is enabled,
contracts will be replaced until there are only contracts with whitelisted
hosts. Even disabling a filter mode can result in a change in contracts if there
are better scoring hosts in your hostdb that were previously being filtered out.


### Query String Parameters
### REQUIRED
**filtermode** | string  
Can be either whitelist, blacklist, or disable.  

**hosts** | array of string  
Comma separated pubkeys.  

### Response

standard success or error response. See [standard
responses](#standard-responses).


# Renter

The renter manages the user's files on the network. The renter's API endpoints
expose methods for managing files on the network and managing the renter's
allocated funds.

## /renter [GET]
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/renter"
```

Returns the current settings along with metrics on the renter's spending.

### JSON Response
> JSON Response Example
 
```go
{
{
  "settings": {
    "allowance": {
      "funds": "1000000000000000000000000000",  // hastings
      "hosts": 5,                               // int   
      "period": 50,                             // blocks
      "renewwindow": 24,                        // blocks
      "paymentcontractinitialfunding": "0",     // hastings
      "expectedstorage": 20480000,              // uint64
      "expectedupload": 2048000,                // uint64
      "expecteddownload": 2048000,              // uint64
      "expectedredundancy": 5,                  // float64
      "maxperiodchurn": 2048000,                // uint64
      "maxrpcprice": "0",                       // hastings
      "maxcontractprice": "0",                  // hastings
      "maxdownloadbandwidthprice": "0",         // hastings
      "maxsectoraccessprice": "0"               // hastings
      "maxstorageprice": "0",                   // hastings
      "maxuploadbandwidthprice": "0"            // hastings
    },
    "ipviolationcheck": true, // bool
    "maxuploadspeed": 0,      // uint64
    "maxdownloadspeed": 0,    // uint64
    "uploadsstatus": {
      "paused": false,                          // bool
      "pauseendtime": "0001-01-01T00:00:00Z"    // time
    }
  },
  "financialmetrics": {
    "contractfees": "1797134052977777761550000",        // hastings
    "downloadspending": "0",                            // hastings
    "fundaccountspending": "3000000000000000000000000", // hastings
    "maintenancespending": {
      "accountbalancecost": "3",                        // hastings
      "fundaccountcost": "3",                           // hastings
      "updatepricetablecost": "3"                       // hastings
    },
    "storagespending": "0",                             // hastings
    "totalallocated": "30000000000000000000000000",     // hastings
    "uploadspending": "0",                              // hastings
    "unspent": "995202865947022222238449991",           // hastings
    "contractspending": "30000000000000000000000000",   // hastings
    "withheldfunds": "0",                               // hastings
    "releaseblock": 0,                                  // blockheight
    "previousspending": "0"                             // hastings
  },
  "currentperiod": 17,                                  // blockheight
  "nextperiod": 67,                                     // blockheight
  "memorystatus":
    "available": 491520                 // uint64
    "base": 491520,                     // uint64
    "requested": 0,                     // uint64
    "priorityavailable": 524288,        // uint64
    "prioritybase": 524288,             // uint64
    "priorityrequested": 0,             // uint64
    "priorityreserve": 32768,           // uint64
    "registry": {                     
      "available": 131072,              // uint64
      "base": 131072,                   // uint64
      "requested": 0,                   // uint64
      "priorityavailable": 131072,      // uint64
      "prioritybase": 131072,           // uint64
      "priorityrequested": 0,           // uint64
      "priorityreserve": 0              // uint64
    },
    "userupload": {
      "available": 131072,              // uint64
      "base": 131072,                   // uint64
      "requested": 0,                   // uint64
      "priorityavailable": 131072,      // uint64
      "prioritybase": 131072,           // uint64
      "priorityrequested": 0,           // uint64
      "priorityreserve": 0              // uint64
    },
    "userdownload": {
      "available": 131072,              // uint64
      "base": 131072,                   // uint64
      "requested": 0,                   // uint64
      "priorityavailable": 131072,      // uint64
      "prioritybase": 131072,           // uint64
      "priorityrequested": 0,           // uint64
      "priorityreserve": 0              // uint64
    },
    "system": {
      "available": 98304,               // uint64
      "base": 98304,                    // uint64
      "requested": 0,                   // uint64
      "priorityavailable": 131072,      // uint64
      "prioritybase": 131072,           // uint64
      "priorityrequested": 0,           // uint64
      "priorityreserve": 32768          // uint64
    }
  }
}
```
**settings**    
Settings that control the behavior of the renter.  

**allowance**   
Allowance dictates how much the renter is allowed to spend in a given period.
Note that funds are spent on both storage and bandwidth.  

**funds** | hastings  
Funds determines the number of siacoins that the renter will spend when forming
contracts with hosts. The renter will not allocate more than this amount of
siacoins into the set of contracts each billing period. If the renter spends all
of the funds but then needs to form new contracts, the renter will wait until
either until the user increase the allowance funds, or until a new billing
period is reached. If there are not enough funds to repair all files, then files
may be at risk of getting lost.

**hosts** | int  
Hosts sets the number of hosts that will be used to form the allowance. Sia
gains most of its resiliancy from having a large number of hosts. More hosts
will mean both more robustness and higher speeds when using the network, however
will also result in more memory consumption and higher blockchain fees. It is
recommended that the default number of hosts be treated as a minimum, and that
double the default number of default hosts be treated as a maximum.

**period** | blocks  
The period is equivalent to the billing cycle length. The renter will not spend
more than the full balance of its funds every billing period. When the billing
period is over, the contracts will be renewed and the spending will be reset.

**renewwindow** | blocks  
The renew window is how long the user has to renew their contracts. At the end
of the period, all of the contracts expire. The contracts need to be renewed
before they expire, otherwise the user will lose all of their files. The renew
window is the window of time at the end of the period during which the renter
will renew the users contracts. For example, if the renew window is 1 week long,
then during the final week of each period the user will renew their contracts.
If the user is offline for that whole week, the user's data will be lost.

Each billing period begins at the beginning of the renew window for the previous
period. For example, if the period is 12 weeks long and the renew window is 4
weeks long, then the first billing period technically begins at -4 weeks, or 4
weeks before the allowance is created. And the second billing period begins at
week 8, or 8 weeks after the allowance is created. The third billing period will
begin at week 20.

**expectedstorage** | bytes  
Expected storage is the amount of storage that the user expects to keep on the
Sia network. This value is important to calibrate the spending habits of siad.
Because Sia is decentralized, there is no easy way for siad to know what the
real world cost of storage is, nor what the real world price of a siacoin is. To
overcome this deficiency, siad depends on the user for guidance.

If the user has a low allowance and a high amount of expected storage, siad will
more heavily prioritize cheaper hosts, and will also be more comfortable with
hosts that post lower amounts of collateral. If the user has a high allowance
and a low amount of expected storage, siad will prioritize hosts that post more
collateral, as well as giving preference to hosts better overall traits such as
uptime and age.

Even when the user has a large allowance and a low amount of expected storage,
siad will try to optimize for saving money; siad tries to meet the users storage
and bandwidth needs while spending significantly less than the overall
allowance.

**expectedupload** | bytes  
Expected upload tells siad how many bytes per block the user expects to upload
during the configured period. If this value is high, siad will more strongly
prefer hosts that have a low upload bandwidth price. If this value is low, siad
will focus on metrics other than upload bandwidth pricing, because even if the
host charges a lot for upload bandwidth, it will not impact the total cost to
the user very much.

The user should not consider upload bandwidth used during repairs, siad will
consider repair bandwidth separately.

**expecteddownload** | bytes  
Expected download tells siad how many bytes per block the user expects to
download during the configured period. If this value is high, siad will more
strongly prefer hosts that have a low download bandwidth price. If this value is
low, siad will focus on metrics other than download bandwidth pricing, because
even if the host charges a lot for downloads, it will not impact the total cost
to the user very much.

The user should not consider download bandwidth used during repairs, siad will
consider repair bandwidth separately.

**expectedredundancy** | bytes  
Expected redundancy is used in conjunction with expected storage to determine
the total amount of raw storage that will be stored on hosts. If the expected
storage is 1 TB and the expected redundancy is 3, then the renter will calculate
that the total amount of storage in the user's contracts will be 3 TiB.

This value does not need to be changed from the default unless the user is
manually choosing redundancy settings for their file. If different files are
being given different redundancy settings, then the average of all the
redundancies should be used as the value for expected redundancy, weighted by
how large the files are.

**maxuploadspeed** | bytes per second  
MaxUploadSpeed by default is unlimited but can be set by the user to manage
bandwidth.  

**maxdownloadspeed** | bytes per second  
MaxDownloadSpeed by default is unlimited but can be set by the user to manage
bandwidth.  

**streamcachesize** | int  
The StreamCacheSize is the number of data chunks that will be cached during
streaming.  

**financialmetrics**    
Metrics about how much the Renter has spent on storage, uploads, and downloads.

**contractfees** | hastings  
Amount of money spent on contract fees, transaction fees and siafund fees.  

**contractspending** | hastings, (deprecated, now totalallocated)  
How much money, in hastings, the Renter has spent on file contracts, including
fees.  

**downloadspending** | hastings  
Amount of money spent on downloads.  

**fundaccountspending** | hastings  
Amount of money spent on funding an ephemeral account on a host. This value
reflects the exact amount that got deposited into the account, meaning it
excludes the cost of the actual funding RPC, which is contained in the
maintenance spending metrics.

**maintenancespending**  
Amount of money spent on maintenance, such as updating price tables or syncing
the ephemeral account balance with the host.  

**accountbalancecost** | hastings  
Amount of money spent on syncing the renter's account balance with the host.

**fundaccountcost** | hastings  
Amount of money spent on funding the ephemeral account. Note that this is only
the cost of executing the RPC, the amount of money that is transferred into the
account is being tracked in the `fundaccountspending` field.

**updatepricetablecost** | hastings  
Amount of money spent on updating the price table with the host.

**storagespending** | hastings  
Amount of money spend on storage.  

**totalallocated** | hastings  
Total amount of money that the renter has put into contracts. Includes spent
money and also money that will be returned to the renter.  

**uploadspending** | hastings  
Amount of money spent on uploads.  

**unspent** | hastings  
Amount of money in the allowance that has not been spent.  

**currentperiod** | blockheight  
Height at which the current allowance period began.  

**nextperiod** | blockheight  
Height at which the next allowance period began.  

**memorystatus**   
Information about the state of the renter's internal memory manager.  

**registry**  
Memory information related to skynet registry operations.  

**userupload**  
Memory information related to user-initiated uploads.  

**userdownload**  
Memory information related to user-initiated downloads.  

**system**  
Memory information related to daemon-initiated tasks. e.g. repair uploads and downloads.  

**available** | uint64  
The amount of currently available for a given category.  

**base** | uint64  
The base amount of memory for a given category.  

**requested** | uint64  
The amount of memory currently requested for a given category.  

**priorityavailable** | uint64  
The amount of available priority memory for a given category.  

**prioritybase** | uint64  
The base amount of priority memory for a given category.  

**priorityrequested** | uint64  
The amount of priority memory currently requested for a given category.  

**priorityreserve** | uint64  
The amount of memory set aside for priority tasks.  

**uploadsstatus**  
Information about the renter's uploads.  

**paused** | boolean  
Indicates whether or not the uploads and repairs are paused.  

**pauseendtime** | unix timestamp  
The time at which the pause will end.  

## /renter [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> --data "period=12096&renewwindow=4032&funds=1000&hosts=50" "localhost:9980/renter"
```

Modify settings that control the renter's behavior.

### Query String Parameters
### REQUIRED
When setting the allowance the Funds and Period are required. Since these are
the two required fields, the allowance can be canceled by submitting the zero
values for these fields.

### OPTIONAL
Any of the renter settings can be set, see fields [here](#settings)

**checkforipviolation** | boolean  
Enables or disables the check for hosts using the same ip subnets within the
hostdb. It's turned on by default and causes Sia to not form contracts with
hosts from the same subnet and if such contracts already exist, it will
deactivate the contract which has occupied that subnet for the shorter time.  

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/allowance/cancel [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword>  "localhost:9980/renter/allowance/cancel"
```

Cancel the Renter's allowance.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/bubble [POST]
> curl example  

```go
// Call recursive bubble for non root directory
curl -A "Sia-Agent" -u "":<apipassword> --data "siapath=home/user/folder&recursive=true"  "localhost:9980/renter/bubble"

// Call force bubble on the root directory
curl -A "Sia-Agent" -u "":<apipassword> --data "rootsiapath=true&force=true"  "localhost:9980/renter/bubble"
```

Manually trigger a bubble update for a directory. This will update the
directory metadata for the directory as well as all parent directories.
Updates to sub directories are dependent on the parameters.

### Query String Parameters
### REQUIRED
One of the following is required. Both **CANNOT** be used at the same time.

**siapath** | string\
The path to the directory that is to be bubbled. All paths should be relative
to the renter's root filesystem directory.

**rootsiapath** | boolean\
Indicates if the bubble is intended for the root directory, ie `/renter/fs/`.
If provided, no `siapath` should be provided.

### OPTIONAL
**force** | boolean\
Indicates if the bubble should only update out of date directories. If `force`
is true, all directories will be updated even if they have a recent
`LastHealthCheckTime`.

**recursive** | boolean\
Indicates if the bubble should also be called on all subdirectories of the
provided directory. **NOTE** it is not recommend to manually the bubble entire
filesystem, i.e. calling this endpoint recursively from the root directory.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/clean [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword>  "localhost:9980/renter/clean"
```

clears any lost files from the renter. A lost file is a file that is viewed as
unrecoverable. A file is unrecoverable when there is not a local copy on disk
and the file's redundancy is less than 1. This means the file can not be
repaired.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/contract/cancel [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> --data "id=bd7ef21b13fb85eda933a9ff2874ec50a1ffb4299e98210bf0dd343ae1632f80" "localhost:9980/renter/contract/cancel"
```

cancels a specific contract of the Renter.

### Query String Parameters
### REQUIRED
**id** | hash  
ID of the file contract

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/backup [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> --data "destination=/home/backups/01-01-1968.backup" "localhost:9980/renter/backup"
```

Creates a backup of all siafiles in the renter at the specified path.

### Query String Parameters
### REQUIRED
**destination** | string  
The path on disk where the backup will be created. Needs to be an absolute path.

### OPTIONAL
**remote** | boolean  
flag indicating if the backup should be stored on hosts. If true,
**destination** is interpreted as the backup's name, not its path.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/recoverbackup [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> --data "source=/home/backups/01-01-1968.backup" "localhost:9980/renter/recoverbackup"
```

Recovers an existing backup from the specified path by adding all the siafiles
contained within it to the renter. Should a siafile for a certain path already
exist, a number will be added as a suffix. e.g. 'myfile_1.sia'

### Query String Parameters
### REQUIRED
**source** | string  
The path on disk where the backup will be recovered from. Needs to be an
absolute path.

### OPTIONAL
**remote** | boolean  
flag indicating if the backup is stored on hosts. If true, **source** is
interpreted as the backup's name, not its path.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/uploadedbackups [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/renter/uploadedbackups"
```

Lists the backups that have been uploaded to hosts.

### JSON Response
> JSON Response Example
 
```go
[
  {
    "name": "foo",                             // string
    "UID": "00112233445566778899aabbccddeeff", // string
    "creationdate": 1234567890,                // Unix timestamp
    "size": 8192                               // bytes
  }
]
```
**name** | string  
The name of the backup.

**UID** | string  
A unique identifier for the backup.

**creationdate** | string  
Unix timestamp of when the backup was created.

**size** Size in bytes of the backup.

## /renter/contracts [GET]
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/renter/contracts?disabled=true&expired=true&recoverable=false"
```

Returns the renter's contracts. Active, passive, and refreshed contracts are
returned by default. Active contracts are contracts that the Renter is currently
using to store, upload, and download data. Passive contracts are contracts that
are no longer GoodForUpload but are GoodForRenew. This means the data will
continue to be available to be downloaded from. Refreshed contracts are
contracts that ran out of funds and needed to be renewed so more money could be
added to the contract with the host. The data reported in these contracts is
duplicate data and should not be included in any accounting. Disabled contracts
are contracts that are in the current period and have not yet expired that are
not being used for uploading as they were replaced instead of renewed. Expired
contracts are contracts with an `EndHeight` in the past, where no more data is
being stored and excess funds have been released to the renter. Expired
Refreshed contracts are contracts that were refreshed at some point in a
previous period. The data reported in these contracts is duplicate data and
should not be included in any accounting. Recoverable contracts are contracts
which the contractor is currently trying to recover and which haven't expired
yet.

| Type              | GoodForUpload | GoodForRenew | Endheight in the Future | Data Counted Elsewhere Already|
| ----------------- | :-----------: | :----------: | :---------------------: | :---------------------------: |
| Active            | Yes           | Yes          | Yes                     | No                            |
| Passive           | No            | Yes          | Yes                     | No                            |
| Refreshed         | No            | No           | Yes                     | Yes                           |
| Disabled          | No            | No           | Yes                     | No                            |
| Expired           | No            | No           | No                      | No                            |
| Expired Refreshed | No            | No           | No                      | Yes                           |

**NOTE:** No spending is double counted anywhere in the contracts, only the data
is double counted in the refreshed contracts. For spending totals in the current
period, all spending in active, passive, refreshed, and disabled contracts
should be counted. For data totals, the data in active and passive contracts is
the total uploaded while the data in disabled contracts is wasted uploaded data.

### Query String Parameters
### OPTIONAL
**disabled** | boolean  
flag indicating if disabled contracts should be returned.

**expired** | boolean  
flag indicating if expired contracts should be returned.

**recoverable** | boolean  
flag indicating if recoverable contracts should be returned.

### JSON Response
> JSON Response Example
 
```go
{
  "activecontracts": [
    {
      "downloadspending": "1234",    // hastings
      "endheight":        50000,     // block height
      "fees":             "1234",    // hastings
      "fundaccountspending": "1234", // hastings
      "hostpublickey": {
        "algorithm": "ed25519",   // string
        "key": "RW50cm9weSBpc24ndCB3aGF0IGl0IHVzZWQgdG8gYmU=" // hash
      },
      "hostversion":      "1.4.0",  // string
      "id": "1234567890abcdef0123456789abcdef0123456789abcdef0123456789abcdef", // hash
      "lasttransaction": {},                // transaction
      "maintenancespending": {
        "accountbalancecost":   "1234", // hastings
        "fundaccountcost":      "1234", // hastings
        "updatepricetablecost": "1234", // hastings
      },
      "netaddress":       "12.34.56.78:9",  // string
      "renterfunds":      "1234",           // hastings
      "size":             8192,             // bytes
      "startheight":      50000,            // block height
      "storagespending":  "1234",           // hastings
      "totalcost":        "1234",           // hastings
      "uploadspending":   "1234"            // hastings
      "goodforupload":    true,             // boolean
      "goodforrenew":     false,            // boolean
      "badcontract":      false,            // boolean
    }
  ],
  "passivecontracts": [],
  "refreshedcontracts": [],
  "disabledcontracts": [],
  "expiredcontracts": [],
  "expiredrefreshedcontracts": [],
  "recoverablecontracts": [],
}
```
**downloadspending** | hastings  
Amount of contract funds that have been spent on downloads.  

**fundaccountspending** | hastings  
Amount of money spent on funding an ephemeral account on a host. This value
reflects the exact amount that got deposited into the account, meaning it
excludes the cost of the actual funding RPC, which is contained in the
maintenance spending metrics.

**endheight** | block height  
Block height that the file contract ends on.  

**fees** | hastings  
Fees paid in order to form the file contract.  

**hostpublickey** | SiaPublicKey  
Public key of the host that the file contract is formed with.  
       
**hostversion** | string  
The version of the host. 

**algorithm** | string  
Algorithm used for signing and verification. Typically "ed25519".  

**key** | hash  
Key used to verify signed host messages.  

**id** | hash  
ID of the file contract.  

**lasttransaction** | transaction  
A signed transaction containing the most recent contract revision.  

**maintenancespending**  
Amount of money spent on maintenance, such as updating price tables or syncing
the ephemeral account balance with the host.  

**accountbalancecost** | hastings  
Amount of money spent on syncing the renter's account balance with the host.

**fundaccountcost** | hastings  
Amount of money spent on funding the ephemeral account. Note that this is only
the cost of executing the RPC, the amount of money that is transferred into the
account is being tracked in the `fundaccountspending` field.

**updatepricetablecost** | hastings  
Amount of money spent on updating the price table with the host.

**netaddress** | string  
Address of the host the file contract was formed with.  

**renterfunds** | hastings  
Remaining funds left for the renter to spend on uploads & downloads.  

**size** | bytes  
Size of the file contract, which is typically equal to the number of bytes that
have been uploaded to the host.

**startheight** | block height  
Block height that the file contract began on.  

**storagespending** | hastings  
Amount of contract funds that have been spent on storage.  

**totalcost** | hastings  
Total cost to the wallet of forming the file contract. This includes both the
fees and the funds allocated in the contract.  

**uploadspending** | hastings  
Amount of contract funds that have been spent on uploads.  

**goodforupload** | boolean  
Signals if contract is good for uploading data.  

**goodforrenew** | boolean  
Signals if contract is good for a renewal.  

**badcontract** | boolean  
Signals whether a contract has been marked as bad. A contract will be marked as
bad if the contract does not make it onto the blockchain or otherwise gets
double spent. A contract can also be marked as bad if the host is refusing to
acknowldege that the contract exists.

## /renter/contractstatus [GET]
> curl example

```go
curl -A "Sia-Agent" "localhost:9980/renter/contractstatus?id=<filecontractid>"
```

### Query String Parameters
**id** | hash
ID of the file contract

### JSON Response
> JSON Response Example

```go
{
  "archived":                  true, // boolean
  "formationsweepheight":      1234, // block height
  "contractfound":             true, // boolean
  "latestrevisionfound",       55,   // uint64
  "storageprooffoundatheight": 0,    // block height
  "doublespendheight":         0,    // block height
  "windowstart":               5000, // block height
  "windowend":                 5555, // block height
}
```
**archived** | boolean  
Indicates whether or not this contract has been archived by the watchdog. This
is done when a file contract's inputs are double-spent or if the storage proof
window has already elapsed.

**formationsweepheight** | block height  
The block height at which the renter's watchdog will try to sweep inputs from
the formation transaction set if it hasn't been confirmed on chain yet.

**contractfound** | boolean  
Indicates whether or not the renter watchdog found the formation transaction set
on chain.

**latestrevisionfound** | uint64  
The highest revision number found by the watchdog for this contract on chain.

**storageprooffoundatheight** | block height  
The height at which the watchdog found a storage proof for this contract on
chain.

**doublespendheight** | block height  
The height at which a double-spend for this transactions formation transaction
was found on chain.

**windowstart** | block height  
The height at which the storage proof window for this contract starts.

**windowend** | block height  
The height at which the storage proof window for this contract ends.


## /renter/contractorchurnstatus [GET]
> curl example

```go
curl -A "Sia-Agent" "localhost:9980/renter/contractorchurnstatus"
```

Returns the churn status for the renter's contractor.

### JSON Response
> JSON Response Example

```go
{
  "aggregatecurrentperiodchurn": 500000,   // uint64
  "maxperiodchurn":              50000000, // uint64
}
```

**aggregatecurrentperiodchurn** | uint64  
Aggregate size of files stored in file contracts that were churned (i.e. not
marked for renewal) in the current period.


**maxperiodchurn** | uint64  
Maximum allowed aggregate churn per period.

## /renter/setmaxperiodchurn [POST]
> curl example

```go
curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/renter/setmaxperiodchurn?newmax=123456789"
```

sets the new max churn per period.

### Query String Parameters
**newmax** | uint64  
New maximum churn per period.

### Response

standard success or error response. See [standard responses](#standard-responses).


## /renter/dir/*siapath* [GET]
> curl example  

> The root siadir path is "" so submitting the API call without an empty siapath
will return the root siadir information.  

```go
curl -A "Sia-Agent" "localhost:9980/renter/dir/"
```  
```go
curl -A "Sia-Agent" "localhost:9980/renter/dir/mydir"
```

retrieves the contents of a directory on the sia network

### Path Parameters
### REQUIRED
**siapath** | string  
Path to the directory on the sia network  

### OPTIONAL
**root** | bool  
Whether or not to treat the siapath as being relative to the user's home
directory. If this field is not set, the siapath will be interpreted as
relative to 'home/user/'.  

### JSON Response
> JSON Response Example

```go
{
  "directories": [
    {
      "aggregatehealth":              1.0,  // float64
      "aggregatelasthealthchecktime": "2018-09-23T08:00:00.000000000+04:00" // timestamp
      "aggregatemaxhealth":           1.0,  // float64
      "aggregatemaxhealthpercentage": 1.0,  // float64
      "aggregateminredundancy":       2.6,  // float64
      "aggregatemostrecentmodtime":   "2018-09-23T08:00:00.000000000+04:00" // timestamp
      "aggregatenumfiles":            2,    // uint64
      "aggregatenumlostfiles":        0,    // uint64
      "aggregatenumstuckchunks":      4,    // uint64
      "aggregatenumsubdirs":          4,    // uint64
      "aggregatenumunfinishedfiles":  100,  // uint64
      "aggregaterepairsize":          4096, // uint64
      "aggregatesize":                4096, // uint64
      "aggregatestuckhealth":         1.0,  // float64
      "aggregatestucksize":           4096, // uint64
      
      "aggregateskynetfiles": 40,   // uint64
      "aggregateskynetsize":  4096, // uint64

      "health":              1.0,      // float64
      "lasthealthchecktime": "2018-09-23T08:00:00.000000000+04:00" // timestamp
      "maxhealth":           0.5,      // float64
      "maxhealthpercentage": 1.0,      // float64
      "minredundancy":       2.6,      // float64
      "mode":                0666,     // uint32
      "mostrecentmodtime":   "2018-09-23T08:00:00.000000000+04:00" // timestamp
      "numfiles":            3,        // uint64
      "numlostfiles":        0,        // uint64
      "numstuckchunks":      3,        // uint64
      "numsubdirs":          2,        // uint64
      "numunfinishedfiles":  100,      // uint64
      "repairsize":          4096,     // uint64
      "siapath":             "foo/bar" // string
      "size":                4096,     // uint64
      "stuckhealth":         1.0,      // float64
      "stucksize":           4096,     // uint64

      "UID": "9ce7ff6c2b65a760b7362f5a041d3e84e65e22dd", // string
      
      "skynetfiles": 40,   // uint64
      "skynetsize":  4096, // uint64
    }
  ],
  "files": []
}
```

**directories**\
An array of sia directories. Directories contain directory level metadata and
aggregate metadata for the subtree of the filesystem of which the directory
is the root of.

**aggregatehealth** | **health** | float64\
This is the worst health of any of the files or subdirectories. Health is the
percent of parity pieces missing.
 - health = 0 is full redundancy
 - health <= 1 is recoverable
 - health > 1 needs to be repaired from disk

**aggregatelasthealthchecktime** | **lasthealthchecktime** | timestamp\
The oldest time that the health of the directory or any of its files or sub
directories' health was checked.

**aggregatemaxhealth** | **maxhealth** | float64\
This is the worst health when comparing stuck health vs health

**aggregatemaxhealthpercentage** | **maxhealthpercentage** | float64\
This is the `maxhealth` displayed as a percentage. Since health is the amount
of redundancy missing, files can have up to 25% of the redundancy missing and
still be considered 100% healthy.

**aggregateminredundancy** | **minredundancy** | float64\
The lowest redundancy of any file or directory in the sub directory tree

**mode** | unit32\
The filesystem mode of the directory. There is no corresponding aggregate
field for mode.

**aggregatemostrecentmodtime** | **mostrecentmodtime** | timestamp\
The most recent mod time of any file or directory in the sub directory tree

**aggregatenumfiles** | **numfiles** | uint64\
The total number of files in the sub directory tree

**aggregatenumlostfiles** | **numlostfiles** | uint64\
The total number of lost files in the sub directory tree. A file is considered
lost if the redundancy has dropped below 1 and there is not a local file to
repair from.

**aggregatenumstuckchunks** | **aggregatenumstuckchunks** | uint64\
The total number of stuck chunks in the sub directory tree

**aggregatenumsubdirs** | **numsubdirs** | uint64\
The number of directories in the directory

**aggregatenumunfinishedfiles** | **numunfinishedfiles** | uint64\
The number of unfinished files in the directory

**aggregaterepairsize** | **repairsize** | uint64\
The total size in bytes that needs to be handled by the repair loop. This
does not include files that only have less than 25% of the redundancy missing
as the repair loop will ignore these files until they lose more redundancy.
This also does not include any stuck data.

**siapath** | string\
The path to the directory on the sia network. There is no corresponding
aggregate value for siapath.

**aggregatesize** | **size** | uint64\
The total size in bytes of files in the sub directory tree

**aggregatestuckhealth** | **stuckhealth** | floatt64\
The health of the most in need stuck siafile in the directory

**aggregatestucksize** | **stucksize** | uint64\
The total size in bytes that needs to be handled by the stuck loop. This does
include files that only have less than 25% of the redundancy missing as the
stuck loop does not take into account the health of the stuck file.

**UID** | string\
The unique identifier for the directory in the filesystem. There is no corresponding aggregate field for UID.

**aggregateskynetfiles** | **skynetfiles** | uint64\
The total number of skyfiles. This includes skyfile uploads and siafile to
skyfile conversions.

**aggregateskynetsize** | **skynetsize** | uint64\
The total size in bytes that corresponds to a skyfile. This includes skyfile
uploads and siafile to skyfile conversions.

**files** Same response as [files](#files)

## /renter/dir/*siapath* [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> --data "action=delete" "localhost:9980/renter/dir/mydir"
```

performs various functions on the renter's directories

### Path Parameters
### REQUIRED
**siapath** | string  
Location where the directory will reside in the renter on the network. The path
must be non-empty, may not include any path traversal strings ("./", "../"), and
may not begin with a forward-slash character.  

### OPTIONAL
**root** | bool  
Whether or not to treat the siapath as being relative to the user's home
directory. If this field is not set, the siapath will be interpreted as
relative to 'home/user/'.  

### Query String Parameters
### REQUIRED
**action** | string  
Action can be either `create`, `delete` or `rename`.
 - `create` will create an empty directory on the sia network
 - `delete` will remove a directory and its contents from the sia network. Will
   return an error if the target is a file.
 - `rename` will rename a directory on the sia network

**newsiapath** | string  
The new siapath of the renamed folder. Only required for the `rename` action.

### OPTIONAL
**mode** | uint32  
The mode can be specified in addition to the `create` action to create the
directory with specific permissions. If not specified, the default permissions
0755 will be used.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/downloadinfo/*uid* [GET]
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/renter/downloadinfo/9d8dd0d5b306f5bb412230bd12b590ae"
```

Lists a file in the download history by UID.

### Path Parameters
### REQUIRED
**uid** | string  
UID returned by the /renter/download/*siapath* endpoint. It is set in the http
header's 'ID' field.

### JSON Response
> JSON Response Example
 
```go
{
  "destination":     "/home/users/alice/bar.txt", // string
  "destinationtype": "file",                      // string
  "length":          8192,                        // bytes
  "offset":          2000,                        // bytes
  "siapath":         "foo/bar.txt",               // string

  "completed":           true,                    // boolean
  "endtime":             "2009-11-10T23:10:00Z",  // RFC 3339 time
  "error":               "",                      // string
  "received":            8192,                    // bytes
  "starttime":           "2009-11-10T23:00:00Z",  // RFC 3339 time
  "totaldatatransferred": 10031                    // bytes
}
```
**destination** | string  
Local path that the file will be downloaded to.  

**destinationtype** | string  
What type of destination was used. Can be "file", indicating a download to disk,
can be "buffer", indicating a download to memory, and can be "http stream",
indicating that the download was streamed through the http API.  

**length** | bytes  
Length of the download. If the download was a partial download, this will
indicate the length of the partial download, and not the length of the full
file.  

**offset** | bytes  
Offset within the file of the download. For full file downloads, the offset will
be '0'. For partial downloads, the offset may be anywhere within the file.
offset+length will never exceed the full file size.  

**siapath** | string  
Siapath given to the file when it was uploaded.  

**completed** | boolean  
Whether or not the download has completed. Will be false initially, and set to
true immediately as the download has been fully written out to the file, to the
http stream, or to the in-memory buffer. Completed will also be set to true if
there is an error that causes the download to fail.  

**endtime** | date, RFC 3339 time  
Time at which the download completed. Will be zero if the download has not yet
completed.  

**error** | string  
Error encountered while downloading. If there was no error (yet), it will be the
empty string.  

**received** | bytes  
Number of bytes downloaded thus far. Will only be updated as segments of the
file complete fully. This typically has a resolution of tens of megabytes.  

**starttime** | date, RFC 3339 time  
Time at which the download was initiated.

**totaldatatransferred** | bytes
The total amount of data transferred when downloading the file. This will
eventually include data transferred during contract + payment negotiation, as
well as data from failed piece downloads.  

## /renter/downloads [GET]
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/renter/downloads"
```

Lists all files in the download queue.

### Query String Parameters
### OPTIONAL
**root** | boolean\
If root is set, the downloads will contain their absolute paths instead of
the relative ones starting at /home/user.

### JSON Response
> JSON Response Example
 
```go
{
  "downloads": [
    {
      "destination":     "/home/users/alice/bar.txt", // string
      "destinationtype": "file",                      // string
      "length":          8192,                        // bytes
      "offset":          2000,                        // bytes
      "siapath":         "foo/bar.txt",               // string

      "completed":           true,                    // boolean
      "endtime":             "2009-11-10T23:10:00Z",  // RFC 3339 time
      "error":               "",                      // string
      "received":            8192,                    // bytes
      "starttime":           "2009-11-10T23:00:00Z",  // RFC 3339 time
      "totaldatatransfered": 10031                    // bytes
    }
  ]
}
```
**destination** | string  
Local path that the file will be downloaded to.  

**destinationtype** | string  
What type of destination was used. Can be "file", indicating a download to disk,
can be "buffer", indicating a download to memory, and can be "http stream",
indicating that the download was streamed through the http API.  

**length** | bytes  
Length of the download. If the download was a partial download, this will
indicate the length of the partial download, and not the length of the full
file.  

**offset** | bytes  
Offset within the file of the download. For full file downloads, the offset will
be '0'. For partial downloads, the offset may be anywhere within the file.
offset+length will never exceed the full file size.  

**siapath** | string  
Siapath given to the file when it was uploaded.  

**completed** | boolean  
Whether or not the download has completed. Will be false initially, and set to
true immediately as the download has been fully written out to the file, to the
http stream, or to the in-memory buffer. Completed will also be set to true if
there is an error that causes the download to fail.  

**endtime** | date, RFC 3339 time  
Time at which the download completed. Will be zero if the download has not yet
completed.  

**error** | string  
Error encountered while downloading. If there was no error (yet), it will be the
empty string.  

**received** | bytes  
Number of bytes downloaded thus far. Will only be updated as segments of the
file complete fully. This typically has a resolution of tens of megabytes.  

**starttime** | date, RFC 3339 time  
Time at which the download was initiated.

**totaldatatransfered** | bytes  
The total amount of data transferred when downloading the file. This will
eventually include data transferred during contract + payment negotiation, as
well as data from failed piece downloads.  

## /renter/downloads/clear [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> -X POST "localhost:9980/renter/downloads/clear?before=1551398400&after=1552176000"
```

Clears the download history of the renter for a range of unix time stamps.  Both
parameters are optional, if no parameters are provided, the entire download
history will be cleared.  To clear a single download, provide the timestamp for
the download as both parameters.  Providing only the before parameter will clear
all downloads older than the timestamp. Conversely, providing only the after
parameter will clear all downloads newer than the timestamp.

### Query String Parameters
### OPTIONAL
**before** | unix timestamp  
unix timestamp found in the download history

**after** | unix timestamp  
unix timestamp found in the download history

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/prices [GET]
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/renter/prices"
```

Lists the estimated prices of performing various storage and data operations. An
allowance can be submitted to provide a more personalized estimate. If no
allowance is submitted then the current set allowance will be used, if there is
no allowance set then sane defaults will be used. Submitting an allowance is
optional, but when submitting an allowance all the components of the allowance
are required. The allowance used to create the estimate is returned with the
estimate.

### Query String Parameters
### REQUIRED or OPTIONAL
Allowance settings, see the fields [here](#allowance)

### JSON Response
> JSON Response Example
 
```go
{
  "downloadterabyte":      "1234",  // hastings
  "formcontracts":         "1234",  // hastings
  "storageterabytemonth":  "1234",  // hastings
  "uploadterabyte":        "1234",  // hastings
  "funds":                 "1234",  // hastings
  "hosts":                     24,  // int
  "period":                  6048,  // blocks
  "renewwindow":             3024   // blocks
}
```
**downloadterabyte** | hastings  
The estimated cost of downloading one terabyte of data from the network.  

**formcontracts** | hastings  
The estimated cost of forming a set of contracts on the network. This cost also
applies to the estimated cost of renewing the renter's set of contracts.  

**storageterabytemonth** | hastings  
The estimated cost of storing one terabyte of data on the network for a month,
including accounting for redundancy.  

**uploadterabyte** | hastings  
The estimated cost of uploading one terabyte of data to the network, including
accounting for redundancy.  

The allowance settings used for the estimation are also returned, see the fields
[here](#allowance)

## /renter/files [GET]
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/renter/files?cached=false"
```

### Query String Parameters
### OPTIONAL
**cached** | boolean  
determines whether cached values should be returned or if the latest values
should be computed. Cached values speed the endpoint up significantly. The
default value is 'false'.

lists the status of all files.

### JSON Response
> JSON Response Example
 
```go
{
  "files": [
    {
      "accesstime":       12578940002019-02-20T17:46:20.34810935+01:00,  // timestamp
      "available":        true,                 // boolean
      "changetime":       12578940002019-02-20T17:46:20.34810935+01:00,  // timestamp
      "ciphertype":       "threefish",          // string   
      "createtime":       12578940002019-02-20T17:46:20.34810935+01:00,  // timestamp
      "expiration":       60000,                // block height
      "filesize":         8192,                 // bytes
      "finished":         true,                 // boolean
      "health":           0.5,                  // float64
      "localpath":        "/home/foo/bar.txt",  // string
      "maxhealth":        0.0,                  // float64  
      "maxhealthpercent": 100%,                 // float64
      "modtime":          12578940002019-02-20T17:46:20.34810935+01:00,  // timestamp
      "mode":             640,                  // uint32
      "numstuckchunks":   0,                    // uint64
      "ondisk":           true,                 // boolean
      "recoverable":      true,                 // boolean
      "redundancy":       5,                    // float64
      "renewing":         true,                 // boolean
      "repairbytes":      4096,                 // uint64
      "siapath":          "foo/bar.txt",        // string
      "skylinks": [                             // []string
        "CABAB_1Dt0FJsxqsu_J4TodNCbCGvtFf1Uys_3EgzOlTcg"
        "GAC38Gan6YHVpLl-bfefa7aY85fn4C0EEOt5KJ6SPmEy4g"
      ], 
      "stuck":            false,                // bool
      "stuckbytes":       4096,                 // uint64
      "stuckhealth":      0.0,                  // float64
      "UID":              "00112233445566778899aabbccddeeff",            // string
      "uploadedbytes":    209715200,            // total bytes uploaded
      "uploadprogress":   100,                  // percent
    }
  ]
}
```
**files**  

**accesstime** | timestamp  
indicates the last time the siafile was accessed

**available** | boolean  
true if the file is available for download. A file is available to download once
it has reached at least 1x redundancy. Files may be available before they have
reached 100% upload progress as upload progress includes the full expected
redundancy of the file.  

**changetime** | timestamp  
indicates the last time the siafile metadata was updated

**ciphertype** | string  
indicates the encryption used for the siafile

**createtime** | timestamp  
indicates when the siafile was created

**expiration** | block height  
Block height at which the file ceases availability.  

**filesize** | bytes  
Size of the file in bytes.  

**finished** | boolean  
finished is a boolean indicating if the original upload ever finished, meaning
the file made it to 1x redundancy.

**health** | float64  
health is an indication of the amount of redundancy missing where 0 is full
redundancy and >1 means the file is not available. The health of the siafile is
the health of the worst unstuck chunk.

**localpath** | string  
Path to the local file on disk.  
**NOTE** `siad` will set the localpath to an empty string if the local file is
not found on disk. This is done to avoid the siafile being corrupted in the
future by a different file being placed on disk at the original localpath
location.  

**maxhealth** | float64  
the maxhealth is either the health or the stuckhealth of the siafile, whichever
is worst

**maxhealthpercent** | float64  
maxhealthpercent is the maxhealth converted to be out of 100% to be more easily
understood

**modtime** | timestamp  
indicates the last time the siafile contents where modified

**mode** | uint32\
The file mode / permissions of the file. Users who download this file will be
presented a file with this mode. If no mode is set, the default of 0644 will be
used.

**numstuckchunks** | uint64  
indicates the number of stuck chunks in a file. A chunk is stuck if it cannot
reach full redundancy

**ondisk** | boolean  
indicates if the source file is found on disk

**recoverable** | boolean  
indicates if the siafile is recoverable. A file is recoverable if it has at
least 1x redundancy or if `siad` knows the location of a local copy of the file.

**redundancy** | float64  
When a file is uploaded, it is first broken into a series of chunks. Each chunk
goes on a different set of hosts, and therefore different chunks of the file can
have different redundancies. The redundancy of a file as reported from the API
will be equal to the lowest redundancy of any of  the file's chunks.

**renewing** | boolean  
true if the file's contracts will be automatically renewed by the renter.  

**repairbytes** | uint64\
The total size in bytes that needs to be handled by the repair loop. This does
not include anything less than 25% of the redundancy missing as the repair loop
will ignore files until they lose more redundancy.  This also does not include
any stuck data.

**siapath** | string  
Path to the file in the renter on the network.  

**skylinks** | []string\
All the skylinks related to the file.

**stuck** | bool  
a file is stuck if there are any stuck chunks in the file, which means the file
cannot reach full redundancy

**stuckhealth** | float64  
stuckhealth is the worst health of any of the stuck chunks.

**stuckbytes** | uint64\
The total size in bytes that needs to be handled by the stuck loop. This does
include anything less than 25% of the redundancy missing as the stuck loop does
not take into account the health of the stuck file.

**UID** | string\
A unique identifier for the file.

**uploadedbytes** | bytes  
Total number of bytes successfully uploaded via current file contracts. This
number includes padding and rendundancy, so a file with a size of 8192 bytes
might be padded to 40 MiB and, with a redundancy of 5, encoded to 200 MiB for
upload.  

**uploadprogress** | percent  
Percentage of the file uploaded, including redundancy. Uploading has completed
when uploadprogress is 100. Files may be available for download before upload
progress is 100.  

## /renter/file/*siapath* [GET]
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/renter/file/myfile"
```

Lists the status of specified file.

### Path Parameters
### REQUIRED
**siapath** | string  
Path to the file in the renter on the network.

### JSON Response
Same response as [files](#files)

## /renter/file/*siapath* [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> --data "trackingpath=/home/myfile" "localhost:9980/renter/file/myfile"
```

endpoint for changing file metadata.

### Path Parameters
### REQUIRED
**siapath** | string  
SiaPath of the file on the network. The path must be non-empty, may not include
any path traversal strings ("./", "../"), and may not begin with a forward-slash
character.

### Query String Parameters
### OPTIONAL
**trackingpath** | string  
If provided, this parameter changes the tracking path of a file to the
specified path. Useful if moving the file to a different location on disk.

**stuck** | bool  
if set a file will be marked as either stuck or not stuck by marking all of
its chunks.

**root** | bool  
Whether or not to treat the siapath as being relative to the user's home
directory. If this field is not set, the siapath will be interpreted as
relative to 'home/user/'.  

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/delete/*siapath* [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> -X POST "localhost:9980/renter/delete/myfile"
```

deletes a renter file entry. Does not delete any downloads or original files,
only the entry in the renter. Will return an error if the target is a folder.

### Path Parameters
### REQUIRED
**siapath** | string  
Path to the file in the renter on the network.

### OPTIONAL
**root** | bool  
Whether or not to treat the siapath as being relative to the user's home
directory. If this field is not set, the siapath will be interpreted as relative
to 'home/user/'.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/download/*siapath* [GET]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/renter/download/myfile?httpresp=true"
```

downloads a file to the local filesystem. The call will block until the file has
been downloaded.

### Path Parameters
### REQUIRED
**siapath** | string  
Path to the file in the renter on the network.

### Query String Parameters
### REQUIRED (Either one or the other)
**destination** | string  
Location on disk that the file will be downloaded to.  

**httpresp** | boolean  
If httresp is true, the data will be written to the http response.

### OPTIONAL
**async** | boolean  
If async is true, the http request will be non blocking. Can't be used with
httpresp.

**disablelocalfetch** | boolean  
If disablelocalfetch is true, downloads won't be served from disk even if the
file is available locally.

**root** | boolean  
If root is true, the provided siapath will not be prefixed with /home/user but is instead taken as an absolute path.

**length** | bytes  
Length of the requested data. Has to be <= filesize-offset.  

**offset** | bytes  
Offset relative to the file start from where the download starts.  

### Response

Unlike most responses, this response modifies the http response header. The
download will set the 'ID' field in the http response header to a unique
identifier which can be used to cancel an async download with the
/renter/download/cancel endpoint and retrieve a download's info from the
download history using the /renter/downloadinfo endpoint. Apart from that the
response is a standard success or error response. See [standard
responses](#standard-responses).

## /renter/download/cancel [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/renter/download/cancel?id=<downloadid>"
```

cancels the download with the given id.

### Query String Parameters
**id** | string  
ID returned by the /renter/download/*siapath* endpoint. It is set in the http
header's 'ID' field.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/downloadsync/*siapath* [GET]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/renter/downloadasync/myfile?destination=/home/myfile"
```

downloads a file to the local filesystem. The call will return immediately.

### Path Parameters
### REQUIRED
**siapath** | string  
Path to the file in the renter on the network.

### Query String Parameters
### REQUIRED
**destination** | string  
Location on disk that the file will be downloaded to.  

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/fuse [GET]
> curl example  

```bash
curl -A "Sia-Agent" "localhost:9980/renter/fuse"
```

Lists the set of folders that have been mounted to the user's filesystem and
which mountpoints have been used for each mount.

### JSON Response
> JSON Response Example

```go
{
  "mountpoints": [ // []skymodules.MountInfo
    {
      "mountpoint": "/home/user/siavideos", // string
      "siapath": "/videos",                 // skymodules.SiaPath

      "mountoptions": { // []skymodules.MountOptions
          "allowother": false, // bool
          "readonly": true,    // bool
        },
    },
  ]
}
```
**mountpoint** | string  
The system path that is being used to mount the fuse folder.

**siapath** | string  
The siapath that has been mounted to the mountpoint.

## /renter/fuse/mount [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> -X POST "localhost:9980/renter/fuse/mount?readonly=true"
```

Mounts a Sia directory to the local filesystem using FUSE.

### Query String Parameters
### REQUIRED
**mount** | string  
Location on disk to use as the mountpoint.

**readonly** | bool  
Whether the directory should be mounted as ReadOnly. Currently, readonly is a
required parameter and must be set to true.

### OPTIONAL
**siapath** | string  
Which path should be mounted to the filesystem. If left blank, the user's home
directory will be used.

**allowother** | boolean  
By default, only the system user that mounted the fuse directory will be allowed
to interact with the directory. Often, applications like Plex run as their own
user, and therefore by default are banned from viewing or otherwise interacting
with the mounted folder. Setting 'allowother' to true will allow other users to
see and interact with the mounted folder.

On Linux, if 'allowother' is set to true, /etc/fuse.conf needs to be modified so
that 'user_allow_other' is set. Typically this involves uncommenting a single
line of code, see the example below of an /etc/fuse.conf file that has
'use_allow_other' enabled.

```bash
# /etc/fuse.conf - Configuration file for Filesystem in Userspace (FUSE)

# Set the maximum number of FUSE mounts allowed to non-root users.
# The default is 1000.
#mount_max = 1000

# Allow non-root users to specify the allow_other or allow_root mount options.
user_allow_other
```

### Response

standard success or error response. See [standard
responses](#standard-responses).


## /renter/fuse/unmount [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> -X POST "localhost:9980/renter/fuse/unmount?mount=/home/user/videos"
```

### Query String Parameters
### REQUIRED
**mount** | string  
Mountpoint that was used when mounting the fuse directory.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/recoveryscan [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> -X POST "localhost:9980/renter/recoveryscan"
```

starts a rescan of the whole blockchain to find recoverable contracts. The
contractor will periodically try to recover found contracts every 10 minutes
until they are recovered or expired.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/recoveryscan [GET]
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/renter/recoveryscan"
```

Returns some information about a potentially ongoing recovery scan.

### JSON Response
> JSON Response Example

```go
{
  "scaninprogress": true // boolean
  "scannedheight" : 1000 // uint64
}
```
**scaninprogress** | boolean  
indicates if a scan for recoverable contracts is currently in progress.

**scannedheight** | uint64  
indicates the progress of a currently ongoing scan in terms of number of blocks
that have already been scanned.

## /renter/rename/*siapath* [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> --data "newsiapath=myfile2" "localhost:9980/renter/rename/myfile"

curl -A "Sia-Agent" -u "":<apipassword> --data "newsiapath=myfile2&root=true" "localhost:9980/renter/rename/myfile"
```

change the siaPath for a file that is being managed by the renter.

### Path Parameters
### REQUIRED
**siapath** | string  
Path to the file in the renter on the network.

### Query String Parameters
### REQUIRED
**newsiapath** | string  
New location of the file in the renter on the network.  

### OPTIONAL
**root** | bool  
Whether or not to treat the siapath as being relative to the user's home
directory. If this field is not set, the siapath will be interpreted as
relative to 'home/user/'.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/stream/*siapath* [GET]
> curl example  

```sh
curl -A "Sia-Agent" "localhost:9980/renter/stream/myfile"
```  

> The file can be streamed partially by using standard partial http requests
> which means setting the "Range" field in the http header.  

```sh
curl -A "Sia-Agent" -H "Range: bytes=0-1023" "localhost:9980/renter/stream/myfile"
```

downloads a file using http streaming. This call blocks until the data is
received. The streaming endpoint also uses caching internally to prevent siad
from re-downloading the same chunk multiple times when only parts of a file are
requested at once. This might lead to a substantial increase in ram usage and
therefore it is not recommended to stream multiple files in parallel at the
moment. This restriction will be removed together with the caching once partial
downloads are supported in the future. If you want to stream multiple files you
should increase the size of the Renter's `streamcachesize` to at least 2x the
number of files you are steaming.

### Path Parameters
### REQUIRED
**siapath** | string  
Path to the file in the renter on the network.

### OPTIONAL
**disablelocalfetch** | boolean  
If disablelocalfetch is true, downloads won't be served from disk even if the
file is available locally.

**root** | boolean  
If root is true, the provided siapath will not be prefixed with /home/user but is instead taken as an absolute path.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/upload/*siapath* [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> --data "source=/home/myfile" "localhost:9980/renter/upload/myfile"
```

uploads a file to the network from the local filesystem.

### Path Parameters
### REQUIRED
**siapath** | string  
Location where the file will reside in the renter on the network. The path must
be non-empty, may not include any path traversal strings ("./", "../"), and may
not begin with a forward-slash character.  

### Query String Parameters
### REQUIRED
**source** | string  
Location on disk of the file being uploaded.  

### OPTIONAL
**datapieces** | int  
The number of data pieces to use when erasure coding the file.  

**paritypieces** | int  
The number of parity pieces to use when erasure coding the file. Total
redundancy of the file is (datapieces+paritypieces)/datapieces.  

**force** | boolean  
Delete potential existing file at siapath.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/uploadstream/*siapath* [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/renter/uploadstream/myfile?datapieces=10&paritypieces=20" --data-binary @myfile.dat

curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/renter/uploadstream/myfile?repair=true" --data-binary @myfile.dat
```

uploads a file to the network using a stream. If the upload stream POST call
fails or quits before the file is fully uploaded, the file can be repaired by a
subsequent call to the upload stream endpoint using the `repair` flag.

### Path Parameters
### REQUIRED
**siapath** | string  
Location where the file will reside in the renter on the network. The path must
be non-empty, may not include any path traversal strings ("./", "../"), and may
not begin with a forward-slash character.  

### Query String Parameters
### OPTIONAL
**datapieces** | int  
The number of data pieces to use when erasure coding the file.  

**paritypieces** | int  
The number of parity pieces to use when erasure coding the file. Total
redundancy of the file is (datapieces+paritypieces)/datapieces.  

**force** | boolean  
Delete potential existing file at siapath.

**repair** | boolean  
Repair existing file from stream. Can't be specified together with datapieces,
paritypieces and force.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /renter/uploadready [GET]
> curl example  

```go
curl -A "Sia-Agent" "localhost:9980/renter/uploadready?datapieces=10&paritypieces=20"
```

Returns the whether or not the renter is ready for upload.

### Path Parameters
### OPTIONAL
datapieces and paritypieces are both optional, however if one is supplied then
the other needs to be supplied. If neither are supplied then the default values
for the erasure coding will be used 

**datapieces** | int  
The number of data pieces to use when erasure coding the file.  

**paritypieces** | int  
The number of parity pieces to use when erasure coding the file.   

### JSON Response
> JSON Response Example

```go
{
"ready":false,            // bool
"contractsneeded":30,     // int
"numactivecontracts":20,  // int
"datapieces":10,          // int
"paritypieces":20         // int 
}
```
**ready** | boolean  
ready indicates if the renter is ready to fully upload a file based on the
erasure coding.  

**contractsneeded** | int  
contractsneeded is how many contracts are needed to fully upload a file.  

**numactivecontracts** | int  
numactivecontracts is the number of active contracts the renter has.

**datapieces** | int  
The number of data pieces to use when erasure coding the file.  

**paritypieces** | int  
The number of parity pieces to use when erasure coding the file.

## /renter/uploads/pause [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> --data "duration=10m" "localhost:9980/renter/uploads/pause"
```

This endpoint will pause any future uploads or repairs for the duration
requested. Any in progress chunks will finish. This can be used to free up
the workers to exclusively focus on downloads. Since this will pause file
repairs it is advised to not pause for too long. If no duration is supplied
then the default duration of 600 seconds will be used. If the uploads are
already paused, additional calls to pause the uploads will result in the
duration of the pause to be reset to the duration supplied as opposed to
pausing for an additional length of time.

### Path Parameters
#### OPTIONAL 
**duration** | string  
duration is how long the repairs and uploads will be paused in seconds. If no
duration is supplied the default pause duration will be used.

### Response
standard success or error response. See [standard
responses](#standard-responses).

## /renter/uploads/resume [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/renter/uploads/resume"
```

This endpoint will resume uploads and repairs.

### Response
standard success or error response. See [standard
responses](#standard-responses).

## /renter/validatesiapath/*siapath* [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/renter/validatesiapath/isthis-aval_idsiapath"
```

validates whether or not the provided siapath is a valid siapath. Every path
valid under Unix is valid as a SiaPath.

### Path Parameters
### REQUIRED
**siapath** | string  
siapath to test.

### Response
standard success or error response, a successful response means a valid siapath.
See [standard responses](#standard-responses).

## /renter/workers [GET] 

**UNSTABLE - subject to change**

> curl example

```go
curl -A "Sia-Agent" "localhost:9980/renter/workers"
```

returns the the status of all the workers in the renter's workerpool.

### JSON Response
> JSON Response Example

```go
{
  "numworkers":            2, // int
  "totaldownloadcooldown": 0, // int
  "totalmaintenancecooldown": 0, // int
  "totaluploadcooldown":   0, // int
  
  "workers": [ // []WorkerStatus
    {
      "contractid": "e93de33cc04bb1f27a412ecdf57b3a7345b9a4163a33e03b4cb23edeb922822c", // hash
      "contractutility": {      // ContractUtility
        "goodforupload": true,  // boolean
        "goodforrenew":  true,  // boolean
        "badcontract":   false, // boolean
        "lastooserr":    0,     // BlockHeight
        "locked":        false  // boolean
      },
      "hostpubkey": {
        "algorithm": "ed25519", // string
        "key": "BervnaN85yB02PzIA66y/3MfWpsjRIgovCU9/L4d8zQ=" // hash
      },
      
      "downloadcooldownerror": "",                   // string
      "downloadcooldowntime":  -9223372036854775808, // time.Duration
      "downloadoncooldown":    false,                // boolean
      "downloadqueuesize":     0,                    // int
      "downloadterminated":    false,                // boolean
      
      "uploadcooldownerror": "",                   // string
      "uploadcooldowntime":  -9223372036854775808, // time.Duration
      "uploadoncooldown":    false,                // boolean
      "uploadqueuesize":     0,                    // int
      "uploadterminated":    false,                // boolean
      
      "balancetarget":       "0", // hastings

      "downloadsnapshotjobqueuesize": 0 // int
      "uploadsnapshotjobqueuesize": 0   // int

      "maintenanceoncooldown": false,                      // bool
      "maintenancerecenterr": "",                          // string
      "maintenancerecenterrtime": "0001-01-01T00:00:00Z",  // time

      "accountstatus": {
        "availablebalance": "1000000000000000000000000", // hasting
        "negativebalance": "0",                          // hasting
        "recenterr": "",                                 // string
        "recenterrtime": "0001-01-01T00:00:00Z"          // time
        "recentsuccesstime": "0001-01-01T00:00:00Z"      // time
      },

      "pricetablestatus": {
        "expirytime": "2020-06-15T16:17:01.040481+02:00", // time
        "updatetime": "2020-06-15T16:12:01.040481+02:00", // time
        "active": true,                                   // boolean
        "recenterr": "",                                  // string
        "recenterrtime": "0001-01-01T00:00:00Z"           // time
      },

      "readjobsstatus": {
        "avgjobtime64k": 0,                               // int
        "avgjobtime1m": 0,                                // int
        "avgjobtime4m": 0,                                // int
        "consecutivefailures": 0,                         // int
        "jobqueuesize": 0,                                // int
        "recenterr": "",                                  // string
        "recenterrtime": "0001-01-01T00:00:00Z"           // time
      },

      "hassectorjobsstatus": {
        "avgjobtime": 0,                                  // int
        "consecutivefailures": 0,                         // int
        "jobqueuesize": 0,                                // int
        "recenterr": "",                                  // string
        "recenterrtime": "0001-01-01T00:00:00Z"           // time
      }
    }
  ]
}
```


**numworkers** | int  
Number of workers in the workerpool

**totaldownloadcooldown** | int  
Number of workers on download cooldown

**totalmaintenancecooldown** | int  
Number of workers on maintenance cooldown

**totaluploadcooldown** | int  
Number of workers on upload cooldown

**workers** | []WorkerStatus  
List of workers

**contractid** | hash  
The ID of the File Contract that the worker is associated with

**contractutility** | ContractUtility  

**goodforupload** | boolean  
The worker's contract can be uploaded to

**goodforrenew** | boolean  
The worker's contract will be renewed

**badcontract** | boolean  
The worker's contract is marked as bad and won't be used

**lastooserr** | BlockHeight  
The blockheight when the host the worker represents was out of storage

**locked** | boolean  
The worker's contract's utility is locked

**hostpublickey** | SiaPublicKey  
Public key of the host that the file contract is formed with.  

**downloadcooldownerror** | error  
The error reason for the worker being on download cooldown

**downloadcooldowntime** | time.Duration  
How long the worker is on download cooldown

**downloadoncooldown** | boolean  
Indicates if the worker is on download cooldown

**downloadqueuesize** | int  
The size of the worker's download queue

**downloadterminated** | boolean  
Downloads for the worker have been terminated

**uploadcooldownerror** | error  
The error reason for the worker being on upload cooldown

**uploadcooldowntime** | time.Duration  
How long the worker is on upload cooldown

**uploadoncooldown** | boolean  
Indicates if the worker is on upload cooldown

**uploadqueuesize** | int  
The size of the worker's upload queue

**uploadterminated** | boolean  
Uploads for the worker have been terminated

**availablebalance** | hastings  
The worker's Ephemeral Account available balance

**balancetarget** | hastings  
The worker's Ephemeral Account target balance

**downloadsnapshotjobqueuesize** | int  
The size of the worker's download snapshot job queue

**uploadsnapshotjobqueuesize** | int  
The size of the worker's upload snapshot job queue

**maintenanceoncooldown** | boolean  
Indicates if the worker is on maintenance cooldown

**maintenancecooldownerror** | string  
The error reason for the worker being on maintenance cooldown

**maintenancecooldowntime** | time.Duration  
How long the worker is on maintenance cooldown

**accountstatus** | object
Detailed information about the workers' ephemeral account status

**pricetablestatus** | object
Detailed information about the workers' price table status

**readjobsstatus** | object
Details of the workers' read jobs queue

**hassectorjobsstatus** | object
Details of the workers' has sector jobs queue

## Resumable Uploads

Skyd supports resumable uploads using the [TUS protocol](https://tus.io/).
The protocol is implemented on the following endpoints:

- [POST]  /skynet/tus
- [HEAD]  /skynet/tus/:id
- [PATCH] /skynet/tus/:id
- [GET]   /skynet/tus/:id

For detailed information about the protocol check out the [specification](https://tus.io/protocols/resumable-upload.html).

### Chunk Size

For uploads to work with Skyd, you need to make sure that the chunk size of
your TUS client is configured correctly. Otherwise, the upload will return an
error which contains the expected chunk size. Right now the chunk size is
expected to be a multiple of `40MiB` for the default upload settings.

The formula for computing the chunk size is:
`chunkSize = (4MiB - encryptionOverhead) * dataPieces`

When using the defaults, the overhead is 0 and the dataPieces are 10. That's
because all files uploaded using this protocol are automatically considered
large uploads.

### Skylink

The Skylink for a TUS upload can be found at the following endpoint once the
upload finished successfully.  It will be returned both as a JSON object in
the response body as well as the http response header `"Skynet-Skylink"`.

### Max Upload Size

To limit the upload size of a file globally, set the `TUS_MAXSIZE` environment
variable to the maximum number of bytes any user can upload. In addition to that
global limit, the `SkynetMaxUploadSize` http header needs to be set on TUS
requests for creating a new upload. This is the local limit for the specific
upload. For security, both the global and local limit need to be set. 

## /skynet/upload/tus/:id [GET]
> curl example  

```bash
curl "localhost:9980/skynet/upload/tus/72a44879cb93d0cd0c13b285a06ce22ce6cd6c8a"
```  

returns information about a finished TUS upload.

### Path Parameters
**id** | string  
The upload ID specified in the TUS protocol.

### JSON Response
> JSON Response Example

```go
{
  "skylink": "AACnV8tE5iQpfl5Bi-iYNx07TF-eOEu1dYUv8KJzfuXUCA",                      // string
  "merkleroot": "a757cb44e624297e5e418be898371d3b4c5f9e384bb575852ff0a2737ee5d408", // crypto.Hash
  "bitfield": 0                                                                     // uint16
}
```

# Skynet

## /skynet/basesector/*skylink* [GET]
> curl example  

```bash
curl -A "Sia-Agent" "localhost:9980/skynet/basesector/CABAB_1Dt0FJsxqsu_J4TodNCbCGvtFf1Uys_3EgzOlTcg"
```  

downloads the basesector of a skylink using http streaming. This call blocks
until the data is received. There is a 30s default timeout applied to
downloading a basesector. If the data cannot be found within this 30s time
constraint, a 404 will be returned. This timeout is configurable through the
query string parameters.


### Path Parameters 
### Required
**skylink** | string  
The skylink of the basesector that should be downloaded.

### Query String Parameters
### OPTIONAL

**timeout** | int  
If 'timeout' is set, the download will fail if the basesector cannot be
retrieved before it expires. Note that this timeout does not cover the actual
download time, but rather covers the TTFB. Timeout is specified in seconds,
a timeout value of 0 will be ignored. If no timeout is given, the default will
be used, which is a 30 second timeout. The maximum allowed timeout is 900s (15
minutes).

**priceperms** | string  
'price per millisecond' is a value that helps the downloader determine whether
to download from cheaper hosts or faster hosts. For a ppms of '0', the
downloader will always select the cheapest hosts that it is able to download
from. If the ppms is 1 SC and the downloader knows it can save 10 milliseconds
by choosing more expensive hosts to download from, it will choose those hosts if
and only if the total cost of the download increases by less than 10 SC,
otherwise it will continue using the cheaper hosts. The default ppms is 100nS.

### Response Body

The response body is the raw data for the basesector.

## /skynet/blocklist [GET]
> curl example

```go
curl -A "Sia-Agent" "localhost:9980/skynet/blocklist"
```

returns the list of hashed merkleroots that are blocked. 

NOTE: these are not the same values that were submitted via the POST endpoint.
This is intentional so that it is harder to find the blocked content.
	
NOTE: With v1.5.0 the return value for the Blocklist changed. Pre v1.5.0 the
[]crypto.Hash was a slice of MerkleRoots. Post v1.5.0 the []crypto.Hash is
a slice of the Hashes of the MerkleRoots

### JSON Response
> JSON Response Example

```go
{
  "blocklist": {
    "QAf9Q7dBSbMarLvyeE6HTQmwhr7RX9VMrP9xIMzpU3I" // hash
    "QAf9Q7dBSbMarLvyeE6HTQmwhr7RX9VMrP9xIMzpU3I" // hash
    "QAf9Q7dBSbMarLvyeE6HTQmwhr7RX9VMrP9xIMzpU3I" // hash
  }
}
```
**blocklist** | Hashes  
The blocklist is a list of hashed merkleroots, that are blocked.

## /skynet/blocklist [POST]
> curl example

```go
curl -A "Sia-Agent" --user "":<apipassword> --data '{"add" : ["GAC38Gan6YHVpLl-bfefa7aY85fn4C0EEOt5KJ6SPmEy4g","GAC38Gan6YHVpLl-bfefa7aY85fn4C0EEOt5KJ6SPmEy4g","GAC38Gan6YHVpLl-bfefa7aY85fn4C0EEOt5KJ6SPmEy4g"]}' "localhost:9980/skynet/blocklist"

curl -A "Sia-Agent" --user "":<apipassword> --data '{"remove" : ["GAC38Gan6YHVpLl-bfefa7aY85fn4C0EEOt5KJ6SPmEy4g","GAC38Gan6YHVpLl-bfefa7aY85fn4C0EEOt5KJ6SPmEy4g","GAC38Gan6YHVpLl-bfefa7aY85fn4C0EEOt5KJ6SPmEy4g"]}' "localhost:9980/skynet/blocklist"
```

updates the list of skylinks that should be blocked from Skynet. This endpoint
can be used to both add and remove skylinks from the blocklist.

**NOTE:** this endpoint accepts both V1 and V2 skylinks. When a V2 skylink is
submitted, it is resolved into a V1 skylink so that the data behind the V1
skylink is blocked. This allows for the V2 skylink to be updated to point to new
content that isn't blocked.

### Path Parameters
### REQUIRED
At least one of the following fields needs to be non empty.

**add** | array of strings  
add is an array of skylinks that should be added to the blocklist.

**remove** | array of strings  
remove is an array of skylinks that should be removed from the blocklist.

### OPTIONAL
**ishash** | boolean  
`ishash` tells skyd if the incoming values are blocklist hashes as oppose to skylinks.

**period** | int64  
`probationaryperiod` defines the probationary period for the blocked content in
seconds. During the probationary period, the data will be blocked but not be
deleted. If left blank the default period of 30days will be used. If you want
the data to be deleted immediately you can set this value to -1.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /skynet/metadata/*skylink* [GET]
> curl example  

```bash
curl -A "Sia-Agent" "localhost:9980/skynet/metadata/CABAB_1Dt0FJsxqsu_J4TodNCbCGvtFf1Uys_3EgzOlTcg"
```  

downloads the metadata of a skylink within its base sector.

### Path Parameters 
### Required
**skylink** | string  
The skylink of the metadata that should be downloaded.

### Query String Parameters
### OPTIONAL

**timeout** | int  
If 'timeout' is set, the download will fail if the basesector cannot be
retrieved before it expires. Note that this timeout does not cover the actual
download time, but rather covers the TTFB. Timeout is specified in seconds,
a timeout value of 0 will be ignored. If no timeout is given, the default will
be used, which is a 30 second timeout. The maximum allowed timeout is 900s (15
minutes).

**priceperms** | string  
'price per millisecond' is a value that helps the downloader determine whether
to download from cheaper hosts or faster hosts. For a ppms of '0', the
downloader will always select the cheapest hosts that it is able to download
from. If the ppms is 1 SC and the downloader knows it can save 10 milliseconds
by choosing more expensive hosts to download from, it will choose those hosts if
and only if the total cost of the download increases by less than 10 SC,
otherwise it will continue using the cheaper hosts. The default ppms is 100nS.

### JSON Response
> JSON Response Example

```go
{
  "filename": "testSmall", // string
  "length":   99,          // uint64
  "mode":     416          // uint32
}
```

## /skynet/pin/:skylink [POST]
> curl example

```go
curl -A "Sia-Agent" -u "":<apipassword> --data "siapath=path/to/pin" "localhost:9980/skynet/pin/CABAB_1Dt0FJsxqsu_J4TodNCbCGvtFf1Uys_3EgzOlTcg"
```

Pinning a skylink to a portal will add the skyfile to the portal's filesystem
making the portal responsible for maintaining the health of this pinned copy of
the skyfile.

### Path Parameters
### REQUIRED
**skylink** | string\
The skylink that should be pinned.

### Query String Parameters
### REQUIRED
**siapath** | string\
The siapath that the skyfile should be pinned at in the portal's filesystem.

### OPTIONAL
**basechunkredundancy** | uint8\
The amount of redundancy to use when uploading the base chunk. The base chunk is
the first chunk of the file, and is always uploaded using 1-of-N redundancy.

**force** | bool\
If the pinned skyfile should overwrite any file currently at the provided
siapath.

**priceperms** | string\
'price per millisecond' is a value that helps the downloader determine whether
to download from cheaper hosts or faster hosts. For a ppms of '0', the
downloader will always select the cheapest hosts that it is able to download
from. If the ppms is 1 SC and the downloader knows it can save 10 milliseconds
by choosing more expensive hosts to download from, it will choose those hosts if
and only if the total cost of the download increases by less than 10 SC,
otherwise it will continue using the cheaper hosts. The default ppms is 100nS.

**root** | bool\
If the siapath should reference the root of the renter's filesystem.

**timeout** | int\
If 'timeout' is set, the download will fail if the Skyfile cannot be retrieved
before it expires. Note that this timeout does not cover the actual download
time, but rather covers the TTFB. Timeout is specified in seconds, a timeout
value of 0 will be ignored. If no timeout is given, the default will be used,
which is a 30 second timeout. The maximum allowed timeout is 900s (15 minutes).

### Http Headers
### OPTIONAL
**Skynet-Disable-Force** | bool\
This request header allows overruling the behaviour of the `force` parameter
that can be passed in through the query string parameters. This header is useful
for Skynet portal operators that would like to have some control over the
requests that are being passed to siad. To avoid having to parse query string
parameters and overrule them that way, this header can be set to disable the
force flag and disallow overwriting the file at the given siapath.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /skynet/portals [GET]
> curl example

```go
curl -A "Sia-Agent" "localhost:9980/skynet/portals"
```

returns the list of known Skynet portals.

### JSON Response
> JSON Response Example

```go
{
  "portals": [ // []SkynetPortal | null
    {
      "address": "siasky.net:443", // string
      "public":  true              // bool
    }
  ]
}
```
**address** | string  
The IP or domain name and the port of the portal. Must be a valid network address.

**public** | bool  
Indicates whether the portal can be accessed publicly or not.

## /skynet/portals [POST]
> curl example

```go
curl -A "Sia-Agent" --user "":<apipassword> --data '{"add" : [{"address":"siasky.net:443","public":true}]}' "localhost:9980/skynet/portals"

curl -A "Sia-Agent" --user "":<apipassword> --data '{"remove" : ["siasky.net:443"]}' "localhost:9980/skynet/portals"
```

updates the list of known Skynet portals. This endpoint can be used to both add
and remove portals from the list.

### Path Parameters
### REQUIRED
At least one of the following fields needs to be non empty.

**add** | array of SkynetPortal  
add is an array of portal info that should be added to the list of portals.

**remove** | array of string  
remove is an array of portal network addresses that should be removed from the
list of portals.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /skynet/registry [GET]
> curl example

```go
curl -A "Sia-Agent" "localhost:9980/skynet/registry?publickey=ed25519%3A69de1a15f17050e6855dd03202eed0cac31fe41865a074a43299ff4a598fe4d2&datakey=3f39b735c705edc2b3b5c5fe465da0de0a0755f5f637a556186f12687225259a
```

This curl command performs a GET request that fetches a registry entry for a publickey and datakey.  
### Query String Parameters
### REQUIRED

**publickey** | SiaPublicKey  
The public key for which to fetch the entry.

**datakey** | Hash  
The hash for which to fetch the entry.

### OPTIONAL
**timeout** | uint64  
The timeout in seconds. Specifies how long it takes the request to time out
in case no registry entry can be found. The default is the maximum allowed
value of 5 minutes. The minimum is 1 second.

### Response
> JSON Response Example

```go
{
  "data": "414141446168453132624d6c715f57663973356b35526d70652d4a4b76566c314b74416d6c70786f4a5f77613241", // []byte
  "revision": 149, // uint64
  "signature":  "03bf093a42f4df024c765fbec308a7f083fb6c1dddad485fe73810c39ed0344ff8e0db78e79bbdbad6be9d1410e2f122f58f490ff5edf7b45e3dc9fa7983ba05" // crypto.Signature
}
```

## /skynet/resolve/:skylink [GET]
> curl example

```go
curl -A "Sia-Agent" "localhost:9980/skynet/resolve/AQBUUNFGvF261JuvCZjEBQILdfB1UqVmaWuLS8MKPv82Yw"
```

This curl command performs a GET request that resolves a version 2 skylink to a version 1 skylink.

### Path Parameters
### REQUIRED
**skylink** | string  
The version 2 skylink that should be resolved.

### Query String Parameters
### OPTIONAL
**timeout** | uint64  
The timeout in seconds. Specifies how long it takes the request to time out
in case no registry entry can be found. The default is the maximum allowed
value of 5 minutes. The minimum is 1 second.

### Response
> JSON Response Example

```go
{
  "skylink": "EAAm6tEKCIostb5TT8o-lkawuWhICWqegs-Ar_kFdr1vBg", // string
}
```


## /skynet/restore [POST]
> curl example  

```go
curl -A "Sia-Agent" -u "":<apipassword> "localhost:9980/skynet/restore" --data-binary @backup.dat
```

restore a skyfile from a backup reader. The backup reader should be generated
from the `client` package method `SkynetSkylinkBackup`.

**NOTE:** The `/skynet/restore` endpoint is intended to use the backup created
with the `SkynetSkylinkBackup` `client` method. 

### Response
> JSON Response Example

```go
{
  "skylink": "CABAB_1Dt0FJsxqsu_J4TodNCbCGvtFf1Uys_3EgzOlTcg", // string
}
```

## /skynet/root [GET]
> curl example  

```bash
curl -A "Sia-Agent" "localhost:9980/skynet/root?root=QAf9Q7dBSbMarLvyeE6HTQmwhr7RX9VMrP9xIMzpU3I&offset=0&length=4096"
```  

downloads a sector of a skyfile by its root hash using http streaming. This call
blocks until the data is received. There is a 30s default timeout applied to
downloading a sector. If the data cannot be found within this 30s time
constraint, a 404 will be returned. This timeout is configurable through the
query string parameters.


### Query String Parameters
### Required
**root** | hash  
The root hash of the sector that should be downloaded.

**offset** | uint64  
The offset where the download should start within a sector.

**length** | uint64  
The amount of data to be downloaded from the sector.

### OPTIONAL

**timeout** | int  
If 'timeout' is set, the download will fail if the basesector cannot be
retrieved before it expires. Note that this timeout does not cover the actual
download time, but rather covers the TTFB. Timeout is specified in seconds,
a timeout value of 0 will be ignored. If no timeout is given, the default will
be used, which is a 30 second timeout. The maximum allowed timeout is 900s (15
minutes).

### Response Body

The response body is the raw data for the sector.

## /skynet/registry [POST]
> curl example

```go
curl -A "Sia-Agent" -u "":<apipassword> --data "<json-encoded-body>" "localhost:9980/skynet/registry"
```

> json body example
```go
{
  "publickey":{
    "algorithm":"ed25519",
    "key":"UDBtQAKGsVcdGk4LT3W3QJNhYirzCzff8T7RucKED+8="
  },
  "datakey":"5345e582d27a2ff7e3d45e2ce3d77acca0dd2cf23d3eaa5592c4095ccee502db",
  "revision":0,
  "signature":[127,39,167,244,6,164,160,7,184,232,14,101,46,148,149,73,52,108,194,195,22,46,188,46,200,20,8,5,71,1,138,216,25,4,29,105,127,63,195,46,214,64,112,72,174,228,66,84,211,254,140,18,181,203,46,199,174,173,112,8,218,238,200,6],
  "data":"AAC0rdNrjqEO2cDMonNlncRf0wu4bBs05rBWy6cQlgVMEA=="
}
```

This curl command performs a POST request that updates a registry entry for a
publickey and datakey.

### JSON Parameters
### REQUIRED

**publickey** | SiaPublicKey  
The public key for which to update the entry.

**datakey** | Hash  
The key for which to update the entry.

**revision** | uint64  
The revision of the entry. Needs to be greater than the most recent
registered entry.

**signature** | uint8 array  
64 byte signature that covers the datakey, data and revision.

**data** | string  
base64 encoded data to register. Up to 113 bytes.

### Response
standard success or error response. See [standard
responses](#standard-responses).

## /skynet/skylink/*skylink* [HEAD]
> curl example

```bash
curl -I -A "Sia-Agent" "localhost:9980/skynet/skylink/CABAB_1Dt0FJsxqsu_J4TodNCbCGvtFf1Uys_3EgzOlTcg"
```

This curl command performs a HEAD request that fetches the headers for
the given skylink. These headers are identical to the ones that would be
returned if the request had been a GET request.

### Path Parameters
See [/skynet/skylink/skylink](#skynetskylinkskylink-get)

### Query String Parameters
See [/skynet/skylink/skylink](#skynetskylinkskylink-get)

### Response Header
See [/skynet/skylink/skylink](#skynetskylinkskylink-get)

### Response Body

This request has an empty response body.

## /skynet/skylink/*skylink* [GET]
> curl example  

> Stream the whole file.  

```bash
# entire file
curl -A "Sia-Agent" "localhost:9980/skynet/skylink/CABAB_1Dt0FJsxqsu_J4TodNCbCGvtFf1Uys_3EgzOlTcg"

# directory
curl -A "Sia-Agent" "localhost:9980/skynet/skylink/CABAB_1Dt0FJsxqsu_J4TodNCbCGvtFf1Uys_3EgzOlTcg/folder"

# sub file
curl -A "Sia-Agent" "localhost:9980/skynet/skylink/CABAB_1Dt0FJsxqsu_J4TodNCbCGvtFf1Uys_3EgzOlTcg/folder/file.txt"
```  

downloads a skylink using http streaming. This call blocks until the data is
received. There is a 30s default timeout applied to downloading a skylink. If
the data cannot be found within this 30s time constraint, a 404 will be
returned. This timeout is configurable through the query string parameters.

In order to make sure skapps function correctly when they rely on relative paths
within the same skyfile, we need the skylink to be followed by a trailing slash.
If that is not the case the API responds with a redirect to the same skylink,
adding that trailing slash. This redirect only happens if the skyfile holds a 
skapp.

### Path Parameters 
### Required
**skylink** | string  
The skylink that should be downloaded. The skylink can contain an optional path.
This path can specify a directory or a particular file. If specified, only that
file or directory will be returned.

### Query String Parameters
### OPTIONAL

**attachment** | bool  
If 'attachment' is set to true, the Content-Disposition http header will be set
to 'attachment' instead of 'inline'. This will cause web browsers to download
the file as though it is an attachment instead of rendering it.

**format** | string  
If 'format' is set, the skylink can point to a directory and it will return the
data inside that directory. Format will decide the format in which it is
returned. Currently, we support the following values:  
 * 'concat' will return the concatenated data of all subfiles in that directory
 * 'tar' will return a tar archive of all subfiles in that directory
 * 'targz' will return a gzipped tar archive of all subfiles in that directory.  
 * 'zip' will return a zip archive
 
If the format is not specified, and the skylink points at a directory, we
default to the zip format and the contents will be downloaded as a zip archive.

**include-layout** | string  
If 'include-layout' is set to true, the API will return the layout in the
"Skynet-File-Layout" response header. In most cases the layout is not needed for
the download which is why it is not returned by default. Cases that require the
layout include backing up skylinks where all the original upload information
about a skylink is needed.

**start | end** | uint64  
The `start` and `end` params can be used for range requests when the client is
unable to use the range field in the Header.

**timeout** | int  
If 'timeout' is set, the download will fail if the Skyfile cannot be retrieved 
before it expires. Note that this timeout does not cover the actual download 
time, but rather covers the TTFB. Timeout is specified in seconds, a timeout 
value of 0 will be ignored. If no timeout is given, the default will be used,
which is a 30 second timeout. The maximum allowed timeout is 900s (15 minutes).

**priceperms** | string  
'price per millisecond' is a value that helps the downloader determine whether
to download from cheaper hosts or faster hosts. For a ppms of '0', the
downloader will always select the cheapest hosts that it is able to download
from. If the ppms is 1 SC and the downloader knows it can save 10 milliseconds
by choosing more expensive hosts to download from, it will choose those hosts if
and only if the total cost of the download increases by less than 10 SC,
otherwise it will continue using the cheaper hosts. The default ppms is 100nS.

### Response Header

**Skynet-File-Metadata** | SkyfileMetadata

The header field "Skynet-FileMetadata" will be set such that it has an encoded
json object which matches the skymodules.SkyfileMetadata struct. If a path was
supplied, this metadata will be relative to the given path.

> Skynet-File-Metadata Response Header Example 

```go
{
  "mode":     640,      // os.FileMode
  "filename": "folder", // string
  "monetization": {
    "license": "CAB-Ra8Zi6jew3w63SJUAKnsBRiZdpmQGLehLJbTd-b_Mg" // skylink
    "monetizers": [
      "address": "e81107109496fe714a492f557c2af4b281e4913c674d10e8b3cd5cd3b7e59c582590531607c8", // hash
      "amount": "1000000000000000000000000"                                                      // types.Currency
      "currency": "usd"                                                                          // string
    ],
  },
  "subfiles": {         // map[string]SkyfileSubfileMetadata | null
    "folder/file1.txt": {                 // string
      "mode":         640,                // os.FileMode
      "filename":     "folder/file1.txt", // string
      "contenttype":  "text/plain",       // string
      "offset":       0,                  // uint64
      "len":          6                   // uint64
    }
  }
}
```

**Skynet-Skylink** | string

The value of "Skynet-Skylink" is a string representation of the base64 encoded
Skylink that was requested.

**ETag** | string

The ETag response header contains a hash that can be supplied using the
"If-None-Match" request header. If that header is supplied, and if we find that
the requested data has not changed, siad will respond with a '304 Not Modified'
response, letting the caller know it can safely reuse it previously cached
response data.

See
https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/ETag for more
information on the ETag header.

### Response Body

The response body is the raw data for the file.

## /skynet/skyfile/*siapath* [POST]
> curl example  

```go
// This command uploads the file 'myImage.png' to the Sia folder
// 'var/skynet/images/myImage.png'. Users who download the file will see the name
// 'image.png'.
curl -A Sia-Agent -u "":<apipassword> "localhost:9980/skynet/skyfile/images/myImage.png" -F 'file=@image.png'

// This command uploads a directory with the local files `src/main.rs` and
// `src/test.c` to the Sia folder 'var/skynet/src'.
curl -A Sia-Agent -u "":<apipassword> "localhost:9980/skynet/skyfile/src?filename=src" -F 'files[]=@./src/main.rs' -F 'files[]=@./src/test.c'

// This command uploads the file 'myImage.png' to the Sia folder
// 'var/skynet/images/myImage.png' as before. This time with a monetizer that
// consists of a payout address and payout amount.
curl -A Sia-Agent -u "":<apipassword> "localhost:9980/skynet/skyfile/images/myImage.png?monetization=%7B%22monetizers%22%3A%5B%7B%22address%22%3A%22cfef78babd3faecb4fb3fdd94bdf4f9385a3cc394be4ae7a21430a425606819024ea37139e36%22%2C%22amount%22%3A%221000000000000000000000000%22%2C%22currency%22%3A%22usd%22%7D%5D%2C%22license%22%3A%22CAB-Ra8Zi6jew3w63SJUAKnsBRiZdpmQGLehLJbTd-b_Mg%22%7D" -F 'file=@image.png'
```

Uploads a file to the network using a stream. If the upload stream POST call
fails or quits before the file is fully uploaded, the file can be repaired by a
subsequent call to the upload stream endpoint using the `repair` flag.

It is also possible to upload a directory as a single piece of content using
multipart uploads. Doing this will allow you to address your content under one
skylink, and access the files by their path. This is especially useful for
webapps.

### Path Parameters
### REQUIRED
**siapath** | string  
Location where the file will reside in the renter on the network. The path must
be non-empty, may not include any path traversal strings ("./", "../"), and may
not begin with a forward-slash character. If the 'root' flag is not set, the
path will be prefixed with 'var/skynet/', placing the skyfile into the Sia
system's default skynet folder.

### Query String Parameters
### OPTIONAL
**basechunkredundancy** | uint8  
The amount of redundancy to use when uploading the base chunk. The base chunk is
the first chunk of the file, and is always uploaded using 1-of-N redundancy.

**convertpath** string  
The siapath of an existing siafile that should be converted to a skylink. A new
skyfile will be created. Both the new skyfile and the existing siafile are
required to be maintained on the network in order for the skylink to remain
active. This field is mutually exclusive with uploading streaming.

**NOTE**: Converting siafiles to skyfiles does not support skykey encryption.

**defaultpath** string  
The path to the default file whose content is to be returned when the skyfile is 
accessed at the root path. The `defaultpath` must point to a file in the root
directory of the skyfile (except for skyfiles with a single file in them). If
the `defaultpath` parameter is not provided, it will default to `index.html` 
for directories that have that file, or it will default to the only file in the 
directory, if a single file directory is uploaded. This behaviour can be 
disabled using the `disabledefaultpath` parameter. The two parameters are 
mutually exclusive and only one can be specified. Neither one is applicable to 
skyfiles without subfiles.

**disabledefaultpath** bool  
The `disabledefaultpath` allows to disable the default path behaviour. If this
parameter is set to `true`, there will be no automatic default to `index.html`,
nor to the single file in directory upload. This parameter is mutually exclusive
with `defaultpath` and specifying both will result in an error. Neither one is 
applicable to skyfiles without subfiles.

**tryfiles** | []string
The `tryfiles` field allows us to set a list of potential subfiles to return in
case the requested one does not exist or is a directory. Those subfiles might
be listed with relative or absolute paths. If the path is absolute the files
must exist.

**errorpages** | JSON
The `errorpages` JSON object defines a mapping of error codes and subfiles which
are to be served in case we are serving the respective error code. All subfiles 
referred like this must be defined with absolute paths and must exist.

**filename** | string  
The name of the file. This name will be encoded into the skyfile metadata, and
will be a part of the skylink. If the name changes, the skylink will change as
well. The name must be non-empty, may not include any path traversal strings
("./", "../"), and may not begin with a forward-slash character. When uploading
a single file using multipart form upload (the recommended method), this
parameter is optional; the name will be taken from the filename of the only
subfile.

**dryrun** | bool  
If dryrun is set to true, the request will return the Skylink of the file
without uploading the actual file to the Sia network.

**force** | bool  
If there is already a file that exists at the provided siapath, setting this
flag will cause the new file to overwrite/delete the existing file. If this flag
is not set, an error will be returned preventing the user from destroying
existing data.

**mode** | uint32  
The file mode / permissions of the file. Users who download this file will be
presented a file with this mode. If no mode is set, the default of 0644 will be
used.

**monetization** | string  
A json encoded array of monetizers. Each monetizer contains an address, a
payout amount, a license and a currency. The specified amount has to be >0,
the only supported currency is "usd" at the moment. NOTE: The precision for
$1 is the same as the siacoin precision. So `1000000000000000000000000`
equals $1.

**root** | bool  
Whether or not to treat the siapath as being relative to the root directory. If
this field is not set, the siapath will be interpreted as relative to
'var/skynet'.


**skykeyname** | string  
The name of the skykey that will be used to encrypt this skyfile. Only the
name or the ID of the skykey should be specified.

**OR**

**skykeyid** | string  
The ID of the skykey that will be used to encrypt this skyfile. Only the
name or the ID of the skykey should be specified.


### Http Headers
### OPTIONAL
**Content-Disposition** | string  
If the filename is set in the Content-Disposition field, that filename will be
used as the filename of the object being uploaded. Note that this header is only
taken into consideration when using a multipart form upload.

For more details on setting Content-Disposition:
https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Content-Disposition

**Skynet-Disable-Force** | bool  
This request header allows overruling the behaviour of the `force` parameter
that can be passed in through the query string parameters. This header is useful
for Skynet portal operators that would like to have some control over the
requests that are being passed to siad. To avoid having to parse query string
parameters and overrule them that way, this header can be set to disable the
force flag and disallow overwriting the file at the given siapath.

### Response Header

**Skynet-Skylink** | string

The value of "Skynet-Skylink" is a string representation of the base64 encoded
Skylink that was uploaded.

### JSON Response
> JSON Response Example

```go
{
"skylink":    "CABAB_1Dt0FJsxqsu_J4TodNCbCGvtFf1Uys_3EgzOlTcg" // string
"merkleroot": "QAf9Q7dBSbMarLvyeE6HTQmwhr7RX9VMrP9xIMzpU3I" // hash
"bitfield":   2048 // int
}
```
**skylink** | string  
This is the skylink that can be used with the `/skynet/skylink` GET endpoint to
retrieve the file that has been uploaded.

**merkleroot** | hash  
This is the hash that is encoded into the skylink.

**bitfield** | int  
This is the bitfield that gets encoded into the skylink. The bitfield contains a
version, an offset and a length in a heavily compressed and optimized format.


## /skynet/stats [GET]
> curl example

```go
curl -A "Sia-Agent" "localhost:9980/skynet/stats"
```

returns statistical information about Skynet, e.g. number of files uploaded

### JSON Response
```json
{
   "basesectoroverdriveavg": 1.1033519553072626,
   "basesectoroverdrivepct": 0.4666255144032922,
   "basesectorupload15mdatapoints":12.032777431483911,
   "basesectorupload15mp99ms":16384,
   "basesectorupload15mp999ms":27648,
   "basesectorupload15mp9999ms":27648,
   "chunkupload15mdatapoints":64.29178098782313,
   "chunkupload15mp99ms":30720,
   "chunkupload15mp999ms":30720,
   "chunkupload15mp9999ms":43008,
   "fanoutsectoroverdriveavg": 0.8033519553072626,
   "fanoutsectoroverdrivepct": 0.5216255144032922,
   "registryread15mdatapoints":126.31844121965291,
   "registryread15mp99ms":132,
   "registryread15mp999ms":288,
   "registryread15mp9999ms":288,
   "registrywrite15mdatapoints":6.57479081135385,
   "registrywrite15mp99ms":104,
   "registrywrite15mp999ms":216,
   "registrywrite15mp9999ms":416,
   "streambufferread15mdatapoints":1221.2823097216672,
   "streambufferread15mp99ms":5376,
   "streambufferread15mp999ms":7936,
   "streambufferread15mp9999ms":7936,
   "systemhealthscandurationhours":1.1795308075927777,
   "allowancestatus":"healthy",                         // 'low', 'high', 'healthy'
   "contractstorage":68897587855360,
   "maxhealthpercentage":100,
   "maxstorageprice":"34722222222",
   "numcritalerts":0,
   "numfiles":403016,
   "portalmode": true,                                  // bool
   "repair":385217462272,
   "storage":3635586064087,
   "stuckchunks":29948,
   "walletstatus":"healthy",                            // 'locked', 'low', 'high', 'healthy'
   "uptime":92812,
   "versioninfo":{
      "version":"1.6.0-master",                         // string
      "gitrevision":"✗-dd6ab5c88"                       // string
   }
}
```

**basesectoroverdriveavg** | float  
The average amount of overdrive workers that are launched for base sector
downloads.

**basesectoroverdrivepct** | float  
The percentage of base sector downloads that require at least one overdrive
worker in order to successfully complete the download.

**fanoutsectoroverdriveavg** | float  
The average amount of overdrive workers that are launched for fanout sector
downloads.

**fanoutsectoroverdrivepct** | float  
The percentage of fanout sector downloads that require at least one overdrive
worker in order to successfully complete the download.

**uptime** | int  
The amount of time in seconds that siad has been running.

**uploadstats** | object  
Uploadstats is an object with statistics about the data uploaded to Skynet.

**maxhealthpercentage** | float  
The maximum, i.e. worst, health of any of the portal's files represented as a percentage.

**numfiles** | int  
Numfiles is the total number of files uploaded to Skynet.

**totalsize** | int  
Totalsize is the total amount of data in bytes uploaded to Skynet.

**versioninfo** | object  
Versioninfo is an object that contains the node's version information.

**version** | string  
Version is the siad version the node is running.

**gitrevision** | string  
Gitrevision refers to the commit hash used to build siad.

Within each container, there is a bucket of half lives. Every time a data point
is added to a container, it is put in to every bucket, counting up the total
number of requests. The buckets decay at the stated half life, which means they
give a good representation of how much activity there has been over twice their
halflife. So for the one minute bucket, the total number of datapoints in the
bucket is a good representation of how many things have happened in the past two
minutes.

Within each bucket, there are several fields. For example, the n60ms field
represents the number of requests that finished in under 60ms. There is an NErr
field which gets incremented if there is a failure that can be attributed to
siad.

Every download request will go into the TimeToFirstByte container, as well as
the appropriate download container based on the size of the download. Within the
chosen containers, every bucket will have the same field incremented. The field
that gets incremented is the one that corresponds to the amount of time the
request took.

The performance stats fields are not protected by a compatibility promise, and
may change over time.

**registrystats**  
Contains some stats about the skynet registry. 

**readprojectpX** | uint64  
The Xth percentile of the execution time of all successful read registry
projects.

## /skynet/unpin/:skylink [POST]
> curl example

```go
curl -A "Sia-Agent" "localhost:9980/skynet/unpin/CABAB_1Dt0FJsxqsu_J4TodNCbCGvtFf1Uys_3EgzOlTcg"
```

Unpinning a skylink will delete the underlying skyfile(s) from the portal. This
will delete any skyfile that has the skylink associated with it.

**NOTE:** There is no siapath stored in the layout or metadata of a skylink
because skyfiles can be pinned to different siapaths on the same or different
portals. Because of this, all skyfiles and siafiles on a portal will need to be
checked to see if the skylink is contained in the file's metadata. For
performance reasons, this process is handled in a background thread and is not
ACID. This means, if there is a shutdown of any kind before all the files have
been checked, the unpin request may not be successfully executed and will need
to be resubmitted. Submitting an unpin request multiple times for the same
skylink is OK and does not duplicate the background unpinning process. If two
`healthCheckInterval`s have passed, it can be assumed that the unpin was
successful. 

### Path Parameters
### REQUIRED
**skylink** | string\
The skylink that should be unpinned.

### Query Parameters
### OPTIONAL
**siapath** | string\
The siapath of the skyfile that should be unpinned.

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /skynet/addskykey [POST]
> curl example

```go
curl -A "Sia-Agent"  -u "":<apipassword> --data "skykey=BAAAAAAAAABrZXkxAAAAAAAAAAQgAAAAAAAAADiObVg49-0juJ8udAx4qMW-TEHgDxfjA0fjJSNBuJ4a" "localhost:9980/skynet/addskykey"
```

Stores the given skykey with the skykey manager.

### Path Parameters
### REQUIRED
**skykey** | string  
base-64 encoded skykey

### Response

standard success or error response. See [standard
responses](#standard-responses).

## /skynet/skykeys [GET]
> curl example

```go
curl -A "Sia-Agent"  -u "":<apipassword> --data "localhost:9980/skynet/skykeys"
```

Returns a list of all Skykeys.

### JSON Response

> JSON Response Example

```go
{
  "skykeys": [
  {
    "skykey": "skykey:AUI0eAOXWXHwW6KOLyI5O1OYduVvHxAA8qUR_fJ8Kluasb-ykPlHBEjDczrL21hmjhH0zAoQ3-Qq?name=testskykey1",
    "name": "testskykey1",
    "id": "ai5z8cf5NWbcvPBaBn0DFQ==",
    "type": "private-id"
  },
  {
    "skykey": "skykey:AUqG0aQmgzCIlse2JxFLBGHCriZNz20IEKQu81XxYsak3rzmuVbZ2P6ZqeJHIlN5bjPqEmC67U8E?name=testskykey2",
    "name": "testskykey2",
    "id": "bi5z8cf5NWbcvPBaBn0DFQ==",
    "type": "private-id"
  },
  {
    "skykey": "skykey:AShQI8fzxoIMc52ZRkoKjOE50bXnCpiPd4zrBl_E-CkmyLgfinAJSdWkJT2QOR6XCRYYgZb63OHw?name=testskykey3",
    "name": "testskykey3",
    "id": "ci5z8cf5NWbcvPBaBn0DFQ==",
    "type": "public-id"
  }
}
```

**skykeys** | []skykeys
Array of skykeys. See the documentation for /skynet/skykey for more detailed
information.



## /skynet/createskykey [POST]
> curl example

```go
curl -A "Sia-Agent"  -u "":<apipassword> --data "name=key_to_the_castle&type=private-id" "localhost:9980/skynet/createskykey"
```

Returns a new skykey created and stored under that name.

### Path Parameters
### REQUIRED
**name** | string  
desired name of the skykey

**type** | string  
desired type of the skykey. The two supported types are "public-id" and
"private-id". Users should use "private-id" skykeys unless they have a specific
reason to use "public-id" skykeys which reveal skykey IDs and show which
skyfiles are encrypted with the same skykey.


### JSON Response
> JSON Response Example

```go
{
  "skykey": "skykey:AUI0eAOXWXHwW6KOLyI5O1OYduVvHxAA8qUR_fJ8Kluasb-ykPlHBEjDczrL21hmjhH0zAoQ3-Qq?name=testskykey1",
  "name": "key_to_the_castle",
  "id": "ai5z8cf5NWbcvPBaBn0DFQ==",
  "type": "private-id"
}
```

**skykey** | skykey  
Skykey. See the documentation for /skynet/skykey for more detailed information.


## /skynet/deleteskykey [POST]
> curl example

```go
curl -A "Sia-Agent"  -u "":<apipassword> --data "name=key_to_the_castle" "localhost:9980/skynet/deleteskykey"
```

Deletes the skykey with that name or ID.

### Path Parameters
### REQUIRED
**name** | string  
name of the skykey being deleted

or

**id** | string  
base-64 encoded ID of the skykey being deleted


### Response
standard success or error response, a successful response means the skykey was
deleted.
See [standard responses](#standard-responses).


## /skynet/skykey [GET]
> curl example

```go
curl -A "Sia-Agent"  -u "":<apipassword> --data "name=key_to_the_castle" "localhost:9980/skynet/skykey"
curl -A "Sia-Agent"  -u "":<apipassword> --data "id=gi5z8cf5NWbcvPBaBn0DFQ==" "localhost:9980/skynet/skykey"
```

Returns the base-64 encoded skykey along with its name and ID.


### Path Parameters
### REQUIRED
**name** | string  
name of the skykey being queried

or

**id** | string  
base-64 encoded ID of the skykey being queried


### JSON Response

```go
{
  "skykey": "skykey:AShQI8fzxoIMc52ZRkoKjOE50bXnCpiPd4zrBl_E-CkmyLgfinAJSdWkJT2QOR6XCRYYgZb63OHw?name=testskykey",
  "name": "testskykey",
  "id": "gi5z8cf5NWbcvPBaBn0DFQ==",
  "type": "private-id"
}
```

**skykey** | string  
base-64 encoded skykey

**name** | string  
name of the skykey

**id** | string  
base-64 encoded skykey ID

**type** | string  
human-readable skykey type. See the documentation for /skynet/createskykey for
type information.

# Versions
