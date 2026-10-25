// Command release-check fails when any plugin or marketplace manifest
// disagrees with the release tag.
//
//	go run ./cmd/release-check v0.1.0
package main

import (
	"fmt"
	"os"

	"github.com/neo-tax/harness-plugins/internal/release"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: release-check <tag>")
		os.Exit(2)
	}
	if err := release.Check(".", os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
