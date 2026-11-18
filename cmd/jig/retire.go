package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/gen"
)

// staleLoopDocs lists the loop names on disk that a managed work entry
// owns (<slug> or <slug>-*) although no configured entry produces them.
// A name owned by several slugs belongs to the longest one, so slugs
// "issues" and "issues-fast" keep their own documents.
func staleLoopDocs(root string, cfg *config.Config) []string {
	resolved := cfg.Resolve()
	produced := map[string]bool{}
	for _, res := range resolved {
		for _, l := range gen.Split(res, cfg.Agents) {
			produced[l.Name] = true
		}
	}
	var stale []string
	for _, l := range listWorkflows(root) {
		if produced[l.name] {
			continue
		}
		owner := ""
		for slug := range resolved {
			if (l.name == slug || strings.HasPrefix(l.name, slug+"-")) && len(slug) > len(owner) {
				owner = slug
			}
		}
		if owner != "" && resolved[owner].Managed {
			stale = append(stale, l.name)
		}
	}
	return stale
}

// retireStaleLoops removes the stale loop documents that Jigz generated.
// Any other stale document is kept with a warning, because it still runs
// as a loop. It returns the removed loop names and the warnings.
func retireStaleLoops(root string) ([]string, []string, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, nil, err
	}
	var removed, warnings []string
	for _, name := range staleLoopDocs(root, cfg) {
		path := gen.DocPath(root, name)
		cur, err := os.ReadFile(path)
		if err != nil {
			return removed, warnings, err
		}
		if gen.Managed(cur) {
			if err := os.Remove(path); err != nil {
				return removed, warnings, err
			}
			removed = append(removed, name)
			continue
		}
		warnings = append(warnings, fmt.Sprintf("%s is not produced by any work entry but still runs as a loop; delete it or set managed: false on the entry that owns it", path))
	}
	return removed, warnings, nil
}

// loopsCheck fails when a document owned by a managed work entry is not
// one that generation produces, because it would run as a duplicate loop.
func loopsCheck(root string, cfg *config.Config) validateCheck {
	stale := staleLoopDocs(root, cfg)
	if len(stale) == 0 {
		return validateCheck{Name: "loops", OK: true}
	}
	files := make([]string, len(stale))
	for i, n := range stale {
		files[i] = "WORKFLOW." + n + ".md"
	}
	return validateCheck{Name: "loops", OK: false, Error: strings.Join(files, ", ") + ": not produced by any work entry, so it would run as a duplicate loop; rerun `jig setup` to retire it or delete it"}
}
