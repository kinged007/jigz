package loopsrc

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/kinged007/jigz/internal/rollup"
)

// Discover lists the workflow databases on disk for each project, whether
// or not its loops are running. It reads files only and never contacts a
// loop. A workflow gets the database Jigz would open for it; a
// database shared by several workflows is listed once, under the first
// of them by name. A jigz-<name>.db that no workflow file points at is
// listed under <name>.
func Discover(roots map[string]string) []rollup.Loop {
	projects := make([]string, 0, len(roots))
	for p := range roots {
		projects = append(projects, p)
	}
	sort.Strings(projects)
	var out []rollup.Loop
	for _, p := range projects {
		dir := filepath.Join(roots[p], ".jigz", "workflows")
		claimed := map[string]bool{}
		files, _ := filepath.Glob(filepath.Join(dir, "WORKFLOW.*.md"))
		slices.Sort(files)
		for _, f := range files {
			name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "WORKFLOW."), ".md")
			path := filepath.Clean(dbPath(roots[p], filepath.Base(f)))
			if claimed[path] {
				continue
			}
			claimed[path] = true
			out = append(out, rollup.Loop{Repo: p, Loop: name, DBPath: path})
		}
		dbs, _ := filepath.Glob(filepath.Join(roots[p], ".jigz", "data", "jigz-*.db"))
		slices.Sort(dbs)
		// Databases left under .jigz/workflows/ predate the data
		// directory; they are still listed so history survives the
		// move, unless the data directory already holds the loop's
		// database. The engine itself follows the document's db_path
		// and never reads the stale copy.
		legacy, _ := filepath.Glob(filepath.Join(dir, "jigz-*.db"))
		slices.Sort(legacy)
		dataNames := map[string]bool{}
		for _, path := range dbs {
			dataNames[filepath.Base(path)] = true
		}
		for _, path := range legacy {
			if !dataNames[filepath.Base(path)] {
				dbs = append(dbs, path)
			}
		}
		for _, path := range dbs {
			if claimed[path] {
				continue
			}
			claimed[path] = true
			name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "jigz-"), ".db")
			out = append(out, rollup.Loop{Repo: p, Loop: name, DBPath: path})
		}
	}
	return out
}
