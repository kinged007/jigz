package store

import (
	"fmt"
	"testing"
)

func qitem(number int, labels ...string) QuestionItem {
	return QuestionItem{
		Project:      "o/a",
		Number:       number,
		Title:        "T",
		URL:          fmt.Sprintf("https://x/%d", number),
		Labels:       labels,
		IssueETag:    `"ie"`,
		CommentsETag: `"ce"`,
		ScannedAt:    "2026-10-03T00:00:00Z",
	}
}

func question(qid string) Question {
	return Question{QID: qid, Audience: "product", Blocking: true, Question: "q " + qid}
}

func qids(t *testing.T, s *Store, f QuestionFilter) []string {
	t.Helper()
	qs, err := s.Questions(f)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, q := range qs {
		out = append(out, q.QID)
	}
	return out
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestReplaceQuestionsReplacesAnItemsRows(t *testing.T) {
	s := open(t)
	it := qitem(3, "agent:x")
	it.Problems = []string{"bad block"}
	if err := s.ReplaceQuestions(it, []Question{question("q1"), question("q2")}); err != nil {
		t.Fatal(err)
	}

	next := qitem(3, "agent:y")
	next.Title = "T2"
	if err := s.ReplaceQuestions(next, []Question{question("q3")}); err != nil {
		t.Fatal(err)
	}

	got, err := s.QuestionItem("o/a", 3)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "T2" || !sameStrings(got.Labels, []string{"agent:y"}) || len(got.Problems) != 0 {
		t.Errorf("item = %+v, want updated title, labels and empty problems", got)
	}
	if want := []string{"q3"}; !sameStrings(qids(t, s, QuestionFilter{}), want) {
		t.Errorf("questions = %v, want %v", qids(t, s, QuestionFilter{}), want)
	}
}

func TestFirstLabelsKeptAcrossPasses(t *testing.T) {
	s := open(t)
	firstLabels := func() []string {
		t.Helper()
		got, err := s.QuestionItem("o/a", 3)
		if err != nil {
			t.Fatal(err)
		}
		return got.FirstLabels
	}

	// A pass that finds no question does not set first_labels.
	it := qitem(3, "agent:x")
	if err := s.ReplaceQuestions(it, nil); err != nil {
		t.Fatal(err)
	}
	if got := firstLabels(); len(got) != 0 {
		t.Fatalf("first_labels after empty pass = %v, want none", got)
	}

	// The first pass that finds a question stores that pass's labels.
	it.Labels = []string{"agent:x", "agent:y"}
	if err := s.ReplaceQuestions(it, []Question{question("q1")}); err != nil {
		t.Fatal(err)
	}
	if got := firstLabels(); !sameStrings(got, []string{"agent:x", "agent:y"}) {
		t.Fatalf("first_labels = %v, want [agent:x agent:y]", got)
	}

	// Later passes never change it, with or without questions.
	it.Labels = []string{"agent:z"}
	if err := s.ReplaceQuestions(it, []Question{question("q2")}); err != nil {
		t.Fatal(err)
	}
	if got := firstLabels(); !sameStrings(got, []string{"agent:x", "agent:y"}) {
		t.Errorf("first_labels = %v, want kept [agent:x agent:y]", got)
	}
	if err := s.ReplaceQuestions(it, nil); err != nil {
		t.Fatal(err)
	}
	if got := firstLabels(); !sameStrings(got, []string{"agent:x", "agent:y"}) {
		t.Errorf("first_labels = %v, want kept [agent:x agent:y]", got)
	}
}

func TestQuestionsFilters(t *testing.T) {
	s := open(t)
	answered := question("q2")
	answered.Audience = "project"
	answered.Answer = "do X"
	answered.AnsweredAt = "2026-10-03T00:00:00Z"
	if err := s.ReplaceQuestions(qitem(1), []Question{question("q1"), answered}); err != nil {
		t.Fatal(err)
	}
	answered3 := question("q3")
	answered3.Blocking = false
	answered3.Answer = "do Y"
	answered3.AnsweredAt = "2026-10-03T00:00:00Z"
	if err := s.ReplaceQuestions(qitem(2), []Question{answered3}); err != nil {
		t.Fatal(err)
	}
	b := qitem(1)
	b.Project = "o/b"
	if err := s.ReplaceQuestions(b, []Question{question("q4")}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		f    QuestionFilter
		want []string
	}{
		{"all", QuestionFilter{}, []string{"q1", "q2", "q3", "q4"}},
		{"audience=product", QuestionFilter{Audience: "product"}, []string{"q1", "q3", "q4"}},
		{"audience=project", QuestionFilter{Audience: "project"}, []string{"q2"}},
		{"blocking=true", QuestionFilter{Blocking: "true"}, []string{"q1", "q2", "q4"}},
		{"blocking=false", QuestionFilter{Blocking: "false"}, []string{"q3"}},
		{"project", QuestionFilter{Project: "o/b"}, []string{"q4"}},
		{"state=open", QuestionFilter{State: "open"}, []string{"q1", "q4"}},
		{"state=answered", QuestionFilter{State: "answered"}, []string{"q2", "q3"}},
	} {
		if got := qids(t, s, tc.f); !sameStrings(got, tc.want) {
			t.Errorf("%s: questions = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPreviousVersionMigratesToQuestionTables(t *testing.T) {
	dir := t.TempDir()
	saved := migrations
	migrations = saved[:len(saved)-1]
	old, err := Open(dir)
	migrations = saved
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`INSERT INTO github_cache (project, kind, number, fetched_at, etag)
		VALUES ('o/a','pr',4,'2026-10-01T00:00:00Z','"e"')`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, err := s.Version(); err != nil || v != len(migrations) {
		t.Fatalf("version = %d, %v, want %d", v, err, len(migrations))
	}
	for _, table := range []string{"question_items", "questions"} {
		if _, err := s.db.Exec("SELECT 1 FROM " + table); err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
	if rows, err := s.GitHubRows("o/a"); err != nil || rows[GitHubKey(GitHubPR, 4)].ETag != `"e"` {
		t.Errorf("github_cache preserved: %v, %v", rows, err)
	}
	if err := s.ReplaceQuestions(qitem(4), []Question{question("q1")}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"q1"}; !sameStrings(qids(t, s, QuestionFilter{}), want) {
		t.Errorf("questions = %v, want %v", qids(t, s, QuestionFilter{}), want)
	}
}
