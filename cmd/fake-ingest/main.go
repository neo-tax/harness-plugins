// Command fake-ingest runs a stand-in for Neo.Tax's enrollment and ingest
// endpoints that validates every request against this repository's wire
// contract and records what it accepts, and for its OTLP endpoints, which
// record every log record and metric data point a harness's telemetry sends.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/neo-tax/harness-plugins/internal/fakeingest"
)

type tokens []string

func (t *tokens) String() string     { return strings.Join(*t, ",") }
func (t *tokens) Set(v string) error { *t = append(*t, v); return nil }

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address")
	base := flag.String("ingest-base-url", "", "ingest base URL returned at enrollment (default: the request's own host)")
	record := flag.String("record", "", "append each accepted event as a JSON line to this file (- for stdout)")
	recordOTLP := flag.String("record-otlp", "", "append each OTLP log record or metric data point as a JSON line to this file")
	var toks, ingestKeys tokens
	flag.Var(&toks, "enrollment-token", "enrollment token to accept (repeatable; default: accept any)")
	flag.Var(&ingestKeys, "ingest-key", "OTLP ingest key to accept (repeatable; default: accept any but a device key)")
	flag.Parse()

	opts := fakeingest.Options{EnrollmentTokens: toks, IngestBaseURL: *base, IngestKeys: ingestKeys}
	if *recordOTLP != "" {
		f, err := os.OpenFile(*recordOTLP, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		opts.RecordOTLP = f
	}
	switch *record {
	case "":
	case "-":
		opts.Record = os.Stdout
	default:
		f, err := os.OpenFile(*record, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		opts.Record = f
	}
	var out io.Writer = os.Stderr
	fmt.Fprintf(out, "fake-ingest listening on http://%s\n", *addr)
	log.Fatal(http.ListenAndServe(*addr, fakeingest.New(opts)))
}
