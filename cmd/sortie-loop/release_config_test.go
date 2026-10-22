package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/sortie-loop/internal/engine"
	"gopkg.in/yaml.v3"
)

// repoFile reads a file from the repository root, which is two levels up
// from this package.
func repoFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

// goreleaser mirrors the parts of .goreleaser.yaml that install.sh
// depends on.
type goreleaser struct {
	ProjectName string `yaml:"project_name"`
	Builds      []struct {
		Binary string   `yaml:"binary"`
		Goos   []string `yaml:"goos"`
		Goarch []string `yaml:"goarch"`
	} `yaml:"builds"`
	Archives []struct {
		NameTemplate string `yaml:"name_template"`
	} `yaml:"archives"`
	Checksum struct {
		NameTemplate string `yaml:"name_template"`
		Algorithm    string `yaml:"algorithm"`
	} `yaml:"checksum"`
	Release struct {
		GitHub struct {
			Owner string `yaml:"owner"`
			Name  string `yaml:"name"`
		} `yaml:"github"`
	} `yaml:"release"`
}

// install.sh builds an asset name from the version and the target; the
// archive template has to produce exactly that. The two files are edited
// separately and a mismatch makes every install fail with a 404, so the
// contract is asserted here rather than discovered in production.
func TestReleaseAssetNameMatchesInstaller(t *testing.T) {
	var g goreleaser
	if err := yaml.Unmarshal(repoFile(t, ".goreleaser.yaml"), &g); err != nil {
		t.Fatalf("parse .goreleaser.yaml: %v", err)
	}
	if len(g.Archives) != 1 {
		t.Fatalf("expected one archive config, got %d", len(g.Archives))
	}
	want := "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
	if got := g.Archives[0].NameTemplate; got != want {
		t.Errorf("archive name_template = %q, want %q", got, want)
	}
	if g.ProjectName != "sortie-loop" {
		t.Errorf("project_name = %q, want sortie-loop (install.sh hardcodes it)", g.ProjectName)
	}
	// install.sh builds the name from the version and target; the archive
	// template has to produce the same string.
	sh := string(repoFile(t, "install.sh"))
	if !strings.Contains(sh, `ASSET="sortie-loop_${VER}_${OS_ARCH}.tar.gz"`) {
		t.Error("install.sh no longer builds the asset name .goreleaser.yaml produces")
	}
	if g.Checksum.NameTemplate != "checksums.txt" || g.Checksum.Algorithm != "sha256" {
		t.Errorf("checksum = %q/%q, want checksums.txt/sha256 (install.sh reads both)",
			g.Checksum.NameTemplate, g.Checksum.Algorithm)
	}
}

// Every target GoReleaser builds must be a target the engine can also be
// fetched for, or the wrapper installs on a platform its own loops cannot
// run.
func TestReleaseTargetsAreSupported(t *testing.T) {
	var g goreleaser
	if err := yaml.Unmarshal(repoFile(t, ".goreleaser.yaml"), &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Builds) == 0 {
		t.Fatal("no build config")
	}
	for _, goos := range g.Builds[0].Goos {
		for _, goarch := range g.Builds[0].Goarch {
			if _, err := engine.AssetName(goos, goarch, "0.0.0"); err != nil {
				t.Errorf("releases %s/%s but the engine resolver rejects it: %v", goos, goarch, err)
			}
		}
	}
}

// The engine fork inherited a .goreleaser.yaml whose publish targets
// pointed at the upstream project, so a release built from it would try
// to write to sortie-ai. Nothing here may name a different owner.
func TestReleasePublishesOnlyToThisRepository(t *testing.T) {
	raw := string(repoFile(t, ".goreleaser.yaml"))
	if strings.Contains(raw, "sortie-ai") {
		t.Error(".goreleaser.yaml references sortie-ai; a release would publish upstream")
	}
	var g goreleaser
	if err := yaml.Unmarshal([]byte(raw), &g); err != nil {
		t.Fatal(err)
	}
	if g.Release.GitHub.Owner != "kinged007" || g.Release.GitHub.Name != "sortie-loop" {
		t.Errorf("releases publish to %s/%s, want kinged007/sortie-loop",
			g.Release.GitHub.Owner, g.Release.GitHub.Name)
	}
	for _, target := range []string{"homebrew_casks", "homebrew_taps", "nfpms", "scoop", "chocolatey", "aur"} {
		if strings.Contains(raw, target+":") {
			t.Errorf(".goreleaser.yaml declares %s, which publishes outside this repository", target)
		}
	}
}
