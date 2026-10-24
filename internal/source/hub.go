package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
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
	// manifestFile records the files applied to a root: the sha256 of
	// config.yaml and of each hub file, by path relative to
	// .jigz/workflows/. Apply writes it before it moves any file, so a
	// root whose files do not match it is half-applied. It also marks the
	// root as written by the hub.
	manifestFile       = "hub-files.json"
	nextDir            = ".jigz.next"
	defaultIdleTimeout = 15 * time.Minute
	wakeInterval       = "24h"
)

var (
	runnerLabelRe  = regexp.MustCompile(`^runner:[a-z0-9][a-z0-9-]*$`)
	workflowRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	workflowFileRe = regexp.MustCompile(`^WORKFLOW\.[A-Za-z0-9][A-Za-z0-9_-]*\.md$`)
	repoSegRe      = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	promptSegRe    = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)
)

// rename is os.Rename; tests replace it to interrupt Apply.
var rename = os.Rename

type manifest struct {
	Config string              `json:"config_sha256"`
	Files  []hubproto.FileHash `json:"files"`
}

// IsHubRoot reports whether root holds files written by Hub. Shipped
// workflows must never be installed into such a root.
func IsHubRoot(root string) bool {
	_, err := os.Lstat(filepath.Join(root, ".jigz", manifestFile))
	return err == nil
}

// AppliedFiles lists the hub files applied to root, for the status
// document.
func AppliedFiles(root string) ([]hubproto.FileHash, error) {
	m, err := readManifest(filepath.Join(root, ".jigz", manifestFile))
	return m.Files, err
}

// ValidateFunc checks a staged workflow file, normally with `sortie
// validate`. workflowFile lies in a staging directory that holds only the
// files the hub served. The check must run with env as its whole process
// environment (exec.Cmd.Env = env): env carries PATH, HOME, the project, a
// placeholder tracker key and a scratch workspace directory, and no token
// or other variable of the daemon. It must not run workflow hooks.
// `sortie validate` (cmd/sortie/validate.go runValidate, engine v1.28.1
// and v1.29.0) runs none: hooks run only through workspace.RunHook, which
// workspace.Prepare and orchestrator.RunReactionTriage call during
// dispatch. Its preflight creates workspace.root and a probe file in it,
// which is why env names a scratch workspace directory.
type ValidateFunc func(workflowFile string, env []string) error

// Hub writes one loop root per (project, workflow) of the hub config under
// <data>/hub/loops/<owner>/<name>/<workflow>/. A changed root is staged in
// <root>/.jigz.next and applied by Apply, which the daemon calls when the
// loop is idle or stopped. A root with nothing applied yet, or one left
// half-applied, gets its files at once. Workspaces and the engine
// database stay in the root.
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
// file whose content does not match its sha256, names a file other than
// a workflow or a prompt, or names a path outside its root is refused as
// a whole and nothing changes.
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
			bad := func(err error) { rep.Invalid = append(rep.Invalid, Invalid{p.Repo, wf, err}) }
			root, err := h.root(p.Repo, wf)
			if err != nil {
				bad(err)
				continue
			}
			want[root] = true
			d, err := h.desired(cfg.Member.Login, p, wf, files)
			if err != nil {
				bad(err)
				continue
			}
			hash, complete, err := appliedState(root)
			if err != nil {
				bad(err)
				continue
			}
			if complete && hash == d.hash() {
				if err := os.RemoveAll(filepath.Join(root, nextDir)); err != nil {
					bad(err)
				}
				continue
			}
			if err := h.stage(root, p.Repo, wf, d); err != nil {
				bad(err)
				continue
			}
			if !complete {
				if err := h.apply(root); err != nil {
					bad(err)
				}
				continue
			}
			rep.Pending = append(rep.Pending, Loop{Project: p.Repo, Workflow: wf, Root: root, Hash: d.hash()})
		}
	}
	removed, err := h.removed(want)
	rep.Removed = removed
	return rep, err
}

// Loops returns the loops of the last synced config whose root has every
// file of its manifest applied, with the hash of those files. A
// half-applied root is left out until Apply or Sync completes it.
func (h *Hub) Loops(context.Context) ([]Loop, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var loops []Loop
	for _, p := range h.cfg.Projects {
		for _, wf := range p.Workflows {
			root, err := h.root(p.Repo, wf)
			if err != nil {
				return nil, err
			}
			hash, complete, err := appliedState(root)
			if err != nil {
				return nil, err
			}
			if complete {
				loops = append(loops, Loop{Project: p.Repo, Workflow: wf, Root: root, Hash: hash})
			}
		}
	}
	return loops, nil
}

// Apply moves the staged files of l into its root and returns l with the
// hash of the applied files. With nothing staged it changes nothing. It
// can be repeated after an interrupted Apply. The caller makes sure the
// loop is idle or stopped.
func (h *Hub) Apply(l Loop) (Loop, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	root, err := h.root(l.Project, l.Workflow)
	if err != nil {
		return l, err
	}
	l.Root = root
	if _, err := os.Lstat(filepath.Join(root, nextDir)); err == nil {
		if err := h.apply(root); err != nil {
			return l, err
		}
	}
	hash, complete, err := appliedState(root)
	if err == nil && !complete {
		err = fmt.Errorf("%s %s: files do not match %s and nothing is staged; the next Sync applies them again", l.Project, l.Workflow, manifestFile)
	}
	l.Hash = hash
	return l, err
}

// root returns the loop root of (project, workflow) after checking that
// it stays inside the loops directory and passes through no symlink.
func (h *Hub) root(project, workflow string) (string, error) {
	if err := checkRoot(project, workflow); err != nil {
		return "", err
	}
	rel := filepath.Join(filepath.FromSlash(project), workflow)
	if err := noSymlink(h.dir, rel); err != nil {
		return "", err
	}
	return filepath.Join(h.dir, rel), nil
}

// removed lists the roots on disk with an applied manifest that are not
// in want, and drops their staged files.
func (h *Hub) removed(want map[string]bool) ([]Loop, error) {
	roots, err := filepath.Glob(filepath.Join(h.dir, "*", "*", "*"))
	if err != nil {
		return nil, err
	}
	var out []Loop
	for _, found := range roots {
		rel, err := filepath.Rel(h.dir, found)
		if err != nil {
			continue
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		project := parts[0] + "/" + parts[1]
		root, err := h.root(project, parts[2])
		if err != nil || want[root] || !IsHubRoot(root) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, nextDir)); err != nil {
			return out, err
		}
		hash, _, err := appliedState(root)
		if err != nil {
			return out, err
		}
		out = append(out, Loop{Project: project, Workflow: parts[2], Root: root, Hash: hash})
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
		if !strings.Contains(name, "/") && name != own {
			continue
		}
		d["workflows/"+name] = b
	}
	return d, nil
}

// stage writes d to <root>/.jigz.next through a temporary directory and
// validates the workflow there, so .jigz.next only ever holds a complete,
// valid set. A set staged before is dropped first, also when d is invalid.
func (h *Hub) stage(root, repo, wf string, d loopFiles) error {
	next := filepath.Join(root, nextDir)
	tmp := next + ".tmp"
	for _, dir := range []string{next, tmp} {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	m := manifest{Config: sha256Hex(d["config.yaml"])}
	for rel, b := range d {
		if name, ok := strings.CutPrefix(rel, "workflows/"); ok {
			m.Files = append(m.Files, hubproto.FileHash{Path: name, SHA256: sha256Hex(b)})
		}
		if err := writeFile(filepath.Join(tmp, filepath.FromSlash(rel)), b); err != nil {
			return err
		}
	}
	mb, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(tmp, manifestFile), mb); err != nil {
		return err
	}
	if h.validate != nil {
		err := h.validate(filepath.Join(tmp, "workflows", WorkflowFile(wf)), validateEnv(repo, tmp))
		_ = os.RemoveAll(filepath.Join(tmp, "workspaces"))
		if err != nil {
			_ = os.RemoveAll(tmp)
			return fmt.Errorf("%s: %w", WorkflowFile(wf), err)
		}
	}
	return rename(tmp, next)
}

// validateEnv is the whole environment of a validation run.
func validateEnv(repo, staged string) []string {
	ws := filepath.Join(staged, "workspaces")
	_ = os.MkdirAll(ws, 0o755)
	env := []string{
		"SORTIE_TRACKER_PROJECT=" + repo,
		"SORTIE_TRACKER_API_KEY=validate-only",
		"JIG_WORKSPACES=" + ws,
	}
	for _, k := range []string{"PATH", "HOME"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// apply moves the staged set into the root. The manifest goes first as the
// record of what the root must hold; then each file is renamed over the
// applied one. Files are replaced one by one, not as a directory, because
// the engine database lives in .jigz/workflows/. A file already moved by
// an interrupted apply is skipped when its content matches the manifest.
func (h *Hub) apply(root string) error {
	next := filepath.Join(root, nextDir)
	m, err := readManifest(filepath.Join(next, manifestFile))
	if err != nil {
		return err
	}
	mb, err := os.ReadFile(filepath.Join(next, manifestFile))
	if err != nil {
		return err
	}
	tmp := filepath.Join(root, ".jigz", manifestFile+".tmp")
	if err := h.safeDest(root, ".jigz/"+manifestFile+".tmp"); err != nil {
		return err
	}
	if err := writeFile(tmp, mb); err != nil {
		return err
	}
	if err := h.safeDest(root, ".jigz/"+manifestFile); err != nil {
		return err
	}
	if err := rename(tmp, filepath.Join(root, ".jigz", manifestFile)); err != nil {
		return err
	}
	keep := map[string]bool{}
	items := []hubproto.FileHash{{Path: "config.yaml", SHA256: m.Config}}
	for _, f := range m.Files {
		keep[f.Path] = true
		items = append(items, hubproto.FileHash{Path: "workflows/" + f.Path, SHA256: f.SHA256})
	}
	for _, it := range items {
		src := filepath.Join(next, filepath.FromSlash(it.Path))
		dst := filepath.Join(root, ".jigz", filepath.FromSlash(it.Path))
		if err := h.safeDest(root, ".jigz/"+it.Path); err != nil {
			return err
		}
		if _, err := os.Lstat(src); err != nil {
			if b, rerr := os.ReadFile(dst); rerr == nil && sha256Hex(b) == it.SHA256 {
				continue
			}
			return fmt.Errorf("staged %s is missing and the applied copy does not match", it.Path)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := rename(src, dst); err != nil {
			return err
		}
	}
	if err := removeStale(filepath.Join(root, ".jigz", "workflows"), keep); err != nil {
		return err
	}
	return os.RemoveAll(next)
}

// removeStale deletes the workflow and prompt files the manifest no
// longer lists. Nothing else in the directory (the engine database) is
// touched.
func removeStale(dir string, keep map[string]bool) error {
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if keep[rel] || checkPath(rel) != nil {
			return nil
		}
		return os.Remove(p)
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// safeDest refuses a destination under root that is, or lies inside, a
// symlink, so a served file is never written through one.
func (h *Hub) safeDest(root, rel string) error {
	r, err := filepath.Rel(h.dir, root)
	if err != nil {
		return err
	}
	return noSymlink(h.dir, filepath.Join(r, filepath.FromSlash(rel)))
}

// noSymlink checks every existing component of base/rel with Lstat and
// fails on a symlink or a non-directory parent.
func noSymlink(base, rel string) error {
	p := base
	segs := strings.Split(filepath.ToSlash(rel), "/")
	for i, seg := range segs {
		p = filepath.Join(p, seg)
		st, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; hub files are not written through one", p)
		}
		if i < len(segs)-1 && !st.IsDir() {
			return fmt.Errorf("%s is not a directory", p)
		}
	}
	return nil
}

// appliedState hashes the applied config.yaml and the files the manifest
// lists. hash is "" when no manifest exists. complete reports that every
// file matches the manifest; a root with no manifest is not complete.
func appliedState(root string) (hash string, complete bool, err error) {
	jigz := filepath.Join(root, ".jigz")
	m, err := readManifest(filepath.Join(jigz, manifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	complete = true
	d := loopFiles{}
	items := []hubproto.FileHash{{Path: "config.yaml", SHA256: m.Config}}
	for _, f := range m.Files {
		if checkPath(f.Path) != nil {
			complete = false
			continue
		}
		items = append(items, hubproto.FileHash{Path: "workflows/" + f.Path, SHA256: f.SHA256})
	}
	for _, it := range items {
		b, err := os.ReadFile(filepath.Join(jigz, filepath.FromSlash(it.Path)))
		if err != nil || sha256Hex(b) != it.SHA256 {
			complete = false
		}
		if err == nil {
			d[it.Path] = b
		}
	}
	return d.hash(), complete, nil
}

func readManifest(p string) (manifest, error) {
	var m manifest
	b, err := os.ReadFile(p)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s: %w", p, err)
	}
	return m, nil
}

func writeFile(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// checkConfig validates cfg and returns its files by path. Paths and
// roots are compared without case, because a case-insensitive file
// system maps acme/W and acme/w to one directory.
func checkConfig(cfg hubproto.ConfigResponse) (map[string][]byte, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	seenFile := map[string]bool{}
	for _, f := range cfg.Files {
		if err := checkPath(f.Path); err != nil {
			return nil, err
		}
		if seenFile[strings.ToLower(f.Path)] {
			return nil, fmt.Errorf("file %q listed twice", f.Path)
		}
		seenFile[strings.ToLower(f.Path)] = true
		if sha256Hex([]byte(f.Content)) != f.SHA256 {
			return nil, fmt.Errorf("file %q: content does not match sha256", f.Path)
		}
		files[f.Path] = []byte(f.Content)
	}
	seenRoot := map[string]bool{}
	for _, p := range cfg.Projects {
		for _, wf := range p.Workflows {
			if err := checkRoot(p.Repo, wf); err != nil {
				return nil, err
			}
			key := strings.ToLower(p.Repo + "/" + wf)
			if seenRoot[key] {
				return nil, fmt.Errorf("project %q workflow %q listed twice", p.Repo, wf)
			}
			seenRoot[key] = true
		}
	}
	return files, nil
}

// checkPath allows only the names a loop reads from the hub:
// WORKFLOW.<name>.md at the top of .jigz/workflows/ and files under
// prompts/. Dotfiles, jigz-* names and anything with .db in it are
// refused, so a served file can never replace the engine database.
func checkPath(p string) error {
	bad := fmt.Errorf("file path %q is not WORKFLOW.<name>.md or a file under prompts/", p)
	segs := strings.Split(p, "/")
	if len(segs) == 1 {
		if !workflowFileRe.MatchString(p) {
			return bad
		}
		return nil
	}
	if segs[0] != "prompts" {
		return bad
	}
	for _, s := range segs[1:] {
		if !promptSegRe.MatchString(s) || strings.HasPrefix(s, "jigz-") || strings.Contains(s, ".db") {
			return bad
		}
	}
	return nil
}

// checkRoot refuses a project or workflow that would place a root outside
// <data>/hub/loops/<owner>/<name>/<workflow>.
func checkRoot(project, workflow string) error {
	owner, name, ok := strings.Cut(project, "/")
	for _, seg := range []string{owner, name} {
		if !ok || !repoSegRe.MatchString(seg) || seg == "." || seg == ".." {
			return fmt.Errorf("project %q does not name a loop root", project)
		}
	}
	if !workflowRe.MatchString(workflow) {
		return fmt.Errorf("workflow %q does not name a loop root", workflow)
	}
	return nil
}
