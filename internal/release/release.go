// Package release checks, before a tag is published, that every plugin and
// marketplace manifest agrees with the tag.
package release

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// Repository is this repository, the source of every released plugin.
const Repository = "neo-tax/harness-plugins"

var semver = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

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
	return m, json.Unmarshal(b, &m)
}

// Check returns an error naming every manifest that disagrees with tag.
//
// @spec: test-and-release#manifest-version-matches-tag
// @spec: test-and-release#marketplace-source-is-this-repo
// @spec: test-and-release#single-channel-v0-1
// @spec: test-and-release#stable-and-latest-channels-v1-0
func Check(root, tag string) error {
	m := semver.FindStringSubmatch(tag)
	if m == nil {
		return fmt.Errorf("%q is not a semantic-version tag like v1.2.3", tag)
	}
	version := tag[1:]
	major, _ := strconv.Atoi(m[1])
	var errs []error

	stable, err := read(filepath.Join(root, ".claude-plugin", "marketplace.json"))
	if err != nil {
		return err
	}
	errs = append(errs, checkMarketplace(root, "marketplace", stable, version, tag)...)

	latestPath := filepath.Join(root, "channels", "latest", "marketplace.json")
	latest, err := read(latestPath)
	switch {
	case major == 0 && err == nil:
		errs = append(errs, errors.New("v0 publishes a single channel; remove channels/latest"))
	case major >= 1 && err != nil:
		errs = append(errs, fmt.Errorf("v1 and later publish a latest channel; %s is missing", latestPath))
	case major >= 1:
		errs = append(errs, checkMarketplace(root, "latest channel", latest, version, "main")...)
	}
	return errors.Join(errs...)
}

func checkMarketplace(root, label string, m manifest, version, ref string) []error {
	var errs []error
	if m.Metadata.Version != version {
		errs = append(errs, fmt.Errorf("%s metadata version %q, want %q", label, m.Metadata.Version, version))
	}
	if len(m.Plugins) == 0 {
		errs = append(errs, fmt.Errorf("%s lists no plugins", label))
	}
	for _, p := range m.Plugins {
		if p.Version != version {
			errs = append(errs, fmt.Errorf("%s plugin %s version %q, want %q", label, p.Name, p.Version, version))
		}
		s := p.Source
		if s.Source != "git-subdir" || s.URL != Repository {
			errs = append(errs, fmt.Errorf("%s plugin %s source must be git-subdir of %s", label, p.Name, Repository))
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
			errs = append(errs, fmt.Errorf("%s plugin.json is %s %q, want %s %q", s.Path, pm.Name, pm.Version, p.Name, version))
		}
	}
	return errs
}
