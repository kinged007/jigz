package questions

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const sample = `<!-- agent-question id="q-7f3a" audience="project" blocking="true" resume="agent:plan" -->
**Question:** Should a blocking question park the issue with ` + "`needs-human`" + `?

**Suggested answer:** Use ` + "`needs-human`" + `.
<!-- /agent-question -->`

func authorized(c Comment) bool {
	switch c.AuthorAssociation {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	}
	return c.Author == "assignee"
}

func comments(bodies ...string) []Comment {
	var cs []Comment
	for i, b := range bodies {
		author, assoc := "owner", "OWNER"
		if rest, ok := strings.CutPrefix(b, "@stranger "); ok {
			author, assoc, b = "stranger", "NONE", rest
		}
		cs = append(cs, Comment{
			ID: int64(i + 1), Author: author, AuthorAssociation: assoc, Body: b,
			URL:     fmt.Sprintf("https://github.com/o/r/issues/1#issuecomment-%d", i+1),
			Created: time.Date(2026, 10, 1, 0, i, 0, 0, time.UTC),
		})
	}
	return cs
}

func q(attrs, text string) string {
	return "<!-- agent-question " + attrs + " -->\n**Question:** " + text + "\n<!-- /agent-question -->"
}

func ans(id, text string) string {
	return `<!-- agent-answer id="` + id + `" by="bot" -->` + "\n" + text + "\n<!-- /agent-answer -->"
}

const blk = `id="q-1" audience="product" blocking="true" resume="agent:plan"`

// summary renders a question as "id audience blocking resume asked=N
// answer=<current text> answers=N stale=N ignored=N comment=N".
func summary(q Question) string {
	cur := "-"
	if a := q.Current(); a != nil {
		cur = a.Text
	}
	return fmt.Sprintf("%s %s %t %s asked=%d answer=%s answers=%d stale=%d ignored=%d comment=%s",
		q.ID, q.Audience, q.Blocking, q.Resume, q.AskedCount, cur, len(q.Answers), len(q.Stale), len(q.Ignored),
		q.CommentURL[strings.LastIndex(q.CommentURL, "-")+1:])
}

func TestParse(t *testing.T) {
	nb := `id="q-1" audience="product" blocking="false"`
	quote := func(s string) string { return "> " + strings.ReplaceAll(s, "\n", "\n> ") }
	tests := []struct {
		name     string
		bodies   []string
		want     []string
		problems []ProblemKind
	}{
		{"sample", []string{sample}, []string{"q-7f3a project true agent:plan asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, nil},
		{"attribute order", []string{q(`resume="agent:build" blocking="true" audience="project" id="q-a_B-9"`, "x")},
			[]string{"q-a_B-9 project true agent:build asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, nil},
		{"non-blocking without resume", []string{q(nb, "x")},
			[]string{"q-1 product false  asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, nil},
		{"non-blocking with resume", []string{q(nb+` resume="agent:docs"`, "x")},
			[]string{"q-1 product false agent:docs asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, nil},
		{"resume with dash", []string{q(`id="q-1" audience="product" blocking="true" resume="agent:plan-build"`, "x")},
			[]string{"q-1 product true agent:plan-build asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, nil},
		{"two blocks in one comment", []string{q(blk, "a") + "\ntext\n" + q(`id="q-2" audience="project" blocking="false"`, "b")},
			[]string{"q-1 product true agent:plan asked=1 answer=- answers=0 stale=0 ignored=0 comment=1",
				"q-2 project false  asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, nil},
		{"multi-line marker", []string{"<!-- agent-question\n  id=\"q-1\"\n  audience=\"product\" blocking=\"false\" -->\n**Question:** x\n<!-- /agent-question -->"},
			[]string{"q-1 product false  asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, nil},
		{"marker indented three spaces", []string{"   " + q(blk, "x")},
			[]string{"q-1 product true agent:plan asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, nil},
		{"marker indented four spaces", []string{"    " + strings.ReplaceAll(q(blk, "x"), "\n", "\n    ")}, nil, nil},
		{"marker in inline code", []string{"Use `" + q(blk, "x") + "`"}, nil, nil},
		{"marker in blockquote", []string{quote(q(blk, "x"))}, nil, nil},
		{"backtick fence", []string{"Format:\n```markdown\n" + q(blk, "x") + "\n```\n"}, nil, nil},
		{"fence indented three spaces", []string{"   ```\n" + q(blk, "x") + "\n   ```\n"}, nil, nil},
		{"fence indented four spaces is no fence", []string{"    ```\n" + q(blk, "x")},
			[]string{"q-1 product true agent:plan asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, nil},
		{"fence in blockquote is no fence", []string{"> ```\n" + q(blk, "x")},
			[]string{"q-1 product true agent:plan asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, nil},
		{"tilde fence", []string{"~~~~\n" + q(blk, "x") + "\n~~~\nstill fenced\n~~~~\n"}, nil, nil},
		{"closing fence with info string", []string{"```\n```go\n" + q(blk, "x") + "\n```"}, nil, nil},
		{"backtick fence not closed by tilde", []string{"```\n~~~\n" + q(blk, "x") + "\n```"}, nil, nil},
		{"unclosed fence", []string{"```\n" + q(blk, "x")}, nil, nil},
		{"fenced end marker inside a block", []string{"<!-- agent-question " + blk + " -->\n**Question:** x\n```\n<!-- /agent-question -->\n" + q(nb, "y") + "\n```\n<!-- /agent-question -->"},
			[]string{"q-1 product true agent:plan asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, nil},
		{"nested start", []string{"<!-- agent-question " + blk + " -->\n" + q(`id="q-2" audience="product" blocking="false"`, "x")},
			nil, []ProblemKind{MalformedBlock, MalformedBlock}},
		{"nested start in answer", []string{sample, "<!-- agent-answer id=\"q-7f3a\" -->\n" + ans("q-7f3a", "x")},
			[]string{"q-7f3a project true agent:plan asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, []ProblemKind{MalformedBlock, MalformedBlock}},
		{"start without end", []string{"<!-- agent-question " + blk + " -->\n**Question:** x", q(`id="q-2" audience="product" blocking="false"`, "y")},
			[]string{"q-2 product false  asked=1 answer=- answers=0 stale=0 ignored=0 comment=2"}, []ProblemKind{MalformedBlock}},
		{"bad id", []string{q(`id="7f3a" audience="product" blocking="false"`, "x")}, nil, []ProblemKind{MalformedQuestion}},
		{"id of 64 characters", []string{q(`id="q-`+strings.Repeat("a", 64)+`" audience="product" blocking="false"`, "x")},
			[]string{"q-" + strings.Repeat("a", 64) + " product false  asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, nil},
		{"id of 65 characters", []string{q(`id="q-`+strings.Repeat("a", 65)+`" audience="product" blocking="false"`, "x")}, nil, []ProblemKind{MalformedQuestion}},
		{"missing audience", []string{q(`id="q-1" blocking="false"`, "x")}, nil, []ProblemKind{MalformedQuestion}},
		{"unknown audience", []string{q(`id="q-1" audience="team" blocking="false"`, "x")}, nil, []ProblemKind{MalformedQuestion}},
		{"unknown blocking", []string{q(`id="q-1" audience="product" blocking="yes"`, "x")}, nil, []ProblemKind{MalformedQuestion}},
		{"blocking without resume", []string{q(`id="q-1" audience="product" blocking="true"`, "x")}, nil, []ProblemKind{MalformedQuestion}},
		{"resume agent:done", []string{q(`id="q-1" audience="product" blocking="true" resume="agent:done"`, "x")}, nil, []ProblemKind{MalformedQuestion}},
		{"resume agent:", []string{q(`id="q-1" audience="product" blocking="true" resume="agent:"`, "x")}, nil, []ProblemKind{MalformedQuestion}},
		{"resume with space", []string{q(`id="q-1" audience="product" blocking="true" resume="agent:a b"`, "x")}, nil, []ProblemKind{MalformedQuestion}},
		{"resume with comma", []string{q(`id="q-1" audience="product" blocking="true" resume="agent:x,y"`, "x")}, nil, []ProblemKind{MalformedQuestion}},
		{"resume not agent", []string{q(nb+` resume="needs-human"`, "x")}, nil, []ProblemKind{MalformedQuestion}},
		{"duplicate attribute", []string{q(`id="q-1" id="q-2" audience="product" blocking="false"`, "x")}, nil, []ProblemKind{MalformedQuestion}},
		{"unquoted attribute", []string{q(`id=q-1 audience="product" blocking="false"`, "x")}, nil, []ProblemKind{MalformedQuestion}},
		{"attributes without space", []string{q(`id="q-1"audience="product" blocking="false"`, "x")}, nil, []ProblemKind{MalformedQuestion}},
		{"unknown question attribute", []string{q(nb+` by="x"`, "x")}, nil, []ProblemKind{MalformedQuestion}},
		{"unknown answer attribute", []string{sample, `<!-- agent-answer id="q-7f3a" audience="product" -->` + "\nyes\n<!-- /agent-answer -->"},
			[]string{"q-7f3a project true agent:plan asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, []ProblemKind{MalformedAnswer}},
		{"uppercase start marker", []string{"<!-- Agent-Question " + nb + " -->\n**Question:** x\n<!-- /agent-question -->"}, nil, []ProblemKind{MalformedQuestion}},
		{"uppercase end marker", []string{"<!-- agent-question " + nb + " -->\n**Question:** x\n<!-- /AGENT-QUESTION -->"}, nil, []ProblemKind{MalformedQuestion}},
		{"no question paragraph", []string{"<!-- agent-question " + blk + " -->\ntext\n<!-- /agent-question -->"}, nil, []ProblemKind{MalformedQuestion}},
		{"duplicate ids", []string{q(blk, "first"), q(`id="q-1" audience="project" blocking="true" resume="agent:build"`, "second")},
			[]string{"q-1 project true agent:build asked=2 answer=- answers=0 stale=0 ignored=0 comment=2"}, nil},
		{"asked three times, twice in one comment", []string{q(blk, "a"), q(blk, "b") + "\n" + q(blk, "c")},
			[]string{"q-1 product true agent:plan asked=3 answer=- answers=0 stale=0 ignored=0 comment=2"}, nil},
		{"answer between two blocks of one comment", []string{q(blk, "a") + "\n" + ans("q-1", "yes") + "\n" + q(blk, "b")},
			[]string{"q-1 product true agent:plan asked=2 answer=- answers=0 stale=1 ignored=0 comment=1"}, nil},
		{"answer after re-ask in one comment", []string{q(blk, "a"), ans("q-1", "old") + "\n" + q(blk, "b") + "\n" + ans("q-1", "new")},
			[]string{"q-1 product true agent:plan asked=2 answer=new answers=1 stale=1 ignored=0 comment=2"}, nil},
		{"answer before question", []string{ans("q-7f3a", "yes"), sample},
			[]string{"q-7f3a project true agent:plan asked=1 answer=- answers=0 stale=1 ignored=0 comment=2"}, nil},
		{"answered", []string{sample, ans("q-7f3a", "yes")},
			[]string{"q-7f3a project true agent:plan asked=1 answer=yes answers=1 stale=0 ignored=0 comment=1"}, nil},
		{"several answers", []string{sample, ans("q-7f3a", "yes"), ans("q-7f3a", "no")},
			[]string{"q-7f3a project true agent:plan asked=1 answer=no answers=2 stale=0 ignored=0 comment=1"}, nil},
		{"empty answer", []string{sample, ans("q-7f3a", " \n ")},
			[]string{"q-7f3a project true agent:plan asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, []ProblemKind{MalformedAnswer}},
		{"unauthorized answer", []string{sample, "@stranger " + ans("q-7f3a", "yes")},
			[]string{"q-7f3a project true agent:plan asked=1 answer=- answers=0 stale=0 ignored=1 comment=1"}, nil},
		{"unauthorized after authorized", []string{sample, ans("q-7f3a", "yes"), "@stranger " + ans("q-7f3a", "no")},
			[]string{"q-7f3a project true agent:plan asked=1 answer=yes answers=1 stale=0 ignored=1 comment=1"}, nil},
		{"unauthorized question", []string{"@stranger " + sample}, nil, []ProblemKind{IgnoredQuestion}},
		{"unauthorized re-ask", []string{sample, "@stranger " + q(`id="q-7f3a" audience="product" blocking="false"`, "x")},
			[]string{"q-7f3a project true agent:plan asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, []ProblemKind{IgnoredQuestion}},
		{"unauthorized malformed blocks", []string{"@stranger " + q(`id="bad"`, "x") + "\n" + ans("bad", "x") + "\n<!-- agent-answer id=\"q-1\" -->"},
			nil, []ProblemKind{IgnoredQuestion, IgnoredQuestion, IgnoredQuestion}},
		{"unknown-id answer", []string{sample, ans("q-other", "yes")},
			[]string{"q-7f3a project true agent:plan asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, []ProblemKind{UnknownAnswer}},
		{"malformed answer", []string{sample, ans("bad", "yes")},
			[]string{"q-7f3a project true agent:plan asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, []ProblemKind{MalformedAnswer}},
		{"fenced answer", []string{sample, "```\n" + ans("q-7f3a", "yes") + "\n```"},
			[]string{"q-7f3a project true agent:plan asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, nil},
		{"quoted answer", []string{sample, quote(ans("q-7f3a", "yes"))},
			[]string{"q-7f3a project true agent:plan asked=1 answer=- answers=0 stale=0 ignored=0 comment=1"}, nil},
		{"stray end marker", []string{"<!-- /agent-question -->"}, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Parse(comments(tt.bodies...), authorized)
			var got []string
			for _, q := range r.Questions {
				got = append(got, summary(q))
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("questions:\n got %q\nwant %q", got, tt.want)
			}
			var kinds []ProblemKind
			for _, p := range r.Problems {
				kinds = append(kinds, p.Kind)
				if p.CommentURL == "" || p.Message == "" || p.Author == "" {
					t.Errorf("problem without comment URL, author or message: %+v", p)
				}
			}
			if !reflect.DeepEqual(kinds, tt.problems) {
				t.Errorf("problems: got %v (%+v), want %v", kinds, r.Problems, tt.problems)
			}
		})
	}
}

func TestParseSortsByID(t *testing.T) {
	cs := comments(sample, ans("q-7f3a", "yes"))
	slices.Reverse(cs)
	r := Parse(cs, authorized)
	if len(r.Questions) != 1 || r.Questions[0].Current() == nil || len(r.Questions[0].Stale) != 0 {
		t.Errorf("questions = %+v", r.Questions)
	}
	if cs[0].ID != 2 {
		t.Errorf("Parse reordered the caller's slice")
	}
}

func TestParseHostileInput(t *testing.T) {
	marked := strings.Repeat(q(`id="q-2" audience="product" blocking="false"`, "x")+"\n", 50000) + "<!-- agent-question " + blk + " -->\n**Question:** x"
	open := strings.Repeat("<!-- agent-answer id=\"q-2\"\n", 50000)
	start := time.Now()
	r := Parse(comments(marked, open), authorized)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Parse took %s", d)
	}
	if len(r.Questions) != 1 || r.Questions[0].AskedCount != 50000 || len(r.Problems) != 1 || !Check(r).Malformed {
		t.Errorf("questions = %d, problems = %+v", len(r.Questions), r.Problems)
	}
}

func TestParseSampleFields(t *testing.T) {
	cs := comments(sample, ans("q-7f3a", "Use it.\n\nSecond paragraph."))
	r := Parse(cs, authorized)
	if len(r.Questions) != 1 {
		t.Fatalf("questions = %+v", r.Questions)
	}
	got := r.Questions[0]
	if got.Text != "Should a blocking question park the issue with `needs-human`?" {
		t.Errorf("text = %q", got.Text)
	}
	if got.Suggested != "Use `needs-human`." {
		t.Errorf("suggested = %q", got.Suggested)
	}
	if got.CommentURL != cs[0].URL || !got.Asked.Equal(cs[0].Created) {
		t.Errorf("asked = %s %s", got.CommentURL, got.Asked)
	}
	want := Answer{
		ID: "q-7f3a", By: "bot", Text: "Use it.\n\nSecond paragraph.", Author: "owner", AuthorAssociation: "OWNER",
		CommentURL: cs[1].URL, Created: cs[1].Created, Authorized: true,
	}
	a := got.Current()
	if a != nil {
		a.seq = 0
	}
	if a == nil || *a != want {
		t.Errorf("answer = %+v, want %+v", a, want)
	}
}

func TestParseProblemLink(t *testing.T) {
	cs := comments(sample, q(`id="q-2" audience="product" blocking="true"`, "x"))
	r := Parse(cs, authorized)
	want := []Problem{{Kind: MalformedQuestion, QuestionID: "q-2", CommentURL: cs[1].URL, Author: "owner",
		Message: `agent-question block: resume must be an agent: label other than agent:done, not ""`}}
	if !reflect.DeepEqual(r.Problems, want) {
		t.Errorf("problems = %+v", r.Problems)
	}
}

func TestParseMultilineText(t *testing.T) {
	body := "<!-- agent-question " + blk + " -->\n**Question:** one\ntwo\n**Suggested answer:** three\nfour\n\nnot part\n<!-- /agent-question -->"
	r := Parse(comments(body), authorized)
	if len(r.Questions) != 1 || r.Questions[0].Text != "one\ntwo" || r.Questions[0].Suggested != "three\nfour" {
		t.Errorf("questions = %+v", r.Questions)
	}
}

func TestParseCRLF(t *testing.T) {
	r := Parse(comments(strings.ReplaceAll("```\n"+q(blk, "x")+"\n```\n"+sample, "\n", "\r\n")), authorized)
	if len(r.Questions) != 1 || r.Questions[0].ID != "q-7f3a" || r.Questions[0].Suggested != "Use `needs-human`." || len(r.Problems) != 0 {
		t.Errorf("result = %+v", r)
	}
}

func TestParseAuthorizedSeesAnswer(t *testing.T) {
	var seen []string
	r := Parse(comments("no blocks", sample, "@stranger "+ans("q-7f3a", "yes")), func(c Comment) bool {
		seen = append(seen, c.Author)
		return c.Author == "owner"
	})
	if !reflect.DeepEqual(seen, []string{"owner", "stranger"}) {
		t.Errorf("authorized called for %v", seen)
	}
	if ig := r.Questions[0].Ignored; len(ig) != 1 || ig[0].Authorized || ig[0].AuthorAssociation != "NONE" {
		t.Errorf("ignored = %+v", ig)
	}
}

func TestCheck(t *testing.T) {
	nb := `id="q-9" audience="project" blocking="false"`
	q2 := `id="q-2" audience="product" blocking="true" resume="agent:plan"`
	tests := []struct {
		name   string
		bodies []string
		want   Status
		resume bool
	}{
		{"no blocks", []string{"hello"}, Status{}, false},
		{"non-blocking only", []string{q(nb, "x"), ans("q-9", "y")}, Status{}, false},
		{"unanswered", []string{q(blk, "x")}, Status{Blocking: true, Resume: "agent:plan"}, false},
		{"one of two answered", []string{q(blk, "x"), q(q2, "y"), ans("q-1", "a")}, Status{Blocking: true, Resume: "agent:plan"}, false},
		{"all answered", []string{q(blk, "x"), q(q2, "y"), q(nb, "z"), ans("q-1", "a"), ans("q-2", "b")},
			Status{Blocking: true, Answered: true, Resume: "agent:plan"}, true},
		{"only unauthorized answer", []string{q(blk, "x"), "@stranger " + ans("q-1", "a")}, Status{Blocking: true, Resume: "agent:plan"}, false},
		{"malformed question block", []string{q(blk, "x"), ans("q-1", "a"), q(`id="q-3" audience="product"`, "z")},
			Status{Blocking: true, Answered: true, Malformed: true, Resume: "agent:plan"}, false},
		{"malformed answer block", []string{q(blk, "x"), ans("q-1", "a"), ans("bad", "b")},
			Status{Blocking: true, Answered: true, Resume: "agent:plan"}, true},
		{"re-asked question needs a new answer", []string{q(blk, "x"), ans("q-1", "a"), q(blk, "x")},
			Status{Blocking: true, Resume: "agent:plan"}, false},
		{"re-asked question answered again", []string{q(blk, "x"), ans("q-1", "a"), q(blk, "x"), ans("q-1", "b")},
			Status{Blocking: true, Answered: true, Resume: "agent:plan"}, true},
		{"answer before question", []string{ans("q-1", "a"), q(blk, "x")}, Status{Blocking: true, Resume: "agent:plan"}, false},
		{"unterminated answer hides question", []string{q(blk, "x"), "<!-- agent-answer id=\"q-1\" -->\nyes\n" + q(q2, "y")},
			Status{Malformed: true, Blocking: true, Resume: "agent:plan"}, false},
		{"answer wraps question", []string{q(blk, "x"), ans("q-1", "a\n"+q(q2, "y"))},
			Status{Malformed: true, Blocking: true, Resume: "agent:plan"}, false},
		{"stranger re-asks as non-blocking", []string{q(blk, "x"), "@stranger " + q(`id="q-1" audience="product" blocking="false"`, "x")},
			Status{Blocking: true, Resume: "agent:plan"}, false},
		{"stranger re-asks with another resume", []string{q(blk, "x"), ans("q-1", "a"), "@stranger " + q(`id="q-1" audience="product" blocking="true" resume="agent:build"`, "x")},
			Status{Blocking: true, Answered: true, Resume: "agent:plan"}, true},
		{"stranger malformed block", []string{q(blk, "x"), ans("q-1", "a"), "@stranger <!-- agent-question " + blk + " -->\n**Question:** x"},
			Status{Blocking: true, Answered: true, Resume: "agent:plan"}, true},
		{"stranger answer block wrapping a question", []string{q(blk, "x"), ans("q-1", "a"), "@stranger " + ans("q-1", q(q2, "y"))},
			Status{Blocking: true, Answered: true, Resume: "agent:plan"}, true},
		{"re-asked as non-blocking", []string{q(blk, "x"), q(`id="q-1" audience="product" blocking="false"`, "x")}, Status{}, false},
		{"latest blocking resume", []string{q(`id="q-1" audience="product" blocking="true" resume="agent:build"`, "x"), q(`id="q-1" audience="product" blocking="true" resume="agent:docs"`, "x"), ans("q-1", "a")},
			Status{Blocking: true, Answered: true, Resume: "agent:docs"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Check(Parse(comments(tt.bodies...), authorized))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
			if got.CanResume() != tt.resume {
				t.Errorf("CanResume = %t, want %t", got.CanResume(), tt.resume)
			}
		})
	}
}

func TestCheckDifferingResume(t *testing.T) {
	cs := comments(q(blk, "x"), q(`id="q-2" audience="product" blocking="true" resume="agent:build"`, "y"), ans("q-1", "a"), ans("q-2", "b"))
	got := Check(Parse(cs, authorized))
	if !got.Blocking || !got.Answered || got.Resume != "agent:build" || got.CanResume() {
		t.Errorf("status = %+v", got)
	}
	want := []Problem{{Kind: ResumeConflict, CommentURL: cs[1].URL,
		Message: "blocking questions name different resume labels: q-1=agent:plan, q-2=agent:build"}}
	if !reflect.DeepEqual(got.Problems, want) {
		t.Errorf("problems = %+v", got.Problems)
	}
}
