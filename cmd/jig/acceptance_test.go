//go:build acceptance

package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"

	"github.com/kinged007/jigz/internal/collector"
	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/engine"
	"github.com/kinged007/jigz/internal/fakegithub"
	"github.com/kinged007/jigz/internal/fakehub"
	"github.com/kinged007/jigz/internal/hubproto"
	"github.com/kinged007/jigz/internal/machine"
	"github.com/kinged007/jigz/internal/rollup"
)

// TestAcceptance runs the pinned sortie engine, supervised by the real
// Jigz binary, against an in-memory fake GitHub. The engine runs its
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
// through `jig agent-exec`, which the supervisor sets as the agent
// command; SORTIE_AGENT_KIND replaces agent.kind: mock.
//
// Labels, states, handoff, query_filter, dispatch rules and prompts are as
// shipped. The assignee scope comes from Jigz itself (config default
// @me), as in a real run.
//
// The engine comes from the same sources Jigz prefers (JIG_ENGINE_BIN,
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
	// Scenario f reads questions through the dashboard's scanner.
	cfgFile, err := os.OpenFile(filepath.Join(repoDir, ".jigz", "config.yaml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(cfgFile, "github:\n  questions_interval: 1m\n  api_url: %s\n", srv.URL)
	if cerr := cfgFile.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}

	dashPort := freePort(t)
	logPath := filepath.Join(root, "jig.log")
	logf, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()
	cmd := exec.Command(loopBin, "--host", "127.0.0.1", "--dashboard-port", strconv.Itoa(dashPort), repoDir)
	cmd.Dir = repoDir
	env := append(isolatedEnv(home), "JIG_ENGINE_BIN="+engineBin, "JIG_ONLY=issues")
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
	run := &acceptanceRun{t: t, gh: gh, slug: slug, name: "Jigz", logPath: logPath, exited: exited, focus: planned}
	waitFor, assertHandedOff := run.waitFor, run.assertHandedOff
	dash := fmt.Sprintf("http://127.0.0.1:%d", dashPort)

	writeMachine := func(content string) {
		t.Helper()
		path := filepath.Join(home, ".config", "jigz", "machine.yaml")
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
			t.Fatalf("jig status: %v", err)
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
		markers, _ := filepath.Glob(filepath.Join(repoDir, ".jigz", "workspaces", "issues", "*", ".after-run"))
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
				// The dashboard's question scanner reads the comments of
				// needs-human items; the engine never does.
				if n == needsHuman && r.Method == http.MethodGet && strings.HasSuffix(r.Path, "/comments") {
					continue
				}
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

	t.Run("c_dashboard_reads_the_run_through_the_engine_api", func(t *testing.T) {
		t0 := time.Now()
		var schema int
		db, err := sql.Open("sqlite", "file:"+filepath.Join(repoDir, ".jigz", "workflows", "jigz-issues.db")+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&schema); err != nil {
			t.Fatalf("read engine schema: %v", err)
		}
		if schema < rollup.OldestSchema || schema > rollup.NewestSchema() {
			t.Errorf("engine database is at schema %d, which the dashboard does not read (%d to %d)", schema, rollup.OldestSchema, rollup.NewestSchema())
		}
		if want, known := rollup.SchemaOf(engine.Current().Tag); known && schema != want {
			t.Errorf("engine %s wrote schema %d, want %d", engine.Current().Tag, schema, want)
		}

		var runs struct {
			Runs []struct {
				ID                        int64
				Project, Workflow, Status string
				AgentAdapter              string `json:"agent_adapter"`
				DisplayIdentifier         string `json:"display_identifier"`
				TurnsCompleted            int    `json:"turns_completed"`
				TotalTokens               int64  `json:"total_tokens"`
			}
		}
		// The collector copies runs on a 60 second timer. The engine is
		// running, so it reads them from the engine's HTTP API.
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

		out, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), "collector: "+slug+"/issues: runs copied from the engine API: 1") {
			t.Errorf("the dashboard did not copy the run from the engine API; Jigz output (tail):\n%s", logTail(logPath, 60))
		}

		for _, page := range []struct {
			path string
			want []string
		}{
			{"/runs", []string{fmt.Sprintf("%s#%d", slug, planned)}},
			{fmt.Sprintf("/runs/%s/issues/%d", slug, r.ID), []string{"Session id", "Workflow file", "Handoff"}},
		} {
			body := getBody(t, dash+page.path)
			for _, want := range page.want {
				if !strings.Contains(body, want) {
					t.Errorf("%s lacks %q", page.path, want)
				}
			}
			for _, gone := range []string{">Workspace<", ">Prompt<", "API requests", "absence counter reset", "handoff counter reset"} {
				if strings.Contains(body, gone) {
					t.Errorf("%s still shows %q", page.path, gone)
				}
			}
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
		run.focus = held
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
		st, err := machine.LoadState(filepath.Join(home, ".local", "state", "jigz", "pool.json"))
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

		db, err := sql.Open("sqlite", "file:"+filepath.Join(repoDir, ".jigz", "workflows", "jigz-issues.db")+"?mode=ro")
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

	t.Run("f_blocking_question_stops_and_answer_resumes", func(t *testing.T) {
		t0 := time.Now()
		const question = `<!-- agent-question id="q-1" audience="product" blocking="true" resume="agent:plan" -->
**Question:** Which colour?

**Suggested answer:** Blue.
<!-- /agent-question -->`
		const answerBlock = "<!-- agent-answer id=%q -->\nGreen.\n<!-- /agent-answer -->"
		// The mock agent writes no comments and edits no labels, so the test
		// writes what an agent and a person write.
		a := gh.Add(fakegithub.Item{Repo: slug, Title: "Ask first", Body: "Needs an answer.",
			Labels: []string{"agent:plan"}, Assignees: []string{owner}})
		gh.AddComment(slug, a, fakegithub.Comment{Author: owner, Association: "OWNER", Body: question})
		assertHandedOff(t, a, 90*time.Second)
		gh.AddLabelAsPerson(slug, a, "needs-human", owner)
		if it, _ := gh.Get(slug, a); !slices.Contains(it.Labels, "needs-human") || !slices.Contains(it.Labels, "agent:done") || slices.Contains(it.Labels, "agent:plan") {
			t.Fatalf("issue #%d labels %v, want needs-human and agent:done without agent:plan", a, it.Labels)
		}

		b := gh.Add(fakegithub.Item{Repo: slug, Title: "Parked without a question", Body: "Blocked.",
			Labels: []string{"agent:plan", "agent:done", "needs-human"}, Assignees: []string{owner}})
		gh.AddComment(slug, b, fakegithub.Comment{Author: owner, Association: "OWNER", Body: "Waiting for a person."})

		const question2 = `<!-- agent-question id="q-2" audience="product" blocking="true" resume="agent:plan" -->
**Question:** Which size?
<!-- /agent-question -->`
		c := gh.Add(fakegithub.Item{Repo: slug, Title: "Answered before parking", Body: "Old answer.",
			Labels: []string{"agent:done"}, Assignees: []string{owner}})
		gh.AddComment(slug, c, fakegithub.Comment{Author: owner, Association: "OWNER", Body: question2})
		gh.AddComment(slug, c, fakegithub.Comment{Author: "alice", Association: "COLLABORATOR", Body: fmt.Sprintf(answerBlock, "q-2")})
		time.Sleep(2 * time.Second)
		gh.AddLabelAsPerson(slug, c, "needs-human", owner)

		claims := func(n int) int {
			count := 0
			for _, e := range labelLog(gh, slug, n) {
				if e == "+in-progress" {
					count++
				}
			}
			return count
		}
		seen := countSearches(gh)
		waitFor("three more candidate polls", 30*time.Second, func() bool { return countSearches(gh) >= seen+3 })
		if n := claims(a); n != 1 {
			t.Errorf("issue #%d was claimed %d times while parked, want once before parking: %v", a, n, labelLog(gh, slug, a))
		}
		for _, n := range []int{b, c} {
			if claims(n) != 0 {
				t.Errorf("issue #%d was claimed: %v", n, labelLog(gh, slug, n))
			}
		}

		parkedEvents, parkedRequests := len(labelLog(gh, slug, a)), len(gh.LabelRequests(slug, a))
		// A plain comment and an answer from an unauthorized author do not resume.
		gh.AddComment(slug, a, fakegithub.Comment{Author: "alice", Association: "COLLABORATOR", Body: "Green, please."})
		gh.AddComment(slug, a, fakegithub.Comment{Author: "mallory", Association: "NONE", Body: fmt.Sprintf(answerBlock, "q-1")})
		type apiQs struct {
			Questions []struct {
				Project, QID, State string
				Number              int
				Blocking            bool
				IgnoredAnswers      []struct{ Author string } `json:"ignored_answers"`
			}
			Items []struct {
				Project  string
				Number   int
				Problems []struct{ Kind string }
			}
		}
		var qs apiQs
		waitFor("the scanner to ignore the answer from mallory", 150*time.Second, func() bool {
			qs = apiQs{}
			if getJSON(dash+"/api/v1/questions", &qs) != nil {
				return false
			}
			for _, q := range qs.Questions {
				if q.Project == slug && q.Number == a && len(q.IgnoredAnswers) == 1 && q.IgnoredAnswers[0].Author == "mallory" {
					return true
				}
			}
			return false
		})
		var answered apiQs
		if err := getJSON(dash+"/api/v1/questions?state=answered", &answered); err != nil {
			t.Fatal(err)
		}
		qs.Questions = append(qs.Questions, answered.Questions...)
		var onA, onC int
		for _, q := range qs.Questions {
			switch {
			case q.Project == slug && q.Number == a:
				onA++
				if q.QID != "q-1" || q.State != "open" || !q.Blocking {
					t.Errorf("question on #%d: %+v, want q-1 open and blocking", a, q)
				}
			case q.Project == slug && q.Number == b:
				t.Errorf("question listed for #%d: %+v", b, q)
			case q.Project == slug && q.Number == c:
				onC++
				if q.QID != "q-2" || q.State != "answered" {
					t.Errorf("question on #%d: %+v, want q-2 answered", c, q)
				}
			}
		}
		if onA != 1 || onC != 1 {
			t.Errorf("%d questions listed for #%d and %d for #%d, want 1 each: %+v", onA, a, onC, c, qs.Questions)
		}
		parkedAgain := false
		for _, it := range qs.Items {
			if it.Project != slug {
				continue
			}
			if it.Number == b {
				t.Errorf("problem item listed for #%d: %+v", b, qs.Items)
			}
			if it.Number == c {
				for _, p := range it.Problems {
					parkedAgain = parkedAgain || p.Kind == "parked_again"
				}
			}
		}
		if !parkedAgain {
			t.Errorf("no parked_again problem for #%d: %+v", c, qs.Items)
		}
		for _, n := range []int{b, c} {
			if reqs := gh.LabelRequests(slug, n); len(reqs) != 0 {
				t.Errorf("label requests for #%d before an authorized answer: %+v", n, reqs)
			}
		}
		if ev := labelLog(gh, slug, a); len(ev) != parkedEvents {
			t.Errorf("label events on #%d after parking: %v", a, ev[parkedEvents:])
		}
		for _, r := range gh.LabelRequests(slug, a)[parkedRequests:] {
			t.Errorf("label request on #%d before an authorized answer: %+v", a, r)
		}

		page := getBody(t, dash+"/questions")
		if strings.Contains(strings.ToLower(page), `method="post"`) || strings.Contains(page, "/questions/answer") {
			t.Errorf("/questions has a form")
		}
		for _, path := range []string{"/questions/answer", "/questions/refresh"} {
			resp, err := http.Post(dash+path, "application/x-www-form-urlencoded", strings.NewReader("qid=q-1"))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("POST %s: %s, want 404", path, resp.Status)
			}
		}

		// An authorized answer posted after needs-human resumes the item.
		gh.AddComment(slug, a, fakegithub.Comment{Author: "alice", Association: "COLLABORATOR", Body: fmt.Sprintf(answerBlock, "q-1")})
		labelReqs, events := len(gh.LabelRequests(slug, a)), len(labelLog(gh, slug, a))
		it, _ := gh.Get(slug, a)
		comments := len(it.Comments)
		run.focus = a
		waitFor(fmt.Sprintf("the scanner to resume issue #%d", a), 150*time.Second, func() bool {
			return len(gh.LabelRequests(slug, a)) >= labelReqs+3
		})
		var got []string
		for _, r := range gh.LabelRequests(slug, a)[labelReqs : labelReqs+3] {
			got = append(got, fmt.Sprintf("%s %s %s %d", r.Method, r.Path, strings.TrimSpace(r.Body), r.Status))
		}
		base := fmt.Sprintf("/repos/%s/issues/%d/labels", slug, a)
		want := []string{
			fmt.Sprintf(`POST %s {"labels":["agent:plan"]} 200`, base),
			fmt.Sprintf("DELETE %s/agent%%3Adone  200", base),
			fmt.Sprintf("DELETE %s/needs-human  200", base),
		}
		if !slices.Equal(got, want) {
			t.Errorf("label requests on #%d after the answer:\n%s\nwant:\n%s", a, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if ev := labelLog(gh, slug, a)[events:]; len(ev) < 3 || !slices.Equal(ev[:3], []string{"+agent:plan", "-agent:done", "-needs-human"}) {
			t.Errorf("label events on #%d after the answer: %v, want +agent:plan, -agent:done, -needs-human first", a, ev)
		}
		if it, _ := gh.Get(slug, a); len(it.Comments) != comments {
			t.Errorf("the token posted comments on #%d: %+v", a, it.Comments[comments:])
		}
		for _, r := range gh.Requests() {
			if r.Method == http.MethodPut {
				t.Errorf("label PUT sent: %s %s", r.Method, r.Path)
			}
		}

		waitFor(fmt.Sprintf("issue #%d to be claimed and handed off again", a), 90*time.Second, func() bool {
			it, _ := gh.Get(slug, a)
			return claims(a) == 2 && slices.Contains(it.Labels, "agent:done") && !slices.Contains(it.Labels, "in-progress")
		})
		if it, _ := gh.Get(slug, a); !slices.Equal(it.Labels, []string{"agent:done"}) {
			t.Errorf("issue #%d labels %v after the second run, want [agent:done]", a, it.Labels)
		}
		var runs struct {
			Runs []struct {
				Status            string
				Attempt           int
				DisplayIdentifier string `json:"display_identifier"`
			}
		}
		id := fmt.Sprintf("%s#%d", slug, a)
		succeeded := func() int {
			n := 0
			for _, r := range runs.Runs {
				if r.DisplayIdentifier == id && r.Status == "succeeded" && r.Attempt == 1 {
					n++
				}
			}
			return n
		}
		waitFor("the dashboard to list the second run of #"+strconv.Itoa(a), 120*time.Second, func() bool {
			runs.Runs = nil
			return getJSON(dash+"/api/v1/runs?project="+slug, &runs) == nil && succeeded() >= 2
		})
		if n := succeeded(); n != 2 {
			t.Errorf("%d succeeded attempt-1 runs of %s, want 2: %+v", n, id, runs.Runs)
		}

		for _, n := range []int{b, c} {
			if it, _ := gh.Get(slug, n); !slices.Contains(it.Labels, "needs-human") || !slices.Contains(it.Labels, "agent:done") {
				t.Errorf("issue #%d labels %v, want needs-human and agent:done kept", n, it.Labels)
			}
			if reqs := gh.LabelRequests(slug, n); len(reqs) != 0 {
				t.Errorf("label requests for #%d: %+v", n, reqs)
			}
		}
		if un := gh.Unexpected(); len(un) > 0 {
			t.Errorf("the fake GitHub rejected requests:\n%s", strings.Join(un, "\n"))
		}
		t.Logf("scenario f: %s, labels %v", time.Since(t0).Round(time.Millisecond), labelLog(gh, slug, a))
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
		t.Errorf("Jigz did not exit within 45s of SIGTERM")
	}
	if left := groupMembers(pgid); len(left) > 0 {
		t.Errorf("processes left in Jigz's process group after it exited: %v", left)
	}
	for _, p := range []int{dashPort} {
		if !probeFreePort(p) {
			t.Errorf("port %d is still in use after shutdown", p)
		}
	}
	t.Logf("shutdown %s, total %s", time.Since(t0).Round(time.Millisecond), time.Since(start).Round(time.Second))
}

// TestAcceptanceUpstreamEngine runs the stack on the upstream engine release
// sortie-ai/sortie v1.29.0, selected by the engine block of machine.yaml. The
// engine has no github-pr tracker, so the issues loop runs and the PR loop is
// disabled with the reason. The workflow is the same acceptance variant of the
// issues workflow as in TestAcceptance. The PR workflow is installed and
// enabled, but its loop never starts, so the fake GitHub needs no PR routes.
//
// The engine is resolved as for the default one: the installed copy in the
// share directory, else a verified download. A failed download fails the test.
func TestAcceptanceUpstreamEngine(t *testing.T) {
	start := time.Now()
	t.Setenv("JIG_ENGINE_BIN", "")
	t.Setenv("JIG_ENGINE_REPO", "sortie-ai/sortie")
	t.Setenv("JIG_ENGINE_VERSION", "v1.29.0")
	engineBin := acceptanceEngine(t)
	spec, err := engine.Configured()
	if err != nil {
		t.Fatal(err)
	}
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

	root := t.TempDir()
	home := filepath.Join(root, "home")
	repoDir := filepath.Join(root, "repo")
	writeAcceptanceRepo(t, repoDir, slug, token, srv.URL)

	// The loop resolves the engine from the isolated share directory and
	// the engine block of machine.yaml, not from JIG_ENGINE_BIN.
	shareDir := filepath.Join(home, ".local", "share", "jigz")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(engineBin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec.CachePath(shareDir), raw, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgDir := filepath.Join(home, ".config", "jigz")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	machineYAML := fmt.Sprintf("engine:\n  repo: %s\n  version: %s\n", spec.Repo, spec.Tag)
	if err := os.WriteFile(filepath.Join(cfgDir, "machine.yaml"), []byte(machineYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	dashPort := freePort(t)
	logPath := filepath.Join(root, "jig.log")
	logf, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()
	cmd := exec.Command(loopBin, "--host", "127.0.0.1", "--dashboard-port", strconv.Itoa(dashPort), repoDir)
	cmd.Dir = repoDir
	env := isolatedEnv(home)
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
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf(format, args...)
		t.Logf("Jigz output (tail):\n%s", logTail(logPath, 60))
		t.Logf("fake GitHub label events for #%d: %v", planned, labelLog(gh, slug, planned))
		t.FailNow()
	}
	waitFor := func(what string, d time.Duration, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(d)
		for !ok() {
			select {
			case <-exited:
				fail("Jigz exited while waiting for %s", what)
			default:
			}
			if time.Now().After(deadline) {
				fail("timed out after %s waiting for %s", d, what)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	dash := fmt.Sprintf("http://127.0.0.1:%d", dashPort)
	waitFor("the dashboard to answer", 60*time.Second, func() bool {
		return getJSON(dash+"/api/v1/fleet", new(map[string]any)) == nil
	})

	t.Run("a_issue_is_handed_off_and_the_pr_loop_is_disabled", func(t *testing.T) {
		t0 := time.Now()
		waitFor("the issue to reach agent:done", 90*time.Second, func() bool {
			it, _ := gh.Get(slug, planned)
			return slices.Contains(it.Labels, "agent:done") && !slices.Contains(it.Labels, "in-progress")
		})
		log := labelLog(gh, slug, planned)
		if c, d := slices.Index(log, "+in-progress"), slices.Index(log, "+agent:done"); c < 0 || d < 0 || c > d {
			t.Errorf("want +in-progress before +agent:done, got %v", log)
		}
		if it, _ := gh.Get(slug, planned); !slices.Equal(it.Labels, []string{"agent:done"}) {
			t.Errorf("final labels %v, want [agent:done]", it.Labels)
		}

		sc := exec.Command(loopBin, "status", "--json", repoDir)
		sc.Env = env
		out, err := sc.Output()
		if err != nil {
			t.Fatalf("jig status: %v", err)
		}
		var doc statusDoc
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("status document: %v", err)
		}
		if doc.EngineVersion != spec.Version() {
			t.Errorf("status engine_version %q, want %q", doc.EngineVersion, spec.Version())
		}
		states := map[string]workflowStatus{}
		for _, w := range doc.Workflows {
			states[w.LoopType] = w
		}
		const wantOff = "PR workflow is off: engine sortie-ai/sortie v1.29.0 has no github-pr tracker."
		if w := states["issues"]; w.State != "running" {
			t.Errorf("issues loop state %q, want running: %+v", w.State, doc.Workflows)
		}
		if w := states["prs"]; w.State != "disabled" || w.Hold != wantOff || w.PID != 0 {
			t.Errorf("PR loop %+v, want state disabled, pid 0, hold %q (workflows %+v)", w, wantOff, doc.Workflows)
		}
		for _, r := range gh.Requests() {
			if strings.Contains(r.Query, "type:pr") || strings.Contains(r.Path, "/pulls") {
				t.Errorf("the PR loop reached GitHub: %s %s?%s", r.Method, r.Path, r.Query)
			}
		}

		vc := exec.Command(loopBin, "validate", repoDir)
		vc.Env = env
		vout, err := vc.CombinedOutput()
		if err != nil {
			t.Errorf("validate failed: %v\n%s", err, vout)
		}
		if !strings.Contains(string(vout), "disabled: "+wantOff) {
			t.Errorf("validate output lacks the disabled line:\n%s", vout)
		}

		live := getBody(t, dash+"/live")
		if !strings.Contains(live, wantOff) {
			t.Errorf("Operations page lacks %q", wantOff)
		}
		settings := getBody(t, dash+"/settings")
		for _, kind := range []string{"github", "mock", "claude-code"} {
			if !strings.Contains(settings, kind) {
				t.Errorf("Settings page does not list the registered kind %q", kind)
			}
		}
		t.Logf("upstream scenario a: %s, engine %s", time.Since(t0).Round(time.Millisecond), spec)
	})

	t.Run("b_dashboard_reads_the_run_through_the_engine_api", func(t *testing.T) {
		t0 := time.Now()
		var runs struct {
			Runs []struct {
				Project, Workflow, Status string
				AgentAdapter              string `json:"agent_adapter"`
			}
		}
		waitFor("the dashboard to list the run", 120*time.Second, func() bool {
			runs.Runs = nil
			return getJSON(dash+"/api/v1/runs?project="+slug, &runs) == nil && len(runs.Runs) > 0
		})
		if len(runs.Runs) != 1 {
			t.Fatalf("dashboard lists %d runs, want 1: %+v", len(runs.Runs), runs.Runs)
		}
		if r := runs.Runs[0]; r.Workflow != "issues" || r.Status != "succeeded" || r.AgentAdapter != "mock" {
			t.Errorf("unexpected run: %+v", r)
		}
		if out, _ := os.ReadFile(logPath); !strings.Contains(string(out), "collector: "+slug+"/issues: runs copied from the engine API: 1") {
			t.Errorf("the dashboard did not copy the run from the engine API; Jigz output (tail):\n%s", logTail(logPath, 60))
		}
		var fleet struct {
			SchemaNotices []json.RawMessage `json:"schema_notices"`
		}
		if err := getJSON(dash+"/api/v1/fleet", &fleet); err != nil {
			t.Fatal(err)
		}
		if len(fleet.SchemaNotices) != 0 {
			t.Errorf("schema notices: %s", fleet.SchemaNotices)
		}
		t.Logf("upstream scenario b: %s, total %s", time.Since(t0).Round(time.Millisecond), time.Since(start).Round(time.Second))
	})
}

// TestAcceptanceConnected runs `jig join` and `jig daemon` against the fake
// hub and the fake GitHub, with the pinned engine and its mock agent. The
// hub serves the acceptance variant of the issues workflow (see
// TestAcceptance) and the shipped prompts for acme/widgets, member octo.
//
// machine.yaml sets the idle time and the fallback interval to 1m, the
// shortest value machine.Load accepts, and the fake hub asks for a heartbeat
// every 5 seconds, the shortest it serves.
func TestAcceptanceConnected(t *testing.T) {
	start := time.Now()
	engineBin := acceptanceEngine(t)
	spec, err := engine.Configured()
	if err != nil {
		t.Fatal(err)
	}
	loopBin := buildLoop(t)

	const (
		slug        = "acme/widgets"
		owner       = "octo"
		token       = "connected-tracker-token"
		enrollToken = "connected-enroll-token"
		runner      = "runner:ci-1"
		fallback    = time.Minute
	)
	gh := fakegithub.New(owner, token)
	gh.Logf = t.Logf
	gh.AddRepo(slug, append(labelNames(config.DefaultLabels), runner, "runner:other", "backlog")...)
	srv := httptest.NewServer(gh)
	defer srv.Close()
	hub := fakehub.New(enrollToken)
	hub.SetInterval(5)
	hubSrv := httptest.NewServer(hub)
	defer hubSrv.Close()

	shipped, err := embeddedWorkflows.ReadFile("workflows/WORKFLOW.issues.md")
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := acceptanceWorkflow(string(shipped), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := fs.ReadDir(embeddedWorkflows, "workflows/prompts")
	if err != nil {
		t.Fatal(err)
	}
	hubFile := func(path, content string) hubproto.ConfigFile {
		return hubproto.ConfigFile{Path: path, SHA256: sha256Hex(content), Content: content}
	}
	hubConfig := func(workflow, additions string) hubproto.ConfigResponse {
		files := []hubproto.ConfigFile{hubFile("WORKFLOW.issues.md", workflow)}
		for _, e := range prompts {
			raw, err := embeddedWorkflows.ReadFile("workflows/prompts/" + e.Name())
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, hubFile("prompts/"+e.Name(), string(raw)))
		}
		labels := []hubproto.Label{}
		for _, l := range config.DefaultLabels {
			labels = append(labels, hubproto.Label{Name: l.Name, Color: l.Color, Description: l.Description})
		}
		return hubproto.ConfigResponse{
			Member: hubproto.Member{Login: owner},
			Projects: []hubproto.ConfigProject{{Provider: hubproto.ProviderGitHub, Repo: slug,
				Workflows: []string{"issues"}, FilterAdditions: additions}},
			Files:  files,
			Labels: labels,
		}
	}
	etag := hub.SetConfig(hubConfig(workflow, ""))

	root := t.TempDir()
	home := filepath.Join(root, "home")
	env := append(isolatedEnv(home), "JIG_ENGINE_BIN="+engineBin)
	cfgDir := filepath.Join(home, ".config", "jigz")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	machineYAML := fmt.Sprintf("connected:\n  idle_timeout: %s\n  fallback_interval: %s\n", "1m", fallback)
	if err := os.WriteFile(filepath.Join(cfgDir, "machine.yaml"), []byte(machineYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. Join.
	join := exec.Command(loopBin, "join", hubSrv.URL, "-", "--no-service", "--name", "ci-1")
	join.Env = append(slices.Clone(env), "JIG_TOKEN="+token, "JIG_GITHUB_API_URL="+srv.URL)
	join.Stdin = strings.NewReader(enrollToken + "\n")
	joinOut, err := join.CombinedOutput()
	if err != nil {
		t.Fatalf("jig join: %v\n%s", err, joinOut)
	}
	enrolled := hub.Enrollments()
	if len(enrolled) != 1 {
		t.Fatalf("%d enrollments, want 1", len(enrolled))
	}
	e := enrolled[0]
	if e.Name != "ci-1" || e.Host.OS != runtime.GOOS || e.Host.Arch != runtime.GOARCH || e.Host.CPUs != runtime.NumCPU() ||
		e.JigVersion != version || e.EngineVersion != spec.Version() || e.TrackerLogin != owner {
		t.Errorf("enrollment %+v, want name ci-1, %s/%s, %d CPUs, jig %s, engine %s, login %s",
			e, runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), version, spec.Version(), owner)
	}
	for path, want := range map[string]os.FileMode{filepath.Join(cfgDir, "hub.json"): 0o600, cfgDir: 0o700} {
		if st, err := os.Stat(path); err != nil {
			t.Error(err)
		} else if st.Mode().Perm() != want {
			t.Errorf("%s: mode %04o, want %04o", path, st.Mode().Perm(), want)
		}
	}

	// 2. Start the daemon.
	jigdLog := filepath.Join(home, ".local", "state", "jigz", "jigd.log")
	outPath := filepath.Join(root, "daemon.out")
	outf, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer outf.Close()
	cmd := exec.Command(loopBin, "daemon")
	cmd.Dir = root
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = outf, outf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	exited := make(chan struct{})
	var exitErr error
	go func() { exitErr = cmd.Wait(); close(exited) }()
	// Loop children lead their own process groups.
	groups := map[int]bool{}
	watch := func() []proc {
		ps := descendants(pid)
		for _, p := range ps {
			if p.pgid != pid {
				groups[p.pgid] = true
			}
		}
		return ps
	}
	defer func() {
		watch()
		for g := range groups {
			_ = syscall.Kill(-g, syscall.SIGKILL)
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-exited
	}()
	engines := func() []proc {
		var out []proc
		for _, p := range watch() {
			if f := strings.Fields(p.args); len(f) > 0 && f[0] == engineBin && (len(f) < 2 || f[1] != "validate") {
				out = append(out, p)
			}
		}
		return out
	}
	run := &acceptanceRun{t: t, gh: gh, slug: slug, name: "the daemon", logPath: jigdLog, exited: exited, keep: []string{runner}}
	waitFor, assertHandedOff := run.waitFor, run.assertHandedOff
	logHas := func(s string) bool {
		raw, _ := os.ReadFile(jigdLog)
		return strings.Contains(string(raw), s)
	}
	loop := func() (hubproto.Loop, bool) {
		st := hub.Statuses()
		if len(st) == 0 {
			return hubproto.Loop{}, false
		}
		for _, l := range st[len(st)-1].Loops {
			if l.Project == slug && l.Workflow == "issues" {
				return l, true
			}
		}
		return hubproto.Loop{}, false
	}
	send := func(typ hubproto.CommandType, args string) hubproto.Ack {
		t.Helper()
		waitFor("the command socket", 90*time.Second, hub.Connected)
		ack, err := hub.Send(hubproto.Command{Type: typ, Args: json.RawMessage(args)})
		if err != nil {
			run.fail("send %s: %v", typ, err)
		}
		return ack
	}
	loopArgs := `{"project":"` + slug + `","workflow":"issues"}`
	add := func(title string, labels ...string) int {
		return gh.Add(fakegithub.Item{Repo: slug, Title: title, Body: title + ".", Labels: labels, Assignees: []string{owner}})
	}
	fallbackQ := "state:open label:" + runner + " repo:" + slug
	fallbacks := func() int {
		_, fb := splitSearches(gh)
		n := 0
		for _, q := range fb {
			if q == fallbackQ {
				n++
			}
		}
		return n
	}

	t.Run("2_daemon_starts_without_a_loop", func(t *testing.T) {
		waitFor("the config fetch, a heartbeat with the loop stopped and the socket", 40*time.Second, func() bool {
			l, ok := loop()
			return logHas("config "+etag+" fetched") && ok && l.State == hubproto.LoopStopped && hub.Connected()
		})
		if ps := engines(); len(ps) != 0 {
			t.Errorf("engine processes before any wake: %+v", ps)
		}
		t.Logf("daemon up after %s", time.Since(start).Round(time.Second))
	})

	// 3. Items with and without this runner's label.
	a := add("Plan A", "agent:plan", runner)
	b := add("Plan B", "agent:plan")
	c := add("Plan C", "agent:plan", "runner:other")

	t.Run("4_wake_runs_only_this_runners_item", func(t *testing.T) {
		if ack := send(hubproto.CmdWake, loopArgs); !ack.OK {
			t.Fatalf("wake ack %+v, want ok", ack)
		}
		assertHandedOff(t, a, 90*time.Second)
		// The engine stays up until the idle time; this also proves the
		// engine process checks can see it.
		if len(engines()) == 0 {
			t.Errorf("no engine process under the daemon after the wake")
		}
		engineQ, _ := splitSearches(gh)
		if len(engineQ) == 0 {
			t.Errorf("no engine search")
		}
		for _, q := range engineQ {
			if !slices.Contains(strings.Fields(q), "label:"+runner) {
				t.Errorf("engine search without label:%s: %q", runner, q)
			}
		}
		for _, n := range []int{b, c} {
			if ev := labelLog(gh, slug, n); len(ev) != 0 {
				t.Errorf("issue #%d got label events %v", n, ev)
			}
		}
	})

	finishedA := func() []hubproto.RunEvent {
		var out []hubproto.RunEvent
		for _, ev := range hub.Runs() {
			if ev.Type == hubproto.EventFinished && ev.Item.Project == slug && ev.Item.Number == a {
				out = append(out, ev)
			}
		}
		return out
	}
	t.Run("5_the_run_reaches_the_hub", func(t *testing.T) {
		waitFor(fmt.Sprintf("the finished run of #%d", a), 120*time.Second, func() bool { return len(finishedA()) > 0 })
		ev := finishedA()[0]
		if ev.Outcome != hubproto.OutcomeSucceeded || ev.Usage == nil || ev.Usage.InputTokens != 100 || ev.Usage.OutputTokens != 50 ||
			ev.Item.Kind != hubproto.KindIssue || ev.Workflow != "issues" {
			t.Errorf("run event %+v (usage %+v), want succeeded issue run with 100 input and 50 output tokens", ev, ev.Usage)
		}
	})

	t.Run("6_idle_loop_sleeps", func(t *testing.T) {
		// Idle time 1m plus the child's 30s idle poll.
		waitFor("a heartbeat with the loop sleeping", 150*time.Second, func() bool {
			l, ok := loop()
			return ok && l.State == hubproto.LoopSleeping
		})
		waitFor("the engine to exit", 30*time.Second, func() bool { return len(engines()) == 0 })
	})

	var d int
	t.Run("7_fallback_search_while_the_hub_is_down", func(t *testing.T) {
		before := fallbacks()
		hub.SetDown(true)
		d = add("Plan D", "agent:plan", runner)
		waitFor("a fallback search while the hub is down", 60*time.Second, func() bool { return fallbacks() > before })
		assertHandedOff(t, d, 90*time.Second)
	})

	t.Run("8_config_change_reaches_the_engine", func(t *testing.T) {
		hub.SetDown(false)
		// The workflow changes too, so the status reports a new hash.
		changed := workflow + "\n"
		etag2 := hub.SetConfig(hubConfig(changed, "-label:backlog"))
		waitFor("the new config to be fetched", 90*time.Second, func() bool { return logHas("config " + etag2 + " fetched") })
		want := hubproto.FileHash{Path: "WORKFLOW.issues.md", SHA256: sha256Hex(changed)}
		waitFor("the status to show the new workflow hash", 90*time.Second, func() bool {
			l, ok := loop()
			return ok && slices.Contains(l.Files, want)
		})
		seen, _ := splitSearches(gh)
		// The loop may have gone to sleep since D; wake is a no-op on an
		// awake loop.
		if ack := send(hubproto.CmdWake, loopArgs); !ack.OK {
			t.Fatalf("wake ack %+v, want ok", ack)
		}
		first := -1
		waitFor("an engine search with -label:backlog", 90*time.Second, func() bool {
			q, _ := splitSearches(gh)
			for i := len(seen); i < len(q); i++ {
				if slices.Contains(strings.Fields(q[i]), "-label:backlog") {
					first = i
					return true
				}
			}
			return false
		})
		time.Sleep(3 * time.Second)
		q, _ := splitSearches(gh)
		for _, s := range q[first:] {
			if f := strings.Fields(s); !slices.Contains(f, "-label:backlog") || !slices.Contains(f, "label:"+runner) {
				t.Errorf("engine search after the change lacks -label:backlog or label:%s: %q", runner, s)
			}
		}
	})

	t.Run("9_pause_holds_and_resume_runs", func(t *testing.T) {
		if ack := send(hubproto.CmdPause, `{}`); !ack.OK {
			t.Fatalf("pause ack %+v, want ok", ack)
		}
		waitFor("the loop to drain", 90*time.Second, func() bool {
			l, ok := loop()
			return ok && l.State == hubproto.LoopStopped && len(engines()) == 0
		})
		e := add("Plan E", "agent:plan", runner)
		run.focus = e
		until := time.Now().Add(2*fallback + 5*time.Second)
		for time.Now().Before(until) {
			if ev := labelLog(gh, slug, e); len(ev) != 0 {
				run.fail("issue #%d got label events while paused: %v", e, ev)
			}
			select {
			case <-exited:
				run.fail("the daemon exited while paused")
			default:
			}
			time.Sleep(time.Second)
		}
		if ack := send(hubproto.CmdResume, `{}`); !ack.OK {
			t.Fatalf("resume ack %+v, want ok", ack)
		}
		assertHandedOff(t, e, 120*time.Second)
	})

	t.Run("10_unsupported_command_and_no_listener", func(t *testing.T) {
		ack := send(hubproto.CmdSessionOpen, `{}`)
		if ack.OK || ack.Error == nil || ack.Error.Code != hubproto.AckUnsupported {
			t.Errorf("session.open ack %+v, want ok false with code unsupported", ack)
		}
		if runtime.GOOS != "linux" {
			t.Skip("listening sockets are read from /proc")
		}
		// The engine serves its state API, so a running engine shows that
		// the check sees a listening socket.
		eng := engines()
		if len(eng) == 0 {
			t.Fatal("no engine process for the listening socket control")
		}
		if l, err := listeningSockets(eng[0].pid); err != nil || len(l) == 0 {
			t.Errorf("engine pid %d listening sockets %v, %v; want at least one", eng[0].pid, l, err)
		}
		l, err := listeningSockets(pid)
		if err != nil {
			t.Fatalf("reading the daemon's sockets: %v", err)
		}
		if len(l) != 0 {
			t.Errorf("the daemon listens on %v", l)
		}
	})

	// 11. Shut down as the service manager does.
	t0 := time.Now()
	before := watch()
	_ = syscall.Kill(pid, syscall.SIGTERM)
	select {
	case <-exited:
		if exitErr != nil {
			t.Errorf("daemon exit: %v", exitErr)
		}
	case <-time.After(120 * time.Second):
		t.Errorf("the daemon did not exit within 120s of SIGTERM")
	}
	for g := range groups {
		if left := groupMembers(g); len(left) > 0 {
			t.Errorf("processes left in loop child group %d: %v", g, left)
		}
	}
	if left := groupMembers(pid); len(left) > 0 {
		t.Errorf("processes left in the daemon's group: %v", left)
	}
	for _, p := range before {
		if syscall.Kill(p.pid, 0) == nil {
			t.Errorf("process %d (%s) survived the daemon", p.pid, p.args)
		}
	}
	t.Logf("shutdown %s: %d processes and %d loop child groups before SIGTERM", time.Since(t0).Round(time.Millisecond), len(before), len(groups))

	// 12. Protocol and secrets.
	if n := len(finishedA()); n != 1 {
		t.Errorf("the finished run of #%d was sent %d times, want once", a, n)
	}
	if un := gh.Unexpected(); len(un) > 0 {
		t.Errorf("the fake GitHub rejected requests:\n%s", strings.Join(un, "\n"))
	}
	if un := hub.Unexpected(); len(un) > 0 {
		t.Errorf("the fake hub rejected requests:\n%s", strings.Join(un, "\n"))
	}
	secrets := map[string]string{"credential": hub.Credential(), "enrollment token": enrollToken, "JIG_TOKEN": token}
	logs := map[string]string{"jig join output": string(joinOut)}
	_ = filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err == nil && !de.IsDir() && (strings.HasSuffix(p, ".log") || strings.Contains(filepath.Base(p), ".log.") || p == outPath) {
			raw, _ := os.ReadFile(p)
			logs[p] = string(raw)
		}
		return nil
	})
	if _, ok := logs[jigdLog]; !ok {
		t.Errorf("no daemon log at %s", jigdLog)
	}
	for i, body := range hub.Bodies() {
		logs[fmt.Sprintf("hub request body %d", i)] = body
	}
	for name, s := range secrets {
		if s == "" {
			t.Errorf("%s is empty", name)
			continue
		}
		for where, text := range logs {
			if strings.Contains(text, s) {
				t.Errorf("the %s appears in %s", name, where)
			}
		}
	}
	t.Logf("connected: %d log files checked, total %s", len(logs)-len(hub.Bodies())-1, time.Since(start).Round(time.Second))
}

// isolatedEnv is the environment of a Jigz process whose home, cache, data,
// config and state directories all lie under home.
func isolatedEnv(home string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
	}
}

// acceptanceRun waits on a running process under test and checks handoffs
// on the fake GitHub. A failure prints the tail of logPath and the label
// events of the focused item.
type acceptanceRun struct {
	t       *testing.T
	gh      *fakegithub.Server
	slug    string
	name    string
	logPath string
	exited  <-chan struct{}
	focus   int
	// keep are labels a handed-off item keeps beside agent:done.
	keep []string
}

func (r *acceptanceRun) fail(format string, args ...any) {
	r.t.Helper()
	r.t.Errorf(format, args...)
	r.t.Logf("%s output (tail):\n%s", r.name, logTail(r.logPath, 60))
	r.t.Logf("fake GitHub label events for #%d: %v", r.focus, labelLog(r.gh, r.slug, r.focus))
	r.t.FailNow()
}

func (r *acceptanceRun) waitFor(what string, d time.Duration, ok func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		select {
		case <-r.exited:
			r.fail("%s exited while waiting for %s", r.name, what)
		default:
		}
		if time.Now().After(deadline) {
			r.fail("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// assertHandedOff waits for issue n to reach agent:done and checks the
// claim and handoff label events.
func (r *acceptanceRun) assertHandedOff(t *testing.T, n int, d time.Duration) []string {
	t.Helper()
	r.focus = n
	gh, slug := r.gh, r.slug
	r.waitFor(fmt.Sprintf("issue #%d to reach agent:done", n), d, func() bool {
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
	want := slices.Sorted(slices.Values(append([]string{"agent:done"}, r.keep...)))
	if !slices.Equal(slices.Sorted(slices.Values(it.Labels)), want) {
		t.Errorf("final labels %v, want %v", it.Labels, want)
	}
	return log
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
	case os.Getenv("JIG_ENGINE_BIN") != "":
		bin = os.Getenv("JIG_ENGINE_BIN")
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

// buildLoop compiles the Jigz under test.
func buildLoop(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "jig")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// writeAcceptanceRepo creates the target repo's .jigz directory: the
// config, the shipped workflows and prompts, and the acceptance variant of
// the issues workflow.
func writeAcceptanceRepo(t *testing.T, dir, slug, token, endpoint string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("repo: %s\ntoken: %s\n", slug, token)
	if err := os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	syncWorkflows(dir)
	path := filepath.Join(dir, ".jigz", "workflows", "WORKFLOW.issues.md")
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

func getBody(t *testing.T, u string) string {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s", u, resp.Status)
	}
	return buf.String()
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

// searchQueries lists the engine's search queries. The dashboard's question
// scanner also searches, for label:needs-human; those are left out.
func searchQueries(gh *fakegithub.Server) []string {
	var out []string
	for _, r := range gh.Requests() {
		if r.Path != "/search/issues" {
			continue
		}
		v, _ := url.ParseQuery(r.Query)
		q := v.Get("q")
		if slices.Contains(strings.Fields(q), "label:"+collector.NeedsHuman) {
			continue
		}
		out = append(out, q)
	}
	return out
}

// splitSearches splits the search queries into the engine's and the
// connected daemon's fallback searches, which start with state:open and
// name no type.
func splitSearches(gh *fakegithub.Server) (engine, fallback []string) {
	for _, q := range searchQueries(gh) {
		if strings.HasPrefix(q, "state:open label:runner:") {
			fallback = append(fallback, q)
		} else {
			engine = append(engine, q)
		}
	}
	return engine, fallback
}

type proc struct {
	pid, ppid, pgid int
	args            string
}

// descendants lists the processes below pid.
func descendants(pid int) []proc {
	out, err := exec.Command("ps", "-eo", "pid=,ppid=,pgid=,args=").Output()
	if err != nil {
		return nil
	}
	children := map[int][]proc{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		var p proc
		p.pid, _ = strconv.Atoi(f[0])
		p.ppid, _ = strconv.Atoi(f[1])
		p.pgid, _ = strconv.Atoi(f[2])
		p.args = strings.Join(f[3:], " ")
		children[p.ppid] = append(children[p.ppid], p)
	}
	var res []proc
	queue := []int{pid}
	for len(queue) > 0 {
		for _, c := range children[queue[0]] {
			res = append(res, c)
			queue = append(queue, c.pid)
		}
		queue = queue[1:]
	}
	return res
}

// listeningSockets lists the TCP sockets in LISTEN state that pid holds,
// as local addresses from /proc (Linux only).
func listeningSockets(pid int) ([]string, error) {
	fds, err := filepath.Glob(fmt.Sprintf("/proc/%d/fd/*", pid))
	if err != nil {
		return nil, err
	}
	if len(fds) == 0 {
		return nil, fmt.Errorf("no file descriptors readable for pid %d", pid)
	}
	held := map[string]bool{}
	for _, fd := range fds {
		link, err := os.Readlink(fd)
		if errors.Is(err, fs.ErrNotExist) {
			// The descriptor was closed after the listing.
			continue
		}
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(link, "socket:[") {
			held[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
		}
	}
	var out []string
	for _, table := range []string{"tcp", "tcp6"} {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/net/%s", pid, table))
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			f := strings.Fields(line)
			// local_address is field 1, st field 3 (0A is LISTEN), inode field 9.
			if len(f) > 9 && f[3] == "0A" && held[f[9]] {
				out = append(out, table+" "+f[1])
			}
		}
	}
	return out, nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
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
