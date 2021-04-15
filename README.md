# [<img src='https://siasky.net/AAAm79DFoiOyJXiFmPqDr-GFUNwgytYK3VAVy67cFTG7-A' width="75%" >](http://siasky.net)

[![Build Status](https://gitlab.com/SkynetLabs/skyd/badges/master/pipeline.svg)](https://gitlab.com/SkynetLabs/skyd/commits/master)
[![Coverage Report](https://gitlab.com/SkynetLabs/skyd/badges/master/coverage.svg)](https://gitlab.com/SkynetLabs/skyd/commits/master)
[![GoDoc](https://godoc.org/gitlab.com/SkynetLabs/skyd?status.svg)](https://godoc.org/gitlab.com/SkynetLabs/skyd)
[![Go Report Card](https://goreportcard.com/badge/gitlab.com/SkynetLabs/skyd)](https://goreportcard.com/report/gitlab.com/SkynetLabs/skyd)
[![Skynet License](https://img.shields.io/badge/license-skynet%20license-blue)](https://gitlab.com/SkynetLabs/skyd/-/blob/master/LICENSE.md)

Skynet is a content and application hosting platform bringing
decentralized storage to users, creators and app developers.

Skynet is build on [Sia](https://github.com/SiaFoundation), the leading
decentralized data storage platform.

This repo contains the code for `skyd` which is required for running a minimum
Skynet Portal. For more information on what a portal is and how it interacts
with the rest of the Skynet ecosytem, head over to the [Skynet Support
Docs](https://support.siasky.net/getting-started/skynet-basics) for the most up
to date documentation. 

License
-----

Skyd uses a custom [License](./LICENSE.md). The Skynet License is a source
code license that allows you to use, modify and distribute the software, but
you must preserve the payment mechanism in the software.

For the purposes of complying with our code license, you can use the
following Siacoin address:

`fb6c9320bc7e01fbb9cd8d8c3caaa371386928793c736837832e634aaaa484650a3177d6714a`

Usage
-----

The majority of Skynet users don't need to interact with `skyd`. If you are
looking to get started using Skynet, we recommended heading over to our
[Public Web Portal](https://siasky.net) and sign up for an account
[here](https://account.siasky.net). For developers, check out our information
on our [Developers page](https://siasky.net/developers).

This repo and README are for those looking to contribute to `skyd` development,
or are interested in running their own Skynet Portal.

`skyd` releases comes with 2 binaries, `skyd` and `skyc`. `skyd` is a
background service, or "daemon," that runs the `skyd` protocol and exposes an
HTTP API on port 9980.  `skyc` is a command-line client that can be used to
interact with `skyd` in a user-friendly way. For interested developers, the
`skyd` API is documented
[here](https://gitlab.com/SkynetLabs/skyd/-/blob/master/doc/api/index.html.md).
**NOTE:** this API is for building directly on `skyd`. Most Skynet developers
will only need to interact with the [Skynet SDK](https://siasky.net/docs).

`skyd` and `skyc` are run via command prompt. On Windows, you can just double-
click `skyd`.exe if you don't need to specify any command-line arguments.
Otherwise, navigate to its containing folder and click File->Open command
prompt. Then, start the `skyd` service by entering `skyd` and pressing Enter.
The command prompt may appear to freeze; this means `skyd` is waiting for
requests. Windows users may see a warning from the Windows Firewall; be sure
to check both boxes ("Private networks" and "Public networks") and click
"Allow access." You can now run `skyc` (in a separate command prompt) or Sia-
UI to interact with `skyd`. From here, you can send money, upload and download
files, and advertise yourself as a host.

Building From Source
--------------------

To build from source, [Go 1.13 or above must be
installed](https://golang.org/doc/install) on the system. Clone the repo and
run `make`:

``
git clone https://gitlab.com/SkynetLabs/skyd
cd skyd && make dependencies && make
``

This will install the `skyd` and `skyc` binaries in your `$GOPATH/bin` folder.
(By default, this is `$HOME/go/bin`.) You can find more information about
`$GOPATH` [here](https://github.com/golang/go/wiki/GOPATH).

You can also run `make test` and `make test-long` to run the short and full test
suites, respectively. Finally, `make cover` will generate code coverage reports
for each package; they are stored in the `cover` folder and can be viewed in
your browser.

Official Releases
--------------------
Official binaries can be found under
[Releases](https://gitlab.com/SkynetLabs/skyd/-/releases)

Additionally, an official Docker image can be found
[here](https://hub.docker.com/r/skynetlabs/skyd).
