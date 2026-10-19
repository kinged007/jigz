package store

import "testing"

func withPR(r Run) Run {
	r.WorkflowKind = KindPR
	return r
}

func TestV6DatabaseMigratesToDetailColumnsKeepingCachedPullRequests(t *testing.T) {
	dir := t.TempDir()
	saved := migrations
	migrations = saved[:6]
	old, err := Open(dir)
	migrations = saved
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`INSERT INTO github_cache (project, kind, number, branch, url, state, title, fetched_at, etag)
		VALUES ('o/a','pr',4,'b','https://x/4','open','T','2026-10-01T00:00:00Z','"e"')`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`INSERT INTO github_links VALUES ('o/a', 3, 4)`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.GitHubRows("o/a")
	if err != nil {
		t.Fatal(err)
	}
	r := rows[GitHubKey(GitHubPR, 4)]
	if r.State != PROpen || r.ETag != `"e"` || r.Details.At != "" || r.Details.Commits != 0 {
		t.Fatalf("migrated row = %+v, want the old row without details", r)
	}
	pr := withPR(run("review", 4, "2026-10-01T00:00:00Z", "succeeded"))
	prs, err := s.RunPRs(pr)
	if err != nil || len(prs) != 1 || prs[0].Details != nil {
		t.Fatalf("prs = %+v, %v, want one without details", prs, err)
	}
	if err := s.SaveDetails("o/a", 4, PRDetails{At: "t", Commits: 2, Messages: []string{"a", "b"}, ChecksFailed: 1}); err != nil {
		t.Fatal(err)
	}
	prs, _ = s.RunPRs(pr)
	if d := prs[0].Details; d == nil || d.Commits != 2 || len(d.Messages) != 2 || d.ChecksFailed != 1 {
		t.Errorf("details = %+v", d)
	}
}

func TestJourneyRunsFollowLinksToPullRequestWorkflows(t *testing.T) {
	s := open(t)
	if err := s.SaveIssueLinks("o/a", 3, "", "2026-10-01T00:00:00Z", []int{4, 5}); err != nil {
		t.Fatal(err)
	}
	issue := run("build", 3, "2026-10-01T00:00:00Z", "succeeded")
	issue.WorkflowKind = KindIssue
	if err := s.AddRuns([]Run{
		issue,
		withPR(run("merge", 5, "2026-10-03T00:00:00Z", "succeeded")),
		withPR(run("review", 4, "2026-10-02T00:00:00Z", "failed")),
		withPR(run("review", 9, "2026-10-02T00:00:00Z", "failed")),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.JourneyRuns("o/a", "3")
	if err != nil || len(got) != 2 || got[0].Workflow != "review" || got[1].Workflow != "merge" {
		t.Fatalf("journey = %+v, %v, want the review run of PR 4 then the merge run of PR 5", got, err)
	}
	if got, _ := s.JourneyRuns("o/a", "8"); len(got) != 0 {
		t.Errorf("journey of an unlinked issue = %+v", got)
	}
}
