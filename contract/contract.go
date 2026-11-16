// Package contract holds the wire contract between the agent and Neo.Tax: the
// JSON Schema documents in this directory and Go types written to match them.
// The schemas are the source of truth; the Go types follow them.
//
// @spec: session-event-wire-contract#schema-is-source-of-truth
// @spec: session-event-wire-contract#four-schema-documents
package contract

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// Version is the contract_version this build of the contract defines.
const Version = "1"

// AcceptedVersions lists every contract_version a server built from this
// contract accepts.
var AcceptedVersions = []string{Version}

// Schema document file names.
const (
	SessionEventSchema   = "session-event.schema.json"
	IngestRequestSchema  = "ingest-request.schema.json"
	IngestResponseSchema = "ingest-response.schema.json"
	EnrollmentSchema     = "enrollment.schema.json"
)

//go:embed *.schema.json
var schemaFS embed.FS

// Schemas returns the embedded schema documents.
func Schemas() fs.FS { return schemaFS }

const baseURL = "https://neo.tax/contract/"

var (
	compileOnce sync.Once
	compiled    map[string]*jsonschema.Schema
	compileErr  error
)

func schemas() (map[string]*jsonschema.Schema, error) {
	compileOnce.Do(func() {
		c := jsonschema.NewCompiler()
		c.AssertFormat()
		entries, err := fs.Glob(schemaFS, "*.schema.json")
		if err != nil {
			compileErr = err
			return
		}
		for _, name := range entries {
			b, err := schemaFS.ReadFile(name)
			if err != nil {
				compileErr = err
				return
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
			if err != nil {
				compileErr = fmt.Errorf("%s: %w", name, err)
				return
			}
			if err := c.AddResource(baseURL+name, doc); err != nil {
				compileErr = err
				return
			}
		}
		compiled = map[string]*jsonschema.Schema{}
		refs := map[string]string{
			"event":               SessionEventSchema,
			"ingest_request":      IngestRequestSchema,
			"ingest_response":     IngestResponseSchema,
			"ingest_error":        IngestResponseSchema + "#/$defs/error",
			"enrollment_request":  EnrollmentSchema + "#/$defs/request",
			"enrollment_response": EnrollmentSchema + "#/$defs/response",
		}
		for key, ref := range refs {
			s, err := c.Compile(baseURL + ref)
			if err != nil {
				compileErr = fmt.Errorf("compile %s: %w", ref, err)
				return
			}
			compiled[key] = s
		}
	})
	return compiled, compileErr
}

func validate(key string, raw []byte) error {
	s, err := schemas()
	if err != nil {
		return err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return s[key].Validate(doc)
}

// ValidateEvent validates one session event.
func ValidateEvent(raw []byte) error { return validate("event", raw) }

// ValidateIngestRequest validates the envelope of a batched ingest request.
// Individual events are validated separately with ValidateEvent so that one
// invalid event does not reject the batch.
func ValidateIngestRequest(raw []byte) error { return validate("ingest_request", raw) }

// ValidateIngestResponse validates a successful ingest response.
func ValidateIngestResponse(raw []byte) error { return validate("ingest_response", raw) }

// ValidateIngestError validates an error response body.
func ValidateIngestError(raw []byte) error { return validate("ingest_error", raw) }

// ValidateEnrollmentRequest validates an enrollment request.
func ValidateEnrollmentRequest(raw []byte) error { return validate("enrollment_request", raw) }

// ValidateEnrollmentResponse validates a successful enrollment response.
func ValidateEnrollmentResponse(raw []byte) error { return validate("enrollment_response", raw) }

// ErrorClass describes a validation error by the keywords that failed and the
// locations they failed at, for example "pattern at /head_sha". It never
// includes an instance value: the validator's own message echoes the value
// for some keywords (pattern, format), so it must not reach a log line.
//
// Under the event schema's oneOf, branches for other event kinds always fail
// on event_kind; those branches are skipped so the class names only what is
// wrong with the event's own kind.
//
// @spec: field-allowlist#values-validated-before-queue
func ErrorClass(err error) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return "not a schema validation error"
	}
	seen := map[string]bool{}
	var out, fallback []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			kw := "false schema"
			if p := e.ErrorKind.KeywordPath(); len(p) > 0 {
				kw = p[len(p)-1]
			}
			s := kw + " at /" + strings.Join(e.InstanceLocation, "/")
			if seen[s] {
				return
			}
			seen[s] = true
			if _, ok := e.ErrorKind.(*kind.FalseSchema); ok {
				// unevaluatedProperties noise that follows a failed oneOf.
				fallback = append(fallback, s)
			} else {
				out = append(out, s)
			}
			return
		}
		for _, c := range e.Causes {
			if _, ok := e.ErrorKind.(*kind.OneOf); ok && failsEventKind(c) {
				continue
			}
			walk(c)
		}
	}
	walk(ve)
	if len(out) == 0 {
		out = fallback
	}
	sort.Strings(out)
	return strings.Join(out, "; ")
}

// failsEventKind reports whether a oneOf branch failed because event_kind is
// another kind's.
func failsEventKind(e *jsonschema.ValidationError) bool {
	if _, ok := e.ErrorKind.(*kind.Const); ok && len(e.InstanceLocation) == 1 && e.InstanceLocation[0] == "event_kind" {
		return true
	}
	for _, c := range e.Causes {
		if failsEventKind(c) {
			return true
		}
	}
	return false
}
