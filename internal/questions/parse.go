// Package questions parses agent-question and agent-answer blocks
// (plans/system/hub-protocol.md section 4) from the comments of one
// issue or pull request, and applies the answer rule of
// plans/jigz/questions.md section 1 to the result. It does no I/O.
package questions

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Comment is one issue or pull request conversation comment, or the
// item's description when IsBody is set. A description is read for question
// blocks only; its answer blocks are ignored.
type Comment struct {
	ID                int64
	IsBody            bool
	Author            string
	AuthorAssociation string
	URL               string
	Created           time.Time
	Body              string
}

// Question is one question id on an item. When the id is asked more than
// once, the latest block defines every field except AskedCount.
type Question struct {
	ID         string
	Audience   string
	Blocking   bool
	Resume     string
	Text       string
	Suggested  string
	CommentURL string
	// CommentID is the ID of the comment with the latest block, 0 for the
	// item's description.
	CommentID  int64
	Asked      time.Time
	AskedCount int
	// Answers holds the authorized answers posted after the latest
	// block, oldest first.
	Answers []Answer
	// Stale holds the authorized answers posted before the latest block;
	// they do not answer the question.
	Stale []Answer
	// Ignored holds the answers from authors that are not authorized;
	// they do not answer the question.
	Ignored []Answer
	seq     int
}

// Answer is one agent-answer block.
type Answer struct {
	ID                string
	By                string
	Text              string
	Author            string
	AuthorAssociation string
	CommentURL        string
	Created           time.Time
	Authorized        bool
	seq               int
}

// Current returns the latest counted answer, or nil when the question
// has none.
func (q Question) Current() *Answer {
	if len(q.Answers) == 0 {
		return nil
	}
	return &q.Answers[len(q.Answers)-1]
}

// ProblemKind says what is wrong. MalformedQuestion, MalformedBlock and
// ResumeConflict stop the answer path from removing labels.
type ProblemKind string

const (
	MalformedQuestion ProblemKind = "malformed_question"
	MalformedAnswer   ProblemKind = "malformed_answer"
	// MalformedBlock is an unterminated block or a nested start, of
	// either kind.
	MalformedBlock ProblemKind = "malformed_block"
	UnknownAnswer  ProblemKind = "unknown_answer"
	// IgnoredQuestion is a question block, or any malformed block, from
	// an author that is not authorized.
	IgnoredQuestion ProblemKind = "ignored_question"
	ResumeConflict  ProblemKind = "resume_conflict"
)

// Problem is a block that could not be used, with the comment it is in.
// QuestionID is empty when the block has no readable id.
type Problem struct {
	Kind       ProblemKind
	QuestionID string
	CommentURL string
	Author     string
	Message    string
}

// Result is the parsed content of one item's comments.
type Result struct {
	// Questions are in order of first appearance.
	Questions []Question
	Problems  []Problem
}

var (
	markerRe = regexp.MustCompile(`(?ms)^ {0,3}<!--\s*(/?)((?i:agent-(?:question|answer)))((?:\s.*?)?)-->`)
	attrRe   = regexp.MustCompile(`^\s+([A-Za-z_-]+)="([^"]*)"`)
	idRe     = regexp.MustCompile(`^q-[A-Za-z0-9_-]{1,64}$`)
	resumeRe = regexp.MustCompile(`^agent:[A-Za-z0-9._/-]+$`)
	fenceRe  = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
)

var allowedAttrs = map[string][]string{
	"question": {"id", "audience", "blocking", "resume"},
	"answer":   {"id", "by"},
}

const (
	questionLabel  = "**Question:**"
	suggestedLabel = "**Suggested answer:**"
)

// Parse reads the blocks in comments, ordered by comment ID. authorized
// decides whether a comment's author may ask and answer questions; it
// must not be nil. The caller holds that rule and its context (token
// account, repository owner, assignees).
func Parse(comments []Comment, authorized func(Comment) bool) Result {
	comments = slices.Clone(comments)
	slices.SortStableFunc(comments, func(a, b Comment) int { return cmp.Compare(a.ID, b.ID) })
	var res Result
	index := map[string]int{}
	var answers []Answer
	seq := 0
	for _, c := range comments {
		bs := blocks(c.Body)
		if len(bs) == 0 {
			continue
		}
		ok := authorized(c)
		for _, b := range bs {
			seq++
			if b.kind == "answer" && c.IsBody {
				continue
			}
			if b.err != "" {
				res.Problems = append(res.Problems, b.problem(c, ok))
				continue
			}
			if b.kind == "answer" {
				a, err := answerOf(b, c)
				if err != "" {
					b.err = err
					res.Problems = append(res.Problems, b.problem(c, ok))
					continue
				}
				a.Authorized, a.seq = ok, seq
				answers = append(answers, a)
				continue
			}
			if !ok {
				res.Problems = append(res.Problems, Problem{
					Kind: IgnoredQuestion, QuestionID: b.attrs["id"], CommentURL: c.URL, Author: c.Author,
					Message: fmt.Sprintf("agent-question block from %s ignored: author not authorized", c.Author),
				})
				continue
			}
			q, err := questionOf(b, c)
			if err != "" {
				b.err = err
				res.Problems = append(res.Problems, b.problem(c, ok))
				continue
			}
			q.seq = seq
			if i, found := index[q.ID]; found {
				q.AskedCount = res.Questions[i].AskedCount + 1
				res.Questions[i] = q
				continue
			}
			q.AskedCount = 1
			index[q.ID] = len(res.Questions)
			res.Questions = append(res.Questions, q)
		}
	}
	for _, a := range answers {
		i, found := index[a.ID]
		if !found {
			res.Problems = append(res.Problems, Problem{
				Kind: UnknownAnswer, QuestionID: a.ID, CommentURL: a.CommentURL, Author: a.Author,
				Message: fmt.Sprintf("answer %s matches no question", a.ID),
			})
			continue
		}
		q := &res.Questions[i]
		switch {
		case !a.Authorized:
			q.Ignored = append(q.Ignored, a)
		case a.seq < q.seq:
			q.Stale = append(q.Stale, a)
		default:
			q.Answers = append(q.Answers, a)
		}
	}
	return res
}

type block struct {
	kind       string // "question" or "answer"
	attrs      map[string]string
	content    string
	err        string
	structural bool
}

func (b block) problem(c Comment, authorized bool) Problem {
	kind := MalformedAnswer
	switch {
	case !authorized:
		kind = IgnoredQuestion
	case b.structural:
		kind = MalformedBlock
	case b.kind == "question":
		kind = MalformedQuestion
	}
	id := b.attrs["id"]
	if !idRe.MatchString(id) {
		id = ""
	}
	msg := fmt.Sprintf("agent-%s block: %s", b.kind, b.err)
	if !authorized {
		msg = fmt.Sprintf("agent-%s block from %s ignored: author not authorized (%s)", b.kind, c.Author, b.err)
	}
	return Problem{Kind: kind, QuestionID: id, CommentURL: c.URL, Author: c.Author, Message: msg}
}

// blocks splits a comment body into blocks. A block never spans comments.
// A start inside an open block is reported as its own malformed block and
// makes the open block malformed.
func blocks(body string) []block {
	var out []block
	var open *block
	var openEnd int
	for _, m := range markerRe.FindAllStringSubmatchIndex(maskFences(body), -1) {
		end := body[m[2]:m[3]] == "/"
		name := body[m[4]:m[5]]
		kind := strings.TrimPrefix(strings.ToLower(name), "agent-")
		caseErr := ""
		if name != strings.ToLower(name) {
			caseErr = fmt.Sprintf("marker name %q must be lowercase", name)
		}
		switch {
		case !end && open == nil:
			attrs, err := parseAttrs(kind, body[m[6]:m[7]])
			if caseErr != "" {
				err = caseErr
			}
			open = &block{kind: kind, attrs: attrs, err: err}
			openEnd = m[1]
		case !end:
			attrs, _ := parseAttrs(kind, body[m[6]:m[7]])
			out = append(out, block{kind: kind, attrs: attrs, err: "start marker inside an open agent-" + open.kind + " block", structural: true})
			open.err, open.structural = "nested start marker", true
		case open != nil && kind == open.kind:
			if open.err == "" {
				open.err = caseErr
			}
			open.content = body[openEnd:m[0]]
			out = append(out, *open)
			open = nil
		}
	}
	if open != nil {
		open.err, open.structural = "start marker without end marker", true
		out = append(out, *open)
	}
	return out
}

func parseAttrs(kind, s string) (map[string]string, string) {
	attrs := map[string]string{}
	for strings.TrimSpace(s) != "" {
		m := attrRe.FindStringSubmatch(s)
		if m == nil {
			return attrs, fmt.Sprintf("cannot read attributes at %q", strings.TrimSpace(s))
		}
		if !slices.Contains(allowedAttrs[kind], m[1]) {
			return attrs, fmt.Sprintf("unknown attribute %s", m[1])
		}
		if _, dup := attrs[m[1]]; dup {
			return attrs, fmt.Sprintf("attribute %s given twice", m[1])
		}
		attrs[m[1]] = m[2]
		s = s[len(m[0]):]
	}
	return attrs, ""
}

func questionOf(b block, c Comment) (Question, string) {
	q := Question{ID: b.attrs["id"], Audience: b.attrs["audience"], Resume: b.attrs["resume"], CommentURL: c.URL, CommentID: c.ID, Asked: c.Created}
	if !idRe.MatchString(q.ID) {
		return q, fmt.Sprintf("bad id %q", q.ID)
	}
	if q.Audience != "product" && q.Audience != "project" {
		return q, fmt.Sprintf("audience must be product or project, not %q", q.Audience)
	}
	switch b.attrs["blocking"] {
	case "true":
		q.Blocking = true
	case "false":
	default:
		return q, fmt.Sprintf("blocking must be true or false, not %q", b.attrs["blocking"])
	}
	_, hasResume := b.attrs["resume"]
	if (q.Blocking || hasResume) && (!resumeRe.MatchString(q.Resume) || q.Resume == "agent:done") {
		return q, fmt.Sprintf("resume must be an agent: label other than agent:done, not %q", q.Resume)
	}
	text, ok := paragraph(b.content, questionLabel)
	if !ok || text == "" {
		return q, "no " + questionLabel + " paragraph"
	}
	q.Text = text
	q.Suggested, _ = paragraph(b.content, suggestedLabel)
	return q, ""
}

func answerOf(b block, c Comment) (Answer, string) {
	a := Answer{
		ID: b.attrs["id"], By: b.attrs["by"], Text: strings.TrimSpace(b.content),
		Author: c.Author, AuthorAssociation: c.AuthorAssociation, CommentURL: c.URL, Created: c.Created,
	}
	if !idRe.MatchString(a.ID) {
		return a, fmt.Sprintf("bad id %q", a.ID)
	}
	if a.Text == "" {
		return a, "empty answer"
	}
	return a, ""
}

// paragraph returns the text after label up to the next blank line or
// the next label.
func paragraph(content, label string) (string, bool) {
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, label) {
			continue
		}
		parts := []string{strings.TrimSpace(strings.TrimPrefix(t, label))}
		for _, next := range lines[i+1:] {
			nt := strings.TrimSpace(next)
			if nt == "" || strings.HasPrefix(nt, questionLabel) || strings.HasPrefix(nt, suggestedLabel) {
				break
			}
			parts = append(parts, nt)
		}
		return strings.TrimSpace(strings.Join(parts, "\n")), true
	}
	return "", false
}

// maskFences replaces every line of a fenced code block, fence lines
// included, with spaces, so markers there are not found and offsets
// still match body. An unclosed fence runs to the end of the body.
// A fence inside a blockquote or indented four spaces is not a fence
// here; markers on those lines are not at a line start and never count.
func maskFences(body string) string {
	b := []byte(body)
	fence := ""
	start := 0
	for start < len(b) {
		end := strings.IndexByte(body[start:], '\n')
		if end < 0 {
			end = len(b)
		} else {
			end += start
		}
		line := body[start:end]
		masked := fence != ""
		if fence == "" {
			if m := fenceRe.FindStringSubmatch(line); m != nil {
				fence, masked = m[1], true
			}
		} else if closesFence(line, fence) {
			fence = ""
		}
		if masked {
			for i := start; i < end; i++ {
				b[i] = ' '
			}
		}
		start = end + 1
	}
	return string(b)
}

func closesFence(line, fence string) bool {
	t := strings.TrimRight(line, " \t\r")
	trimmed := strings.TrimLeft(t, " ")
	if len(t)-len(trimmed) > 3 {
		return false
	}
	return len(trimmed) >= len(fence) && strings.Trim(trimmed, fence[:1]) == ""
}
