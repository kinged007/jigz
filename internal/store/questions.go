package store

import (
	"encoding/json"
	"strings"
)

// QuestionItem is one scanned issue or pull request, the question_items row.
type QuestionItem struct {
	Project      string
	Number       int
	IsPR         bool
	Title        string
	URL          string
	Labels       []string
	Assignees    []string
	IssueETag    string
	CommentsETag string
	ScannedAt    string
	Problems     []string
	// Body is the item's description, with its author, the author's
	// association and the creation time (RFC 3339).
	Body              string
	Author            string
	AuthorAssociation string
	CreatedAt         string
}

// IgnoredAnswer is one answer kept out of a question.
type IgnoredAnswer struct {
	Author     string
	CommentURL string
}

// Question is one question found on an item, the questions row. Project and
// Number come from the item the question was found on.
type Question struct {
	Project         string
	Number          int
	QID             string
	Audience        string
	Blocking        bool
	Resume          string
	Question        string
	Suggested       string
	AskedCommentURL string
	// AskedCommentID is the ID of the comment with the question, 0 when it
	// is in the item's description.
	AskedCommentID   int64
	AskedAt          string
	AskedCount       int
	Answer           string
	AnsweredBy       string
	AnswerCommentURL string
	AnsweredAt       string
	IgnoredAnswers   []IgnoredAnswer
}

// jsonList encodes a JSON list column. Empty input round-trips to nil.
func jsonList(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// readJSONList decodes a JSON list column, treating ” and 'null' as empty.
func readJSONList(raw string, v any) error {
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), v)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

const questionItemColumns = `project, number, is_pr, title, url, labels, assignees, issue_etag, comments_etag, scanned_at, problems, body, author, author_association, created_at`

func scanQuestionItem(row interface{ Scan(...any) error }) (QuestionItem, error) {
	var it QuestionItem
	var isPR int
	var labels, assignees, problems string
	err := row.Scan(&it.Project, &it.Number, &isPR, &it.Title, &it.URL,
		&labels, &assignees, &it.IssueETag, &it.CommentsETag, &it.ScannedAt, &problems,
		&it.Body, &it.Author, &it.AuthorAssociation, &it.CreatedAt)
	if err != nil {
		return QuestionItem{}, err
	}
	it.IsPR = isPR != 0
	for _, d := range []struct {
		raw string
		dst any
	}{{labels, &it.Labels}, {assignees, &it.Assignees}, {problems, &it.Problems}} {
		if err := readJSONList(d.raw, d.dst); err != nil {
			return QuestionItem{}, err
		}
	}
	return it, nil
}

// ReplaceQuestions replaces one item's row and its questions in a single
// transaction. Question Project and Number come from item.
func (s *Store) ReplaceQuestions(item QuestionItem, qs []Question) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO question_items (`+questionItemColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (project, number) DO UPDATE SET
		is_pr=excluded.is_pr, title=excluded.title, url=excluded.url, labels=excluded.labels,
		assignees=excluded.assignees,
		issue_etag=excluded.issue_etag, comments_etag=excluded.comments_etag,
		scanned_at=excluded.scanned_at, problems=excluded.problems,
		body=excluded.body, author=excluded.author, author_association=excluded.author_association,
		created_at=excluded.created_at`,
		item.Project, item.Number, boolInt(item.IsPR), item.Title, item.URL,
		jsonList(item.Labels), jsonList(item.Assignees),
		item.IssueETag, item.CommentsETag, item.ScannedAt, jsonList(item.Problems),
		item.Body, item.Author, item.AuthorAssociation, item.CreatedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM questions WHERE project = ? AND number = ?`, item.Project, item.Number); err != nil {
		return err
	}
	for _, q := range lastPerQID(qs) {
		if _, err := tx.Exec(`INSERT INTO questions (project, number, qid, audience, blocking, resume, question,
			suggested, asked_comment_url, asked_comment_id, asked_at, asked_count, answer, answered_by, answer_comment_url, answered_at, ignored_answers)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			item.Project, item.Number, q.QID, q.Audience, boolInt(q.Blocking), q.Resume, q.Question,
			q.Suggested, q.AskedCommentURL, q.AskedCommentID, q.AskedAt, q.AskedCount, q.Answer, q.AnsweredBy,
			q.AnswerCommentURL, q.AnsweredAt, jsonList(q.IgnoredAnswers)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// lastPerQID drops all but the last question of each qid, keeping the
// position of the first.
func lastPerQID(qs []Question) []Question {
	idx := map[string]int{}
	var out []Question
	for _, q := range qs {
		if i, ok := idx[q.QID]; ok {
			out[i] = q
			continue
		}
		idx[q.QID] = len(out)
		out = append(out, q)
	}
	return out
}

// QuestionItem returns one item with its problems.
func (s *Store) QuestionItem(project string, number int) (QuestionItem, error) {
	return scanQuestionItem(s.db.QueryRow(`SELECT `+questionItemColumns+` FROM question_items WHERE project = ? AND number = ?`, project, number))
}

// QuestionFilter selects questions. Empty fields match everything.
type QuestionFilter struct {
	// Audience is "product" or "project".
	Audience string
	// Blocking is "true" or "false".
	Blocking string
	Project  string
	// Number is the item number; 0 matches every item.
	Number int
	// State is "open" or "answered".
	State string
}

func (f QuestionFilter) where() (string, []any) {
	var where []string
	var args []any
	add := func(cond string, v ...any) {
		where = append(where, cond)
		args = append(args, v...)
	}
	if f.Audience != "" {
		add("audience = ?", f.Audience)
	}
	switch f.Blocking {
	case "true":
		add("blocking = 1")
	case "false":
		add("blocking = 0")
	}
	if f.Project != "" {
		add("project = ?", f.Project)
	}
	if f.Number != 0 {
		add("number = ?", f.Number)
	}
	switch f.State {
	case "open":
		add("answered_at = ''")
	case "answered":
		add("answered_at != ''")
	}
	if len(where) == 0 {
		return "", nil
	}
	return ` WHERE ` + strings.Join(where, " AND "), args
}

// Questions lists questions matching the filter.
func (s *Store) Questions(f QuestionFilter) ([]Question, error) {
	w, args := f.where()
	rows, err := s.db.Query(`SELECT project, number, qid, audience, blocking, resume, question, suggested,
		asked_comment_url, asked_comment_id, asked_at, asked_count, answer, answered_by, answer_comment_url, answered_at, ignored_answers
		FROM questions`+w+` ORDER BY project, number, qid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Question
	for rows.Next() {
		var q Question
		var blocking int
		var ignored string
		if err := rows.Scan(&q.Project, &q.Number, &q.QID, &q.Audience, &blocking, &q.Resume, &q.Question,
			&q.Suggested, &q.AskedCommentURL, &q.AskedCommentID, &q.AskedAt, &q.AskedCount, &q.Answer, &q.AnsweredBy,
			&q.AnswerCommentURL, &q.AnsweredAt, &ignored); err != nil {
			return nil, err
		}
		q.Blocking = blocking != 0
		if err := readJSONList(ignored, &q.IgnoredAnswers); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
