package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/hubproto"
)

const testToken = "ghp_secret_tracker_token"

func wfText(filter string) string {
	return "---\ntracker:\n  kind: github\n  query_filter: \"" + filter + "\"\n---\nprompt\n"
}

func file(p, content string) hubproto.ConfigFile {
	return hubproto.ConfigFile{Path: p, SHA256: sha256Hex([]byte(content)), Content: content}
}

func testConfig(projects ...hubproto.ConfigProject) hubproto.ConfigResponse {
	return hubproto.ConfigResponse{
		Member:   hubproto.Member{Login: "octo"},
		Projects: projects,
		Files: []hubproto.ConfigFile{
			file("WORKFLOW.issues.md", wfText("label:agent:plan -label:needs-human")),
			file("WORKFLOW.prs.md", wfText("label:agent:review")),
			file("prompts/plan.md", "plan prompt"),
		},
		Labels: []hubproto.Label{},
	}
}

func project(repo, additions string, workflows ...string) hubproto.ConfigProject {
	return hubproto.ConfigProject{Provider: "github", Repo: repo, Workflows: workflows, FilterAdditions: additions}
}

func newTestHub(t *testing.T, validate ValidateFunc) (*Hub, string) {
	t.Helper()
	data := t.TempDir()
	h, err := NewHub(data, "runner:ci-1", 0, validate)
	if err != nil {
		t.Fatal(err)
	}
	return h, data
}

func mustSync(t *testing.T, h *Hub, cfg hubproto.ConfigResponse) Report {
	t.Helper()
	rep, err := h.Sync(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func loops(t *testing.T, h *Hub) map[string]Loop {
	t.Helper()
	ls, err := h.Loops(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Loop{}
	for _, l := range ls {
		out[l.Project+" "+l.Workflow] = l
	}
	return out
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestHubWritesRootsWithRunnerLabelInEveryFilter(t *testing.T) {
	t.Setenv("JIG_ASSIGNEE", "")
	os.Unsetenv("JIG_ASSIGNEE")
	h, data := newTestHub(t, nil)
	rep := mustSync(t, h, testConfig(
		project("acme/widgets", "-label:backlog", "issues", "prs"),
		project("acme/gadgets", "", "issues"),
	))
	if len(rep.Pending)+len(rep.Removed)+len(rep.Invalid) != 0 {
		t.Fatalf("first sync report = %+v, want everything applied", rep)
	}
	got := loops(t, h)
	if len(got) != 3 {
		t.Fatalf("Loops = %v, want 3", got)
	}
	for _, l := range got {
		want := filepath.Join(data, "hub", "loops", filepath.FromSlash(l.Project), l.Workflow)
		if l.Root != want || l.Hash == "" {
			t.Errorf("loop %+v, want root %s and a hash", l, want)
		}
		cfg, err := config.Load(l.Root)
		if err != nil {
			t.Fatal(err)
		}
		filter := cfg.FilterFor(l.Workflow)
		if !strings.Contains(filter, "label:runner:ci-1") {
			t.Errorf("%s %s filter %q has no runner label", l.Project, l.Workflow, filter)
		}
		if cfg.Repo != l.Project || !cfg.Sleep.Enabled || cfg.Sleep.IdleTimeout.String() != "15m0s" {
			t.Errorf("config = repo %q sleep %+v", cfg.Repo, cfg.Sleep)
		}
		if _, err := os.Stat(filepath.Join(l.Root, ".jigz", "workflows", "prompts", "plan.md")); err != nil {
			t.Errorf("prompt not written: %v", err)
		}
	}
	w := got["acme/widgets issues"]
	cfg, _ := config.Load(w.Root)
	if f := cfg.FilterFor("issues"); f != "label:agent:plan -label:needs-human -label:backlog label:runner:ci-1 assignee:@me" {
		t.Errorf("widgets issues filter = %q", f)
	}
	if _, err := os.Stat(filepath.Join(w.Root, ".jigz", "workflows", "WORKFLOW.prs.md")); !os.IsNotExist(err) {
		t.Errorf("issues root holds the prs workflow: %v", err)
	}
	files, err := AppliedFiles(w.Root)
	if err != nil || len(files) != 2 {
		t.Errorf("AppliedFiles = %v, %v; want the issues workflow and the prompt", files, err)
	}
}

func TestHubStagesChangeAndAppliesOnlyWhenTold(t *testing.T) {
	h, _ := newTestHub(t, nil)
	mustSync(t, h, testConfig(project("acme/widgets", "", "issues")))
	before := loops(t, h)["acme/widgets issues"]
	db := filepath.Join(before.Root, ".jigz", "workflows", "jigz-issues.db")
	os.WriteFile(db, []byte("engine state"), 0o644)
	ws := filepath.Join(before.Root, ".jigz", "workspaces", "issues", "7")
	os.MkdirAll(ws, 0o755)

	// Unchanged config: nothing staged.
	if rep := mustSync(t, h, testConfig(project("acme/widgets", "", "issues"))); len(rep.Pending) != 0 {
		t.Fatalf("unchanged config staged %+v", rep.Pending)
	}

	cfg := testConfig(project("acme/widgets", "-label:backlog", "issues"))
	cfg.Files[2] = file("prompts/build.md", "build prompt")
	rep := mustSync(t, h, cfg)
	if len(rep.Pending) != 1 || rep.Pending[0].Hash == before.Hash {
		t.Fatalf("Pending = %+v, want the issues loop with a new hash", rep.Pending)
	}
	next := filepath.Join(before.Root, ".jigz.next")
	if _, err := os.Stat(filepath.Join(next, "config.yaml")); err != nil {
		t.Fatalf("no staged config: %v", err)
	}
	if now := loops(t, h)["acme/widgets issues"]; now.Hash != before.Hash {
		t.Fatal("applied files changed before Apply")
	}
	if strings.Contains(readFile(t, filepath.Join(before.Root, ".jigz", "config.yaml")), "backlog") {
		t.Fatal("config.yaml changed before Apply")
	}

	after, err := h.Apply(rep.Pending[0])
	if err != nil {
		t.Fatal(err)
	}
	if after.Hash != rep.Pending[0].Hash || after.Hash == before.Hash {
		t.Errorf("Apply hash = %s, want staged %s", after.Hash, rep.Pending[0].Hash)
	}
	if _, err := os.Stat(next); !os.IsNotExist(err) {
		t.Errorf(".jigz.next left after Apply: %v", err)
	}
	if !strings.Contains(readFile(t, filepath.Join(before.Root, ".jigz", "config.yaml")), "-label:backlog label:runner:ci-1") {
		t.Error("config.yaml not swapped")
	}
	if _, err := os.Stat(filepath.Join(before.Root, ".jigz", "workflows", "prompts", "plan.md")); !os.IsNotExist(err) {
		t.Errorf("prompt the hub stopped serving is still applied: %v", err)
	}
	if readFile(t, filepath.Join(before.Root, ".jigz", "workflows", "prompts", "build.md")) != "build prompt" {
		t.Error("new prompt not applied")
	}
	if readFile(t, db) != "engine state" {
		t.Error("engine database lost in the swap")
	}
	if _, err := os.Stat(ws); err != nil {
		t.Errorf("workspace lost in the swap: %v", err)
	}
	if again, err := h.Apply(after); err != nil || again.Hash != after.Hash {
		t.Errorf("Apply with nothing staged = %+v, %v", again, err)
	}
	if rep := mustSync(t, h, cfg); len(rep.Pending) != 0 {
		t.Errorf("applied config staged again: %+v", rep.Pending)
	}
}

func TestHubKeepsInvalidWorkflowOut(t *testing.T) {
	validate := func(wf string, env []string) error {
		if filepath.Base(filepath.Dir(filepath.Dir(wf))) != ".jigz.next.tmp" {
			t.Errorf("validated %s, not a staged file", wf)
		}
		if strings.Contains(readFile(t, wf), "BROKEN") {
			return errors.New("tracker.kind: unknown")
		}
		return nil
	}
	h, _ := newTestHub(t, validate)
	good := testConfig(project("acme/widgets", "", "issues"), project("acme/gadgets", "", "prs"))
	bad := testConfig(project("acme/widgets", "", "issues"), project("acme/gadgets", "", "prs"))
	bad.Files[0] = file("WORKFLOW.issues.md", wfText("BROKEN"))

	// New loop with an invalid workflow: nothing written, reported.
	rep := mustSync(t, h, bad)
	if len(rep.Invalid) != 1 || rep.Invalid[0].Workflow != "issues" || !strings.Contains(rep.Invalid[0].Error(), "tracker.kind") {
		t.Fatalf("Invalid = %v", rep.Invalid)
	}
	got := loops(t, h)
	if _, ok := got["acme/widgets issues"]; ok || len(got) != 1 {
		t.Fatalf("Loops = %v, want only gadgets prs", got)
	}

	// Applied loop, then an invalid change: old files stay, nothing staged.
	mustSync(t, h, good)
	before := loops(t, h)["acme/widgets issues"]
	rep = mustSync(t, h, bad)
	if len(rep.Invalid) != 1 || len(rep.Pending) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if after := loops(t, h)["acme/widgets issues"]; after.Hash != before.Hash {
		t.Error("invalid workflow changed the applied files")
	}
	if _, err := os.Stat(filepath.Join(before.Root, ".jigz.next")); !os.IsNotExist(err) {
		t.Errorf("invalid workflow staged: %v", err)
	}
	if strings.Contains(readFile(t, filepath.Join(before.Root, ".jigz", "workflows", "WORKFLOW.issues.md")), "BROKEN") {
		t.Error("invalid workflow written")
	}

	// A workflow the config does not serve, or with no front matter.
	missing := testConfig(project("acme/widgets", "", "triage"))
	if rep := mustSync(t, h, missing); len(rep.Invalid) != 1 {
		t.Errorf("missing workflow file: Invalid = %v", rep.Invalid)
	}
	noFront := testConfig(project("acme/widgets", "", "issues"))
	noFront.Files[0] = file("WORKFLOW.issues.md", "no front matter")
	if rep := mustSync(t, h, noFront); len(rep.Invalid) != 1 {
		t.Errorf("no front matter: Invalid = %v", rep.Invalid)
	}
}

func TestHubReportsRemovedLoopsAndKeepsRoots(t *testing.T) {
	h, _ := newTestHub(t, nil)
	mustSync(t, h, testConfig(project("acme/widgets", "", "issues", "prs"), project("acme/gadgets", "", "issues")))
	rep := mustSync(t, h, testConfig(project("acme/widgets", "", "issues")))
	removed := map[string]Loop{}
	for _, l := range rep.Removed {
		removed[l.Project+" "+l.Workflow] = l
	}
	if len(removed) != 2 || removed["acme/widgets prs"].Root == "" || removed["acme/gadgets issues"].Hash == "" {
		t.Fatalf("Removed = %+v, want widgets prs and gadgets issues", rep.Removed)
	}
	for _, l := range removed {
		if _, err := os.Stat(filepath.Join(l.Root, ".jigz", "config.yaml")); err != nil {
			t.Errorf("removed root %s deleted: %v", l.Root, err)
		}
	}
	if got := loops(t, h); len(got) != 1 {
		t.Errorf("Loops = %v, want only widgets issues", got)
	}
}

func TestHubRefusesPathTraversal(t *testing.T) {
	for name, mutate := range map[string]func(*hubproto.ConfigResponse){
		"dotdot file":   func(c *hubproto.ConfigResponse) { c.Files = append(c.Files, file("../../evil.md", "x")) },
		"nested dotdot": func(c *hubproto.ConfigResponse) { c.Files = append(c.Files, file("prompts/../../evil.md", "x")) },
		"absolute file": func(c *hubproto.ConfigResponse) { c.Files = append(c.Files, file("/etc/evil", "x")) },
		"dotdot repo": func(c *hubproto.ConfigResponse) {
			c.Projects = []hubproto.ConfigProject{project("../..", "", "issues")}
		},
		"dotdot repo name": func(c *hubproto.ConfigResponse) {
			c.Projects = []hubproto.ConfigProject{project("acme/..", "", "issues")}
		},
		"wrong sha256": func(c *hubproto.ConfigResponse) { c.Files[2].Content = "changed" },
	} {
		t.Run(name, func(t *testing.T) {
			h, data := newTestHub(t, nil)
			cfg := testConfig(project("acme/widgets", "", "issues"))
			mutate(&cfg)
			if _, err := h.Sync(cfg); err == nil {
				t.Fatal("Sync accepted the config")
			}
			if entries, _ := os.ReadDir(data); len(entries) != 0 {
				t.Errorf("refused config wrote %v", entries)
			}
		})
	}
	for _, p := range []string{"..", "a/../b", "a//b", "./a", `a\b`, ""} {
		if checkPath(p) == nil {
			t.Errorf("checkPath(%q) accepted", p)
		}
	}
	h, _ := newTestHub(t, nil)
	if _, err := h.Apply(Loop{Project: "../x", Workflow: "issues"}); err == nil {
		t.Error("Apply accepted a project outside the loops directory")
	}
}

func TestHubWritesNoToken(t *testing.T) {
	t.Setenv("JIG_TOKEN", testToken)
	t.Setenv("GITHUB_TOKEN", testToken)
	h, data := newTestHub(t, nil)
	mustSync(t, h, testConfig(project("acme/widgets", "", "issues")))
	err := filepath.WalkDir(data, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err == nil && strings.Contains(string(b), testToken) {
			t.Errorf("%s holds the token", p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	l := loops(t, h)["acme/widgets issues"]
	if strings.Contains(readFile(t, filepath.Join(l.Root, ".jigz", "config.yaml")), "token") {
		t.Error("config.yaml has a token key")
	}
}

func TestNewHubRefusesBadRunnerLabel(t *testing.T) {
	for _, l := range []string{"", "ci-1", "runner:", "runner:CI 1"} {
		if _, err := NewHub(t.TempDir(), l, 0, nil); err == nil {
			t.Errorf("NewHub(%q) accepted", l)
		}
	}
}

func TestHubApplyIsReplayTolerantAfterInterruption(t *testing.T) {
	v1 := testConfig(project("acme/widgets", "", "issues"))
	v2 := testConfig(project("acme/widgets", "-label:backlog", "issues"))
	v2.Files[0] = file("WORKFLOW.issues.md", wfText("label:agent:build"))
	v2.Files[2] = file("prompts/plan.md", "new plan prompt")
	// A full apply renames the manifest, config.yaml, the workflow and the
	// prompt.
	for _, completeBy := range []string{"Apply", "Sync"} {
		for n := 0; n < 4; n++ {
			t.Run(fmt.Sprintf("%s after %d renames", completeBy, n), func(t *testing.T) {
				h, _ := newTestHub(t, nil)
				mustSync(t, h, v1)
				old := loops(t, h)["acme/widgets issues"]
				rep := mustSync(t, h, v2)
				if len(rep.Pending) != 1 {
					t.Fatalf("Pending = %+v", rep.Pending)
				}
				calls := 0
				rename = func(a, b string) error {
					if calls++; calls > n {
						return errors.New("interrupted")
					}
					return os.Rename(a, b)
				}
				_, err := h.Apply(rep.Pending[0])
				rename = os.Rename
				if err == nil {
					t.Fatal("interrupted Apply reported success")
				}
				if l, ok := loops(t, h)["acme/widgets issues"]; ok && l.Hash != old.Hash {
					t.Fatalf("Loops served a half-applied root with hash %s", l.Hash)
				}
				if n > 0 {
					if _, ok := loops(t, h)["acme/widgets issues"]; ok {
						t.Fatal("Loops served a root whose manifest is ahead of its files")
					}
				}
				var got Loop
				if completeBy == "Apply" {
					if got, err = h.Apply(rep.Pending[0]); err != nil {
						t.Fatal(err)
					}
				} else {
					// With no rename done the root is still complete on
					// the old files, so Sync stages it again instead.
					again := mustSync(t, h, v2)
					if want := map[bool]int{true: 1, false: 0}[n == 0]; len(again.Pending) != want || len(again.Invalid) != 0 {
						t.Fatalf("Sync after interruption = %+v, want %d pending", again, want)
					}
					if n == 0 {
						if _, err := h.Apply(again.Pending[0]); err != nil {
							t.Fatal(err)
						}
					}
					got = loops(t, h)["acme/widgets issues"]
				}
				if got.Hash != rep.Pending[0].Hash {
					t.Errorf("hash after completion = %s, want %s", got.Hash, rep.Pending[0].Hash)
				}
				if l := loops(t, h)["acme/widgets issues"]; l.Hash != rep.Pending[0].Hash {
					t.Errorf("Loops hash = %s, want %s", l.Hash, rep.Pending[0].Hash)
				}
			})
		}
	}
}

func TestHubRefusesFilesOutsideWorkflowsAndPrompts(t *testing.T) {
	for _, p := range []string{
		"jigz-issues.db", "prompts/jigz-issues.db", "prompts/x.db-wal", "prompts/x.db-shm", "x.db",
		".env", "prompts/.hidden", "notes.md", "WORKFLOW.issues.txt", "other/plan.md", "prompts",
	} {
		if checkPath(p) == nil {
			t.Errorf("checkPath(%q) accepted", p)
		}
		h, data := newTestHub(t, nil)
		cfg := testConfig(project("acme/widgets", "", "issues"))
		cfg.Files = append(cfg.Files, file(p, "x"))
		if _, err := h.Sync(cfg); err == nil {
			t.Errorf("Sync accepted served file %q", p)
		}
		if entries, _ := os.ReadDir(data); len(entries) != 0 {
			t.Errorf("refused %q wrote %v", p, entries)
		}
	}
	for _, p := range []string{"WORKFLOW.issues.md", "prompts/plan.md", "prompts/sub/part.md"} {
		if err := checkPath(p); err != nil {
			t.Errorf("checkPath(%q) = %v", p, err)
		}
	}
}

func TestHubDoesNotWriteThroughSymlinks(t *testing.T) {
	v2 := testConfig(project("acme/widgets", "", "issues"))
	v2.Files[2] = file("prompts/plan.md", "new plan prompt")
	for name, plant := range map[string]func(root, outside string){
		"symlinked prompts dir": func(root, outside string) {
			dir := filepath.Join(root, ".jigz", "workflows", "prompts")
			os.RemoveAll(dir)
			os.Symlink(outside, dir)
		},
		"symlinked prompt file": func(root, outside string) {
			f := filepath.Join(root, ".jigz", "workflows", "prompts", "plan.md")
			os.Remove(f)
			os.Symlink(filepath.Join(outside, "plan.md"), f)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := newTestHub(t, nil)
			mustSync(t, h, testConfig(project("acme/widgets", "", "issues")))
			root := loops(t, h)["acme/widgets issues"].Root
			outside := t.TempDir()
			os.WriteFile(filepath.Join(outside, "plan.md"), []byte("outside"), 0o644)
			plant(root, outside)
			rep, err := h.Sync(v2)
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Pending) == 1 {
				_, err = h.Apply(rep.Pending[0])
			}
			if err == nil && len(rep.Invalid) == 0 {
				t.Error("files written through a symlink without an error")
			}
			if got := readFile(t, filepath.Join(outside, "plan.md")); got != "outside" {
				t.Errorf("file outside the root changed to %q", got)
			}
		})
	}
}

func TestHubRefusesCaseInsensitiveDuplicateRoots(t *testing.T) {
	h, data := newTestHub(t, nil)
	if _, err := h.Sync(testConfig(project("acme/W", "", "issues"), project("acme/w", "", "issues"))); err == nil {
		t.Error("Sync accepted acme/W and acme/w")
	}
	if _, err := h.Sync(testConfig(project("acme/w", "", "issues", "Issues"))); err == nil {
		t.Error("Sync accepted issues and Issues")
	}
	if entries, _ := os.ReadDir(data); len(entries) != 0 {
		t.Errorf("refused config wrote %v", entries)
	}
}

func TestHubValidatesWithMinimalEnv(t *testing.T) {
	t.Setenv("JIG_TOKEN", testToken)
	t.Setenv("GITHUB_TOKEN", testToken)
	t.Setenv("JIG_CANARY", "canary")
	var got []string
	var staged string
	h, _ := newTestHub(t, func(wf string, env []string) error {
		got, staged = env, wf
		return nil
	})
	mustSync(t, h, testConfig(project("acme/widgets", "", "issues")))
	if got == nil {
		t.Fatal("validator not called")
	}
	allowed := map[string]bool{"PATH": true, "HOME": true, "SORTIE_TRACKER_PROJECT": true, "SORTIE_TRACKER_API_KEY": true, "JIG_WORKSPACES": true}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		if !allowed[k] || strings.Contains(v, testToken) || strings.Contains(v, "canary") {
			t.Errorf("validator env carries %q", kv)
		}
	}
	if filepath.Base(filepath.Dir(filepath.Dir(staged))) != ".jigz.next.tmp" {
		t.Errorf("validator got %s, not the staged file", staged)
	}
}
