//go:build acceptance

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"

	"github.com/kinged007/sortie-loop/internal/config"
	"github.com/kinged007/sortie-loop/internal/engine"
	"github.com/kinged007/sortie-loop/internal/fakegithub"
	"github.com/kinged007/sortie-loop/internal/machine"
	"github.com/kinged007/sortie-loop/internal/rollup"
)

// TestAcceptance runs the pinned sortie engine, supervised by the real
// sortie-loop binary, against an in-memory fake GitHub. The engine runs its
// `mock` agent, so no model, Docker, or real GitHub is involved.
//
// The shipped issues workflow and prompts are used unchanged except for
// these front matter fields, which are replaced because they need the
// outside world:
//   - tracker.endpoint is added and points at the fake GitHub.
//   - polling.interval_ms is 1000 instead of 60000 so the test does not wait
//     a minute per poll.
//   - hooks.after_create, before_run and after_run write marker files
//     instead of cloning the repository and pushing a branch. The clone URL
//     is always https://github.com/<repo>.git, which the fake cannot serve.
//   - agent.kind is mock and agent.command is removed, because no agent
//     binary runs. agent.max_turns is 1: the mock agent never edits labels,
//     so the issue would stay active and run all ten turns before the
//     handoff, where the shipped agent removes its own trigger.
//   - the pi: block becomes a mock: block.
//
// Scenarios d and e write machine.yaml into the isolated config directory
// while the stack runs. The supervisor reads it again when it changes. In e
// the pool entries run fake `claude` scripts, and the engine reaches them
// through `sortie-loop agent-exec`, which the supervisor sets as the agent
// command; SORTIE_AGENT_KIND replaces agent.kind: mock.
//
// Labels, states, handoff, query_filter, dispatch rules and prompts are as
// shipped. The assignee scope comes from sortie-loop itself (config default
// @me), as in a real run.
//
// The engine comes from the same sources sortie-loop prefers (SORTIE_BIN,
// then the pinned copy in the share directory, then a verified download),
// and must report the pinned version. PATH and a sibling binary are not
// consulted, because a stale `sortie` there would silently test the wrong
// engine. A missing engine fails the test.
func TestAcceptance(t *testing.T) {
	start := time.Now()
	engineBin := acceptanceEngine(t)
	loopBin := buildLoop(t)

	const (
		slug  = "acme/widgets"
		owner = "octo"
		token = "acceptance-token"
	)
	gh := fakegithub.New(owner, token)
	gh.Logf = t.Logf
	gh.AddRepo(slug, labelNames(config.DefaultLabels)...)
	srv := httptest.NewServer(gh)
	defer srv.Close()

	planned := gh.Add(fakegithub.Item{Repo: slug, Title: "Plan this", Body: "Write a plan.",
		Labels: []string{"agent:plan"}, Assignees: []string{owner}})
	needsHuman := gh.Add(fakegithub.Item{Repo: slug, Title: "Needs a person", Body: "Blocked.",
		Labels: []string{"agent:plan", "needs-human"}, Assignees: []string{owner}})
	otherUser := gh.Add(fakegithub.Item{Repo: slug, Title: "Someone else's", Body: "Not mine.",
		Labels: []string{"agent:plan"}, Assignees: []string{"someone-else"}})

	root := t.TempDir()
	home := filepath.Join(root, "home")
	repoDir := filepath.Join(root, "repo")
	writeAcceptanceRepo(t, repoDir, slug, token, srv.URL)

	dashPort := freePort(t)
	logPath := filepath.Join(root, "sortie-loop.log")
	logf, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()
	cmd := exec.Command(loopBin, "--host", "127.0.0.1", "--dashboard-port", strconv.Itoa(dashPort), repoDir)
	cmd.Dir = repoDir
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"SORTIE_BIN=" + engineBin,
		"SORTIE_LOOP_ONLY=issues",
	}
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-exited
	}()
	focus := planned
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf(format, args...)
		t.Logf("sortie-loop output (tail):\n%s", logTail(logPath, 60))
		t.Logf("fake GitHub label events for #%d: %v", focus, labelLog(gh, slug, focus))
		t.FailNow()
	}
	waitFor := func(what string, d time.Duration, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(d)
		for !ok() {
			select {
			case <-exited:
				fail("sortie-loop exited while waiting for %s", what)
			default:
			}
			if time.Now().After(deadline) {
				fail("timed out after %s waiting for %s", d, what)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	dash := fmt.Sprintf("http://127.0.0.1:%d", dashPort)

	assertHandedOff := func(t *testing.T, n int, d time.Duration) []string {
		t.Helper()
		focus = n
		waitFor(fmt.Sprintf("issue #%d to reach agent:done", n), d, func() bool {
			it, _ := gh.Get(slug, n)
			return slices.Contains(it.Labels, "agent:done") && !slices.Contains(it.Labels, "in-progress")
		})
		log := labelLog(gh, slug, n)
		addedClaim := slices.Index(log, "+in-progress")
		addedDone := slices.Index(log, "+agent:done")
		if addedClaim < 0 || addedDone < 0 || addedClaim > addedDone {
			t.Errorf("want +in-progress before +agent:done, got %v", log)
		}
		for _, want := range []string{"-in-progress", "-agent:plan"} {
			if !slices.Contains(log, want) {
				t.Errorf("label event %s missing from %v", want, log)
			}
		}
		it, _ := gh.Get(slug, n)
		if !slices.Equal(it.Labels, []string{"agent:done"}) {
			t.Errorf("final labels %v, want [agent:done]", it.Labels)
		}
		return log
	}
	writeMachine := func(content string) {
		t.Helper()
		path := filepath.Join(home, ".config", "sortie-loop", "machine.yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".tmp", []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			t.Fatal(err)
		}
	}
	status := func() statusDoc {
		t.Helper()
		sc := exec.Command(loopBin, "status", "--json", repoDir)
		sc.Env = env
		out, err := sc.Output()
		if err != nil {
			t.Fatalf("sortie-loop status: %v", err)
		}
		var doc statusDoc
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("status document: %v", err)
		}
		return doc
	}

	waitFor("the dashboard to answer", 60*time.Second, func() bool {
		return getJSON(dash+"/api/v1/fleet", new(map[string]any)) == nil
	})
	t.Logf("stack up after %s", time.Since(start).Round(time.Second))

	t.Run("a_plan_issue_is_claimed_and_handed_off", func(t *testing.T) {
		t0 := time.Now()
		log := assertHandedOff(t, planned, 90*time.Second)
		it, _ := gh.Get(slug, planned)
		if it.State != "open" || len(it.Comments) != 0 {
			t.Errorf("state %q with %d comments, want open with none (comments.on_completion is off)", it.State, len(it.Comments))
		}
		markers, _ := filepath.Glob(filepath.Join(repoDir, ".sortie", "workspaces", "issues", "*", ".after-run"))
		if len(markers) != 1 {
			t.Errorf("after_run marker files: %v, want exactly one", markers)
		}
		wantQ := "repo:" + slug + " type:issue state:open " + config.DefaultLabelFilter("issues") + " assignee:@me"
		if !hasSearch(gh, wantQ) {
			t.Errorf("no candidate search with the shipped filter plus assignee scope; want q=%q", wantQ)
		}
		t.Logf("scenario a: %s, labels %v", time.Since(t0).Round(time.Millisecond), log)
	})

	t.Run("b_excluded_issues_are_never_dispatched", func(t *testing.T) {
		t0 := time.Now()
		seen := countSearches(gh)
		waitFor("two more candidate polls", 30*time.Second, func() bool { return countSearches(gh) >= seen+2 })
		for _, n := range []int{needsHuman, otherUser} {
			it, _ := gh.Get(slug, n)
			if ev := gh.LabelEvents(slug, n); len(ev) != 0 {
				t.Errorf("issue #%d got label events %v", n, labelLog(gh, slug, n))
			}
			if slices.Contains(it.Labels, "in-progress") || slices.Contains(it.Labels, "agent:done") {
				t.Errorf("issue #%d was claimed: labels %v", n, it.Labels)
			}
			for _, r := range gh.Requests() {
				if strings.HasPrefix(r.Path, fmt.Sprintf("/repos/%s/issues/%d", slug, n)) {
					t.Errorf("issue #%d was fetched by the engine: %s %s", n, r.Method, r.Path)
				}
			}
		}
		if log := labelLog(gh, slug, planned); len(log) != 4 {
			t.Errorf("the finished issue was touched again: %v", log)
		}
		t.Logf("scenario b: %s", time.Since(t0).Round(time.Millisecond))
	})

	t.Run("c_dashboard_reads_the_run_from_the_engine_database", func(t *testing.T) {
		t0 := time.Now()
		var schema int
		db, err := sql.Open("sqlite", "file:"+filepath.Join(repoDir, ".sortie", "workflows", ".sortie-issues.db")+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&schema); err != nil {
			t.Fatalf("read engine schema: %v", err)
		}
		if !slices.Contains(rollup.SupportedSchemas, schema) {
			t.Errorf("engine database is at schema %d, which the dashboard does not read (supported %v)", schema, rollup.SupportedSchemas)
		}
		if engine.Current().Tag == engine.DefaultTag && schema != rollup.EngineSchema {
			t.Errorf("the pinned engine wrote schema %d, want %d", schema, rollup.EngineSchema)
		}

		var runs struct {
			Runs []struct {
				Project, Workflow, Status string
				AgentAdapter              string `json:"agent_adapter"`
				DisplayIdentifier         string `json:"display_identifier"`
				TurnsCompleted            int    `json:"turns_completed"`
				TotalTokens               int64  `json:"total_tokens"`
			}
		}
		// The collector copies runs on a 60 second timer.
		waitFor("the dashboard to list the run", 120*time.Second, func() bool {
			runs.Runs = nil
			return getJSON(dash+"/api/v1/runs?project="+slug, &runs) == nil && len(runs.Runs) > 0
		})
		if len(runs.Runs) != 1 {
			t.Fatalf("dashboard lists %d runs, want 1: %+v", len(runs.Runs), runs.Runs)
		}
		r := runs.Runs[0]
		if r.Project != slug || r.Workflow != "issues" || r.Status != "succeeded" || r.AgentAdapter != "mock" ||
			r.DisplayIdentifier != fmt.Sprintf("%s#%d", slug, planned) || r.TurnsCompleted != 1 || r.TotalTokens != 150 {
			t.Errorf("unexpected run: %+v", r)
		}

		var fleet struct {
			SchemaNotices []json.RawMessage `json:"schema_notices"`
			Totals        struct{ Runs int }
			Loops         []struct {
				Loop string
				Runs int
			}
		}
		if err := getJSON(dash+"/api/v1/fleet", &fleet); err != nil {
			t.Fatal(err)
		}
		if len(fleet.SchemaNotices) != 0 {
			t.Errorf("schema notices: %s", fleet.SchemaNotices)
		}
		if fleet.Totals.Runs != 1 {
			t.Errorf("fleet counts %d runs, want 1: %+v", fleet.Totals.Runs, fleet)
		}
		t.Logf("scenario c: %s, schema %d, run %+v", time.Since(t0).Round(time.Millisecond), schema, r)
	})

	t.Run("d_no_claim_outside_working_hours", func(t *testing.T) {
		t0 := time.Now()
		now := time.Now().UTC()
		window := func(from, to time.Time) string {
			return fmt.Sprintf("hours:\n  timezone: UTC\n  windows:\n    - days: [mon, tue, wed, thu, fri, sat, sun]\n      start: %q\n      end: %q\n",
				from.Format("15:04"), to.Format("15:04"))
		}
		// A one-minute window twelve hours away is closed for the whole test.
		writeMachine(window(now.Add(12*time.Hour), now.Add(12*time.Hour+time.Minute)))
		waitFor("the gate to hold the loop outside the window", 60*time.Second, func() bool {
			doc := status()
			return doc.Gate != nil && !doc.Gate.HoursOpen && len(doc.Workflows) == 1 && doc.Workflows[0].State == "held"
		})
		doc := status()
		if doc.Gate.Open || doc.Gate.Hold != "outside working hours" || doc.Workflows[0].Hold != doc.Gate.Hold {
			t.Errorf("gate %+v, workflow hold %q, want closed with hold %q", *doc.Gate, doc.Workflows[0].Hold, "outside working hours")
		}

		held := gh.Add(fakegithub.Item{Repo: slug, Title: "Outside hours", Body: "Wait for the window.",
			Labels: []string{"agent:plan"}, Assignees: []string{owner}})
		focus = held
		searches := countSearches(gh)
		time.Sleep(5 * time.Second)
		it, _ := gh.Get(slug, held)
		if ev := labelLog(gh, slug, held); len(ev) != 0 || slices.Contains(it.Labels, "in-progress") {
			t.Errorf("issue #%d was claimed outside the window: events %v, labels %v", held, ev, it.Labels)
		}
		if n := countSearches(gh); n != searches {
			t.Errorf("the engine polled %d times while the gate was closed", n-searches)
		}

		writeMachine(window(now.Add(-time.Hour), now.Add(time.Hour)))
		assertHandedOff(t, held, 90*time.Second)
		if doc := status(); doc.Gate == nil || !doc.Gate.HoursOpen || !doc.Gate.Open {
			t.Errorf("gate %+v after the window opened, want open", doc.Gate)
		}
		t.Logf("scenario d: %s", time.Since(t0).Round(time.Millisecond))
	})

	t.Run("e_usage_limit_moves_to_the_next_pool_entry", func(t *testing.T) {
		t0 := time.Now()
		fakes := filepath.Join(root, "fakes")
		if err := os.MkdirAll(fakes, 0o755); err != nil {
			t.Fatal(err)
		}
		calls := filepath.Join(fakes, "calls")
		reset := time.Now().Add(2 * time.Hour).Truncate(time.Second)
		fakeClaude := func(name, result string, isError bool, exit int) string {
			path := filepath.Join(fakes, name)
			script := fmt.Sprintf("#!/bin/sh\necho %s >> %s\necho '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"%s\"}'\n"+
				"echo '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":%t,\"result\":\"%s\",\"usage\":{\"input_tokens\":100,\"output_tokens\":50}}'\nexit %d\n",
				name, calls, name, isError, result, exit)
			if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			return path
		}
		limitMsg := fmt.Sprintf("Claude AI usage limit reached|%d", reset.Unix())
		a := fakeClaude("claude-a", limitMsg, true, 1)
		b := fakeClaude("claude-b", "All done.", false, 0)
		writeMachine(fmt.Sprintf("pool:\n  - name: claude-a\n    runtime: claude-code\n    command: %q\n  - name: claude-b\n    runtime: claude-code\n    command: %q\n", a, b))

		// The loop has to restart with the pool environment before the issue
		// exists, or a loop without the pool could take it.
		before := status().Workflows[0].PID
		waitFor("the loop to restart with the pool", 90*time.Second, func() bool {
			doc := status()
			return doc.Gate != nil && doc.Gate.Runtime == "claude-code" && len(doc.Workflows) == 1 &&
				doc.Workflows[0].State == "running" && doc.Workflows[0].PID != before
		})
		pooled := gh.Add(fakegithub.Item{Repo: slug, Title: "Hit a limit", Body: "Switch accounts.",
			Labels: []string{"agent:plan"}, Assignees: []string{owner}})
		assertHandedOff(t, pooled, 90*time.Second)

		raw, _ := os.ReadFile(calls)
		// The engine first runs a credential check through the agent command,
		// so claude-a hits its limit there and claude-b answers the check and
		// then the turn.
		got := strings.Fields(string(raw))
		if len(got) < 2 || got[0] != "claude-a" || slices.Contains(got[1:], "claude-a") {
			t.Errorf("fake claude calls %v, want one claude-a call and then only claude-b", got)
		}
		st, err := machine.LoadState(filepath.Join(home, ".local", "state", "sortie-loop", "pool.json"))
		if err != nil {
			t.Fatal(err)
		}
		sa, sb := st["claude-a"], st["claude-b"]
		if !sa.ExhaustedUntil.Equal(reset) || !strings.Contains(sa.Reason, "usage limit reached") || sa.LastUsed.IsZero() {
			t.Errorf("claude-a state %+v, want exhausted until %s with the limit message and last_used set", sa, reset)
		}
		if !sb.ExhaustedUntil.IsZero() || sb.LastUsed.IsZero() {
			t.Errorf("claude-b state %+v, want not exhausted and last_used set", sb)
		}

		db, err := sql.Open("sqlite", "file:"+filepath.Join(repoDir, ".sortie", "workflows", ".sortie-issues.db")+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		rows, err := db.Query(`SELECT attempt, agent_adapter, status FROM run_history WHERE display_identifier = ? ORDER BY id`,
			fmt.Sprintf("%s#%d", slug, pooled))
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var attempts []string
		for rows.Next() {
			var attempt int
			var adapter, status string
			if err := rows.Scan(&attempt, &adapter, &status); err != nil {
				t.Fatal(err)
			}
			attempts = append(attempts, fmt.Sprintf("%d:%s:%s", attempt, adapter, status))
		}
		if len(attempts) != 2 || !strings.HasPrefix(attempts[0], "1:claude-code:") || strings.HasSuffix(attempts[0], ":succeeded") ||
			!strings.HasSuffix(attempts[1], ":claude-code:succeeded") {
			t.Errorf("engine run history %v, want a failed attempt then a succeeded one on claude-code", attempts)
		}
		it, _ := gh.Get(slug, pooled)
		t.Logf("scenario e: %s, runs %v, %d comments, claude-a %+v", time.Since(t0).Round(time.Millisecond), attempts, len(it.Comments), sa)
	})

	if un := gh.Unexpected(); len(un) > 0 {
		t.Errorf("the fake GitHub rejected requests it does not implement:\n%s", strings.Join(un, "\n"))
	}

	// Shut down the way an operator does and check that nothing survives.
	t0 := time.Now()
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(45 * time.Second):
		t.Errorf("sortie-loop did not exit within 45s of SIGTERM")
	}
	if left := groupMembers(pgid); len(left) > 0 {
		t.Errorf("processes left in sortie-loop's process group after it exited: %v", left)
	}
	for _, p := range []int{dashPort} {
		if !probeFreePort(p) {
			t.Errorf("port %d is still in use after shutdown", p)
		}
	}
	t.Logf("shutdown %s, total %s", time.Since(t0).Round(time.Millisecond), time.Since(start).Round(time.Second))
}

func labelNames(ls []config.Label) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Name
	}
	return out
}

// acceptanceEngine returns the configured engine binary or fails the test.
func acceptanceEngine(t *testing.T) string {
	t.Helper()
	spec, err := engine.Configured()
	if err != nil {
		t.Fatal(err)
	}
	var bin string
	switch {
	case os.Getenv("SORTIE_BIN") != "":
		bin = os.Getenv("SORTIE_BIN")
	case engine.Installed(engine.ShareDirs(), spec) != "":
		bin = engine.Installed(engine.ShareDirs(), spec)
	default:
		p, err := engine.Fetch(spec)
		if err != nil {
			t.Fatalf("engine %s is not installed and could not be downloaded: %v", spec, err)
		}
		bin = p
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Fatalf("%s --version: %v", bin, err)
	}
	f := strings.Fields(string(out))
	if len(f) < 2 || f[1] != spec.Version() {
		t.Fatalf("%s reports %q, want engine %s", bin, strings.TrimSpace(string(out)), spec.Version())
	}
	t.Logf("engine %s", bin)
	return bin
}

// buildLoop compiles the sortie-loop under test.
func buildLoop(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "sortie-loop")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// writeAcceptanceRepo creates the target repo's .sortie directory: the
// config, the shipped workflows and prompts, and the acceptance variant of
// the issues workflow.
func writeAcceptanceRepo(t *testing.T, dir, slug, token, endpoint string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".sortie"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("repo: %s\ntoken: %s\n", slug, token)
	if err := os.WriteFile(filepath.Join(dir, ".sortie", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	syncWorkflows(dir)
	path := filepath.Join(dir, ".sortie", "workflows", "WORKFLOW.issues.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := acceptanceWorkflow(string(raw), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}

// acceptanceWorkflow applies the field replacements listed on
// TestAcceptance to the shipped issues workflow.
func acceptanceWorkflow(shipped, endpoint string) (string, error) {
	front, body, ok := strings.Cut(strings.TrimPrefix(shipped, "---\n"), "\n---\n")
	if !ok {
		return "", fmt.Errorf("workflow has no front matter")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(front), &doc); err != nil {
		return "", err
	}
	top := doc.Content[0]
	node := func(src string) *yaml.Node {
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(src), &n); err != nil {
			panic(err)
		}
		return n.Content[0]
	}
	child := func(m *yaml.Node, key string) *yaml.Node {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == key {
				return m.Content[i+1]
			}
		}
		return nil
	}
	set := func(m *yaml.Node, key string, val *yaml.Node) {
		if old := child(m, key); old != nil {
			*old = *val
			return
		}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, val)
	}
	del := func(m *yaml.Node, key string) {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == key {
				m.Content = append(m.Content[:i], m.Content[i+2:]...)
				return
			}
		}
	}
	for _, key := range []string{"tracker", "polling", "hooks", "agent", "pi"} {
		if child(top, key) == nil {
			return "", fmt.Errorf("shipped workflow has no %s: block; update the acceptance replacements", key)
		}
	}
	set(child(top, "tracker"), "endpoint", node("endpoint: " + endpoint).Content[1])
	set(child(top, "polling"), "interval_ms", node("interval_ms: 1000").Content[1])
	hooks := child(top, "hooks")
	set(hooks, "after_create", node("after_create: echo created > .after-create").Content[1])
	set(hooks, "before_run", node("before_run: touch .before-run").Content[1])
	set(hooks, "after_run", node("after_run: touch .after-run").Content[1])
	agent := child(top, "agent")
	set(agent, "kind", node("kind: mock").Content[1])
	set(agent, "max_turns", node("max_turns: 1").Content[1])
	del(agent, "command")
	del(top, "pi")
	set(top, "mock", node("turn_outcomes: [completed]\nturn_delay_ms: 500\ninput_tokens_per_turn: 100\noutput_tokens_per_turn: 50"))

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", err
	}
	return "---\n" + buf.String() + "---\n" + body, nil
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func getJSON(url string, v any) error {
	c := http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// labelLog renders an item's label events as +name and -name.
func labelLog(gh *fakegithub.Server, slug string, n int) []string {
	var out []string
	for _, e := range gh.LabelEvents(slug, n) {
		sign := "-"
		if e.Added {
			sign = "+"
		}
		out = append(out, sign+e.Label)
	}
	return out
}

func searchQueries(gh *fakegithub.Server) []string {
	var out []string
	for _, r := range gh.Requests() {
		if r.Path == "/search/issues" {
			v, _ := url.ParseQuery(r.Query)
			out = append(out, v.Get("q"))
		}
	}
	return out
}

func hasSearch(gh *fakegithub.Server, q string) bool { return slices.Contains(searchQueries(gh), q) }

func countSearches(gh *fakegithub.Server) int { return len(searchQueries(gh)) }

// groupMembers lists the pids still in process group pgid.
func groupMembers(pgid int) []int {
	out, err := exec.Command("ps", "-eo", "pid=,pgid=").Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == strconv.Itoa(pgid) {
			pid, _ := strconv.Atoi(f[0])
			pids = append(pids, pid)
		}
	}
	return pids
}

func logTail(path string, n int) string {
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
