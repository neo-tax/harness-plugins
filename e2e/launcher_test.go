package e2e

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const releaseURL = "https://github.com/$repo/releases/download"

// fakeRelease serves one release's asset and checksums.txt the way GitHub's
// release download URLs do, and records what was requested.
type fakeRelease struct {
	mu        sync.Mutex
	requested []string
	asset     []byte
	sums      func(name string, sum string) string // checksums.txt body
	missing   bool                                 // 404 the asset
}

func (f *fakeRelease) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requested = append(f.requested, r.URL.Path)
	f.mu.Unlock()
	dir, name := filepath.Split(r.URL.Path)
	if dir != "/v9.9.9/" {
		http.NotFound(w, r)
		return
	}
	if name == "checksums.txt" {
		fmt.Fprint(w, f.sums(assetName(), fmt.Sprintf("%x", sha256.Sum256(f.asset))))
		return
	}
	if name != assetName() || f.missing {
		http.NotFound(w, r)
		return
	}
	w.Write(f.asset)
}

func (f *fakeRelease) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requested)
}

// assetName is the name the release workflow gives this platform's binary.
func assetName() string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	return "neo-tax-agent_" + runtime.GOOS + "_" + runtime.GOARCH + ext
}

// installLauncher copies the plugin's launcher into a plugin root whose
// manifest says version 9.9.9, with the release URL pointed at srv. That one
// URL is the only change to the shipped script.
func installLauncher(t *testing.T, srv string) string {
	t.Helper()
	b, err := os.ReadFile(pluginDir + "/bin/neo-tax-agent")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), releaseURL); n != 1 {
		t.Fatalf("launcher names the release URL %d times, want 1", n)
	}
	root := t.TempDir()
	for _, d := range []string{"bin", ".claude-plugin"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.Replace(string(b), releaseURL, srv, 1)
	if err := os.WriteFile(filepath.Join(root, "bin/neo-tax-agent"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// The shipped manifest with only its version changed, since the launcher
	// reads the version line by line.
	manifest, err := os.ReadFile(pluginDir + "/.claude-plugin/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	version := regexp.MustCompile(`("version":\s*")[^"]*"`)
	if n := len(version.FindAll(manifest, -1)); n != 1 {
		t.Fatalf("plugin.json has %d version fields, want 1", n)
	}
	manifest = version.ReplaceAll(manifest, []byte(`${1}9.9.9"`))
	if err := os.WriteFile(filepath.Join(root, ".claude-plugin/plugin.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "bin/neo-tax-agent")
}

func runLauncher(t *testing.T, launcher, data string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := exec.Command(launcher, "hook", "Stop")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "NEO_TAX_DATA_DIR=" + data}
	cmd.Stdin = strings.NewReader("{}")
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	return out.String(), errOut.String(), err
}

// The launcher a fleet runs: no NEO_TAX_AGENT_BIN, so it downloads the binary
// for the plugin's version from the release, checks it against the release's
// checksums.txt, caches it, and execs it with the same arguments.
// @spec: test-and-release#launcher-download-verified
func TestLauncherDownloadsVerifiesAndExecs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher is a POSIX shell script")
	}
	cases := []struct {
		name string
		sums func(name, sum string) string
	}{
		{"text", func(n, s string) string { return "0000  other_file\n" + s + "  " + n + "\n" }},
		{"binary", func(n, s string) string { return s + " *" + n + "\n" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rel := &fakeRelease{asset: []byte("#!/bin/sh\necho \"ran $*\"\n"), sums: c.sums}
			srv := httptest.NewServer(rel)
			defer srv.Close()
			launcher := installLauncher(t, srv.URL)
			data := t.TempDir()

			out, errOut, err := runLauncher(t, launcher, data)
			if err != nil || strings.TrimSpace(out) != "ran hook Stop" {
				t.Fatalf("out %q stderr %q err %v", out, errOut, err)
			}
			want := []string{"/v9.9.9/" + assetName(), "/v9.9.9/checksums.txt"}
			if strings.Join(rel.requested, " ") != strings.Join(want, " ") {
				t.Fatalf("requested %v, want %v", rel.requested, want)
			}
			if _, err := os.Stat(filepath.Join(data, "bin/neo-tax-agent-v9.9.9")); err != nil {
				t.Fatal(err)
			}

			// The cached binary is reused without another download.
			if out, _, err := runLauncher(t, launcher, data); err != nil || strings.TrimSpace(out) != "ran hook Stop" || rel.count() != 2 {
				t.Fatalf("second run: out %q err %v requests %d", out, err, rel.count())
			}
		})
	}
}

// A binary that does not match checksums.txt, or a release missing the asset,
// is never cached or run, and the hook still exits 0 so the harness is never
// blocked.
// @spec: test-and-release#launcher-download-verified
func TestLauncherRejectsUnverifiedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher is a POSIX shell script")
	}
	cases := []struct {
		name    string
		rel     *fakeRelease
		message string
	}{
		{"checksum mismatch", &fakeRelease{
			asset: []byte("#!/bin/sh\necho ran\n"),
			sums:  func(n, _ string) string { return strings.Repeat("ab", 32) + "  " + n + "\n" },
		}, "checksum mismatch"},
		{"not listed", &fakeRelease{
			asset: []byte("#!/bin/sh\necho ran\n"),
			sums:  func(_, s string) string { return s + "  neo-tax-agent_plan9_386\n" },
		}, "checksum mismatch"},
		{"asset missing", &fakeRelease{
			asset:   []byte("#!/bin/sh\necho ran\n"),
			sums:    func(n, s string) string { return s + "  " + n + "\n" },
			missing: true,
		}, "could not download"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(c.rel)
			defer srv.Close()
			data := t.TempDir()
			out, errOut, err := runLauncher(t, installLauncher(t, srv.URL), data)
			if err != nil || out != "" || !strings.Contains(errOut, c.message) {
				t.Fatalf("out %q stderr %q err %v", out, errOut, err)
			}
			entries, _ := os.ReadDir(filepath.Join(data, "bin"))
			if len(entries) != 0 {
				t.Fatalf("left %d files in the cache", len(entries))
			}
		})
	}
}
