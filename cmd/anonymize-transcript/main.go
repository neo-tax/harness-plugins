// Command anonymize-transcript turns a real Claude Code session transcript,
// with its subagent transcripts, into a parser fixture directory.
//
//	go run ./cmd/anonymize-transcript <session.jsonl> <fixture-dir>
//
// Review the output before committing it; then generate its expected event
// stream with: go test ./internal/adapter/claudecode -run TestFixtures -update
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/neo-tax/harness-plugins/internal/adapter/claudecode"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: anonymize-transcript <session.jsonl> <fixture-dir>")
		os.Exit(2)
	}
	src, dst := os.Args[1], os.Args[2]
	a := claudecode.NewAnonymizer()
	session := strings.TrimSuffix(filepath.Base(src), ".jsonl")
	if err := copyAnon(a, src, filepath.Join(dst, session+".jsonl")); err != nil {
		fail(err)
	}
	subs, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(src, ".jsonl"), "subagents", "*.jsonl"))
	for _, s := range subs {
		if err := copyAnon(a, s, filepath.Join(dst, session, "subagents", filepath.Base(s))); err != nil {
			fail(err)
		}
	}
}

func copyAnon(a *claudecode.Anonymizer, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := a.Anonymize(in, out); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "anonymize-transcript:", err)
	os.Exit(1)
}
