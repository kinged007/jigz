package engine

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/machine"
)

func writeMachine(t *testing.T, body string) {
	t.Helper()
	path, err := machine.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stubDownload(t *testing.T, fn func(Spec) (string, error)) {
	t.Helper()
	t.Cleanup(SetDownload(fn))
}

func pathEngine(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "sortie")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return p
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, _ := os.Pipe()
	saved := os.Stderr
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = saved
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestConfiguredPrecedence(t *testing.T) {
	isolate(t)
	def := Spec{Repo: DefaultRepo, Tag: DefaultTag}
	if got, err := Configured(); err != nil || got != def {
		t.Fatalf("defaults = %v, %v; want %v", got, err, def)
	}

	writeMachine(t, "engine:\n  repo: file/engine\n  version: v2.0.0\n")
	if got, err := Configured(); err != nil || got != (Spec{"file/engine", "v2.0.0"}) {
		t.Fatalf("machine.yaml = %v, %v", got, err)
	}

	writeMachine(t, "engine:\n  version: v2.0.0\n")
	if got, _ := Configured(); got != (Spec{DefaultRepo, "v2.0.0"}) {
		t.Errorf("partial machine.yaml = %v; want the default repo with v2.0.0", got)
	}

	writeMachine(t, "engine:\n  repo: file/engine\n  version: v2.0.0\n")
	t.Setenv("JIG_ENGINE_REPO", "env/engine/")
	t.Setenv("JIG_ENGINE_VERSION", "3.1.4")
	if got, err := Configured(); err != nil || got != (Spec{"env/engine", "v3.1.4"}) {
		t.Errorf("env = %v, %v; want env/engine v3.1.4", got, err)
	}

	t.Setenv("JIG_ENGINE_BIN", "/custom/sortie")
	bin, unpinned, err := ResolveIn(nil)
	if err != nil || bin != "/custom/sortie" || unpinned {
		t.Errorf("JIG_ENGINE_BIN = %q %v %v; want it to win over everything", bin, unpinned, err)
	}
}

func TestConfiguredValidation(t *testing.T) {
	cases := []struct {
		name, yaml, repo, version, want string
	}{
		{name: "env repo", repo: "no-slash", want: "JIG_ENGINE_REPO"},
		{name: "env repo traversal", repo: "owner/..", want: "JIG_ENGINE_REPO"},
		{name: "env version", version: "1.2", want: "JIG_ENGINE_VERSION"},
		{name: "env prerelease", version: "v1.2.3-rc1", want: "JIG_ENGINE_VERSION"},
		{name: "file repo", yaml: "engine:\n  repo: a/b/c\n", want: "engine.repo"},
		{name: "file version", yaml: "engine:\n  version: latest\n", want: "engine.version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			if tc.yaml != "" {
				writeMachine(t, tc.yaml)
			}
			t.Setenv("JIG_ENGINE_REPO", tc.repo)
			t.Setenv("JIG_ENGINE_VERSION", tc.version)
			_, err := Configured()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to name %s", err, tc.want)
			}
			if _, _, err := ResolveIn([]string{t.TempDir()}); err == nil {
				t.Error("Resolve accepted an invalid configuration")
			}
		})
	}
}

func TestConfiguredIgnoresUnrelatedMachineErrors(t *testing.T) {
	isolate(t)
	writeMachine(t, "hours:\n  end: sometime\nengine:\n  version: v2.0.0\n")
	if got, err := Configured(); err != nil || got.Tag != "v2.0.0" {
		t.Errorf("Configured = %v, %v", got, err)
	}
}

func TestCacheNameSeparatesRepositories(t *testing.T) {
	fork := Spec{"kinged007/sortie", "v1.28.1"}
	upstream := Spec{"sortie-ai/sortie", "v1.28.1"}
	if fork.CacheName() == upstream.CacheName() {
		t.Fatalf("fork and upstream share %q", fork.CacheName())
	}
	if got, want := fork.CacheName(), "sortie-kinged007+sortie-1.28.1"; got != want {
		t.Errorf("CacheName = %q, want %q", got, want)
	}
	if a, b := (Spec{"a-b/c", "v1.0.0"}), (Spec{"a/b-c", "v1.0.0"}); a.CacheName() == b.CacheName() {
		t.Errorf("a-b/c and a/b-c share %q", a.CacheName())
	}
}

func TestInstalledMatchesRepositoryAndVersion(t *testing.T) {
	isolate(t)
	share := t.TempDir()
	want := Spec{"sortie-ai/sortie", "v1.29.0"}
	for _, s := range []Spec{{"kinged007/sortie", "v1.29.0"}, {"sortie-ai/sortie", "v1.28.1"}} {
		if err := os.WriteFile(s.CachePath(share), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(share, "sortie-1.29.0"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Installed([]string{share}, want); got != "" {
		t.Errorf("Installed = %q, want none: only other repositories, versions and the unqualified name exist", got)
	}
	if err := os.WriteFile(want.CachePath(share), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Installed([]string{share}, want); got != want.CachePath(share) {
		t.Errorf("Installed = %q, want %q", got, want.CachePath(share))
	}
	link := want.CachePath(share) + ".link"
	os.Symlink(want.CachePath(share), link)
	os.Remove(want.CachePath(share))
	if got := Installed([]string{share}, want); got != "" {
		t.Errorf("Installed = %q, want none for a symlink", got)
	}
}

func TestResolveInstalledBeatsPATH(t *testing.T) {
	isolate(t)
	calls := 0
	stubDownload(t, func(Spec) (string, error) { calls++; return "", errors.New("must not download") })
	pathEngine(t)
	share := t.TempDir()
	pin := Current().CachePath(share)
	if err := os.WriteFile(pin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, unpinned, err := ResolveIn([]string{share})
	if err != nil || got != pin || unpinned || calls != 0 {
		t.Errorf("got %q unpinned=%v err=%v calls=%d; want the installed engine", got, unpinned, err, calls)
	}
}

func TestResolveDownloadBeatsPATH(t *testing.T) {
	isolate(t)
	downloaded := filepath.Join(t.TempDir(), "sortie-pinned")
	stubDownload(t, func(Spec) (string, error) { return downloaded, nil })
	pathEngine(t)
	got, unpinned, err := ResolveIn([]string{t.TempDir()})
	if err != nil || got != downloaded || unpinned {
		t.Errorf("got %q unpinned=%v err=%v; want %q", got, unpinned, err, downloaded)
	}
}

func TestResolveDownloadsTheConfiguredRelease(t *testing.T) {
	isolate(t)
	t.Setenv("JIG_ENGINE_REPO", "sortie-ai/sortie")
	t.Setenv("JIG_ENGINE_VERSION", "v1.29.0")
	var asked []Spec
	stubDownload(t, func(s Spec) (string, error) { asked = append(asked, s); return "/dl/sortie", nil })
	for range 2 {
		if _, _, err := ResolveIn([]string{t.TempDir()}); err != nil {
			t.Fatal(err)
		}
	}
	if len(asked) != 1 || asked[0] != (Spec{"sortie-ai/sortie", "v1.29.0"}) {
		t.Errorf("downloads = %v, want one for sortie-ai/sortie v1.29.0", asked)
	}
}

func TestResolveFallsBackToPATHWithOneWarning(t *testing.T) {
	isolate(t)
	calls := 0
	stubDownload(t, func(Spec) (string, error) { calls++; return "", errors.New("network down") })
	hit := pathEngine(t)
	stderr := captureStderr(t, func() {
		for range 3 {
			got, unpinned, err := ResolveIn([]string{t.TempDir()})
			if err != nil || !unpinned || got != hit {
				t.Errorf("got %q unpinned=%v err=%v; want the PATH fallback %q", got, unpinned, err, hit)
			}
		}
	})
	if calls != 1 {
		t.Errorf("download attempted %d times, want 1", calls)
	}
	if n := strings.Count(stderr, "warning"); n != 1 {
		t.Errorf("%d warnings, want 1: %q", n, stderr)
	}
	for _, want := range []string{hit, "not checked", Current().String(), "network down"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("warning %q does not contain %q", stderr, want)
		}
	}
}

func TestResolveErrorsWithoutFallback(t *testing.T) {
	isolate(t)
	stubDownload(t, func(Spec) (string, error) { return "", errors.New("network down") })
	t.Setenv("PATH", t.TempDir())
	_, _, err := ResolveIn([]string{t.TempDir()})
	if err == nil {
		t.Fatal("expected an error with no engine anywhere")
	}
	for _, want := range []string{"network down", "JIG_ENGINE_BIN", Current().CacheName()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestShareDirsAddsPrefixOfEachBinary(t *testing.T) {
	isolate(t)
	prefix := t.TempDir()
	dirs := ShareDirs(filepath.Join(prefix, "bin", "jig"))
	if dirs[0] != DataDir() {
		t.Errorf("first dir = %q, want the data dir", dirs[0])
	}
	want := filepath.Join(prefix, "share", "jigz")
	if dirs[len(dirs)-1] != want {
		t.Errorf("dirs = %v, want %q last", dirs, want)
	}
}
