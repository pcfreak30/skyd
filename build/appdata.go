package build

import (
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
)

// APIPassword returns the Sia API Password either from the environment variable
// or from the password file. If no environment variable is set and no file
// exists, a password file is created and that password is returned
func APIPassword() (string, error) {
	// Check the environment variable.
	pw := os.Getenv(siaAPIPassword)
	if pw != "" {
		return pw, nil
	}

	// Try to read the password from disk.
	path := apiPasswordFilePath()
	pwFile, err := ioutil.ReadFile(path)
	if err == nil {
		// This is the "normal" case, so don't print anything.
		return strings.TrimSpace(string(pwFile)), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	// No password file; generate a secure one.
	// Generate a password file.
	pw, err = createAPIPasswordFile()
	if err != nil {
		return "", err
	}
	return pw, nil
}

// MaxDownloadDiskCache returns the max disk cache used for downloading if
// specified by the environment variable.
func MaxDownloadDiskCache() (uint64, bool, error) {
	cacheSizeStr, set := os.LookupEnv(maxDownloadDiskCache)
	if !set {
		return 0, false, nil
	}
	var cacheSize uint64
	_, err := fmt.Sscan(cacheSizeStr, &cacheSize)
	if err != nil {
		return 0, false, errors.AddContext(err, "failed to parse custom cache size")
	}
	return cacheSize, true, nil
}

// MinDownloadDiskCacheHits returns the min disk cache hits used for the
// download cache if specified by the environment variable.
func MinDownloadDiskCacheHits() (uint, bool, error) {
	minCacheHitsStr, set := os.LookupEnv(minDownloadDiskCacheHits)
	if !set {
		return 0, false, nil
	}
	var minCacheHits uint
	_, err := fmt.Sscan(minCacheHitsStr, &minCacheHits)
	if err != nil {
		return 0, false, errors.AddContext(err, "failed to parse custom min cache hits")
	}
	return minCacheHits, true, nil
}

// DownloadDiskCacheHitDuration returns the disk cache hit duration used for the
// download cache if specified by the environment variable.
func DownloadDiskCacheHitDuration() (time.Duration, bool, error) {
	cacheHitDurationStr, set := os.LookupEnv(downloadDiskCacheHitDuration)
	if !set {
		return 0, false, nil
	}
	var cacheHitDuration uint64
	_, err := fmt.Sscan(cacheHitDurationStr, &cacheHitDuration)
	if err != nil {
		return 0, false, errors.AddContext(err, "failed to parse custom cache hit duration")
	}
	if cacheHitDuration == 0 {
		return 0, false, errors.New("0 is an invalid value for the cache hit duration")
	}
	return time.Duration(cacheHitDuration) * time.Second, true, nil
}

// MongoDBURI returns the URI that the mongodb client in skyd should connect to.
func MongoDBURI() (string, bool) {
	return os.LookupEnv(mongoDBURI)
}

// MongoDBUser returns the user that the mongodb client in skyd should authenticate as.
func MongoDBUser() (string, bool) {
	return os.LookupEnv(mongoDBUser)
}

// MongoDBPassword returns the password that the mongodb client in skyd should authenticate with.
func MongoDBPassword() (string, bool) {
	return os.LookupEnv(mongoDBPassword)
}

// SkynetPortalHostname returns the hostname of the portal and whether it was
// set.
func SkynetPortalHostname() (string, bool) {
	return os.LookupEnv(portalName)
}

// ProfileDir returns the directory where any profiles for the running siad
// instance will be stored
func ProfileDir() string {
	return filepath.Join(SiadDataDir(), "profile")
}

// SiadDataDir returns the siad consensus data directory from the
// environment variable. If there is no environment variable it returns an empty
// string, instructing siad to store the consensus in the current directory.
func SiadDataDir() string {
	return os.Getenv(siadDataDir)
}

// SiaDir returns the Sia data directory either from the environment variable or
// the default.
func SiaDir() string {
	siaDir := os.Getenv(siaDataDir)
	if siaDir == "" {
		siaDir = defaultSiaDir()
	}
	return siaDir
}

// SkynetDir returns the Skynet data directory.
func SkynetDir() string {
	return defaultSkynetDir()
}

// WalletPassword returns the SiaWalletPassword environment variable.
func WalletPassword() string {
	return os.Getenv(siaWalletPassword)
}

// ExchangeRate returns the siaExchangeRate environment variable.
func ExchangeRate() string {
	return os.Getenv(siaExchangeRate)
}

// TUSMaxSize returns the tusMaxSize environment variable if set.
func TUSMaxSize() (int64, bool) {
	maxSizeStr, ok := os.LookupEnv(tusMaxSize)
	if !ok {
		return 0, false
	}
	var maxSize int64
	_, err := fmt.Sscan(maxSizeStr, &maxSize)
	if err != nil {
		Critical("failed to marshal TUS_MAXSIZE environment variable")
		return 0, false
	}
	return maxSize, true
}

// apiPasswordFilePath returns the path to the API's password file. The password
// file is stored in the Sia data directory.
func apiPasswordFilePath() string {
	return filepath.Join(SiaDir(), "apipassword")
}

// createAPIPasswordFile creates an api password file in the Sia data directory
// and returns the newly created password
func createAPIPasswordFile() (string, error) {
	err := os.MkdirAll(SiaDir(), 0700)
	if err != nil {
		return "", err
	}
	// Ensure SiaDir has the correct mode as MkdirAll won't change the mode of
	// an existent directory. We specifically use 0700 in order to prevent
	// potential attackers from accessing the sensitive information inside, both
	// by reading the contents of the directory and/or by creating files with
	// specific names which siad would later on read from and/or write to.
	err = os.Chmod(SiaDir(), 0700)
	if err != nil {
		return "", err
	}
	pw := hex.EncodeToString(fastrand.Bytes(16))
	err = ioutil.WriteFile(apiPasswordFilePath(), []byte(pw+"\n"), 0600)
	if err != nil {
		return "", err
	}
	return pw, nil
}

// defaultSiaDir returns the default data directory of siad. The values for
// supported operating systems are:
//
// Linux:   $HOME/.sia
// MacOS:   $HOME/Library/Application Support/Sia
// Windows: %LOCALAPPDATA%\Sia
func defaultSiaDir() string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "Sia")
	case "darwin":
		return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Sia")
	default:
		return filepath.Join(os.Getenv("HOME"), ".sia")
	}
}

// defaultSkynetDir returns default data directory for miscellaneous Skynet data,
// e.g. skykeys. The values for supported operating systems are:
//
// Linux:   $HOME/.skynet
// MacOS:   $HOME/Library/Application Support/Skynet
// Windows: %LOCALAPPDATA%\Skynet
func defaultSkynetDir() string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "Skynet")
	case "darwin":
		return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Skynet")
	default:
		return filepath.Join(os.Getenv("HOME"), ".skynet")
	}
}
