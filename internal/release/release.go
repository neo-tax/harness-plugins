// Package release bumps the plugin manifests to the next version, pinning
// the stable marketplace to its tag, and checks, before a tag is published,
// that they agree with the tag.
//
// Release procedure: one commit on a release branch off main bumps every
// plugins/*/.claude-plugin/plugin.json and pins
// .claude-plugin/marketplace.json to the new tag, and that commit is tagged.
// The release workflow checks, builds, publishes, and installs the release
// from the tag; only then is main fast-forwarded to the commit. Engineers
// read the marketplace from main, so until the fast-forward they stay on the
// previous release and are never pointed at a tag whose binaries are not
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

// Repository is this repository, the source of every released plugin. It is
// an HTTPS URL because Claude Code clones an owner/name shorthand over SSH,
// which fails on machines without a GitHub SSH key.
const Repository = "https://github.com/neo-tax/harness-plugins.git"

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
// must take its plugins from this repository, the stable marketplace must be
// pinned to the tag, and v0 publishes only the stable marketplace while v1
// and later also publish a latest channel that follows main.
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
	errs := checkMarketplace(root, "marketplace", stable, version, tag)

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

// checkMarketplace checks a marketplace's versions and plugin sources; ref is
// the ref each source must name.
func checkMarketplace(root, label string, m manifest, version, ref string) []error {
	var errs []error
	if len(m.Plugins) == 0 {
		errs = append(errs, fmt.Errorf("%s lists no plugins", label))
	}
	if m.Metadata.Version != version {
		errs = append(errs, fmt.Errorf("%s metadata version %q, want %q", label, m.Metadata.Version, version))
	}
	for _, p := range m.Plugins {
		if p.Version != version {
			errs = append(errs, fmt.Errorf("%s plugin %s version %q, want %q", label, p.Name, p.Version, version))
		}
		s := p.Source
		if s.Source != "git-subdir" || s.URL != Repository || s.Path == "" {
			errs = append(errs, fmt.Errorf("%s plugin %s source must be git-subdir of %s", label, p.Name, Repository))
			continue
		}
		if s.Ref != ref {
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

// Bump moves every released manifest to the next major, minor, or patch
// version and returns the new tag: each plugin.json the stable marketplace
// lists, when it exists the latest channel's metadata and plugin versions,
// and, through Pin, the stable marketplace itself. It edits only version and
// ref values, keeping each file's layout, refuses plugins whose versions
// already disagree, and fails if the result does not pass Check.
//
// latest is the newest release tag, or empty when there is none. The bump
// starts from it when it is newer than the manifests, which happens when a
// release was published but never rolled out: main still carries the version
// before it, and bumping from that would land on the abandoned tag again.
//
// @spec: test-and-release#release-bumps-version
func Bump(root, kind, latest string) (string, error) {
	stable, err := read(filepath.Join(root, stablePath))
	if err != nil {
		return "", err
	}
	var paths []string
	current := ""
	for _, p := range stable.Plugins {
		path := filepath.Join(root, p.Source.Path, ".claude-plugin", "plugin.json")
		pm, err := read(path)
		if err != nil {
			return "", err
		}
		if current != "" && pm.Version != current {
			return "", fmt.Errorf("plugin versions disagree (%q and %q); reconcile them first", current, pm.Version)
		}
		current = pm.Version
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("%s lists no plugins", stablePath)
	}
	from := "v" + current
	if !semver.MatchString(from) {
		return "", fmt.Errorf("plugin version %q is not a stable version like 1.2.3", current)
	}
	if latest != "" {
		if !semver.MatchString(latest) {
			return "", fmt.Errorf("latest release %q is not a semantic-version tag like v1.2.3", latest)
		}
		if older(from, latest) {
			from = latest
		}
	}
	m := semver.FindStringSubmatch(from)
	var n [3]int
	for i := range n {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	switch kind {
	case "major":
		n = [3]int{n[0] + 1, 0, 0}
	case "minor":
		n = [3]int{n[0], n[1] + 1, 0}
	case "patch":
		n[2]++
	default:
		return "", fmt.Errorf("release type %q is not major, minor, or patch", kind)
	}
	version := fmt.Sprintf("%d.%d.%d", n[0], n[1], n[2])
	tag := "v" + version

	if _, err := os.Stat(filepath.Join(root, latestPath)); err == nil {
		paths = append(paths, filepath.Join(root, latestPath))
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(path, versionField.ReplaceAll(b, []byte(`${1}"`+version+`"`)), 0o644); err != nil {
			return "", err
		}
	}
	if err := Pin(root, tag); err != nil {
		return "", err
	}
	return tag, Check(root, tag)
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
