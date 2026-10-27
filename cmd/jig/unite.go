package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kinged007/jigz/internal/dirs"
)

const (
	dashDefaultPort = 7677
	dashScanFloor   = 7670
	registryName    = "registry.json"
)

// loopEndpoint describes one running loop process: the repo it
// watches, the workflow it runs, and where its SQLite state lives for
// history reads.
type loopEndpoint struct {
	Repo     string `json:"repo"`
	RepoName string `json:"repo_name"`
	Loop     string `json:"loop"`
	DBPath   string `json:"db_path"`
	// WorkflowFile is the absolute path of the workflow the loop runs.
	WorkflowFile string `json:"workflow_file,omitempty"`
	// LoopType is "issues" or "prs".
	LoopType string `json:"loop_type,omitempty"`
}

// registry maps repo root -> its loop endpoints. One registry file is
// shared by every Jigz on the machine so a --unite dashboard can
// find them all. Entries carry a heartbeat; readers skip stale ones.
type registry struct {
	Entries map[string]registryEntry `json:"entries"`
}

type registryEntry struct {
	RepoName  string         `json:"repo_name"`
	UpdatedAt time.Time      `json:"updated_at"`
	PID       int            `json:"pid"`
	Loops     []loopEndpoint `json:"loops"`
	// Args is the run's own command line, so a client replacing it can
	// start the next one with the same flags instead of guessing.
	Args []string `json:"args,omitempty"`
	// Version is the Jigz version of the supervisor.
	Version   string    `json:"version,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	// StatusPath is the supervisor's status document.
	StatusPath string `json:"status_path,omitempty"`
	// Dashboard is the address of the dashboard this run serves or joined.
	Dashboard string `json:"dashboard,omitempty"`
	// ServesDashboard is true for the run whose process serves it.
	ServesDashboard bool `json:"serves_dashboard,omitempty"`
}

// dashInfo is what a run records about its dashboard in the registry.
type dashInfo struct {
	URL   string
	Serve bool
}

// registryPath returns the registry file, creating its directory. A
// connected daemon sets JIG_REGISTRY_DIR for its loop children, so their
// registry and status files are kept apart from the shared one that
// standalone and united runs use.
func registryPath() (string, error) {
	if dir := os.Getenv("JIG_REGISTRY_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		return filepath.Join(dir, registryName), nil
	}
	return sharedRegistryPath()
}

// sharedRegistryPath is the machine-wide registry file in the user cache
// directory, whatever JIG_REGISTRY_DIR says.
func sharedRegistryPath() (string, error) {
	dir, err := dirs.Cache()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, registryName), nil
}

// repoName returns a short display name for a repo root: owner/name when
// the git remote parses, else the directory base.
// repoName is the identity recorded in the shared registry, so it has
// to agree with the slug a caller asked for. It resolves through
// configRepo, which prefers the repo: already written in
// .jigz/config.yaml, then the git remote. Reading the git remote
// directly is wrong for a state directory that is not a checkout: it is
// not a git repository, so the remote lookup failed and the fallback
// kept only filepath.Base, recording "myxon-beta" where every caller
// keys on "kinged007/myxon-beta". Anything grouping or filtering the
// dashboard by owner/name then silently matched nothing.
func repoName(root string) string {
	if slug := configRepo(root, ""); strings.Contains(slug, "/") {
		return slug
	}
	return filepath.Base(root)
}

// probeFreePort reports whether nothing is listening on localhost:port.
func probeFreePort(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return true
	}
	_ = c.Close()
	return false
}

// claimDashboard checks the dashboard port. A --dashboard-port=N flag
// always claims N for this run (fatal if busy) so the operator can force
// a fresh dashboard; otherwise a live unite dashboard found by
// findUniteDashboard is reused (a negative dashPort, no port claimed).
func claimDashboard(dashPort int, noDashboard bool) (int, error) {
	if noDashboard || dashPort < 0 {
		return dashPort, nil
	}
	if dashPort == 0 {
		dashPort = dashDefaultPort
	}
	if probeFreePort(dashPort) {
		return dashPort, nil
	}
	return 0, fmt.Errorf("dashboard port %d is busy (another dashboard is running?)", dashPort)
}

// findUniteDashboard returns the port of a live unite dashboard: the
// first busy port at or below the dashboard default whose page marks
// itself unite. Other listeners and non-unite dashboards are skipped.
func findUniteDashboard() int {
	for p := dashDefaultPort; p >= dashScanFloor; p-- {
		if found := findUniteDashboardOn(p); found > 0 {
			return found
		}
	}
	return 0
}

// findUniteDashboardOn reports port when its page marks itself unite;
// split out so tests can target ephemeral ports.
func findUniteDashboardOn(p int) int {
	client := &http.Client{Timeout: 2 * time.Second}
	if probeFreePort(p) {
		return 0
	}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/status", p))
	if err != nil {
		return 0
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if rerr != nil || resp.StatusCode != http.StatusOK {
		return 0
	}
	if strings.Contains(string(body), `data-dashboard="unite"`) {
		return p
	}
	return 0
}

// writeRegistry records this Jigz's endpoints so a --unite
// dashboard started later can discover them without probing.
func writeRegistry(root string, endpoints []loopEndpoint, startedAt time.Time, dash dashInfo) error {
	path, err := registryPath()
	if err != nil {
		return err
	}
	lock, err := lockRegistry()
	if err != nil {
		return err
	}
	defer lock.release()
	reg := registry{Entries: map[string]registryEntry{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &reg)
		if reg.Entries == nil {
			reg.Entries = map[string]registryEntry{}
		}
	}
	name := repoName(root)
	statusFile, err := statusPath(root)
	if err != nil {
		return err
	}
	reg.Entries[root] = registryEntry{
		RepoName:   name,
		UpdatedAt:  time.Now().UTC(),
		PID:        os.Getpid(),
		Loops:      endpoints,
		Args:       os.Args[1:],
		Version:    version,
		StartedAt:  startedAt.UTC(),
		StatusPath: statusFile,

		Dashboard:       dash.URL,
		ServesDashboard: dash.Serve,
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// dropRegistry marks this repo's entry stopped on clean shutdown. The
// entry stays so a dashboard that outlives this run keeps the repo
// listed with its last known state and the arguments this run was started
// with.
func dropRegistry(root string) {
	path, err := registryPath()
	if err != nil {
		return
	}
	lock, err := lockRegistry()
	if err != nil {
		return
	}
	defer lock.release()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var reg registry
	if err := json.Unmarshal(data, &reg); err != nil || reg.Entries == nil {
		return
	}
	e, ok := reg.Entries[root]
	if !ok {
		return
	}
	// The loops stay so the page can still read their databases: a
	// stopped repository keeps its history. The zero pid is what stops
	// the reader from probing their dead ports.
	e.PID = 0
	e.Dashboard, e.ServesDashboard = "", false
	e.UpdatedAt = time.Now().UTC()
	reg.Entries[root] = e
	data, err = json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return
	}
	_ = writeFileAtomic(path, data)
}

// dbPathFor resolves a workflow's db_path front-matter the way the
// engine does: relative paths join with the workflow directory (the repo root
// here, since the engine runs with Dir=root). Falls back to .sortie.db.
func dbPathFor(root, file string) string {
	data, err := os.ReadFile(filepath.Join(root, ".jigz", "workflows", file))
	if err != nil {
		return filepath.Join(root, ".jigz", "workflows", ".sortie.db")
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "db_path:"); ok {
			v = strings.TrimSpace(v)
			if v == "" {
				break
			}
			if filepath.IsAbs(v) {
				return v
			}
			return filepath.Join(root, ".jigz", "workflows", v)
		}
	}
	return filepath.Join(root, ".jigz", "workflows", ".sortie.db")
}

// pidAlive reports whether pid names a running process.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 performs error checking without delivery; on unix a nil
	// error means the process exists (or we lack permission, in which
	// case treating it as alive is the safe side for discovery).
	return p.Signal(syscall.Signal(0)) == nil
}

// startLoopChild starts one loop child. The engine's own server is
// always off. Output goes to this process's stdout and stderr, and also
// to logw when it is not nil. The tracker reads the same output.
func startLoopChild(bin, envFile, dir, file string, env []string, live *liveTracker, logw io.Writer) (*exec.Cmd, error) {
	cmd := exec.Command(bin, engineArgs(envFile, workflowPath(dir, file))...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = io.MultiWriter(os.Stdout, live.writer())
	cmd.Stderr = io.MultiWriter(os.Stderr, live.writer())
	if logw != nil {
		cmd.Stdout = io.MultiWriter(logw, cmd.Stdout)
		cmd.Stderr = io.MultiWriter(logw, cmd.Stderr)
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// engineArgs is the engine command line of a loop child.
func engineArgs(envFile, workflow string) []string {
	return []string{"--env-file", envFile, "--port", "0", workflow}
}

// fileLock is an exclusive lock on a file, shared by every Jigz process
// on the machine.
type fileLock struct{ f *os.File }

// lockRegistry serializes read-modify-write cycles on registry.json.
func lockRegistry() (*fileLock, error) {
	return lockFile(".update.lock")
}

func lockFile(suffix string) (*fileLock, error) {
	path, err := registryPath()
	if err != nil {
		return nil, err
	}
	return lockAt(path + suffix)
}

func lockAt(path string) (*fileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &fileLock{f: f}, nil
}

// release frees the lock; the kernel also drops it when the process
// exits, so a crashed run never wedges later ones.
func (l *fileLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
