package hubstate

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func TestSaveLoadPermissions(t *testing.T) {
	setup(t)
	want := State{HubURL: "https://h", DaemonID: "d1", Name: "n", Credential: "c", RunnerLabel: "runner:n", TrackerToken: "t"}
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
	path, _ := Path()
	for p, mode := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
		} else if info.Mode().Perm() != mode {
			t.Errorf("%s: mode %04o", p, info.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temporary file left: %v", entries)
	}
}

func TestLoadRefusesGroupReadable(t *testing.T) {
	setup(t)
	if err := Save(State{HubURL: "https://h"}); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	for _, mode := range []os.FileMode{0o640, 0o604, 0o660} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(); err == nil {
			t.Errorf("mode %04o accepted", mode)
		}
	}
}

func TestLoadMissingAndRemove(t *testing.T) {
	setup(t)
	if _, err := Load(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err %v", err)
	}
	if err := Remove(); err != nil {
		t.Error(err)
	}
}

func TestLoadRefusesASymlink(t *testing.T) {
	setup(t)
	if err := Save(State{HubURL: "https://h"}); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	target := filepath.Join(filepath.Dir(path), "real.json")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skip(err)
	}
	if _, err := Load(); err == nil {
		t.Error("symlinked hub.json accepted")
	}
}

func TestLoadRefusesAWritableDirectory(t *testing.T) {
	setup(t)
	if err := Save(State{HubURL: "https://h"}); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	for _, mode := range []os.FileMode{0o770, 0o702} {
		if err := os.Chmod(filepath.Dir(path), mode); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(); err == nil {
			t.Errorf("directory mode %04o accepted", mode)
		}
	}
}
