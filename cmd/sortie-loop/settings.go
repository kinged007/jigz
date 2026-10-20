package main

import (
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/kinged007/sortie-loop/internal/edit"
	fleetconfig "github.com/kinged007/sortie-loop/internal/fleetconfig"
	"github.com/kinged007/sortie-loop/internal/rollup"
	"github.com/kinged007/sortie-loop/internal/store"
)

type pair struct{ Name, Value string }

type settingsView struct {
	pageData
	Access      fleetconfig.Access
	Versions    []pair
	Paths       []pair
	SampleDays  int
	BackupsKept int
	GitHub      fleetconfig.GitHub
	// GitHubInterval and GitHubAPI are the effective values of the github
	// section; Statuses has one line per project and is set only when
	// enrichment is on.
	GitHubInterval time.Duration
	GitHubAPI      string
	Statuses       []pair
}

func (a *app) settings(w http.ResponseWriter, r *http.Request) {
	v := settingsView{pageData: a.pageView(r, "settings", "Settings"), SampleDays: int(store.SampleRetention.Hours() / 24), BackupsKept: edit.KeepBackups}
	a.mu.Lock()
	v.Access, v.Paths = a.access, nil
	dir := a.stateDir
	a.mu.Unlock()
	v.GitHub = a.githubSettings()
	v.GitHubInterval, v.GitHubAPI = v.GitHub.PassInterval(), v.GitHub.BaseURL()
	if v.GitHub.Enrich {
		for _, p := range a.projects() {
			v.Statuses = append(v.Statuses, pair{p.Repo.Repo, a.githubStatus(p.Repo.Repo)})
		}
	}
	v.Versions = a.toolVersions()
	v.Paths = []pair{{"config", a.configPath}, {"state directory", dir},
		{"history database", filepath.Join(dir, store.FileName)}, {"backups", filepath.Join(dir, "backups")},
		{"session key", filepath.Join(dir, "session.key")}}
	execPage(w, "settings", v)
}

// toolVersions asks the binaries central drives for their versions, once.
func (a *app) toolVersions() []pair {
	a.verOnce.Do(func() {
		p := a.sup.Plan()
		a.versions = []pair{{"sortie-central", centralVersion()},
			{"sortie-loop", firstLine(p.LoopBinary(), "--dump-version")}}
		engine := "not found"
		if bin, err := p.SortieBinPath(); err == nil {
			engine = firstLine(bin, "--version")
		}
		a.versions = append(a.versions, pair{"engine", engine}, pair{"supported database schema", schemaList(rollup.SupportedSchemas)})
	})
	return a.versions
}

// centralVersion is the build ref with the short commit the binary was built from, when known.
func centralVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 7 {
			return version + " (" + s.Value[:7] + ")"
		}
	}
	return version
}

func schemaList(versions []int) string {
	parts := make([]string, len(versions))
	for i, v := range versions {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ", ")
}

func firstLine(bin string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		return "not available"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}
