package questions

import (
	"fmt"
	"strings"
)

// Status is rule 2 of plans/jigz/questions.md section 1 applied to one
// item.
type Status struct {
	// Blocking is true when the item has at least one blocking question.
	Blocking bool
	// Answered is true when every blocking question has an authorized
	// answer.
	Answered bool
	// Malformed is true when the item has a malformed question block or
	// a structural error in any block from an authorized author.
	Malformed bool
	// Resume is the resume label of the latest blocking question.
	Resume string
	// Problems holds a ResumeConflict when blocking questions name
	// different resume labels.
	Problems []Problem
}

// CanResume reports whether the answer path may remove needs-human and
// agent:done and add Resume.
func (s Status) CanResume() bool {
	return s.Blocking && s.Answered && !s.Malformed && len(s.Problems) == 0
}

// Check applies rule 2 to a parsed item.
func Check(r Result) Status {
	s := Status{Answered: true}
	for _, p := range r.Problems {
		if p.Kind == MalformedQuestion || p.Kind == MalformedBlock {
			s.Malformed = true
		}
	}
	var latest *Question
	resumes := map[string]bool{}
	for i := range r.Questions {
		q := &r.Questions[i]
		if !q.Blocking {
			continue
		}
		s.Blocking = true
		if q.Current() == nil {
			s.Answered = false
		}
		resumes[q.Resume] = true
		if latest == nil || q.seq > latest.seq {
			latest = q
		}
	}
	if latest == nil {
		s.Answered = false
		return s
	}
	s.Resume = latest.Resume
	if len(resumes) > 1 {
		var names []string
		for _, q := range r.Questions {
			if q.Blocking {
				names = append(names, q.ID+"="+q.Resume)
			}
		}
		s.Problems = append(s.Problems, Problem{
			Kind: ResumeConflict, CommentURL: latest.CommentURL,
			Message: fmt.Sprintf("blocking questions name different resume labels: %s", strings.Join(names, ", ")),
		})
	}
	return s
}
