package siatest

import (
	"bytes"
	"mime/multipart"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/node/api"
	"gitlab.com/SkynetLabs/skyd/skykey"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// DefaulTestingBaseChunkRedundancy is the redundancy used for the base chunk
// when uploading skyfiles for testing.
const DefaulTestingBaseChunkRedundancy = 2

// TestFile is a small helper struct that identifies a file to be uploaded. The
// upload helpers take a slice of these files to ensure order is maintained.
type TestFile struct {
	Name string
	Data []byte
}

// SkylinkV2 is a convenience type around a skylink for creating and updating V2
// skylinks.
type SkylinkV2 struct {
	skymodules.Skylink
	staticPK types.SiaPublicKey
	staticSK crypto.SecretKey
	srv      modules.SignedRegistryValue
}

// DeleteSkylinkV2 sets a V2 skylink to the empty skylink.
func (tn *TestNode) DeleteSkylinkV2(sl *SkylinkV2) error {
	return tn.UpdateSkylinkV2(sl, skymodules.Skylink{})
}

// NewSkylinkV2 creates a new V2 skylink from a V1 skylink.
func (tn *TestNode) NewSkylinkV2(sl skymodules.Skylink) (SkylinkV2, error) {
	// Update the registry with that link.
	sk, pk := crypto.GenerateKeyPair()
	var dataKey crypto.Hash
	fastrand.Read(dataKey[:])
	spk := types.SiaPublicKey{
		Algorithm: types.SignatureEd25519,
		Key:       pk[:],
	}
	srv := modules.NewRegistryValue(dataKey, sl.Bytes(), 0, modules.RegistryTypeWithoutPubkey).Sign(sk)
	err := tn.RegistryUpdate(spk, dataKey, srv.Revision, srv.Signature, sl)
	if err != nil {
		return SkylinkV2{}, err
	}
	// Create a v2 link.
	return SkylinkV2{
		Skylink:  skymodules.NewSkylinkV2(spk, dataKey),
		staticPK: spk,
		staticSK: sk,
		srv:      srv,
	}, nil
}

// NewSkylinkV2FromString creates a new V2 skylink from a V1 skylink string.
func (tn *TestNode) NewSkylinkV2FromString(sl string) (SkylinkV2, error) {
	var skylink skymodules.Skylink
	err := skylink.LoadString(sl)
	if err != nil {
		return SkylinkV2{}, err
	}
	return tn.NewSkylinkV2(skylink)
}

// UploadNewSkyfileWithDataBlocking attempts to upload a skyfile with given
// data. After it has successfully performed the upload, it will verify the file
// can be downloaded using its Skylink. Returns the skylink, the parameters used
// for the upload and potentially an error.
func (tn *TestNode) UploadNewSkyfileWithDataBlocking(filename string, filedata []byte, force bool) (skylink string, sup skymodules.SkyfileUploadParameters, sshp api.SkynetSkyfileHandlerPOST, err error) {
	return tn.UploadNewEncryptedSkyfileBlocking(filename, filedata, "", force)
}

// UploadNewEncryptedSkyfileBlocking attempts to upload a skyfile. After it has
// successfully performed the upload, it will verify the file can be downloaded
// using its Skylink. Returns the skylink, the parameters used for the upload
// and potentially an error.
func (tn *TestNode) UploadNewEncryptedSkyfileBlocking(filename string, filedata []byte, skykeyName string, force bool) (skylink string, sup skymodules.SkyfileUploadParameters, sshp api.SkynetSkyfileHandlerPOST, err error) {
	return tn.UploadSkyfileBlockingCustom(filename, filedata, skykeyName, DefaulTestingBaseChunkRedundancy, force)
}

// UploadSkyfileCustom attempts to upload a skyfile. Returns the skylink, the
// parameters used for the upload and potentially an error.
func (tn *TestNode) UploadSkyfileCustom(filename string, filedata []byte, skykeyName string, baseChunkRedundancy uint8, force bool) (skylink string, sup skymodules.SkyfileUploadParameters, sshp api.SkynetSkyfileHandlerPOST, rf *RemoteFile, err error) {
	// create the siapath
	skyfilePath := skymodules.RandomSkynetFilePath()

	// rebase to relative path
	skyfilePath, err = skyfilePath.Rebase(skymodules.SkynetFolder, skymodules.RootSiaPath())
	if err != nil {
		return
	}

	// wrap the data in a reader
	reader := bytes.NewReader(filedata)
	sup = skymodules.SkyfileUploadParameters{
		SiaPath:             skyfilePath,
		BaseChunkRedundancy: baseChunkRedundancy,
		Filename:            filename,
		Mode:                skymodules.DefaultFilePerm,
		Reader:              reader,
		Force:               force,
		Root:                false,
		SkykeyName:          skykeyName,
	}

	// upload a skyfile
	skylink, sshp, err = tn.SkynetSkyfilePost(sup)
	if err != nil {
		err = errors.AddContext(err, "Failed to upload skyfile")
		return
	}

	if !sup.Root {
		skyfilePath, err = skymodules.SkynetFolder.Join(skyfilePath.String())
		if err != nil {
			err = errors.AddContext(err, "Failed to rebase skyfile path")
			return
		}
	}
	// Return the Remote File for callers to block for upload progress
	rf = &RemoteFile{
		checksum: crypto.HashBytes(filedata),
		siaPath:  skyfilePath,
		root:     true,
	}
	return
}

// UpdateSkylinkV2 updates a V2 skylink with a new V1 skylink's value.
func (tn *TestNode) UpdateSkylinkV2(sl *SkylinkV2, slNew skymodules.Skylink) error {
	sl.srv = modules.NewRegistryValue(sl.srv.Tweak, slNew.Bytes(), sl.srv.Revision+1, modules.RegistryTypeWithoutPubkey).Sign(sl.staticSK)
	return tn.RegistryUpdate(sl.staticPK, sl.srv.Tweak, sl.srv.Revision, sl.srv.Signature, slNew)
}

// UploadSkyfileBlockingCustom attempts to upload a skyfile. After it has
// successfully performed the upload, it will verify the file can be downloaded
// using its Skylink. Returns the skylink, the parameters used for the upload
// and potentially an error.
func (tn *TestNode) UploadSkyfileBlockingCustom(filename string, filedata []byte, skykeyName string, baseChunkRedundancy uint8, force bool) (skylink string, sup skymodules.SkyfileUploadParameters, sshp api.SkynetSkyfileHandlerPOST, err error) {
	// Upload the file
	var rf *RemoteFile
	skylink, sup, sshp, rf, err = tn.UploadSkyfileCustom(filename, filedata, skykeyName, baseChunkRedundancy, force)
	if err != nil {
		err = errors.AddContext(err, "Skyfile upload failed")
		return
	}

	// Wait until upload reached the specified progress
	if err = tn.WaitForUploadProgress(rf, 1); err != nil {
		err = errors.AddContext(err, "Skyfile upload failed, progress did not reach a value of 1")
		return
	}

	// wait until upload reaches a certain health
	if err = tn.WaitForUploadHealth(rf); err != nil {
		err = errors.AddContext(err, "Skyfile upload failed, health did not reach the repair threshold")
		return
	}

	return
}

// UploadNewSkyfileBlocking attempts to upload a skyfile of given size. After it
// has successfully performed the upload, it will verify the file can be
// downloaded using its Skylink. Returns the skylink, the parameters used for
// the upload and potentially an error.
func (tn *TestNode) UploadNewSkyfileBlocking(filename string, filesize uint64, force bool) (skylink string, sup skymodules.SkyfileUploadParameters, sshp api.SkynetSkyfileHandlerPOST, err error) {
	data := fastrand.Bytes(int(filesize))
	return tn.UploadNewSkyfileWithDataBlocking(filename, data, force)
}

// UploadNewMultipartSkyfileBlocking uploads a multipart skyfile that
// contains several files. After it has successfully performed the upload, it
// will verify the file can be downloaded using its Skylink. Returns the
// skylink, the parameters used for the upload and potentially an error.
// The `files` argument is a map of filepath->fileContent.
func (tn *TestNode) UploadNewMultipartSkyfileBlocking(filename string, files []TestFile, defaultPath string, disableDefaultPath bool, force bool) (skylink string, sup skymodules.SkyfileMultipartUploadParameters, sshp api.SkynetSkyfileHandlerPOST, err error) {
	var tf []string
	if defaultPath == "" && disableDefaultPath == false {
		tf = skymodules.DefaultTryFilesValue
	}
	return tn.UploadNewMultipartSkyfileEncryptedBlocking(filename, files, defaultPath, disableDefaultPath, tf, nil, force, "", skykey.SkykeyID{})
}

// UploadNewMultipartSkyfileEncryptedBlocking uploads a multipart skyfile that
// contains several files. After it has successfully performed the upload, it
// will verify if the file can be downloaded using its Skylink. Returns the
// skylink, the parameters used for the upload and potentially an error.  The
// `files` argument is a map of filepath->fileContent.
func (tn *TestNode) UploadNewMultipartSkyfileEncryptedBlocking(filename string, files []TestFile, defaultPath string, disableDefaultPath bool, tryFiles []string, errorPages map[int]string, force bool, skykeyName string, skykeyID skykey.SkykeyID) (skylink string, sup skymodules.SkyfileMultipartUploadParameters, sshp api.SkynetSkyfileHandlerPOST, err error) {
	// create the siapath
	skyfilePath, err := skymodules.NewSiaPath(filename)
	if err != nil {
		err = errors.AddContext(err, "Failed to create siapath")
		return
	}

	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)

	// add the files
	var offset uint64
	for _, tf := range files {
		_, err = skymodules.AddMultipartFile(writer, tf.Data, "files[]", tf.Name, skymodules.DefaultFilePerm, &offset)
		if err != nil {
			panic(err)
		}
	}

	if err = writer.Close(); err != nil {
		return
	}
	reader := bytes.NewReader(body.Bytes())

	sup = skymodules.SkyfileMultipartUploadParameters{
		SiaPath:             skyfilePath,
		BaseChunkRedundancy: 2,
		Reader:              reader,
		Force:               force,
		Root:                false,
		ContentType:         writer.FormDataContentType(),
		Filename:            filename,
		DefaultPath:         defaultPath,
		DisableDefaultPath:  disableDefaultPath,
		TryFiles:            tryFiles,
		ErrorPages:          errorPages,
	}

	// upload a skyfile
	skylink, sshp, err = tn.SkynetSkyfileMultiPartEncryptedPost(sup, skykeyName, skykeyID)
	if err != nil {
		err = errors.AddContext(err, "Failed to upload skyfile")
		return
	}

	if !sup.Root {
		skyfilePath, err = skymodules.SkynetFolder.Join(skyfilePath.String())
		if err != nil {
			err = errors.AddContext(err, "Failed to rebase skyfile path")
			return
		}
	}
	rf := &RemoteFile{
		checksum: crypto.HashBytes(body.Bytes()),
		siaPath:  skyfilePath,
		root:     true,
	}

	// Wait until upload reached the specified progress
	if err = tn.WaitForUploadProgress(rf, 1); err != nil {
		err = errors.AddContext(err, "Skyfile upload failed, progress did not reach a value of 1")
		return
	}

	// wait until upload reaches a certain health
	if err = tn.WaitForUploadHealth(rf); err != nil {
		err = errors.AddContext(err, "Skyfile upload failed, health did not reach the repair threshold")
		return
	}

	return
}
