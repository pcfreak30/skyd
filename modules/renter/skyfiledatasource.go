package renter

// skylinkDataSource implements streamBufferDataSource on a Skylink. Notably, it
// creates a pcws for every single chunk in the Skylink and keeps them in
// memory, to reduce latency on seeking through the file.
type skylinkDataSource struct {
}

// skylinkDataSource will create a streamBufferDataSource for the data contained
// inside of a Skylink.
//
// NOTE: we could adjust the skylinkDataSource to be returned almost
// immediately, and then block on a channel that waits until the base sector has
// been downloaded to return any of the data source methods. The streamBuffer
// may be implemented however such that it expects some of the data methods to
// return immediately, so currently this method is implemented to block on
// returning until the base sector has been downloaded and processed.
func (r *Renter) skylinkDataSource(ctx context.Context, pricePerMs types.Currency, link modules.Skylink) (streamBufferDataSource, error) {
	// Create the pcws for the first chunk, which is just a single root with
	// both passthrough encryption and passthrough erasure coding.
	ec := modules.NewPassthroughErasureCoder()
	sk, err := crypto.NewSiaKey(crypto.TypePlain, nil)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create plain skykey")
	}
	pcws, err := r.newPCWSByRoots(ctx, []crypto.Hash{link.MerkleRoot()}, ec, sk, 0)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create the worker set for this skylink")
	}

	// Download the base sector. The base sector contains the metadata, without
	// it we can't provide a completed data source.
	offset, fetchSize, err := link.OffsetAndFetchSize()
	if err != nil {
		return nil, errors.AddContext(err, "unable to parse skylink")
	}
	respChan, err := pcws.managedDownload(ctx, pricePerMs, offset, fetchSize)
	if err != nil {
		return nil, errors.AddContext(err, "unable to start download")
	}
	resp := <-respChan
	if resp.err !=  nil {
		return nil, errors.AddContext(err, "base sector download did not succeed")
	}
	baseSector := resp.data
	if len(baseSector) < SkyfileLayoutSize {
		return modules.SkyfileMetadata{}, nil, errors.New("download did not fetch enough data, layout cannot be decoded")
	}

	// Check if the base sector is encrypted, and attempt to decrypt it.
	// This will fail if we don't have the decryption key.
	var fileSpecificSkykey skykey.Skykey
	if isEncryptedBaseSector(baseSector) {
		fileSpecificSkykey, err = r.decryptBaseSector(baseSector)
		if err != nil {
			return modules.SkyfileMetadata{}, nil, errors.AddContext(err, "unable to decrypt skyfile base sector")
		}
	}

	// Parse out the metadata of the skyfile.
	layout, fanoutBytes, metadata, baseSectorPayload, err := parseSkyfileMetadata(baseSector)
	if err != nil {
		return modules.SkyfileMetadata{}, nil, errors.AddContext(err, "error parsing skyfile metadata")
	}

	/*
		// If there is no fanout, all of the data will be contained in the base
		// sector, return a streamer using the data from the base sector.
		if layout.fanoutSize == 0 {
			streamer := streamerFromSlice(baseSectorPayload)
			return metadata, streamer, nil
		}

		// There is a fanout, create a fanout streamer and return that.
		fs, err := r.newFanoutStreamer(link, layout, metadata, fanoutBytes, timeout, fileSpecificSkykey)
		if err != nil {
			return modules.SkyfileMetadata{}, nil, errors.AddContext(err, "unable to create fanout fetcher")
		}
		return metadata, fs, nil
	*/
}
