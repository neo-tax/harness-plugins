// Command release-check fails when the plugin manifests disagree with a
// release tag, and with -bump moves the manifests, the stable marketplace's
// pin included, to the next version and prints its tag.
//
//	go run ./cmd/release-check v0.1.0
//	go run ./cmd/release-check -bump patch
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/neo-tax/harness-plugins/internal/release"
)

func main() {
	bump := flag.Bool("bump", false, "bump the manifests by major, minor, or patch and print the new tag")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: release-check <tag> | -bump major|minor|patch")
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
	if err := release.Check(".", flag.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
