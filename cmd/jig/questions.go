package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/kinged007/jigz/internal/collector"
	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/store"
)

func itemKey(project string, n int) string { return project + "#" + strconv.Itoa(n) }

// questionRow is one question on the page. Template is the agent-answer
// comment a person posts on GitHub.
type questionRow struct {
	store.Question
	Title    string
	URL      string
	Template string
}

// questionItem is a scanned item with problems.
type questionItem struct {
	store.QuestionItem
	Problems  []collector.ItemProblem
	Malformed bool
}

// projectPass is the scanner's state for one project.
type projectPass struct {
	Project string
	Status  string
}

type questionsView struct {
	pageData
	On        bool
	Projects  []projectPass
	Filter    store.QuestionFilter
	Questions []questionRow
	Items     []questionItem
}

// questionFilter reads the page's filters. Unknown values are dropped, and
// the state defaults to open.
func questionFilter(q url.Values) store.QuestionFilter {
	f := store.QuestionFilter{Project: q.Get("project"), State: "open"}
	if v := q.Get("audience"); v == "product" || v == "project" {
		f.Audience = v
	}
	if v := q.Get("blocking"); v == "true" || v == "false" {
		f.Blocking = v
	}
	if q.Get("state") == "answered" {
		f.State = "answered"
	}
	return f
}

// projectQuestionsOn reports whether github.questions is on for a project.
// A project whose config cannot be read uses the app-level block.
func (a *app) projectQuestionsOn(project string) bool {
	for _, r := range a.projects() {
		if r.Repo.Repo != project {
			continue
		}
		if g, err := config.LoadGitHub(r.Dir); err == nil {
			return g.QuestionsOn()
		}
		break
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.github.QuestionsOn()
}

// questionsOn reports whether the scanner and the page are on: the store is
// open and at least one registered project has github.questions on.
func (a *app) questionsOn() bool {
	if a.questions == nil {
		return false
	}
	ps := a.projects()
	if len(ps) == 0 {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.github.QuestionsOn()
	}
	for _, r := range ps {
		if a.projectQuestionsOn(r.Repo.Repo) {
			return true
		}
	}
	return false
}

// answerTemplate is the comment that answers q.
func answerTemplate(q store.Question) string {
	return fmt.Sprintf("<!-- agent-answer id=%q -->\n%s\n<!-- /agent-answer -->", q.QID, q.Suggested)
}

// questionsData collects the questions and problem items for f.
func (a *app) questionsData(f store.QuestionFilter) ([]questionRow, []questionItem, error) {
	if !a.questionsOn() || a.store == nil {
		return nil, nil, nil
	}
	items, err := a.questions.Items()
	if err != nil {
		return nil, nil, err
	}
	byKey := map[string]store.QuestionItem{}
	var withProblems []questionItem
	for _, it := range items {
		byKey[itemKey(it.Project, it.Number)] = it
		ps := collector.Problems(it)
		if len(ps) == 0 || f.Project != "" && it.Project != f.Project {
			continue
		}
		withProblems = append(withProblems, questionItem{QuestionItem: it, Problems: ps, Malformed: collector.Malformed(ps)})
	}
	qs, err := a.store.Questions(f)
	if err != nil {
		return nil, nil, err
	}
	rows := make([]questionRow, 0, len(qs))
	for _, q := range qs {
		row := questionRow{Question: q, Template: answerTemplate(q)}
		if q.IgnoredAnswers == nil {
			row.IgnoredAnswers = []store.IgnoredAnswer{}
		}
		if it, ok := byKey[itemKey(q.Project, q.Number)]; ok {
			row.Title, row.URL = it.Title, it.URL
		} else if it, err := a.store.QuestionItem(q.Project, q.Number); err == nil {
			row.Title, row.URL = it.Title, it.URL
		}
		rows = append(rows, row)
	}
	return rows, withProblems, nil
}

// passStatuses describes the last scanner pass of each project that has
// github.questions on.
func (a *app) passStatuses() []projectPass {
	var out []projectPass
	interval := a.questionsSettings().QuestionPassInterval()
	for _, t := range a.questions.Targets() {
		if !a.projectQuestionsOn(t.Project) {
			continue
		}
		p := projectPass{Project: t.Project}
		st := a.questions.Status(t.Project)
		switch {
		case t.Token == "":
			p.Status = "no GitHub token"
		case st.At.IsZero():
			p.Status = "no pass yet"
		default:
			p.Status = fmt.Sprintf("last pass %s ago, %s; next pass due %s", since(st.At.Format(time.RFC3339)), st.Text(), st.At.Add(interval).Local().Format("15:04"))
		}
		out = append(out, p)
	}
	return out
}

func (a *app) questionsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := questionsView{pageData: a.dataFor(r, "", ""), On: a.questionsOn(), Filter: questionFilter(q)}
	v.Nav, v.Title = "questions", "Questions"
	if v.On {
		var err error
		if v.Questions, v.Items, err = a.questionsData(v.Filter); err != nil {
			a.fail(w, "questions", err)
			return
		}
		v.Projects = a.passStatuses()
	}
	execPage(w, "questions", v)
}
