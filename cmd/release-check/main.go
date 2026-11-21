// Command release-check fails when the plugin manifests disagree with a
// release tag, with -bump moves the manifests to the next version and prints
// its tag, and with -pin points the stable marketplace at the tag after its
// release is published.
//
//	go run ./cmd/release-check v0.1.0
//	go run ./cmd/release-check -bump patch
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
	bump := flag.Bool("bump", false, "bump the manifests by major, minor, or patch and print the new tag")
	flag.Parse()
	if flag.NArg() != 1 || (*pin && *bump) {
		fmt.Fprintln(os.Stderr, "usage: release-check [-pin] <tag> | -bump major|minor|patch")
		os.Exit(2)
	}
	if *bump {
		tag, err := release.Bump(".", flag.Arg(0))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(tag)
		return
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
