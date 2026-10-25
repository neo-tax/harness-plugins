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
	errs := checkMarketplace(root, "marketplace", stable, version, "")

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
		errs = append(errs, checkMarketplace(root, "latest channel", latest, version, "main")...)
	}
	return errors.Join(errs...)
}

// checkMarketplace checks a marketplace's plugin sources; ref, when set, is
// the ref each source must name.
func checkMarketplace(root, label string, m manifest, version, ref string) []error {
	var errs []error
	if len(m.Plugins) == 0 {
		errs = append(errs, fmt.Errorf("%s lists no plugins", label))
	}
	for _, p := range m.Plugins {
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
// version, and each plugin source's ref. Every other field is kept.
//
// @spec: test-and-release#marketplace-source-is-this-repo
func Pin(root, tag string) error {
	version, _, err := parse(tag)
	if err != nil {
		return err
	}
	path := filepath.Join(root, stablePath)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	meta, _ := m["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		m["metadata"] = meta
	}
	meta["version"] = version
	plugins, _ := m["plugins"].([]any)
	for _, p := range plugins {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		pm["version"] = version
		if src, ok := pm["source"].(map[string]any); ok {
			src["ref"] = tag
		}
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}
