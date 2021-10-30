package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
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
	fmt.Fprintf(w, "\tgenerate-pubkey [salt] [seed]\tgenerates a pubkey from a seed using the provided salt\n")
	err := w.Flush()
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
	dataKey := crypto.HashObject(dataKeyBase)

	// Get the actual crypto keys.
	_, pk := crypto.GenerateKeyPairDeterministic(entropy)
	spk := types.SiaPublicKey{
		Algorithm: types.SignatureEd25519,
		Key: pk[:],
	}
	skylinkV2 := skymodules.NewSkylinkV2(spk, dataKey)

	// Print the salt and seed.
	fmt.Println(skylinkV2)
	os.Exit(0)
}

// main checks the args to figure out what command to run, then calls the
// corresponding command.
func main() {
	args := os.Args
	if len(args) == 2 {
		switch args[1] {
		case "generate-seed", "s":
			generateAndPrintSeed()
		}
	}
	if len(args) > 3 {
		switch args[1] {
		case "generate-v2Skylink", "v2":
			generateV2SkylinkFromSeed(args[2], args[3:])
		}
	}
	printHelp()
}
