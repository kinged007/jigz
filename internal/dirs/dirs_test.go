package dirs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootMovesLegacyDirectory(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, LegacyName)
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "machine.yaml"), []byte("hours: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Root(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, Name)
	if got != want {
		t.Fatalf("Root = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(want, "machine.yaml")); err != nil {
		t.Fatalf("machine.yaml not moved: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy directory still there: %v", err)
	}
}

func TestRootKeepsExistingDirectory(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{Name, LegacyName} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, LegacyName, "old"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Root(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(root, Name) {
		t.Fatalf("Root = %q, want %q", got, filepath.Join(root, Name))
	}
	if _, err := os.Stat(filepath.Join(root, LegacyName, "old")); err != nil {
		t.Fatalf("legacy directory was moved into an existing one: %v", err)
	}
}

func TestStateMergesLegacyDirectories(t *testing.T) {
	root := t.TempDir()
	pool := filepath.Join(root, LegacyName)
	dash := filepath.Join(root, legacyDashboardName)
	for _, dir := range []string{pool, dash} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pool, "pool.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dash, "history.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", root)
	got, err := State()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, Name)
	if got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	for _, name := range []string{"pool.json", "history.db"} {
		if _, err := os.Stat(filepath.Join(want, name)); err != nil {
			t.Fatalf("%s not moved: %v", name, err)
		}
	}
}
