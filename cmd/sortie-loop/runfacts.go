package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kinged007/sortie-loop/internal/store"
)

// Observed sources of an event.
const (
	sourceHistory  = "from run history"
	sourceObserved = "observed by central"
)

// errorClass names why an attempt ended badly, read from the engine's
// error text. The zero value means there is no error to classify.
type errorClass struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// attemptClass is the store's outcome of an attempt, empty when it
// succeeded.
func attemptClass(status, text string) errorClass {
	if status == "succeeded" {
		return errorClass{}
	}
	o := store.ClassifyOutcome(status, text)
	return errorClass{o.Class, o.Text}
}

// handoffReason is the detail the engine gave for withholding a handoff:
// the text after "handoff withheld:" in the stored error, or after "Sortie
// did not hand the issue off:" in the sentence the API serves. It is empty
// when the error carries no detail.
func handoffReason(text string) string {
	for _, prefix := range []string{"handoff withheld:", "Sortie did not hand the issue off:"} {
		if detail, ok := strings.CutPrefix(text, prefix); ok {
			return strings.TrimSuffix(strings.TrimSpace(detail), ".")
		}
	}
	return ""
}

// attemptRow is one stored run of an issue, formatted for the table and
// the timeline.
type attemptRow struct {
	Run           store.Run
	Attempt       int
	Start, End    string
	Duration, Gap string
	Status        string
	Turns         int
	Tokens        string
	Unmeasured    bool
	Class         errorClass
	Error         string
	Href          string
	Current       bool
	ErrorExpected bool
	// Collapsed rows are hidden until the table is expanded.
	Collapsed      bool
	started, ended time.Time
}

// collapseOver is the attempt count above which the table shows only the
// first and last collapseKeep attempts.
const (
	collapseOver = 12
	collapseKeep = 3
)

// buildAttempts formats the runs of one issue, oldest first. current is
// the id of the run being viewed.
func buildAttempts(runs []store.Run, current int64) []attemptRow {
	out := make([]attemptRow, len(runs))
	for i, r := range runs {
		row := attemptRow{
			Run: r, Attempt: r.Attempt, Start: utc(r.StartedAt), End: utc(r.CompletedAt),
			Duration: duration(r.StartedAt, r.CompletedAt), Status: r.Status, Turns: r.TurnsCompleted,
			Tokens: tokenText(r), Unmeasured: !r.TokensMeasured, Class: attemptClass(r.Status, r.Error),
			Error: r.Error, Href: "/runs/" + r.Project + "/" + r.Workflow + "/" + strconv.FormatInt(r.ID, 10),
			Current: r.ID == current, ErrorExpected: r.Status != "succeeded",
		}
		row.started, _ = parseTS(r.StartedAt)
		row.ended, _ = parseTS(r.CompletedAt)
		if i > 0 && !row.started.IsZero() && !out[i-1].ended.IsZero() && !row.started.Before(out[i-1].ended) {
			row.Gap = spanText(row.started.Sub(out[i-1].ended))
		}
		row.Collapsed = len(runs) > collapseOver && i >= collapseKeep && i < len(runs)-collapseKeep && !row.Current
		out[i] = row
	}
	return out
}

// summarize states in one sentence what happened up to and including the
// attempt at index cur.
func summarize(rows []attemptRow, cur int) string {
	row := rows[cur]
	s := strings.ToUpper(row.Status[:1]) + row.Status[1:] + " on attempt " + strconv.Itoa(row.Attempt)
	if failed, label, n := commonFailure(rows[:cur]); failed > 0 {
		s += fmt.Sprintf(" after %d earlier failed %s (most often %s, %d %s)",
			failed, plural(failed, "attempt", "attempts"), label, n, plural(n, "time", "times"))
	}
	if row.Status != "succeeded" && row.Class.Label != "" {
		s += ": " + row.Class.Label
	}
	if first := rows[0].started; cur > 0 && !first.IsZero() && !row.ended.IsZero() && row.ended.After(first) {
		s += ", " + spanText(row.ended.Sub(first)) + " in total"
	}
	if row.Run.TokensMeasured && row.Run.TotalTokens > 0 {
		s += ", " + commas(row.Run.TotalTokens) + " tokens on this attempt"
	}
	return s + "."
}

// commonFailure counts the failed attempts and the class that most of
// them share.
func commonFailure(rows []attemptRow) (failed int, label string, n int) {
	counts := map[string]int{}
	for _, r := range rows {
		if r.Status == "succeeded" {
			continue
		}
		failed++
		counts[r.Class.Label]++
	}
	for l, c := range counts {
		if c > n || c == n && l < label {
			label, n = l, c
		}
	}
	return failed, label, n
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// eventRow is one line of the Events table.
type eventRow struct {
	TS, Event, Detail, Source string
}

// runEvents merges what the run history implies with the events central
// observed. The history gives at least the start and end of each attempt,
// so the list is never empty for a stored run.
func runEvents(rows []attemptRow, observed []store.Event) []eventRow {
	var out []eventRow
	for i, r := range rows {
		if i == 0 {
			out = append(out, eventRow{r.Run.StartedAt, "issue picked up", "attempt " + strconv.Itoa(r.Attempt) + " started", sourceHistory})
		}
		detail := []string{}
		if r.Duration != "" {
			detail = append(detail, "after "+r.Duration)
		}
		if r.Class.Label != "" {
			detail = append(detail, r.Class.Label)
		}
		if i+1 < len(rows) && rows[i+1].Gap != "" {
			detail = append(detail, "next attempt starts after "+rows[i+1].Gap)
		}
		out = append(out, eventRow{r.Run.CompletedAt, "attempt " + strconv.Itoa(r.Attempt) + " ended: " + r.Status, strings.Join(detail, ", "), sourceHistory})
	}
	for _, e := range observed {
		out = append(out, eventRow{e.TS, e.Kind, e.Detail, sourceObserved})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}
