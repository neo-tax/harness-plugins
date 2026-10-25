package claudecode_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/neo-tax/harness-plugins/internal/adapter/claudecode"
)

// @spec: device-enrollment#email-candidates
func TestAccountEmail(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if got := claudecode.AccountEmail(); got != "" {
		t.Fatalf("no file: %q", got)
	}
	os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"me@example.com","displayName":"Me"},"projects":{"/Users/me/secret":{}}}`), 0o600)
	if got := claudecode.AccountEmail(); got != "me@example.com" {
		t.Fatalf("got %q", got)
	}
	os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`not json`), 0o600)
	if got := claudecode.AccountEmail(); got != "" {
		t.Fatalf("corrupt: %q", got)
	}
}
