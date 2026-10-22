package edit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/store"
)

const jigzYAML = `# fleet config
state_dir: ""

defaults:
  token_env: GH_TOKEN
  assignee: "@me"   # the token owner
  agent:
    max_turns: 10

repos:
  - repo: owner/alpha
    workflows: [plan, build]
    agent:
      model: gpt-5
  # a second project
  - repo: owner/beta
`

const workflowMD = `---
tracker:
  kind: github
  # keep this comment
  handoff_state: agent:done
  terminal_states: []

polling:
  interval_ms: 60000

workspace:
  root: $ROOT/plan
  retention_days: 30

custom_extension:
  keep: me

dispatch:
  default:
    template: ./prompts/plan.md
---

{{/* body: untouched */}}
Text with --- inside and trailing space   
`

func rootOf(t *testing.T, text string) *Doc {
	t.Helper()
	d, err := ParseDoc(text)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestUnchangedFormWritesTheSameBytes(t *testing.T) {
	d := rootOf(t, jigzYAML)
	var c Jigz
	Read(d.Root(), &c)
	Write(d.Root(), &c)
	var a Agent
	Read(Scope(d.Root(), "defaults", false), &a)
	Write(Scope(d.Root(), "defaults", false), &a)
	got, err := d.String()
	if err != nil {
		t.Fatal(err)
	}
	if got != jigzYAML {
		t.Errorf("a no-op edit changed the file:\n%s", Diff("c", jigzYAML, got))
	}
}

func TestEditKeepsCommentsAndOrder(t *testing.T) {
	d := rootOf(t, jigzYAML)
	var c Jigz
	Read(d.Root(), &c)
	c.Assignee = "someone"
	c.Milestone = "v2"
	Write(d.Root(), &c)
	got, _ := d.String()
	want := strings.NewReplacer(
		`assignee: "@me"   # the token owner`, `assignee: "someone" # the token owner`,
		"    max_turns: 10\n", "    max_turns: 10\n  milestone: v2\n").Replace(jigzYAML)
	if got != want {
		t.Errorf("edit result differs:\n%s", Diff("c", want, got))
	}
}

func TestEmptyValueRemovesTheKeyAndAnEmptyParent(t *testing.T) {
	d := rootOf(t, jigzYAML)
	var a Agent
	repo := Scope(d.Root(), "owner/alpha", false)
	Read(repo, &a)
	if a.Model != "gpt-5" {
		t.Fatalf("read model = %q", a.Model)
	}
	a.Model = ""
	Write(repo, &a)
	got, _ := d.String()
	alpha := strings.SplitN(strings.SplitN(got, "owner/alpha", 2)[1], "owner/beta", 2)[0]
	if strings.Contains(got, "gpt-5") || strings.Contains(alpha, "agent:") {
		t.Errorf("model and its empty agent block should be gone:\n%s", got)
	}
	if !strings.Contains(got, "# a second project") {
		t.Errorf("comment lost:\n%s", got)
	}
}

func TestScopeCreatesWorkflowOverrides(t *testing.T) {
	d := rootOf(t, jigzYAML)
	if Scope(d.Root(), "owner/alpha/plan", false) != nil {
		t.Fatal("override exists before it was created")
	}
	var a Agent
	n := 3
	a.MaxTurns = &n
	Write(Scope(d.Root(), "owner/alpha/plan", true), &a)
	got, _ := d.String()
	if !strings.Contains(got, "workflow_overrides:\n      plan:\n        agent:\n          max_turns: 3") {
		t.Errorf("override not written:\n%s", got)
	}
	if Scope(d.Root(), "owner/missing/plan", true) != nil {
		t.Error("a scope under an unknown project must be nil")
	}
}

func TestFrontMatterEditLeavesTheBodyAndUnknownKeys(t *testing.T) {
	build := BuildFront(func(d *Doc) []Problem {
		var w Workflow
		Read(d.Root(), &w)
		days := 7
		w.RetentionDays = &days
		w.HandoffState = "agent:finished"
		w.Rules = append(w.Rules, Rule{Name: "quick", Labels: []string{"agent:quick"}, Agent: "pi", Template: "./prompts/quick.md"})
		Write(d.Root(), &w)
		return nil
	})
	got, probs := build(workflowMD)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	_, wantBody, _ := SplitFrontMatter(workflowMD)
	_, gotBody, _ := SplitFrontMatter(got)
	if gotBody != wantBody {
		t.Errorf("body changed: %q", gotBody)
	}
	for _, keep := range []string{"# keep this comment", "custom_extension:\n  keep: me", "root: $ROOT/plan", "interval_ms: 60000", "retention_days: 7", "handoff_state: agent:finished", "- name: quick"} {
		if !strings.Contains(got, keep) {
			t.Errorf("result lacks %q:\n%s", keep, got)
		}
	}
	var back Workflow
	d := rootOf(t, strings.TrimSuffix(strings.SplitN(got, "---\n", 3)[1], "\n")+"\n")
	Read(d.Root(), &back)
	if len(back.Rules) != 1 || back.Rules[0].Labels[0] != "agent:quick" || back.Rules[0].Template != "./prompts/quick.md" {
		t.Errorf("rule did not round trip: %+v", back.Rules)
	}
}

func TestRulesAreEditedInPlaceAndRemovedByClearingTheName(t *testing.T) {
	text := "---\ndispatch:\n  rules:\n    - name: a\n      match:\n        labels: [x]\n      agent: pi\n      stage: keepme\n    - name: b\n      agent: codex\n  default:\n    template: t.md\n---\nbody\n"
	build := BuildFront(func(d *Doc) []Problem {
		var w Workflow
		Read(d.Root(), &w)
		w.Rules[0].Agent = "claude-code"
		w.Rules[1].Name = ""
		Write(d.Root(), &w)
		return nil
	})
	got, _ := build(text)
	want := "---\ndispatch:\n  rules:\n    - name: a\n      match:\n        labels: [x]\n      agent: claude-code\n      stage: keepme\n  default:\n    template: t.md\n---\nbody\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	build = BuildFront(func(d *Doc) []Problem {
		var w Workflow
		Read(d.Root(), &w)
		w.Rules = nil
		Write(d.Root(), &w)
		return nil
	})
	got, _ = build(want)
	if strings.Contains(got, "rules:") {
		t.Errorf("rules key should be gone with its last rule:\n%s", got)
	}
}

func TestParseTakesTableRowsAndReportsBadValuesAgainstTheirField(t *testing.T) {
	vals := map[string][]string{
		"agent.max_turns":           {"abc"},
		"polling_interval_ms":       {" 5000 "},
		"agent.kind":                {"pi"},
		"dispatch.rules.0.name":     {"r"},
		"dispatch.rules.1.name":     {""},
		"dispatch.rules.1.template": {"t"},
	}
	var a Agent
	probs := Parse(vals, &a)
	if len(probs) != 1 || probs[0].Path != "agent.max_turns" || !strings.Contains(probs[0].Msg, "whole number") {
		t.Fatalf("problems = %+v", probs)
	}
	if a.Kind != "pi" || *a.PollingInterval != 5000 || a.MaxTurns != nil {
		t.Errorf("parsed %+v", a)
	}
	var w Workflow
	Parse(vals, &w)
	if len(w.Rules) != 2 || w.Rules[0].Name != "r" || w.Rules[1].Template != "t" {
		t.Errorf("rows = %+v", w.Rules)
	}
}

func TestDescribeMatchesProblemsToFieldsAndAddsASpareRow(t *testing.T) {
	var w Workflow
	w.Rules = []Rule{{Name: "a"}}
	form := Describe(&w, []Problem{{Path: "dispatch.rules.0.agent", Msg: "bad"}, {Path: "tracker.handoff_state", Msg: "worse"}}, nil)
	if len(form.Tables) != 1 || len(form.Tables[0].Rows) != 2 {
		t.Fatalf("tables = %+v", form.Tables)
	}
	if got := form.Tables[0].Rows[0][2].Error; got != "bad" {
		t.Errorf("cell error = %q", got)
	}
	var handoff Field
	for _, f := range form.Fields {
		if f.Name == "tracker.handoff_state" {
			handoff = f
		}
	}
	if handoff.Error != "worse" {
		t.Errorf("field = %+v", handoff)
	}
}

func TestOnlyOneSpareRowIsDrawn(t *testing.T) {
	w := Workflow{Rules: []Rule{{Name: "a"}, {}}}
	if rows := Describe(&w, nil, nil).Tables[0].Rows; len(rows) != 2 {
		t.Errorf("%d rows drawn, want the rule and one blank row", len(rows))
	}
}

func TestDiffIsUnifiedWithContext(t *testing.T) {
	a := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"
	b := "1\n2\n3\n4\nFIVE\n6\n7\n8\n9\n10\n"
	want := "--- a/f\n+++ b/f\n@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-5\n+FIVE\n 6\n 7\n 8\n"
	if got := Diff("f", a, b); got != want {
		t.Errorf("diff:\n%s\nwant:\n%s", got, want)
	}
	if Diff("f", a, a) != "" {
		t.Error("equal texts must give an empty diff")
	}
}

type fakeApplier struct {
	calls []string
	// fail lists the call numbers, from 1, that fail.
	fail map[int]bool
	read string
}

func (f *fakeApplier) Apply(t Target) (string, error) {
	raw, _ := os.ReadFile(t.Path)
	f.calls = append(f.calls, string(raw))
	if f.fail[len(f.calls)] {
		return "", errors.New("child did not come back")
	}
	return "", nil
}

type rig struct {
	p      *Pipeline
	t      Target
	app    *fakeApplier
	st     *store.Store
	now    time.Time
	hashes []string
}

func newRig(t *testing.T, content string) *rig {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	path := filepath.Join(dir, "conf.yaml")
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	r := &rig{app: &fakeApplier{fail: map[int]bool{}}, st: st, now: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC),
		t: Target{Scope: "owner/alpha", Name: "conf.yaml", Path: path}}
	r.p = &Pipeline{Dir: filepath.Join(dir, "backups"), Store: st, Applier: r.app,
		Now: func() time.Time { r.now = r.now.Add(time.Second); return r.now },
		Validate: func(_ Target, c string) (Report, error) {
			if strings.Contains(c, "BAD") {
				return Report{Errors: []Problem{{Path: "x.y", Msg: "bad value"}}}, nil
			}
			return Report{}, nil
		}}
	return r
}

func (r *rig) read(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(r.t.Path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func replaceWith(s string) Build {
	return func(string) (string, []Problem) { return s, nil }
}

func TestApplyBacksUpWritesAppliesAndAudits(t *testing.T) {
	r := newRig(t, "a: 1\n")
	out, err := r.p.Apply(r.t, Hash("a: 1\n"), replaceWith("a: 2\n"), "10.0.0.1")
	if err != nil || !out.Applied || out.BackupID == 0 {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
	if got := r.read(t); got != "a: 2\n" {
		t.Errorf("file = %q", got)
	}
	if st, _ := os.Stat(r.t.Path); st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want the original 0640", st.Mode().Perm())
	}
	bs, _ := r.st.Backups(r.t.Path, 10)
	if len(bs) != 1 || !strings.HasPrefix(bs[0].BackupPath, filepath.Join(r.p.Dir, "owner", "alpha", "conf.yaml.")) {
		t.Fatalf("backups = %+v", bs)
	}
	if raw, _ := os.ReadFile(bs[0].BackupPath); string(raw) != "a: 1\n" || bs[0].SHA256 != Hash("a: 1\n") || bs[0].Actor != "10.0.0.1" {
		t.Errorf("backup = %q %+v", raw, bs[0])
	}
	if len(r.app.calls) != 1 || r.app.calls[0] != "a: 2\n" {
		t.Errorf("applier saw %q", r.app.calls)
	}
	rows, _ := r.st.Audit(5)
	if len(rows) != 1 || rows[0].Action != "edit_apply" || rows[0].Actor != "10.0.0.1" || rows[0].Result != "ok" || rows[0].Project != "owner/alpha" {
		t.Errorf("audit = %+v", rows)
	}
}

func TestStaleHashIsAConflictAndWritesNothing(t *testing.T) {
	r := newRig(t, "a: 1\n")
	pv, err := r.p.Preview(r.t, Hash("older\n"), replaceWith("a: 2\n"))
	if err != nil || !pv.Stale {
		t.Fatalf("preview = %+v, err = %v", pv, err)
	}
	out, _ := r.p.Apply(r.t, Hash("older\n"), replaceWith("a: 2\n"), "x")
	if !out.Conflict || out.Applied || r.read(t) != "a: 1\n" || len(r.app.calls) != 0 {
		t.Errorf("outcome = %+v, file = %q", out, r.read(t))
	}
	if bs, _ := r.st.Backups("", 10); len(bs) != 0 {
		t.Errorf("a conflict made backups: %+v", bs)
	}
}

func TestInvalidCandidateIsNeverWritten(t *testing.T) {
	r := newRig(t, "a: 1\n")
	pv, _ := r.p.Preview(r.t, Hash("a: 1\n"), replaceWith("a: BAD\n"))
	if pv.Report.OK() || pv.Report.Errors[0].Path != "x.y" || pv.Diff == "" {
		t.Errorf("preview = %+v", pv)
	}
	out, _ := r.p.Apply(r.t, Hash("a: 1\n"), replaceWith("a: BAD\n"), "x")
	if out.Applied || out.Report.OK() || r.read(t) != "a: 1\n" || len(r.app.calls) != 0 {
		t.Errorf("outcome = %+v", out)
	}
	out, _ = r.p.Apply(r.t, Hash("a: 1\n"), func(string) (string, []Problem) { return "", []Problem{{Path: "f", Msg: "nope"}} }, "x")
	if out.Report.OK() || out.Report.Errors[0].Path != "f" {
		t.Errorf("build problems not reported: %+v", out)
	}
}

func TestNoChangeIsNotApplied(t *testing.T) {
	r := newRig(t, "a: 1\n")
	out, _ := r.p.Apply(r.t, Hash("a: 1\n"), replaceWith("a: 1\n"), "x")
	if !out.NoChange || len(r.app.calls) != 0 {
		t.Errorf("outcome = %+v", out)
	}
}

func TestFailedApplyRestoresTheBackupAndAppliesAgain(t *testing.T) {
	r := newRig(t, "a: 1\n")
	r.app.fail[1] = true
	out, err := r.p.Apply(r.t, Hash("a: 1\n"), replaceWith("a: 2\n"), "x")
	if err != nil || out.Applied || !out.RolledBack {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
	if r.read(t) != "a: 1\n" {
		t.Errorf("file = %q, want the original back", r.read(t))
	}
	if len(r.app.calls) != 2 || r.app.calls[0] != "a: 2\n" || r.app.calls[1] != "a: 1\n" {
		t.Errorf("applier saw %q, want the new file then the restored one", r.app.calls)
	}
	var names []string
	for _, s := range out.Steps {
		names = append(names, fmt.Sprintf("%s:%v", s.Name, s.OK))
	}
	if got := strings.Join(names, " "); got != "Validate:true Backup:true Write:true Apply:false Rollback:true" {
		t.Errorf("steps = %s", got)
	}
	rows, _ := r.st.Audit(5)
	if len(rows) != 1 || rows[0].Action != "edit_rollback" || !strings.Contains(rows[0].Result, "child did not come back") {
		t.Errorf("audit = %+v", rows)
	}
}

func TestRollbackThatCannotApplyIsReported(t *testing.T) {
	r := newRig(t, "a: 1\n")
	r.app.fail[1], r.app.fail[2] = true, true
	out, _ := r.p.Apply(r.t, Hash("a: 1\n"), replaceWith("a: 2\n"), "x")
	if out.Applied || out.RolledBack || !out.Restored || r.read(t) != "a: 1\n" {
		t.Fatalf("outcome = %+v", out)
	}
	last := out.Steps[len(out.Steps)-1]
	if last.Name != "Rollback" || last.OK || !strings.Contains(last.Msg, "not live") {
		t.Errorf("last step = %+v", last)
	}
}

func TestOnlyTheLastTwentyBackupsOfAFileAreKept(t *testing.T) {
	r := newRig(t, "n: 0\n")
	for i := 1; i <= 25; i++ {
		cur := r.read(t)
		out, err := r.p.Apply(r.t, Hash(cur), replaceWith(fmt.Sprintf("n: %d\n", i)), "x")
		if err != nil || !out.Applied {
			t.Fatalf("apply %d: %+v %v", i, out, err)
		}
	}
	bs, _ := r.st.Backups(r.t.Path, 100)
	if len(bs) != KeepBackups {
		t.Fatalf("%d backups recorded, want %d", len(bs), KeepBackups)
	}
	files, _ := os.ReadDir(filepath.Join(r.p.Dir, "owner", "alpha"))
	if len(files) != KeepBackups {
		t.Errorf("%d backup files on disk, want %d", len(files), KeepBackups)
	}
	if raw, _ := os.ReadFile(bs[len(bs)-1].BackupPath); string(raw) != "n: 5\n" {
		t.Errorf("oldest kept backup = %q, want n: 5", raw)
	}
}

func TestRestorePutsABackupBackAndKeepsTheReplacedFile(t *testing.T) {
	r := newRig(t, "a: 1\n")
	r.p.Apply(r.t, Hash("a: 1\n"), replaceWith("a: 2\n"), "x")
	bs, _ := r.st.Backups(r.t.Path, 10)
	out, err := r.p.Restore(bs[0].ID, "10.0.0.2")
	if err != nil || !out.Applied {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
	if r.read(t) != "a: 1\n" {
		t.Errorf("file = %q", r.read(t))
	}
	bs, _ = r.st.Backups(r.t.Path, 10)
	if len(bs) != 2 {
		t.Fatalf("backups = %+v", bs)
	}
	if raw, _ := os.ReadFile(bs[0].BackupPath); string(raw) != "a: 2\n" {
		t.Errorf("the replaced file was not backed up: %q", raw)
	}
	rows, _ := r.st.Audit(1)
	if rows[0].Action != "edit_restore" || rows[0].Actor != "10.0.0.2" {
		t.Errorf("audit = %+v", rows)
	}
}

func TestRestoreRefusesATamperedBackup(t *testing.T) {
	r := newRig(t, "a: 1\n")
	r.p.Apply(r.t, Hash("a: 1\n"), replaceWith("a: 2\n"), "x")
	bs, _ := r.st.Backups(r.t.Path, 10)
	os.WriteFile(bs[0].BackupPath, []byte("a: 99\n"), 0o600)
	if _, err := r.p.Restore(bs[0].ID, "x"); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Errorf("err = %v, want a hash refusal", err)
	}
	if r.read(t) != "a: 2\n" {
		t.Errorf("file = %q", r.read(t))
	}
}

func TestRawFrontMatterKeepsTheBodyAndChecksYAML(t *testing.T) {
	got, probs := Raw("a: 1\r\nb: 2", true)(workflowMD)
	_, body, _ := SplitFrontMatter(workflowMD)
	if len(probs) > 0 || got != "---\na: 1\nb: 2\n"+body {
		t.Errorf("got %q %v", got, probs)
	}
	if _, probs := Raw("a: [", true)(workflowMD); len(probs) == 0 {
		t.Error("invalid YAML accepted")
	}
	if _, _, err := SplitFrontMatter("no fence"); err == nil {
		t.Error("missing fence accepted")
	}
}

func TestTheShippedExampleConfigRoundTripsUnchanged(t *testing.T) {
	raw, err := os.ReadFile("../fleetconfig/testdata/example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d := rootOf(t, string(raw))
	var c Jigz
	Read(d.Root(), &c)
	Write(d.Root(), &c)
	if got, _ := d.String(); got != string(raw) {
		t.Errorf("round trip changed the example:\n%s", Diff("example", string(raw), got))
	}
}
