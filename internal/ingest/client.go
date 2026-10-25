// Package ingest is the agent's HTTP client for Neo.Tax's enrollment and
// ingest endpoints, speaking the wire contract in package contract.
package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/neo-tax/harness-plugins/contract"
)

// StatusError is a non-2xx response. Body is the server's error message,
// never request content.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string { return fmt.Sprintf("ingest: HTTP %d: %s", e.Code, e.Message) }

// IsAuth reports whether the device key was rejected (401) or is not
// permitted to ingest (403).
func (e *StatusError) IsAuth() bool {
	return e.Code == http.StatusUnauthorized || e.Code == http.StatusForbidden
}

// Client sends batches of session events with a device key.
type Client struct {
	HTTP      *http.Client
	BaseURL   string
	DeviceKey string
}

// Send posts one batch, oldest first, and returns the server's account of it.
func (c *Client) Send(ctx context.Context, events []json.RawMessage) (contract.IngestResponse, error) {
	var out contract.IngestResponse
	body, err := json.Marshal(contract.IngestRequest{ContractVersion: contract.Version, Events: events})
	if err != nil {
		return out, err
	}
	err = do(ctx, c.HTTP, endpoint(c.BaseURL, "/v1/events"), c.DeviceKey, body, &out)
	return out, err
}

// Enroll exchanges an enrollment token for a device key.
func Enroll(ctx context.Context, hc *http.Client, baseURL string, req contract.EnrollmentRequest) (contract.EnrollmentResponse, error) {
	var out contract.EnrollmentResponse
	body, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	if err := do(ctx, hc, endpoint(baseURL, "/v1/enroll"), "", body, &out); err != nil {
		return out, err
	}
	if err := contract.ValidateEnrollmentResponse(mustJSON(out)); err != nil {
		return out, fmt.Errorf("enroll: malformed response: %w", err)
	}
	return out, nil
}

func endpoint(base, path string) string { return strings.TrimRight(base, "/") + path }

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func do(ctx context.Context, hc *http.Client, url, key string, body []byte, out any) error {
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e contract.ErrorResponse
		json.Unmarshal(respBody, &e)
		msg := e.Error
		if len(e.AcceptedVersions) > 0 {
			msg += fmt.Sprintf(" (accepted versions: %s)", strings.Join(e.AcceptedVersions, ", "))
		}
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return &StatusError{Code: resp.StatusCode, Message: msg}
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("ingest: malformed response: %w", err)
	}
	return nil
}
