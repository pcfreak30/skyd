package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/node/api"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/modules"
)

// fileData is a small helper for reading and returning the data from a file.
func fileData(filename string) []byte {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		die(err)
	}
	return data
}

// printSkynetDirs is a helper for printing skynet directoryInfos
func printSkynetDirs(dirs []directoryInfo, recursive bool) error {
	for _, dir := range dirs {
		// Don't print directories that have 0 skyfiles if they are outside the
		// skynet folder. We print directories that are within the skynet folder
		// that have 0 skyfiles because a user would expect to see the skynet
		// folder structure they have created. If they want to see the folder
		// structure they have created outside of the Skynet Folder then they should
		// use the renter ls command.
		if !skymodules.IsSkynetDir(dir.dir.SiaPath) && dir.dir.SkynetFiles == 0 {
			continue
		}

		// Initialize a tab writer for the diretory
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

		// Print the directory SiaPath
		fmt.Fprintf(w, "%v/", dir.dir.SiaPath)

		// In Skyfile only directories, NumFiles is equal to SkynetFiles
		omitted := dir.dir.NumFiles - dir.dir.SkynetFiles
		if omitted > 0 {
			fmt.Fprintf(w, "\t(%v omitted)", omitted)
		}
		fmt.Fprintln(w)

		// Print subdirs.
		for _, subDir := range dir.subDirs {
			// Don't print directories that have 0 skyfiles if they are outside the
			// skynet folder.
			if !skymodules.IsSkynetDir(subDir.SiaPath) && dir.dir.SkynetFiles == 0 {
				continue
			}
			subDirName := subDir.SiaPath.Name() + "/"
			sizeUnits := modules.FilesizeUnits(subDir.AggregateSkynetSize)
			fmt.Fprintf(w, "  %v\t\t%9v\n", subDirName, sizeUnits)
		}

		// Print skyfiles if the directory contains any. This is for printing only
		// skyfiles from directories that are outside the Skynet Folder.
		if dir.dir.SkynetFiles != 0 {
			printSkyFiles(w, dir.files)
		}
		fmt.Fprintln(w)

		// Flush the writer
		if err := w.Flush(); err != nil {
			return errors.AddContext(err, "failed to flush writer")
		}

		// Check if this was a recursive request.
		if !recursive {
			// If not recursive, finish early after the first dir.
			return nil
		}
	}
	return nil
}

// printSkyFiles is a helper for printing out Skyfile information
func printSkyFiles(w io.Writer, files []skymodules.FileInfo) {
	fmt.Fprintf(w, "  Filename\tSkylink\tFilesize\n")
	for _, file := range files {
		// Skip any non skyfiles
		if len(file.Skylinks) == 0 {
			continue
		}
		// Print Skyfile
		name := file.SiaPath.Name()
		firstSkylink := file.Skylinks[0]
		size := modules.FilesizeUnits(file.Filesize)
		fmt.Fprintf(w, "  %v\t%v\t%9v\n", name, firstSkylink, size)
		for _, skylink := range file.Skylinks[1:] {
			fmt.Fprintf(w, "\t%v\t\n", skylink)
		}
	}
}

// sanitizeSkylinks will trim away `sia://` from skylinks
func sanitizeSkylinks(links []string) []string {
	var result []string

	for _, link := range links {
		trimmed := strings.TrimPrefix(link, "sia://")
		result = append(result, trimmed)
	}

	return result
}

// smallSkyfileDownload is a helper that downloads a small skyfile and returns
// the file data, layout, and metadata.
func smallSkyfileDownload(skylink string) (fileData []byte, sl skymodules.SkyfileLayout, sm []byte, _ error) {
	// If no portal is set, we download the baseSector and parse the
	// SkyfileLayout and SkyfileMetadata from it.
	if skynetDownloadPortal == "" {
		reader, err := httpClient.SkynetBaseSectorGet(skylink)
		if err != nil {
			return nil, sl, sm, errors.AddContext(err, "unable to download basesector")
		}
		baseSector, err := ioutil.ReadAll(reader)
		if err != nil {
			return nil, sl, sm, errors.AddContext(err, "unable to reader basesector data from reader")
		}
		sl, _, _, sm, fileData, err = skymodules.ParseSkyfileMetadata(baseSector)
		if err != nil {
			return nil, sl, sm, errors.AddContext(err, "unable to parse layout and metadata from basesector")
		}
	} else {
		// Download the SkyfileLayout and SkyfileMetadata from the portal
		url := fmt.Sprintf("%s/%s?include-layout=true&nocache=true", skynetDownloadPortal, skylink)
		resp, err := http.Get(url)
		if err != nil {
			return nil, sl, sm, errors.AddContext(err, "unable to download from the portal")
		}
		defer func() {
			_ = resp.Body.Close()
		}()

		// Read the fileData
		fileData, err = ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, sl, sm, errors.AddContext(err, "unable to read fileData")
		}

		// Grab the Layout
		layoutStr := resp.Header.Get(api.SkynetFileLayoutHeader)
		if layoutStr == "" {
			fmt.Println("No Layout returned from Portal")
		} else {
			// Decode the layout
			layoutBytes, err := hex.DecodeString(layoutStr)
			if err != nil {
				return nil, sl, sm, errors.AddContext(err, "unable to decode layout string from header")
			}
			sl.Decode(layoutBytes)
		}

		// Grab the metadata
		metadataStr := resp.Header.Get(api.SkynetFileMetadataHeader)
		if metadataStr == "" {
			fmt.Println("No metadata returned from Portal")
		} else {
			sm = []byte(metadataStr)
		}
	}
	return fileData, sl, sm, nil
}
