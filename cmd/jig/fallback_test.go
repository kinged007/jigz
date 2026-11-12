package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/gen"
	"github.com/kinged007/jigz/internal/machine"
)

func writeFallbackConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	body := `repo: owner/repo
tracker:
  kind: github
  api_key: { from_env: JIG_TEST_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
  handoff_state: agent:done
agents:
  main: { runtime: claude-code, model: claude-sonnet-5-5 }
  spare: { runtime: claude-code, model: claude-haiku-5-5 }
work:
  delivery:
    query_filter: "label:in-progress -label:needs-human"
    agent: [main, spare]
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
`
	if err := os.MkdirAll(filepath.Join(root, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".jigz", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JIG_TEST_TOKEN", "test-token")
	return root
}

func fallbackSupervisor(t *testing.T, root string) *supervisor {
	t.Helper()
	s := testSupervisor(t, root)
	s.refreshWork("test")
	for _, d := range listWorkflows(root) {
		s.defs[d.name] = d
	}
	// An idle running loop: sessions settle, none active.
	s.procs["delivery"] = &loopProc{def: s.defs["delivery"], since: time.Now().Add(-time.Hour)}
	s.quiet = func(string) (bool, bool) { return true, true }
	s.sessions = func(string) (bool, bool) { return false, true }
	return s
}

func gateViews(t *testing.T, s *supervisor, now time.Time) (gateInputs, map[string]loopView) {
	t.Helper()
	s.refreshState(now, true)
	in := s.gateInputsLocked()
	views, _ := gateDecide(in, now, s.gateLoopsLocked())
	return in, views
}

func docText(t *testing.T, root, loop string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ".jigz", "workflows", "WORKFLOW."+loop+".md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestFallbackRewritesAtIdle switches the document to the standby agent
// when the primary is exhausted, and records the trigger for status.
func TestFallbackRewritesAtIdle(t *testing.T) {
	root := writeFallbackConfig(t)
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promoteDocs(root, docs); err != nil {
		t.Fatal(err)
	}
	s := fallbackSupervisor(t, root)
	until := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	if err := machine.MarkExhausted(machine.StatePath(root), "main", until, "limit"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	in, views := gateViews(t, s, now)
	if views["delivery"].Agent != "spare" {
		t.Fatalf("gate picks %q, want spare", views["delivery"].Agent)
	}
	s.fallbackStep(in, views, now)
	got := docText(t, root, "delivery")
	if !strings.Contains(got, "agent-exec spare") || !strings.Contains(got, "claude-haiku-5-5") {
		t.Errorf("document not rewritten for spare:\n%s", got)
	}
	if s.docAgent["delivery"] != "spare" {
		t.Errorf("docAgent = %q, want spare", s.docAgent["delivery"])
	}
	fb, ok := s.fallback["delivery"]
	if !ok || fb.trigger != "usage limit" || !fb.usable.Equal(until) {
		t.Errorf("fallback = %+v, want usage-limit trigger usable %v", fb, until)
	}
	doc := s.statusDoc("running")
	if len(doc.Workflows) != 1 {
		t.Fatalf("workflows = %+v", doc.Workflows)
	}
	w := doc.Workflows[0]
	if w.Agent != "spare" || w.Primary != "main" || !strings.Contains(w.FallbackReason, "usage limit") {
		t.Errorf("status = agent %q primary %q fallback %q", w.Agent, w.Primary, w.FallbackReason)
	}
}

// TestFallbackReturnsAfterCooldown rewrites back to the primary only
// after the pick survives consecutive ticks.
func TestFallbackReturnsAfterCooldown(t *testing.T) {
	root := writeFallbackConfig(t)
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promoteDocs(root, docs); err != nil {
		t.Fatal(err)
	}
	s := fallbackSupervisor(t, root)
	statePath := machine.StatePath(root)
	if err := machine.MarkExhausted(statePath, "main", time.Now().Add(2*time.Hour), "limit"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	in, views := gateViews(t, s, now)
	s.fallbackStep(in, views, now)
	if got := docText(t, root, "delivery"); !strings.Contains(got, "agent-exec spare") {
		t.Fatalf("not on spare:\n%s", got)
	}
	// The primary recovers: clear the exhaustion.
	if err := machine.UpdateState(statePath, func(st *machine.State) {
		a := st.Agents["main"]
		a.ExhaustedUntil = time.Time{}
		st.Agents["main"] = a
	}); err != nil {
		t.Fatal(err)
	}
	in, views = gateViews(t, s, now.Add(2*time.Second))
	s.fallbackStep(in, views, now.Add(2*time.Second))
	if got := docText(t, root, "delivery"); !strings.Contains(got, "agent-exec spare") {
		t.Errorf("rewrote back after one tick:\n%s", got)
	}
	in, views = gateViews(t, s, now.Add(4*time.Second))
	s.fallbackStep(in, views, now.Add(4*time.Second))
	if got := docText(t, root, "delivery"); !strings.Contains(got, "agent-exec main") {
		t.Errorf("not back on main after stable ticks:\n%s", got)
	}
	if _, ok := s.fallback["delivery"]; ok {
		t.Errorf("fallback record kept after returning to primary")
	}
}

// TestFallbackSurvivesReload keeps the standby document across reload and
// restores it when a primary render replaces it behind docAgent's back.
func TestFallbackSurvivesReload(t *testing.T) {
	root := writeFallbackConfig(t)
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promoteDocs(root, docs); err != nil {
		t.Fatal(err)
	}
	s := fallbackSupervisor(t, root)
	if err := machine.MarkExhausted(machine.StatePath(root), "main", time.Now().Add(2*time.Hour), "limit"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	in, views := gateViews(t, s, now)
	s.fallbackStep(in, views, now)
	if got := docText(t, root, "delivery"); !strings.Contains(got, "agent-exec spare") {
		t.Fatalf("not on spare:\n%s", got)
	}
	// Reload leaves a standby document alone.
	docs, _, err = generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promoteDocs(root, docs); err != nil {
		t.Fatal(err)
	}
	if got := docText(t, root, "delivery"); !strings.Contains(got, "agent-exec spare") {
		t.Fatalf("reload rewrote the standby document:\n%s", got)
	}
	// A primary render written behind docAgent's back (an older jig) is restored.
	if err := gen.AtomicWrite(gen.DocPath(root, "delivery"), docs[0].Text); err != nil {
		t.Fatal(err)
	}
	in, views = gateViews(t, s, now.Add(2*time.Second))
	s.fallbackStep(in, views, now.Add(2*time.Second))
	if got := docText(t, root, "delivery"); !strings.Contains(got, "agent-exec spare") {
		t.Errorf("standby document lost after reload:\n%s", got)
	}
}

// TestFallbackRigRewritesAndReturns drives the whole supervisor path on
// a fake clock: exhaustion moves the document to the standby at idle,
// and the primary returns after the pick survives consecutive ticks.
func TestFallbackRigRewritesAndReturns(t *testing.T) {
	sup, root := startedSup(t, "while true; do sleep 1; done\n")
	mustWrite(t, filepath.Join(root, ".jigz", "config.yaml"), `repo: owner/repo
tracker:
  kind: github
  api_key: { from_env: JIG_TEST_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
  handoff_state: agent:done
agents:
  main: { runtime: claude-code, model: claude-sonnet-5-5 }
  spare: { runtime: claude-code, model: claude-haiku-5-5 }
work:
  delivery:
    query_filter: "label:in-progress -label:needs-human"
    agent: [main, spare]
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
`)
	t.Setenv("JIG_TEST_TOKEN", "test-token")
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promoteDocs(root, docs); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sup.clock = func() time.Time { return now }
	active := false
	sup.sessions = func(string) (bool, bool) { return active, true }
	sup.quiet = func(string) (bool, bool) { return true, true }
	sup.defs["delivery"] = loopDef{"delivery", "WORKFLOW.delivery.md"}
	if err := sup.gateStart("delivery"); err != nil {
		t.Fatal(err)
	}
	step := func() {
		t.Helper()
		now = now.Add(2 * time.Second)
		sup.mu.Lock()
		for _, p := range sup.procs {
			p.since = now.Add(-time.Hour)
		}
		sup.mu.Unlock()
		sup.refreshState(now, true)
		if code, done := sup.gateStep(); done {
			t.Fatalf("gateStep ended the run with code %d", code)
		}
	}
	until := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	if err := machine.MarkExhausted(machine.StatePath(root), "main", until, "limit"); err != nil {
		t.Fatal(err)
	}
	step()
	if got := docText(t, root, "delivery"); !strings.Contains(got, "agent-exec spare") {
		t.Fatalf("no standby rewrite:\n%s", got)
	}
	doc := sup.statusDoc("running")
	var w *workflowStatus
	for i := range doc.Workflows {
		if doc.Workflows[i].Name == "delivery" {
			w = &doc.Workflows[i]
		}
	}
	if w == nil {
		t.Fatalf("delivery missing from status: %+v", doc.Workflows)
	}
	if w.Agent != "spare" || w.Primary != "main" || !strings.Contains(w.FallbackReason, "usage limit") {
		t.Errorf("status = agent %q primary %q fallback %q", w.Agent, w.Primary, w.FallbackReason)
	}
	// The primary recovers: one stable tick is not enough, two rewrites.
	if err := machine.UpdateState(machine.StatePath(root), func(st *machine.State) {
		a := st.Agents["main"]
		a.ExhaustedUntil = time.Time{}
		st.Agents["main"] = a
	}); err != nil {
		t.Fatal(err)
	}
	step()
	if got := docText(t, root, "delivery"); !strings.Contains(got, "agent-exec spare") {
		t.Errorf("rewrote back after one tick:\n%s", got)
	}
	step()
	if got := docText(t, root, "delivery"); !strings.Contains(got, "agent-exec main") {
		t.Errorf("not back on main:\n%s", got)
	}
}

// TestFallbackLabelsModelError records a model failure as "model
// unavailable", not "usage limit": model errors exhaust via
// ExhaustedUntil too, so the exhausted arm would fire first.
func TestFallbackLabelsModelError(t *testing.T) {
	root := writeFallbackConfig(t)
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promoteDocs(root, docs); err != nil {
		t.Fatal(err)
	}
	s := fallbackSupervisor(t, root)
	until := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	if err := machine.MarkModelExhausted(machine.StatePath(root), "main", until, "claude-sonnet-5-5", "model unavailable: unknown model xyz"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	in, views := gateViews(t, s, now)
	if views["delivery"].Agent != "spare" {
		t.Fatalf("gate picks %q, want spare", views["delivery"].Agent)
	}
	if trigger, usable := fallbackTrigger(in, in.work["delivery"], "main", now); trigger != "model unavailable" || !usable.IsZero() {
		t.Fatalf("trigger = %q usable %v, want model unavailable with zero usable", trigger, usable)
	}
	s.fallbackStep(in, views, now)
	fb, ok := s.fallback["delivery"]
	if !ok || fb.trigger != "model unavailable" || !fb.usable.IsZero() {
		t.Fatalf("fallback = %+v, want model-unavailable trigger with zero usable", fb)
	}
	w := s.statusDoc("running").Workflows[0]
	if !strings.Contains(w.FallbackReason, "model unavailable") || strings.Contains(w.FallbackReason, "usage limit") {
		t.Errorf("status fallback %q, want model unavailable without usage limit", w.FallbackReason)
	}
}

// TestFallbackOnUnusableAgent switches the document to the next
// candidate and shows the unusable reason as the fallback trigger.
func TestFallbackOnUnusableAgent(t *testing.T) {
	root := writeFallbackConfig(t)
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promoteDocs(root, docs); err != nil {
		t.Fatal(err)
	}
	s := fallbackSupervisor(t, root)
	now := time.Now()
	if err := machine.MarkUnusable(machine.StatePath(root), "main", now, "Invalid API key"); err != nil {
		t.Fatal(err)
	}
	in, views := gateViews(t, s, now)
	if views["delivery"].Agent != "spare" {
		t.Fatalf("gate picks %q, want spare", views["delivery"].Agent)
	}
	s.fallbackStep(in, views, now)
	if got := docText(t, root, "delivery"); !strings.Contains(got, "agent-exec spare") {
		t.Errorf("document not switched to spare:\n%s", got)
	}
	fb := s.fallback["delivery"]
	if fb.trigger != "agent unusable: Invalid API key" || !fb.usable.IsZero() {
		t.Errorf("fallback = %+v", fb)
	}
}

// TestFallbackWaitsForIdle keeps the document while sessions run.
func TestFallbackWaitsForIdle(t *testing.T) {
	root := writeFallbackConfig(t)
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promoteDocs(root, docs); err != nil {
		t.Fatal(err)
	}
	s := fallbackSupervisor(t, root)
	s.sessions = func(string) (bool, bool) { return true, true }
	if err := machine.MarkExhausted(machine.StatePath(root), "main", time.Now().Add(2*time.Hour), "limit"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	in, views := gateViews(t, s, now)
	s.fallbackStep(in, views, now)
	if got := docText(t, root, "delivery"); !strings.Contains(got, "agent-exec main") {
		t.Errorf("rewrote while busy:\n%s", got)
	}
}
