package questions

import (
	"testing"
	"time"
)

func TestParseDescription(t *testing.T) {
	desc := Comment{
		IsBody: true, Author: "owner", AuthorAssociation: "OWNER", URL: "https://github.com/o/r/issues/1",
		Created: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		Body:    sample + "\n\n<!-- agent-answer id=\"q-7f3a\" -->\nno\n<!-- /agent-answer -->",
	}
	cs := comments("<!-- agent-answer id=\"q-7f3a\" -->\nyes\n<!-- /agent-answer -->")
	cs[0].Created = desc.Created.Add(time.Minute)
	res := Parse(append(cs, desc), authorized)
	if len(res.Questions) != 1 || len(res.Problems) != 0 {
		t.Fatalf("result = %+v", res)
	}
	q := res.Questions[0]
	if q.CommentID != 0 || q.CommentURL != desc.URL {
		t.Errorf("question source = %d %s", q.CommentID, q.CommentURL)
	}
	if a := q.Current(); a == nil || a.Text != "yes" {
		t.Errorf("current answer = %+v, want the comment answer", a)
	}
}
