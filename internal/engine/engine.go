package engine

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/dirs"
)

// The engine is the sortie binary this tool drives. The default fork is
// required: upstream sortie-ai/sortie ships neither the pi agent adapter
// nor the github-pr tracker in every release, and the shipped workflows
// depend on both. A repository set in machine.yaml or JIG_ENGINE_REPO must
// still carry both adapters.

const (
	DefaultRepo = "kinged007/sortie"
	// DefaultTag is the engine release the workflows are written for. The
	// version in asset and cache names is the tag without its leading "v".
	DefaultTag = "v1.28.1"
	Binary     = "sortie"
	// maxEngineBytes caps what a release archive may expand to, so a
	// corrupt or hostile download cannot fill the disk.
	maxEngineBytes = 512 << 20
	// engineHTTPTimeout is generous: a release archive is ~8MB.
	engineHTTPTimeout = 10 * time.Minute
)

var engineHTTP = &http.Client{Timeout: engineHTTPTimeout}

// supportedTargets are the platforms with a tar.gz release asset.
// Windows releases ship a .zip, which nothing here unpacks.
var supportedTargets = []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"}

// AssetName is the release asset holding the engine for one target.
func AssetName(goos, goarch, version string) (string, error) {
	target := goos + "/" + goarch
	if !slices.Contains(supportedTargets, target) {
		return "", fmt.Errorf("no engine release for %s (supported: %s)", target, strings.Join(supportedTargets, ", "))
	}
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", Binary, version, goos, goarch), nil
}

// CachePath is the file holding the engine for one repository and
// version. The repository is part of the name, so a fork and upstream at
// the same version do not share a file, and a new pin cannot disturb a
// loop still running the old one. "+" cannot occur in a GitHub owner or
// repository name, so two repositories never produce the same name.
func (s Spec) CachePath(dir string) string {
	return filepath.Join(dir, s.CacheName())
}

// CacheName is the file name of the engine in a share directory.
func (s Spec) CacheName() string {
	return Binary + "-" + strings.Replace(s.Repo, "/", "+", 1) + "-" + s.Version()
}

// DataDir is $XDG_DATA_HOME/jigz, or ~/.local/share/jigz.
func DataDir() string {
	return dirs.Data()
}

// releaseBase is the download root for one engine release. It is a
// variable so tests can point it at a stub server.
var releaseBase = func(repo, version string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/v%s", repo, version)
}

// Ensure returns a path to the engine for spec, downloading it when it is
// not cached. A freshly installed engine is also linked onto PATH, so a
// bare `sortie` works in any new shell.
func Ensure(spec Spec) (string, error) {
	bin, err := Fetch(spec)
	if err != nil {
		return "", fmt.Errorf("%w%s", err, releaseHint(spec, err))
	}
	LinkOnPath(bin)
	return bin, nil
}

// Fetch returns a path to the engine for spec, downloading it when the
// cache is empty. A cached file is trusted: it was verified when written and
// the directory is the user's own, so re-verifying would cost a network round
// trip on every start.
func Fetch(spec Spec) (string, error) {
	dest := spec.CachePath(DataDir())
	if fi, err := os.Stat(dest); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
		return dest, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	archive, err := downloadEngine(spec)
	if err != nil {
		return "", err
	}
	defer os.Remove(archive)
	return dest, extractEngine(archive, dest)
}

// downloadEngine fetches the release archive into a temp file, verified
// against the release's checksums.txt, and returns the archive path.
func downloadEngine(spec Spec) (string, error) {
	url := os.Getenv("JIG_ENGINE_URL")
	want := strings.ToLower(os.Getenv("JIG_ENGINE_SHA256"))
	if url == "" {
		asset, err := AssetName(runtime.GOOS, runtime.GOARCH, spec.Version())
		if err != nil {
			return "", err
		}
		base := releaseBase(spec.Repo, spec.Version())
		sum, err := releaseChecksum(base, asset)
		if err != nil {
			return "", err
		}
		url, want = base+"/"+asset, sum
	} else if want == "" {
		fmt.Fprintln(os.Stderr, "jig: warning: JIG_ENGINE_URL is set without JIG_ENGINE_SHA256; this download is unverified")
	}
	return fetchVerified(url, want)
}

// releaseHint turns a missing checksums.txt into something the user
// can act on. A release published by hand — one bare binary named
// sortie-linux-amd64 and nothing else — is the shape the fork currently
// has, and "404 Not Found" alone sends people looking for a network
// problem that is not there.
func releaseHint(spec Spec, err error) string {
	repo, version := spec.Repo, spec.Version()
	if !strings.Contains(err.Error(), "checksums.txt") {
		return ""
	}
	asset, aerr := AssetName(runtime.GOOS, runtime.GOARCH, version)
	if aerr != nil {
		asset = "sortie_" + version + "_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	}
	return fmt.Sprintf(`; %s release %s has no checksums.txt, so its engine cannot be verified.
  It needs %s and checksums.txt, which a GoReleaser release produces.
  Meanwhile: build the engine from source and point JIG_ENGINE_BIN at it, or set
  JIG_ENGINE_URL and JIG_ENGINE_SHA256 to fetch a verified archive.`,
		repo, version, asset)
}

// releaseChecksum reads one asset's sha256 out of a release's
// checksums.txt, whose lines are "<hex>  <name>".
func releaseChecksum(base, asset string) (string, error) {
	url := base + "/checksums.txt"
	resp, err := engineHTTP.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", url, err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != asset {
			continue
		}
		if len(fields[0]) != sha256.Size*2 {
			return "", fmt.Errorf("%s: %q is not a sha256 digest", url, fields[0])
		}
		return fields[0], nil
	}
	return "", fmt.Errorf("%s lists no %s; refusing to install an unverifiable engine", url, asset)
}

// fetchVerified streams url into a temp file, hashing as it goes, and
// returns its path. A wanted digest must match, so an interrupted or
// tampered download is discarded rather than kept. An empty wantSHA
// skips the check and leaves that to the caller to have warned about.
func fetchVerified(url, wantSHA string) (path string, err error) {
	resp, err := engineHTTP.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: %s", url, resp.Status)
	}
	f, err := os.CreateTemp("", "sortie-engine-*.tar.gz")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	h := sha256.New()
	if _, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxEngineBytes+1)); err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if wantSHA != "" {
		if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA {
			err = fmt.Errorf("checksum mismatch for %s: got %s, want %s", url, got, wantSHA)
			return "", err
		}
	}
	return f.Name(), nil
}

// extractEngine writes the engine binary out of a release archive and
// moves it into place at dest. Only the `sortie` entry is read, so a
// crafted archive cannot place a file anywhere else on disk.
func extractEngine(archivePath, dest string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}
	defer gz.Close()
	out, err := os.CreateTemp(filepath.Dir(dest), ".sortie-engine-*")
	if err != nil {
		return err
	}
	defer func() {
		out.Close()
		os.Remove(out.Name())
	}()
	if err := copyEngineEntry(tar.NewReader(gz), out); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chmod(out.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(out.Name(), dest)
}

// engineEntry reports whether a tar header names the engine binary.
// GoReleaser produces two shapes depending on its archive config: a flat
// archive with `sortie` at the root, and one wrapped in a per-platform
// directory (`linux_amd64/sortie`). Both are accepted. A deeper path or
// a traversal segment is not, so a tampered archive cannot reach the
// match through `../../sortie` or `a/b/sortie`.
//
// The archive is already digest-verified before it gets here, so this
// locates the file rather than establishing trust; it does not need to be
// strict enough to reject every name a real release would never use.
func engineEntry(name string) bool {
	if name == Binary {
		return true
	}
	dir, file, ok := strings.Cut(name, "/")
	return ok && file == Binary && dir != "" && dir != "." && dir != ".." &&
		!strings.ContainsAny(dir, `/\`)
}

// copyEngineEntry finds the engine in the archive and copies it to w.
// Reading stops at the first match, so the licence and readme entries
// that share the archive are never decompressed.
func copyEngineEntry(tr *tar.Reader, w io.Writer) error {
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("archive holds no %q", Binary)
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || !engineEntry(hdr.Name) {
			continue
		}
		if hdr.Size > maxEngineBytes {
			return fmt.Errorf("%s expands to %d bytes, over the %d limit", hdr.Name, hdr.Size, int64(maxEngineBytes))
		}
		_, err = io.Copy(w, io.LimitReader(tr, maxEngineBytes))
		return err
	}
}

// LinkOnPath exposes the engine under its plain name in a
// directory on the user's PATH, so `sortie validate` and `sortie stats`
// work in any new shell without an alias or the version in the path. It
// prefers the directory holding the jig binary when that
// directory is on PATH — the `go install` layout, where one PATH entry
// covers both tools — and falls back to ~/.local/bin.
func LinkOnPath(src string) {
	dir, onPath := bindir()
	if LinkAt(src, dir) == "" {
		return
	}
	if !onPath {
		fmt.Fprintf(os.Stderr, "jig: note: add %s to PATH to run the engine by name\n", dir)
	}
}

// LinkAt links src into dir under the engine's plain name and
// returns the link path, or "" when dir cannot be used. Every failure is
// reported and then ignored: the engine is already usable by absolute
// path, and a working loop matters more than a convenient command name.
func LinkAt(src, dir string) string {
	link := filepath.Join(dir, Binary)
	if cur, err := os.Readlink(link); err == nil {
		if cur == src {
			return link
		}
		// Only a link into our own cache is ours to refresh. A link
		// anywhere else is the user's, and replacing it silently would
		// break a hand-built engine they point it at.
		if !strings.HasPrefix(cur, DataDir()+string(filepath.Separator)) {
			fmt.Fprintf(os.Stderr, "jig: warning: %s points at %s, which Jigz did not install; left alone (the engine runs from %s)\n", link, cur, src)
			return ""
		}
	} else if _, err := os.Lstat(link); err == nil {
		fmt.Fprintf(os.Stderr, "jig: warning: %s exists and was not installed by Jigz; left alone (the engine runs from %s)\n", link, src)
		return ""
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "jig: warning: cannot create %s: %v\n", dir, err)
		return ""
	}
	// Rename a fresh symlink into place so a shell starting mid-install
	// never sees the link missing.
	tmp := link + ".new"
	os.Remove(tmp)
	if err := os.Symlink(src, tmp); err != nil {
		fmt.Fprintf(os.Stderr, "jig: warning: cannot link %s: %v\n", link, err)
		return ""
	}
	if err := os.Rename(tmp, link); err != nil {
		os.Remove(tmp)
		fmt.Fprintf(os.Stderr, "jig: warning: cannot link %s: %v\n", link, err)
		return ""
	}
	return link
}

// bindir is where the engine is linked, and whether that directory
// is already on PATH.
func bindir() (dir string, onPath bool) {
	local := filepath.Join(homeDir(), ".local", "bin")
	if self, err := os.Executable(); err == nil {
		if d := filepath.Dir(self); dirOnPath(d) {
			return d, true
		}
	}
	return local, dirOnPath(local)
}

func dirOnPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	return false
}
