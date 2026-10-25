// Package release checks, before a tag is published, that the plugin
// manifests agree with the tag, and pins the stable marketplace to the tag
// once its release is published.
//
// Release procedure: bump plugins/*/.claude-plugin/plugin.json to the new
// version on main, push the tag, and the release workflow checks, builds,
// publishes, and only then pins .claude-plugin/marketplace.json to the tag on
// main. Until that last step the marketplace keeps pointing at the previous
// release, so no engineer is ever pointed at a tag whose binaries are not
// attached yet.
package release

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// Repository is this repository, the source of every released plugin.
const Repository = "neo-tax/harness-plugins"

var semver = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

const (
	stablePath = ".claude-plugin/marketplace.json"
	latestPath = "channels/latest/marketplace.json"
)

type manifest struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Metadata struct {
		Version string `json:"version"`
	} `json:"metadata"`
	Plugins []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Source  struct {
			Source string `json:"source"`
			URL    string `json:"url"`
			Path   string `json:"path"`
			Ref    string `json:"ref"`
		} `json:"source"`
	} `json:"plugins"`
}

func read(path string) (manifest, error) {
	var m manifest
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

func parse(tag string) (version string, major int, err error) {
	m := semver.FindStringSubmatch(tag)
	if m == nil {
		return "", 0, fmt.Errorf("%q is not a semantic-version tag like v1.2.3", tag)
	}
	major, _ = strconv.Atoi(m[1])
	return tag[1:], major, nil
}

// Check returns an error naming every manifest that disagrees with tag:
// each plugin's plugin.json must carry the tag's version, each marketplace
// must take its plugins from this repository, and v0 publishes only the
// stable marketplace while v1 and later also publish a latest channel that
// follows main. The stable marketplace's ref is not checked here; Pin sets it
// after the release is published.
//
// @spec: test-and-release#manifest-version-matches-tag
// @spec: test-and-release#single-channel-v0-1
// @spec: test-and-release#stable-and-latest-channels-v1-0
func Check(root, tag string) error {
	version, major, err := parse(tag)
	if err != nil {
		return err
	}
	stable, err := read(filepath.Join(root, stablePath))
	if err != nil {
		return err
	}
	errs := checkMarketplace(root, "marketplace", stable, version, "", false)

	latest, err := read(filepath.Join(root, latestPath))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if major >= 1 {
			errs = append(errs, fmt.Errorf("v1 and later publish a latest channel; %s is missing", latestPath))
		}
	case err != nil:
		errs = append(errs, err)
	case major == 0:
		errs = append(errs, fmt.Errorf("v0 publishes a single channel; remove %s", latestPath))
	default:
		errs = append(errs, checkMarketplace(root, "latest channel", latest, version, "main", true)...)
	}
	return errors.Join(errs...)
}

// checkMarketplace checks a marketplace's plugin sources; ref, when set, is
// the ref each source must name, and versions says whether the marketplace's
// own versions must already match (the stable one's are set by Pin).
func checkMarketplace(root, label string, m manifest, version, ref string, versions bool) []error {
	var errs []error
	if len(m.Plugins) == 0 {
		errs = append(errs, fmt.Errorf("%s lists no plugins", label))
	}
	if versions && m.Metadata.Version != version {
		errs = append(errs, fmt.Errorf("%s metadata version %q, want %q", label, m.Metadata.Version, version))
	}
	for _, p := range m.Plugins {
		if versions && p.Version != version {
			errs = append(errs, fmt.Errorf("%s plugin %s version %q, want %q", label, p.Name, p.Version, version))
		}
		s := p.Source
		if s.Source != "git-subdir" || s.URL != Repository || s.Path == "" {
			errs = append(errs, fmt.Errorf("%s plugin %s source must be git-subdir of %s", label, p.Name, Repository))
			continue
		}
		if ref != "" && s.Ref != ref {
			errs = append(errs, fmt.Errorf("%s plugin %s source ref %q, want %q", label, p.Name, s.Ref, ref))
		}
		pm, err := read(filepath.Join(root, s.Path, ".claude-plugin", "plugin.json"))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s plugin %s: %w", label, p.Name, err))
			continue
		}
		if pm.Name != p.Name || pm.Version != version {
			errs = append(errs, fmt.Errorf("%s plugin.json is %s version %q, want %s version %q", s.Path, pm.Name, pm.Version, p.Name, version))
		}
	}
	return errs
}

// Pin points the stable marketplace at tag: its version, each plugin's
// version, and each plugin source's ref. It edits only those values, keeping
// the file's layout, and refuses a tag older than the one already pinned.
//
// @spec: test-and-release#marketplace-source-is-this-repo
func Pin(root, tag string) error {
	version, _, err := parse(tag)
	if err != nil {
		return err
	}
	path := filepath.Join(root, stablePath)
	current, err := read(path)
	if err != nil {
		return err
	}
	for _, p := range current.Plugins {
		if p.Source.Ref != "" && semver.MatchString(p.Source.Ref) && older(tag, p.Source.Ref) {
			return fmt.Errorf("%s is older than the pinned %s; refusing to move the marketplace back", tag, p.Source.Ref)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	b = versionField.ReplaceAll(b, []byte(`${1}"`+version+`"`))
	b = refField.ReplaceAll(b, []byte(`${1}"`+tag+`"`))
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	// Confirm the targeted edit landed where it should.
	pinned, err := read(path)
	if err != nil {
		return err
	}
	if pinned.Metadata.Version != version {
		return fmt.Errorf("%s: metadata version not pinned", path)
	}
	for _, p := range pinned.Plugins {
		if p.Version != version || p.Source.Ref != tag {
			return fmt.Errorf("%s: plugin %s not pinned", path, p.Name)
		}
	}
	return nil
}

var (
	versionField = regexp.MustCompile(`("version"\s*:\s*)"[^"]*"`)
	refField     = regexp.MustCompile(`("ref"\s*:\s*)"[^"]*"`)
)

// older reports whether semver tag a precedes b.
func older(a, b string) bool {
	pa, pb := semver.FindStringSubmatch(a), semver.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return false
}
