package main

// One-action restart for a stuck issue (#72).
//
// Recovery is label surgery plus a wake, through the same tracker path
// the loop uses: release the claim marker, make sure the trigger label
// is present, record a restart event, and wake the loop so the engine
// re-polls. Parked and retry rows in the engine database are never
// written by Jigz (there is no supported engine clear operation); the
// engine reconciles them when it re-dispatches the re-queued issue.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/github"
	"github.com/kinged007/jigz/internal/store"
)

// defaultClaimLabel is the claim marker when a tracker sets no
// in_progress_state of its own.
const defaultClaimLabel = "in-progress"

// restartRequestSuffix names the durable restart-request files next to
// the status file. One file per issue, so concurrent restarts of one
// issue leave one pending re-queue; the supervisor consumes them on
// its next tick.
const restartRequestSuffix = ".restart.request."

// restartEventKind is the history event kind a restart records. The run
// page already renders observed events, so the event needs no template
// change.
const restartEventKind = "restart"

// restartRequest is one durable restart ask: who wants which issue back.
type restartRequest struct {
	Issue int       `json:"issue"`
	Actor string    `json:"actor"`
	At    time.Time `json:"at"`
}

// issueRestartResult is the machine-readable restart report. It carries
// the same actor and timestamp the dashboard event shows.
type issueRestartResult struct {
	Issue          int      `json:"issue"`
	Restarted      bool     `json:"restarted"`
	Pending        bool     `json:"pending,omitempty"`
	Loop           string   `json:"loop,omitempty"`
	Trigger        string   `json:"trigger,omitempty"`
	ReleasedClaim  bool     `json:"released_claim,omitempty"`
	AppliedTrigger bool     `json:"applied_trigger,omitempty"`
	Woke           bool     `json:"woke,omitempty"`
	Actor          string   `json:"actor,omitempty"`
	At             string   `json:"at,omitempty"`
	Effects        []string `json:"effects,omitempty"`
	Error          string   `json:"error,omitempty"`
}

// issueTracker is the tracker surface a restart needs. The production
// implementation talks to the GitHub REST API with the loop's token;
// tests substitute a fake.
type issueTracker interface {
	issueLabels(ctx context.Context, slug, token string, num int) (labels []string, isPR bool, err error)
	addLabels(ctx context.Context, slug, token string, num int, labels []string) error
	removeLabel(ctx context.Context, slug, token string, num int, label string) error
	user(ctx context.Context, slug, token string) (string, error)
}

// ghIssueTracker implements issueTracker over the GitHub REST API, the
// same endpoints the engine uses, so permissions and rate limits match
// normal operation.
type ghIssueTracker struct {
	client *github.Client
}

func (g *ghIssueTracker) issueLabels(ctx context.Context, slug, token string, num int) ([]string, bool, error) {
	resp, err := g.client.Get(ctx, slug, token, "/repos/"+slug+"/issues/"+strconv.Itoa(num), "")
	if err != nil {
		return nil, false, err
	}
	var item struct {
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		PullRequest *struct{} `json:"pull_request"`
	}
	if err := json.Unmarshal(resp.Body, &item); err != nil {
		return nil, false, fmt.Errorf("GitHub response: %w", err)
	}
	labels := make([]string, 0, len(item.Labels))
	for _, l := range item.Labels {
		labels = append(labels, l.Name)
	}
	return labels, item.PullRequest != nil, nil
}

func (g *ghIssueTracker) addLabels(ctx context.Context, slug, token string, num int, labels []string) error {
	body, _ := json.Marshal(map[string][]string{"labels": labels})
	_, err := g.client.Post(ctx, slug, token, "/repos/"+slug+"/issues/"+strconv.Itoa(num)+"/labels", body)
	return err
}

func (g *ghIssueTracker) removeLabel(ctx context.Context, slug, token string, num int, label string) error {
	err := g.client.Delete(ctx, slug, token, "/repos/"+slug+"/issues/"+strconv.Itoa(num)+"/labels/"+url.PathEscape(label))
	if err != nil {
		var e *github.Error
		if errors.As(err, &e) && e.Is(github.ErrNotFound) {
			return nil
		}
	}
	return err
}

func (g *ghIssueTracker) user(ctx context.Context, slug, token string) (string, error) {
	return g.client.User(ctx, slug, token)
}

// restartRefusal is a refusal of an unsafe restart. It carries the
// one-line reason unchanged, so the command line, the dashboard and the
// supervisor report the same sentence. Loop names the owning loop when
// one was resolved, so the outcome can be recorded against it.
type restartRefusal struct{ reason, loop string }

func (e *restartRefusal) Error() string { return e.reason }

// restartActions is what one restart does: refuse with a one-line
// reason, or change the labels named by the flags.
type restartActions struct {
	refuse       string
	releaseClaim bool
	applyTrigger bool
}

// planRestart decides one restart from already-gathered state. Live
// means a live engine session holds the issue, or the live set is
// unknown (an unknown set refuses like a live one, never like an empty
// one); loop and trigger name the owning loop and the label to ensure,
// empty when unknown; claim is the loop's claim marker.
func planRestart(num int, labels []string, isPR, live bool, loop, trigger, claim string) restartActions {
	if isPR {
		return restartActions{refuse: fmt.Sprintf("issue %d is a pull request, not an issue", num)}
	}
	if live {
		if hasLabelFold(labels, claim) {
			return restartActions{refuse: fmt.Sprintf("issue %d is mid-run", num)}
		}
		return restartActions{refuse: fmt.Sprintf("issue %d is claimed by a live session", num)}
	}
	if loop == "" {
		return restartActions{refuse: fmt.Sprintf("issue %d belongs to another loop", num)}
	}
	if trigger == "" {
		// The owning loop matches by query, not by label: there is no
		// trigger to re-apply, only a claim to release.
		return restartActions{releaseClaim: hasLabelFold(labels, claim)}
	}
	return restartActions{
		releaseClaim: hasLabelFold(labels, claim),
		applyTrigger: !hasLabelFold(labels, trigger),
	}
}

func hasLabelFold(labels []string, want string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, want) {
			return true
		}
	}
	return false
}

// resolveRestartLoop finds the loop that owns an issue: the loop whose
// rule labels hold the issue's trigger label. Loops are tried in name
// order so the answer is deterministic.
func resolveRestartLoop(resolved map[string]config.Resolved, labels []string) (loop, trigger string) {
	for _, n := range sortedKeys(resolved) {
		for _, r := range resolved[n].Rules {
			for _, lb := range r.Labels {
				if hasLabelFold(labels, lb) {
					return n, lb
				}
			}
		}
	}
	return "", ""
}

// triggerOf returns the first rule label of a loop, the label a restart
// re-applies when the issue carries no trigger of its own.
func triggerOf(resolved map[string]config.Resolved, loop string) string {
	r, ok := resolved[loop]
	if !ok {
		return ""
	}
	for _, rule := range r.Rules {
		if len(rule.Labels) > 0 {
			return rule.Labels[0]
		}
	}
	return ""
}

// triggerForRule is the first label of a loop's named rule, the label a
// restart re-applies when the issue carries no trigger of its own. An
// unknown rule name falls back to the loop's first rule label.
func triggerForRule(resolved map[string]config.Resolved, loop, rule string) string {
	if r, ok := resolved[loop]; ok {
		for _, rr := range r.Rules {
			if rr.Name == rule && len(rr.Labels) > 0 {
				return rr.Labels[0]
			}
		}
	}
	return triggerOf(resolved, loop)
}

// latestRestartWorkflow is the history fallback for a
// claim-without-trigger issue: the workflow of its most recent stored
// run, with that run's rule. Run ids are per-workflow counters, so
// recency compares start times, not ids; ties keep name order, and the
// newest run of one workflow wins by id. A nil store means no history
// is available.
func latestRestartWorkflow(st *store.Store, project string, resolved map[string]config.Resolved, num int) (string, string) {
	if st == nil {
		return "", ""
	}
	best, bestRule := "", ""
	var bestAt time.Time
	bestID := int64(-1)
	have := false
	for _, n := range sortedKeys(resolved) {
		runs, err := st.IssueRuns(project, n, strconv.Itoa(num))
		if err != nil {
			continue
		}
		for _, r := range runs {
			at := parseRestartTime(r.StartedAt)
			if !have || at.After(bestAt) || (at.Equal(bestAt) && n == best && r.ID > bestID) {
				best, bestAt, bestID, bestRule, have = n, at, r.ID, r.RuleName, true
			}
		}
	}
	return best, bestRule
}

// parseRestartTime reads a stored run start. An empty or unreadable
// stamp sorts before any real one.
func parseRestartTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// liveRestartIssues normalizes supervisor sessions to plain issue
// numbers: session issue ids look like "7" or "owner/repo#7".
func liveRestartIssues(sessions []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range sessions {
		if i := strings.LastIndex(s, "#"); i >= 0 {
			s = s[i+1:]
		}
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
			out[strconv.Itoa(n)] = true
		}
	}
	return out
}

// restartDeps is everything applying a restart needs. sessionsUnknown
// is true while the supervisor cannot account for every engine session;
// an unknown live set refuses like a live one, never like an empty one.
type restartDeps struct {
	tracker         issueTracker
	token           string
	slug            string
	loops           map[string]config.Resolved
	live            map[string]bool
	sessionsUnknown bool
	history         *store.Store
	events          *store.Store
	now             func() time.Time
	actor           string
}

func (d *restartDeps) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// applyIssueRestart performs one restart: refuse unsafe ones with a
// one-line error, otherwise fix the labels, record the event and report
// what changed. hintLoop constrains the owning loop (the dashboard run
// page knows its workflow); empty means any loop of this root.
func applyIssueRestart(ctx context.Context, d *restartDeps, num int, hintLoop string) (issueRestartResult, error) {
	res := issueRestartResult{Issue: num, Actor: d.actor, At: d.clock().UTC().Format(time.RFC3339)}
	labels, isPR, err := d.tracker.issueLabels(ctx, d.slug, d.token, num)
	if err != nil {
		var e *github.Error
		if errors.As(err, &e) && e.Is(github.ErrNotFound) {
			return res, &restartRefusal{reason: fmt.Sprintf("no issue %d in %s", num, d.slug)}
		}
		return res, fmt.Errorf("issue %d: %w", num, err)
	}
	loop, trigger := resolveRestartLoop(d.loops, labels)
	if loop == "" {
		if w, rule := latestRestartWorkflow(d.history, d.slug, d.loops, num); w != "" && (hintLoop == "" || w == hintLoop) {
			loop, trigger = w, triggerForRule(d.loops, w, rule)
		}
	}
	if hintLoop != "" && loop != hintLoop {
		return res, &restartRefusal{reason: fmt.Sprintf("issue %d belongs to another loop", num)}
	}
	act := planRestart(num, labels, isPR, d.live[strconv.Itoa(num)] || d.sessionsUnknown, loop, trigger, claimLabelOf(d.loops, loop))
	if act.refuse != "" {
		return res, &restartRefusal{reason: act.refuse, loop: loop}
	}
	claim := claimLabelOf(d.loops, loop)
	effects := []string{}
	if act.releaseClaim {
		if err := d.tracker.removeLabel(ctx, d.slug, d.token, num, claim); err != nil {
			return res, fmt.Errorf("issue %d: release claim: %w", num, err)
		}
		effects = append(effects, "released claim")
	}
	if act.applyTrigger {
		if err := d.tracker.addLabels(ctx, d.slug, d.token, num, []string{trigger}); err != nil {
			return res, fmt.Errorf("issue %d: re-apply %s: %w", num, trigger, err)
		}
		effects = append(effects, "re-applied "+trigger)
	}
	if len(effects) == 0 {
		effects = append(effects, "already restartable")
	}
	res.Restarted = true
	res.Loop, res.Trigger = loop, trigger
	res.ReleasedClaim, res.AppliedTrigger = act.releaseClaim, act.applyTrigger
	res.Effects = effects
	if d.events != nil {
		ev := store.Event{
			TS: res.At, Project: d.slug, Workflow: loop,
			IssueID: strconv.Itoa(num), Kind: restartEventKind,
			Detail: "restarted by " + res.Actor + ": " + strings.Join(effects, ", "),
		}
		if err := d.events.AddEvents([]store.Event{ev}); err != nil {
			return res, fmt.Errorf("issue %d: record event: %w", num, err)
		}
	}
	return res, nil
}

// claimLabelOf is the tracker's in-progress state, the marker a restart
// releases.
func claimLabelOf(resolved map[string]config.Resolved, loop string) string {
	if r, ok := resolved[loop]; ok && r.Tracker.InProgressState != "" {
		return r.Tracker.InProgressState
	}
	return defaultClaimLabel
}

// pendingRestartIssues lists the issues with a stored restart request,
// oldest first. A restart while the supervisor is down stores one; the
// supervisor consumes them on its next tick and on start.
func pendingRestartIssues(root string) ([]restartRequest, error) {
	prefix, err := cleanupFilePath(root, restartRequestSuffix)
	if err != nil {
		return nil, err
	}
	files, _ := filepath.Glob(prefix + "*")
	sort.Strings(files)
	var out []restartRequest
	for _, f := range files {
		data, rerr := os.ReadFile(f)
		if rerr != nil {
			continue
		}
		var req restartRequest
		if json.Unmarshal(data, &req) != nil || req.Issue <= 0 {
			continue
		}
		out = append(out, req)
	}
	return out, nil
}

// storeRestartRequest records one restart for the next supervisor start.
// It reports whether an identical request was already pending, so two
// runs leave exactly one pending re-queue. The pending record is one
// file per issue published by linking a complete temp file into place,
// so two concurrent restarts leave one file (the loser sees the
// winner's file) and a reader never sees a half-written request.
// restartLink publishes a complete temp request file into its per-issue
// name. It is a variable so tests can force the hard-link failure path.
var restartLink = os.Link

// restartRestoreWrite restores a retryable request file after a
// transient config failure. It is a variable so tests can force the
// restore failure path without making the state dir unwritable.
var restartRestoreWrite = writeFileAtomic

func storeRestartRequest(root string, req restartRequest) (already bool, err error) {
	if pending, err := pendingRestartIssues(root); err == nil {
		for _, p := range pending {
			if p.Issue == req.Issue {
				return true, nil
			}
		}
	} else {
		return false, err
	}
	prefix, err := cleanupFilePath(root, restartRequestSuffix)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(prefix), 0o755); err != nil {
		return false, err
	}
	path := prefix + strconv.Itoa(req.Issue)
	data, _ := json.Marshal(req)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".restart-request-tmp-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, werr := tmp.Write(data); werr != nil {
		_ = tmp.Close()
		return false, werr
	}
	if cerr := tmp.Close(); cerr != nil {
		return false, cerr
	}
	if cerr := os.Chmod(tmpName, 0o644); cerr != nil {
		return false, cerr
	}
	if lerr := restartLink(tmpName, path); lerr != nil {
		if os.IsExist(lerr) {
			return true, nil
		}
		// Filesystems without hard links (some FUSE/SMB state dirs)
		// reject link: publish the complete temp file with a rename.
		// The payload is already whole in tmpName, so a crash cannot
		// leave a partial per-issue file; re-check for a winner first
		// because rename replaces its destination.
		if _, serr := os.Lstat(path); serr == nil {
			return true, nil
		} else if !os.IsNotExist(serr) {
			return false, serr
		}
		if rerr := os.Rename(tmpName, path); rerr != nil {
			if os.IsExist(rerr) {
				return true, nil
			}
			return false, rerr
		}
		return false, nil
	}
	return false, nil
}

// runIssue runs `jig issue restart <number> [--json] [repo-root]`.
func runIssue(args []string) {
	if len(args) == 0 || args[0] != "restart" {
		fatal(fmt.Errorf("usage: jig issue restart <number> [--json] [repo-root]"))
	}
	var bare []string
	asJSON := false
	rest := args[1:]
	for i, a := range rest {
		switch {
		case a == "--json":
			asJSON = true
		case a == "--repo":
			continue
		case i > 0 && rest[i-1] == "--repo":
			continue
		case strings.HasPrefix(a, "-"):
			fatal(fmt.Errorf("unknown flag %q", a))
		default:
			bare = append(bare, a)
		}
	}
	if len(bare) == 0 || len(bare) > 2 {
		fatal(fmt.Errorf("usage: jig issue restart <number> [--json] [repo-root]"))
	}
	num, err := strconv.Atoi(bare[0])
	if err != nil || num <= 0 {
		fatal(fmt.Errorf("usage: jig issue restart <number> [--json] [repo-root]"))
	}
	dir := "."
	if len(bare) == 2 {
		dir = bare[1]
	}
	abs, err := absRoot(dir)
	if err != nil {
		fatal(err)
	}
	res, rerr := restartFromCLI(abs, num)
	if rerr != nil {
		res.Error = rerr.Error()
	}
	if asJSON {
		out, _ := json.Marshal(res)
		fmt.Println(string(out))
	}
	if rerr != nil {
		fatal(rerr)
	}
	if !asJSON {
		printRestartResult(abs, res)
	}
}

// restartFromCLI restarts one issue from the command line: apply now
// when a supervisor serves the root, otherwise store the request for
// the next start. Either way the loop is woken so the issue re-polls.
func restartFromCLI(root string, num int) (issueRestartResult, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return issueRestartResult{Issue: num}, err
	}
	if err := cfg.CheckToken(); err != nil {
		return issueRestartResult{Issue: num}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tracker := &ghIssueTracker{client: &github.Client{BaseURL: "https://api.github.com"}}
	actor := restartActor(ctx, tracker, cfg.Repo, cfg.Token)
	_, rerr := runningEntry(root)
	deps := &restartDeps{
		tracker: tracker, token: cfg.Token, slug: cfg.Repo,
		loops: cfg.Resolve(), actor: actor,
	}
	sessions, known := statusSessions(root)
	deps.live = liveRestartIssues(sessions)
	deps.sessionsUnknown = !known
	if hst, herr := store.Open(dashboardStateDir()); herr == nil {
		deps.history, deps.events = hst, hst
		defer hst.Close()
	}
	if rerr == nil {
		res, err := applyIssueRestart(ctx, deps, num, "")
		if err != nil {
			return res, err
		}
		if werr := requestWake(root); werr != nil {
			fmt.Fprintln(os.Stderr, "jig: wake:", werr)
		} else {
			res.Woke = true
		}
		return res, nil
	}
	req := restartRequest{Issue: num, Actor: actor, At: time.Now().UTC()}
	already, err := storeRestartRequest(root, req)
	if err != nil {
		return issueRestartResult{Issue: num, Actor: actor}, err
	}
	if werr := requestWake(root); werr != nil {
		fmt.Fprintln(os.Stderr, "jig: wake:", werr)
	}
	res := issueRestartResult{Issue: num, Pending: true, Actor: actor, At: req.At.Format(time.RFC3339)}
	if already {
		res.Effects = []string{"restart already recorded"}
	} else {
		res.Effects = []string{"restart recorded for next start"}
	}
	return res, nil
}

// statusSessions lists the session issue ids the supervisor of root
// knows about, with whether the set is complete. It reports unknown
// while a running loop cannot account for every engine session, and
// known-empty when no supervisor serves the root. Callers must treat
// an unknown set as live, never as empty.
func statusSessions(root string) ([]string, bool) {
	if _, err := runningEntry(root); err != nil {
		return nil, true
	}
	doc, err := readStatusDoc(root)
	if err != nil {
		return nil, false
	}
	var out []string
	for _, w := range doc.Workflows {
		if !engineUp(w) {
			continue
		}
		if !w.SessionsKnown {
			return nil, false
		}
		for _, s := range w.Sessions {
			out = append(out, s.IssueID)
		}
	}
	return out, true
}

// restartActor is the tracker login of the loop's token, the "who" of
// the restart event. It falls back to "operator" when the lookup fails
// so a restart is never blocked on it.
func restartActor(ctx context.Context, tracker issueTracker, slug, token string) string {
	if login, err := tracker.user(ctx, slug, token); err == nil && login != "" {
		return login
	}
	return "operator"
}

// restartStep applies the restart requests stored while the supervisor
// was down or busy, oldest first. Each file is renamed to an in-flight
// marker before its request runs, so two ticks never apply one request
// twice; the marker is removed on success or refusal and renamed back
// for a retryable failure. While the live session set is unknown the
// request is kept for a later tick, not refused. A crash between the
// label change and the marker removal re-applies idempotent labels on
// the next start and may record the restart event twice; the duplicate
// is accepted because the labels stay correct and the window is short.
// A request that no longer restarts is final: the marker is gone and the
// outcome is recorded as an event. A permanent failure (a missing config
// file, a missing token, or a tracker 401/403/404) drops the request
// with an outcome instead of retrying forever. Any other config read
// failure (for example a half-written config file) is retryable but
// capped: after maxRestartConfigAttempts ticks the request is dropped
// with an outcome, so a transient error cannot retry forever. Any other
// failure keeps the file for the next tick and moves on to the next
// request, so one stuck issue never starves the rest.
func (s *supervisor) restartStep() {
	prefix, err := cleanupFilePath(s.root, restartRequestSuffix)
	if err != nil {
		return
	}
	files, _ := filepath.Glob(prefix + "*")
	sort.Strings(files)
	for _, f := range files {
		if strings.HasSuffix(f, ".applying") {
			// A marker left by a crash: restore it to a request file.
			base := strings.TrimSuffix(f, ".applying")
			if err := os.Rename(f, base); err != nil {
				continue
			}
			f = base
		}
		data, rerr := os.ReadFile(f)
		var req restartRequest
		if rerr != nil || json.Unmarshal(data, &req) != nil || req.Issue <= 0 {
			fmt.Fprintln(os.Stderr, "jig: restart: dropping an unreadable request file", filepath.Base(f))
			_ = os.Remove(f)
			continue
		}
		inflight := f + ".applying"
		if err := os.Rename(f, inflight); err != nil {
			continue
		}
		if err := s.applyStoredRestart(req); err != nil {
			var refusal *restartRefusal
			if errors.As(err, &refusal) {
				_ = os.Remove(inflight)
				s.recordRestartOutcome(req, "", refusal.loop, refusal.reason)
				continue
			}
			if restartPermanent(err) {
				_ = os.Remove(inflight)
				_, loop := restartHistoryLocation(req.Issue, restartDropRepo(s.root))
				s.recordRestartOutcome(req, "", loop, fmt.Sprintf("gave up on issue %d: %v", req.Issue, err))
				continue
			}
			var terr *restartTransientConfigError
			if errors.As(err, &terr) {
				attempts := restartConfigAttempts(data)
				if attempts+1 >= maxRestartConfigAttempts {
					_ = os.Remove(inflight)
					_, loop := restartHistoryLocation(req.Issue, restartDropRepo(s.root))
					s.recordRestartOutcome(req, "", loop, fmt.Sprintf("gave up on issue %d after %d attempts: %v", req.Issue, attempts+1, err))
					continue
				}
				if werr := restartRestoreWrite(f, append(data, '\n')); werr != nil {
					fmt.Fprintln(os.Stderr, "jig: restart: restore:", werr)
					continue
				}
				_ = os.Remove(inflight)
				continue
			}
			fmt.Fprintln(os.Stderr, "jig: restart:", err)
			if rerr := os.Rename(inflight, f); rerr != nil {
				if werr := writeFileAtomic(f, data); werr != nil {
					fmt.Fprintln(os.Stderr, "jig: restart: restore:", werr)
				}
			}
			continue
		}
		_ = os.Remove(inflight)
	}
}

// restartPermanent reports a stored-restart failure retrying cannot
// fix: a missing config file, a missing token, a forbidden label write,
// or a gone repository. Any other config read failure is transient and
// stays retryable under a bounded attempt count.
func restartPermanent(err error) bool {
	var perm *restartPermanentError
	if errors.As(err, &perm) {
		return true
	}
	var e *github.Error
	if errors.As(err, &e) {
		return e.Status == 401 || e.Status == 403 || e.Status == 404
	}
	return false
}

// restartPermanentError marks a stored-restart failure no retry can
// fix: a missing config file or token, or a final tracker refusal.
type restartPermanentError struct{ err error }

func (e *restartPermanentError) Error() string { return e.err.Error() }
func (e *restartPermanentError) Unwrap() error { return e.err }

// maxRestartConfigAttempts caps retries of a transient config read
// failure (for example a half-written config file) before the request
// is dropped with a recorded outcome.
const maxRestartConfigAttempts = 5

// restartTransientConfigError marks a stored-restart config failure that
// may heal between ticks. restartStep retries it, not forever.
type restartTransientConfigError struct{ err error }

func (e *restartTransientConfigError) Error() string { return e.err.Error() }
func (e *restartTransientConfigError) Unwrap() error { return e.err }

// restartConfigAttempts counts prior transient-config attempts stored as
// trailing newlines on the request file. JSON ignores the whitespace, so
// the count survives without a format change.
func restartConfigAttempts(data []byte) int {
	n := 0
	for len(data) > 0 && data[len(data)-1] == '\n' {
		n++
		data = data[:len(data)-1]
	}
	return n
}

// isMissingConfig reports whether root has no config file. config.Load
// does not preserve the not-exist chain, so check the file directly.
func isMissingConfig(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".jigz", "config.yaml"))
	return os.IsNotExist(err)
}

// applyStoredRestart runs one stored restart request to completion:
// labels, event and wake, like the command line. While the live session
// set is unknown the request is deferred with a retryable error, so
// restartStep keeps it for a later tick instead of refusing it as
// mid-run; the command line and dashboard keep refusing that state.
func (s *supervisor) applyStoredRestart(req restartRequest) error {
	cfg, err := config.Load(s.root)
	if err != nil {
		if isMissingConfig(s.root) {
			return &restartPermanentError{err}
		}
		return &restartTransientConfigError{err}
	}
	if err := cfg.CheckToken(); err != nil {
		return &restartPermanentError{err}
	}
	sessions, known := statusSessions(s.root)
	if !known {
		return fmt.Errorf("restart deferred: live sessions unknown")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tracker := s.restartTracker
	if tracker == nil {
		tracker = &ghIssueTracker{client: &github.Client{BaseURL: "https://api.github.com"}}
	}
	deps := &restartDeps{
		tracker: tracker, token: cfg.Token, slug: cfg.Repo,
		loops: cfg.Resolve(), actor: req.Actor,
	}
	deps.live = liveRestartIssues(sessions)
	if hst, herr := store.Open(dashboardStateDir()); herr == nil {
		deps.history, deps.events = hst, hst
		defer hst.Close()
	}
	res, err := applyIssueRestart(ctx, deps, req.Issue, "")
	if err != nil {
		return err
	}
	_ = res
	if werr := requestWake(s.root); werr != nil {
		fmt.Fprintln(os.Stderr, "jig: restart: wake:", werr)
	}
	return nil
}

// recordRestartOutcome writes the restart event for a stored request,
// including refusals, so the run page shows what happened to it. A
// refusal without a resolved loop is only logged: no run page could
// show it.
func (s *supervisor) recordRestartOutcome(req restartRequest, at, loop, refusal string) {
	if loop == "" {
		fmt.Fprintln(os.Stderr, "jig: restart: issue", req.Issue, refusal)
		return
	}
	hst, err := store.Open(dashboardStateDir())
	if err != nil {
		fmt.Fprintln(os.Stderr, "jig: restart: event:", err)
		return
	}
	defer hst.Close()
	ts := at
	if ts == "" {
		ts = time.Now().UTC().Format(time.RFC3339)
	}
	detail := "restarted by " + req.Actor
	if refusal != "" {
		detail = "restart refused by " + req.Actor + ": " + refusal
	}
	project := restartProject(s.root)
	if project == "" {
		if p, _ := restartHistoryLocation(req.Issue, restartDropRepo(s.root)); p != "" {
			project = p
		}
	}
	ev := store.Event{
		TS: ts, Project: project, Workflow: loop,
		IssueID: strconv.Itoa(req.Issue), Kind: restartEventKind, Detail: detail,
	}
	if err := hst.AddEvents([]store.Event{ev}); err != nil {
		fmt.Fprintln(os.Stderr, "jig: restart: event:", err)
	}
}

// restartHistoryLocation returns the project and workflow of the most
// recent stored run of an issue in repo, across that repo's workflows in
// history. Stored-request drops use it when the config no longer loads
// and no loop was resolved. An empty repo constrains nothing, matching
// the pre-filter behavior when this supervisor's slug is unknown. Empty
// when history has no run of the issue in repo.
func restartHistoryLocation(issue int, repo string) (string, string) {
	hst, err := store.Open(dashboardStateDir())
	if err != nil {
		return "", ""
	}
	defer hst.Close()
	opts, err := hst.RunOptions()
	if err != nil {
		return "", ""
	}
	want := strconv.Itoa(issue)
	bestProject, bestLoop := "", ""
	var bestAt time.Time
	have := false
	for _, p := range opts.Projects {
		if repo != "" && p != repo {
			continue
		}
		for _, w := range opts.Workflows {
			runs, err := hst.IssueRuns(p, w, want)
			if err != nil {
				continue
			}
			for _, r := range runs {
				at := parseRestartTime(r.StartedAt)
				if !have || at.After(bestAt) {
					bestProject, bestLoop, bestAt, have = p, w, at, true
				}
			}
		}
	}
	return bestProject, bestLoop
}

// restartProject is the repo slug of a root, empty when unknown. The
// event carries it so the run page can find the restart.
func restartProject(root string) string {
	cfg, err := config.Load(root)
	if err != nil {
		return ""
	}
	return cfg.Repo
}

// restartDropRepo is the repo slug constraining the history fallback of
// a stored-request drop. It prefers the config slug and falls back to
// the status document's repo name, which carries the slug when the
// config was readable at status-write time. Empty when neither yields a
// slug, leaving the history scan unconstrained.
func restartDropRepo(root string) string {
	if slug := restartProject(root); slug != "" {
		return slug
	}
	if doc, err := readStatusDoc(root); err == nil && strings.Contains(doc.RepoName, "/") {
		return doc.RepoName
	}
	return ""
}

// printRestartResult reports one restart in human form.
func printRestartResult(root string, res issueRestartResult) {
	if res.Pending {
		fmt.Printf("jig: %s: issue %d %s by %s\n", root, res.Issue, res.Effects[0], res.Actor)
		return
	}
	fmt.Printf("jig: %s: issue %d restarted in %s by %s: %s\n",
		root, res.Issue, res.Loop, res.Actor, strings.Join(res.Effects, ", "))
}
