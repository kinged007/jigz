package main

import (
	"bufio"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/kinged007/jigz/internal/config"
)

//go:embed workflows
var embeddedWorkflows embed.FS

//go:embed pm-agent.md
var embeddedPMAgent embed.FS

// writePolicy decides what writeWorkflow does when an installed file
// already exists and differs from the embedded copy. The project-manager
// prompt is confirmed by setup; the loop startup path keeps edits because
// it must never block on a prompt.
type writePolicy int

const (
	keepEdits writePolicy = iota
	askBeforeOverwrite
)

// legacyWorkflows are the per-role workflow files that WORKFLOW.issues.md
// and WORKFLOW.prs.md replace. While any of them is installed, the new
// files are not written, because both sets would process the same issues
// and PRs.
var legacyWorkflows = []string{
	"WORKFLOW.plan.md", "WORKFLOW.build.md", "WORKFLOW.review.md",
	"WORKFLOW.review-fix.md", "WORKFLOW.merge.md",
}

var consolidatedWorkflows = []string{"WORKFLOW.issues.md", "WORKFLOW.prs.md"}

// promptIn wraps stdin so answers typed ahead are not dropped between
// per-file prompts by a reader rebuilt each time.
var promptIn *bufio.Reader

// syncWorkflows installs the embedded workflow files (WORKFLOW.*.md +
// prompts/) into <root>/.jigz/workflows/ so they sit visible next to
// the loop's config, env, and workspaces. A missing file is written and
// an existing file is never changed or removed, because installed
// workflows and prompts are hand-edited. It returns the legacy workflow
// files found; while there are any, WORKFLOW.issues.md and
// WORKFLOW.prs.md are not written.
// ponytail: no version stamp, so a file left untouched but outdated by a
// binary upgrade is never noticed; delete it and re-run setup.
func syncWorkflows(root string) []string {
	dir := filepath.Join(root, ".jigz", "workflows")
	var legacy []string
	for _, name := range legacyWorkflows {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			legacy = append(legacy, name)
		}
	}
	entries, err := embeddedWorkflows.ReadDir("workflows")
	if err != nil {
		fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || (len(legacy) > 0 && slices.Contains(consolidatedWorkflows, e.Name())) {
			continue
		}
		writeWorkflow(embeddedWorkflows, filepath.Join(dir, e.Name()), "workflows/"+e.Name(), keepEdits)
	}
	prompts, err := embeddedWorkflows.ReadDir("workflows/prompts")
	if err != nil {
		fatal(err)
	}
	for _, p := range prompts {
		writeWorkflow(embeddedWorkflows, filepath.Join(dir, "prompts", p.Name()), "workflows/prompts/"+p.Name(), keepEdits)
	}
	return legacy
}

// warnStalePrompts names the installed plan and build prompts that differ
// from the shipped copies. Existing installs keep their old prompts, and an
// old build.md that does not remove agent:plan-build would re-dispatch an
// issue.
func warnStalePrompts(root string) {
	for _, name := range []string{"plan.md", "build.md"} {
		cur, err := os.ReadFile(filepath.Join(root, ".jigz", "workflows", "prompts", name))
		if err != nil {
			continue
		}
		want, err := embeddedWorkflows.ReadFile("workflows/prompts/" + name)
		if err == nil && string(cur) != string(want) {
			fmt.Fprintf(os.Stderr, "note: .jigz/workflows/prompts/%s differs from the shipped copy; diff it or delete it and rerun setup to take the shipped version.\n", name)
		}
	}
}

// warnStaleWorkflows names the installed workflow files that differ from
// the shipped copies. A stale copy keeps variables the binary no longer
// sets, so setup must point at it instead of leaving a run to fail on an
// empty expansion.
func warnStaleWorkflows(root string) {
	dir := filepath.Join(root, ".jigz", "workflows")
	entries, err := embeddedWorkflows.ReadDir("workflows")
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !slices.Contains(consolidatedWorkflows, e.Name()) {
			continue
		}
		cur, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		want, err := embeddedWorkflows.ReadFile("workflows/" + e.Name())
		if err == nil && string(cur) != string(want) {
			fmt.Fprintf(os.Stderr, "note: .jigz/workflows/%s differs from the shipped copy; diff it or delete it and rerun setup to take the shipped version.\n", e.Name())
		}
	}
}

// warnMissingLabels names the shipped labels absent from a configured
// labels list, because setup only creates the labels in that list.
func warnMissingLabels(labels []config.Label) {
	have := map[string]bool{}
	for _, l := range labels {
		have[l.Name] = true
	}
	for _, l := range config.DefaultLabels {
		if !have[l.Name] {
			fmt.Fprintf(os.Stderr, "note: label %q is not in the labels list of .jigz/config.yaml, so setup does not create it.\n", l.Name)
		}
	}
}

// warnLegacy tells the user why the consolidated workflow files were not
// written. It prints nothing when legacy is empty.
func warnLegacy(legacy []string) {
	if len(legacy) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "warning: legacy workflow files found in .jigz/workflows/ (%s).\n"+
		"  WORKFLOW.issues.md and WORKFLOW.prs.md are skipped until the legacy files are deleted.\n"+
		"  Delete them, then rerun `jig setup`.\n", strings.Join(legacy, ", "))
}

// syncPMAgent installs the project-manager prompt into <root>/.jigz/
// beside config.yaml, so every repo that runs setup gets a PM ready to
// use. Hand-edited copies follow the same rules as the workflows.
func syncPMAgent(root string, policy writePolicy) {
	writeWorkflow(embeddedPMAgent, filepath.Join(root, ".jigz", "pm-agent.md"), "pm-agent.md", policy)
}

func writeWorkflow(fsys fs.FS, dest, src string, policy writePolicy) {
	data, err := fs.ReadFile(fsys, src)
	if err != nil {
		fatal(err)
	}
	if cur, err := os.ReadFile(dest); err == nil {
		if string(cur) == string(data) {
			return
		}
		if policy == keepEdits || !confirmOverwrite(displayPath(dest)) {
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		fatal(err)
	}
}

// confirmOverwrite asks before replacing something a person may have
// changed: a hand-edited project-manager prompt, or an existing label whose colour
// or description differs. what is the text to show. Only an explicit yes
// proceeds, so a closed, piped, or non-interactive stdin declines instead
// of guessing.
func confirmOverwrite(what string) bool {
	fmt.Printf("  %s differs from what Jigz ships. Overwrite? [y/N] ", what)
	if promptIn == nil {
		promptIn = bufio.NewReader(os.Stdin)
	}
	line, err := promptIn.ReadString('\n')
	if err != nil && line == "" {
		fmt.Println()
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	fmt.Printf("  kept %s — delete it and re-run setup to take the shipped version\n", what)
	return false
}

// displayPath shortens a path for a prompt, preferring a path relative to
// the current directory over a long absolute one.
func displayPath(dest string) string {
	if rel, err := filepath.Rel(".", dest); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return dest
}

// workflowPath returns the repo-local workflow file, filling gaps from the
// embedded copies so checkouts set up before workflows moved into .jigz
// still run.
func workflowPath(root, name string) string {
	syncWorkflows(root)
	return filepath.Join(root, ".jigz", "workflows", name)
}

// discoverWorkflows lists the installed WORKFLOW.*.md files in name order,
// filling gaps from the embedded copies first. The startup set is
// whatever files exist on disk, so adding WORKFLOW.triage.md (embedded
// or hand-written) starts a triage loop with no code change — the loop
// name is the filename stem ("triage").
func discoverWorkflows(root string) []loopDef {
	warnLegacy(syncWorkflows(root))
	entries, err := os.ReadDir(filepath.Join(root, ".jigz", "workflows"))
	if err != nil {
		fatal(err)
	}
	var loops []loopDef
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "WORKFLOW.") || !strings.HasSuffix(name, ".md") {
			continue
		}
		stem := strings.TrimSuffix(strings.TrimPrefix(name, "WORKFLOW."), ".md")
		loops = append(loops, loopDef{stem, name})
	}
	return loops
}

// loopDef is one loop: its name (the WORKFLOW.*.md stem) and the file
// sortie runs it from.
type loopDef struct{ name, file string }

// selectLoops narrows the discovered set to the comma-separated names
// in only, so a supervisor can start a chosen subset of a repo's
// loops. An empty only keeps them all, which is what a plain run does.
// A name matching no installed workflow is an error rather than a
// silent omission: the caller asked for a loop that does not exist, and
// quietly starting the rest would leave that work unwatched.
func selectLoops(loops []loopDef, only string) ([]loopDef, error) {
	if strings.TrimSpace(only) == "" {
		return loops, nil
	}
	keep := map[string]bool{}
	for _, n := range strings.Split(only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			keep[n] = true
		}
	}
	if len(keep) == 0 {
		return nil, fmt.Errorf("JIG_ONLY=%q names no workflow", only)
	}
	var out []loopDef
	for _, l := range loops {
		if keep[l.name] {
			out = append(out, l)
			delete(keep, l.name)
		}
	}
	if len(keep) > 0 {
		unknown := make([]string, 0, len(keep))
		for n := range keep {
			unknown = append(unknown, n)
		}
		sort.Strings(unknown)
		return nil, fmt.Errorf("no WORKFLOW.*.md named %s in this repo", strings.Join(unknown, " or "))
	}
	return out, nil
}
