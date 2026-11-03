package loopsrc

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDiscoverReadsTheDataDirectory lists databases under .jigz/data/;
// a legacy copy left under .jigz/workflows/ is skipped when the data
// directory holds the loop's database, and still listed otherwise.
func TestDiscoverReadsTheDataDirectory(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, ".jigz", "workflows")
	data := filepath.Join(root, ".jigz", "data")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(wf, "WORKFLOW.a.md"), "---\ndb_path: jigz-a.db\n---\n")
	write(filepath.Join(data, "jigz-b.db"), "x")
	write(filepath.Join(wf, "jigz-c.db"), "stale")
	write(filepath.Join(data, "jigz-c.db"), "current")
	got := map[string]string{}
	for _, l := range Discover(map[string]string{"p": root}) {
		got[l.Loop] = l.DBPath
	}
	want := map[string]string{
		"a": filepath.Join(wf, "jigz-a.db"),
		"b": filepath.Join(data, "jigz-b.db"),
		"c": filepath.Join(data, "jigz-c.db"),
	}
	if len(got) != len(want) {
		t.Fatalf("discovered = %v, want %v", got, want)
	}
	for n, p := range want {
		if got[n] != p {
			t.Errorf("loop %s = %s, want %s", n, got[n], p)
		}
	}
}
