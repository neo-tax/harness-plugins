package release_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neo-tax/harness-plugins/internal/release"
)

type plugin struct {
	Name    string         `json:"name"`
	Version string         `json:"version"`
	Source  map[string]any `json:"source"`
}

// repo writes a minimal repository layout at version v with the marketplace
// pinned to ref.
func repo(t *testing.T, v, ref string, latest bool) string {
	t.Helper()
	root := t.TempDir()
	write := func(path string, v any) {
		b, _ := json.MarshalIndent(v, "", "  ")
		os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755)
		os.WriteFile(filepath.Join(root, path), b, 0o644)
	}
	write("plugins/claude-code/.claude-plugin/plugin.json", map[string]any{"name": "neo-tax", "version": v})
	src := map[string]any{"source": "git-subdir", "url": release.Repository, "path": "plugins/claude-code", "ref": ref}
	write(".claude-plugin/marketplace.json", map[string]any{
		"name": "neo-tax", "metadata": map[string]any{"version": v},
		"plugins": []plugin{{Name: "neo-tax", Version: v, Source: src}},
	})
	if latest {
		lsrc := map[string]any{"source": "git-subdir", "url": release.Repository, "path": "plugins/claude-code", "ref": "main"}
		write("channels/latest/marketplace.json", map[string]any{
			"name": "neo-tax-latest", "metadata": map[string]any{"version": v},
			"plugins": []plugin{{Name: "neo-tax", Version: v, Source: lsrc}},
		})
	}
	return root
}

// @spec: test-and-release#manifest-version-matches-tag
// @spec: test-and-release#marketplace-source-is-this-repo
// @spec: test-and-release#single-channel-v0-1
// @spec: test-and-release#stable-and-latest-channels-v1-0
func TestCheck(t *testing.T) {
	cases := []struct {
		name    string
		root    string
		tag     string
		wantErr string
	}{
		{"v0 ok", repo(t, "0.1.0", "v0.1.0", false), "v0.1.0", ""},
		{"not semver", repo(t, "0.1.0", "v0.1.0", false), "v0.1", "semantic-version"},
		{"plugin version differs", repo(t, "0.1.0", "v0.2.0", false), "v0.2.0", "version"},
		{"source not at tag", repo(t, "0.2.0", "main", false), "v0.2.0", "ref"},
		{"v0 has latest channel", repo(t, "0.1.0", "v0.1.0", true), "v0.1.0", "single channel"},
		{"v1 without latest", repo(t, "1.0.0", "v1.0.0", false), "v1.0.0", "latest channel"},
		{"v1 ok", repo(t, "1.0.0", "v1.0.0", true), "v1.0.0", ""},
	}
	for _, c := range cases {
		err := release.Check(c.root, c.tag)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: err %v, want %q", c.name, err, c.wantErr)
		}
	}
}

// The committed manifests pass for the version they declare.
func TestRepositoryManifests(t *testing.T) {
	b, err := os.ReadFile("../../plugins/claude-code/.claude-plugin/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var p plugin
	json.Unmarshal(b, &p)
	if err := release.Check("../..", "v"+p.Version); err != nil {
		t.Fatal(err)
	}
}
