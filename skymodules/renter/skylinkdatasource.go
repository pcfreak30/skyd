package renter

import (
	"context"

	"github.com/opentracing/opentracing-go"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skykey"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"

	"gitlab.com/NebulousLabs/errors"
)

var (
	// SkylinkDataSourceRequestSize is the size that is suggested by the data
	// source to be used when reading data from it.
	SkylinkDataSourceRequestSize = build.Select(build.Var{
		Dev:      uint64(1 << 18), // 256 KiB
		Standard: uint64(1 << 20), // 1 MiB
		Testing:  uint64(1 << 9),  // 512 B
	}).(uint64)
)

type (
	// skylinkDataSource implements streamBufferDataSource on a Skylink.
	// Notably, it creates a pcws for every single chunk in the Skylink and
	// keeps them in memory, to reduce latency on seeking through the file.
	skylinkDataSource struct {
		// Metadata.
		staticID          skymodules.DataSourceID
		staticLayout      skymodules.SkyfileLayout
		staticMetadata    skymodules.SkyfileMetadata
		staticRawMetadata []byte
		staticSkylink     skymodules.Skylink

		// staticBaseSectorPayload will contain the raw data for the skylink
		// if there is no fanout. However if there's a fanout it will be nil.
		staticBaseSectorPayload []byte

		// staticChunkFetchers contains one pcws for every chunk in the fanout.
		// The worker sets are spun up in advance so that the HasSector queries
		// have completed by the time that someone needs to fetch the data.
		//
		// a staticChunkFetcher cannot be used until the corresponding
		// staticChunksReady channel has been closed, it will not exist until
		// that point. After it is known to exist, the error needs to be
		// checked.
		staticChunkFetchers []chunkFetcher
		staticChunksReady   []chan struct{}
		staticChunkErrs     []error

		// Utilities
		staticCtx        context.Context
		staticCancelFunc context.CancelFunc
		staticRenter     *Renter
	}
)

// DataSize implements streamBufferDataSource
func (sds *skylinkDataSource) DataSize() uint64 {
	return sds.staticLayout.Filesize
}

// ID implements streamBufferDataSource
func (sds *skylinkDataSource) ID() skymodules.DataSourceID {
	return sds.staticID
}

// Layout implements streamBufferDataSource
func (sds *skylinkDataSource) Layout() skymodules.SkyfileLayout {
	return sds.staticLayout
}

// Metadata implements streamBufferDataSource
func (sds *skylinkDataSource) Metadata() skymodules.SkyfileMetadata {
	return sds.staticMetadata
}

// RawMetadata implements streamBufferDataSource
func (sds *skylinkDataSource) RawMetadata() []byte {
	return sds.staticRawMetadata
}

// RequestSize implements streamBufferDataSource
func (sds *skylinkDataSource) RequestSize() uint64 {
	return SkylinkDataSourceRequestSize
}

// Skylink implements streamBufferDataSource
func (sds *skylinkDataSource) Skylink() skymodules.Skylink {
	return sds.staticSkylink
}

// SilentClose implements streamBufferDataSource
func (sds *skylinkDataSource) SilentClose() {
	// Cancelling the context for the data source should be sufficient. As all
	// child processes (such as the pcws for each chunk) should be using
	// contexts derived from the sds context.
	sds.staticCancelFunc()
}

// ReadStream implements streamBufferDataSource
func (sds *skylinkDataSource) ReadStream(ctx context.Context, off, fetchSize uint64, pricePerMS types.Currency) chan *readResponse {
	// Prepare the response channel
	responseChan := make(chan *readResponse, 1)
	if off+fetchSize > sds.staticLayout.Filesize {
		responseChan <- &readResponse{
			staticErr: errors.New("given offset and fetchsize exceed the underlying filesize"),
		}
		return responseChan
	}

	// If there's data in the base sector payload it means we are dealing with a
	// small skyfile without fanout bytes. This means we can simply read from
	// that and return early.
	baseSectorPayloadLen := uint64(len(sds.staticBaseSectorPayload))
	if baseSectorPayloadLen != 0 {
		bytesLeft := baseSectorPayloadLen - off
		if fetchSize > bytesLeft {
			fetchSize = bytesLeft
		}
		responseChan <- &readResponse{
			staticData: sds.staticBaseSectorPayload[off : off+fetchSize],
		}
		return responseChan
	}

	// Determine how large each chunk is.
	chunkSize := skymodules.ChunkSize(sds.staticLayout.CipherType, uint64(sds.staticLayout.FanoutDataPieces))

	// Prepare an array of download chans on which we'll receive the data.
	numChunks := fetchSize / chunkSize
	if fetchSize%chunkSize != 0 {
		numChunks += 1
	}
	downloadChans := make([]chan *downloadResponse, 0, numChunks)

	// Otherwise we are dealing with a large skyfile and have to aggregate the
	// download responses for every chunk in the fanout. We keep reading from
	// chunks until all the data has been read.
	var n uint64
	for n < fetchSize && off < sds.staticLayout.Filesize {
		// Determine which chunk the offset is currently in.
		chunkIndex := off / chunkSize
		offsetInChunk := off % chunkSize
		remainingBytes := fetchSize - n

		// Determine how much data to read from the chunk.
		remainingInChunk := chunkSize - offsetInChunk
		downloadSize := remainingInChunk
		if remainingInChunk > remainingBytes {
			downloadSize = remainingBytes
		}

		// Wait until the chunk fetcher is ready, and check if there was any
		// error in initializing the chunk fetcher.
		select {
		case <-sds.staticChunksReady[chunkIndex]:
		case <-sds.staticRenter.tg.StopChan():
			responseChan <- &readResponse{
				staticErr: errors.New("stream fetch aborted because of renter shutdown"),
			}
			return responseChan
		}
		if sds.staticChunkErrs[chunkIndex] != nil {
			responseChan <- &readResponse{
				staticErr: errors.AddContext(sds.staticChunkErrs[chunkIndex], "unable to start download"),
			}
			return responseChan
		}

		// Schedule the download.
		respChan, err := sds.staticChunkFetchers[chunkIndex].Download(ctx, pricePerMS, offsetInChunk, downloadSize, false, false)
		if err != nil {
			responseChan <- &readResponse{
				staticErr: errors.AddContext(err, "unable to start download"),
			}
			return responseChan
		}
		downloadChans = append(downloadChans, respChan)

		off += downloadSize
		n += downloadSize
	}

	// Launch a goroutine that collects all download responses, aggregates them
	// and sends it as a single response over the response channel.
	err := sds.staticRenter.tg.Launch(func() {
		data := make([]byte, fetchSize)
		offset := 0
		failed := false

		for _, respChan := range downloadChans {
			resp := <-respChan
			if resp.err == nil {
				n := copy(data[offset:], resp.data)
				offset += n
				continue
			}
			if !failed {
				failed = true
				responseChan <- &readResponse{staticErr: resp.err}
				close(responseChan)
			}
		}

		if !failed {
			responseChan <- &readResponse{staticData: data}
			close(responseChan)
		}
	})
	if err != nil {
		responseChan <- &readResponse{staticErr: err}
	}
	return responseChan
}

// managedDownloadByRoot will fetch data using the merkle root of that data.
func (r *Renter) managedDownloadByRoot(ctx context.Context, root crypto.Hash, offset, length uint64, pricePerMS types.Currency) ([]byte, *pcwsWorkerState, error) {
	// Create a context that dies when the function ends, this will cancel all
	// of the worker jobs that get created by this function.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Capture the base sector download in a new span.
	span, ctx := opentracing.StartSpanFromContext(ctx, "managedDownloadByRoot")
	span.SetTag("root", root)
	defer span.Finish()

	// Create the pcws for the first chunk. We use a passthrough cipher and
	// erasure coder. If the base sector is encrypted, we will notice and be
	// able to decrypt it once we have fully downloaded it and are able to
	// access the layout. We can make the assumption on the erasure coding being
	// of 1-N seeing as we currently always upload the basechunk using 1-N
	// redundancy.
	ptec := skymodules.NewPassthroughErasureCoder()
	tpsk, err := crypto.NewSiaKey(crypto.TypePlain, nil)
	if err != nil {
		return nil, nil, errors.AddContext(err, "unable to create plain skykey")
	}
	pcws, err := r.newPCWSByRoots(ctx, []crypto.Hash{root}, ptec, tpsk, 0)
	if err != nil {
		return nil, nil, errors.AddContext(err, "unable to create the worker set for this skylink")
	}

	// Download the base sector. The base sector contains the metadata, without
	// it we can't provide a completed data source.
	//
	// NOTE: we pass in the provided context here, if the user imposed a timeout
	// on the download request, this will fire if it takes too long.
	respChan, err := pcws.managedDownload(ctx, pricePerMS, offset, length, false, false)
	if err != nil {
		return nil, nil, errors.AddContext(err, "unable to start download")
	}
	resp := <-respChan
	if resp.err != nil {
		return nil, nil, errors.AddContext(resp.err, "base sector download did not succeed")
	}
	baseSector := resp.data
	if len(baseSector) < skymodules.SkyfileLayoutSize {
		return nil, nil, errors.New("download did not fetch enough data, layout cannot be decoded")
	}

	return baseSector, pcws.managedWorkerState(), nil
}

// managedSkylinkDataSource will create a streamBufferDataSource for the data
// contained inside of a Skylink. The function will not return until the base
// sector and all skyfile metadata has been retrieved.
//
// NOTE: Skylink data sources are cached and outlive the user's request because
// multiple different callers may want to use the same data source. We do have
// to pass in a context though to adhere to a possible user-imposed request
// timeout. This can be optimized to always create the data source when it was
// requested, but we should only do so after gathering some real world feedback
// that indicates we would benefit from this.
func (r *Renter) managedSkylinkDataSource(ctx context.Context, skylink skymodules.Skylink, pricePerMS types.Currency) (streamBufferDataSource, error) {
	// Get the offset and fetchsize from the skylink
	offset, fetchSize, err := skylink.OffsetAndFetchSize()
	if err != nil {
		return nil, errors.AddContext(err, "unable to parse skylink")
	}

	// Download the base sector. The base sector contains the metadata, without
	// it we can't provide a completed data source.
	//
	// NOTE: we pass in the provided context here, if the user imposed a timeout
	// on the download request, this will fire if it takes too long.
	baseSector, _, err := r.managedDownloadByRoot(ctx, skylink.MerkleRoot(), offset, fetchSize, pricePerMS)
	if err != nil {
		return nil, errors.AddContext(err, "unable to download base sector")
	}

	// Check if the base sector is encrypted, and attempt to decrypt it.
	// This will fail if we don't have the decryption key.
	var fileSpecificSkykey skykey.Skykey
	if skymodules.IsEncryptedBaseSector(baseSector) {
		fileSpecificSkykey, err = r.managedDecryptBaseSector(baseSector)
		if err != nil {
			return nil, errors.AddContext(err, "unable to decrypt skyfile base sector")
		}
	}

	// Parse out the metadata of the skyfile.
	// TODO: (f/u?) it might be better to resolve the parts of the fanout we
	// need on demand. But that's quite the undertaking by itself.
	// e.g. if we don't start resolving the recursive fanout right away we
	// lose the benefit of the workerset, because we will add some latency
	// later once we actually know what the user wants to download.
	layout, fanoutBytes, metadata, rawMetadata, baseSectorPayload, _, err := r.ParseSkyfileMetadata(baseSector)
	if err != nil {
		return nil, errors.AddContext(err, "unable to parse skyfile metadata")
	}

	// Tag the span with its size. We tag it with 64kb, 1mb, 4mb and 10mb as
	// those are the size increments used by the benchmark tool. This way we can
	// run the benchmark and then filter the results using these tags.
	//
	// NOTE: the sizes used are "exact sizes", meaning they are as close as
	// possible to their eventual size after taking into account the size of the
	// metadata. See cmd/skynet-benchmark/dl.go for more info.
	span := opentracing.SpanFromContext(ctx)
	switch length := metadata.Length; {
	case length <= 61e3:
		span.SetTag("length", "64kb")
	case length <= 982e3:
		span.SetTag("length", "1mb")
	case length <= 3931e3:
		span.SetTag("length", "4mb")
	default:
		span.SetTag("length", "10mb")
	}

	// Create the context for the data source - a child of the renter
	// threadgroup but otherwise independent.
	dsCtx, cancelFunc := context.WithCancel(r.tg.StopCtx())

	// Attach the span to the ctx
	dsCtx = opentracing.ContextWithSpan(dsCtx, span)

	// If there's a fanout create a PCWS for every chunk.
	var fanoutChunkFetchers []chunkFetcher
	var fanoutChunksReady []chan struct{}
	var fanoutChunkErrs []error
	if len(fanoutBytes) > 0 {
		// Derive the fanout key
		fanoutKey, err := skymodules.DeriveFanoutKey(&layout, fileSpecificSkykey)
		if err != nil {
			cancelFunc()
			return nil, errors.AddContext(err, "unable to derive encryption key")
		}

		// Create the erasure coder
		ec, err := skymodules.NewRSSubCode(int(layout.FanoutDataPieces), int(layout.FanoutParityPieces), crypto.SegmentSize)
		if err != nil {
			cancelFunc()
			return nil, errors.AddContext(err, "unable to derive erasure coding settings for fanout")
		}

		// Create the list of chunks from the fanout.
		fanoutChunks, err := layout.DecodeFanoutIntoChunks(fanoutBytes)
		if err != nil {
			cancelFunc()
			return nil, errors.AddContext(err, "error parsing skyfile fanout")
		}

		// Initialize the fanout chunk fetchers. To improve TTFB, the list is
		// returned prior to all of the PCWS objects actually being created,
		// because the pcws objects will sometimes block on network connections
		// before returning. That blocking is desirable behavior, because it
		// ensures that earlier chunks are ready first. What's not desirable is
		// blocking any downloads at all until the final PCWS has been
		// scheduled, so instead we allocate an array of blank chunk fetchers
		// and update them one at a time, then close channels to specify when
		// they are ready for use.
		numChunks := len(fanoutChunks)
		fanoutChunkFetchers = make([]chunkFetcher, numChunks)
		fanoutChunksReady = make([]chan struct{}, numChunks)
		fanoutChunkErrs = make([]error, numChunks)
		for i := 0; i < numChunks; i++ {
			fanoutChunksReady[i] = make(chan struct{})
		}

		// Initialize all of the PCWS objects in a goroutine, closing the
		// channels as they are ready.
		err = r.tg.Launch(func() {
			for i, chunk := range fanoutChunks {
				pcws, err := r.newPCWSByRoots(dsCtx, chunk, ec, fanoutKey, uint64(i))
				fanoutChunkErrs[i] = err
				fanoutChunkFetchers[i] = pcws
				close(fanoutChunksReady[i])
			}
		})
		if err != nil {
			cancelFunc()
			return nil, errors.AddContext(err, "unable to launch thread to create chunk fetchers")
		}
	}

	sds := &skylinkDataSource{
		staticID:          skylink.DataSourceID(),
		staticLayout:      layout,
		staticMetadata:    metadata,
		staticRawMetadata: rawMetadata,
		staticSkylink:     skylink,

		staticBaseSectorPayload: baseSectorPayload,
		staticChunkFetchers:     fanoutChunkFetchers,
		staticChunksReady:       fanoutChunksReady,
		staticChunkErrs:         fanoutChunkErrs,

		staticCtx:        dsCtx,
		staticCancelFunc: cancelFunc,
		staticRenter:     r,
	}
	return sds, nil
}
