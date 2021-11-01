package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"gitlab.com/SkynetLabs/skyd/node/api/client"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"github.com/SkynetLabs/go-skynet/v2"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// printHelp lists all of the supported commands and their functions.
func printHelp() {
	// Basic output
	fmt.Println("skynet-utils v0.0.1")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("	skynet-utils [command]")
	fmt.Println()
	fmt.Println("Available Commands:")
	// List the commands through a cleanly formatted tabwriter.
	w := tabwriter.NewWriter(os.Stdout, 2, 0, 2, ' ', 0)
	fmt.Fprintf(w, "\tgenerate-seed\tgenerates a secure seed\n")
	fmt.Fprintf(w, "\tgenerate-v2skylink [salt] [seed]\tgenerates a pubkey from a seed using the provided salt\n")
	fmt.Fprintf(w, "\tupload-file [filepath]\tuploads the provided file and returns a skylink\n")
	err := w.Flush()
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	fmt.Println("Shortcuts:")
	// List the commands through a cleanly formatted tabwriter.
	w = tabwriter.NewWriter(os.Stdout, 2, 0, 2, ' ', 0)
	fmt.Fprintf(w, "\tgenerate-seed\t(g) (s) (gs)\n")
	fmt.Fprintf(w, "\tgenerate-v2skylink\t(p) (v2)\n")
	fmt.Fprintf(w, "\tupload-file\t(u) (uf)\n")
	err = w.Flush()
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}

// generateAndPrintSeed will generate a new seed and print it.
func generateAndPrintSeed() {
	seed := generateSeed()
	fmt.Println(seed)
	os.Exit(0)
}

// generateV2SkylinkFromSeed will generate a V2 skylink from a seed using the
// specified salt.
func generateV2SkylinkFromSeed(salt string, phraseWords []string) {
	// Get the crypto keys.
	_, spk, dataKey := skylinkKeysFromPhraseWords(salt, phraseWords)
	skylinkV2 := skymodules.NewSkylinkV2(spk, dataKey)

	// Print the salt and seed.
	fmt.Println(skylinkV2)
	os.Exit(0)
}

// uploadToV2Skylnk will upload the provided v1Skylink to the v2Skylink that
// corresponds to the provided salt and phraseWords.
func uploadToV2Skylink(v1Skylink string, salt string, phraseWords []string) {
	// Get the keys that we need.
	sk, spk, dataKey := skylinkKeysFromPhraseWords(salt, phraseWords)

	// Get the raw bytes of the v1 skylink.
	var skylink skymodules.Skylink
	err := skylink.LoadString(v1Skylink)
	if err != nil {
		fmt.Println("Invalid skylink:", err)
		os.Exit(1)
	}
	linkBytes := skylink.Bytes()

	// Create a signed registry entry containing the v1skylink.
	//
	// TODO: Need to learn what revision number we are supposed to get,
	// can't just use 0.
	srv := modules.NewRegistryValue(dataKey, linkBytes, 0, modules.RegistryTypeWithoutPubkey).Sign(sk)

	// TODO: Need to upload the srv to a portal. Check out
	// testNode.RegistryUpdate.
	//
	// TODO: Need to adjust the client so that we're using the portal
	// environment variable.
	c := client.New(client.Options{
		Address: "siasky.net",
	})
	fmt.Println(spk)
	fmt.Println()
	fmt.Println(srv)
	err = c.RegistryUpdateWithEntry(spk, srv)
	if err != nil {
		fmt.Println("Error while trying to update the registry:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// uploadFile will upload a file to the user's preferred skynet portal, which
// is detected via an environment variable. If no portal is set, siasky.net is
// used.
//
// TODO: We need to update this function to verify that the skylink being
// returned by the portal is correct. We should probably do this by extending
// the client.
func uploadFile(path string) {
	client := skynet.New()
	skylink, err := client.UploadFile(path, skynet.DefaultUploadOptions)
	if err != nil {
		fmt.Println("Upload failed:", err)
		os.Exit(1)
	}
	skylink = strings.TrimPrefix(skylink, "sia://")
	fmt.Println(skylink)
	os.Exit(0)
}

// skylinkKeysFromPhraseWords returns the entropy for a given seed and salt.
func skylinkKeysFromPhraseWords(salt string, phraseWords []string) (sk crypto.SecretKey, spk types.SiaPublicKey, dataKey crypto.Hash) {
	// Turn the phrase words in to a phrase.
	var phrase string
	for i, word := range phraseWords {
		phrase += word
		if i != len(phraseWords)-1 {
			phrase += " "
		}
	}

	// Turn the phrase into entropy.
	seed, err := readSeed(phrase)
	if err != nil {
		fmt.Println("Invalid seed provided:", err)
		os.Exit(1)
	}
	// Use the salt to deterministically generate entropy for this specific
	// V2 link. Add some pepper to the salt to minimize footgun potential.
	//
	// We want the data key to appear random, so we are going to hash a
	// value deterministically to get that as well. We are going to use a
	// different pepper but the same salt to get the data key.
	saltedSeed := "v2SkylinkFromSeed" + salt + string(seed[:])
	dataKeyBase := "v2SkylinkFromSeedDataKey" + salt + string(seed[:])
	entropy := crypto.HashObject(saltedSeed)
	dataKey = crypto.HashObject(dataKeyBase)

	// Get the actual crypto keys.
	sk, pk := crypto.GenerateKeyPairDeterministic(entropy)
	spk = types.Ed25519PublicKey(pk)

	// Return the secret key and the SiaPublicKey.
	return sk, spk, dataKey
}

// main checks the args to figure out what command to run, then calls the
// corresponding command.
func main() {
	args := os.Args
	if len(args) == 2 {
		switch args[1] {
		case "generate-seed", "g", "s", "gs":
			generateAndPrintSeed()
		}
	}
	if len(args) == 3 {
		switch args[1] {
		case "upload-file", "u", "uf":
			uploadFile(args[2])
		}
	}
	if len(args) > 3 {
		switch args[1] {
		case "generate-v2skylink", "p", "v2":
			generateV2SkylinkFromSeed(args[2], args[3:])
		case "upload-to-v2skylink", "u2", "u2v2", "utv", "utv2":
			uploadToV2Skylink(args[2], args[3], args[4:])
		}
	}
	printHelp()
}
