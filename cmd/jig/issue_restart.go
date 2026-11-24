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
// the status file. One file per request, so concurrent restarts never
// overwrite each other; the supervisor consumes them on its next tick.
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
// means a live engine session holds the issue; loop and trigger name
// the owning loop and the label to ensure, empty when unknown; claim is
// the loop's claim marker.
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

// latestRestartWorkflow is the history fallback for a
// claim-without-trigger issue: the workflow of its most recent stored
// run. A nil store means no history is available.
func latestRestartWorkflow(st *store.Store, project string, resolved map[string]config.Resolved, num int) string {
	if st == nil {
		return ""
	}
	best, bestID := "", int64(-1)
	for _, n := range sortedKeys(resolved) {
		runs, err := st.IssueRuns(project, n, strconv.Itoa(num))
		if err != nil {
			continue
		}
		for _, r := range runs {
			if r.ID > bestID {
				best, bestID = n, r.ID
			}
		}
	}
	return best
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

// restartDeps is everything applying a restart needs.
type restartDeps struct {
	tracker issueTracker
	token   string
	slug    string
	loops   map[string]config.Resolved
	live    map[string]bool
	history *store.Store
	events  *store.Store
	now     func() time.Time
	actor   string
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
		if w := latestRestartWorkflow(d.history, d.slug, d.loops, num); w != "" && (hintLoop == "" || w == hintLoop) {
			loop, trigger = w, triggerOf(d.loops, w)
		}
	}
	if hintLoop != "" && loop != hintLoop {
		return res, &restartRefusal{reason: fmt.Sprintf("issue %d belongs to another loop", num)}
	}
	act := planRestart(num, labels, isPR, d.live[strconv.Itoa(num)], loop, trigger, claimLabelOf(d.loops, loop))
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

// requestRestartPath is the file one restart request is stored in.
func requestRestartPath(root string, at time.Time) (string, error) {
	prefix, err := cleanupFilePath(root, restartRequestSuffix)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%d-%d", prefix, at.UnixNano(), os.Getpid()), nil
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
// runs leave exactly one pending re-queue.
func storeRestartRequest(root string, req restartRequest) (already bool, err error) {
	pending, err := pendingRestartIssues(root)
	if err != nil {
		return false, err
	}
	for _, p := range pending {
		if p.Issue == req.Issue {
			return true, nil
		}
	}
	path, err := requestRestartPath(root, req.At)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	data, _ := json.Marshal(req)
	if err := writeFileAtomic(path, data); err != nil {
		return false, err
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
	deps.live = liveRestartIssues(statusSessions(root))
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
// knows about, empty when no supervisor serves it.
func statusSessions(root string) []string {
	if _, err := runningEntry(root); err != nil {
		return nil
	}
	doc, err := readStatusDoc(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, w := range doc.Workflows {
		for _, s := range w.Sessions {
			out = append(out, s.IssueID)
		}
	}
	return out
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
// was down or busy, oldest first. Each file is removed before its
// request runs, so a request applies exactly once even when the run
// restarts mid-sequence. A request that no longer restarts is final:
// the file is gone and the outcome is recorded as an event. A tracker
// or state failure keeps the file for the next tick, so the issue stays
// restartable.
func (s *supervisor) restartStep() {
	prefix, err := cleanupFilePath(s.root, restartRequestSuffix)
	if err != nil {
		return
	}
	files, _ := filepath.Glob(prefix + "*")
	sort.Strings(files)
	for _, f := range files {
		data, rerr := os.ReadFile(f)
		var req restartRequest
		if rerr != nil || json.Unmarshal(data, &req) != nil || req.Issue <= 0 {
			fmt.Fprintln(os.Stderr, "jig: restart: dropping an unreadable request file", filepath.Base(f))
			_ = os.Remove(f)
			continue
		}
		if err := s.applyStoredRestart(req); err != nil {
			var refusal *restartRefusal
			if errors.As(err, &refusal) {
				_ = os.Remove(f)
				s.recordRestartOutcome(req, "", refusal.loop, refusal.reason)
				continue
			}
			fmt.Fprintln(os.Stderr, "jig: restart:", err)
			return
		}
		_ = os.Remove(f)
	}
}

// applyStoredRestart runs one stored restart request to completion:
// labels, event and wake, like the command line.
func (s *supervisor) applyStoredRestart(req restartRequest) error {
	cfg, err := config.Load(s.root)
	if err != nil {
		return err
	}
	if err := cfg.CheckToken(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tracker := &ghIssueTracker{client: &github.Client{BaseURL: "https://api.github.com"}}
	deps := &restartDeps{
		tracker: tracker, token: cfg.Token, slug: cfg.Repo,
		loops: cfg.Resolve(), actor: req.Actor,
	}
	deps.live = liveRestartIssues(statusSessions(s.root))
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
	ev := store.Event{
		TS: ts, Project: restartProject(s.root), Workflow: loop,
		IssueID: strconv.Itoa(req.Issue), Kind: restartEventKind, Detail: detail,
	}
	if err := hst.AddEvents([]store.Event{ev}); err != nil {
		fmt.Fprintln(os.Stderr, "jig: restart: event:", err)
	}
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

// printRestartResult reports one restart in human form.
func printRestartResult(root string, res issueRestartResult) {
	if res.Pending {
		fmt.Printf("jig: %s: issue %d %s by %s\n", root, res.Issue, res.Effects[0], res.Actor)
		return
	}
	fmt.Printf("jig: %s: issue %d restarted in %s by %s: %s\n",
		root, res.Issue, res.Loop, res.Actor, strings.Join(res.Effects, ", "))
}
