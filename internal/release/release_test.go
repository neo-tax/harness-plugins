package release_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neo-tax/harness-plugins/internal/release"
)

type layout struct {
	version     string // plugin.json version
	pluginName  string
	source      map[string]any
	latest      bool
	latestBytes string // raw latest-channel manifest, overriding latest
}

func defaultSource(ref string) map[string]any {
	return map[string]any{"source": "git-subdir", "url": release.Repository, "path": "plugins/claude-code", "ref": ref}
}

func write(t *testing.T, root, path string, v any) {
	t.Helper()
	b, _ := json.MarshalIndent(v, "", "  ")
	os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755)
	os.WriteFile(filepath.Join(root, path), b, 0o644)
}

// repo writes a repository layout. The stable marketplace still names the
// previous release, as it does on main until a release is published.
func repo(t *testing.T, l layout) string {
	t.Helper()
	root := t.TempDir()
	if l.pluginName == "" {
		l.pluginName = "neo-tax"
	}
	if l.source == nil {
		l.source = defaultSource("v0.0.9")
	}
	write(t, root, "plugins/claude-code/.claude-plugin/plugin.json", map[string]any{"name": l.pluginName, "version": l.version})
	write(t, root, ".claude-plugin/marketplace.json", map[string]any{
		"name": "neo-tax", "owner": map[string]any{"name": "Neo.Tax"}, "metadata": map[string]any{"version": "0.0.9"},
		"plugins": []any{map[string]any{"name": "neo-tax", "version": "0.0.9", "description": "d", "source": l.source}},
	})
	switch {
	case l.latestBytes != "":
		os.MkdirAll(filepath.Join(root, "channels/latest"), 0o755)
		os.WriteFile(filepath.Join(root, "channels/latest/marketplace.json"), []byte(l.latestBytes), 0o644)
	case l.latest:
		write(t, root, "channels/latest/marketplace.json", map[string]any{
			"name": "neo-tax-latest", "metadata": map[string]any{"version": l.version},
			"plugins": []any{map[string]any{"name": "neo-tax", "version": l.version, "source": defaultSource("main")}},
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
		{"v0 ok", repo(t, layout{version: "0.1.0"}), "v0.1.0", ""},
		{"not semver", repo(t, layout{version: "0.1.0"}), "v0.1", "semantic-version"},
		{"plugin version differs", repo(t, layout{version: "0.1.0"}), "v0.2.0", "version"},
		{"github source", repo(t, layout{version: "0.1.0", source: map[string]any{"source": "github", "repo": release.Repository}}), "v0.1.0", "git-subdir"},
		{"fork", repo(t, layout{version: "0.1.0", source: map[string]any{"source": "git-subdir", "url": "someone/fork", "path": "plugins/claude-code"}}), "v0.1.0", "git-subdir"},
		{"plugin renamed", repo(t, layout{version: "0.1.0", pluginName: "other"}), "v0.1.0", "plugin.json is"},
		{"v0 has latest channel", repo(t, layout{version: "0.1.0", latest: true}), "v0.1.0", "single channel"},
		{"v0 malformed latest", repo(t, layout{version: "0.1.0", latestBytes: "{"}), "v0.1.0", "channels/latest/marketplace.json"},
		{"v1 without latest", repo(t, layout{version: "1.0.0"}), "v1.0.0", "is missing"},
		{"v1 malformed latest", repo(t, layout{version: "1.0.0", latestBytes: "{"}), "v1.0.0", "unexpected end"},
		{"v1 ok", repo(t, layout{version: "1.0.0", latest: true}), "v1.0.0", ""},
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

// Pin points the stable marketplace at the published tag, keeping every
// other field.
// @spec: test-and-release#marketplace-source-is-this-repo
// @spec: test-and-release#single-channel-v0-1
func TestPin(t *testing.T) {
	root := repo(t, layout{version: "0.2.0"})
	if err := release.Pin(root, "v0.2.0"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, ".claude-plugin/marketplace.json"))
	var m struct {
		Owner    map[string]any `json:"owner"`
		Metadata struct{ Version string }
		Plugins  []struct {
			Version     string
			Description string
			Source      map[string]any
		}
	}
	json.Unmarshal(b, &m)
	p := m.Plugins[0]
	if m.Metadata.Version != "0.2.0" || p.Version != "0.2.0" || p.Source["ref"] != "v0.2.0" {
		t.Fatalf("not pinned: %s", b)
	}
	if p.Source["source"] != "git-subdir" || p.Source["path"] != "plugins/claude-code" || p.Description != "d" || m.Owner["name"] != "Neo.Tax" {
		t.Fatalf("fields lost: %s", b)
	}
	if err := release.Check(root, "v0.2.0"); err != nil {
		t.Fatal(err)
	}
	if err := release.Pin(root, "v0.2"); err == nil {
		t.Fatal("pinned a non-semver tag")
	}
}

// The committed manifests pass for the version plugin.json declares.
func TestRepositoryManifests(t *testing.T) {
	b, err := os.ReadFile("../../plugins/claude-code/.claude-plugin/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var p struct{ Version string }
	json.Unmarshal(b, &p)
	if err := release.Check("../..", "v"+p.Version); err != nil {
		t.Fatal(err)
	}
}
