package renter

// skyfile.go provides the tools for creating and uploading skyfiles, and then
// receiving the associated skylinks to recover the files. The skyfile is the
// fundamental data structure underpinning Skynet.
//
// The primary trick of the skyfile is that the initial data is stored entirely
// in a single sector which is put on the Sia network using 1-of-N redundancy.
// Every replica has an identical Merkle root, meaning that someone attempting
// to fetch the file only needs the Merkle root and then some way to ask hosts
// on the network whether they have access to the Merkle root.
//
// That single sector then contains all of the other information that is
// necessary to recover the rest of the file. If the file is small enough, the
// entire file will be stored within the single sector. If the file is larger,
// the Merkle roots that are needed to download the remaining data get encoded
// into something called a 'fanout'. While the base chunk is required to use
// 1-of-N redundancy, the fanout chunks can use more sophisticated redundancy.
//
// The 1-of-N redundancy requirement really stems from the fact that Skylinks
// are only 34 bytes of raw data, meaning that there's only enough room in a
// Skylink to encode a single root. The fanout however has much more data to
// work with, meaning there is space to describe much fancier redundancy schemes
// and data fetching patterns.
//
// Skyfiles also contain some metadata which gets encoded as json. The
// intention is to allow uploaders to put any arbitrary metadata fields into
// their file and know that users will be able to see that metadata after
// downloading. A couple of fields such as the mode of the file are supported at
// the base level by Sia.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/opentracing/opentracing-go"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/fixtures"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/skykey"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter/filesystem"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

var (
	// MaxSkylinkV2ResolvingDepth defines the maximum recursion depth the
	// renter tries to resolve when downloading v2 skylinks.
	MaxSkylinkV2ResolvingDepth = build.Select(build.Var{
		Standard: uint8(2),
		Dev:      uint8(5),
		Testing:  uint8(5),
	}).(uint8)
)

var (
	// SkyfileDefaultBaseChunkRedundancy establishes the default redundancy for
	// the base chunk of a skyfile.
	SkyfileDefaultBaseChunkRedundancy = build.Select(build.Var{
		Dev:      uint8(2),
		Standard: uint8(10),
		Testing:  uint8(2),
	}).(uint8)
)

var (
	// ErrEncryptionNotSupported is the error returned when Skykey encryption is
	// not supported for a Skynet action.
	ErrEncryptionNotSupported = errors.New("skykey encryption not supported")

	// ErrInvalidMetadata is the error returned when the metadata is not valid.
	ErrInvalidMetadata = errors.New("metadata is invalid")

	// ErrMetadataTooBig is the error returned when the metadata exceeds a
	// sectorsize.
	ErrMetadataTooBig = errors.New("metadata exceeds sectorsize")

	// ErrSkylinkBlocked is the error returned when a skylink is blocked
	ErrSkylinkBlocked = errors.New("skylink is blocked")

	// ErrInvalidSkylinkVersion is returned when an operation fails due to the
	// skylink having the wrong version.
	ErrInvalidSkylinkVersion = errors.New("skylink had unexpected version")
)

// skyfileEstablishDefaults will set any zero values in the lup to be equal to
// the desired defaults.
func skyfileEstablishDefaults(lup *skymodules.SkyfileUploadParameters) {
	if lup.BaseChunkRedundancy == 0 {
		lup.BaseChunkRedundancy = SkyfileDefaultBaseChunkRedundancy
	}
}

// fileUploadParams will create an erasure coder and return the FileUploadParams
// to use when uploading using the provided parameters.
func fileUploadParams(siaPath skymodules.SiaPath, dataPieces, parityPieces int, force bool, ct crypto.CipherType) (skymodules.FileUploadParams, error) {
	// Create the erasure coder
	ec, err := skymodules.NewRSSubCode(dataPieces, parityPieces, crypto.SegmentSize)
	if err != nil {
		return skymodules.FileUploadParams{}, errors.AddContext(err, "unable to create erasure coder")
	}

	// Return the FileUploadParams
	return skymodules.FileUploadParams{
		SiaPath:             siaPath,
		ErasureCode:         ec,
		Force:               force,
		DisablePartialChunk: true,  // must be set to true - partial chunks change, content addressed files must not change.
		Repair:              false, // indicates whether this is a repair operation
		CipherType:          ct,
	}, nil
}

// baseSectorUploadParamsFromSUP will derive the FileUploadParams to use when
// uploading the base chunk siafile of a skyfile using the skyfile's upload
// parameters.
func baseSectorUploadParamsFromSUP(sup skymodules.SkyfileUploadParameters) (skymodules.FileUploadParams, error) {
	// Establish defaults
	skyfileEstablishDefaults(&sup)

	// Create parameters to upload the file with 1-of-N erasure coding and no
	// encryption. This should cause all of the pieces to have the same Merkle
	// root, which is critical to making the file discoverable to viewnodes and
	// also resilient to host failures.
	return fileUploadParams(sup.SiaPath, 1, int(sup.BaseChunkRedundancy)-1, sup.Force, crypto.TypePlain)
}

// streamerFromReader wraps a bytes.Reader to give it a Close() method, which
// allows it to satisfy the skymodules.Streamer interface.
type streamerFromReader struct {
	*bytes.Reader
}

// skylinkStreamerFromReader wraps a streamerFromReader to give it a Metadata()
// method, which allows it to satisfy the modules.SkyfileStreamer interface.
type skylinkStreamerFromReader struct {
	modules.Streamer
	staticLayout  skymodules.SkyfileLayout
	staticMD      skymodules.SkyfileMetadata
	staticRawMD   []byte
	staticSkylink skymodules.Skylink
}

// Close is a no-op because a bytes.Reader doesn't need to be closed.
func (sfr *streamerFromReader) Close() error {
	return nil
}

// StreamerFromSlice returns a skymodules.Streamer given a slice. This is
// non-trivial because a bytes.Reader does not implement Close.
func StreamerFromSlice(b []byte) skymodules.Streamer {
	reader := bytes.NewReader(b)
	return &streamerFromReader{
		Reader: reader,
	}
}

// SkylinkStreamerFromSlice creates a modules.SkyfileStreamer from a byte slice.
func SkylinkStreamerFromSlice(b []byte, md skymodules.SkyfileMetadata, rawMD []byte, skylink skymodules.Skylink, layout skymodules.SkyfileLayout) skymodules.SkyfileStreamer {
	streamer := StreamerFromSlice(b)
	return &skylinkStreamerFromReader{
		Streamer:      streamer,
		staticLayout:  layout,
		staticMD:      md,
		staticRawMD:   rawMD,
		staticSkylink: skylink,
	}
}

// Layout implements the skymodules.SkyfileStreamer interface.
func (sfr *skylinkStreamerFromReader) Layout() skymodules.SkyfileLayout {
	return sfr.staticLayout
}

// Metadata implements the skymodules.SkyfileStreamer interface.
func (sfr *skylinkStreamerFromReader) Metadata() skymodules.SkyfileMetadata {
	return sfr.staticMD
}

// RawMetadata implements the modules.SkyfileStreamer interface.
func (sfr *skylinkStreamerFromReader) RawMetadata() []byte {
	return sfr.staticRawMD
}

// Skylink implements the modules.SkyfileStreamer interface.
func (sfr *skylinkStreamerFromReader) Skylink() skymodules.Skylink {
	return sfr.staticSkylink
}

// CreateSkylinkFromSiafile creates a skyfile from a siafile. This requires
// uploading a new skyfile which contains fanout information pointing to the
// siafile data. The SiaPath provided in 'sup' indicates where the new base
// sector skyfile will be placed, and the siaPath provided as its own input is
// the siaPath of the file that is being used to create the skyfile.
func (r *Renter) CreateSkylinkFromSiafile(sup skymodules.SkyfileUploadParameters, siaPath skymodules.SiaPath) (_ skymodules.Skylink, err error) {
	// Encryption is not supported for SiaFile conversion.
	if encryptionEnabled(&sup) {
		return skymodules.Skylink{}, errors.AddContext(ErrEncryptionNotSupported, "unable to convert siafile")
	}
	// Set reasonable default values for any sup fields that are blank.
	skyfileEstablishDefaults(&sup)

	// Grab the filenode for the provided siapath.
	fileNode, err := r.staticFileSystem.OpenSiaFile(siaPath)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to open siafile")
	}
	defer func() {
		err = errors.Compose(err, fileNode.Close())
	}()

	// Override the metadata with the info from the fileNode.
	metadata := skymodules.SkyfileMetadata{
		Filename:     siaPath.Name(),
		Mode:         fileNode.Mode(),
		Monetization: sup.Monetization,
		Length:       fileNode.Size(),
	}

	// Generate the fanoutBytes
	dataPieces := fileNode.ErasureCode().MinPieces()
	cipherType := fileNode.Metadata().StaticMasterKeyType
	onlyOnePieceNeeded := dataPieces == 1 && cipherType == crypto.TypePlain
	fanoutBytes, err := skyfileEncodeFanoutFromFileNode(fileNode, onlyOnePieceNeeded)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to generate the fanout bytes")
	}

	return r.managedCreateSkylinkFromFileNode(r.tg.StopCtx(), sup, metadata, fileNode, fanoutBytes)
}

// managedCreateSkylinkFromFileNode creates a skylink from a file node.
//
// The name needs to be passed in explicitly because a file node does not track
// its own name, which allows the file to be renamed concurrently without
// causing any race conditions.
func (r *Renter) managedCreateSkylinkFromFileNode(ctx context.Context, sup skymodules.SkyfileUploadParameters, skyfileMetadata skymodules.SkyfileMetadata, fileNode *filesystem.FileNode, fanoutBytes []byte) (skymodules.Skylink, error) {
	// Check if the given metadata is valid
	err := skymodules.ValidateSkyfileMetadata(skyfileMetadata)
	if err != nil {
		return skymodules.Skylink{}, errors.Compose(ErrInvalidMetadata, err)
	}

	// Check if any of the skylinks associated with the siafile are blocked
	if r.managedIsFileNodeBlocked(fileNode) {
		err = ErrSkylinkBlocked
		// Skylink is blocked, return error and try and delete file
		deleteErr := r.DeleteFile(sup.SiaPath)
		// Don't bother returning an error if the file doesn't exist
		if !errors.Contains(deleteErr, filesystem.ErrNotExist) {
			err = errors.Compose(err, deleteErr)
		}
		return skymodules.Skylink{}, err
	}

	// Check that the encryption key and erasure code is compatible with the
	// skyfile format. This is intentionally done before any heavy computation
	// to catch errors early on.
	var sl skymodules.SkyfileLayout
	masterKey := fileNode.MasterKey()
	if len(masterKey.Key()) > len(sl.KeyData) {
		return skymodules.Skylink{}, errors.New("cipher key is not supported by the skyfile format")
	}
	ec := fileNode.ErasureCode()
	if ec.Type() != skymodules.ECReedSolomonSubShards64 {
		return skymodules.Skylink{}, errors.New("siafile has unsupported erasure code type")
	}

	// Marshal the metadata.
	metadataBytes, err := skymodules.SkyfileMetadataBytes(skyfileMetadata)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "error retrieving skyfile metadata bytes")
	}

	// Check the header size.
	headerSize := uint64(skymodules.SkyfileLayoutSize + len(metadataBytes) + len(fanoutBytes))
	if headerSize > modules.SectorSize {
		return skymodules.Skylink{}, errors.AddContext(ErrMetadataTooBig, fmt.Sprintf("skyfile does not fit in leading chunk - metadata size plus fanout size must be less than %v bytes, metadata size is %v bytes and fanout size is %v bytes", modules.SectorSize-skymodules.SkyfileLayoutSize, len(metadataBytes), len(fanoutBytes)))
	}

	// Assemble the first chunk of the skyfile.
	sl = skymodules.SkyfileLayout{
		Version:            skymodules.SkyfileVersion,
		Filesize:           fileNode.Size(),
		MetadataSize:       uint64(len(metadataBytes)),
		FanoutSize:         uint64(len(fanoutBytes)),
		FanoutDataPieces:   uint8(ec.MinPieces()),
		FanoutParityPieces: uint8(ec.NumPieces() - ec.MinPieces()),
		CipherType:         masterKey.Type(),
	}
	// If we're uploading in plaintext, we put the key in the baseSector
	if !encryptionEnabled(&sup) {
		copy(sl.KeyData[:], masterKey.Key())
	}

	// Create the base sector.
	baseSector, fetchSize := skymodules.BuildBaseSector(sl.Encode(), fanoutBytes, metadataBytes, nil)

	// Encrypt the base sector if necessary.
	if encryptionEnabled(&sup) {
		err = encryptBaseSectorWithSkykey(baseSector, sl, sup.FileSpecificSkykey)
		if err != nil {
			return skymodules.Skylink{}, errors.AddContext(err, "Failed to encrypt base sector for upload")
		}
	}

	// Create the skylink.
	baseSectorRoot := crypto.MerkleRoot(baseSector)
	skylink, err := skymodules.NewSkylinkV1(baseSectorRoot, 0, fetchSize)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to build skylink")
	}
	if sup.DryRun {
		return skylink, nil
	}

	// Check if the new skylink is blocked
	if r.staticSkynetBlocklist.IsBlocked(skylink) {
		err = ErrSkylinkBlocked
		// Skylink is blocked, return error and try and delete file
		deleteErr := r.DeleteFile(sup.SiaPath)
		// Don't bother returning an error if the file doesn't exist
		if !errors.Contains(deleteErr, filesystem.ErrNotExist) {
			err = errors.Compose(err, deleteErr)
		}
		return skymodules.Skylink{}, err
	}

	// Add the skylink to the siafiles.
	err = fileNode.AddSkylink(skylink)
	if err != nil {
		return skylink, errors.AddContext(err, "unable to add skylink to the sianodes")
	}

	// Upload the base sector.
	err = r.managedUploadBaseSector(ctx, sup, baseSector, skylink)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "Unable to upload base sector for file node. ")
	}

	return skylink, errors.AddContext(err, "unable to add skylink to the sianodes")
}

// managedPopulateFileNodeFromReader takes the fileNode and a reader and returns
// a populated filenode without uploading any data. It is used to perform a
// dry-run of a skyfile upload.
func (r *Renter) managedPopulateFileNodeFromReader(fileNode *filesystem.FileNode, reader skymodules.ChunkReader) error {
	// Extract some helper variables
	hpk := types.SiaPublicKey{} // blank host key
	csize := fileNode.ChunkSize()

	for chunkIndex := uint64(0); ; chunkIndex++ {
		// Allocate data pieces and fill them with data from r.
		dataEncoded, total, errRead := reader.ReadChunk()
		if errors.Contains(errRead, io.EOF) {
			return nil
		}
		if errRead != nil {
			return errRead
		}
		// If no more data is read from the stream we are done.
		if total == 0 {
			return nil // done
		}

		// Grow the SiaFile to the right size.
		err := fileNode.SiaFile.GrowNumChunks(chunkIndex + 1)
		if err != nil {
			return err
		}

		for pieceIndex, dataPieceEnc := range dataEncoded {
			if err := fileNode.SiaFile.AddPiece(hpk, chunkIndex, uint64(pieceIndex), crypto.MerkleRoot(dataPieceEnc)); err != nil {
				return err
			}
		}

		adjustedSize := fileNode.Size() - csize + total
		if err := fileNode.SetFileSize(adjustedSize); err != nil {
			return errors.AddContext(err, "failed to adjust FileSize")
		}
	}
}

// Blocklist returns the merkleroots that are on the blocklist
func (r *Renter) Blocklist() ([]crypto.Hash, error) {
	err := r.tg.Add()
	if err != nil {
		return []crypto.Hash{}, err
	}
	defer r.tg.Done()
	return r.staticSkynetBlocklist.Blocklist(), nil
}

// UpdateSkynetBlocklist updates the list of hashed merkleroots that are blocked
func (r *Renter) UpdateSkynetBlocklist(additions, removals []crypto.Hash) error {
	err := r.tg.Add()
	if err != nil {
		return err
	}
	defer r.tg.Done()
	return r.staticSkynetBlocklist.UpdateBlocklist(additions, removals)
}

// Portals returns the list of known skynet portals.
func (r *Renter) Portals() ([]skymodules.SkynetPortal, error) {
	err := r.tg.Add()
	if err != nil {
		return []skymodules.SkynetPortal{}, err
	}
	defer r.tg.Done()
	return r.staticSkynetPortals.Portals(), nil
}

// UpdateSkynetPortals updates the list of known Skynet portals that are listed.
func (r *Renter) UpdateSkynetPortals(additions []skymodules.SkynetPortal, removals []modules.NetAddress) error {
	err := r.tg.Add()
	if err != nil {
		return err
	}
	defer r.tg.Done()
	return r.staticSkynetPortals.UpdatePortals(additions, removals)
}

// managedUploadBaseSector will take the raw baseSector bytes and upload them,
// returning the resulting merkle root, and the fileNode of the siafile that is
// tracking the base sector.
func (r *Renter) managedUploadBaseSector(ctx context.Context, sup skymodules.SkyfileUploadParameters, baseSector []byte, skylink skymodules.Skylink) (err error) {
	// Trace the base sector upload in its own span if the given ctx already has
	// a span attached.
	span, ctx := opentracing.StartSpanFromContext(ctx, "managedUploadBaseSector")
	span.SetTag("skylink", skylink.String())
	defer func() {
		if err != nil {
			span.LogKV("err", err)
		}
		span.SetTag("success", err == nil)
		span.Finish()
	}()

	uploadParams, err := baseSectorUploadParamsFromSUP(sup)
	if err != nil {
		return errors.AddContext(err, "failed to create siafile upload parameters")
	}

	// Turn the base sector into a reader
	reader := bytes.NewReader(baseSector)

	// Perform the actual upload.
	fileNode, err := r.callUploadStreamFromReader(ctx, uploadParams, reader)
	if err != nil {
		return errors.AddContext(err, "failed to stream upload base sector")
	}
	defer func() {
		// If there was an error, try and delete the file that was created
		if err != nil {
			deleteErr := r.DeleteFile(sup.SiaPath)
			// Don't bother returning an error if the file doesn't exist
			if !errors.Contains(deleteErr, filesystem.ErrNotExist) {
				err = errors.Compose(err, deleteErr)
			}
		}
		err = errors.Compose(err, fileNode.Close())
	}()

	// Add the skylink to the Siafile.
	err = fileNode.AddSkylink(skylink)
	return errors.AddContext(err, "unable to add skylink to siafile")
}

// managedUploadSkyfile uploads a file and returns the skylink and whether or
// not it was a large file.
func (r *Renter) managedUploadSkyfile(ctx context.Context, sup skymodules.SkyfileUploadParameters, reader skymodules.SkyfileUploadReader) (skymodules.Skylink, error) {
	// see if we can fit the entire upload in a single chunk
	buf := make([]byte, modules.SectorSize)
	numBytes, err := io.ReadFull(reader, buf)
	buf = buf[:numBytes] // truncate the buffer

	// if we've reached EOF, we can safely fetch the metadata and calculate the
	// actual header size, if that fits in a single sector we can upload the
	// Skyfile as a small file
	if errors.Contains(err, io.EOF) || errors.Contains(err, io.ErrUnexpectedEOF) {
		// get the skyfile metadata from the reader
		metadata, err := reader.SkyfileMetadata(ctx)
		if err != nil {
			return skymodules.Skylink{}, errors.AddContext(err, "unable to get skyfile metadata")
		}

		// check whether it's valid
		err = skymodules.ValidateSkyfileMetadata(metadata)
		if err != nil {
			return skymodules.Skylink{}, errors.Compose(ErrInvalidMetadata, err)
		}
		// marshal the skyfile metadata into bytes
		metadataBytes, err := skymodules.SkyfileMetadataBytes(metadata)
		if err != nil {
			return skymodules.Skylink{}, errors.AddContext(err, "unable to get skyfile metadata bytes")
		}

		// verify if it fits in a single chunk
		headerSize := uint64(skymodules.SkyfileLayoutSize + len(metadataBytes))
		if uint64(numBytes)+headerSize <= modules.SectorSize {
			return r.managedUploadSkyfileSmallFile(ctx, sup, metadataBytes, buf)
		}
	}

	// if we reach this point it means either we have not reached the EOF or the
	// data combined with the header exceeds a single sector, we add the data we
	// already read and upload as a large file
	reader.SetReadBuffer(buf)
	// set buffer nil to allow for GC to pick it up before starting the upload.
	// That way it won't stick around until the upload is done.
	buf = nil
	return r.managedUploadSkyfileLargeFile(ctx, sup, reader)
}

// managedUploadSkyfileSmallFile uploads a file that fits entirely in the
// leading chunk of a skyfile to the Sia network and returns the skylink that
// can be used to access the file.
func (r *Renter) managedUploadSkyfileSmallFile(ctx context.Context, sup skymodules.SkyfileUploadParameters, metadataBytes, fileBytes []byte) (skylink skymodules.Skylink, err error) {
	// Fetch the span from our context and tag it as small (large=false).
	if span := opentracing.SpanFromContext(ctx); span != nil {
		defer func() {
			if err != nil {
				span.LogKV("err", err)
			}
			span.SetTag("large", false)
		}()
	}

	sl := skymodules.SkyfileLayout{
		Version:      skymodules.SkyfileVersion,
		Filesize:     uint64(len(fileBytes)),
		MetadataSize: uint64(len(metadataBytes)),
		// No fanout is set yet.
		// If encryption is set in the upload params, this will be overwritten.
		CipherType: crypto.TypePlain,
	}

	// Create the base sector. This is done as late as possible so that any
	// errors are caught before a large block of memory is allocated.
	baseSector, fetchSize := skymodules.BuildBaseSector(sl.Encode(), nil, metadataBytes, fileBytes) // 'nil' because there is no fanout

	if encryptionEnabled(&sup) {
		err = encryptBaseSectorWithSkykey(baseSector, sl, sup.FileSpecificSkykey)
		if err != nil {
			return skymodules.Skylink{}, errors.AddContext(err, "Failed to encrypt base sector for upload")
		}
	}

	// Create the skylink.
	baseSectorRoot := crypto.MerkleRoot(baseSector) // Should be identical to the sector roots for each sector in the siafile.
	skylink, err = skymodules.NewSkylinkV1(baseSectorRoot, 0, fetchSize)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "failed to build the skylink")
	}

	// If this is a dry-run, we do not need to upload the base sector
	if sup.DryRun {
		return skylink, nil
	}

	// Upload the base sector.
	start := time.Now()
	err = r.managedUploadBaseSector(ctx, sup, baseSector, skylink)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "failed to upload base sector")
	}
	r.staticBaseSectorUploadStats.AddDataPoint(time.Since(start))
	return skylink, nil
}

// managedUploadSkyfileLargeFile will accept a fileReader containing all of the
// data to a large siafile and upload it to the Sia network using
// 'callUploadStreamFromReader'. The final skylink is created by calling
// 'CreateSkylinkFromSiafile' on the resulting siafile.
func (r *Renter) managedUploadSkyfileLargeFile(ctx context.Context, sup skymodules.SkyfileUploadParameters, fileReader skymodules.SkyfileUploadReader) (skylink skymodules.Skylink, err error) {
	// Fetch the span from our context and tag it as large.
	if span := opentracing.SpanFromContext(ctx); span != nil {
		defer func() {
			if err != nil {
				span.LogKV("err", err)
			}
			span.SetTag("large", true)
		}()
	}

	// Create the siapath for the skyfile extra data. This is going to be the
	// same as the skyfile upload siapath, except with a suffix.
	siaPath, err := sup.SiaPath.AddSuffixStr(skymodules.ExtendedSuffix)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to create SiaPath for large skyfile extended data")
	}

	// Disrupt and use custom redundancy if the StandardUploadRedundancy
	// dependency is set.
	dataPieces := skymodules.RenterDefaultDataPieces
	parityPieces := skymodules.RenterDefaultParityPieces
	if r.staticDeps.Disrupt("StandardUploadRedundancy") {
		dataPieces = 10
		parityPieces = 20
	}

	// Create the FileUploadParams
	fup, err := fileUploadParams(siaPath, dataPieces, parityPieces, sup.Force, crypto.TypePlain)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to create FileUploadParams for large file")
	}

	// Generate a Cipher Key for the FileUploadParams.
	err = generateCipherKey(&fup, sup)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to create Cipher key for FileUploadParams")
	}

	// Check the upload params first and create a fileNode.
	fileNode, err := r.managedInitUploadStream(fup)
	if err != nil {
		return skymodules.Skylink{}, err
	}
	// Defer closing the file
	defer func() {
		// If there was an error, try and delete the file that was created
		if err != nil {
			deleteErr := r.DeleteFile(sup.SiaPath)
			// Don't bother returning an error if the file doesn't exist
			if !errors.Contains(deleteErr, filesystem.ErrNotExist) {
				err = errors.Compose(err, deleteErr)
			}
		}
		err = errors.Compose(err, fileNode.Close())
	}()

	// Figure out how to create the fanout. If only one piece is needed, we
	// create it from the node directly after the upload.
	cipherType := fileNode.MasterKey().Type()
	dataPieces = fileNode.ErasureCode().MinPieces()
	onlyOnePieceNeeded := dataPieces == 1 && cipherType == crypto.TypePlain

	// Wrap the reader in a FanoutChunkReader.
	cr := NewFanoutChunkReader(fileReader, fileNode.ErasureCode(), onlyOnePieceNeeded, fileNode.MasterKey())
	if sup.DryRun {
		// In case of a dry-run we don't want to perform the actual upload,
		// instead we create a filenode that contains all of the data pieces and
		// their merkle roots.
		err = r.managedPopulateFileNodeFromReader(fileNode, cr)
	} else {
		// Upload the file using a streamer.
		_, err = r.callUploadStreamFromReaderWithFileNode(ctx, fileNode, cr, 0)
	}
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "failed to upload file")
	}

	// If there was no reader then the fanout creation failed. We need to create
	// the fanout from the fileNode in that case.
	var fanout []byte
	if fileReader != nil {
		fanout = cr.Fanout()
	} else {
		fanout, err = skyfileEncodeFanoutFromFileNode(fileNode, onlyOnePieceNeeded)
	}
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "failed to compute fanout")
	}

	// Get the SkyfileMetadata from the reader object.
	metadata, err := fileReader.SkyfileMetadata(ctx)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to get skyfile metadata")
	}

	// Convert the new siafile we just uploaded into a skyfile using the
	// convert function.
	skylink, err = r.managedCreateSkylinkFromFileNode(ctx, sup, metadata, fileNode, fanout)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to create skylink from filenode")
	}
	return skylink, nil
}

// DownloadByRoot will fetch data using the merkle root of that data. This uses
// all of the async worker primitives to improve speed and throughput.
func (r *Renter) DownloadByRoot(root crypto.Hash, offset, length uint64, timeout time.Duration, pricePerMS types.Currency) ([]byte, error) {
	if err := r.tg.Add(); err != nil {
		return nil, err
	}
	defer r.tg.Done()

	// Check if the merkleroot is blocked
	if r.staticSkynetBlocklist.IsHashBlocked(crypto.HashObject(root)) {
		return nil, ErrSkylinkBlocked
	}

	// Create the context
	ctx := r.tg.StopCtx()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(r.tg.StopCtx(), timeout)
		defer cancel()
	}

	// Start tracing.
	span := opentracing.StartSpan("DownloadByRoot")
	span.SetTag("root", root)
	defer span.Finish()

	// Attach the span to the ctx
	ctx = opentracing.ContextWithSpan(ctx, span)

	// Fetch the data
	data, err := r.managedDownloadByRoot(ctx, root, offset, length, pricePerMS)
	if errors.Contains(err, ErrProjectTimedOut) {
		err = errors.AddContext(err, fmt.Sprintf("timed out after %vs", timeout.Seconds()))
	}
	return data, err
}

// DownloadSkylink will take a link and turn it into the metadata and data of a
// download.
func (r *Renter) DownloadSkylink(link skymodules.Skylink, timeout time.Duration, pricePerMS types.Currency) (skymodules.SkyfileStreamer, []skymodules.RegistryEntry, error) {
	if err := r.tg.Add(); err != nil {
		return nil, nil, err
	}
	defer r.tg.Done()

	// Create a context
	ctx := r.tg.StopCtx()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(r.tg.StopCtx(), timeout)
		defer cancel()
	}

	// Create a new span.
	span := opentracing.StartSpan("DownloadSkylink")
	span.SetTag("skylink", link.String())

	// Attach the span to the ctx
	ctx = opentracing.ContextWithSpan(ctx, span)

	// Check if link needs to be resolved from V2 to V1.
	link, srvs, err := r.managedTryResolveSkylinkV2Recursive(ctx, link)
	if err != nil {
		return nil, nil, err
	}

	// Check if link resolved to a v1 link.
	if !link.IsSkylinkV1() {
		return nil, nil, errors.AddContext(ErrRootNotFound, "failed to fully resolve skylink - max number of recursions reached")
	}

	// Check if link is blocked
	if r.staticSkynetBlocklist.IsBlocked(link) {
		return nil, nil, ErrSkylinkBlocked
	}

	// Download the data
	streamer, err := r.managedDownloadSkylink(ctx, link, timeout, pricePerMS)
	if errors.Contains(err, ErrProjectTimedOut) {
		span.LogKV("timeout", timeout)
		span.SetTag("timeout", true)
		err = errors.AddContext(err, fmt.Sprintf("timed out after %vs", timeout.Seconds()))
	}

	return streamer, srvs, err
}

// DownloadSkylinkBaseSector will take a link and turn it into the data of
// a basesector without any decoding of the metadata, fanout, or decryption.
func (r *Renter) DownloadSkylinkBaseSector(link skymodules.Skylink, timeout time.Duration, pricePerMS types.Currency) (skymodules.Streamer, []skymodules.RegistryEntry, error) {
	if err := r.tg.Add(); err != nil {
		return nil, nil, err
	}
	defer r.tg.Done()

	// Create the context
	ctx := r.tg.StopCtx()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(r.tg.StopCtx(), timeout)
		defer cancel()
	}

	// Create a span
	span := opentracing.StartSpan("DownloadSkylinkBaseSector")
	span.SetTag("skylink", link.String())
	defer span.Finish()

	// Attach the span to the ctx
	ctx = opentracing.ContextWithSpan(ctx, span)

	// Check if link needs to be resolved from V2 to V1.
	link, srvs, err := r.managedTryResolveSkylinkV2Recursive(ctx, link)
	if err != nil {
		return nil, nil, err
	}

	// Check if link is blocked
	if r.staticSkynetBlocklist.IsBlocked(link) {
		return nil, nil, ErrSkylinkBlocked
	}

	// Find the fetch size.
	offset, fetchSize, err := link.OffsetAndFetchSize()
	if err != nil {
		return nil, nil, errors.AddContext(err, "unable to get offset and fetch size")
	}

	// Download the base sector
	baseSector, err := r.managedDownloadByRoot(ctx, link.MerkleRoot(), offset, fetchSize, pricePerMS)
	return StreamerFromSlice(baseSector), srvs, err
}

// managedDownloadSkylink will take a link and turn it into the metadata and
// data of a download.
func (r *Renter) managedDownloadSkylink(ctx context.Context, link skymodules.Skylink, streamReadTimeout time.Duration, pricePerMS types.Currency) (skymodules.SkyfileStreamer, error) {
	if r.staticDeps.Disrupt("resolveSkylinkToFixture") {
		sf, err := fixtures.LoadSkylinkFixture(link)
		if err != nil {
			return nil, errors.AddContext(err, "failed to fetch fixture")
		}
		rawMD, err := json.Marshal(sf.Metadata)
		if err != nil {
			return nil, errors.AddContext(err, "failed to fetch fixture")
		}
		return SkylinkStreamerFromSlice(sf.Content, sf.Metadata, rawMD, link, skymodules.SkyfileLayout{}), err
	}

	// Get the span from our context and defer cached tag update.
	var exists bool
	span := opentracing.SpanFromContext(ctx)
	defer func() {
		span.SetTag("cached", exists)
	}()

	// Check if this skylink is already in the stream buffer set. If so, we can
	// skip the lookup procedure and use any data that other threads have
	// cached.
	id := link.DataSourceID()
	var stream *stream
	stream, exists = r.staticStreamBufferSet.callNewStreamFromID(ctx, id, 0, streamReadTimeout)
	if exists {
		return stream, nil
	}

	// Create the data source and add it to the stream buffer set.
	dataSource, err := r.managedSkylinkDataSource(ctx, link, pricePerMS)
	if err != nil {
		return nil, errors.AddContext(err, "unable to create data source for skylink")
	}
	stream = r.staticStreamBufferSet.callNewStream(ctx, dataSource, 0, streamReadTimeout, pricePerMS)
	return stream, nil
}

// PinSkylink will fetch the file associated with the Skylink, and then pin all
// necessary content to maintain that Skylink.
func (r *Renter) PinSkylink(skylink skymodules.Skylink, lup skymodules.SkyfileUploadParameters, timeout time.Duration, pricePerMS types.Currency) error {
	err := r.tg.Add()
	if err != nil {
		return err
	}
	defer r.tg.Done()

	// Check if link is v2.
	if skylink.IsSkylinkV2() {
		return errors.New("can't pin version 2 skylink")
	}

	// Check if link is blocked
	if r.staticSkynetBlocklist.IsBlocked(skylink) {
		return ErrSkylinkBlocked
	}

	// Create a context.
	ctx := r.tg.StopCtx()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Create a span.
	span := opentracing.StartSpan("PinSkylink")
	span.SetTag("skylink", skylink.String())
	defer span.Finish()

	// Attach the span to the ctx
	ctx = opentracing.ContextWithSpan(ctx, span)

	// Fetch the leading chunk.
	baseSector, err := r.DownloadByRoot(skylink.MerkleRoot(), 0, modules.SectorSize, timeout, pricePerMS)
	if err != nil {
		return errors.AddContext(err, "unable to fetch base sector of skylink")
	}
	if uint64(len(baseSector)) != modules.SectorSize {
		return errors.New("download did not fetch enough data, file cannot be re-pinned")
	}

	// Check if the base sector is encrypted, and attempt to decrypt it.
	var fileSpecificSkykey skykey.Skykey
	encrypted := skymodules.IsEncryptedBaseSector(baseSector)
	if encrypted {
		fileSpecificSkykey, err = r.managedDecryptBaseSector(baseSector)
		if err != nil {
			return errors.AddContext(err, "Unable to decrypt skyfile base sector")
		}
	}

	// Parse out the metadata of the skyfile.
	layout, _, _, _, _, err := skymodules.ParseSkyfileMetadata(baseSector)
	if err != nil {
		return errors.AddContext(err, "error parsing skyfile metadata")
	}

	// Set sane defaults for unspecified values.
	skyfileEstablishDefaults(&lup)

	// Start setting up the FUP.
	fup := skymodules.FileUploadParams{
		Force:               lup.Force,
		DisablePartialChunk: true,  // must be set to true - partial chunks change, content addressed files must not change.
		Repair:              false, // indicates whether this is a repair operation
		CipherType:          crypto.TypePlain,
	}

	// Re-encrypt the baseSector for upload and add the fanout key to the fup.
	if encrypted {
		err = encryptBaseSectorWithSkykey(baseSector, layout, fileSpecificSkykey)
		if err != nil {
			return errors.AddContext(err, "Error re-encrypting base sector")
		}

		// Derive the fanout key and add to the fup.
		fanoutSkykey, err := fileSpecificSkykey.DeriveSubkey(skymodules.FanoutNonceDerivation[:])
		if err != nil {
			return errors.AddContext(err, "Error deriving fanout skykey")
		}
		fup.CipherKey, err = fanoutSkykey.CipherKey()
		if err != nil {
			return errors.AddContext(err, "Error getting fanout CipherKey")
		}
		fup.CipherType = fanoutSkykey.CipherType()

		// These fields aren't used yet, but we'll set them anyway to mimic
		// behavior in upload/download code for consistency.
		lup.SkykeyName = fileSpecificSkykey.Name
		lup.FileSpecificSkykey = fileSpecificSkykey
	}

	// Re-upload the baseSector.
	err = r.managedUploadBaseSector(ctx, lup, baseSector, skylink)
	if err != nil {
		return errors.AddContext(err, "unable to upload base sector")
	}

	// If there is no fanout, nothing more to do, the pin is complete.
	if layout.FanoutSize == 0 {
		return nil
	}
	// Create the erasure coder to use when uploading the file bulk.
	fup.ErasureCode, err = skymodules.NewRSSubCode(int(layout.FanoutDataPieces), int(layout.FanoutParityPieces), crypto.SegmentSize)
	if err != nil {
		return errors.AddContext(err, "unable to create erasure coder for large file")
	}
	// Create the siapath for the skyfile extra data. This is going to be the
	// same as the skyfile upload siapath, except with a suffix.
	fup.SiaPath, err = lup.SiaPath.AddSuffixStr(skymodules.ExtendedSuffix)
	if err != nil {
		return errors.AddContext(err, "unable to create SiaPath for large skyfile extended data")
	}

	// Create the data source and add it to the stream buffer set.
	dataSource, err := r.managedSkylinkDataSource(ctx, skylink, pricePerMS)
	if err != nil {
		return errors.AddContext(err, "unable to create data source for skylink")
	}
	stream := r.staticStreamBufferSet.callNewStream(ctx, dataSource, 0, timeout, pricePerMS)

	// Upload directly from the stream.
	fileNode, err := r.callUploadStreamFromReader(ctx, fup, stream)
	if err != nil {
		return errors.AddContext(err, "unable to upload large skyfile")
	}
	err = fileNode.AddSkylink(skylink)
	if err != nil {
		return errors.AddContext(err, "unable to upload skyfile fanout")
	}
	return nil
}

// RestoreSkyfile restores a skyfile from disk such that the skylink is
// preserved.
func (r *Renter) RestoreSkyfile(reader io.Reader) (skymodules.Skylink, error) {
	// Restore the skylink and baseSector from the reader
	skylinkStr, baseSector, err := skymodules.RestoreSkylink(reader)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to restore skyfile from backup")
	}

	// Load the skylink
	var skylink skymodules.Skylink
	err = skylink.LoadString(skylinkStr)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to load skylink")
	}

	// Check if the new skylink is blocked
	if r.staticSkynetBlocklist.IsBlocked(skylink) {
		return skymodules.Skylink{}, ErrSkylinkBlocked
	}

	// Check if the base sector is encrypted, and attempt to decrypt it.
	// This will fail if we don't have the decryption key.
	var fileSpecificSkykey skykey.Skykey
	encrypted := skymodules.IsEncryptedBaseSector(baseSector)
	if encrypted {
		fileSpecificSkykey, err = r.managedDecryptBaseSector(baseSector)
		if err != nil {
			return skymodules.Skylink{}, errors.AddContext(err, "Unable to decrypt skyfile base sector")
		}
	}

	// Parse the baseSector.
	sl, _, sm, _, _, err := skymodules.ParseSkyfileMetadata(baseSector)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "error parsing the baseSector")
	}

	// Create the upload parameters
	siaPath, err := skymodules.SkynetFolder.Join(skylinkStr)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to create siapath")
	}
	sup := skymodules.SkyfileUploadParameters{
		BaseChunkRedundancy: sl.FanoutDataPieces + sl.FanoutParityPieces,
		SiaPath:             siaPath,

		// Set filename and mode
		Filename: sm.Filename,
		Mode:     sm.Mode,

		// Set the default path params
		DefaultPath:        sm.DefaultPath,
		DisableDefaultPath: sm.DisableDefaultPath,

		// Set the directory resolution params
		DirResMode:         sm.DirResMode,
		DirResNotFound:     sm.DirResNotFound,
		DirResNotFoundCode: sm.DirResNotFoundCode,
	}
	skyfileEstablishDefaults(&sup)

	// Re-encrypt the baseSector for upload and set the Skykey fields of the
	// sup.
	if encrypted {
		err = encryptBaseSectorWithSkykey(baseSector, sl, fileSpecificSkykey)
		if err != nil {
			return skymodules.Skylink{}, errors.AddContext(err, "error re-encrypting base sector")
		}

		// Set the Skykey fields
		sup.SkykeyName = fileSpecificSkykey.Name
		sup.FileSpecificSkykey = fileSpecificSkykey
	}

	// Create the SkyfileUploadReader for the restoration
	var restoreReader skymodules.SkyfileUploadReader
	if len(sm.Subfiles) == 0 {
		restoreReader = skymodules.NewSkyfileReader(reader, sup)
	} else {
		// Create multipart reader from the subfiles
		multiReader, err := skymodules.NewMultipartReader(reader, sm.Subfiles)
		if err != nil {
			return skymodules.Skylink{}, errors.AddContext(err, "unable to create multireader")
		}
		restoreReader = skymodules.NewSkyfileMultipartReader(multiReader, sup)
	}

	// Upload the Base Sector of the skyfile
	err = r.managedUploadBaseSector(r.tg.StopCtx(), sup, baseSector, skylink)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "failed to upload base sector")
	}

	// If there was no fanout then we are done.
	if sl.FanoutSize == 0 {
		return skylink, nil
	}

	// Create erasure coder and FileUploadParams
	extendedPath, err := sup.SiaPath.AddSuffixStr(skymodules.ExtendedSuffix)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to create extended siapath")
	}

	// Create the FileUploadParams
	fup, err := fileUploadParams(extendedPath, int(sl.FanoutDataPieces), int(sl.FanoutParityPieces), sup.Force, sl.CipherType)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to create FileUploadParams for large file")
	}

	// Generate a Cipher Key for the FileUploadParams.
	//
	// NOTE: Specifically using TypeThreefish instead of TypeDefaultRenter for two
	// reason. First, TypeThreefish was the CipherType of the siafiles when
	// Skyfiles were introduced. Second, this should make the tests fail if the
	// TypeDefaultRenter changes, ensuring we add compat code for older converted
	// siafiles.
	if sl.CipherType == crypto.TypeThreefish {
		// For converted files we need to generate a SiaKey
		fup.CipherKey, err = crypto.NewSiaKey(sl.CipherType, sl.KeyData[:])
		if err != nil {
			return skymodules.Skylink{}, errors.AddContext(err, "unable to create Cipher key from SkyfileLayout KeyData")
		}
	} else {
		err = generateCipherKey(&fup, sup)
		if err != nil {
			return skymodules.Skylink{}, errors.AddContext(err, "unable to create Cipher key for FileUploadParams")
		}
	}

	// Upload the file
	fileNode, err := r.callUploadStreamFromReader(r.tg.StopCtx(), fup, restoreReader)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to upload large skyfile")
	}

	// Defer closing the file
	defer func() {
		if err := fileNode.Close(); err != nil {
			r.staticLog.Printf("Could not close node, err: %s\n", err.Error())
		}
	}()

	// Check if any of the skylinks associated with the siafile are blocked
	if r.managedIsFileNodeBlocked(fileNode) {
		err = ErrSkylinkBlocked
		// Skylink is blocked, return error and try and delete file
		deleteErr := r.DeleteFile(sup.SiaPath)
		// Don't bother returning an error if the file doesn't exist
		if !errors.Contains(deleteErr, filesystem.ErrNotExist) {
			err = errors.Compose(err, deleteErr)
		}
		return skymodules.Skylink{}, err
	}

	// Add the skylink to the siafiles.
	err = fileNode.AddSkylink(skylink)
	if err != nil {
		err = errors.AddContext(err, "unable to add skylink to the sianodes")
		deleteErr := r.DeleteFile(sup.SiaPath)
		// Don't bother returning an error if the file doesn't exist
		if !errors.Contains(deleteErr, filesystem.ErrNotExist) {
			err = errors.Compose(err, deleteErr)
		}
		return skymodules.Skylink{}, err
	}

	return skylink, nil
}

// UploadSkyfile will upload the provided data with the provided metadata,
// returning a skylink which can be used by any portal to recover the full
// original file and metadata. The skylink will be unique to the combination of
// both the file data and metadata.
func (r *Renter) UploadSkyfile(ctx context.Context, sup skymodules.SkyfileUploadParameters, reader skymodules.SkyfileUploadReader) (skylink skymodules.Skylink, err error) {
	// Set reasonable default values for any sup fields that are blank.
	skyfileEstablishDefaults(&sup)

	// If a skykey name or ID was specified, generate a file-specific key for
	// this upload.
	err = r.managedGenerateFilekey(&sup, nil)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to upload skyfile")
	}

	// defer a function that cleans up the siafiles after a failed upload
	// attempt or after a dry run
	defer func() {
		if err != nil || sup.DryRun {
			if err := r.DeleteFile(sup.SiaPath); err != nil && !errors.Contains(err, filesystem.ErrNotExist) {
				r.staticLog.Printf("error deleting siafile after upload error: %v", err)
			}

			extendedSiaPath, spErr := sup.SiaPath.AddSuffixStr(skymodules.ExtendedSuffix)
			if spErr == nil {
				if err := r.DeleteFile(extendedSiaPath); err != nil && !errors.Contains(err, filesystem.ErrNotExist) {
					r.staticLog.Printf("error deleting extended siafile after upload error: %v\n", err)
				}
			}
		}
	}()

	// Create a span and attach it to our context
	span := opentracing.StartSpan("UploadSkyfile")
	ctx = opentracing.ContextWithSpan(ctx, span)
	defer func() {
		if err != nil {
			span.LogKV("err", err)
		}
		span.SetTag("success", err == nil)
		span.SetTag("skylink", skylink.String())
		span.Finish()
	}()

	// Upload the skyfile
	skylink, err = r.managedUploadSkyfile(ctx, sup, reader)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to upload skyfile")
	}
	if r.staticDeps.Disrupt("SkyfileUploadFail") {
		return skymodules.Skylink{}, errors.New("SkyfileUploadFail")
	}

	// Check if skylink is blocked
	if r.staticSkynetBlocklist.IsBlocked(skylink) && !sup.DryRun {
		// No need to try and delete the file, the above defer func will handle
		// the deletion
		return skymodules.Skylink{}, ErrSkylinkBlocked
	}

	return skylink, nil
}

// managedIsFileNodeBlocked checks if any of the skylinks associated with the
// siafile are blocked
func (r *Renter) managedIsFileNodeBlocked(fileNode *filesystem.FileNode) bool {
	skylinkstrs := fileNode.Metadata().Skylinks
	for _, skylinkstr := range skylinkstrs {
		var skylink skymodules.Skylink
		err := skylink.LoadString(skylinkstr)
		if err != nil {
			// If there is an error just continue as we shouldn't prevent the
			// conversion due to bad old skylinks
			//
			// Log the error for debugging purposes
			r.staticLog.Printf("WARN: previous skylink for siafile %v could not be loaded from string; potentially corrupt skylink: %v", fileNode.SiaFilePath(), skylinkstr)
			continue
		}
		// Check if skylink is blocked
		if r.staticSkynetBlocklist.IsBlocked(skylink) {
			return true
		}
	}
	return false
}

// ResolveSkylinkV2 resolves a V2 skylink to a V1 skylink if possible.
func (r *Renter) ResolveSkylinkV2(ctx context.Context, sl skymodules.Skylink) (skymodules.Skylink, *skymodules.RegistryEntry, error) {
	if err := r.tg.Add(); err != nil {
		return skymodules.Skylink{}, nil, err
	}
	defer r.tg.Done()
	slResolved, srv, err := r.managedTryResolveSkylinkV2(ctx, sl)
	if err != nil {
		return skymodules.Skylink{}, nil, err
	}
	if slResolved == sl {
		return skymodules.Skylink{}, nil, ErrInvalidSkylinkVersion
	}
	return slResolved, srv, nil
}

// managedTryResolveSkylinkV2 resolves a V2 skylink to a V1 skylink. If the
// skylink is not a V2 skylink, the input link is returned.
func (r *Renter) managedTryResolveSkylinkV2(ctx context.Context, sl skymodules.Skylink) (skylink skymodules.Skylink, _ *skymodules.RegistryEntry, err error) {
	if sl.Version() != 2 {
		return sl, nil, nil
	}

	// Create a child span to capture the resolve for v2 skylinks.
	span, ctx := opentracing.StartSpanFromContext(ctx, "managedTryResolveSkylinkV2")
	defer func() {
		if err != nil {
			span.LogKV("error", err)
		}
		span.SetTag("success", err == nil)
		span.SetTag("skylinkv2", skylink.String())
		span.Finish()
	}()

	// Get link from registry entry.
	srv, err := r.ReadRegistryRID(ctx, sl.RegistryEntryID())
	if err != nil {
		return skymodules.Skylink{}, nil, err
	}
	if len(srv.Data) == 0 {
		return skymodules.Skylink{}, nil, errors.New("failed to resolve skylink")
	}

	err = skylink.LoadBytes(srv.Data)
	if err != nil {
		return skymodules.Skylink{}, nil, err
	}
	// If the link resolves to an empty skylink, return ErrRootNotFound to cause
	// the API to return a 404.
	if skylink == (skymodules.Skylink{}) {
		return skymodules.Skylink{}, nil, ErrRootNotFound
	}
	// Check if link is blocked
	if r.staticSkynetBlocklist.IsBlocked(skylink) {
		return skymodules.Skylink{}, nil, ErrSkylinkBlocked
	}
	return skylink, &srv, nil
}

// managedTryResolveSkylinkV2Recursive resolves a V2 skylink to a V1 skylink. If
// the skylink is not a V2 skylink, the input link is returned.
func (r *Renter) managedTryResolveSkylinkV2Recursive(ctx context.Context, link skymodules.Skylink) (_ skymodules.Skylink, srvs []skymodules.RegistryEntry, err error) {
	// Check if link needs to be resolved from V2 to V1.
	for i := 0; i < int(MaxSkylinkV2ResolvingDepth) && link.IsSkylinkV2(); i++ {
		var srv *skymodules.RegistryEntry
		link, srv, err = r.managedTryResolveSkylinkV2(ctx, link)
		if err != nil {
			return skymodules.Skylink{}, nil, err
		}
		if srv != nil {
			srvs = append(srvs, *srv)
		}
	}
	return link, srvs, nil
}
