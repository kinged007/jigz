package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/fakegithub"
)

const (
	questionBlock = "<!-- agent-question id=\"q-1\" audience=\"product\" blocking=\"true\" resume=\"agent:plan\" -->\n**Question:** Which one?\n<!-- /agent-question -->"
	answerBlock   = "<!-- agent-answer id=\"q-1\" -->\nThis one.\n<!-- /agent-answer -->"
)

// questionClock makes every fake GitHub timestamp two seconds later than
// the last, and keeps the fallback search from waking loops.
func (r *rig) questionClock() {
	var ticks atomic.Int64
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	r.gh.SetClock(func() time.Time { return base.Add(time.Duration(ticks.Add(1)) * 2 * time.Second) })
	r.opt.fallback = 24 * time.Hour
}

// parked adds an item that was parked with needs-human and then answered.
func (r *rig) parked(labels ...string) int {
	n := r.gh.Add(fakegithub.Item{Repo: testProject, Title: "parked", Labels: append([]string{"agent:done"}, labels...)})
	r.gh.AddComment(testProject, n, fakegithub.Comment{Author: "bot", Association: "OWNER", Body: questionBlock})
	r.gh.AddLabelAsPerson(testProject, n, "needs-human", "bot")
	r.gh.AddComment(testProject, n, fakegithub.Comment{Author: "alice", Association: "COLLABORATOR", Body: answerBlock})
	return n
}

func (r *rig) hasLabel(n int, label string) bool {
	it, _ := r.gh.Get(testProject, n)
	return slices.Contains(it.Labels, label)
}

func (r *rig) waitRoot() {
	r.t.Helper()
	r.eventually("the loop root", func() bool {
		_, err := os.Stat(filepath.Join(r.root(testProject), ".jigz", "config.yaml"))
		return err == nil
	})
}

func needsHumanSearches(r *rig) []string {
	var out []string
	for _, q := range searches(r.gh) {
		if strings.Contains(q, "label:needs-human") {
			out = append(out, q)
		}
	}
	return out
}

func TestDaemonQuestionsResumeRoutedItemsOnly(t *testing.T) {
	r := newRig(t)
	r.questionClock()
	routed := r.parked(testRunner)
	other := r.parked()
	r.start()
	r.eventually("the resume", func() bool { return r.hasLabel(routed, "agent:plan") })
	r.eventually("the wake of the loop", func() bool { return strings.Contains(r.record("starts"), r.root(testProject)) })
	if r.hasLabel(routed, "needs-human") || r.hasLabel(routed, "agent:done") {
		t.Errorf("a resumed item keeps its parking labels")
	}
	searches := needsHumanSearches(r)
	if len(searches) == 0 {
		t.Fatal("no question search")
	}
	for _, q := range searches {
		if !strings.Contains(q, "label:"+testRunner) {
			t.Errorf("question search %q has no runner label", q)
		}
	}
	if w := r.gh.LabelRequests(testProject, other); len(w) != 0 {
		t.Errorf("an item without the runner label was written: %v", w)
	}
	if !r.hasLabel(other, "needs-human") {
		t.Errorf("an item without the runner label lost needs-human")
	}
	if !strings.Contains(r.logs.String(), "questions: resumed "+testProject+"#") {
		t.Errorf("the resume is not logged")
	}
}

func TestDaemonQuestionsResumeWhilePausedWakesNothing(t *testing.T) {
	r := newRig(t)
	r.questionClock()
	n := r.parked(testRunner)
	if err := os.MkdirAll(r.opt.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(filepath.Join(r.opt.stateDir, "state.json"), daemonState{Paused: true}); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.eventually("the resume", func() bool { return r.hasLabel(n, "agent:plan") })
	time.Sleep(300 * time.Millisecond)
	if got := r.record("starts"); got != "" {
		t.Errorf("a paused daemon started a loop: %q", got)
	}
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	if r.d.lastErr != "" {
		t.Errorf("an ignored wake was reported as an error: %q", r.d.lastErr)
	}
}

func TestDaemonQuestionsOffSkipsTheProject(t *testing.T) {
	r := newRig(t)
	r.questionClock()
	r.start()
	r.waitRoot()
	r.eventually("the first pass", func() bool { return len(needsHumanSearches(r)) > 0 })
	before := len(needsHumanSearches(r))
	cfg := filepath.Join(r.root(testProject), ".jigz", "config.yaml")
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, append(raw, []byte("\ngithub:\n  questions: false\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	n := r.parked(testRunner)
	r.d.scan.Pass(context.Background())
	if got := needsHumanSearches(r); len(got) != before {
		t.Errorf("a project with github.questions off was searched: %v", got)
	}
	if !r.hasLabel(n, "needs-human") {
		t.Errorf("a project with github.questions off was resumed")
	}
}

func TestDaemonQuestionsWithoutATokenReportAStatus(t *testing.T) {
	r := newRig(t)
	r.questionClock()
	r.opt.hub.TrackerToken = ""
	n := r.parked(testRunner)
	r.start()
	r.waitRoot()
	r.d.scan.Pass(context.Background())
	if got := r.d.scan.Status(testProject).Err; got != "no GitHub token" {
		t.Errorf("status = %q, want no GitHub token", got)
	}
	if !r.hasLabel(n, "needs-human") {
		t.Errorf("an item was resumed without a token")
	}
}
