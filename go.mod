module gitlab.com/SkynetLabs/skyd

go 1.13

replace go.sia.tech/siad v1.5.7 => github.com/peterjan/siad v1.5.10

require (
	github.com/HdrHistogram/hdrhistogram-go v1.1.0 // indirect
	github.com/aead/chacha20 v0.0.0-20180709150244-8b13a72661da
	github.com/dchest/threefish v0.0.0-20120919164726-3ecf4c494abf
	github.com/eventials/go-tus v0.0.0-20200718001131-45c7ec8f5d59
	github.com/gorilla/websocket v1.4.2
	github.com/hanwen/go-fuse/v2 v2.1.0
	github.com/julienschmidt/httprouter v1.3.0
	github.com/klauspost/cpuid/v2 v2.0.6 // indirect
	github.com/klauspost/reedsolomon v1.9.12
	github.com/montanaflynn/stats v0.6.3
	github.com/opentracing/opentracing-go v1.1.0
	github.com/spf13/cobra v1.1.3
	github.com/square/mongo-lock v0.0.0-20201208161834-4db518ed7fb2
	github.com/tus/tusd v1.6.0
	github.com/uber/jaeger-client-go v2.27.0+incompatible
	github.com/uber/jaeger-lib v2.4.1+incompatible
	github.com/vbauerster/mpb/v5 v5.0.3
	gitlab.com/NebulousLabs/encoding v0.0.0-20200604091946-456c3dc907fe
	gitlab.com/NebulousLabs/entropy-mnemonics v0.0.0-20181018051301-7532f67e3500
	gitlab.com/NebulousLabs/errors v0.0.0-20200929122200-06c536cf6975
	gitlab.com/NebulousLabs/fastrand v0.0.0-20181126182046-603482d69e40
	gitlab.com/NebulousLabs/log v0.0.0-20200604091839-0ba4a941cdc2
	gitlab.com/NebulousLabs/ratelimit v0.0.0-20200811080431-99b8f0768b2e
	gitlab.com/NebulousLabs/siamux v0.0.0-20210824082138-a4ebafe4b9d9
	gitlab.com/NebulousLabs/threadgroup v0.0.0-20200608151952-38921fbef213
	gitlab.com/NebulousLabs/writeaheadlog v0.0.0-20200618142844-c59a90f49130
	go.mongodb.org/mongo-driver v1.4.2
	go.sia.tech/siad v1.5.7
	golang.org/x/crypto v0.0.0-20210322153248-0c34fe9e7dc2
	golang.org/x/net v0.0.0-20210410081132-afb366fc7cd1
)
