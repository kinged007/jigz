package main

import (
	"encoding/json"
	"errors"
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
)

const (
	dashDefaultPort = 7677
	loopBasePort    = 7678
	probeCeilPort   = 7900
	probeStopEmpty  = 3
	registryDirName = "sortie-loop"
	registryName    = "registry.json"
)

// loopEndpoint describes one running sortie loop process: the repo it
// watches, the workflow it runs, where its dashboard API lives, and
// where its SQLite state lives for direct history reads.
type loopEndpoint struct {
	Repo     string `json:"repo"`
	RepoName string `json:"repo_name"`
	Loop     string `json:"loop"`
	Port     int    `json:"port"`
	DBPath   string `json:"db_path"`
	// WorkflowFile is the absolute path of the workflow the loop runs.
	WorkflowFile string `json:"workflow_file,omitempty"`
	// LoopType is "issues" or "prs".
	LoopType string `json:"loop_type,omitempty"`
}

// registry maps repo root -> its loop endpoints. One registry file is
// shared by every sortie-loop on the machine so a --unite dashboard can
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
	// Version is the sortie-loop version of the supervisor.
	Version   string    `json:"version,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	// StatusPath is the supervisor's status document.
	StatusPath string `json:"status_path,omitempty"`
}

// registryPath returns the shared registry file, creating its directory.
func registryPath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, registryDirName)
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
// .sortie/config.yaml, then the git remote. Reading the git remote
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

// claimPorts finds free ports for n loops plus the dashboard by
// probing upward from the base ports (busy ports are skipped — each
// loop server needs its own). Dashboard takes dashPort unless disabled.
// A --dashboard-port=N flag always claims N for this run (fatal if
// busy) so the operator can force a fresh dashboard; otherwise a live
// unite dashboard found by findUniteDashboard is reused (dash=-1).
// ponytail: two repos cannot share loop ports (one server per port),
// so each repo's loops stack to the right; the unite dashboard reads
// them all via the registry instead.
func claimPorts(n int, dashPort int, noDashboard bool) (loops []int, dash int, err error) {
	loops = make([]int, 0, n)
	for p := loopBasePort; len(loops) < n && p <= probeCeilPort; p++ {
		if probeFreePort(p) {
			loops = append(loops, p)
		}
	}
	if len(loops) < n {
		return nil, 0, fmt.Errorf("only %d free ports below %d, need %d", len(loops), probeCeilPort, n)
	}
	if noDashboard {
		return loops, 0, nil
	}
	// Negative dashPort joins a live dashboard found earlier: no port
	// is claimed and none is started (main prints the reused URL).
	if dashPort < 0 {
		return loops, dashPort, nil
	}
	if dashPort == 0 {
		dashPort = dashDefaultPort
	}
	if probeFreePort(dashPort) {
		return loops, dashPort, nil
	}
	return nil, 0, fmt.Errorf("dashboard port %d is busy (another dashboard is running?)", dashPort)
}

// findUniteDashboard returns the port of a live unite dashboard: the
// first busy port at or below the dashboard default whose page marks
// itself unite. Loop API ports and non-unite dashboards are skipped.
func findUniteDashboard() int {
	for p := dashDefaultPort; p >= loopBasePort-8 && p > 0; p-- {
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
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", p))
	if err != nil {
		return 0
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if rerr != nil || resp.StatusCode != http.StatusOK {
		return 0
	}
	if strings.Contains(string(body), "\u2014 Unite") {
		return p
	}
	return 0
}

// writeRegistry records this sortie-loop's endpoints so a --unite
// dashboard started later can discover them without probing.
func writeRegistry(root string, endpoints []loopEndpoint, startedAt time.Time) error {
	path, err := registryPath()
	if err != nil {
		return err
	}
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
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// dropRegistry removes this repo's entry on clean shutdown.
func dropRegistry(root string) {
	path, err := registryPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var reg registry
	if err := json.Unmarshal(data, &reg); err != nil || reg.Entries == nil {
		return
	}
	delete(reg.Entries, root)
	data, err = json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// dbPathFor resolves a workflow's db_path front-matter the way sortie
// does: relative paths join with the workflow directory (the repo root
// here, since sortie runs with Dir=root). Falls back to .sortie.db.
func dbPathFor(root, file string) string {
	data, err := os.ReadFile(filepath.Join(root, ".sortie", "workflows", file))
	if err != nil {
		return filepath.Join(root, ".sortie", "workflows", ".sortie.db")
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
			return filepath.Join(root, ".sortie", "workflows", v)
		}
		if strings.TrimSpace(line) == "---" && !strings.HasPrefix(strings.TrimSpace(line), "db_path") {
			continue
		}
	}
	return filepath.Join(root, ".sortie", "workflows", ".sortie.db")
}

// readRegistry returns live endpoints across all repos: entries whose
// loop API ports still answer, probed upward from 7678. After 3
// consecutive empty ports the scan stops, tolerating gaps left by
// stopped loops in between. Stale registry entries (dead owner, old
// heartbeat) are skipped, never deleted here.
func readRegistry() []loopEndpoint {
	path, err := registryPath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var reg registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil
	}
	known := map[int]loopEndpoint{}
	for _, e := range reg.Entries {
		if time.Since(e.UpdatedAt) > 5*time.Minute && !pidAlive(e.PID) {
			continue
		}
		for _, l := range e.Loops {
			if l.Port > 0 {
				known[l.Port] = l
			}
		}
	}
	var out []loopEndpoint
	empty := 0
	for p := loopBasePort; p <= probeCeilPort && empty < probeStopEmpty; p++ {
		if probeFreePort(p) {
			empty++
			continue
		}
		empty = 0
		if l, ok := known[p]; ok {
			out = append(out, l)
			continue
		}
		// ponytail: answering port with no registry entry is a
		// loop whose owner died without cleanup; include it
		// under an unknown repo so its activity stays visible.
		out = append(out, loopEndpoint{Repo: "unknown", RepoName: "unknown", Loop: "unknown", Port: p})
	}
	return out
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

// waitLoopBound blocks until the loop child answers its state API on
// port, or until it dies, or timeout elapses. bind errors are the main
// startup failure (address already in use) and sortie exits when its
// HTTP server fails to bind, so a child that dies during startup lost
// the bind race and the caller must retry on the next port. A foreign
// server answering on port does not count: the child must bind it
// itself (the process group leader check guards against a reparented
// zombie answering for a dead child).
func waitLoopBound(cmd *exec.Cmd, port int, timeout time.Duration) error {
	if port == 0 {
		return nil
	}
	deadline := time.Now().Add(timeout)
	for {
		if childExited(cmd) {
			return fmt.Errorf("loop exited before answering (port %d)", port)
		}
		if probeLoopAPI(port) {
			return nil
		}
		if time.Now().After(deadline) {
			return nil // slow start is short of fatal; the dashboard retries
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// probeLoopAPI reports whether p answers the loop state API.
func probeLoopAPI(p int) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/state", p))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var payload struct {
		Counts json.RawMessage `json:"counts"`
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&payload) == nil && len(payload.Counts) > 0
}

// childExited reports whether the child already terminated, reaping it
// without blocking when it has. A later cmd.Wait only gets an error.
func childExited(cmd *exec.Cmd) bool {
	var ws syscall.WaitStatus
	pid, err := syscall.Wait4(cmd.Process.Pid, &ws, syscall.WNOHANG, nil)
	if pid == cmd.Process.Pid {
		return true
	}
	return err != nil && !errors.Is(err, syscall.EINTR)
}

// startLoopChild starts one loop child on the first port at or above
// start that the child actually bounds, retrying upward when it does
// not. Two sortie-loop runs started close together probe the same free
// ports; sortie ends the process when its HTTP server cannot bind, so
// the loser moves to the next free port instead of leaving its repo
// unwatched with no dashboard endpoint.
func startLoopChild(bin, envFile, dir, file string, env []string, start int) (*exec.Cmd, int, error) {
	if start == 0 {
		cmd, port, err := startOn(bin, envFile, dir, file, env, 0)
		return cmd, port, err
	}
	for p := start; p <= probeCeilPort; p++ {
		if !probeFreePort(p) {
			continue
		}
		cmd, port, err := startOn(bin, envFile, dir, file, env, p)
		if err != nil {
			return nil, 0, err
		}
		if waitLoopBound(cmd, port, 15*time.Second) == nil {
			return cmd, port, nil
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	return nil, 0, fmt.Errorf("no free loop port below %d", probeCeilPort)
}

// startOn launches one loop child on port; port 0 disables its server.
func startOn(bin, envFile, dir, file string, env []string, port int) (*exec.Cmd, int, error) {
	cmd := exec.Command(bin, "--env-file", envFile, "--port", fmt.Sprint(port), workflowPath(dir, file))
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	return cmd, port, nil
}

// portLock serializes the port claim and the child startup of one
// sortie-loop run against other runs on the machine.
type portLock struct{ f *os.File }

// lockStarts takes the machine-wide port claim lock, blocking until it
// is free. Held from before claimPorts until every loop child has
// bound its port, so a run started at the same moment probes ports the
// earlier run has already claimed instead of stealing them.
func lockStarts() (*portLock, error) {
	path, err := registryPath()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &portLock{f: f}, nil
}

// release frees the lock; the kernel also drops it when the process
// exits, so a crashed run never wedges later ones.
func (l *portLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
