package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kinged007/jigz/internal/hubproto"
	"github.com/kinged007/jigz/internal/plan"
)

const (
	// manifestFile lists the hub files applied to a root, relative to
	// .jigz/workflows/. It also marks the root as written by the hub.
	manifestFile       = "hub-files.json"
	nextDir            = ".jigz.next"
	defaultIdleTimeout = 15 * time.Minute
	wakeInterval       = "24h"
)

var runnerLabelRe = regexp.MustCompile(`^runner:[a-z0-9][a-z0-9-]*$`)

// IsHubRoot reports whether root holds files written by Hub. Shipped
// workflows must never be installed into such a root.
func IsHubRoot(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".jigz", manifestFile))
	return err == nil
}

// AppliedFiles lists the hub files applied to root, for the status
// document.
func AppliedFiles(root string) ([]hubproto.FileHash, error) {
	return readManifest(filepath.Join(root, ".jigz", manifestFile))
}

// ValidateFunc checks a staged workflow file (sortie validate). root is
// the loop root the file is for.
type ValidateFunc func(root, workflowFile string) error

// Hub writes one loop root per (project, workflow) of the hub config under
// <data>/hub/loops/<owner>/<name>/<workflow>/. A changed root is staged in
// <root>/.jigz.next and applied by Apply, which the daemon calls when the
// loop is idle or stopped. A root with nothing applied yet gets its files
// at once. Workspaces and the engine database stay in the root.
type Hub struct {
	dir         string
	runnerLabel string
	idle        time.Duration
	validate    ValidateFunc

	mu  sync.Mutex
	cfg hubproto.ConfigResponse
}

// NewHub returns a Hub writing under data (dirs.Data()). runnerLabel is
// runner:<name> from enrollment. idleTimeout is the machine's idle time,
// 15m when zero. validate may be nil, which only parses the front matter.
func NewHub(data, runnerLabel string, idleTimeout time.Duration, validate ValidateFunc) (*Hub, error) {
	if !runnerLabelRe.MatchString(runnerLabel) {
		return nil, fmt.Errorf("runner label %q does not match %s", runnerLabel, runnerLabelRe)
	}
	if idleTimeout <= 0 {
		idleTimeout = defaultIdleTimeout
	}
	return &Hub{dir: filepath.Join(data, "hub", "loops"), runnerLabel: runnerLabel, idle: idleTimeout, validate: validate}, nil
}

// Report is the outcome of Sync.
type Report struct {
	// Pending loops have new files staged; Hash is the staged hash. The
	// daemon calls Apply when each is idle, then restarts it.
	Pending []Loop
	// Removed loops have a root on disk and are not in the config. The
	// daemon drains and stops them; the roots are kept.
	Removed []Loop
	// Invalid loops were not written; a loop already applied keeps its
	// files.
	Invalid []Invalid
}

type Invalid struct {
	Project, Workflow string
	Err               error
}

func (i Invalid) Error() string {
	return fmt.Sprintf("%s %s: %v", i.Project, i.Workflow, i.Err)
}

// Sync takes a new hub config. A config that fails validation, carries a
// file whose content does not match its sha256, or names a path outside
// its root is refused as a whole and nothing changes.
func (h *Hub) Sync(cfg hubproto.ConfigResponse) (Report, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	files, err := checkConfig(cfg)
	if err != nil {
		return Report{}, fmt.Errorf("hub config: %w", err)
	}
	h.cfg = cfg
	var rep Report
	want := map[string]bool{}
	for _, p := range cfg.Projects {
		for _, wf := range p.Workflows {
			root := h.root(p.Repo, wf)
			want[root] = true
			l := Loop{Project: p.Repo, Workflow: wf, Root: root}
			bad := func(err error) { rep.Invalid = append(rep.Invalid, Invalid{p.Repo, wf, err}) }
			d, err := h.desired(cfg.Member.Login, p, wf, files)
			if err != nil {
				bad(err)
				continue
			}
			applied, err := appliedHash(root)
			if err != nil {
				bad(err)
				continue
			}
			if d.hash() == applied {
				if err := os.RemoveAll(filepath.Join(root, nextDir)); err != nil {
					bad(err)
				}
				continue
			}
			if err := h.stage(root, wf, d); err != nil {
				bad(err)
				continue
			}
			if applied == "" {
				if err := apply(root); err != nil {
					bad(err)
				}
				continue
			}
			l.Hash = d.hash()
			rep.Pending = append(rep.Pending, l)
		}
	}
	removed, err := h.removed(want)
	rep.Removed = removed
	return rep, err
}

// Loops returns the loops of the last synced config that have files
// applied, with the hash of those files.
func (h *Hub) Loops(context.Context) ([]Loop, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var loops []Loop
	for _, p := range h.cfg.Projects {
		for _, wf := range p.Workflows {
			root := h.root(p.Repo, wf)
			hash, err := appliedHash(root)
			if err != nil {
				return nil, err
			}
			if hash != "" {
				loops = append(loops, Loop{Project: p.Repo, Workflow: wf, Root: root, Hash: hash})
			}
		}
	}
	return loops, nil
}

// Apply moves the staged files of l into its root and returns l with the
// hash of the applied files. With nothing staged it changes nothing. The
// caller makes sure the loop is idle or stopped.
func (h *Hub) Apply(l Loop) (Loop, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := checkRoot(l.Project, l.Workflow); err != nil {
		return l, err
	}
	l.Root = h.root(l.Project, l.Workflow)
	if _, err := os.Stat(filepath.Join(l.Root, nextDir)); err == nil {
		if err := apply(l.Root); err != nil {
			return l, err
		}
	}
	hash, err := appliedHash(l.Root)
	l.Hash = hash
	return l, err
}

func (h *Hub) root(project, workflow string) string {
	return filepath.Join(h.dir, filepath.FromSlash(project), workflow)
}

// removed lists the applied roots on disk that are not in want and drops
// their staged files.
func (h *Hub) removed(want map[string]bool) ([]Loop, error) {
	roots, err := filepath.Glob(filepath.Join(h.dir, "*", "*", "*"))
	if err != nil {
		return nil, err
	}
	var out []Loop
	for _, root := range roots {
		if want[root] || !IsHubRoot(root) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, nextDir)); err != nil {
			return out, err
		}
		hash, err := appliedHash(root)
		if err != nil {
			return out, err
		}
		rel, _ := filepath.Rel(h.dir, root)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		out = append(out, Loop{Project: parts[0] + "/" + parts[1], Workflow: parts[2], Root: root, Hash: hash})
	}
	return out, nil
}

// loopFiles is the content of one root: config.yaml and the files under
// workflows/, keyed by path relative to .jigz.
type loopFiles map[string][]byte

func (d loopFiles) hash() string { return hashFiles(d) }

// desired builds the files of one loop: the hub config's files except the
// other workflows' WORKFLOW.*.md, and a config.yaml with no token.
func (h *Hub) desired(login string, p hubproto.ConfigProject, wf string, files map[string][]byte) (loopFiles, error) {
	own := WorkflowFile(wf)
	text, ok := files[own]
	if !ok {
		return nil, fmt.Errorf("no %s in the hub config files", own)
	}
	w, err := plan.ParseWorkflow(string(text))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", own, err)
	}
	var terms []string
	for _, t := range []string{w.QueryFilter, p.FilterAdditions, "label:" + h.runnerLabel} {
		if t = strings.TrimSpace(t); t != "" {
			terms = append(terms, t)
		}
	}
	type sleep struct {
		Enabled      bool   `yaml:"enabled"`
		IdleTimeout  string `yaml:"idle_timeout"`
		WakeInterval string `yaml:"wake_interval"`
	}
	cfg, err := yaml.Marshal(struct {
		Repo     string            `yaml:"repo"`
		Assignee string            `yaml:"assignee"`
		Filters  map[string]string `yaml:"filters"`
		Sleep    sleep             `yaml:"sleep"`
	}{p.Repo, login, map[string]string{wf: strings.Join(terms, " ")}, sleep{true, h.idle.String(), wakeInterval}})
	if err != nil {
		return nil, err
	}
	d := loopFiles{"config.yaml": cfg}
	for name, b := range files {
		if !strings.Contains(name, "/") && strings.HasPrefix(name, "WORKFLOW.") && name != own {
			continue
		}
		d["workflows/"+name] = b
	}
	return d, nil
}

// stage writes d to <root>/.jigz.next through a temporary directory and
// validates the workflow there, so .jigz.next only ever holds a complete,
// valid set. A set staged before is dropped first, also when d is invalid.
func (h *Hub) stage(root, wf string, d loopFiles) error {
	next := filepath.Join(root, nextDir)
	tmp := next + ".tmp"
	for _, dir := range []string{next, tmp} {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	var manifest []hubproto.FileHash
	for rel, b := range d {
		if name, ok := strings.CutPrefix(rel, "workflows/"); ok {
			manifest = append(manifest, hubproto.FileHash{Path: name, SHA256: sha256Hex(b)})
		}
		if err := writeFile(filepath.Join(tmp, filepath.FromSlash(rel)), b); err != nil {
			return err
		}
	}
	mb, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(tmp, manifestFile), mb); err != nil {
		return err
	}
	if h.validate != nil {
		if err := h.validate(root, filepath.Join(tmp, "workflows", WorkflowFile(wf))); err != nil {
			_ = os.RemoveAll(tmp)
			return fmt.Errorf("%s: %w", WorkflowFile(wf), err)
		}
	}
	return os.Rename(tmp, next)
}

// apply renames each staged file over the applied one. Files are replaced
// one by one, not as a directory, because the engine database lives in
// .jigz/workflows/. config.yaml and the manifest go last.
func apply(root string) error {
	next := filepath.Join(root, nextDir)
	jigz := filepath.Join(root, ".jigz")
	files, err := readManifest(filepath.Join(next, manifestFile))
	if err != nil {
		return err
	}
	old, err := readManifest(filepath.Join(jigz, manifestFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	keep := map[string]bool{}
	for _, f := range files {
		keep[f.Path] = true
		dst := filepath.Join(jigz, "workflows", filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(next, "workflows", filepath.FromSlash(f.Path)), dst); err != nil {
			return err
		}
	}
	for _, f := range old {
		if !keep[f.Path] && checkPath(f.Path) == nil {
			if err := os.Remove(filepath.Join(jigz, "workflows", filepath.FromSlash(f.Path))); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	for _, name := range []string{"config.yaml", manifestFile} {
		if err := os.Rename(filepath.Join(next, name), filepath.Join(jigz, name)); err != nil {
			return err
		}
	}
	return os.RemoveAll(next)
}

// appliedHash hashes the applied config.yaml and the files the manifest
// lists, "" when nothing is applied.
func appliedHash(root string) (string, error) {
	jigz := filepath.Join(root, ".jigz")
	files, err := readManifest(filepath.Join(jigz, manifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	d := loopFiles{}
	if b, err := os.ReadFile(filepath.Join(jigz, "config.yaml")); err == nil {
		d["config.yaml"] = b
	}
	for _, f := range files {
		if checkPath(f.Path) != nil {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(jigz, "workflows", filepath.FromSlash(f.Path))); err == nil {
			d["workflows/"+f.Path] = b
		}
	}
	return d.hash(), nil
}

func readManifest(p string) ([]hubproto.FileHash, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var files []hubproto.FileHash
	if err := json.Unmarshal(b, &files); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return files, nil
}

func writeFile(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// checkConfig validates cfg and returns its files by path.
func checkConfig(cfg hubproto.ConfigResponse) (map[string][]byte, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, f := range cfg.Files {
		if err := checkPath(f.Path); err != nil {
			return nil, err
		}
		if _, dup := files[f.Path]; dup {
			return nil, fmt.Errorf("file %q listed twice", f.Path)
		}
		if sha256Hex([]byte(f.Content)) != f.SHA256 {
			return nil, fmt.Errorf("file %q: content does not match sha256", f.Path)
		}
		files[f.Path] = []byte(f.Content)
	}
	seen := map[string]bool{}
	for _, p := range cfg.Projects {
		for _, wf := range p.Workflows {
			if err := checkRoot(p.Repo, wf); err != nil {
				return nil, err
			}
		}
		if seen[p.Repo] {
			return nil, fmt.Errorf("project %q listed twice", p.Repo)
		}
		seen[p.Repo] = true
	}
	return files, nil
}

// checkPath refuses a served file name that is not a clean relative path
// inside .jigz/workflows/.
func checkPath(p string) error {
	if p == "" || strings.Contains(p, `\`) || path.Clean(p) != p || !filepath.IsLocal(filepath.FromSlash(p)) {
		return fmt.Errorf("file path %q is not a relative path inside the workflows directory", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("file path %q is not a relative path inside the workflows directory", p)
		}
	}
	return nil
}

// checkRoot refuses a project or workflow that would place a root outside
// <data>/hub/loops/<owner>/<name>/<workflow>.
func checkRoot(project, workflow string) error {
	owner, name, ok := strings.Cut(project, "/")
	for _, seg := range []string{owner, name, workflow} {
		if !ok || seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, `/\`) {
			return fmt.Errorf("project %q workflow %q does not name a loop root", project, workflow)
		}
	}
	return nil
}
