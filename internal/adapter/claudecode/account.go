// Package claudecode is the Claude Code adapter: it maps Claude Code's hook
// payloads and on-disk transcripts into session events.
package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Harness is the harness name carried on every event from this adapter.
const Harness = "claude-code"

// ConfigDir is Claude Code's configuration directory: $CLAUDE_CONFIG_DIR, or
// ~/.claude.
func ConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// accountFile is where Claude Code keeps the signed-in account.
func accountFile() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

// AccountEmail returns the email of the account Claude Code is signed in
// with, or "". Only the email is decoded. It is read during login only,
// never on the hook path.
//
// @spec: device-enrollment#email-candidates
func AccountEmail() string {
	b, err := os.ReadFile(accountFile())
	if err != nil {
		return ""
	}
	var f struct {
		OAuthAccount struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(b, &f) != nil {
		return ""
	}
	return f.OAuthAccount.EmailAddress
}
