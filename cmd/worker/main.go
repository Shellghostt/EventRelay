// Command worker is the entry point for EventRelay.
//
// It consumes queued deliveries and performs HTTP delivery with retries.
//
// Status: skeleton. Nothing is implemented yet. See docs/PLAN.md.
package main

import (
	"fmt"
	"os"
)

// version is overridden at build time with:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always)"
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Printf("%s %s\n", "worker", version)
		return
	}

	fmt.Println("worker EventRelay - skeleton, not implemented")
	fmt.Println("Read docs/PLAN.md before running this binary.")
	os.Exit(0)
}
