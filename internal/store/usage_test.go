package store

import (
	"reflect"
	"testing"
)

func usageRun(id int64, project, workflow, issue, status, started string, tokens int64, measured bool) Run {
	return Run{
		Project: project, Workflow: workflow, ID: id, IssueID: issue, DisplayIdentifier: project + "#" + issue,
		Status: status, StartedAt: started, CompletedAt: started, Attempt: 1, AgentAdapter: "claude-code",
		Model: "m1", InputTokens: tokens * 3 / 4, OutputTokens: tokens / 4, CacheReadTokens: tokens / 2,
		TotalTokens: tokens, TokensMeasured: measured,
	}
}

func inKind(r Run, kind string) Run {
	r.WorkflowKind = kind
	return r
}

func seedUsage(t *testing.T) *Store {
	t.Helper()
	s := open(t)
	err := s.AddRuns([]Run{
		usageRun(1, "a/x", "build", "1", "succeeded", "2026-10-01T23:59:59Z", 100, true),
		usageRun(2, "a/x", "build", "1", "failed", "2026-10-02T00:00:00Z", 50, true),
		usageRun(3, "a/x", "plan", "1", "succeeded", "2026-10-02T10:00:00Z", 10, true),
		inKind(usageRun(4, "a/x", "review", "9", "succeeded", "2026-10-02T11:00:00Z", 200, true), KindPR),
		usageRun(5, "a/y", "build", "1", "cancelled", "2026-10-02T12:00:00Z", 0, false),
		inKind(usageRun(6, "a/y", "review", "9", "failed", "2026-10-03T00:00:01Z", 400, true), KindPR),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUsageByDayBucketsUTCAndCountsDistinct(t *testing.T) {
	s := seedUsage(t)
	got, err := s.Usage(UsageFilter{}, "day")
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		Key                                 string
		Runs, Succeeded, Failed, Unmeasured int
		Issues, PRs                         int
		Total                               int64
	}
	var rows []row
	for _, g := range got {
		rows = append(rows, row{g.Key, g.Runs, g.Succeeded, g.Failed, g.Unmeasured, g.Issues, g.PRs, g.Total})
	}
	want := []row{
		{"2026-10-01", 1, 1, 0, 0, 1, 0, 100},
		{"2026-10-02", 4, 2, 1, 1, 2, 1, 260},
		{"2026-10-03", 1, 0, 1, 0, 0, 1, 400},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("by day = %+v\nwant %+v", rows, want)
	}
}

func TestUsageFiltersByPeriodAndSkipsUnmeasuredTokens(t *testing.T) {
	s := seedUsage(t)
	got, err := s.Usage(UsageFilter{Since: "2026-10-02T00:00:00Z", Until: "2026-10-03T00:00:00Z"}, "all")
	if err != nil || len(got) != 1 {
		t.Fatalf("all = %+v, %v", got, err)
	}
	g := got[0]
	if g.Runs != 4 || g.Total != 260 || g.Unmeasured != 1 || g.Issues != 2 || g.PRs != 1 {
		t.Errorf("window = %+v", g.Stats)
	}
	if g.CacheRead != 130 || g.Input != 194 || g.Output != 64 {
		t.Errorf("token split = %d/%d/%d", g.Input, g.Output, g.CacheRead)
	}
	empty, err := s.Usage(UsageFilter{Since: "2027-01-01T00:00:00Z"}, "all")
	if err != nil || len(empty) != 1 || empty[0].Runs != 0 || empty[0].Total != 0 {
		t.Errorf("empty window = %+v, %v", empty, err)
	}
}

func TestUsageGroupsByProjectWorkflowAndModel(t *testing.T) {
	s := seedUsage(t)
	byProject, _ := s.Usage(UsageFilter{}, "project")
	if len(byProject) != 2 || byProject[0].Key != "a/y" || byProject[0].Total != 400 || byProject[1].Total != 360 {
		t.Errorf("by project (largest first) = %+v", byProject)
	}
	byWorkflow, _ := s.Usage(UsageFilter{Project: "a/x"}, "workflow")
	if len(byWorkflow) != 3 || byWorkflow[0].Workflow != "review" || byWorkflow[0].Project != "a/x" ||
		byWorkflow[0].Key != "a/x review" {
		t.Errorf("by workflow = %+v", byWorkflow)
	}
	byModel, _ := s.Usage(UsageFilter{Workflow: "build"}, "model")
	if len(byModel) != 1 || byModel[0].Key != "m1" || byModel[0].Runs != 3 {
		t.Errorf("by model = %+v", byModel)
	}
	if _, err := s.Usage(UsageFilter{}, "bogus"); err == nil {
		t.Error("an unknown grouping must be an error")
	}
}

func TestDailyTokensPerProjectAndWorkflow(t *testing.T) {
	s := seedUsage(t)
	got, err := s.DailyTokens(UsageFilter{}, "project")
	if err != nil {
		t.Fatal(err)
	}
	want := []DayTokens{
		{Day: "2026-10-01", Project: "a/x", Total: 100},
		{Day: "2026-10-02", Project: "a/x", Total: 260},
		{Day: "2026-10-02", Project: "a/y", Total: 0},
		{Day: "2026-10-03", Project: "a/y", Total: 400},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("per project = %+v", got)
	}
	wf, _ := s.DailyTokens(UsageFilter{Project: "a/x", Since: "2026-10-02T00:00:00Z"}, "workflow")
	if len(wf) != 3 || wf[0].Workflow != "build" || wf[0].Total != 50 {
		t.Errorf("per workflow = %+v", wf)
	}
}

func TestItemTokensSumOverStagesAndRetries(t *testing.T) {
	s := seedUsage(t)
	got, err := s.ItemTokens(UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	// Issue a/x#1 over build, retry and plan is 160 tokens; a/y#1 is unmeasured and left out.
	want := ItemTokens{Issues: 1, PRs: 2, IssueTokens: 160, PRTokens: 600}
	if got != want {
		t.Errorf("item tokens = %+v, want %+v", got, want)
	}
}

func TestTopItemsOrderedByTokens(t *testing.T) {
	s := seedUsage(t)
	got, err := s.TopItems(UsageFilter{}, 2)
	if err != nil || len(got) != 2 {
		t.Fatalf("top = %+v, %v", got, err)
	}
	if got[0].Project != "a/y" || got[0].Total != 400 || got[1].IssueID != "9" || got[1].Total != 200 {
		t.Errorf("top = %+v", got)
	}
	if got[0].DisplayIdentifier != "a/y#9" {
		t.Errorf("identifier = %q", got[0].DisplayIdentifier)
	}
}

func TestWorkflowKindDecidesIssueOrPRNotTheName(t *testing.T) {
	s := open(t)
	err := s.AddRuns([]Run{
		inKind(usageRun(1, "a/x", "pr-fixer", "1", "succeeded", "2026-10-02T10:00:00Z", 10, true), KindIssue),
		inKind(usageRun(2, "a/x", "audit", "2", "succeeded", "2026-10-02T10:00:00Z", 10, true), KindPR),
		usageRun(3, "a/x", "improve", "3", "succeeded", "2026-10-02T10:00:00Z", 10, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	all, _ := s.Usage(UsageFilter{}, "all")
	if all[0].Issues != 2 || all[0].PRs != 1 {
		t.Errorf("issues, prs = %d, %d, want 2 (issue and unknown kind) and 1", all[0].Issues, all[0].PRs)
	}
	if err := s.SetWorkflowKind("a/x", "improve", KindPR); err != nil {
		t.Fatal(err)
	}
	all, _ = s.Usage(UsageFilter{}, "all")
	if all[0].Issues != 1 || all[0].PRs != 2 {
		t.Errorf("after backfill issues, prs = %d, %d, want 1 and 2", all[0].Issues, all[0].PRs)
	}
	// Re-adding a run without a kind keeps the stored kind.
	if err := s.AddRuns([]Run{usageRun(3, "a/x", "improve", "3", "succeeded", "2026-10-02T10:00:00Z", 10, true)}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Run("a/x", "improve", 3); r.WorkflowKind != KindPR {
		t.Errorf("kind after re-add = %q, want pr", r.WorkflowKind)
	}
}
