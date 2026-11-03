// Package gen renders the engine's workflow documents from the resolved
// configuration. One loop is one engine process and one document.
package gen

import (
	"errors"
	"sort"
	"strings"
)

// Profile is the compiled contract of one engine release: what the
// generator may emit and what the engine accepts. Releases newer than
// the newest profile use the newest one; a release older than the
// oldest starts no loop.
type Profile struct {
	Repo    string
	Version string
	// Trackers and Agents are the kinds the engine registers. A kind
	// outside these lists takes the loop offline with the existing
	// reason text.
	Trackers []string
	Agents   []string
	// Schema is the database schema version the release creates.
	Schema int
}

// profiles is the single list of supported engine releases. The engine
// pin, the rollup schema table and the capability probe all agree with
// it; a new release is added here and nowhere else.
var profiles = []Profile{
	{
		Repo:     "kinged007/sortie",
		Version:  "v1.28.1",
		Trackers: []string{"file", "gitea", "github", "github-pr", "gitlab", "jira", "linear"},
		Agents:   []string{"agent-client-protocol", "claude-code", "codex", "copilot-cli", "mock", "opencode", "pi"},
		Schema:   38,
	},
	{
		Repo:     "sortie-ai/sortie",
		Version:  "v1.29.0",
		Trackers: []string{"file", "gitea", "github", "gitlab", "jira", "linear"},
		Agents:   []string{"agent-client-protocol", "claude-code", "codex", "copilot-cli", "mock", "opencode"},
		Schema:   43,
	},
}

// DefaultRepo and DefaultVersion are the engine target a config with no
// engine section resolves to.
const (
	DefaultRepo    = "kinged007/sortie"
	DefaultVersion = "v1.28.1"
)

// MinVersion is the oldest release that starts a loop.
const MinVersion = "v1.27.0"

// Profiles returns the supported releases, oldest first.
func Profiles() []Profile {
	out := make([]Profile, len(profiles))
	copy(out, profiles)
	sort.Slice(out, func(i, j int) bool { return out[i].Repo+out[i].Version < out[j].Repo+out[j].Version })
	return out
}

// ProfileFor resolves the engine target to a profile. A release newer
// than the newest entry for its repository uses that repository's newest
// profile and reports that it has no contract tests; a release older
// than MinVersion is an error, since no loop starts on it. An unknown
// repository with a version newer than every known release uses the
// newest profile overall; otherwise it is unsupported.
func ProfileFor(repo, version string) (Profile, string, error) {
	if repo == "" {
		repo = DefaultRepo
	}
	if version == "" {
		version = DefaultVersion
	}
	for _, p := range profiles {
		if p.Repo == repo && p.Version == version {
			return p, profileName(p), nil
		}
	}
	if compareVersion(version, MinVersion) < 0 {
		return Profile{}, "", errors.New("engine " + repo + "@" + version + " is older than " + MinVersion + "; no loop starts on it")
	}
	if newest, ok := newestForRepo(repo); ok {
		if compareVersion(version, newest.Version) > 0 {
			// Newer than the newest entry for this repository: use it.
			// The caller warns that the release has no contract tests.
			return newest, profileName(newest), nil
		}
		return Profile{}, "", errors.New("unsupported engine " + repo + "@" + version)
	}
	newest := profiles[len(profiles)-1]
	for _, p := range profiles {
		if compareVersion(p.Version, newest.Version) > 0 {
			newest = p
		}
	}
	if compareVersion(version, newest.Version) >= 0 {
		return newest, profileName(newest), nil
	}
	return Profile{}, "", errors.New("unsupported engine " + repo + "@" + version)
}

// newestForRepo returns the highest-versioned profile for a repository.
func newestForRepo(repo string) (Profile, bool) {
	var out Profile
	var ok bool
	for _, p := range profiles {
		if p.Repo != repo {
			continue
		}
		if !ok || compareVersion(p.Version, out.Version) > 0 {
			out, ok = p, true
		}
	}
	return out, ok
}

// profileName is the short name tests and status pages use for a release.
func profileName(p Profile) string {
	switch p.Repo {
	case "sortie-ai/sortie":
		return "upstream"
	default:
		return "fork"
	}
}

// SupportsTracker and SupportsAgent report whether the release
// registers the kind. They are what takes a loop offline when the
// config asks for something the engine does not have.
func (p Profile) SupportsTracker(kind string) bool { return contains(p.Trackers, kind) }
func (p Profile) SupportsAgent(kind string) bool   { return contains(p.Agents, kind) }

// EngineVersion is the string the generated marker carries. It is the
// repository and release the document targets.
func (p Profile) EngineVersion() string { return p.Repo + "@" + p.Version }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// compareVersion orders two vX.Y.Z strings by their numeric parts. A
// missing part counts as zero.
func compareVersion(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] - pb[i]
		}
	}
	return 0
}

func versionParts(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	for i, part := range strings.SplitN(v, ".", 3) {
		n := 0
		for _, c := range part {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		out[i] = n
	}
	return out
}
