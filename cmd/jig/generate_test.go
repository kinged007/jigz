package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/gen"
)

func writeGenConfig(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".jigz", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JIG_TEST_TOKEN", "test-token-12345678")
	return root
}

func genBase() string {
	return `repo: owner/repo
tracker:
  kind: github
  api_key: { from_env: JIG_TEST_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
  handoff_state: agent:done
agents:
  builder: { runtime: claude-code, model: claude-haiku-5-5 }
`
}

func testEngineBin(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("JIG_ENGINE_BIN"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	for _, n := range []string{"sortie-kinged007+sortie-1.28.1", "sortie-1.28.1"} {
		p := filepath.Join(home, ".local", "share", "jigz", n)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	t.Skip("no cached engine binary")
	return ""
}

// TestGenerateNoWork is the legacy path: without work entries nothing
// is generated and nothing fails.
func TestGenerateNoWork(t *testing.T) {
	root := writeGenConfig(t, "repo: owner/repo\n")
	docs, warnings, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 0 || len(warnings) != 0 {
		t.Errorf("got %d docs %d warnings, want none", len(docs), len(warnings))
	}
}

// TestGeneratePromote renders, promotes, and detects drift with a .bak.
func TestGeneratePromote(t *testing.T) {
	root := writeGenConfig(t, genBase()+`work:
  issues:
    query_filter: "label:agent:build,in-progress -label:needs-human"
    agent: builder
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
`)
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Loop != "issues" {
		t.Fatalf("docs = %+v, want one issues loop", docs)
	}
	sum, err := promoteDocs(root, docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Generated) != 1 {
		t.Errorf("generated = %v", sum.Generated)
	}
	if _, err := os.Stat(gen.DocPath(root, "issues")); err != nil {
		t.Errorf("doc missing: %v", err)
	}
	if _, err := ensurePrompts(root, docs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".jigz", "workflows", "prompts", "build.md")); err != nil {
		t.Errorf("prompt missing: %v", err)
	}

	reports, err := driftReports(root)
	if err != nil {
		t.Fatal(err)
	}
	if reports["issues"] != "" {
		t.Errorf("fresh doc drifts: %q", reports["issues"])
	}

	// A hand edit is drift, and promoting keeps it as .bak.
	path := gen.DocPath(root, "issues")
	raw, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(raw, []byte("# hand edit\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	reports, err = driftReports(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reports["issues"], "hand edit") {
		t.Errorf("drift report = %q, want the hand edit", reports["issues"])
	}
	sum, err = promoteDocs(root, docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Drifted) != 1 {
		t.Errorf("drifted = %v", sum.Drifted)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil || !strings.Contains(string(bak), "hand edit") {
		t.Errorf("bak keeps the hand edit: %v", err)
	}
}

// TestGenerateUnmanaged never writes the document.
func TestGenerateUnmanaged(t *testing.T) {
	root := writeGenConfig(t, genBase()+`work:
  issues:
    managed: false
    query_filter: "label:agent:build,in-progress -label:needs-human"
    agent: builder
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
`)
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 0 {
		t.Errorf("unmanaged entry generated %d docs", len(docs))
	}
	reports, err := driftReports(root)
	if err != nil {
		t.Fatal(err)
	}
	if reports["issues"] != "unmanaged" {
		t.Errorf("report = %q, want unmanaged", reports["issues"])
	}
	if _, err := os.Stat(gen.DocPath(root, "issues")); !os.IsNotExist(err) {
		t.Errorf("unmanaged doc written")
	}
}

// TestGenerateMissing reports a managed loop with no file.
func TestGenerateMissing(t *testing.T) {
	root := writeGenConfig(t, genBase()+`work:
  issues:
    query_filter: "label:agent:build,in-progress -label:needs-human"
    agent: builder
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
`)
	reports, err := driftReports(root)
	if err != nil {
		t.Fatal(err)
	}
	if reports["issues"] != "missing" {
		t.Errorf("report = %q, want missing", reports["issues"])
	}
}

// TestGenerateAllEndToEnd renders, validates, promotes and ensures
// prompts in one call.
func TestGenerateAllEndToEnd(t *testing.T) {
	t.Setenv("JIG_ENGINE_BIN", testEngineBin(t))
	root := writeGenConfig(t, genBase()+`work:
  issues:
    query_filter: "label:agent:build,in-progress -label:needs-human"
    agent: builder
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
`)
	sum, err := generateAll(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Generated) != 1 || len(sum.Prompts) == 0 {
		t.Errorf("summary = %+v", sum)
	}
	if _, err := os.Stat(gen.DocPath(root, "issues")); err != nil {
		t.Errorf("doc missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".jigz", "data")); err != nil {
		t.Errorf("data dir missing: %v", err)
	}
}

// TestGenerateAllWritesNothingOnError promotes no document when
// rendering fails: a cross-group next rejects the whole generation.
func TestGenerateAllWritesNothingOnError(t *testing.T) {
	root2 := writeGenConfig(t, `repo: owner/repo
tracker:
  kind: github
  api_key: { from_env: JIG_TEST_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
  handoff_state: agent:done
agents:
  planner: { runtime: claude-code, model: claude-sonnet-5-5 }
  builder: { runtime: claude-code, model: claude-haiku-5-5 }
work:
  delivery:
    query_filter: "label:in-progress -label:needs-human"
    rules:
      - { name: plan, match: { labels: [agent:plan] }, prompt: prompts/plan.md, agent: planner, next: chain }
      - { name: chain, stage: stage-build, prompt: prompts/build.md, agent: builder }
`)
	if _, err := generateAll(root2); err == nil {
		t.Fatalf("cross-group next generated")
	}
	if _, err := os.Stat(gen.DocPath(root2, "delivery-planner")); !os.IsNotExist(err) {
		t.Errorf("doc written despite generation failure")
	}
}

// TestValidateOutputReportsDrift wires the generation checks into jig
// validate: clean passes, a hand edit fails the loop's check.
func TestValidateOutputReportsDrift(t *testing.T) {
	t.Setenv("JIG_ENGINE_BIN", testEngineBin(t))
	root := writeGenConfig(t, genBase()+`work:
  issues:
    query_filter: "label:agent:build,in-progress -label:needs-human"
    agent: builder
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
`)
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promoteDocs(root, docs); err != nil {
		t.Fatal(err)
	}
	if _, err := ensurePrompts(root, docs); err != nil {
		t.Fatal(err)
	}
	if out, ok := validateOutputFor(root, false); !ok {
		t.Errorf("clean repo fails validate:\n%s", out)
	}
	path := gen.DocPath(root, "issues")
	raw, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(raw, []byte("# owner tweak\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	out, ok := validateOutputFor(root, false)
	if ok {
		t.Errorf("drifted repo passes validate:\n%s", out)
	} else if !strings.Contains(out, "FAIL generation:issues") {
		t.Errorf("no drift check in output:\n%s", out)
	}
}

// TestValidateEngineProfile rejects engines the profile table does not
// support.
func TestValidateEngineProfile(t *testing.T) {
	root := writeGenConfig(t, `repo: owner/repo
engine: { repo: kinged007/sortie, version: v1.26.0 }
tracker:
  kind: github
  api_key: { from_env: JIG_TEST_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
  handoff_state: agent:done
agents:
  builder: { runtime: claude-code, model: claude-haiku-5-5 }
work:
  issues:
    query_filter: "label:agent:build,in-progress -label:needs-human"
    agent: builder
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
`)
	out, ok := validateOutputFor(root, false)
	if ok {
		t.Errorf("too-old engine passes validate:\n%s", out)
	} else if !strings.Contains(out, "FAIL engine") {
		t.Errorf("no engine check in output:\n%s", out)
	}
}

// TestGenerateSkipsUnsupportedKinds leaves loops whose kind the engine
// profile does not register without a document: Example 2 on upstream,
// where pi-api is skipped with the reason shown.
func TestGenerateSkipsUnsupportedKinds(t *testing.T) {
	root := writeGenConfig(t, `repo: owner/repo
engine: { repo: sortie-ai/sortie, version: v1.29.0 }
tracker:
  kind: github
  api_key: { from_env: JIG_TEST_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
  handoff_state: agent:done
agents:
  pi-api: { runtime: pi, model: sonnet }
work:
  issues:
    query_filter: "label:agent:build,in-progress -label:needs-human"
    agent: pi-api
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
`)
	docs, warnings, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 0 {
		t.Errorf("unsupported agent generated %d docs", len(docs))
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "does not support") {
		t.Errorf("warnings = %v, want the skip reason", warnings)
	}
}

// TestValidateDocsEngine runs the rendered document through the real
// fork engine: valid with no warnings.
func TestValidateDocsEngine(t *testing.T) {
	t.Setenv("JIG_ENGINE_BIN", testEngineBin(t))
	root := writeGenConfig(t, genBase()+`work:
  issues:
    query_filter: "label:agent:build,in-progress -label:needs-human"
    agent: builder
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
`)
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ensurePrompts(root, docs); err != nil {
		t.Fatal(err)
	}
	if warn, err := validateDocs(root, docs); err != nil || warn != "" {
		t.Errorf("validate: %v %q", err, warn)
	}
	// A broken render fails validation instead of being promoted.
	bad := []generatedDoc{{Loop: "issues", Text: []byte("---\nnot: [valid\n"), Managed: true}}
	if _, err := validateDocs(root, bad); err == nil {
		t.Errorf("broken document validated")
	}
}
