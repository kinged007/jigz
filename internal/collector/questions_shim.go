package collector

// The dashboard's answer, retry and remove-labels routes in
// cmd/jig/questions.go still call the declarations in this file. WP3 of
// plans/jigz/questions.md removes those routes; this file is deleted with
// them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/kinged007/jigz/internal/github"
	"github.com/kinged007/jigz/internal/questions"
)

// LabelsNotRemoved is no longer reported; see ResumeFailed and ParkedAgain.
const LabelsNotRemoved = "labels_not_removed"

// Question returns the question with id qid, or nil.
func (sc ItemScan) Question(qid string) *questions.Question {
	for i := range sc.Result.Questions {
		if sc.Result.Questions[i].ID == qid {
			return &sc.Result.Questions[i]
		}
	}
	return nil
}

// Login returns the token account's login, read once per project token.
func (s *Scanner) Login(ctx context.Context, t Target) (string, error) {
	if t.Token == "" {
		return "", errNoToken
	}
	return s.gh().User(ctx, t.Project, t.Token)
}

// ScanItem reads one item and its comments without ETags, stores the
// result, and returns it.
func (s *Scanner) ScanItem(ctx context.Context, t Target, n int) (ItemScan, error) {
	login, err := s.Login(ctx, t)
	if err != nil {
		return ItemScan{}, err
	}
	it, _, err := s.issue(ctx, t, n, false)
	if err != nil {
		return ItemScan{}, err
	}
	return s.scan(ctx, t, login, it, true)
}

// ErrAnswerText means an answer text was refused before anything was sent.
var ErrAnswerText = errors.New("answer text")

// AnswerBody is the comment that answers question qid as login.
func AnswerBody(qid, login, text string) (string, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return "", fmt.Errorf("%w: the answer is empty", ErrAnswerText)
	case strings.Contains(text, "<!--"):
		return "", fmt.Errorf("%w: the answer must not contain <!--", ErrAnswerText)
	}
	body := fmt.Sprintf("<!-- agent-answer id=\"%s\" by=\"%s\" -->\n%s\n<!-- /agent-answer -->", qid, login, text)
	res := questions.Parse([]questions.Comment{{Body: body}}, func(questions.Comment) bool { return true })
	if len(res.Problems) != 1 || res.Problems[0].Kind != questions.UnknownAnswer || res.Problems[0].QuestionID != qid {
		return "", fmt.Errorf("%w: the text would not form one answer block, for example because of an unclosed code fence", ErrAnswerText)
	}
	return body, nil
}

// Answer posts the answer to question qid on item n as the token account.
func (s *Scanner) Answer(ctx context.Context, t Target, n int, qid, text string) error {
	login, err := s.Login(ctx, t)
	if err != nil {
		return err
	}
	body, err := AnswerBody(qid, login, text)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}
	_, err = s.gh().Post(ctx, t.Project, t.Token, fmt.Sprintf("/repos/%s/issues/%d/comments", t.Project, n), payload)
	return err
}

// Resumed says what Resume did.
type Resumed struct {
	Scan    ItemScan
	Removed bool
	Added   string
}

// Resume rescans item n and, when rule 2 holds, relabels it.
func (s *Scanner) Resume(ctx context.Context, t Target, n int) (Resumed, error) {
	sc, err := s.ScanItem(ctx, t, n)
	if err != nil {
		return Resumed{}, err
	}
	if !sc.Status.CanResume() {
		return Resumed{Scan: sc}, nil
	}
	return s.shimRelabel(ctx, t, sc, sc.Status.Resume)
}

// ErrNotMalformed refuses remove-labels on an item without a malformed
// question block.
var ErrNotMalformed = errors.New("the item has no malformed question block")

// ErrNeedTrigger means remove-labels found no trigger to add.
var ErrNeedTrigger = errors.New("choose the trigger to add")

// RemoveTrigger is the resume label of the latest blocking question when
// the questions agree, else "".
func RemoveTrigger(sc ItemScan) string {
	if sc.Status.Resume != "" && len(sc.Status.Problems) == 0 {
		return sc.Status.Resume
	}
	return ""
}

// RemoveLabels relabels item n, which must have a malformed question block.
func (s *Scanner) RemoveLabels(ctx context.Context, t Target, n int, choice string) (Resumed, error) {
	sc, err := s.ScanItem(ctx, t, n)
	if err != nil {
		return Resumed{}, err
	}
	if !sc.Status.Malformed {
		return Resumed{Scan: sc}, ErrNotMalformed
	}
	trigger := RemoveTrigger(sc)
	if trigger == "" && !HasTrigger(sc.Item.Labels) {
		if !IsTrigger(choice) {
			return Resumed{Scan: sc}, ErrNeedTrigger
		}
		trigger = choice
	}
	return s.shimRelabel(ctx, t, sc, trigger)
}

func (s *Scanner) shimRelabel(ctx context.Context, t Target, sc ItemScan, trigger string) (Resumed, error) {
	n := sc.Item.Number
	out := Resumed{Scan: sc}
	err := func() error {
		if !HasTrigger(sc.Item.Labels) && trigger != "" {
			body, err := json.Marshal(map[string][]string{"labels": {trigger}})
			if err != nil {
				return err
			}
			if _, err := s.gh().Post(ctx, t.Project, t.Token, fmt.Sprintf("/repos/%s/issues/%d/labels", t.Project, n), body); err != nil {
				return fmt.Errorf("add %s: %w", trigger, err)
			}
			out.Added = trigger
		}
		for _, l := range []string{AgentDone, NeedsHuman} {
			name := strings.ReplaceAll(url.PathEscape(l), ":", "%3A")
			err := s.gh().Delete(ctx, t.Project, t.Token, fmt.Sprintf("/repos/%s/issues/%d/labels/%s", t.Project, n, name))
			if err != nil && !errors.Is(err, github.ErrNotFound) {
				return fmt.Errorf("remove %s: %w", l, err)
			}
		}
		return nil
	}()
	out.Removed = err == nil
	if after, serr := s.ScanItem(ctx, t, n); serr == nil {
		out.Scan = after
	}
	return out, err
}

// Triggers lists the project's trigger labels.
func (s *Scanner) Triggers(ctx context.Context, t Target) ([]string, error) {
	resp, err := s.gh().Get(ctx, t.Project, t.Token, fmt.Sprintf("/repos/%s/labels?per_page=100", t.Project), "")
	if err != nil {
		return nil, err
	}
	var ls []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(resp.Body, &ls); err != nil {
		return nil, fmt.Errorf("labels: %w", err)
	}
	var out []string
	for _, l := range ls {
		if IsTrigger(l.Name) {
			out = append(out, l.Name)
		}
	}
	return out, nil
}
