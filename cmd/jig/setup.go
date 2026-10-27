package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kinged007/jigz/internal/config"
)

// runSetup creates .jigz/config.yaml if missing, syncs labels via gh,
// and ensures .gitignore covers the loop's local state. dir is the repo
// root, empty for the current directory; repo is the --repo value, a repo
// slug and never a directory. asJSON enables non-interactive JSON output.
func runSetup(dir, repo string, asJSON bool) {
	if asJSON {
		runSetupJSON(dir, repo)
		return
	}
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		fatal(err)
	}
	config.MigrateRepoDir(abs)
	// Dogfooding is supported: setup runs in this checkout like any other
	// repo. Everything it writes lands under .jigz/, which
	// ensureGitignore keeps untracked.
	if _, err := exec.LookPath("gh"); err != nil {
		fatal(fmt.Errorf("gh CLI not found"))
	}
	cfgPath := filepath.Join(abs, ".jigz", "config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		if repo == "" {
			repo = guessRepo(abs)
		}
		body := configTemplateBody(repo)
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
			fatal(err)
		}
		if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
			fatal(err)
		}
		fmt.Println("created", cfgPath)
	}
	warnLegacy(syncWorkflows(abs))
	warnStalePrompts(abs)
	warnStaleWorkflows(abs)
	fmt.Println("workflows ready in", filepath.Join(abs, ".jigz", "workflows"))
	syncPMAgent(abs, askBeforeOverwrite)
	fmt.Println("project manager prompt ready in", filepath.Join(abs, ".jigz", "pm-agent.md"))
	// The engine is resolved here so a working setup leaves a bare
	// `sortie` on PATH, for go install users and for anyone who only
	// ever runs setup. A missing engine is a warning: config and labels
	// do not need it. The order is resolveSortieBin's, so a JIG_ENGINE_BIN the
	// user set is used as-is rather than downloading a second engine.
	if _, err := resolveSortieBin(); err != nil {
		fmt.Println("warning: engine not installed:", err)
	}
	repo = configRepo(abs, repo)
	if err := os.MkdirAll(filepath.Join(abs, ".jigz", "data"), 0o755); err != nil {
		fatal(err)
	}
	ensureGitignore(abs)
	// Labels come from the config file so custom loops can add their own:
	// list entries under `labels:`, re-run setup, and they are created.
	// Sync is additive — labels absent from the list are left alone.
	// An existing label whose colour or description differs is a hand
	// edit, so it is confirmed before being overwritten, the same as the
	// project-manager prompt.
	cfg, err := config.Load(abs)
	if err != nil {
		fatal(err)
	}
	warnMissingLabels(cfg.Labels)
	for _, l := range cfg.Labels {
		cur, err := labelOn(repo, l.Name)
		if err != nil {
			fatal(fmt.Errorf("read label %s: %w", l.Name, err))
		}
		if cur != nil {
			if cur.color == l.Color && cur.description == l.Description {
				continue
			}
			if !confirmOverwrite(fmt.Sprintf("label %s (colour %s, description %q)", l.Name, cur.color, cur.description)) {
				continue
			}
		}
		create := exec.Command("gh", "label", "create", l.Name, "--repo", repo,
			"--color", l.Color, "--description", l.Description)
		if _, err := create.CombinedOutput(); err != nil {
			edit := exec.Command("gh", "label", "edit", l.Name, "--repo", repo,
				"--color", l.Color, "--description", l.Description)
			if out, err := edit.CombinedOutput(); err != nil {
				// TrimSpace: gh's output ends in a newline, and
				// interpolating it raw splits one error across two
				// lines, leaving the last line a bare fragment that
				// says nothing on its own.
				fatal(fmt.Errorf("label %s: %s: %w", l.Name, strings.TrimSpace(string(out)), err))
			}
		}
	}
	fmt.Println("labels synced to", repo)
}

// setup types for --json output.

type setupCreated struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type setupSkipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type setupLabelResult struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	Error  string `json:"error,omitempty"`
}

type setupEngineInfo struct {
	Resolved bool    `json:"resolved"`
	Path     *string `json:"path,omitempty"`
	Error    *string `json:"error,omitempty"`
}

type setupResult struct {
	Created      []setupCreated     `json:"created"`
	Skipped      []setupSkipped     `json:"skipped"`
	SyncedLabels []setupLabelResult `json:"synced_labels"`
	Warnings     []string           `json:"warnings"`
	Errors       []string           `json:"errors"`
	Engine       setupEngineInfo    `json:"engine"`
	Error        *string            `json:"error,omitempty"`
}

// configTemplateBody builds the first .jigz/config.yaml `jig setup`
// writes. It carries the keys the loader accepts today: the repository
// section plus commented examples; the fleet and work sections the owner
// adds after setup are documented in the README.
func configTemplateBody(repo string) string {
	body := "# Repo id (owner/name). Empty = detect from git remote at runtime.\n" +
		"repo: " + repo + "\n" +
		"# Token for the tracker. Empty = GITHUB_TOKEN/GH_TOKEN env.\n" +
		"token: \"\"\n" +
		"# Per-loop query-filter overrides, keyed by WORKFLOW.*.md stem.\n" +
		"# New loops start on the default filter; narrow them here, e.g.:\n" +
		"#filters:\n" +
		"#  triage: \"label:agent:triage -label:needs-human\"\n" +
		"# Workspace cleanup: runs command every interval. max_age_days must be\n" +
		"# less than workspace.retention_days in every workflow. dry_run passes\n" +
		"# JIG_CLEANUP_DRY_RUN=1; timeout defaults to 10m.\n" +
		"#cleanup:\n" +
		"#  enabled: true\n" +
		"#  interval: 6h\n" +
		"#  command: ./scripts/cleanup.sh\n" +
		"#  max_age_days: 14\n" +
		"#  dry_run: false\n" +
		"#  timeout: 10m\n" +
		"# Idle sleep: stop the engine children after idle_timeout with no running\n" +
		"# or retrying work, start them again after wake_interval or on\n" +
		"# `jig wake`. idle_timeout should exceed the workflows' polling\n" +
		"# interval. New issues wait while sleeping.\n" +
		"#sleep:\n" +
		"#  enabled: true\n" +
		"#  idle_timeout: 15m\n" +
		"#  wake_interval: 1h\n"
	labelsBody := ""
	for _, l := range config.DefaultLabels {
		labelsBody += fmt.Sprintf("- {name: %q, color: %q, description: %q}\n", l.Name, l.Color, l.Description)
	}
	return body + "# GitHub labels setup ensures exist (create-or-edit; others left alone).\n" +
		"# Add a custom loop's labels here, then re-run setup.\n" +
		"labels:\n" + labelsBody
}

func runSetupJSON(dir, repo string) {
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		printSetupJSONAndFatal(&setupResult{
			Created: []setupCreated{}, Skipped: []setupSkipped{},
			SyncedLabels: []setupLabelResult{}, Warnings: []string{}, Errors: []string{},
			Engine: setupEngineInfo{},
		}, err)
		return
	}
	config.MigrateRepoDir(abs)
	res := &setupResult{
		Created: []setupCreated{}, Skipped: []setupSkipped{},
		SyncedLabels: []setupLabelResult{}, Warnings: []string{}, Errors: []string{},
		Engine: setupEngineInfo{},
	}
	if _, err := exec.LookPath("gh"); err != nil {
		printSetupJSONAndFatal(res, fmt.Errorf("gh CLI not found"))
		return
	}
	cfgPath := filepath.Join(abs, ".jigz", "config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		if repo == "" {
			repo = guessRepo(abs)
		}
		body := configTemplateBody(repo)
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
			printSetupJSONAndFatal(res, err)
			return
		}
		if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
			printSetupJSONAndFatal(res, err)
			return
		}
		res.Created = append(res.Created, setupCreated{Path: cfgPath, Kind: "config"})
	} else {
		res.Skipped = append(res.Skipped, setupSkipped{Path: cfgPath, Reason: "exists"})
	}
	// Workflows and prompts.
	legacy := syncWorkflowsJSON(abs, res)
	if len(legacy) > 0 {
		msg := fmt.Sprintf("warning: legacy workflow files found in .jigz/workflows/ (%s).", strings.Join(legacy, ", ")) +
			" WORKFLOW.issues.md and WORKFLOW.prs.md are skipped until the legacy files are deleted. Delete them, then rerun `jig setup`."
		res.Warnings = append(res.Warnings, msg)
	}
	// Stale prompts.
	for _, name := range []string{"plan.md", "build.md"} {
		cur, err := os.ReadFile(filepath.Join(abs, ".jigz", "workflows", "prompts", name))
		if err != nil {
			continue
		}
		want, err := embeddedWorkflows.ReadFile("workflows/prompts/" + name)
		if err == nil && string(cur) != string(want) {
			msg := fmt.Sprintf("note: .jigz/workflows/prompts/%s differs from the shipped copy; diff it or delete it and rerun setup to take the shipped version.", name)
			res.Warnings = append(res.Warnings, msg)
		}
	}
	for _, name := range consolidatedWorkflows {
		cur, err := os.ReadFile(filepath.Join(abs, ".jigz", "workflows", name))
		if err != nil {
			continue
		}
		want, err := embeddedWorkflows.ReadFile("workflows/" + name)
		if err == nil && string(cur) != string(want) {
			msg := fmt.Sprintf("note: .jigz/workflows/%s differs from the shipped copy; diff it or delete it and rerun setup to take the shipped version.", name)
			res.Warnings = append(res.Warnings, msg)
		}
	}
	// pm-agent.
	syncPMAgentJSON(abs, res)
	// Engine.
	if p, err := resolveSortieBin(); err != nil {
		msg := err.Error()
		res.Engine = setupEngineInfo{Resolved: false, Error: &msg}
		res.Warnings = append(res.Warnings, fmt.Sprintf("engine not installed: %s", msg))
	} else {
		res.Engine = setupEngineInfo{Resolved: true, Path: &p}
	}
	repo = configRepo(abs, repo)
	// Gitignore.
	if err := os.MkdirAll(filepath.Join(abs, ".jigz", "data"), 0o755); err != nil {
		printSetupJSONAndFatal(res, err)
		return
	}
	ensureGitignoreJSON(abs, res)
	// Config.
	cfg, err := config.Load(abs)
	if err != nil {
		printSetupJSONAndFatal(res, err)
		return
	}
	// Missing labels warning.
	have := map[string]bool{}
	for _, l := range cfg.Labels {
		have[l.Name] = true
	}
	for _, l := range config.DefaultLabels {
		if !have[l.Name] {
			msg := fmt.Sprintf("note: label %q is not in the labels list of .jigz/config.yaml, so setup does not create it.", l.Name)
			res.Warnings = append(res.Warnings, msg)
		}
	}
	// Labels.
	for _, l := range cfg.Labels {
		cur, err := labelOn(repo, l.Name)
		if err != nil {
			// Fatal read error (e.g. bad token, missing repo, permissions).
			msg := fmt.Sprintf("read label %s: %v", l.Name, err)
			res.Errors = append(res.Errors, msg)
			res.SyncedLabels = append(res.SyncedLabels, setupLabelResult{Name: l.Name, Action: "skipped", Error: msg})
			printSetupJSONAndFatal(res, fmt.Errorf("%s", msg))
			return
		}
		if cur != nil {
			if cur.color == l.Color && cur.description == l.Description {
				res.SyncedLabels = append(res.SyncedLabels, setupLabelResult{Name: l.Name, Action: "skipped"})
				continue
			}
			// JSON mode auto-declines mismatched labels.
			res.Skipped = append(res.Skipped, setupSkipped{Path: "label:" + l.Name, Reason: "confirm-declined"})
			res.SyncedLabels = append(res.SyncedLabels, setupLabelResult{Name: l.Name, Action: "mismatch-declined"})
			continue
		}
		create := exec.Command("gh", "label", "create", l.Name, "--repo", repo,
			"--color", l.Color, "--description", l.Description)
		if _, err := create.CombinedOutput(); err != nil {
			edit := exec.Command("gh", "label", "edit", l.Name, "--repo", repo,
				"--color", l.Color, "--description", l.Description)
			if out, err2 := edit.CombinedOutput(); err2 != nil {
				msg := fmt.Sprintf("label %s: %s", l.Name, strings.TrimSpace(string(out)))
				res.Errors = append(res.Errors, msg)
				res.SyncedLabels = append(res.SyncedLabels, setupLabelResult{Name: l.Name, Action: "skipped", Error: msg})
				printSetupJSONAndFatal(res, fmt.Errorf("%s: %w", msg, err2))
				return
			} else {
				res.SyncedLabels = append(res.SyncedLabels, setupLabelResult{Name: l.Name, Action: "edited"})
				continue
			}
		}
		res.SyncedLabels = append(res.SyncedLabels, setupLabelResult{Name: l.Name, Action: "created"})
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
}

func printSetupJSONAndFatal(res *setupResult, err error) {
	msg := err.Error()
	res.Error = &msg
	if len(res.Errors) == 0 || res.Errors[len(res.Errors)-1] != msg {
		res.Errors = append(res.Errors, msg)
	}
	if res.Created == nil {
		res.Created = []setupCreated{}
	}
	if res.Skipped == nil {
		res.Skipped = []setupSkipped{}
	}
	if res.SyncedLabels == nil {
		res.SyncedLabels = []setupLabelResult{}
	}
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
	if res.Errors == nil {
		res.Errors = []string{}
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	fatal(err)
}

func syncWorkflowsJSON(root string, res *setupResult) []string {
	dir := filepath.Join(root, ".jigz", "workflows")
	var legacy []string
	for _, name := range legacyWorkflows {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			legacy = append(legacy, name)
		}
	}
	entries, err := embeddedWorkflows.ReadDir("workflows")
	if err != nil {
		printSetupJSONAndFatal(res, err)
		return legacy
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if len(legacy) > 0 && containsConsolidated(e.Name()) {
			dest := filepath.Join(dir, e.Name())
			res.Skipped = append(res.Skipped, setupSkipped{Path: dest, Reason: "legacy-present"})
			continue
		}
		writeWorkflowJSON(embeddedWorkflows, filepath.Join(dir, e.Name()), "workflows/"+e.Name(), keepEdits, "workflow", res)
	}
	prompts, err := embeddedWorkflows.ReadDir("workflows/prompts")
	if err != nil {
		printSetupJSONAndFatal(res, err)
		return legacy
	}
	for _, p := range prompts {
		writeWorkflowJSON(embeddedWorkflows, filepath.Join(dir, "prompts", p.Name()), "workflows/prompts/"+p.Name(), keepEdits, "prompt", res)
	}
	return legacy
}

func containsConsolidated(name string) bool {
	for _, n := range consolidatedWorkflows {
		if n == name {
			return true
		}
	}
	return false
}

func writeWorkflowJSON(fsys fs.FS, dest, src string, policy writePolicy, kind string, res *setupResult) {
	data, err := fs.ReadFile(fsys, src)
	if err != nil {
		printSetupJSONAndFatal(res, err)
		return
	}
	if cur, err := os.ReadFile(dest); err == nil {
		if string(cur) == string(data) {
			res.Skipped = append(res.Skipped, setupSkipped{Path: dest, Reason: "exists"})
			return
		}
		// In JSON mode, any existing differing file is kept (auto-decline for pm-agent, keepEdits for workflows).
		// For workflows the policy is keepEdits, for pm-agent it is askBeforeOverwrite.
		if policy == keepEdits {
			res.Skipped = append(res.Skipped, setupSkipped{Path: dest, Reason: "exists"})
			return
		}
		res.Skipped = append(res.Skipped, setupSkipped{Path: dest, Reason: "confirm-declined"})
		return
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		printSetupJSONAndFatal(res, err)
		return
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		printSetupJSONAndFatal(res, err)
		return
	}
	res.Created = append(res.Created, setupCreated{Path: dest, Kind: kind})
}

func syncPMAgentJSON(root string, res *setupResult) {
	writeWorkflowJSON(embeddedPMAgent, filepath.Join(root, ".jigz", "pm-agent.md"), "pm-agent.md", askBeforeOverwrite, "pm-agent", res)
}

func ensureGitignoreJSON(dir string, res *setupResult) {
	path := filepath.Join(dir, ".gitignore")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		printSetupJSONAndFatal(res, fmt.Errorf("read .gitignore: %w", err))
		return
	}
	var selective bool
	have := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		have[t] = true
		if strings.HasPrefix(t, "!") && strings.Contains(t, ".jigz") {
			selective = true
		}
	}
	if selective {
		if have["jigz-*.db"] {
			res.Skipped = append(res.Skipped, setupSkipped{Path: path, Reason: "selective-tracked"})
			return
		}
		if err := appendDBIgnore(path, string(raw)); err != nil {
			printSetupJSONAndFatal(res, fmt.Errorf("write .gitignore: %w", err))
			return
		}
		res.Created = append(res.Created, setupCreated{Path: path, Kind: "gitignore"})
		return
	}
	want := []string{".jigz/", "/jig"}
	old := map[string]bool{}
	for _, e := range oldGitignoreEntries {
		old[e] = true
	}
	var kept []string
	var dropped bool
	for _, line := range strings.Split(string(raw), "\n") {
		if old[strings.TrimSpace(line)] {
			dropped = true
			continue
		}
		kept = append(kept, line)
	}
	var missing []string
	for _, w := range want {
		if !have[w] {
			missing = append(missing, w)
		}
	}
	if len(missing) == 0 && !dropped {
		res.Skipped = append(res.Skipped, setupSkipped{Path: path, Reason: "already-ignored"})
		return
	}
	out := strings.TrimRight(strings.Join(kept, "\n"), "\n")
	if len(out) > 0 {
		out += "\n"
	}
	if !have["# Jigz local state (added by jig setup)"] {
		out += "# Jigz local state (added by jig setup)\n"
	}
	for _, m := range missing {
		out += m + "\n"
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		printSetupJSONAndFatal(res, fmt.Errorf("write .gitignore: %w", err))
		return
	}
	res.Created = append(res.Created, setupCreated{Path: path, Kind: "gitignore"})
}

// existingLabel is a label as GitHub currently has it.
type existingLabel struct {
	color       string
	description string
}

// labelOn reads a label, returning nil when it does not exist.
func labelOn(repo, name string) (*existingLabel, error) {
	out, err := exec.Command("gh", "label", "list", "--repo", repo,
		"--search", name, "--limit", "100", "--json", "name,color,description").Output()
	if err != nil {
		return nil, err
	}
	var labels []struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &labels); err != nil {
		return nil, err
	}
	// gh --search is a substring match, so an exact name check follows.
	for _, l := range labels {
		if l.Name == name {
			return &existingLabel{color: l.Color, description: l.Description}, nil
		}
	}
	return nil, nil
}

// oldGitignoreEntries is the granular scheme setup wrote before ignoring
// .jigz/ wholesale (and its earlier .sortie/ spellings); dropped as
// subsumed when seen.
var oldGitignoreEntries = []string{
	".jigz/.env.loop", ".jigz/workflows/", ".jigz/workspaces/", ".sortie-*.db",
	".sortie/.env.loop", ".sortie/workflows/", ".sortie/workspaces/",
	"jig", "jigz",
}

// selectiveGitignore replaces `.jigz/` for teams that share loop config
// in git. Only track config.yaml with `token: ""` — secrets stay in env.
// (`.jigz/*` rather than `.jigz/` so the `!` re-includes take effect:
// git never descends into an excluded directory.)
const selectiveGitignore = `# jig: track shared config, ignore run state
.jigz/*
!.jigz/config.yaml
!.jigz/workflows/
jigz-*.db`

// appendDBIgnore appends the jigz-*.db ignore on its own line and
// writes the result to path.
func appendDBIgnore(path, content string) error {
	out := content
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += "jigz-*.db\n"
	return os.WriteFile(path, []byte(out), 0o644)
}

// ensureGitignore ignores .jigz/ wholesale — generated env, workspaces,
// db files, and local config all stay untracked — creating .gitignore if
// missing. Stale lines from the old granular scheme are removed. When the
// file re-includes parts of .jigz/ via negations the user tracks
// selectively on purpose, so those lines are left alone and a missing
// jigz-*.db ignore is appended.
func ensureGitignore(dir string) {
	path := filepath.Join(dir, ".gitignore")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		fatal(fmt.Errorf("read .gitignore: %w", err))
	}
	var selective bool
	have := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		have[t] = true
		if strings.HasPrefix(t, "!") && strings.Contains(t, ".jigz") {
			selective = true
		}
	}
	if selective {
		if have["jigz-*.db"] {
			return
		}
		if err := appendDBIgnore(path, string(raw)); err != nil {
			fatal(fmt.Errorf("write .gitignore: %w", err))
		}
		fmt.Println("added jigz-*.db to", path)
		return
	}
	want := []string{".jigz/", "/jig"}
	old := map[string]bool{}
	for _, e := range oldGitignoreEntries {
		old[e] = true
	}
	var kept []string
	var dropped bool
	for _, line := range strings.Split(string(raw), "\n") {
		if old[strings.TrimSpace(line)] {
			dropped = true
			continue
		}
		kept = append(kept, line)
	}
	var missing []string
	for _, w := range want {
		if !have[w] {
			missing = append(missing, w)
		}
	}
	if len(missing) == 0 && !dropped {
		fmt.Println(".jigz/ is fully ignored")
		return
	}
	out := strings.TrimRight(strings.Join(kept, "\n"), "\n")
	if len(out) > 0 {
		out += "\n"
	}
	if !have["# Jigz local state (added by jig setup)"] {
		out += "# Jigz local state (added by jig setup)\n"
	}
	for _, m := range missing {
		out += m + "\n"
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		fatal(fmt.Errorf("write .gitignore: %w", err))
	}
	fmt.Println("updated", path, "— .jigz/ is now fully ignored")
	fmt.Println("To share loop config in git instead, replace `.jigz/` with:\n" + selectiveGitignore)
}
