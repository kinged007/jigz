package collector

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/fakegithub"
	"github.com/kinged007/jigz/internal/store"
)

func (r *qRig) addWithBody(author, assoc, body string, labels ...string) int {
	return r.fake.Add(fakegithub.Item{
		Repo: qRepo, Title: "T", Body: body, Author: author, Association: assoc, Labels: labels,
	})
}

func (r *qRig) questionsOf(t *testing.T) map[string]store.Question {
	t.Helper()
	out := map[string]store.Question{}
	for _, q := range r.stored(t, store.QuestionFilter{}) {
		out[q.QID] = q
	}
	return out
}

func TestBodyQuestionIsParsed(t *testing.T) {
	r := newQRig(t)
	n := r.addWithBody("bot", "OWNER", "Intro\n\n"+qblock("q-1", true, "agent:plan"), "needs-human", "agent:done")
	r.Pass(context.Background())
	q, ok := r.questionsOf(t)["q-1"]
	if !ok {
		t.Fatal("question in the description not stored")
	}
	if q.AskedCommentID != 0 || q.AskedCommentURL != "https://github.com/acme/app/issues/1" || q.AskedAt == "" || q.AnsweredAt != "" {
		t.Errorf("question = %+v", q)
	}
	if got := r.labelWrites(n); len(got) != 0 {
		t.Errorf("label writes = %v", got)
	}
}

func TestBodyQuestionFromUnauthorizedAuthorIsIgnored(t *testing.T) {
	r := newQRig(t)
	r.addWithBody("eve", "NONE", qblock("q-1", true, "agent:plan"), "needs-human")
	r.Pass(context.Background())
	if qs := r.questionsOf(t); len(qs) != 0 {
		t.Errorf("questions = %+v, want none", qs)
	}
}

func TestBodyQuestionFromAssigneeAndTokenAccount(t *testing.T) {
	r := newQRig(t)
	n := r.addWithBody("helper", "NONE", qblock("q-1", false, ""), "needs-human")
	r.fake.SetAssignees(qRepo, n, "helper")
	r.addWithBody("bot", "NONE", qblock("q-2", false, ""), "needs-human")
	r.Pass(context.Background())
	qs := r.questionsOf(t)
	if _, ok := qs["q-1"]; !ok {
		t.Error("assignee's description question not stored")
	}
	if _, ok := qs["q-2"]; !ok {
		t.Error("token account's description question not stored")
	}
}

func TestBodyAndCommentQuestionsTogether(t *testing.T) {
	r := newQRig(t)
	n := r.addWithBody("bot", "OWNER", qblock("q-1", true, "agent:plan"), "needs-human", "agent:done")
	id := r.fake.AddComment(qRepo, n, fakegithub.Comment{Author: "bot", Association: "OWNER", Body: qblock("q-2", true, "agent:plan")})
	r.Pass(context.Background())
	qs := r.questionsOf(t)
	if len(qs) != 2 {
		t.Fatalf("questions = %+v", qs)
	}
	if qs["q-1"].AskedCommentID != 0 {
		t.Errorf("q-1 comment id = %d, want 0", qs["q-1"].AskedCommentID)
	}
	if qs["q-2"].AskedCommentID != id {
		t.Errorf("q-2 comment id = %d, want %d", qs["q-2"].AskedCommentID, id)
	}
}

func TestAnswerBlockInBodyIsIgnored(t *testing.T) {
	r := newQRig(t)
	n := r.addWithBody("bot", "OWNER", qblock("q-1", true, "agent:plan")+"\n\n"+ablock("q-1", "from the body"), "needs-human", "agent:done")
	r.Pass(context.Background())
	q := r.questionsOf(t)["q-1"]
	if q.AnsweredAt != "" || len(q.IgnoredAnswers) != 0 {
		t.Errorf("question = %+v, want unanswered", q)
	}
	if got := r.problemKinds(t, n); len(got) != 0 {
		t.Errorf("problems = %v", got)
	}
	if got := r.labelWrites(n); len(got) != 0 {
		t.Errorf("label writes = %v", got)
	}
}

func TestBodyQuestionResumesOnCommentAnswer(t *testing.T) {
	r := newQRig(t)
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	r.fake.SetClock(func() time.Time { return clock })
	n := r.addWithBody("bot", "OWNER", qblock("q-1", true, "agent:plan"), "agent:done")
	clock = clock.Add(2 * time.Second)
	r.fake.AddLabelAsPerson(qRepo, n, "needs-human", "bot")
	r.Pass(context.Background())
	if got := r.labelWrites(n); len(got) != 0 {
		t.Fatalf("label writes before an answer = %v", got)
	}

	clock = clock.Add(5 * time.Second)
	r.comment(n, "alice", "COLLABORATOR", ablock("q-1", "go"))
	r.Pass(context.Background())
	if got := r.labelWrites(n); !slices.Equal(got, resumeWrites) {
		t.Errorf("label writes = %v, want %v", got, resumeWrites)
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"agent:plan"}) {
		t.Errorf("labels = %v", got)
	}
}

func TestBodyEditIsRescanned(t *testing.T) {
	r := newQRig(t)
	n := r.addWithBody("bot", "OWNER", "no question yet", "needs-human", "agent:done")
	r.Pass(context.Background())
	if qs := r.questionsOf(t); len(qs) != 0 {
		t.Fatalf("questions = %+v", qs)
	}
	r.fake.SetBody(qRepo, n, qblock("q-1", false, ""))
	r.now = r.now.Add(5 * time.Minute)
	r.Pass(context.Background())
	if _, ok := r.questionsOf(t)["q-1"]; !ok {
		t.Error("question added by editing the description not stored")
	}
}
