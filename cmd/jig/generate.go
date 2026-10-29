package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/gen"
)

// defaultPrompt is the dispatch safety net every generated document
// points at.
const defaultPrompt = "prompts/default.md"

// genSummary reports what generation did, for setup output and reload
// decisions.
type genSummary struct {
	Generated []string
	Unchanged []string
	Drifted   []string
	Skipped   []string
	Prompts   []string
	Warnings  []string
}

// generatedDoc is one rendered loop document and the entry it came from.
type generatedDoc struct {
	Loop    string
	Text    []byte
	Managed bool
}

// generateDocs loads the config, resolves the engine profile per work
// entry, splits entries into loops and renders each managed loop. A
// loop whose tracker or agent kind the profile does not support is
// skipped with a warning: it goes offline instead of failing the run.
// Anything else fails the whole generation, so reload replaces
// documents only when all of them validate.
func generateDocs(root string) ([]generatedDoc, []string, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, nil, err
	}
	if len(cfg.Work) == 0 {
		return nil, nil, nil
	}
	jigPath, err := selfExe()
	if err != nil {
		return nil, nil, err
	}
	var docs []generatedDoc
	var warnings []string
	resolved := cfg.Resolve()
	for _, slug := range sortedKeys(resolved) {
		res := resolved[slug]
		if !res.Managed {
			continue
		}
		p, _, err := gen.ProfileFor(res.Engine.Repo, res.Engine.Version)
		if err != nil {
			return nil, nil, fmt.Errorf("work.%s: %w", slug, err)
		}
		if res.Engine.Version != "" && res.Engine.Version != p.Version {
			warnings = append(warnings, fmt.Sprintf("work.%s: engine %s@%s has no contract tests; using the %s profile", slug, res.Engine.Repo, res.Engine.Version, p.Version))
		}
		for _, l := range gen.Split(res, cfg.Agents) {
			if !p.SupportsTracker(l.Entry.Tracker.Kind) {
				warnings = append(warnings, fmt.Sprintf("work.%s: loop %s uses tracker kind %q, which %s does not support; the loop stays offline", slug, l.Name, l.Entry.Tracker.Kind, p.EngineVersion()))
				continue
			}
			if l.Runtime != "" && !p.SupportsAgent(l.Runtime) {
				warnings = append(warnings, fmt.Sprintf("work.%s: loop %s uses agent kind %q, which %s does not support; the loop stays offline", slug, l.Name, l.Runtime, p.EngineVersion()))
				continue
			}
			text, err := gen.Render(p, l, cfg.Agents, jigPath, defaultPrompt)
			if err != nil {
				return nil, nil, fmt.Errorf("work.%s: %w", slug, err)
			}
			docs = append(docs, generatedDoc{Loop: l.Name, Text: text, Managed: res.Managed})
		}
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Loop < docs[j].Loop })
	return docs, warnings, nil
}

// validateDocs engine-validates rendered documents. Prompts resolve
// against the repository, so the renders go to a scratch directory
// with the prompts copied in; nothing is written to the repo. Without
// an installed engine there is nothing to validate against, which is
// a warning and not an error: setup works engine-less.
func validateDocs(root string, docs []generatedDoc) (string, error) {
	if len(docs) == 0 {
		return "", nil
	}
	cfg, err := config.Load(root)
	if err != nil {
		return "", err
	}
	bin, err := resolveSortieBin()
	if err != nil {
		return "engine not installed: generated documents not validated", nil
	}
	tmp, err := os.MkdirTemp("", "jigz-validate-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	prompts := filepath.Join(root, ".jigz", "workflows", "prompts")
	if _, err := os.Stat(prompts); err == nil {
		if err := copyDir(prompts, filepath.Join(tmp, "prompts")); err != nil {
			return "", err
		}
	}
	for _, d := range docs {
		file := "WORKFLOW." + d.Loop + ".md"
		if err := os.WriteFile(filepath.Join(tmp, file), d.Text, 0o644); err != nil {
			return "", err
		}
		if out, err := runSortieValidate(bin, tmp, loopDef{d.Loop, file}, cfg); err != nil {
			return "", fmt.Errorf("loop %s: %w%s", d.Loop, err, validateOutputSuffix(out))
		}
	}
	return "", nil
}

func validateOutputSuffix(out string) string {
	if strings.TrimSpace(out) == "" {
		return ""
	}
	return ": " + strings.TrimSpace(out)
}

// promoteDocs writes rendered documents, keeping a .bak copy when a
// file on disk differs. Unmanaged loops are never written. It returns
// what changed, so setup can report it and reload can decide.
func promoteDocs(root string, docs []generatedDoc) (genSummary, error) {
	var sum genSummary
	if err := os.MkdirAll(filepath.Join(root, ".jigz", "workflows"), 0o755); err != nil {
		return sum, err
	}
	if err := os.MkdirAll(filepath.Join(root, ".jigz", "data"), 0o755); err != nil {
		return sum, err
	}
	for _, d := range docs {
		if !d.Managed {
			sum.Skipped = append(sum.Skipped, d.Loop)
			continue
		}
		path := gen.DocPath(root, d.Loop)
		text := d.Text
		st, _, _, err := gen.Check(path, true, func() ([]byte, error) { return text, nil })
		if err != nil {
			return sum, fmt.Errorf("loop %s: %w", d.Loop, err)
		}
		switch st {
		case gen.Clean:
			sum.Unchanged = append(sum.Unchanged, d.Loop)
		default:
			exists := true
			if _, err := os.Stat(path); os.IsNotExist(err) {
				exists = false
			}
			if err := gen.AtomicWrite(path, text); err != nil {
				return sum, fmt.Errorf("loop %s: %w", d.Loop, err)
			}
			if exists {
				sum.Drifted = append(sum.Drifted, d.Loop)
			} else {
				sum.Generated = append(sum.Generated, d.Loop)
			}
		}
	}
	return sum, nil
}

// ensurePrompts writes the prompt files the generated documents point
// at when they are missing: the embedded copy when one exists, else a
// stub the owner fills in.
func ensurePrompts(root string, docs []generatedDoc) ([]string, error) {
	need := map[string]bool{defaultPrompt: true}
	for _, d := range docs {
		for _, p := range promptRefs(d.Text) {
			need[p] = true
		}
	}
	var created []string
	for _, p := range sortedStrings(keys(need)) {
		clean := strings.TrimPrefix(p, "./")
		if filepath.IsAbs(clean) || strings.Contains(clean, "..") {
			continue
		}
		dest := filepath.Join(root, ".jigz", "workflows", clean)
		if _, err := os.Stat(dest); err == nil {
			continue
		}
		var body []byte
		if data, err := embeddedWorkflows.ReadFile("workflows/prompts/" + filepath.Base(clean)); err == nil {
			body = data
		} else {
			body = []byte("# " + filepath.Base(clean) + "\n\nFill in the prompt for this rule.\n")
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return created, err
		}
		if err := os.WriteFile(dest, body, 0o644); err != nil {
			return created, err
		}
		created = append(created, dest)
	}
	return created, nil
}

// copyDir copies a directory tree of files.
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// promptRefs lists the template paths a rendered document points at.
func promptRefs(text []byte) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(text), "\n") {
		t := strings.TrimSpace(line)
		v, ok := strings.CutPrefix(t, "template:")
		if !ok {
			continue
		}
		p := strings.Trim(strings.TrimSpace(v), `"`)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// driftReports re-renders the managed loops and compares with the files
// on disk without writing anything. The report maps loop to "" when
// clean, "missing" when the file is absent, "unmanaged" for entries
// Jigz does not own, and a short diff otherwise.
func driftReports(root string) (map[string]string, error) {
	out := map[string]string{}
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	jigPath, err := selfExe()
	if err != nil {
		return nil, err
	}
	resolved := cfg.Resolve()
	for _, slug := range sortedKeys(resolved) {
		res := resolved[slug]
		if !res.Managed {
			for _, l := range gen.Split(res, cfg.Agents) {
				out[l.Name] = "unmanaged"
			}
			continue
		}
		p, _, err := gen.ProfileFor(res.Engine.Repo, res.Engine.Version)
		if err != nil {
			return nil, fmt.Errorf("work.%s: %w", slug, err)
		}
		for _, l := range gen.Split(res, cfg.Agents) {
			loop := l
			if !p.SupportsTracker(l.Entry.Tracker.Kind) || (l.Runtime != "" && !p.SupportsAgent(l.Runtime)) {
				out[l.Name] = "offline: unsupported kind"
				continue
			}
			text, err := gen.Render(p, loop, cfg.Agents, jigPath, defaultPrompt)
			if err != nil {
				return nil, fmt.Errorf("work.%s: %w", slug, err)
			}
			st, disk, want, err := gen.Check(gen.DocPath(root, l.Name), true, func() ([]byte, error) { return text, nil })
			if err != nil {
				return nil, fmt.Errorf("loop %s: %w", l.Name, err)
			}
			switch st {
			case gen.Clean:
				out[l.Name] = ""
			case gen.Drift:
				if disk == nil {
					out[l.Name] = "missing"
				} else {
					out[l.Name] = diffLines(string(disk), string(want), 10)
				}
			default:
				out[l.Name] = "unmanaged"
			}
		}
	}
	return out, nil
}

// diffLines describes the first differing lines of disk versus want.
func diffLines(disk, want string, max int) string {
	d, w := strings.Split(disk, "\n"), strings.Split(want, "\n")
	var b strings.Builder
	n := 0
	for i := 0; i < len(d) || i < len(w); i++ {
		var g, v string
		if i < len(d) {
			g = d[i]
		}
		if i < len(w) {
			v = w[i]
		}
		if g == v {
			continue
		}
		if n >= max {
			fmt.Fprintf(&b, "... (%d more)", len(d)-i+len(w)-i)
			break
		}
		fmt.Fprintf(&b, "line %d: disk %q generated %q\n", i+1, g, v)
		n++
	}
	return strings.TrimRight(b.String(), "\n")
}

func sortedStrings(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// generateAll renders the managed documents, ensures the prompts they
// point at, engine-validates the renders and then promotes them. It is
// what setup, start and reload run before anything else. A document on
// disk is replaced only when every render validates; missing prompts
// are additive and never overwrite.
func generateAll(root string) (genSummary, error) {
	var sum genSummary
	docs, warnings, err := generateDocs(root)
	if err != nil {
		return sum, err
	}
	sum.Warnings = warnings
	created, err := ensurePrompts(root, docs)
	if err != nil {
		return sum, err
	}
	sum.Prompts = created
	if warn, err := validateDocs(root, docs); err != nil {
		return sum, err
	} else if warn != "" {
		sum.Warnings = append(sum.Warnings, warn)
	}
	promoted, err := promoteDocs(root, docs)
	if err != nil {
		return sum, err
	}
	promoted.Prompts = sum.Prompts
	promoted.Warnings = append(sum.Warnings, promoted.Warnings...)
	return promoted, nil
}
