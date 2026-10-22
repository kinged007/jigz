package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/kinged007/sortie-loop/internal/engine"
	"github.com/kinged007/sortie-loop/internal/rollup"
)

func startedSup(t *testing.T, script string) (*supervisor, string) {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".sortie", "config.yaml"), "repo: owner/name\n")
	mustWrite(t, filepath.Join(root, ".sortie", "workflows", "WORKFLOW.plan.md"),
		"---\ntracker:\n  kind: github-pr\n---\nbody\n")
	sup := newTestSup(t, root, "plan")
	sup.ports["plan"] = 0
	sup.bin = writeScript(t, root, "sortie", script)
	if err := sup.startOne("plan"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.shutdown)
	return sup, root
}

func readStatus(t *testing.T, root string) statusDoc {
	t.Helper()
	path, err := statusPath(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc statusDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestStatusAfterStart(t *testing.T) {
	_, root := startedSup(t, "while true; do sleep 1; done\n")
	doc := readStatus(t, root)
	if doc.SchemaVersion != 1 || doc.State != "running" || doc.PID != os.Getpid() ||
		doc.Repo != root || doc.RepoName != "owner/name" || doc.EngineVersion != engine.Current().Version() ||
		doc.Version != version || doc.StartedAt.IsZero() {
		t.Errorf("unexpected top-level status: %+v", doc)
	}
	if !slices.Equal(doc.TestedSchemaVersions, rollup.SupportedSchemas) {
		t.Errorf("tested_schema_versions = %v, want %v", doc.TestedSchemaVersions, rollup.SupportedSchemas)
	}
	if len(doc.Workflows) != 1 {
		t.Fatalf("got %d workflows, want 1", len(doc.Workflows))
	}
	w := doc.Workflows[0]
	if w.Name != "plan" || w.WorkflowFile != "WORKFLOW.plan.md" || w.LoopType != "prs" ||
		w.State != "running" || w.RestartCount != 0 || w.LastExitCode != nil ||
		len(w.ConfigHash) != 64 || w.PID == 0 {
		t.Errorf("unexpected workflow status: %+v", w)
	}
}

func TestStatusAfterRecycle(t *testing.T) {
	sup, root := startedSup(t, "while true; do sleep 1; done\n")
	before := readStatus(t, root).Workflows[0].ConfigHash
	sup.stopAll(false, true)
	select {
	case ev := <-sup.events:
		if _, done := sup.onExit(ev); done {
			t.Fatal("a recycle ended the run")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the loop did not stop")
	}
	w := readStatus(t, root).Workflows[0]
	if w.RestartCount != 1 || w.ConfigHash != before || w.State != "running" {
		t.Errorf("after recycle got restart_count=%d state=%s hash stable=%v",
			w.RestartCount, w.State, w.ConfigHash == before)
	}
}

func TestStatusAfterUnrequestedExit(t *testing.T) {
	sup, root := startedSup(t, "exit 3\n")
	var ev exitEvent
	select {
	case ev = <-sup.events:
	case <-time.After(10 * time.Second):
		t.Fatal("the loop did not exit")
	}
	if _, done := sup.onExit(ev); !done {
		t.Fatal("an unrequested exit did not end the run")
	}
	sup.shutdown()
	sup.closeStatus()
	doc := readStatus(t, root)
	w := doc.Workflows[0]
	if doc.State != "exited" || w.State != "exited" || w.LastExitCode == nil || *w.LastExitCode != 3 {
		t.Errorf("unexpected final status: %+v", doc)
	}
}

func TestCleanExitRemovesStatus(t *testing.T) {
	sup, root := startedSup(t, "while true; do sleep 1; done\n")
	sup.shutdown()
	sup.closeStatus()
	path, _ := statusPath(root)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("status file remains after a clean exit: %v", err)
	}
}

func TestAtomicWriteLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "a.json")
	for _, body := range []string{"one", "two"} {
		if err := writeFileAtomic(path, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "a.json" {
		t.Errorf("directory holds %d entries, want only a.json", len(entries))
	}
	if got, _ := os.ReadFile(path); string(got) != "two" {
		t.Errorf("file holds %q, want two", got)
	}
}

func TestLoopTypeOf(t *testing.T) {
	cases := map[string]string{
		"---\ntracker:\n  kind: github\n---\n":                      "issues",
		"---\ntracker:\n  kind: github-pr\n---\n":                   "prs",
		"---\ntracker:\n  kind: \"github-pr\"\n---\n":               "prs",
		"---\ntracker:\n  kind: 'github-pr' # pull requests\n---\n": "prs",
		"---\ntracker:\n  kind: github # not github-pr\n---\n":      "issues",
		"---\ntracker:\n  kind: github\n---\nkind: github-pr\n":     "issues",
		"tracker:\n  kind: github-pr\n":                             "issues",
	}
	for in, want := range cases {
		if got := loopTypeOf([]byte(in)); got != want {
			t.Errorf("loopTypeOf(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestRegistryEntryHasStatusFields(t *testing.T) {
	_, root := startedSup(t, "while true; do sleep 1; done\n")
	entry := registryEntryFor(t, root)
	wantStatus, _ := statusPath(root)
	if entry.Version != version || entry.StartedAt.IsZero() || entry.StatusPath != wantStatus {
		t.Errorf("unexpected entry fields: %+v", entry)
	}
	l := entry.Loops[0]
	if l.WorkflowFile != filepath.Join(root, ".sortie", "workflows", "WORKFLOW.plan.md") || l.LoopType != "prs" {
		t.Errorf("unexpected endpoint fields: %+v", l)
	}
}

func TestOldFormatRegistryStillLoads(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path, err := registryPath()
	if err != nil {
		t.Fatal(err)
	}
	old := `{"entries":{"/r":{"repo_name":"o/n","updated_at":"2026-01-01T00:00:00Z","pid":1,` +
		`"loops":[{"repo":"/r","repo_name":"o/n","loop":"plan","port":7678,"db_path":"/x"}]}}}`
	mustWrite(t, path, old)
	if err := writeRegistry("/s", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var reg registry
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatal(err)
	}
	if got := reg.Entries["/r"].Loops[0]; got.Loop != "plan" || got.WorkflowFile != "" {
		t.Errorf("old entry did not load: %+v", got)
	}
	if reg.Entries["/s"].Version == "" {
		t.Error("new entry was not written beside the old one")
	}
}

func TestConfigHashCoversEveryInput(t *testing.T) {
	base := configHash([]byte("wf"), []byte("env"), "label:a")
	if base != configHash([]byte("wf"), []byte("env"), "label:a") {
		t.Error("same inputs gave different hashes")
	}
	for name, got := range map[string]string{
		"workflow":  configHash([]byte("wf2"), []byte("env"), "label:a"),
		"env":       configHash([]byte("wf"), []byte("env2"), "label:a"),
		"filter":    configHash([]byte("wf"), []byte("env"), "label:b"),
		"boundary":  configHash([]byte("w"), []byte("fenv"), "label:a"),
		"no filter": configHash([]byte("wf"), []byte("env"), ""),
	} {
		if got == base {
			t.Errorf("changing the %s did not change the hash", name)
		}
	}
}

func TestLastEnvTakesTheLastEntry(t *testing.T) {
	env := []string{"K=old", "OTHER=x", "K=new"}
	if got := lastEnv(env, "K"); got != "new" {
		t.Errorf("got %q, want new", got)
	}
	if got := lastEnv(env, "MISSING"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestStatusDocWithoutTestedSchemaVersionsStillLoads(t *testing.T) {
	// Old documents have no tested_schema_versions; new readers must ignore the absence.
	data := `{"schema_version":1,"version":"dev","engine_version":"1.26.0","pid":1,"repo":"/r","repo_name":"o/n","started_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","state":"running","workflows":[],"sleep":{"enabled":false,"awake":true}}`
	var doc statusDoc
	if err := json.Unmarshal([]byte(data), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.TestedSchemaVersions != nil {
		t.Errorf("want nil tested_schema_versions for old doc, got %v", doc.TestedSchemaVersions)
	}
}

func TestStatusJSONPassthroughKeepsTestedSchemaVersions(t *testing.T) {
	_, root := startedSup(t, "while true; do sleep 1; done\n")
	out, err := statusOutput(root, true)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["tested_schema_versions"]; !ok {
		t.Errorf("status --json missing tested_schema_versions: %s", out)
	}
	var vals []int
	if err := json.Unmarshal(raw["tested_schema_versions"], &vals); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(vals, rollup.SupportedSchemas) {
		t.Errorf("tested_schema_versions = %v, want %v", vals, rollup.SupportedSchemas)
	}
}

func TestTestedSchemaVersionsConstant(t *testing.T) {
	if !slices.Equal(testedSchemaVersions, []int{18, 35, 38, 43}) {
		t.Errorf("testedSchemaVersions = %v, want [18 35 38 43]", testedSchemaVersions)
	}
}
