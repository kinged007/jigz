package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Files is the loop set of one repo checkout: its installed
// WORKFLOW.*.md files, narrowed by Only (the JIG_ONLY list).
type Files struct {
	Root    string
	Project string
	Only    string
	// Sync installs the shipped workflows into Root before they are
	// listed; nil lists what is on disk.
	Sync func(root string)
}

func (f Files) Loops(context.Context) ([]Loop, error) {
	if f.Sync != nil {
		f.Sync(f.Root)
	}
	names, err := Discover(f.Root)
	if err != nil {
		return nil, err
	}
	if names, err = Select(names, f.Only); err != nil {
		return nil, err
	}
	loops := make([]Loop, 0, len(names))
	for _, n := range names {
		h, err := filesHash(f.Root, n)
		if err != nil {
			return nil, err
		}
		loops = append(loops, Loop{Project: f.Project, Workflow: n, Root: f.Root, Hash: h})
	}
	return loops, nil
}

// filesHash hashes what a checkout loop reads: config.yaml, its workflow
// file and the prompts directory. The engine database also lives in
// .jigz/workflows/ and changes on every poll, so the directory as a whole
// is not hashed.
func filesHash(root, workflow string) (string, error) {
	files := map[string][]byte{}
	for _, rel := range []string{"config.yaml", "workflows/" + WorkflowFile(workflow)} {
		b, err := os.ReadFile(filepath.Join(root, ".jigz", filepath.FromSlash(rel)))
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil {
			files[rel] = b
		}
	}
	if err := readTree(files, filepath.Join(root, ".jigz", "workflows", "prompts"), "workflows/prompts/"); err != nil {
		return "", err
	}
	return hashFiles(files), nil
}

// Discover lists the stems of the WORKFLOW.*.md files in
// <root>/.jigz/workflows in name order.
func Discover(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, ".jigz", "workflows"))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "WORKFLOW.") || !strings.HasSuffix(name, ".md") {
			continue
		}
		names = append(names, strings.TrimSuffix(strings.TrimPrefix(name, "WORKFLOW."), ".md"))
	}
	return names, nil
}

// Select narrows names to the comma-separated list in only, keeping the
// order of names. An empty only keeps them all. A name in only that is
// not in names is an error: starting the rest would leave that work
// unwatched.
func Select(names []string, only string) ([]string, error) {
	if strings.TrimSpace(only) == "" {
		return names, nil
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
	var out []string
	for _, n := range names {
		if keep[n] {
			out = append(out, n)
			delete(keep, n)
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
