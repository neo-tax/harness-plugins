// Command privacy-statement prints the published privacy statement, generated
// from the agent's field allowlist.
package main

import (
	"fmt"

	"github.com/neo-tax/harness-plugins/internal/allowlist"
)

func main() { fmt.Print(allowlist.Statement()) }
