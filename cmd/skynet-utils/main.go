package main

import (
	"fmt"
	"os"
	"text/tabwriter"
)

// printHelp lists all of the supported commands and their functions.
func printHelp() {
	// Basic output
	fmt.Println("skynet-utils v0.0.1")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("	skynet-utils [command]")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("	skynet-utils generate-seed")
	fmt.Println("	skynet-utils g")
	fmt.Println()
	fmt.Println("Available Commands:")

	// List the commands through a cleanly formatted tabwriter.
	w := tabwriter.NewWriter(os.Stdout, 2, 0, 2, ' ', 0)
	fmt.Fprintf(w, "\tgenerate-seed (g)\tgenerates a secure seed\n")
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

// main checks the args to figure out what command to run, then calls the
// corresponding command.
func main() {
	args := os.Args
	if len(args) == 2 {
		switch args[1] {
		case "generate-seed", "g":
			generateAndPrintSeed()
		}
	}
	printHelp()
}
