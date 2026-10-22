package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEngineAssetName(t *testing.T) {
	got, err := AssetName("darwin", "arm64", "1.24.0-pi.github-pr.1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "sortie_1.24.0-pi.github-pr.1_darwin_arm64.tar.gz"; got != want {
		t.Errorf("asset = %q, want %q", got, want)
	}
	_, err = AssetName("windows", "amd64", "1.0.0")
	if err == nil {
		t.Error("windows has no tar.gz asset and should be rejected")
	} else if !strings.Contains(err.Error(), "windows/amd64") {
		t.Errorf("error should name the unsupported target, got %v", err)
	}
}

// tarGz builds an archive from name -> body.
func tarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReleaseChecksum(t *testing.T) {
	asset := "sortie_1.0.0_linux_amd64.tar.gz"
	want := "b" + strings.Repeat("0", 63)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The real file lists archives and SBOMs, two spaces per line.
		fmt.Fprintf(w, "%s  sortie_1.0.0_linux_amd64.tar.gz.sbom.json\n", "c"+strings.Repeat("0", 63))
		fmt.Fprintf(w, "%s  %s\n", want, asset)
	}))
	defer srv.Close()

	got, err := releaseChecksum(srv.URL, asset)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("checksum = %q, want %q", got, want)
	}
	if _, err := releaseChecksum(srv.URL, "sortie_1.0.0_plan9_mips.tar.gz"); err == nil {
		t.Error("an asset missing from checksums.txt must be an error, not a skip")
	}
}

func TestFetchVerifiedRejectsWrongDigest(t *testing.T) {
	body := []byte("archive bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	sum := sha256.Sum256(body)
	got, err := fetchVerified(srv.URL, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatalf("matching digest rejected: %v", err)
	}
	// A verified download is the caller's to keep and delete; fetch
	// removes it once the engine is extracted.
	os.Remove(got)

	wrong := strings.Repeat("0", 64)
	path, err := fetchVerified(srv.URL, wrong)
	if err == nil {
		t.Fatal("mismatched digest accepted")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error should say the digest mismatched, got %v", err)
	}
	if path != "" {
		t.Error("a rejected download must not report a path")
	}
	// Nothing is left behind: a failed fetch cleans up its temp file.
	entries, _ := filepath.Glob(filepath.Join(os.TempDir(), "sortie-engine-*"))
	if len(entries) != 0 {
		t.Errorf("temp archives left behind: %v", entries)
	}
}

// The engine is pulled out of a release archive that also carries a
// licence and a readme, and lands executable.
func TestExtractEngine(t *testing.T) {
	engine := []byte("#!/bin/sh\necho engine\n")
	archive := filepath.Join(t.TempDir(), "release.tar.gz")
	if err := os.WriteFile(archive, tarGz(t, map[string][]byte{
		"LICENSE":   []byte("Apache-2.0"),
		"sortie":    engine,
		"README.md": []byte("# sortie"),
	}), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "sortie-1.0.0")
	if err := extractEngine(archive, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, engine) {
		t.Errorf("extracted %q, want %q", got, engine)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("mode = %v, want executable", fi.Mode().Perm())
	}
}

// Only the `sortie` entry is read, so an archive that tries to place a
// file elsewhere writes nothing outside the destination.
func TestExtractEngineIgnoresForeignEntries(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "hostile.tar.gz")
	if err := os.WriteFile(archive, tarGz(t, map[string][]byte{
		"../../sortie": []byte("escape attempt"),
		"/etc/sortie":  []byte("escape attempt"),
	}), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out", "sortie-1.0.0")
	if err := extractEngine(archive, dest); err == nil {
		t.Error("an archive with no plain `sortie` entry should fail")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("a failed extraction left a file at the destination")
	}
}

// The whole path a user with no engine takes: empty cache, resolve the
// asset, fetch, verify, extract, land executable.
func TestFetchEndToEnd(t *testing.T) {
	engine := []byte("#!/bin/sh\necho engine\n")
	archive := tarGz(t, map[string][]byte{
		"LICENSE": []byte("Apache-2.0"),
		"sortie":  engine,
	})
	sum := sha256.Sum256(archive)
	asset, err := AssetName(runtime.GOOS, runtime.GOARCH, "1.0.0")
	if err != nil {
		t.Skipf("no engine release for this platform: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/owner/engine/v1.0.0/"+asset, func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	})
	mux.HandleFunc("/owner/engine/v1.0.0/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	orig := releaseBase
	releaseBase = func(_, _ string) string { return srv.URL + "/owner/engine/v1.0.0" }
	defer func() { releaseBase = orig }()

	isolate(t)
	spec := Spec{Repo: "owner/engine", Tag: "v1.0.0"}
	t.Setenv("JIG_ENGINE_URL", "")
	t.Setenv("JIG_ENGINE_SHA256", "")

	got, err := Fetch(spec)
	if err != nil {
		t.Fatal(err)
	}
	if want := spec.CachePath(DataDir()); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	body, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, engine) {
		t.Errorf("engine = %q, want %q", body, engine)
	}

	// A second call must be served from the cache, so a server that now
	// fails must not change the answer.
	srv.Close()
	again, err := Fetch(spec)
	if err != nil {
		t.Fatalf("cached engine was refetched: %v", err)
	}
	if again != got {
		t.Errorf("cache path changed: %q then %q", got, again)
	}
}

// A URL override is honoured but still checked when a digest is given.
func TestDownloadEngineURLOverride(t *testing.T) {
	body := []byte("not a tarball")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	t.Setenv("JIG_ENGINE_URL", srv.URL+"/custom.tar.gz")
	t.Setenv("JIG_ENGINE_SHA256", strings.Repeat("a", 64))
	if _, err := downloadEngine(Spec{Repo: "owner/engine", Tag: "v1.0.0"}); err == nil {
		t.Fatal("digest mismatch should be reported")
	}

	sum := sha256.Sum256(body)
	t.Setenv("JIG_ENGINE_SHA256", hex.EncodeToString(sum[:]))
	path, err := downloadEngine(Spec{Repo: "owner/engine", Tag: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, body) {
		t.Errorf("downloaded %q, %v; want %q", raw, err, body)
	}
}

// isolate points every directory the engine package reads at a temp dir and
// clears the engine environment.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, k := range []string{"JIG_ENGINE_BIN", "JIG_ENGINE_REPO", "JIG_ENGINE_VERSION", "JIG_ENGINE_URL", "JIG_ENGINE_SHA256"} {
		t.Setenv(k, "")
	}
}

func TestMain(m *testing.M) {
	SetDownload(func(Spec) (string, error) { return "", errors.New("engine download disabled in unit tests") })
	os.Exit(m.Run())
}
