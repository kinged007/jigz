package main

import (
	"bufio"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/gen"
	"github.com/kinged007/jigz/internal/source"
)

//go:embed workflows
var embeddedWorkflows embed.FS

//go:embed scripts
var embeddedScripts embed.FS

// promptIn wraps stdin so answers typed ahead are not dropped between
// per-file prompts by a reader rebuilt each time.
var promptIn *bufio.Reader

// syncPrompts installs the embedded prompts into
// <root>/.jigz/workflows/prompts/, next to the loop's config, env and
// workspaces. A missing file is written and an existing file is never
// changed or removed, because installed prompts are hand-edited.
// ponytail: no version stamp, so a file left untouched but outdated by a
// binary upgrade is never noticed; delete it and re-run setup.
//
// A root written by the hub source holds only the files the hub served,
// so nothing is installed there.
func syncPrompts(root string) {
	if source.IsHubRoot(root) {
		return
	}
	dir := filepath.Join(root, ".jigz", "workflows")
	prompts, err := embeddedWorkflows.ReadDir("workflows/prompts")
	if err != nil {
		fatal(err)
	}
	for _, p := range prompts {
		writeIfMissing(embeddedWorkflows, filepath.Join(dir, "prompts", p.Name()), "workflows/prompts/"+p.Name())
	}
}

// syncScripts installs the embedded cleanup script as
// <root>/.jigz/scripts/cleanup.sh so the cleanup example in a fresh
// config points at a file that exists. A missing file is written with
// mode 0755; an existing file is never changed, because it is
// hand-edited. It returns the installed path, or "" when one already
// existed. Hub roots hold only the files the hub served, so nothing is
// installed there.
func syncScripts(root string) string {
	if source.IsHubRoot(root) {
		return ""
	}
	dest := filepath.Join(root, ".jigz", "scripts", "cleanup.sh")
	if _, err := os.Stat(dest); err == nil {
		return ""
	}
	data, err := embeddedScripts.ReadFile("scripts/cleanup.sh")
	if err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(dest, data, 0o755); err != nil {
		fatal(err)
	}
	// WriteFile is umask-filtered; the exec bits must survive for
	// `sh -c <command>` to run the script directly.
	if err := os.Chmod(dest, 0o755); err != nil {
		fatal(err)
	}
	return dest
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

func writeIfMissing(fsys fs.FS, dest, src string) {
	if _, err := os.Stat(dest); err == nil {
		return
	}
	data, err := fs.ReadFile(fsys, src)
	if err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		fatal(err)
	}
}

// confirmOverwrite asks before replacing something a person may have
// changed: an existing label whose colour or description differs. what is
// the text to show. Only an explicit yes proceeds, so a closed, piped, or
// non-interactive stdin declines instead of guessing. Answering "a" or
// "all" sets *all, and later calls with the same *all proceed without
// asking.
func confirmOverwrite(what string, all *bool) bool {
	if *all {
		return true
	}
	fmt.Printf("  %s differs from .jigz/config.yaml. Update it? [y/N/a] ", what)
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
	case "a", "all":
		*all = true
		return true
	}
	fmt.Printf("  kept %s; answer y or a on the next setup run to update it from .jigz/config.yaml\n", what)
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

// workflowPath returns the repo-local workflow file, filling missing
// prompts from the embedded copies.
func workflowPath(root, name string) string {
	syncPrompts(root)
	return filepath.Join(root, ".jigz", "workflows", name)
}

// discoverWorkflows lists the installed WORKFLOW.*.md files in name order
// that a work entry produces. The loop name is the filename stem.
func discoverWorkflows(root string) []loopDef {
	loops, err := filesSource(root, "", "").Loops(context.Background())
	if err != nil {
		fatal(err)
	}
	return loopDefs(loops)
}

// filesSource is the loop source of `jig <root>`: the workflows installed
// in root, narrowed by only (JIG_ONLY).
func filesSource(root, project, only string) source.Files {
	return source.Files{Root: root, Project: project, Only: only,
		Sync:   syncPrompts,
		Filter: configuredOnly,
	}
}

// configuredOnly keeps the discovered workflows that a work entry of
// root's config produces: <key>, or <key>-<group> for an entry split by
// agent (see gen.Split). Other WORKFLOW.*.md files stay on disk
// and are not started. A config that cannot be loaded is an error. A hub
// root holds only the files the hub served and is not filtered.
func configuredOnly(root string, names []string) ([]string, error) {
	if source.IsHubRoot(root) {
		return names, nil
	}
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	produced := map[string]bool{}
	for _, res := range cfg.Resolve() {
		for _, l := range gen.Split(res, cfg.Agents) {
			produced[l.Name] = true
		}
	}
	var out []string
	for _, n := range names {
		if produced[n] {
			out = append(out, n)
		}
	}
	return out, nil
}

func loopDefs(loops []source.Loop) []loopDef {
	defs := make([]loopDef, len(loops))
	for i, l := range loops {
		defs[i] = loopDef{l.Workflow, source.WorkflowFile(l.Workflow)}
	}
	return defs
}

// loopDef is one loop: its name (the WORKFLOW.*.md stem) and the file
// sortie runs it from.
type loopDef struct{ name, file string }

// selectLoops narrows the discovered set to the comma-separated names
// in only (source.Select), keeping discovery order.
func selectLoops(loops []loopDef, only string) ([]loopDef, error) {
	byName := map[string]loopDef{}
	names := make([]string, len(loops))
	for i, l := range loops {
		byName[l.name] = l
		names[i] = l.name
	}
	names, err := source.Select(names, only)
	if err != nil {
		return nil, err
	}
	out := make([]loopDef, len(names))
	for i, n := range names {
		out[i] = byName[n]
	}
	return out, nil
}
