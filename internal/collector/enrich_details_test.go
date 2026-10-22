package collector

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/store"
)

func detailPull(n int, state, merged string, commits int) string {
	m := "null"
	if merged != "" {
		m = `"` + merged + `"`
	}
	return fmt.Sprintf(`{"number":%d,"html_url":"https://github.com/acme/app/pull/%d","state":%q,"title":"PR %d",
		"created_at":"2026-10-01T10:00:00Z","closed_at":null,"merged_at":%s,"head":{"ref":"b%d","sha":"abc%d"},
		"commits":%d,"changed_files":4,"additions":30,"deletions":7}`, n, n, state, n, m, n, n, commits)
}

func commitsJSON(n int) string {
	var parts []string
	for i := 1; i <= n; i++ {
		parts = append(parts, fmt.Sprintf(`{"commit":{"message":"commit %d\n\nbody %d"}}`, i, i))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

const checksJSON = `{"check_runs":[
	{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"skipped"},
	{"status":"completed","conclusion":"failure"},{"status":"in_progress","conclusion":null}]}`

func detailsRig(t *testing.T, details bool, n int, state string) (*enrichRig, store.Run) {
	run := ghRun(1, "acme/app", "review", store.KindPR, fmt.Sprint(n), "2026-10-01T00:00:00Z")
	r := newEnrichRig(t, run)
	r.cfg.Details = details
	merged := ""
	if state == "merged" {
		state, merged = "closed", "2026-10-02T09:00:00Z"
	}
	r.gh.pulls[fmt.Sprintf("acme/app/%d", n)] = detailPull(n, state, merged, 5)
	r.gh.commits[fmt.Sprintf("acme/app/%d", n)] = commitsJSON(5)
	r.gh.checks[fmt.Sprintf("acme/app/abc%d", n)] = checksJSON
	return r, run
}

func (r *enrichRig) resetRequests() {
	r.gh.mu.Lock()
	r.gh.reqs = nil
	r.gh.mu.Unlock()
}

func TestDetailsOffMakesNoExtraRequests(t *testing.T) {
	r, run := detailsRig(t, false, 5, "open")
	r.Pass(context.Background())
	if n := r.gh.count(); n != 1 {
		t.Errorf("requests = %d, want 1 (%q)", n, r.gh.reqs)
	}
	if p := prs(t, r.st, run)[0]; p.Details != nil {
		t.Errorf("details = %+v, want none", p.Details)
	}
}

func TestDetailsNeedEnrichment(t *testing.T) {
	r, _ := detailsRig(t, true, 5, "open")
	r.cfg.Enrich = false
	r.Pass(context.Background())
	if n := r.gh.count(); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
}

func TestDetailsAreStoredAndBounded(t *testing.T) {
	r, run := detailsRig(t, true, 5, "merged")
	r.Pass(context.Background())
	if n := r.gh.count(); n != 3 {
		t.Errorf("requests = %d, want 3 (%q)", n, r.gh.reqs)
	}
	d := prs(t, r.st, run)[0].Details
	if d == nil || d.Commits != 5 || d.Files != 4 || d.Additions != 30 || d.Deletions != 7 ||
		d.ChecksOK != 2 || d.ChecksFailed != 1 || d.ChecksPending != 1 {
		t.Fatalf("details = %+v", d)
	}
	if got := strings.Join(d.Messages, "|"); got != "commit 5|commit 4|commit 3" {
		t.Errorf("messages = %q, want the newest three, first lines only", got)
	}
	long := strings.Repeat("x", 500)
	if err := r.st.SaveDetails("acme/app", 5, store.PRDetails{At: "t", Messages: []string{long, "b", "c", "d"}}); err != nil {
		t.Fatal(err)
	}
	d = prs(t, r.st, run)[0].Details
	if len(d.Messages) != store.MaxCommitMessages || len([]rune(d.Messages[0])) != store.MaxMessageRunes {
		t.Errorf("stored messages = %d, first %d runes", len(d.Messages), len([]rune(d.Messages[0])))
	}
}

func TestMergedPullRequestDetailsAreFetchedOnce(t *testing.T) {
	r, _ := detailsRig(t, true, 5, "merged")
	r.Pass(context.Background())
	r.now = r.now.Add(48 * time.Hour)
	r.Pass(context.Background())
	if n := r.gh.count(); n != 3 {
		t.Errorf("requests = %d, want 3", n)
	}
}

func TestSwitchingDetailsOnFetchesAlreadyCachedPullRequests(t *testing.T) {
	r, run := detailsRig(t, false, 5, "merged")
	r.Pass(context.Background())
	r.cfg.Details = true
	r.Pass(context.Background())
	if n := r.gh.count(); n != 4 {
		t.Errorf("requests = %d, want 1 plus 3 (%q)", n, r.gh.reqs)
	}
	if d := prs(t, r.st, run)[0].Details; d == nil || d.Commits != 5 {
		t.Errorf("details = %+v", d)
	}
}

func TestOpenPullRequestDetailsAreConditional(t *testing.T) {
	r, run := detailsRig(t, true, 5, "open")
	r.Pass(context.Background())
	r.resetRequests()
	r.now = r.now.Add(10 * time.Minute)
	r.Pass(context.Background())
	if len(r.gh.reqs) != 2 || !strings.Contains(r.gh.reqs[0], `"`) || !strings.Contains(r.gh.reqs[1], "check-runs") ||
		!strings.Contains(r.gh.reqs[1], `"`) {
		t.Fatalf("requests = %q, want the pull request and the checks, both with an ETag", r.gh.reqs)
	}
	if d := prs(t, r.st, run)[0].Details; d == nil || d.ChecksFailed != 1 || len(d.Messages) != 3 {
		t.Errorf("details = %+v, want kept after the 304s", d)
	}
	r.gh.checks["acme/app/abc5"] = `{"check_runs":[{"status":"completed","conclusion":"success"}]}`
	r.now = r.now.Add(10 * time.Minute)
	r.Pass(context.Background())
	if d := prs(t, r.st, run)[0].Details; d.ChecksOK != 1 || d.ChecksFailed != 0 || d.ChecksPending != 0 {
		t.Errorf("details = %+v, want the new check results", d)
	}
}

func TestDetailRequestsCountAgainstTheBudget(t *testing.T) {
	r, run := detailsRig(t, true, 5, "merged")
	r.Budget = 2
	r.Pass(context.Background())
	if n := r.gh.count(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
	if p := prs(t, r.st, run)[0]; p.Details != nil {
		t.Errorf("details = %+v, want none stored until the budget allows all of them", p.Details)
	}
	r.Budget = 0
	r.Pass(context.Background())
	if d := prs(t, r.st, run)[0].Details; d == nil {
		t.Error("details missing after the next pass")
	}
}

func TestMissingChecksDoNotRepeatEveryPass(t *testing.T) {
	r, run := detailsRig(t, true, 5, "merged")
	delete(r.gh.checks, "acme/app/abc5")
	r.Pass(context.Background())
	r.Pass(context.Background())
	if n := r.gh.count(); n != 3 {
		t.Errorf("requests = %d, want 3", n)
	}
	if p := prs(t, r.st, run)[0]; p.State != store.PRMerged || p.Details == nil || p.Details.ChecksOK != 0 {
		t.Errorf("pr = %+v, want merged with empty checks", p)
	}
}
