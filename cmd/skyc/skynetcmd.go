package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/vbauerster/mpb/v5"
	"github.com/vbauerster/mpb/v5/decor"
	"gitlab.com/NebulousLabs/errors"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"

	"gitlab.com/SkynetLabs/skyd/node/api"
	"gitlab.com/SkynetLabs/skyd/siatest"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"gitlab.com/SkynetLabs/skyd/skymodules/renter"
)

var (
	skynetCmd = &cobra.Command{
		Use:   "skynet",
		Short: "Perform actions related to Skynet",
		Long: `Perform actions related to Skynet, a file sharing and data publication platform
on top of Sia.`,
		Run: skynetcmd,
	}

	skynetBackupCmd = &cobra.Command{
		Use:   "backup [skylink] [backup path]",
		Short: "Backup a skyfile to a file on disk.",
		Long:  "Create a backup of a skyfile as a file on disk.",
		Run:   wrap(skynetbackupcmd),
	}

	skynetBlocklistCmd = &cobra.Command{
		Use:   "blocklist",
		Short: "Add, remove, or list skylinks from the blocklist.",
		Long:  "Add, remove, or list skylinks from the blocklist.",
		Run:   skynetblocklistgetcmd,
	}

	skynetBlocklistAddCmd = &cobra.Command{
		Use:   "add [skylink] ...",
		Short: "Add skylinks to the blocklist",
		Long:  "Add space separated skylinks to the blocklist.",
		Run:   skynetblocklistaddcmd,
	}

	skynetBlocklistRemoveCmd = &cobra.Command{
		Use:   "remove [skylink] ...",
		Short: "Remove skylinks from the blocklist",
		Long:  "Remove space separated skylinks from the blocklist.",
		Run:   skynetblocklistremovecmd,
	}

	skynetConvertCmd = &cobra.Command{
		Use:   "convert [source siaPath] [destination siaPath]",
		Short: "Convert a siafile to a skyfile with a skylink.",
		Long: `Convert a siafile to a skyfile and then generate its skylink. A new skylink
	will be created in the user's skyfile directory. The skyfile and the original
	siafile are both necessary to pin the file and keep the skylink active. The
	skyfile will consume an additional 40 MiB of storage.`,
		Run: wrap(skynetconvertcmd),
	}

	skynetDownloadCmd = &cobra.Command{
		Use:   "download [skylink] [destination]",
		Short: "Download a skylink from skynet.",
		Long: `Download a file from skynet using a skylink. The download may fail unless this
node is configured as a skynet portal. Use the --portal flag to fetch a skylink
file from a chosen skynet portal.`,
		Run: skynetdownloadcmd,
	}

	skynetIsBlockedCmd = &cobra.Command{
		Use:   "isblocked [skylink] ...",
		Short: "Checks if a skylink is on the blocklist.",
		Long: `Checks if a skylink, or a list of space separated skylinks, is on the blocklist 
since the list returned from 'skyc skynet blocklist' is a list of hashes of the skylinks' 
merkleroots so they cannot be visually verified.`,
		Run: skynetisblockedcmd,
	}

	skynetLsCmd = &cobra.Command{
		Use:   "ls",
		Short: "List all skyfiles that the user has pinned.",
		Long: `List all skyfiles that the user has pinned along with the corresponding
skylinks. By default, only files in var/skynet/ will be displayed. The --root
flag can be used to view skyfiles pinned in other folders.`,
		Run: skynetlscmd,
	}

	skynetPinCmd = &cobra.Command{
		Use:   "pin [skylink] [destination siapath]",
		Short: "Pin a skylink from skynet by re-uploading it yourself.",
		Long: `Pin the file associated with this skylink by re-uploading an exact copy. This
ensures that the file will still be available on skynet as long as you continue
maintaining the file in your renter.`,
		Run: wrap(skynetpincmd),
	}

	skynetPortalsCmd = &cobra.Command{
		Use:   "portals",
		Short: "Add, remove, or list registered Skynet portals.",
		Long:  "Add, remove, or list registered Skynet portals.",
		Run:   wrap(skynetportalsgetcmd),
	}

	skynetPortalsAddCmd = &cobra.Command{
		Use:   "add [url]",
		Short: "Add a Skynet portal as public or private to the persisted portals list.",
		Long: `Add a Skynet portal as public or private. Specify the url of the Skynet portal followed
by --public if you want it to be publicly available.`,
		Run: wrap(skynetportalsaddcmd),
	}

	skynetPortalsRemoveCmd = &cobra.Command{
		Use:   "remove [url]",
		Short: "Remove a Skynet portal from the persisted portals list.",
		Long:  "Remove a Skynet portal from the persisted portals list.",
		Run:   wrap(skynetportalsremovecmd),
	}

	skynetRestoreCmd = &cobra.Command{
		Use:   "restore [backup source]",
		Short: "Restore a skyfile from a backup file.",
		Long:  "Restore a skyfile from a backup file.",
		Run:   wrap(skynetrestorecmd),
	}

	skynetSkylinkCmd = &cobra.Command{
		Use:   "skylink",
		Short: "Perform various util functions for a skylink.",
		Long: `Perform various util functions for a skylink like check the layout
metadata, or recomputing.`,
		Run: skynetskylinkcmd,
	}

	skynetSkylinkCompareCmd = &cobra.Command{
		Use:   "compare [skylink] [metadata filename]",
		Short: "Compare a skylink to a regenerated skylink",
		Long: `This command regenerates a skylink by doing the following:
First, it reads some provided metadata from the provided filename.
Second, it downloads the skylink and records the metadata, layout, and filedata.
Third, it compares the downloaded metadata to the metadata read from disk.
Fourth, it computesthe base sector and then the skylink from the downloaded information.
Lastly, it compares the generated skylink to the skylink that was passed in.`,
		Run: wrap(skynetskylinkcomparecmd),
	}

	skynetSkylinkLayoutCmd = &cobra.Command{
		Use:   "layout [skylink]",
		Short: "Print the layout associated with a skylink",
		Long:  "Print the layout associated with a skylink",
		Run:   wrap(skynetskylinklayoutcmd),
	}

	skynetSkylinkMetadataCmd = &cobra.Command{
		Use:   "metadata [skylink]",
		Short: "Print the metadata associated with a skylink",
		Long:  "Print the metadata associated with a skylink",
		Run:   wrap(skynetskylinkmetadatacmd),
	}

	skynetUnpinCmd = &cobra.Command{
		Use:   "unpin [skylink]",
		Short: "Unpin pinned skyfiles by skylink.",
		Long: `Unpin one or more pinned skyfiles by skylink. The files and
directories will continue to be available on Skynet if other nodes have pinned
them.

NOTE: To use the prior functionality of unpinning by SiaPath, use the 'skyc
renter delete' command and set the --root flag.`,
		Run: skynetunpincmd,
	}

	skynetUploadCmd = &cobra.Command{
		Use:   "upload [source path] [destination siapath]",
		Short: "Upload a file or a directory to Skynet.",
		Long: `Upload a file or a directory to Skynet. A skylink will be 
produced which can be shared and used to retrieve the file. If the given path is
a directory it will be uploaded as a single skylink unless the --separately flag
is passed, in which case all files under that directory will be uploaded 
individually and an individual skylink will be produced for each. All files that
get uploaded will be pinned to this Sia node, meaning that this node will pay
for storage and repairs until the files are manually deleted. Use the --dry-run 
flag to fetch the skylink without actually uploading the file.`,
		Run: skynetuploadcmd,
	}
)

// skynetcmd displays the usage info for the command.
//
// TODO: Could put some stats or summaries or something here.
func skynetcmd(cmd *cobra.Command, _ []string) {
	_ = cmd.UsageFunc()(cmd)
	os.Exit(exitCodeUsage)
}

// skynetbackupcmd will backup a skyfile by writing it to a backup writer.
func skynetbackupcmd(skylinkStr, backupPath string) {
	// Create backup file
	f, err := os.Create(backupPath)
	if err != nil {
		die("Unable to create backup file:", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			die("Unable to close backup file:", err)
		}
	}()

	// Create backup
	err = httpClient.SkynetSkylinkBackup(skylinkStr, f)
	if err != nil {
		die("Unable to create backup:", err)
	}
	fmt.Println("Backup successfully created at ", backupPath)
}

// skynetblocklistaddcmd adds skylinks to the blocklist
func skynetblocklistaddcmd(cmd *cobra.Command, args []string) {
	skynetBlocklistUpdate(args, nil)
}

// skynetblocklistremovecmd removes skylinks from the blocklist
func skynetblocklistremovecmd(cmd *cobra.Command, args []string) {
	skynetBlocklistUpdate(nil, args)
}

// skynetBlocklistUpdate adds/removes trimmed skylinks to the blocklist
func skynetBlocklistUpdate(additions, removals []string) {
	additions = sanitizeSkylinks(additions)
	removals = sanitizeSkylinks(removals)

	err := httpClient.SkynetBlocklistHashPost(additions, removals, skynetBlocklistHash)
	if err != nil {
		die("Unable to update skynet blocklist:", err)
	}

	fmt.Println("Skynet Blocklist updated")
}

// skynetblocklistgetcmd will return the list of hashed merkleroots that are blocked
// from Skynet.
func skynetblocklistgetcmd(_ *cobra.Command, _ []string) {
	response, err := httpClient.SkynetBlocklistGet()
	if err != nil {
		die("Unable to get skynet blocklist:", err)
	}

	fmt.Printf("Listing %d blocked skylink(s) merkleroots:\n", len(response.Blocklist))
	for _, hash := range response.Blocklist {
		fmt.Printf("\t%s\n", hash)
	}
}

// skynetconvertcmd will convert an existing siafile to a skyfile and skylink on
// the Sia network.
func skynetconvertcmd(sourceSiaPathStr, destSiaPathStr string) {
	// Create the siapaths.
	sourceSiaPath, err := skymodules.NewSiaPath(sourceSiaPathStr)
	if err != nil {
		die("Could not parse source siapath:", err)
	}
	destSiaPath, err := skymodules.NewSiaPath(destSiaPathStr)
	if err != nil {
		die("Could not parse destination siapath:", err)
	}

	// Perform the conversion and print the result.
	sup := skymodules.SkyfileUploadParameters{
		SiaPath: destSiaPath,
	}
	sup = parseAndAddSkykey(sup)
	sshp, err := httpClient.SkynetConvertSiafileToSkyfilePost(sup, sourceSiaPath)
	if err != nil {
		die("could not convert siafile to skyfile:", err)
	}
	skylink := sshp.Skylink

	// Calculate the siapath that was used for the upload.
	var skypath skymodules.SiaPath
	if skynetUploadRoot {
		skypath = destSiaPath
	} else {
		skypath, err = skymodules.SkynetFolder.Join(destSiaPath.String())
		if err != nil {
			die("could not fetch skypath:", err)
		}
	}
	fmt.Printf("Skyfile uploaded successfully to %v\nSkylink: sia://%v\n", skypath, skylink)
}

// skynetdownloadcmd will perform the download of a skylink.
func skynetdownloadcmd(cmd *cobra.Command, args []string) {
	if len(args) != 2 {
		_ = cmd.UsageFunc()(cmd)
		os.Exit(exitCodeUsage)
	}

	// Open the file.
	skylink := args[0]
	skylink = strings.TrimPrefix(skylink, "sia://")
	filename := args[1]
	file, err := os.Create(filename)
	if err != nil {
		die("Unable to create destination file:", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			die(err)
		}
	}()

	// Check whether the portal flag is set, if so use the portal download
	// method.
	var reader io.ReadCloser
	if skynetDownloadPortal != "" {
		url := skynetDownloadPortal + "/" + skylink
		resp, err := http.Get(url)
		if err != nil {
			die("Unable to download from portal:", err)
		}
		reader = resp.Body
		defer func() {
			err = resp.Body.Close()
			if err != nil {
				die("unable to close reader:", err)
			}
		}()
	} else {
		// Try to perform a download using the client package.
		reader, err = httpClient.SkynetSkylinkReaderGet(skylink)
		if err != nil {
			die("Unable to fetch skylink:", err)
		}
		defer func() {
			err = reader.Close()
			if err != nil {
				die("unable to close reader:", err)
			}
		}()
	}

	_, err = io.Copy(file, reader)
	if err != nil {
		die("Unable to write full data:", err)
	}
}

// skynetisblockedcmd will check if a skylink, or list of skylinks, is on the
// blocklist.
func skynetisblockedcmd(_ *cobra.Command, skylinkStrs []string) {
	// Get the blocklist
	response, err := httpClient.SkynetBlocklistGet()
	if err != nil {
		die("Unable to get skynet blocklist:", err)
	}

	// Parse the slice response into a map
	blocklistMap := make(map[crypto.Hash]struct{})
	for _, hash := range response.Blocklist {
		blocklistMap[hash] = struct{}{}
	}

	// Check the skylinks
	//
	// NOTE: errors are printed and won't cause the function to exit.
	for _, skylinkStr := range skylinkStrs {
		// Load the string
		var skylink skymodules.Skylink
		err := skylink.LoadString(skylinkStr)
		if err != nil {
			fmt.Printf("Skylink %v \tis an invalid skylink: %v\n", skylinkStr, err)
			continue
		}
		// Generate the hash of the merkleroot and check the blocklist
		hash := crypto.HashObject(skylink.MerkleRoot())
		_, blocked := blocklistMap[hash]
		if blocked {
			fmt.Printf("Skylink %v \tis on the blocklist\n", skylinkStr)
		}
	}
}

// skynetlscmd is the handler for the command `skyc skynet ls`. Works very
// similar to 'skyc renter ls' but defaults to the SkynetFolder and only
// displays files that are pinning skylinks.
func skynetlscmd(cmd *cobra.Command, args []string) {
	// Parse the SiaPath
	sp, err := parseLSArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = cmd.UsageFunc()(cmd)
		os.Exit(exitCodeUsage)
	}

	// Check whether the command is based in root or based in the skynet folder.
	if !skynetLsRoot {
		if sp.IsRoot() {
			sp = skymodules.SkynetFolder
		} else {
			sp, err = skymodules.SkynetFolder.Join(sp.String())
			if err != nil {
				die("could not build siapath:", err)
			}
		}
	}

	// Check if the command is hitting a single file.
	if !sp.IsRoot() {
		tryDir, err := printSingleFile(sp, true, true)
		if err != nil {
			die(err)
		}
		if !tryDir {
			return
		}
	}

	// Get the full set of files and directories. They will be sorted by siapath
	//
	// NOTE: we always pass in true for root as this is referring to the client
	// method needed to query the directories, not whether or not the siapath is
	// relative to root or the skynet folder.
	//
	// NOTE: We want to get the directories recursively if we are either checking
	// from root or the user wants recursive.
	getRecursive := skynetLsRecursive || skynetLsRoot
	dirs := getDirSorted(sp, true, getRecursive, verbose)

	// Determine the total number of Skyfiles and Skynet Directories. A Skynet
	// directory is a directory that is either in the Skynet Folder or it contains
	// at least one skyfile.
	var numFilesDirs uint64
	root := dirs[0] // Root directory we are querying.

	// Grab the starting value for the number of Skyfiles and Skynet Directories.
	if skynetLsRecursive {
		numFilesDirs = root.dir.AggregateSkynetFiles + root.dir.AggregateNumSubDirs
	} else {
		numFilesDirs = root.dir.SkynetFiles + root.dir.NumSubDirs
	}

	// If we are referencing the root of the file system then we need to check all
	// the directories we queried and see which ones need to be omitted.
	if skynetLsRoot {
		// Figure out how many directories don't contain skyfiles
		var nonSkynetDir uint64
		for _, dir := range dirs {
			if !skymodules.IsSkynetDir(dir.dir.SiaPath) && dir.dir.SkynetFiles == 0 {
				nonSkynetDir++
			}
		}

		// Subtract the number of non skynet directories from the total.
		numFilesDirs -= nonSkynetDir
	}

	// Print totals.
	totalStoredStr := modules.FilesizeUnits(root.dir.AggregateSkynetSize)
	fmt.Printf("\nListing %v files/dirs:\t%9s\n\n", numFilesDirs, totalStoredStr)

	// Print Dirs
	err = printSkynetDirs(dirs, skynetLsRecursive)
	if err != nil {
		die(err)
	}
}

// skynetPin will pin the Skyfile associated with the provided Skylink at the
// provided SiaPath
func skynetPin(skylink string, siaPath skymodules.SiaPath) (string, error) {
	// Check if --portal was set
	if skynetDownloadPortal == "" {
		spp := skymodules.SkyfilePinParameters{
			SiaPath: siaPath,
			Root:    skynetUploadRoot,
		}
		fmt.Println("Pinning Skyfile ...")
		return skylink, httpClient.SkynetSkylinkPinPost(skylink, spp)
	}

	// Download skyfile from the Portal
	fmt.Printf("Downloading Skyfile from %v ...", skynetDownloadPortal)
	url := skynetDownloadPortal + "/" + skylink
	resp, err := http.Get(url)
	if err != nil {
		return "", errors.AddContext(err, "unable to download from portal")
	}
	reader := resp.Body
	defer func() {
		err = resp.Body.Close()
		if err != nil {
			die("unable to close reader:", err)
		}
	}()

	// Get the SkyfileMetadata.
	_, sm, err := httpClient.SkynetMetadataGet(skylink)
	if err != nil {
		return "", errors.AddContext(err, "unable to fetch skyfile metadata")
	}

	// Upload the skyfile to pin it to the renter node
	sup := skymodules.SkyfileUploadParameters{
		SiaPath:  siaPath,
		Reader:   reader,
		Filename: sm.Filename,
		Mode:     sm.Mode,
	}

	// NOTE: Since the user can define a new siapath for the Skyfile the skylink
	// returned from the upload may be different than the original skylink which
	// is why we are overwriting the skylink here.
	fmt.Println("Pinning Skyfile ...")
	skylink, _, err = httpClient.SkynetSkyfilePost(sup)
	if err != nil {
		return "", errors.AddContext(err, "unable to upload skyfile")
	}
	return skylink, nil
}

// skynetpincmd will pin the file from this skylink.
func skynetpincmd(sourceSkylink, destSiaPath string) {
	skylink := strings.TrimPrefix(sourceSkylink, "sia://")
	// Create the siapath.
	siaPath, err := skymodules.NewSiaPath(destSiaPath)
	if err != nil {
		die("Could not parse destination siapath:", err)
	}

	// Pin the Skyfile
	skylink, err = skynetPin(skylink, siaPath)
	if err != nil {
		die("Unable to Pin Skyfile:", err)
	}
	fmt.Printf("Skyfile pinned successfully\nSkylink: sia://%v\n", skylink)
}

// skynetrestorecmd will restore a skyfile from a backup writer.
func skynetrestorecmd(backupPath string) {
	// Open the backup file
	f, err := os.Open(backupPath)
	if err != nil {
		die("Unable to open backup file:", err)
	}
	defer func() {
		// Attempt to close the file, API call appears to close file so ignore the
		// error to avoid getting an error for closing a closed file.
		_ = f.Close()
	}()

	// Create backup
	skylink, err := httpClient.SkynetSkylinkRestorePost(f)
	if err != nil {
		die("Unable to restore skyfile:", err)
	}
	fmt.Println("Restore successful! Skylink: ", skylink)
}

// skynetskylinkcmd displays the usage info for the command.
func skynetskylinkcmd(cmd *cobra.Command, args []string) {
	_ = cmd.UsageFunc()(cmd)
	os.Exit(exitCodeUsage)
}

// skynetskylinkcomparecmd compares a provided skylink to with a re-generated
// skylink based on metadata provided in a metadata.json file and downloading
// the file data and the layout from the skylink.
func skynetskylinkcomparecmd(expectedSkylink string, filename string) {
	// Read Metadata file and trim a potential newline.
	skyfileMetadataFromFile := fileData(filename)
	skyfileMetadataFromFile = bytes.TrimSuffix(skyfileMetadataFromFile, []byte{'\n'})

	// Download the skyfile
	skyfileDownloadedData, layoutFromHeader, skyfileMetadataFromHeader, err := smallSkyfileDownload(expectedSkylink)
	if err != nil {
		die(err)
	}

	// Check if the metadata download is the same as the metadata loaded from disk
	if !bytes.Equal(skyfileMetadataFromFile, skyfileMetadataFromHeader) {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Metadata read from file %d\n", len(skyfileMetadataFromFile)))
		sb.WriteString(string(skyfileMetadataFromFile))
		sb.WriteString(fmt.Sprintf("Metadata read from header %d\n", len(skyfileMetadataFromHeader)))
		sb.WriteString(string(skyfileMetadataFromHeader))
		die(sb.String(), "Metadatas not equal")
	}
	fmt.Println("Metadatas Equal")

	// build base sector
	baseSector, fetchSize, _ := skymodules.BuildBaseSector(layoutFromHeader.Encode(), nil, skyfileMetadataFromFile, skyfileDownloadedData)
	baseSectorRoot := crypto.MerkleRoot(baseSector)
	skylink, err := skymodules.NewSkylinkV1(baseSectorRoot, 0, fetchSize)
	if err != nil {
		die(err)
	}

	if skylink.String() != expectedSkylink {
		comp := fmt.Sprintf("Expected %s\nGenerated %s\n", expectedSkylink, skylink.String())
		die(comp, "Generated Skylink not Equal to Expected")
	}
	fmt.Println("Generated Skylink as Expected!")
}

// skynetskylinklayoutcmd prints the SkyfileLayout of the skylink.
func skynetskylinklayoutcmd(skylink string) {
	// Download the layout
	_, sl, _, err := smallSkyfileDownload(skylink)
	if err != nil {
		die(err)
	}
	// Print the layout
	str, err := siatest.PrintJSONProd(sl)
	if err != nil {
		die(err)
	}
	fmt.Println("Skyfile Layout:")
	fmt.Println(str)
}

// skynetskylinkmetadatacmd downloads and prints the SkyfileMetadata for a
// skylink.
func skynetskylinkmetadatacmd(skylink string) {
	// Download the metadata
	_, _, sm, err := smallSkyfileDownload(skylink)
	if err != nil {
		die(err)
	}
	// Print the metadata
	fmt.Println("Skyfile Metadata:")
	fmt.Println(string(sm))
}

// skynetunpincmd will unpin and delete either a single or multiple skylinks
// from the renter.
func skynetunpincmd(cmd *cobra.Command, skylinks []string) {
	if len(skylinks) == 0 {
		_ = cmd.UsageFunc()(cmd)
		os.Exit(exitCodeUsage)
	}

	for _, skylink := range skylinks {
		// Unpin skylink
		err := httpClient.SkynetSkylinkUnpinPost(skylink)
		if err != nil {
			fmt.Printf("Unable to unpin skylink %v: %v\n", skylink, err)
		}
	}
}

// skynetuploadcmd will upload a file or directory to Skynet. If --dry-run is
// passed, it will fetch the skylinks without uploading.
func skynetuploadcmd(_ *cobra.Command, args []string) {
	if len(args) == 1 {
		skynetuploadpipecmd(args[0])
		return
	}
	if len(args) != 2 {
		die("wrong number of arguments")
	}
	sourcePath, destSiaPath := args[0], args[1]
	fi, err := os.Stat(sourcePath)
	if err != nil {
		die("Unable to fetch source fileinfo:", err)
	}

	// create a new progress bar set:
	pbs := mpb.New(mpb.WithWidth(40))

	if !fi.IsDir() {
		skynetUploadFile(sourcePath, sourcePath, destSiaPath, pbs)
		if skynetUploadDryRun {
			fmt.Print("[dry run] ")
		}
		pbs.Wait()
		fmt.Printf("Successfully uploaded skyfile!\n")
		return
	}

	if skynetUploadSeparately {
		skynetUploadFilesSeparately(sourcePath, destSiaPath, pbs)
		return
	}
	skynetUploadDirectory(sourcePath, destSiaPath)
}

// skynetuploadpipecmd will upload a file or directory to Skynet. If --dry-run is
// passed, it will fetch the skylinks without uploading.
func skynetuploadpipecmd(destSiaPath string) {
	fi, err := os.Stdin.Stat()
	if err != nil {
		die(err)
	}
	if fi.Mode()&os.ModeNamedPipe == 0 {
		die("Command is meant to be used with either a pipe or src file")
	}
	// Create the siapath.
	siaPath, err := skymodules.NewSiaPath(destSiaPath)
	if err != nil {
		die("Could not parse destination siapath:", err)
	}
	filename := siaPath.Name()

	// create a new progress bar set:
	pbs := mpb.New(mpb.WithWidth(40))
	// Create the single bar.
	bar := pbs.AddSpinner(
		-1, // size is unknown
		mpb.SpinnerOnLeft,
		mpb.SpinnerStyle([]string{"∙∙∙", "●∙∙", "∙●∙", "∙∙●", "∙∙∙"}),
		mpb.BarFillerClearOnComplete(),
		mpb.PrependDecorators(
			decor.AverageSpeed(decor.UnitKiB, "% .1f", decor.WC{W: 4}),
			decor.Counters(decor.UnitKiB, " - %.1f / %.1f", decor.WC{W: 4}),
		),
	)
	// Create the proxy reader from stdin.
	r := bar.ProxyReader(os.Stdin)
	// Set a spinner to start after the upload is finished
	pSpinner := newProgressSpinner(pbs, bar, filename)
	// Perform the upload
	skylink := skynetUploadFileFromReader(r, filename, siaPath, skymodules.DefaultFilePerm)
	// Replace the spinner with the skylink and stop it
	newProgressSkylink(pbs, pSpinner, filename, skylink)
	return
}

// skynetportalsgetcmd displays the list of persisted Skynet portals
func skynetportalsgetcmd() {
	portals, err := httpClient.SkynetPortalsGet()
	if err != nil {
		die("Could not get portal list:", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	fmt.Fprintf(w, "Address\tPublic\n")
	fmt.Fprintf(w, "-------\t------\n")

	for _, portal := range portals.Portals {
		fmt.Fprintf(w, "%s\t%t\n", portal.Address, portal.Public)
	}

	if err = w.Flush(); err != nil {
		die(err)
	}
}

// skynetportalsaddcmd adds a Skynet portal as either public or private
func skynetportalsaddcmd(portalURL string) {
	addition := skymodules.SkynetPortal{
		Address: modules.NetAddress(portalURL),
		Public:  skynetPortalPublic,
	}

	err := httpClient.SkynetPortalsPost([]skymodules.SkynetPortal{addition}, nil)
	if err != nil {
		die("Could not add portal:", err)
	}
}

// skynetportalsremovecmd removes a Skynet portal
func skynetportalsremovecmd(portalUrl string) {
	removal := modules.NetAddress(portalUrl)

	err := httpClient.SkynetPortalsPost(nil, []modules.NetAddress{removal})
	if err != nil {
		die("Could not remove portal:", err)
	}
}

// skynetUploadFile uploads a file to Skynet
func skynetUploadFile(basePath, sourcePath string, destSiaPath string, pbs *mpb.Progress) {
	// Create the siapath.
	siaPath, err := skymodules.NewSiaPath(destSiaPath)
	if err != nil {
		die("Could not parse destination siapath:", err)
	}
	filename := filepath.Base(sourcePath)

	// Open the source.
	file, err := os.Open(sourcePath)
	if err != nil {
		die("Unable to open source path:", err)
	}
	defer func() { _ = file.Close() }()

	fi, err := file.Stat()
	if err != nil {
		die("Unable to fetch source fileinfo:", err)
	}

	if skynetUploadSilent {
		// Silently upload the file and print a simple source -> skylink
		// matching after it's done.
		skylink := skynetUploadFileFromReader(file, filename, siaPath, fi.Mode())
		fmt.Printf("%s -> %s\n", sourcePath, skylink)
		return
	}

	// Display progress bars while uploading and processing the file.
	var relPath string
	if sourcePath == basePath {
		// when uploading a single file we only display the filename
		relPath = filename
	} else {
		// when uploading multiple files we strip the common basePath
		relPath, err = filepath.Rel(basePath, sourcePath)
		if err != nil {
			die("Could not get relative path:", err)
		}
	}
	// Wrap the file reader in a progress bar reader
	pUpload, rc := newProgressReader(pbs, fi.Size(), relPath, file)
	// Set a spinner to start after the upload is finished
	pSpinner := newProgressSpinner(pbs, pUpload, relPath)
	// Perform the upload
	skylink := skynetUploadFileFromReader(rc, filename, siaPath, fi.Mode())
	// Replace the spinner with the skylink and stop it
	newProgressSkylink(pbs, pSpinner, relPath, skylink)
	return
}

// skynetUploadFilesSeparately uploads a number of files to Skynet, printing out
// separate skylink for each
func skynetUploadFilesSeparately(sourcePath, destSiaPath string, pbs *mpb.Progress) {
	// Walk the target directory and collect all files that are going to be
	// uploaded.
	filesToUpload := make([]string, 0)
	err := filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			fmt.Println("Warning: skipping file:", err)
			return nil
		}
		if !info.IsDir() {
			filesToUpload = append(filesToUpload, path)
		}
		return nil
	})
	if err != nil {
		die(err)
	}

	// Confirm with the user that they want to upload all of them.
	if skynetUploadDryRun {
		fmt.Print("[dry run] ")
	}
	ok := askForConfirmation(fmt.Sprintf("Are you sure that you want to upload %d files to Skynet?", len(filesToUpload)))
	if !ok {
		os.Exit(0)
	}

	// Start the workers.
	filesChan := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < SimultaneousSkynetUploads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for filename := range filesChan {
				// get only the filename and path, relative to the original destSiaPath
				// in order to figure out where to put the file
				newDestSiaPath := filepath.Join(destSiaPath, strings.TrimPrefix(filename, sourcePath))
				skynetUploadFile(sourcePath, filename, newDestSiaPath, pbs)
			}
		}()
	}
	// Send all files for upload.
	for _, path := range filesToUpload {
		filesChan <- path
	}
	// Signal the workers that there is no more work.
	close(filesChan)
	wg.Wait()
	pbs.Wait()
	if skynetUploadDryRun {
		fmt.Print("[dry run] ")
	}
	fmt.Printf("Successfully uploaded %d skyfiles!\n", len(filesToUpload))
}

// skynetUploadDirectory uploads a directory as a single skyfile
func skynetUploadDirectory(sourcePath, destSiaPath string) {
	skyfilePath, err := skymodules.NewSiaPath(destSiaPath)
	if err != nil {
		die(fmt.Sprintf("Failed to create siapath %s\n", destSiaPath), err)
	}
	if skynetUploadDisableDefaultPath && skynetUploadDefaultPath != "" {
		die("Illegal combination of parameters: --defaultpath and --disabledefaultpath are mutually exclusive.")
	}
	if skynetUploadTryFiles != "" && (skynetUploadDisableDefaultPath || skynetUploadDefaultPath != "") {
		die("Illegal combination of parameters: --tryfiles is not compatible with --defaultpath and --disabledefaultpath.")
	}
	tryfiles := strings.Split(skynetUploadTryFiles, ",")
	errPages, err := api.UnmarshalErrorPages(skynetUploadErrorPages)
	if err != nil {
		die(err)
	}
	pr, pw := io.Pipe()
	defer pr.Close()
	writer := multipart.NewWriter(pw)
	go func() {
		defer pw.Close()
		// Walk the target directory and collect all files that are going to be
		// uploaded.
		var offset uint64
		err = filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				die(fmt.Sprintf("Failed to read file %s.\n", path), err)
			}
			if info.IsDir() {
				return nil
			}
			data, err := ioutil.ReadFile(path)
			if err != nil {
				die(fmt.Sprintf("Failed to read file %s.\n", path), err)
			}
			_, err = skymodules.AddMultipartFile(writer, data, "files[]", info.Name(), skymodules.DefaultFilePerm, &offset)
			if err != nil {
				die(fmt.Sprintf("Failed to add file %s to multipart upload.\n", path), err)
			}
			return nil
		})
		if err != nil {
			die(err)
		}
		if err = writer.Close(); err != nil {
			die(err)
		}
	}()

	sup := skymodules.SkyfileMultipartUploadParameters{
		SiaPath:             skyfilePath,
		Force:               false,
		Root:                false,
		BaseChunkRedundancy: renter.SkyfileDefaultBaseChunkRedundancy,
		Reader:              pr,
		Filename:            skyfilePath.Name(),
		DefaultPath:         skynetUploadDefaultPath,
		DisableDefaultPath:  skynetUploadDisableDefaultPath,
		TryFiles:            tryfiles,
		ErrorPages:          errPages,
		ContentType:         writer.FormDataContentType(),
	}
	skylink, _, err := httpClient.SkynetSkyfileMultiPartPost(sup)
	if err != nil {
		die("Failed to upload directory.", err)
	}
	fmt.Println("Successfully uploaded directory:", skylink)
}

// skynetUploadFileFromReader is a helper method that uploads a file to Skynet
func skynetUploadFileFromReader(source io.Reader, filename string, siaPath skymodules.SiaPath, mode os.FileMode) (skylink string) {
	// Upload the file and return a skylink
	sup := skymodules.SkyfileUploadParameters{
		SiaPath: siaPath,
		Root:    skynetUploadRoot,

		Filename: filename,
		Mode:     mode,

		DryRun: skynetUploadDryRun,
		Reader: source,
	}
	sup = parseAndAddSkykey(sup)
	skylink, _, err := httpClient.SkynetSkyfilePost(sup)
	if err != nil {
		die("could not upload file to Skynet:", err)
	}
	return skylink
}

// newProgressSkylink creates a static progress bar that starts after `afterBar`
// and displays the skylink. The bar is stopped immediately.
func newProgressSkylink(pbs *mpb.Progress, afterBar *mpb.Bar, filename, skylink string) *mpb.Bar {
	bar := pbs.AddBar(
		1, // we'll increment it once to stop it
		mpb.BarQueueAfter(afterBar),
		mpb.PrependDecorators(
			decor.Name(pBarJobDone, decor.WC{W: 10}),
			decor.Name(skylink),
		),
		mpb.AppendDecorators(
			decor.Name(filename, decor.WC{W: len(filename) + 1, C: decor.DidentRight}),
		),
	)
	afterBar.Increment()
	bar.Increment()
	// Wait for finished bars to be rendered.
	pbs.Wait()
	return bar
}
