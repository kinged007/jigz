package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/engine"
	"github.com/kinged007/jigz/internal/rollup"
)

// TestProfileTableFoldsReleases pins section 4.7: the profile table,
// rollup.EngineReleases and engine.MinVersion agree, and every release
// has a schema fixture. Adding a release to the table without fixtures
// fails this test.
func TestProfileTableFoldsReleases(t *testing.T) {
	profs := Profiles()
	if len(profs) != len(rollup.EngineReleases) {
		t.Fatalf("%d profiles, %d engine releases", len(profs), len(rollup.EngineReleases))
	}
	seen := map[string]bool{}
	for _, p := range profs {
		v := strings.TrimPrefix(p.Version, "v")
		seen[v] = true
		schema, ok := rollup.SchemaOf(v)
		if !ok {
			t.Errorf("release %s not in EngineReleases", v)
			continue
		}
		if schema != p.Schema {
			t.Errorf("release %s schema %d, profile says %d", v, schema, p.Schema)
		}
		fixture := filepath.Join("..", "rollup", "testdata", "schema", fmt.Sprintf("%d.sql", p.Schema))
		if _, err := os.Stat(fixture); err != nil {
			t.Errorf("release %s: no schema fixture: %v", v, err)
		}
	}
	for _, r := range rollup.EngineReleases {
		if !seen[r.Version] {
			t.Errorf("release %s has no profile", r.Version)
		}
	}
	if strings.TrimPrefix(MinVersion, "v") != strings.TrimPrefix(engine.MinVersion, "v") {
		t.Errorf("gen MinVersion %s != engine MinVersion %s", MinVersion, engine.MinVersion)
	}
}
