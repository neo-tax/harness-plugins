// Command usage-audit checks the agent's token counts against the API's own.
//
//	usage-audit proxy [-addr 127.0.0.1:8788] [-log usage.jsonl]
//	    Run Claude Code with ANTHROPIC_BASE_URL=http://127.0.0.1:8788 to log
//	    the usage the API returns for every request it makes.
//	usage-audit compare -log usage.jsonl [-events http://127.0.0.1:8787/_fake/events] [-json]
//	    Match the logged requests to the session.turn events the fake ingest
//	    server accepted and report what the events miss or count differently.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/neo-tax/harness-plugins/internal/usageaudit"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: usage-audit proxy|compare [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "proxy":
		proxy(os.Args[2:])
	case "compare":
		compare(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "usage-audit: unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

func proxy(args []string) {
	fs := flag.NewFlagSet("proxy", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8788", "listen address")
	upstream := fs.String("upstream", "https://api.anthropic.com", "API base URL")
	logPath := fs.String("log", "usage.jsonl", "append one JSON line per request to this file")
	fs.Parse(args)
	f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	p, err := usageaudit.NewProxy(*upstream, f)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "usage-audit proxying http://%s to %s, logging to %s\n", *addr, *upstream, *logPath)
	log.Fatal(http.ListenAndServe(*addr, p))
}

func compare(args []string) {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	logPath := fs.String("log", "usage.jsonl", "proxy log")
	eventsSrc := fs.String("events", "http://127.0.0.1:8787/_fake/events", "fake ingest events URL, or a file of JSON lines from fake-ingest -record")
	asJSON := fs.Bool("json", false, "print the full report as JSON")
	fs.Parse(args)

	f, err := os.Open(*logPath)
	if err != nil {
		log.Fatal(err)
	}
	records, err := usageaudit.ReadRecords(f)
	f.Close()
	if err != nil {
		log.Fatal(err)
	}
	events, err := readEvents(*eventsSrc)
	if err != nil {
		log.Fatal(err)
	}
	rep, err := usageaudit.Compare(records, events)
	if err != nil {
		log.Fatal(err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rep)
		return
	}
	printReport(os.Stdout, rep)
}

func readEvents(src string) ([]json.RawMessage, error) {
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		resp, err := http.Get(src)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", src, resp.Status)
		}
		var out []json.RawMessage
		return out, json.NewDecoder(resp.Body).Decode(&out)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	var out []json.RawMessage
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, json.RawMessage(line))
		}
	}
	return out, nil
}

func printReport(w io.Writer, r usageaudit.Report) {
	billable := r.Matched + len(r.Missing)
	fmt.Fprintf(w, "Billable API requests: %d; reported as turns: %d; missing: %d; counts differ: %d\n",
		billable, r.Matched, len(r.Missing), len(r.Mismatched))
	row := func(name string, u usageaudit.Usage) {
		fmt.Fprintf(w, "  %-22s input %9d  output %8d  cache read %10d  write 5m %8d  write 1h %8d  total %10d\n",
			name, u.Input, u.Output, u.CacheRead, u.CacheWrite5m, u.CacheWrite1h, u.Total())
	}
	row("API (all billable)", r.API)
	row("events (matched)", r.Events)
	row("side usage (closes)", r.Side)
	if t := r.API.Total(); t > 0 {
		var missing int64
		for _, u := range r.MissingByModel {
			missing += u.Total()
		}
		fmt.Fprintf(w, "Tokens in requests with no turn: %d (%.2f%% of API total)\n", missing, 100*float64(missing)/float64(t))
	}
	if t := r.API.Total(); t > 0 {
		fmt.Fprintf(w, "Tokens neither turns nor side usage report: %d (%.2f%% of API total)\n", r.Unaccounted(), 100*float64(r.Unaccounted())/float64(t))
	}
	if len(r.MissingByModel) > 0 {
		fmt.Fprintln(w, "Missing, by model:")
		models := make([]string, 0, len(r.MissingByModel))
		for m := range r.MissingByModel {
			models = append(models, m)
		}
		sort.Strings(models)
		for _, m := range models {
			row(m, r.MissingByModel[m])
		}
	}
	for _, m := range r.Mismatched {
		fmt.Fprintf(w, "Differs %s (%s): API %+v, event %+v\n", m.RequestID, m.Model, m.API, m.Event)
	}
	if len(r.Unexplained) > 0 {
		fmt.Fprintf(w, "Turns the proxy never saw (other sessions, or a backfill): %d\n", len(r.Unexplained))
	}
}
