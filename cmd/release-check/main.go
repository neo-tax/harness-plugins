// Command release-check fails when the plugin manifests disagree with a
// release tag, and with -pin points the stable marketplace at the tag after
// its release is published.
//
//	go run ./cmd/release-check v0.1.0
//	go run ./cmd/release-check -pin v0.1.0
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/neo-tax/harness-plugins/internal/release"
)

func main() {
	pin := flag.Bool("pin", false, "pin .claude-plugin/marketplace.json to the tag")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: release-check [-pin] <tag>")
		os.Exit(2)
	}
	tag := flag.Arg(0)
	if *pin {
		if err := release.Pin(".", tag); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if err := release.Check(".", tag); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
