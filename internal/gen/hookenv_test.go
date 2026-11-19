package gen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func hookScripts(t *testing.T, doc []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	var key string
	var body []string
	flush := func() {
		if key != "" {
			out[key] = strings.Join(body, "\n")
		}
		key, body = "", nil
	}
	in := false
	for _, l := range strings.Split(string(doc), "\n") {
		switch {
		case l == "hooks:":
			in = true
		case in && strings.HasPrefix(l, "  ") && strings.HasSuffix(l, ": |"):
			flush()
			key = strings.TrimSuffix(strings.TrimSpace(l), ": |")
		case in && key != "" && strings.HasPrefix(l, "    "):
			body = append(body, l[4:])
		case in && l != "" && !strings.HasPrefix(l, "    "):
			flush()
			if !strings.HasPrefix(l, "  ") {
				in = false
			}
		}
	}
	flush()
	return out
}

func TestHookPreamble(t *testing.T) {
	p, _, err := ProfileFor("", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, agents := variantFixture(t, `repo: owner/repo
tracker:
  kind: github
  api_key: { from_env: JIG_TRACKER_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
  handoff_state: agent:done
agents:
  builder: { runtime: claude-code, model: claude-haiku-5-5 }
work:
  issues:
    query_filter: "label:agent:build,in-progress -label:needs-human"
    hooks: { before_run: "git checkout -B auto/$JIG_ISSUE_IDENTIFIER", after_run: ./hooks/post-run.sh }
    agent: builder
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: ./prompts/custom.md }
`)
	hooks := hookScripts(t, renderEntry(t, p, cfg, agents, "issues"))

	for _, name := range []string{"after_create", "before_run", "after_run"} {
		if !strings.HasPrefix(hooks[name], "JIG_ISSUE_ID=$SORTIE_ISSUE_ID ") {
			t.Errorf("%s has no preamble:\n%s", name, hooks[name])
		}
	}
	if !strings.Contains(hooks["after_create"], "$JIG_TRACKER_PROJECT") {
		t.Errorf("default after_create does not use JIG_TRACKER_PROJECT:\n%s", hooks["after_create"])
	}
	if strings.Contains(hooks["before_run"], "JIG_SELF_REVIEW") {
		t.Error("before_run has the after_run variables")
	}
	if !strings.Contains(hooks["after_run"], "export JIG_SELF_REVIEW_STATUS JIG_SELF_REVIEW_SUMMARY_PATH") {
		t.Errorf("after_run has no self-review variables:\n%s", hooks["after_run"])
	}

	// A path hook is a command line, so the exported names reach the file.
	file := filepath.Join(t.TempDir(), "post-run.sh")
	body := "#!/bin/sh\nprintf '%s|%s|%s|%s' \"$JIG_ISSUE_IDENTIFIER\" \"$JIG_WORKSPACE\" \"$JIG_TRACKER_PROJECT\" \"$JIG_SELF_REVIEW_STATUS\"\n"
	if err := os.WriteFile(file, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(hooks["after_run"], "./hooks/post-run.sh", file, 1)
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"SORTIE_ISSUE_IDENTIFIER=o/r#7", "SORTIE_WORKSPACE=/w", "SORTIE_TRACKER_PROJECT=o/r", "SORTIE_SELF_REVIEW_STATUS=passed"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if string(out) != "o/r#7|/w|o/r|passed" {
		t.Errorf("hook output = %q", out)
	}
}
